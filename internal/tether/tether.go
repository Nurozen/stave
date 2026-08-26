// Package tether records passive, machine-local learning of which repos are
// used together. It persists a sidecar file (repo-tethers.yaml) under an
// advisory lock with atomic, torn-read-safe writes, mirroring the manifest
// version-ratchet and fsio primitives.
//
// The package is deliberately config-light: it imports config only for the
// file location and shared permission constants, and it must never import
// internal/space (space owns the capture callers, so that direction would
// cycle). Mode/Strength are local string types whose values match
// space.RepoMode ("edit"/"reference") and are cast at the space boundary.
package tether

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"gopkg.in/yaml.v3"
)

// FileName is the sidecar's basename, joined onto config.Root.
const FileName = "repo-tethers.yaml"

// CurrentTethersVersion is the highest repo-tethers.yaml schema version this
// binary understands. Load is permissive (older/missing versions load); Save
// refuses when the on-disk version is newer than this constant.
const CurrentTethersVersion = 1

// Mode records how the associated ("to") repo co-occurred with the editable
// ("from") repo. It mirrors space.RepoMode's values without importing space.
type Mode string

const (
	ModeEdit      Mode = "edit"
	ModeReference Mode = "reference"
)

// Strength is a tether's effective or pinned classification. Effective
// strength is derived from Count vs the configured threshold at read time;
// this package never persists a derived strength (see the D6 note on Save).
type Strength string

const (
	Strong Strength = "strong"
	Weak   Strength = "weak"
)

// Tether is one directed co-occurrence edge: editable From used alongside
// associate To (materialized as ToMode) Count times, last seen at LastSeen.
// Identity is (From, To); ToMode is a stored attribute with edit-over-reference
// precedence. Pinned, when set, overrides derived strength.
type Tether struct {
	From     string    `yaml:"from" json:"from"`
	To       string    `yaml:"to" json:"to"`
	ToMode   Mode      `yaml:"toMode" json:"toMode"`
	Count    int       `yaml:"count" json:"count"`
	Strength Strength  `yaml:"strength,omitempty" json:"strength,omitempty"`
	Pinned   Strength  `yaml:"pinned,omitempty" json:"pinned,omitempty"`
	LastSeen time.Time `yaml:"lastSeen" json:"lastSeen"`
}

// File is the on-disk document.
type File struct {
	Version int      `yaml:"version" json:"version"`
	Tethers []Tether `yaml:"tethers" json:"tethers"`
}

// ErrTethersVersionTooNew is returned by Save when the on-disk version is newer
// than CurrentTethersVersion (read-permissive / write-refusing), mirroring
// space.ErrManifestVersionTooNew.
type ErrTethersVersionTooNew struct {
	OnDisk  int
	Current int
}

func (e *ErrTethersVersionTooNew) Error() string {
	return fmt.Sprintf("repo tethers file version %d is newer than this stave understands (%d); upgrade stave before saving", e.OnDisk, e.Current)
}

// Path returns the sidecar location for a config (~/stave/repo-tethers.yaml by
// default). config never imports tether, so this one-way dependency is safe.
func Path(cfg config.Config) string {
	return filepath.Join(cfg.Root, FileName)
}

func lockPath(path string) string { return path + ".lock" }

// Load reads the sidecar. An absent file is not an error: it yields a fresh
// empty File stamped at CurrentTethersVersion (unlike space.LoadManifest, which
// errors on absence). Existing files are returned as-is (version untouched) so
// the ratchet, not Load, decides whether a newer file may be overwritten.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{Version: CurrentTethersVersion}, nil
		}
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Save writes f atomically at 0o600, creating the parent directory first. It
// refuses to overwrite an on-disk file whose version is newer than
// CurrentTethersVersion and never steps the stamped version down. Save is
// deliberately dumb about strength: it cannot know the configured threshold, so
// deriving effective strength is left to the threshold-aware helpers and the
// read path (D6).
func Save(path string, f *File) error {
	return saveWithCeiling(path, f, CurrentTethersVersion)
}

