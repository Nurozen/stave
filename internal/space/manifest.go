package space

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"gopkg.in/yaml.v3"
)

const (
	ManifestName = ".stave.yaml"
	AgentsName   = "AGENTS.md"
	ClaudeName   = "CLAUDE.md"

	// CurrentManifestVersion is the highest .stave.yaml schema version this
	// binary understands. Load is permissive (older/missing versions load);
	// Save refuses when the on-disk version is newer than this constant.
	// The version actually stamped on a file is content-dependent (see
	// Manifest.schemaVersion), so a saga-less space still writes version 1.
	CurrentManifestVersion = 2
)

type RepoMode string

const (
	ModeEdit      RepoMode = "edit"
	ModeReference RepoMode = "reference"

	// KindSaga marks a space that coordinates member spaces rather than
	// holding work of its own.
	KindSaga = "saga"
)

// MemoryManifest is one durable attachment record in .stave.yaml.
// owned:true means stave created the store and it follows space fate flags;
// owned:false means an existing store was attached and is never destroyed by stave.
type MemoryManifest struct {
	Name     string `yaml:"name" json:"name"`
	Provider string `yaml:"provider" json:"provider"`
	ID       string `yaml:"id" json:"id"`
	Owned    bool   `yaml:"owned" json:"owned"`
}

type Manifest struct {
	// Version is a write-ceiling schema marker. 0 (or omitted) means pre-version.
	Version   int              `yaml:"version,omitempty" json:"version,omitempty"`
	ID        string           `yaml:"id" json:"id"`
	Kind      string           `yaml:"kind,omitempty" json:"kind,omitempty"`
	CreatedAt time.Time        `yaml:"createdAt" json:"createdAt"`
	SpecPath  string           `yaml:"specPath,omitempty" json:"specPath,omitempty"`
	Repos     []RepoManifest   `yaml:"repos" json:"repos"`
	Memories  []MemoryManifest `yaml:"memories,omitempty" json:"memories,omitempty"`
	// Saga is set only on saga spaces; nil on every ordinary space.
	Saga *SagaManifest `yaml:"saga,omitempty" json:"saga,omitempty"`
}

// SagaManifest is the member roster of a saga space.
type SagaManifest struct {
	Members []SagaMember `yaml:"members" json:"members"`
}

// SagaMember is one member space enrolled in a saga. After lists the ids of
// members this one lands behind; the resulting graph must stay acyclic.
type SagaMember struct {
	ID    string   `yaml:"id" json:"id"`
	After []string `yaml:"after,omitempty" json:"after,omitempty"`
	// CreatedAt is the member manifest's creation stamp captured when the
	// member was added. It is compared with time.Time.Equal to detect a
	// space that was destroyed and recreated under the same id, so a zero
	// value reads as a mismatch.
	CreatedAt time.Time `yaml:"createdAt,omitempty" json:"createdAt,omitzero"`
	PRs       []SagaPR  `yaml:"prs,omitempty" json:"prs,omitempty"`
}

// SagaPR identifies a pull request opened for a member. Only identity is
// recorded: merge state is always re-read from the forge, never persisted.
type SagaPR struct {
	Repo   string `yaml:"repo" json:"repo"`
	Number int    `yaml:"number" json:"number"`
}

