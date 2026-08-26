package space

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/memory"
	"github.com/Nurozen/stave/internal/tether"
)

// captureService builds a Service with tethers enabled (StrongThreshold 3),
// repos a/b/c/d registered, deterministic Now, and an output buffer so notices
// can be asserted. NO t.Parallel: tether writes are keyed by cfg.Root, and the
// shared root would otherwise race across parallel subtests.
func captureService(t *testing.T) (Service, config.Config, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	repo := func(name string) config.Repository {
		return config.Repository{
			Name:         name,
			URL:          "file:///" + name,
			BareRepoPath: filepath.Join(root, "bare-repos", name+".git"),
		}
	}
	enabled := true
	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"a": repo("a"), "b": repo("b"), "c": repo("c"), "d": repo("d"),
		},
		Tethers: config.TethersConfig{Enabled: &enabled, StrongThreshold: 3},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	svc := NewService(cfg, &fakeGit{}, &buf)
	svc.Now = func() time.Time { return time.Date(2026, 5, 27, 1, 2, 3, 0, time.UTC) }
	return svc, cfg, &buf
}

func loadTethers(t *testing.T, cfg config.Config) *tether.File {
	t.Helper()
	f, err := tether.Load(tether.Path(cfg))
	if err != nil {
		t.Fatalf("load tethers: %v", err)
	}
	return f
}

func findTether(t *testing.T, f *tether.File, from, to string) tether.Tether {
	t.Helper()
	for _, te := range f.Tethers {
		if te.From == from && te.To == to {
			return te
		}
	}
	t.Fatalf("tether %s→%s not found in %#v", from, to, f.Tethers)
	return tether.Tether{}
}

func hasTether(f *tether.File, from, to string) bool {
	for _, te := range f.Tethers {
		if te.From == from && te.To == to {
			return true
		}
	}
	return false
}

func tethersFileExists(cfg config.Config) bool {
	_, err := os.Stat(tether.Path(cfg))
	return err == nil
}

func TestCaptureCreateEditWithReferences(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	opts := func(id string) CreateOptions {
		return CreateOptions{
			ID:         id,
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}, {Name: "c"}},
		}
	}
	if err := svc.Create(ctx, opts("s1")); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	ab := findTether(t, f, "a", "b")
	if ab.Count != 1 || ab.ToMode != tether.ModeReference {
		t.Fatalf("a→b = %#v, want count 1 reference", ab)
	}
	if ac := findTether(t, f, "a", "c"); ac.Count != 1 || ac.ToMode != tether.ModeReference {
		t.Fatalf("a→c = %#v, want count 1 reference", ac)
	}
	// Re-run against a fresh space id: same tether file, counts advance.
	if err := svc.Create(ctx, opts("s2")); err != nil {
		t.Fatal(err)
	}
	f = loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 2 {
		t.Fatalf("a→b count after re-run = %d, want 2", ab.Count)
	}
	if ac := findTether(t, f, "a", "c"); ac.Count != 2 {
		t.Fatalf("a→c count after re-run = %d, want 2", ac.Count)
	}
}

func TestCaptureTwoEditsRecordsBothAnchors(t *testing.T) {
	svc, cfg, _ := captureService(t)
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}, {Name: "b"}},
		References: []RepoSpec{{Name: "c"}},
	}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}, {"a", "c"}, {"b", "c"}} {
		te := findTether(t, f, pair[0], pair[1])
		if te.Count != 1 {
			t.Fatalf("%s→%s count = %d, want 1", pair[0], pair[1], te.Count)
		}
	}
	if te := findTether(t, f, "a", "b"); te.ToMode != tether.ModeEdit {
		t.Fatalf("a→b ToMode = %q, want edit", te.ToMode)
	}
	if te := findTether(t, f, "a", "c"); te.ToMode != tether.ModeReference {
		t.Fatalf("a→c ToMode = %q, want reference", te.ToMode)
	}
}

func TestCaptureSagaMemberRecordsOnce(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- svc.Create(ctx, CreateOptions{
			ID:         "m1",
			SagaID:     "saga1",
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}},
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("saga-member create hung (capture likely inside the saga lock)")
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want exactly 1 (D2 regression: double capture under saga)", ab.Count)
	}
}

