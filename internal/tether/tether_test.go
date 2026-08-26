package tether

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
)

func TestPathJoinsRoot(t *testing.T) {
	cfg := config.Config{Root: "/home/u/stave"}
	if got, want := Path(cfg), filepath.Join("/home/u/stave", FileName); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestLoadAbsentReturnsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", FileName)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load absent: %v", err)
	}
	if f.Version != CurrentTethersVersion {
		t.Fatalf("Version = %d, want %d", f.Version, CurrentTethersVersion)
	}
	if len(f.Tethers) != 0 {
		t.Fatalf("expected no tethers, got %#v", f.Tethers)
	}
}

func TestLoadPropagatesReadError(t *testing.T) {
	// A directory at the path makes ReadFile fail with a non-IsNotExist error.
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error reading a directory as a file")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("::: not yaml :::\n\t- broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unmarshal error for malformed yaml")
	}
}

func TestSaveLoadRoundTripDualTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	f := &File{
		Version: CurrentTethersVersion,
		Tethers: []Tether{
			{From: "api", To: "web", ToMode: ModeReference, Count: 4, Strength: Strong, LastSeen: now},
			{From: "api", To: "db", ToMode: ModeEdit, Count: 1, Pinned: Weak, LastSeen: now},
		},
	}
	if err := Save(path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Perm must be private.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != config.DefaultConfigMode {
		t.Fatalf("perm = %v, want %v", info.Mode().Perm(), os.FileMode(config.DefaultConfigMode))
	}

	// YAML field names come from the yaml tags.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version:", "from:", "to:", "tomode:", "count:", "strength:", "pinned:", "lastseen:"} {
		if !strings.Contains(strings.ToLower(string(raw)), key) {
			t.Fatalf("yaml missing %q:\n%s", key, raw)
		}
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tethers) != 2 {
		t.Fatalf("tethers = %#v", loaded.Tethers)
	}
	got := loaded.Tethers[0]
	if got.From != "api" || got.To != "web" || got.ToMode != ModeReference || got.Count != 4 || !got.LastSeen.Equal(now) {
		t.Fatalf("round-trip drifted: %#v", got)
	}
	if loaded.Tethers[1].Pinned != Weak {
		t.Fatalf("pinned not preserved: %#v", loaded.Tethers[1])
	}

	// JSON tags support --json output.
	jb, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version"`, `"from"`, `"to"`, `"toMode"`, `"count"`, `"lastSeen"`} {
		if !strings.Contains(string(jb), key) {
			t.Fatalf("json missing %s: %s", key, jb)
		}
	}
}

func TestSaveOmitsEmptyOptionalFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f := &File{Tethers: []Tether{{From: "a", To: "b", ToMode: ModeReference, Count: 1}}}
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pinned:") {
		t.Fatalf("omitempty pinned leaked:\n%s", raw)
	}
	// Save ratchets the version even when the caller left it zero.
	if !strings.Contains(string(raw), "version: 1") {
		t.Fatalf("expected stamped version 1:\n%s", raw)
	}
}

func TestSaveRatchetRefusesNewerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	planted := []byte("version: 6\ntethers: []\n")
	if err := os.WriteFile(path, planted, 0o600); err != nil {
		t.Fatal(err)
	}

	// Load stays permissive on a future-version file.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load newer: %v", err)
	}
	if loaded.Version != 6 {
		t.Fatalf("Load should preserve on-disk version, got %d", loaded.Version)
	}

	err = Save(path, &File{Version: CurrentTethersVersion})
	var tooNew *ErrTethersVersionTooNew
	if !errors.As(err, &tooNew) {
		t.Fatalf("Save error = %v, want *ErrTethersVersionTooNew", err)
	}
	if tooNew.OnDisk != 6 || tooNew.Current != CurrentTethersVersion {
		t.Fatalf("ceiling err = %#v", tooNew)
	}
	if tooNew.Error() == "" {
		t.Fatal("Error() must be non-empty")
	}

	// The on-disk bytes must be untouched by a refused write.
	still, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(still) != string(planted) {
		t.Fatalf("on-disk mutated despite ceiling: %q", still)
	}

	// Update must refuse too (its Save hits the same ceiling).
	err = Update(path, func(f *File) error {
		Bump(f, "a", "b", ModeReference, time.Now())
		return nil
	})
	if !errors.As(err, &tooNew) {
		t.Fatalf("Update error = %v, want *ErrTethersVersionTooNew", err)
	}
}

