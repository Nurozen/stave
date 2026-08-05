package space

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/memory"
)

// countDetachCalls counts fake Detach records naming storeID.
func countDetachCalls(fake *memory.Fake, storeID string) int {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	n := 0
	for _, c := range fake.Calls {
		if strings.Contains(c, "detach") && strings.Contains(c, storeID) {
			n++
		}
	}
	return n
}

// twoDenSpace builds a space with two owned attachments (den-alpha, den-beta).
func twoDenSpace(t *testing.T, svc Service, id string) string {
	t.Helper()
	if err := svc.InitSpace(context.Background(), InitOptions{ID: id}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath(id)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Memories = []MemoryManifest{
		{Name: "alpha", Provider: "fake", ID: "den-alpha", Owned: true},
		{Name: "beta", Provider: "fake", ID: "den-beta", Owned: true},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.writeAgents(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	return spacePath
}

// TestDestroyPartialFateRetryConverges: destroy-fate teardown records each
// destroyed attachment durably, so a mid-loop refusal leaves the manifest
// listing exactly the unprocessed attachment and a retry never re-destroys
// the den that is already gone.
func TestDestroyPartialFateRetryConverges(t *testing.T) {
	svc, _, _ := testService(t)
	refuseBeta := true
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "den-beta" && refuseBeta {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Destroyed: true}, nil
	}
	svc.Memory = fake
	spacePath := twoDenSpace(t, svc, "converge")

	err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "converge", MemoryFate: memory.FateDestroy, Force: true})
	if err == nil {
		t.Fatal("expected refusal on den-beta")
	}
	var inUse *MemoryInUseError
	if !errors.As(err, &inUse) || inUse.SpaceID != "converge" {
		t.Fatalf("expected MemoryInUseError classification: %v", err)
	}
	// Space intact; manifest lists ONLY the unprocessed attachment.
	if _, statErr := os.Stat(spacePath); statErr != nil {
		t.Fatalf("space must remain after partial failure: %v", statErr)
	}
	manifest, loadErr := LoadManifest(spacePath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 1 || manifest.Memories[0].ID != "den-beta" {
		t.Fatalf("manifest after partial failure = %#v", manifest.Memories)
	}
	// AGENTS.md must no longer advertise the destroyed den.
	agents, readErr := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(agents), "den-alpha") {
		t.Fatalf("AGENTS.md still advertises the destroyed den:\n%s", agents)
	}
	if !strings.Contains(string(agents), "den-beta") {
		t.Fatalf("AGENTS.md must keep the surviving den:\n%s", agents)
	}

	// Flip to success: the retry converges without re-destroying den-alpha.
	refuseBeta = false
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "converge", MemoryFate: memory.FateDestroy, Force: true}); err != nil {
		t.Fatalf("retry must converge: %v", err)
	}
	if _, statErr := os.Stat(spacePath); !os.IsNotExist(statErr) {
		t.Fatal("space must be destroyed on retry")
	}
	if n := countDetachCalls(fake, "den-alpha"); n != 1 {
		t.Fatalf("den-alpha detached %d times; the retry must not re-destroy it: %#v", n, fake.Calls)
	}
	if n := countDetachCalls(fake, "den-beta"); n != 2 {
		t.Fatalf("den-beta detached %d times, want 2: %#v", n, fake.Calls)
	}
}

// TestDestroyPartialContributeRetryNoRecontribute: the contribute-fate flavor
// — a retry after a mid-loop refusal must not re-issue the contribute-detach
// for the attachment already recorded as processed.
func TestDestroyPartialContributeRetryNoRecontribute(t *testing.T) {
	svc, _, _ := testService(t)
	refuseBeta := true
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "den-beta" && refuseBeta {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Destroyed: true}, nil
	}
	svc.Memory = fake
	spacePath := twoDenSpace(t, svc, "ccnv")

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "ccnv", MemoryFate: memory.FateContribute, Force: true}); err == nil {
		t.Fatal("expected refusal on den-beta")
	}
	manifest, _ := LoadManifest(spacePath)
	if len(manifest.Memories) != 1 || manifest.Memories[0].ID != "den-beta" {
		t.Fatalf("manifest after partial failure = %#v", manifest.Memories)
	}
	refuseBeta = false
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "ccnv", MemoryFate: memory.FateContribute, Force: true}); err != nil {
		t.Fatalf("retry must converge: %v", err)
	}
	// Exactly one contribute-fate detach for den-alpha across both runs: the
	// provider runs contribute+propose inside Detach, so a second call would
	// re-contribute.
	if n := countDetachCalls(fake, "den-alpha"); n != 1 {
		t.Fatalf("den-alpha detached %d times; the retry must not re-contribute it: %#v", n, fake.Calls)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	for _, c := range fake.Calls {
		if strings.Contains(c, "den-alpha") && !strings.Contains(c, string(memory.FateContribute)) {
			t.Fatalf("den-alpha call lost its contribute fate: %q", c)
		}
	}
}