func TestCaptureCreateSagaRefsRecordsNothing(t *testing.T) {
	svc, cfg, _ := captureService(t)
	if err := svc.CreateSaga(context.Background(), SagaCreateOptions{
		ID:         "saga1",
		References: []RepoSpec{{Name: "a"}, {Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	if tethersFileExists(cfg) {
		if f := loadTethers(t, cfg); len(f.Tethers) != 0 {
			t.Fatalf("saga (refs only) recorded tethers: %#v", f.Tethers)
		}
	}
}

func TestCaptureAddRepoDelta(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	// Space with a single edit a: no co-occurrence yet.
	if err := svc.Create(ctx, CreateOptions{ID: "s1", Edits: []RepoSpec{{Name: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if tethersFileExists(cfg) {
		if f := loadTethers(t, cfg); len(f.Tethers) != 0 {
			t.Fatalf("solo edit recorded tethers: %#v", f.Tethers)
		}
	}
	// Add reference b with capture → a→b.
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "b", Mode: ModeReference, CaptureOnAdd: true}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 || ab.ToMode != tether.ModeReference {
		t.Fatalf("a→b = %#v, want count 1 reference", ab)
	}
	// Add reference c: delta records ONLY a→c, never re-bumps a→b (D13).
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "c", Mode: ModeReference, CaptureOnAdd: true}); err != nil {
		t.Fatal(err)
	}
	f = loadTethers(t, cfg)
	if ac := findTether(t, f, "a", "c"); ac.Count != 1 {
		t.Fatalf("a→c count = %d, want 1", ac.Count)
	}
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count after adding c = %d, want 1 (delta must not re-bump)", ab.Count)
	}
}

func TestCaptureAddEditableDelta(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	// Space with edit a + reference b (a→b captured at create).
	if err := svc.Create(ctx, CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Add a SECOND editable c: delta must record c→a(edit), a→c(edit) and
	// c→b(reference), but never re-touch the existing a→b.
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "c", Mode: ModeEdit, CaptureOnAdd: true}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ca := findTether(t, f, "c", "a"); ca.Count != 1 || ca.ToMode != tether.ModeEdit {
		t.Fatalf("c→a = %#v, want count 1 edit", ca)
	}
	if ac := findTether(t, f, "a", "c"); ac.Count != 1 || ac.ToMode != tether.ModeEdit {
		t.Fatalf("a→c = %#v, want count 1 edit", ac)
	}
	if cb := findTether(t, f, "c", "b"); cb.Count != 1 || cb.ToMode != tether.ModeReference {
		t.Fatalf("c→b = %#v, want count 1 reference", cb)
	}
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count after adding edit c = %d, want 1 (delta must not re-bump old pair)", ab.Count)
	}
	if hasTether(f, "b", "c") {
		t.Fatal("b→c recorded, but b is a reference (references never anchor)")
	}
}

func TestCaptureCreateInternalLoopNoDoubleCount(t *testing.T) {
	svc, cfg, _ := captureService(t)
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1 (Create's internal AddRepo loop must not double-count)", ab.Count)
	}
}

func TestCaptureDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.Create(ctx, CreateOptions{
			ID:         "s1",
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}},
			DryRun:     true,
		}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatal("dry-run create wrote a tethers file")
		}
	})

	t.Run("add", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.Create(ctx, CreateOptions{ID: "s1", Edits: []RepoSpec{{Name: "a"}}}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "b", Mode: ModeReference, CaptureOnAdd: true, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatal("dry-run add wrote a tethers file")
		}
	})

	t.Run("saga", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Create(ctx, CreateOptions{
			ID:         "m1",
			SagaID:     "saga1",
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}},
			DryRun:     true,
		}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatal("dry-run saga-member create wrote a tethers file")
		}
	})
}

func TestCaptureSameRepoEditAndReference(t *testing.T) {
	svc, cfg, _ := captureService(t)
	// b appears as both an edit and a reference; edit precedence wins.
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}, {Name: "b"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	ab := findTether(t, f, "a", "b")
	if ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1 (b as edit and reference must count once)", ab.Count)
	}
	if ab.ToMode != tether.ModeEdit {
		t.Fatalf("a→b ToMode = %q, want edit (edit precedence)", ab.ToMode)
	}
}

func TestCaptureFailureNonFatal(t *testing.T) {
	svc, cfg, buf := captureService(t)
	// Plant a future-version file so tether.Save refuses inside Update.
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	future := "version: 999\ntethers: []\n"
	if err := os.WriteFile(tether.Path(cfg), []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	})
	if err != nil {
		t.Fatalf("Create failed on capture error (must be best-effort): %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(svc.SpacePath("s1"), ManifestName)); statErr != nil {
		t.Fatalf("space not created despite capture failure: %v", statErr)
	}
	if !bytes.Contains(buf.Bytes(), []byte("notice:")) {
		t.Fatalf("expected a notice on capture failure, got:\n%s", buf.String())
	}
}