// saveWithCeiling is Save with the write ceiling injected so tests can drive an
// older binary's ceiling against a newer file.
func saveWithCeiling(path string, f *File, ceiling int) error {
	onDiskVersion := 0
	if existing, err := os.ReadFile(path); err == nil {
		var onDisk File
		if err := yaml.Unmarshal(existing, &onDisk); err != nil {
			return fmt.Errorf("read existing repo tethers for version check: %w", err)
		}
		if onDisk.Version > ceiling {
			return &ErrTethersVersionTooNew{OnDisk: onDisk.Version, Current: ceiling}
		}
		onDiskVersion = onDisk.Version
	} else if !os.IsNotExist(err) {
		return err
	}
	// Never step the file back down: ratchet up to at least the current schema
	// version and whatever the file already carried.
	for _, v := range []int{CurrentTethersVersion, onDiskVersion} {
		if v > f.Version {
			f.Version = v
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), config.DefaultDirMode); err != nil {
		return err
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return fsio.WriteFileAtomic(path, data, config.DefaultConfigMode)
}

// Update runs fn against the loaded File under an exclusive advisory lock,
// serializing load-modify-save cycles across concurrent stave processes, then
// persists the result. The parent directory is created up front because
// fsio.WithLock opens the lock file before fn runs (a later MkdirAll in Save
// would be too late for the lock open).
//
// On Windows the lock is a no-op, so concurrent captures can lose an increment;
// counts are advisory and this is accepted for v1. The atomic rename still
// prevents torn reads there.
func Update(path string, fn func(*File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), config.DefaultDirMode); err != nil {
		return err
	}
	return fsio.WithLock(lockPath(path), func() error {
		f, err := Load(path)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
		return Save(path, f)
	})
}

// EffectiveStrength derives a tether's classification: a pin wins outright,
// otherwise it is strong once Count reaches the threshold. This is the single
// place strength is derived; both the capture path and the read path call it.
func EffectiveStrength(t Tether, threshold int) Strength {
	if t.Pinned == Strong || t.Pinned == Weak {
		return t.Pinned
	}
	if t.Count >= threshold {
		return Strong
	}
	return Weak
}

// find returns the index of the (from,to) tether, or -1. Identity is the pair
// only; ToMode is not part of identity (D14).
func find(f *File, from, to string) int {
	for i := range f.Tethers {
		if f.Tethers[i].From == from && f.Tethers[i].To == to {
			return i
		}
	}
	return -1
}

// Bump records one co-occurrence of (from → to). It finds-or-creates by
// (from,to), increments Count, and stamps LastSeen. ToMode follows
// edit-over-reference precedence: an existing edit is kept, and a new edit
// upgrades an existing reference. from==to is a no-op (a repo never tethers to
// itself). Bump does not derive Strength (Save is dumb about it).
func Bump(f *File, from, to string, mode Mode, now time.Time) {
	if from == "" || to == "" || from == to {
		return
	}
	if i := find(f, from, to); i >= 0 {
		f.Tethers[i].Count++
		f.Tethers[i].LastSeen = now
		if mode == ModeEdit {
			f.Tethers[i].ToMode = ModeEdit
		}
		return
	}
	f.Tethers = append(f.Tethers, Tether{
		From:     from,
		To:       to,
		ToMode:   mode,
		Count:    1,
		LastSeen: now,
	})
}

// Find returns every tether whose From matches, in stored order.
func Find(f *File, from string) []Tether {
	var out []Tether
	for _, t := range f.Tethers {
		if t.From == from {
			out = append(out, t)
		}
	}
	return out
}

// Common returns from's tethers ordered strong-first (then by Count desc, then
// To for determinism). Weak tethers are included only when includeWeak is set.
// Effective strength is derived per tether via the threshold.
func Common(f *File, from string, includeWeak bool, threshold int) []Tether {
	var out []Tether
	for _, t := range f.Tethers {
		if t.From != from {
			continue
		}
		s := EffectiveStrength(t, threshold)
		if s == Weak && !includeWeak {
			continue
		}
		t.Strength = s
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].Strength == Strong, out[j].Strength == Strong
		if si != sj {
			return si // strong before weak
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].To < out[j].To
	})
	return out
}

// Pin sets a manual strength override on (from,to), creating the tether (as a
// reference, Count 0) when it does not yet exist. from==to is a no-op (a repo
// never tethers to itself), mirroring Bump.
func Pin(f *File, from, to string, s Strength) {
	if from == "" || to == "" || from == to {
		return
	}
	if i := find(f, from, to); i >= 0 {
		f.Tethers[i].Pinned = s
		return
	}
	f.Tethers = append(f.Tethers, Tether{
		From:   from,
		To:     to,
		ToMode: ModeReference,
		Pinned: s,
	})
}

// Remove drops the (from,to) tether if present.
func Remove(f *File, from, to string) {
	if i := find(f, from, to); i >= 0 {
		f.Tethers = append(f.Tethers[:i], f.Tethers[i+1:]...)
	}
}

// RemoveAll drops every tether originating from `from`.
func RemoveAll(f *File, from string) {
	kept := f.Tethers[:0]
	for _, t := range f.Tethers {
		if t.From != from {
			kept = append(kept, t)
		}
	}
	f.Tethers = kept
}