// TestDestroyDenNotFoundToleratedDestroyFate: a den_not_found refusal on a
// destroy-fate detach is the crash-window signature (den destroyed, manifest
// save lost) — the destroy treats it as done and converges.
func TestDestroyDenNotFoundToleratedDestroyFate(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "den-alpha" {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeDenNotFound, Message: "den \"den-alpha\" not found"}
		}
		return memory.DetachResult{Destroyed: true}, nil
	}
	svc.Memory = fake
	spacePath := twoDenSpace(t, svc, "dnf")
	var out strings.Builder
	svc.Out = &out

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "dnf", MemoryFate: memory.FateDestroy, Force: true}); err != nil {
		t.Fatalf("den_not_found must be tolerated as already-done: %v", err)
	}
	if !strings.Contains(out.String(), "den not found") || !strings.Contains(out.String(), "treating as done") {
		t.Fatalf("expected already-done notice:\n%s", out.String())
	}
	if _, statErr := os.Stat(spacePath); !os.IsNotExist(statErr) {
		t.Fatal("space must be destroyed")
	}
}

// TestDestroyDenNotFoundContributeNotice: for the contribute fate the den
// vanished before its contribution could be verified — the notice must say
// so (never phrased as contributed), and the teardown still converges.
func TestDestroyDenNotFoundContributeNotice(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeDenNotFound, Message: "den not found"}
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "cnf"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("cnf")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "alpha", Provider: "fake", ID: "den-alpha", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "cnf", MemoryFate: memory.FateContribute, Force: true}); err != nil {
		t.Fatalf("den_not_found must be tolerated so retries converge: %v", err)
	}
	if !strings.Contains(out.String(), "den vanished before its contribution could be verified") {
		t.Fatalf("expected unverified-contribution notice:\n%s", out.String())
	}
	if strings.Contains(out.String(), "contributed") {
		t.Fatalf("notice must never claim the content was contributed:\n%s", out.String())
	}
	if _, statErr := os.Stat(spacePath); !os.IsNotExist(statErr) {
		t.Fatal("space must be destroyed")
	}
}

// TestDetachMemoryDestroyDenNotFoundConverges: detachMemoryLocked shares the
// crash window — the manifest names a den that no longer exists; a
// destroy-fate detach must notice, splice the record and converge instead of
// wedging every retry.
func TestDetachMemoryDestroyDenNotFoundConverges(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeDenNotFound, Message: "den not found"}
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "dwedge"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("dwedge")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "gone-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out

	if err := svc.DetachMemory(context.Background(), "dwedge", "default", memory.FateDestroy, false, false); err != nil {
		t.Fatalf("detach of a vanished den must converge: %v", err)
	}
	if !strings.Contains(out.String(), "den not found") {
		t.Fatalf("expected den_not_found notice:\n%s", out.String())
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 0 {
		t.Fatalf("attachment record must be spliced: %#v", manifest.Memories)
	}
}

// TestDestroyFateDryRunManifestUntouched: dry-run destroying fates never save
// — the manifest still lists every attachment afterwards.
func TestDestroyFateDryRunManifestUntouched(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	spacePath := twoDenSpace(t, svc, "drykeep")

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "drykeep", MemoryFate: memory.FateDestroy, Force: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(spacePath); statErr != nil {
		t.Fatalf("dry-run must not remove the space: %v", statErr)
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 2 {
		t.Fatalf("dry-run must not splice attachments: %#v", manifest.Memories)
	}
}

// TestDestroySagaManifestLockedSpliceSurvivesConcurrentWrite: a direct
// `space destroy` of a saga space (per-saga lock NOT held by the caller) must
// run each splice-save as a locked reload → remove → save transaction. A row
// written concurrently between Destroy's manifest load and the splice (here: a
// saga member appended during the first Detach) must survive — saving the
// pre-loop snapshot would silently drop it.
func TestDestroySagaManifestLockedSpliceSurvivesConcurrentWrite(t *testing.T) {
	svc, _, _ := testService(t)
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "sagadirect"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("sagadirect")
	manifest, _ := LoadManifest(spacePath)
	manifest.Saga = &SagaManifest{}
	manifest.Memories = []MemoryManifest{
		{Name: "alpha", Provider: "fake", ID: "den-alpha", Owned: true},
		{Name: "beta", Provider: "fake", ID: "den-beta", Owned: true},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	fake := &memory.Fake{}
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		switch opts.StoreID {
		case "den-alpha":
			// Concurrent saga writer: lands a marker row AFTER Destroy loaded
			// its manifest snapshot and BEFORE the splice-save.
			fresh, err := LoadManifest(spacePath)
			if err != nil {
				return memory.DetachResult{}, err
			}
			fresh.Saga.Members = append(fresh.Saga.Members, SagaMember{ID: "marker", CreatedAt: time.Now().UTC()})
			if err := SaveManifest(spacePath, fresh); err != nil {
				return memory.DetachResult{}, err
			}
			return memory.DetachResult{Destroyed: true}, nil
		default: // den-beta refuses so the mid-teardown state stays observable
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
	}
	svc.Memory = fake

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "sagadirect", MemoryFate: memory.FateDestroy, Force: true}); err == nil {
		t.Fatal("expected refusal on den-beta")
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 1 || manifest.Memories[0].ID != "den-beta" {
		t.Fatalf("manifest after partial failure = %#v", manifest.Memories)
	}
	found := false
	for _, member := range manifest.Saga.Members {
		if member.ID == "marker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("concurrently-written saga row was dropped by the splice-save: %#v", manifest.Saga)
	}
}
