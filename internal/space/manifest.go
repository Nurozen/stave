package space

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"gopkg.in/yaml.v3"
)

const (
	ManifestName = ".stave.yaml"
	AgentsName   = "AGENTS.md"

	// CurrentManifestVersion is the highest .stave.yaml schema version this
	// binary understands. Load is permissive (older/missing versions load);
	// Save refuses when the on-disk version is newer than this constant.
	CurrentManifestVersion = 1
)

type RepoMode string

const (
	ModeEdit      RepoMode = "edit"
	ModeReference RepoMode = "reference"
)

// MemoryManifest is one durable attachment record in .stave.yaml.
// owned:true means stave created the store and it follows space fate flags;
// owned:false means an existing store was attached and is never destroyed by stave.
type MemoryManifest struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	ID       string `yaml:"id"`
	Owned    bool   `yaml:"owned"`
}

type Manifest struct {
	// Version is a write-ceiling schema marker. 0 (or omitted) means pre-version.
	Version   int              `yaml:"version,omitempty"`
	ID        string           `yaml:"id"`
	Kind      string           `yaml:"kind,omitempty"`
	CreatedAt time.Time        `yaml:"createdAt"`
	SpecPath  string           `yaml:"specPath,omitempty"`
	Repos     []RepoManifest   `yaml:"repos"`
	Memories  []MemoryManifest `yaml:"memories,omitempty"`
}

type RepoManifest struct {
	Name         string   `yaml:"name"`
	Mode         RepoMode `yaml:"mode"`
	Path         string   `yaml:"path"`
	Base         string   `yaml:"base,omitempty"`
	Ref          string   `yaml:"ref,omitempty"`
	Branch       string   `yaml:"branch,omitempty"`
	BareRepoPath string   `yaml:"bareRepoPath"`
}

// ErrManifestVersionTooNew is returned by SaveManifest when the on-disk
// version is newer than CurrentManifestVersion (read-permissive / write-refusing).
type ErrManifestVersionTooNew struct {
	OnDisk  int
	Current int
}

func (e *ErrManifestVersionTooNew) Error() string {
	return fmt.Sprintf("space manifest version %d is newer than this stave understands (%d); upgrade stave before saving", e.OnDisk, e.Current)
}

func LoadManifest(spacePath string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// SaveManifest writes the manifest atomically. It refuses to overwrite an
// on-disk file whose version is greater than CurrentManifestVersion, so a
// newer binary's schema is never clobbered by an older one. New manifests
// (no on-disk file) always write at CurrentManifestVersion.
func SaveManifest(spacePath string, manifest Manifest) error {
	path := filepath.Join(spacePath, ManifestName)
	if existing, err := os.ReadFile(path); err == nil {
		var onDisk Manifest
		if err := yaml.Unmarshal(existing, &onDisk); err != nil {
			return fmt.Errorf("read existing manifest for version check: %w", err)
		}
		if onDisk.Version > CurrentManifestVersion {
			return &ErrManifestVersionTooNew{OnDisk: onDisk.Version, Current: CurrentManifestVersion}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.Version == 0 {
		manifest.Version = CurrentManifestVersion
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return fsio.WriteFileAtomic(path, data, 0o644)
}

// Validate checks id/name charset for memory attachments (and space id when set).
func (m Manifest) Validate() error {
	if m.ID != "" {
		if err := config.ValidateName("space id", m.ID); err != nil {
			return err
		}
	}
	for i, mem := range m.Memories {
		if mem.Name != "" {
			if err := config.ValidateName(fmt.Sprintf("memories[%d].name", i), mem.Name); err != nil {
				return err
			}
		}
		if mem.ID != "" {
			if err := config.ValidateName(fmt.Sprintf("memories[%d].id", i), mem.ID); err != nil {
				return err
			}
		}
		if mem.Provider != "" {
			if err := config.ValidateName(fmt.Sprintf("memories[%d].provider", i), mem.Provider); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m Manifest) FindRepo(name string) (RepoManifest, int, bool) {
	for i, repo := range m.Repos {
		if repo.Name == name {
			return repo, i, true
		}
	}
	return RepoManifest{}, -1, false
}

// FindMemory returns the attachment matching name (alias), or the sole
// attachment when name is empty and only one is present.
func (m Manifest) FindMemory(name string) (MemoryManifest, int, bool) {
	if name == "" {
		if len(m.Memories) == 1 {
			return m.Memories[0], 0, true
		}
		return MemoryManifest{}, -1, false
	}
	for i, mem := range m.Memories {
		if mem.Name == name || mem.ID == name {
			return mem, i, true
		}
	}
	return MemoryManifest{}, -1, false
}

func (m Manifest) HasPath(path string) bool {
	for _, repo := range m.Repos {
		if repo.Path == path {
			return true
		}
	}
	return false
}