func TestSaveWithCeilingRatchetsUpNeverDown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	// A file already at a higher-than-current version, written under a matching
	// ceiling, keeps its version (never stepped down).
	if err := saveWithCeiling(path, &File{Version: 3}, 5); err != nil {
		t.Fatalf("saveWithCeiling: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 3 {
		t.Fatalf("version = %d, want 3 (ratchet must not step down to Current)", loaded.Version)
	}
}

func TestSaveWithCeilingSurfacesMalformedExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(":\n\tnope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, &File{}); err == nil {
		t.Fatal("expected error reading malformed existing file for version check")
	}
}

func TestEffectiveStrength(t *testing.T) {
	tests := []struct {
		name      string
		tether    Tether
		threshold int
		want      Strength
	}{
		{"below threshold is weak", Tether{Count: 2}, 3, Weak},
		{"at threshold is strong", Tether{Count: 3}, 3, Strong},
		{"above threshold is strong", Tether{Count: 9}, 3, Strong},
		{"pin strong overrides low count", Tether{Count: 0, Pinned: Strong}, 3, Strong},
		{"pin weak overrides high count", Tether{Count: 99, Pinned: Weak}, 3, Weak},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveStrength(tt.tether, tt.threshold); got != tt.want {
				t.Fatalf("EffectiveStrength = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBumpDeDupByPair(t *testing.T) {
	f := &File{}
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	Bump(f, "api", "web", ModeReference, now)
	Bump(f, "api", "web", ModeReference, now.Add(time.Hour))
	if len(f.Tethers) != 1 {
		t.Fatalf("identity (from,to) must de-dup, got %#v", f.Tethers)
	}
	if f.Tethers[0].Count != 2 {
		t.Fatalf("Count = %d, want 2", f.Tethers[0].Count)
	}
	if !f.Tethers[0].LastSeen.Equal(now.Add(time.Hour)) {
		t.Fatalf("LastSeen not refreshed: %v", f.Tethers[0].LastSeen)
	}
}

func TestBumpModePrecedenceEditWins(t *testing.T) {
	now := time.Now()

	// Existing edit stays edit when a reference co-occurrence arrives.
	f := &File{}
	Bump(f, "api", "web", ModeEdit, now)
	Bump(f, "api", "web", ModeReference, now)
	if f.Tethers[0].ToMode != ModeEdit {
		t.Fatalf("edit must not be downgraded to reference: %#v", f.Tethers[0])
	}

	// Existing reference is upgraded to edit.
	g := &File{}
	Bump(g, "api", "web", ModeReference, now)
	Bump(g, "api", "web", ModeEdit, now)
	if g.Tethers[0].ToMode != ModeEdit {
		t.Fatalf("reference must upgrade to edit: %#v", g.Tethers[0])
	}
	if g.Tethers[0].Count != 2 {
		t.Fatalf("edit+ref for one pair counted %d, want 2", g.Tethers[0].Count)
	}
}

func TestBumpIgnoresSelfAndEmpty(t *testing.T) {
	f := &File{}
	now := time.Now()
	Bump(f, "api", "api", ModeReference, now) // self
	Bump(f, "", "web", ModeReference, now)    // empty from
	Bump(f, "api", "", ModeReference, now)    // empty to
	if len(f.Tethers) != 0 {
		t.Fatalf("no-op bumps recorded: %#v", f.Tethers)
	}
}

func TestFindReturnsByFrom(t *testing.T) {
	f := &File{Tethers: []Tether{
		{From: "api", To: "web"},
		{From: "api", To: "db"},
		{From: "web", To: "cdn"},
	}}
	got := Find(f, "api")
	if len(got) != 2 {
		t.Fatalf("Find api = %#v", got)
	}
	if Find(f, "nope") != nil {
		t.Fatal("Find of unknown from should be nil")
	}
}

func TestCommonStrongFirstAndIncludeWeak(t *testing.T) {
	f := &File{Tethers: []Tether{
		{From: "api", To: "weak1", Count: 1},
		{From: "api", To: "strongA", Count: 5},
		{From: "api", To: "strongB", Count: 8},
		{From: "other", To: "x", Count: 9},
	}}
	threshold := 3

	strongOnly := Common(f, "api", false, threshold)
	if len(strongOnly) != 2 {
		t.Fatalf("strong-only = %#v", strongOnly)
	}
	// Strong first, count desc tiebreak: strongB(8) before strongA(5).
	if strongOnly[0].To != "strongB" || strongOnly[1].To != "strongA" {
		t.Fatalf("ordering wrong: %#v", strongOnly)
	}
	for _, tt := range strongOnly {
		if tt.Strength != Strong {
			t.Fatalf("weak leaked into strong-only: %#v", tt)
		}
	}

	withWeak := Common(f, "api", true, threshold)
	if len(withWeak) != 3 {
		t.Fatalf("include-weak = %#v", withWeak)
	}
	// Weak sorts last.
	if withWeak[2].To != "weak1" || withWeak[2].Strength != Weak {
		t.Fatalf("weak should sort last: %#v", withWeak)
	}
}

func TestPinCreatesAndOverrides(t *testing.T) {
	f := &File{Tethers: []Tether{{From: "api", To: "web", Count: 0}}}
	Pin(f, "api", "web", Strong)
	if f.Tethers[0].Pinned != Strong {
		t.Fatalf("existing pin not set: %#v", f.Tethers[0])
	}
	if EffectiveStrength(f.Tethers[0], 100) != Strong {
		t.Fatal("pin should force strong regardless of threshold")
	}

	// Pinning a non-existent pair creates it.
	Pin(f, "api", "new", Weak)
	if find(f, "api", "new") < 0 {
		t.Fatal("Pin should create a missing tether")
	}

	// A repo never tethers to itself; self/empty pins are no-ops.
	before := len(f.Tethers)
	Pin(f, "api", "api", Strong)
	Pin(f, "", "web", Strong)
	Pin(f, "api", "", Strong)
	if len(f.Tethers) != before {
		t.Fatalf("Pin should ignore self/empty pairs: %#v", f.Tethers)
	}
}

func TestRemoveAndRemoveAll(t *testing.T) {
	f := &File{Tethers: []Tether{
		{From: "api", To: "web"},
		{From: "api", To: "db"},
		{From: "web", To: "cdn"},
	}}
	Remove(f, "api", "web")
	if find(f, "api", "web") >= 0 {
		t.Fatal("Remove did not drop the pair")
	}
	if len(f.Tethers) != 2 {
		t.Fatalf("Remove dropped wrong count: %#v", f.Tethers)
	}
	Remove(f, "api", "nope") // no-op, must not panic
	if len(f.Tethers) != 2 {
		t.Fatalf("no-op Remove changed the file: %#v", f.Tethers)
	}

	RemoveAll(f, "api")
	if len(Find(f, "api")) != 0 {
		t.Fatalf("RemoveAll left tethers: %#v", f.Tethers)
	}
	if len(f.Tethers) != 1 || f.Tethers[0].From != "web" {
		t.Fatalf("RemoveAll dropped unrelated rows: %#v", f.Tethers)
	}
}

func TestUpdateLoadModifySave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName) // dir does not exist yet
	now := time.Now()
	if err := Update(path, func(f *File) error {
		Bump(f, "api", "web", ModeReference, now)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tethers) != 1 || loaded.Tethers[0].Count != 1 {
		t.Fatalf("Update did not persist: %#v", loaded.Tethers)
	}
}

func TestUpdatePropagatesFnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	sentinel := errors.New("boom")
	if err := Update(path, func(*File) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want sentinel", err)
	}
	// A failed fn must not have written the file.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after fn error, stat err = %v", err)
	}
}

// TestUpdateSerializesConcurrentBumps races many goroutines bumping the same
// pair; the advisory lock must prevent lost increments so the final Count
// equals the total number of bumps. Modeled on fsio's WithLock serialization
// test. Run under -race.
func TestUpdateSerializesConcurrentBumps(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	const goroutines = 12
	const perGoroutine = 25
	now := time.Now()

	var wg sync.WaitGroup
	var failure atomic.Value // error
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := Update(path, func(f *File) error {
					Bump(f, "api", "web", ModeReference, now)
					return nil
				}); err != nil {
					failure.Store(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err, ok := failure.Load().(error); ok && err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tethers) != 1 {
		t.Fatalf("concurrent bumps de-dup to one pair, got %#v", loaded.Tethers)
	}
	if want := goroutines * perGoroutine; loaded.Tethers[0].Count != want {
		t.Fatalf("Count = %d, want %d (lost update under lock)", loaded.Tethers[0].Count, want)
	}
}
