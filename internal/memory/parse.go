package memory

import (
	"fmt"
	"strings"

	"github.com/Nurozen/stave/internal/config"
)

// MemorySpec is a parsed `--memory [provider:]<spec>` value.
// Spec "." means a fresh task store on the given (or default) provider.
// Spec with an id attaches existing when it looks like an id (not ".").
type MemorySpec struct {
	Provider string
	// Spec is "." for fresh task store, or a store id for attach-existing sugar.
	Spec string
	// Fresh is true when Spec is "." or empty after provider strip for create sugar.
	Fresh bool
}

// ParseMemorySpec parses `[provider:]<spec>`. Bare "." → fresh default provider.
// `marmot:.` or `marmot` → fresh marmot. `marmot:existing-id` → use existing.
func ParseMemorySpec(raw, defaultProvider string) (MemorySpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return MemorySpec{}, fmt.Errorf("memory spec is empty")
	}
	if defaultProvider == "" {
		defaultProvider = "marmot"
	}
	// Bare "." = fresh task store on default provider.
	if raw == "." {
		return MemorySpec{Provider: defaultProvider, Spec: ".", Fresh: true}, nil
	}
	provider, spec, found := strings.Cut(raw, ":")
	if !found {
		// Bare token: either a provider name alone (fresh) or treated as id on default provider.
		// Convention: if it equals a known provider name → fresh on that provider;
		// otherwise treat as store id on the default provider (attach-existing sugar).
		if isKnownProvider(raw) {
			return MemorySpec{Provider: raw, Spec: ".", Fresh: true}, nil
		}
		if err := config.ValidateName("memory id", raw); err != nil {
			return MemorySpec{}, err
		}
		return MemorySpec{Provider: defaultProvider, Spec: raw, Fresh: false}, nil
	}
	if provider == "" {
		return MemorySpec{}, fmt.Errorf("memory spec %q has empty provider", raw)
	}
	if err := config.ValidateName("memory provider", provider); err != nil {
		return MemorySpec{}, err
	}
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "." {
		return MemorySpec{Provider: provider, Spec: ".", Fresh: true}, nil
	}
	if err := config.ValidateName("memory id", spec); err != nil {
		return MemorySpec{}, err
	}
	return MemorySpec{Provider: provider, Spec: spec, Fresh: false}, nil
}

func isKnownProvider(name string) bool {
	for _, n := range Names() {
		if n == name {
			return true
		}
	}
	return false
}