type RepoManifest struct {
	Name         string   `yaml:"name" json:"name"`
	Mode         RepoMode `yaml:"mode" json:"mode"`
	Path         string   `yaml:"path" json:"path"`
	Base         string   `yaml:"base,omitempty" json:"base,omitempty"`
	Ref          string   `yaml:"ref,omitempty" json:"ref,omitempty"`
	Branch       string   `yaml:"branch,omitempty" json:"branch,omitempty"`
	BareRepoPath string   `yaml:"bareRepoPath" json:"bareRepoPath"`
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
// newer binary's schema is never clobbered by an older one. The stamped
// version is the lowest one that can express the manifest's own content, so
// spaces that use no post-v1 field stay readable and writable by older stave
// binaries.
func SaveManifest(spacePath string, manifest Manifest) error {
	return saveManifestWithCeiling(filepath.Join(spacePath, ManifestName), manifest, CurrentManifestVersion)
}

// saveManifestWithCeiling is SaveManifest with the write ceiling injected, so
// tests can drive an older binary's ceiling against a newer file.
func saveManifestWithCeiling(path string, manifest Manifest, ceiling int) error {
	onDiskVersion := 0
	if existing, err := os.ReadFile(path); err == nil {
		var onDisk Manifest
		if err := yaml.Unmarshal(existing, &onDisk); err != nil {
			return fmt.Errorf("read existing manifest for version check: %w", err)
		}
		if onDisk.Version > ceiling {
			return &ErrManifestVersionTooNew{OnDisk: onDisk.Version, Current: ceiling}
		}
		onDiskVersion = onDisk.Version
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	// Never step a file back down: a version already reached on disk (or
	// already carried by the manifest) stays, even if the content that
	// required it has since been removed.
	for _, v := range []int{manifest.schemaVersion(), onDiskVersion} {
		if v > manifest.Version {
			manifest.Version = v
		}
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return fsio.WriteFileAtomic(path, data, 0o644)
}

// saveManifestExclusive writes a brand-new manifest, failing with os.ErrExist
// when one is already on disk. InitSpace uses it for the initial write so
// concurrent same-ID creations (plain create vs saga create) get exactly one
// winner instead of silently clobbering each other's manifests.
func saveManifestExclusive(spacePath string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if v := manifest.schemaVersion(); v > manifest.Version {
		manifest.Version = v
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return fsio.WriteFileExclusive(filepath.Join(spacePath, ManifestName), data, 0o644)
}

// schemaVersion is the lowest schema version able to represent this
// manifest's content.
func (m Manifest) schemaVersion() int {
	if m.Saga != nil {
		return 2
	}
	return 1
}

// Validate checks id/name charset for memory attachments (and space id when
// set), plus the saga roster's ids and after-graph when one is present.
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
	if m.Saga != nil {
		if err := validateSagaMembers(m.Saga.Members); err != nil {
			return err
		}
	}
	return nil
}

func validateSagaMembers(members []SagaMember) error {
	byID := make(map[string]struct{}, len(members))
	for i, member := range members {
		if err := config.ValidateSpaceID(member.ID); err != nil {
			return fmt.Errorf("saga.members[%d]: %w", i, err)
		}
		if _, dup := byID[member.ID]; dup {
			return fmt.Errorf("saga.members[%d]: duplicate member %q", i, member.ID)
		}
		byID[member.ID] = struct{}{}
	}
	for i, member := range members {
		for j, after := range member.After {
			if _, ok := byID[after]; !ok {
				return fmt.Errorf("saga.members[%d].after[%d]: %q is not a saga member", i, j, after)
			}
		}
	}
	if cycle := sagaCycle(members); len(cycle) > 0 {
		return fmt.Errorf("saga.members: after cycle among %s", strings.Join(cycle, ", "))
	}
	return nil
}

// sagaCycle returns the member ids left unsettled by a Kahn toposort of the
// after-graph, in roster order; empty means the graph is acyclic. Callers must
// have already rejected duplicate ids and unknown after targets.
func sagaCycle(members []SagaMember) []string {
	pending := make(map[string]int, len(members))
	unblocks := make(map[string][]string, len(members))
	for _, member := range members {
		pending[member.ID] = len(member.After)
		for _, after := range member.After {
			unblocks[after] = append(unblocks[after], member.ID)
		}
	}
	ready := make([]string, 0, len(members))
	for _, member := range members {
		if pending[member.ID] == 0 {
			ready = append(ready, member.ID)
		}
	}
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		for _, blocked := range unblocks[id] {
			pending[blocked]--
			if pending[blocked] == 0 {
				ready = append(ready, blocked)
			}
		}
	}
	var stuck []string
	for _, member := range members {
		if pending[member.ID] > 0 {
			stuck = append(stuck, member.ID)
		}
	}
	return stuck
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
