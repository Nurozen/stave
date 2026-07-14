package memory

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Nurozen/stave/internal/config"
)

var (
	registryMu sync.RWMutex
	// factories map provider name → constructor from config.
	factories = map[string]func(config.MemoryConfig) Provider{
		"marmot": func(mc config.MemoryConfig) Provider {
			return NewMarmot(mc.Binary)
		},
	}
)

// Register adds or replaces a provider factory (tests / future providers).
func Register(name string, factory func(config.MemoryConfig) Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	factories[name] = factory
}

// Lookup returns a Provider for the given name using memory config.
func Lookup(name string, mc config.MemoryConfig) (Provider, error) {
	if name == "" {
		name = mc.Provider
	}
	if name == "" {
		name = "marmot"
	}
	registryMu.RLock()
	factory, ok := factories[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown memory provider %q", name)
	}
	return factory(mc), nil
}

// FromConfig returns the configured default provider.
func FromConfig(cfg config.Config) (Provider, error) {
	mc := cfg.Memory
	mc.ApplyDefaults()
	return Lookup(mc.Provider, mc)
}

// Names returns registered provider names sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
