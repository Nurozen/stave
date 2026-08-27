package space

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/memory"
)

// failingMemory returns a memory provider whose Attach always fails with an
// UnavailableError, forcing a saga-member create to fail AFTER its repos are
// durably materialized (memory attach runs post-repo-loop).
func failingMemory() *memory.Fake {
	return &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing binary")}
		},
	}
}

// A saga member whose repos are durably built but whose create FAILS (memory
// attach) must still record its co-occurrence tether — captured post-lock in the
// saga branch's error path — and exactly once.
func TestSagaRecoveryCaptureOnMemoryFailure(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	svc.Memory = failingMemory()

	err := svc.Create(ctx, CreateOptions{
		ID:         "m1",
		SagaID:     "saga1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
		Memories:   []string{"."},
	})
	if err == nil {
		t.Fatal("expected the failed saga-member --memory attach to surface an error")
	}
	// The member is durably materialized despite the create failure.
	m, mErr := LoadManifest(svc.SpacePath("m1"))
	if mErr != nil {
		t.Fatalf("member manifest should exist after durable build: %v", mErr)
	}
	if len(m.Repos) == 0 {
		t.Fatalf("member manifest has no repos, want durably built repos: %#v", m)
	}
	// Recovery capture recorded a→b exactly once.
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1 (recovery capture must run on durable materialization)", ab.Count)
	}
}

// The success path (no failing memory) still captures exactly once — the error
// path and success path are mutually exclusive, so no double-count.
func TestSagaMemberSuccessCapturesOnce(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{
		ID:         "m1",
		SagaID:     "saga1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1 (success path must capture exactly once)", ab.Count)
	}
}

// `stave saga add` of a normally-created (already-captured) space must NOT
// capture again — SagaAdd is untouched, so adding never double-counts.
func TestSagaAddDoesNotCapture(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	// Create a standalone space normally → captured once.
	if err := svc.Create(ctx, CreateOptions{
		ID:         "s1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d after create, want 1", ab.Count)
	}
	// Add it to a saga: must not re-capture.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "saga1", "s1", nil, false); err != nil {
		t.Fatal(err)
	}
	f = loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d after saga add, want 1 (saga add must not capture)", ab.Count)
	}
}

// A create that fails BEFORE the member is materialized (unresolvable saga →
// createInSaga errors before any repo is built) must record nothing: the
// spaceHasRepos gate rejects the un-built member.
func TestSagaRecoveryNoCaptureBeforeMaterialization(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	err := svc.Create(ctx, CreateOptions{
		ID:         "m1",
		SagaID:     "nope", // no such saga → createInSaga fails pre-materialization
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
	})
	if err == nil {
		t.Fatal("expected create against a non-existent saga to fail")
	}
	if tethersFileExists(cfg) {
		t.Fatalf("pre-materialization failure wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
	}
}

// A durable-but-failed member with fewer than two distinct repos yields no pair,
// so captureCoOccurrence writes no tether file (its <2-associates guard).
func TestSagaRecoveryNoCaptureBelowTwoRepos(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	svc.Memory = failingMemory()
	err := svc.Create(ctx, CreateOptions{
		ID:       "m1",
		SagaID:   "saga1",
		Edits:    []RepoSpec{{Name: "a"}}, // single repo → no pair
		Memories: []string{"."},
	})
	if err == nil {
		t.Fatal("expected the failed saga-member --memory attach to surface an error")
	}
	if tethersFileExists(cfg) {
		t.Fatalf("single-repo member wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
	}
}

// --no-learn (CreateOptions.NoLearn) at the failing create suppresses the
// recovery capture entirely.
func TestSagaRecoveryNoLearnSuppresses(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	svc.Memory = failingMemory()
	err := svc.Create(ctx, CreateOptions{
		ID:         "m1",
		SagaID:     "saga1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
		Memories:   []string{"."},
		NoLearn:    true,
	})
	if err == nil {
		t.Fatal("expected the failed saga-member --memory attach to surface an error")
	}
	if tethersFileExists(cfg) {
		t.Fatalf("--no-learn recovery wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
	}
}

// Tethers disabled at the failing create suppresses the recovery capture.
func TestSagaRecoveryTethersDisabledSuppresses(t *testing.T) {
	svc, cfg, _ := captureService(t)
	disabled := false
	svc.Config.Tethers.Enabled = &disabled
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	svc.Memory = failingMemory()
	err := svc.Create(ctx, CreateOptions{
		ID:         "m1",
		SagaID:     "saga1",
		Edits:      []RepoSpec{{Name: "a"}},
		References: []RepoSpec{{Name: "b"}},
		Memories:   []string{"."},
	})
	if err == nil {
		t.Fatal("expected the failed saga-member --memory attach to surface an error")
	}
	if tethersFileExists(cfg) {
		t.Fatalf("tethers-disabled recovery wrote a tether file: %#v", loadTethers(t, cfg).Tethers)
	}
}

// The recovery capture runs POST-LOCK (createInSaga released the saga/membership
// locks before Create's error path calls captureCoOccurrence), so a failing
// saga-member create must not deadlock the saga↔tether lock ordering. This test
// is meaningful under `go test -race`.
func TestSagaRecoveryCaptureNoDeadlock(t *testing.T) {
	svc, cfg, _ := captureService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga1"}); err != nil {
		t.Fatal(err)
	}
	svc.Memory = failingMemory()
	done := make(chan error, 1)
	go func() {
		done <- svc.Create(ctx, CreateOptions{
			ID:         "m1",
			SagaID:     "saga1",
			Edits:      []RepoSpec{{Name: "a"}},
			References: []RepoSpec{{Name: "b"}},
			Memories:   []string{"."},
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the failed saga-member --memory attach to surface an error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("failing saga-member create hung (recovery capture deadlocked saga↔tether)")
	}
	f := loadTethers(t, cfg)
	if ab := findTether(t, f, "a", "b"); ab.Count != 1 {
		t.Fatalf("a→b count = %d, want 1", ab.Count)
	}
}