func TestCaptureNoLearnSuppresses(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.Create(ctx, CreateOptions{
			ID:         "s1",
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}},
			NoLearn:    true,
		}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatal("--no-learn create still captured")
		}
	})

	t.Run("add", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.Create(ctx, CreateOptions{ID: "s1", Edits: []RepoSpec{{Name: "a"}}}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "b", Mode: ModeReference, CaptureOnAdd: true, NoLearn: true}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatal("--no-learn add still captured")
		}
	})
}

func TestCaptureDisabledSuppresses(t *testing.T) {
	svc, cfg, _ := captureService(t)
	disabled := false
	svc.Config.Tethers.Enabled = &disabled
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	if tethersFileExists(cfg) {
		t.Fatal("tethers disabled but capture still wrote a file")
	}
}

func TestExpandCommonRefs(t *testing.T) {
	svc, cfg, _ := captureService(t)
	// Seed: a→b strong (count 3), a→c weak (count 1), a→z weak where z is
	// unregistered.
	if err := tether.Update(tether.Path(cfg), func(f *tether.File) error {
		now := svc.now()
		for i := 0; i < 3; i++ {
			tether.Bump(f, "a", "b", tether.ModeReference, now)
		}
		tether.Bump(f, "a", "c", tether.ModeReference, now)
		tether.Bump(f, "a", "z", tether.ModeReference, now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("strong only", func(t *testing.T) {
		extra, notes, err := svc.ExpandCommonRefs(cfg, []RepoSpec{{Name: "a"}}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(extra) != 1 || extra[0].Name != "b" {
			t.Fatalf("strong-only extra = %#v, want [b]", extra)
		}
		if len(notes) != 0 {
			t.Fatalf("strong-only notes = %#v, want none (z is weak, excluded)", notes)
		}
	})

	t.Run("include weak with unregistered note", func(t *testing.T) {
		extra, notes, err := svc.ExpandCommonRefs(cfg, []RepoSpec{{Name: "a"}}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, r := range extra {
			names[r.Name] = true
		}
		if !names["b"] || !names["c"] || names["z"] {
			t.Fatalf("include-weak extra = %#v, want b and c (not z)", extra)
		}
		if len(notes) != 1 {
			t.Fatalf("include-weak notes = %#v, want one (z unregistered)", notes)
		}
	})

	t.Run("dedup against explicit refs and edits", func(t *testing.T) {
		extra, _, err := svc.ExpandCommonRefs(cfg, []RepoSpec{{Name: "a"}}, []RepoSpec{{Name: "b"}}, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range extra {
			if r.Name == "b" {
				t.Fatalf("b not deduped against explicit -r: %#v", extra)
			}
		}
		// c is an edit → excluded too.
		extra, _, err = svc.ExpandCommonRefs(cfg, []RepoSpec{{Name: "a"}, {Name: "c"}}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range extra {
			if r.Name == "c" {
				t.Fatalf("c not deduped against -e edits: %#v", extra)
			}
		}
	})

	t.Run("disabled errors", func(t *testing.T) {
		disabledCfg := cfg
		off := false
		disabledCfg.Tethers = config.TethersConfig{Enabled: &off, StrongThreshold: 3}
		if _, _, err := svc.ExpandCommonRefs(disabledCfg, []RepoSpec{{Name: "a"}}, nil, false); err == nil {
			t.Fatal("ExpandCommonRefs succeeded with tethers disabled, want error")
		}
	})
}

func TestCreateWithCommonReferencesMaterializesNotCaptured(t *testing.T) {
	svc, cfg, _ := captureService(t)
	if err := svc.Create(context.Background(), CreateOptions{
		ID:               "s1",
		Edits:            []RepoSpec{{Name: "a"}},
		References:       []RepoSpec{{Name: "b"}},
		CommonReferences: []RepoSpec{{Name: "c"}},
	}); err != nil {
		t.Fatal(err)
	}
	// c's reference worktree is materialized.
	if _, err := os.Stat(filepath.Join(svc.SpacePath("s1"), "references", "c")); err != nil {
		t.Fatalf("common reference c worktree missing: %v", err)
	}
	// But c is NOT captured (OQ-E): only the explicit a→b edge exists.
	f := loadTethers(t, cfg)
	if !hasTether(f, "a", "b") {
		t.Fatal("explicit a→b not captured")
	}
	if hasTether(f, "a", "c") {
		t.Fatal("common reference c must not be captured (OQ-E)")
	}
}

// A repo can be present as BOTH an edit and a reference worktree in one space.
// Adding a new editable c must bump c→a exactly once, not twice (the dual-mode
// prior must be deduped by Name with edit precedence).
func TestCaptureAddEditableDualModePriorNoDoubleCount(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	// a as edit (path "a") AND as reference (path "references/a"): both
	// materialize; create-time capture records nothing (single distinct repo).
	if err := svc.Create(ctx, CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "a"}},
	}); err != nil {
		t.Fatal(err)
	}
	if tethersFileExists(cfg) {
		if f := loadTethers(t, cfg); len(f.Tethers) != 0 {
			t.Fatalf("dual-mode solo repo recorded tethers: %#v", f.Tethers)
		}
	}
	// Add editable c: a appears twice in prior but must be counted once (edit).
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "c", Mode: ModeEdit, CaptureOnAdd: true}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ca := findTether(t, f, "c", "a"); ca.Count != 1 || ca.ToMode != tether.ModeEdit {
		t.Fatalf("c→a = %#v, want count 1 edit (dual-mode prior must not double-count)", ca)
	}
	if ac := findTether(t, f, "a", "c"); ac.Count != 1 || ac.ToMode != tether.ModeEdit {
		t.Fatalf("a→c = %#v, want count 1 edit", ac)
	}
}

// A delta add that can produce no pair must write NO tether file and must not
// hang: the first repo into an empty space, a reference added where no editable
// exists, and a reference added to a saga space (which re-enters under the
// per-saga lock — the tether lock must never be opened there).
func TestCaptureDeltaNoPairWritesNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("first repo", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.InitSpace(ctx, InitOptions{ID: "s1"}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "a", Mode: ModeEdit, CaptureOnAdd: true}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatalf("first-repo add wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
		}
	})

	t.Run("reference with no editable", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.InitSpace(ctx, InitOptions{ID: "s1"}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "a", Mode: ModeReference, CaptureOnAdd: true}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AddRepo(ctx, AddOptions{SpaceID: "s1", RepoName: "b", Mode: ModeReference, CaptureOnAdd: true}); err != nil {
			t.Fatal(err)
		}
		if tethersFileExists(cfg) {
			t.Fatalf("reference-only adds wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
		}
	})

	t.Run("saga reference add", func(t *testing.T) {
		svc, cfg, _ := captureService(t)
		if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- svc.AddRepo(ctx, AddOptions{SpaceID: "saga1", RepoName: "a", Mode: ModeReference, CaptureOnAdd: true})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("saga reference-add hung (tether lock opened under the saga lock)")
		}
		if tethersFileExists(cfg) {
			t.Fatalf("saga reference-add wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
		}
	})
}

// Capture is tied to durable materialization, not command success: a create
// whose repos are durably built but whose explicit --memory attach fails must
// still record the co-occurrence tether AND still surface the attach error.
func TestCaptureRunsEvenWhenMemoryAttachFails(t *testing.T) {
	svc, cfg, _ := captureService(t)
	svc.Memory = &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing binary")}
		},
	}
	err := svc.Create(context.Background(), CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
		Memories:   []string{"."},
	})
	if err == nil {
		t.Fatal("expected the failed explicit --memory attach to surface an error")
	}
	// Repos are durably materialized → the tether is recorded despite the error.
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1 (capture must run on durable materialization)", ab.Count)
	}
}

// -c/--common references must be linked into attached memory (they earn a
// worktree AND a memory link), while STILL being excluded from co-occurrence
// capture (OQ-E: the tether file must not gain the common ref's edge).
func TestCaptureCommonRefLinkedIntoMemoryButNotTethered(t *testing.T) {
	svc, cfg, _ := captureService(t)
	var saw []memory.ReferenceSpec
	svc.Memory = &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			saw = opts.ReferenceSpecs
			return memory.AttachResult{Provider: "fake", StoreID: opts.SpaceID, Name: "default", Owned: true, MCPConfigWritten: true}, nil
		},
	}
	if err := svc.Create(context.Background(), CreateOptions{
		ID:               "s1",
		Edits:            []RepoSpec{{Name: "a"}},
		References:       []RepoSpec{{Name: "b"}},
		CommonReferences: []RepoSpec{{Name: "c"}},
		Memories:         []string{"."},
	}); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range saw {
		names[r.Name] = true
	}
	if !names["b"] || !names["c"] {
		t.Fatalf("memory attach reference specs = %#v, want both explicit b and common c", saw)
	}
	// Capture still excludes the common ref (OQ-E).
	f := loadTethers(t, cfg)
	if !hasTether(f, "a", "b") {
		t.Fatal("explicit a→b not captured")
	}
	if hasTether(f, "a", "c") {
		t.Fatal("common reference c must not be captured (OQ-E)")
	}
}
