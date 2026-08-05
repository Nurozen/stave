package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/memory"
)

// createMemberInSaga creates a live member with one edit repo-a via the real
// create-in-saga path (after edges drive both the DAG and the stacked base).
func createMemberInSaga(t *testing.T, svc Service, sagaID, id string, after ...string) {
	t.Helper()
	if err := svc.Create(context.Background(), CreateOptions{ID: id, SagaID: sagaID, Edits: []RepoSpec{{Name: "repo-a"}}, After: after}); err != nil {
		t.Fatalf("Create(%s) error = %v", id, err)
	}
}

// writeLifecycleMember hand-writes a member space (manifest + worktree dir)
// so tests can shape stacking independently of after edges.
func writeLifecycleMember(t *testing.T, svc Service, id, base string) {
	t.Helper()
	path := svc.SpacePath(id)
	if err := os.MkdirAll(filepath.Join(path, "repo-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: id, CreatedAt: svc.now(), Repos: []RepoManifest{{
		Name: "repo-a", Mode: ModeEdit, Path: "repo-a", Base: base,
		Branch: DefaultBranch(id, "repo-a"), BareRepoPath: svc.Config.Repos["repo-a"].BareRepoPath,
	}}}
	if err := SaveManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func dirExists(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatal(err)
	}
	return info.IsDir()
}

// callIndex returns the index of the first fakeGit call with the prefix, or -1.
func callIndex(calls []string, prefix string) int {
	for i, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}

func TestSagaArchiveReverseTopoSagaLast(t *testing.T) {
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ta"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-ta", "a-1")
	createMemberInSaga(t, svc, "epic-ta", "b-1", "a-1") // stacks on a-1's branch

	fg.calls = nil
	if err := svc.SagaArchive(ctx, "epic-ta", SagaArchiveOptions{}); err != nil {
		t.Fatalf("SagaArchive() error = %v", err)
	}
	// Reverse topo: the dependent b-1 tears down BEFORE its base a-1.
	bIdx := callIndex(fg.calls, "remove|"+cfg.Repos["repo-a"].BareRepoPath+"|"+filepath.Join(svc.SpacePath("b-1"), "repo-a"))
	aIdx := callIndex(fg.calls, "remove|"+cfg.Repos["repo-a"].BareRepoPath+"|"+filepath.Join(svc.SpacePath("a-1"), "repo-a"))
	if bIdx < 0 || aIdx < 0 || bIdx > aIdx {
		t.Fatalf("teardown order wrong (b-1 at %d, a-1 at %d):\n%#v", bIdx, aIdx, fg.calls)
	}
	archiveRoot := filepath.Join(cfg.AgentWorkDir, ".archive")
	for _, id := range []string{"a-1", "b-1", "epic-ta"} {
		if dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s still present after saga archive", id)
		}
		if !dirExists(t, filepath.Join(archiveRoot, id)) {
			t.Fatalf("space %s missing from .archive", id)
		}
	}
	// The archived saga keeps its roster (the record is the durable artifact).
	manifest, err := LoadManifest(filepath.Join(archiveRoot, "epic-ta"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Saga == nil || len(manifest.Saga.Members) != 2 {
		t.Fatalf("archived saga manifest = %+v, want intact roster", manifest)
	}
}

func TestSagaArchiveFailFastGuards(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()

	// All creation up front: a corrupt fixture would otherwise poison later
	// SagaAdd membership scans (they fail closed on unreadable siblings).
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-cor"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-cor", "c-1")
	createMemberInSaga(t, svc, "epic-cor", "c-2")
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dirty"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-dirty", "w-1")
	createMemberInSaga(t, svc, "epic-dirty", "w-2", "w-1")
	mustInitSpace(t, svc, "plain-lc")

	// Dirty member: refused across ALL live members before any teardown. w-1
	// is torn down LAST (reverse topo), so a pre-teardown dirty check is the
	// only thing that can catch it before w-2 is gone.
	fg.dirty = map[string]bool{filepath.Join(svc.SpacePath("w-1"), "repo-a"): true}
	fg.calls = nil
	err := svc.SagaArchive(ctx, "epic-dirty", SagaArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), "dirty editable worktrees") || !strings.Contains(err.Error(), "w-1") {
		t.Fatalf("SagaArchive(dirty member) error = %v", err)
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("dirty member did not fail before teardown: %#v", fg.calls)
	}
	// Force overrides.
	if err := svc.SagaArchive(ctx, "epic-dirty", SagaArchiveOptions{Force: true}); err != nil {
		t.Fatalf("SagaArchive(force) error = %v", err)
	}

	// Archive never destroys memory.
	err = svc.SagaArchive(ctx, "epic-x", SagaArchiveOptions{MemoryFate: memory.FateDestroy})
	if err == nil || !strings.Contains(err.Error(), "archive does not destroy memory") {
		t.Fatalf("SagaArchive(fate destroy) error = %v", err)
	}
	// Non-sagas are refused.
	if err := svc.SagaArchive(ctx, "plain-lc", SagaArchiveOptions{}); err == nil || !strings.Contains(err.Error(), "is not a saga") {
		t.Fatalf("SagaArchive(plain) error = %v", err)
	}

	// Corrupt member: abort naming it, nothing torn down.
	if err := os.WriteFile(filepath.Join(svc.SpacePath("c-1"), ManifestName), []byte("\t- not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	err = svc.SagaArchive(ctx, "epic-cor", SagaArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), "member c-1 has a corrupt manifest") {
		t.Fatalf("SagaArchive(corrupt member) error = %v", err)
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("corrupt member did not fail before teardown: %#v", fg.calls)
	}
	for _, id := range []string{"c-2", "epic-cor"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s torn down despite corrupt-member abort", id)
		}
	}
}

// TestSagaArchiveDependentGuardExemption pins the guard exemption: intra-saga
// stacked bases retired in the same operation must not refuse even when the
// walk retires the BASE before its dependent (after edges declared opposite
// to the stacking), while an external dependent still refuses without Force.
func TestSagaArchiveDependentGuardExemption(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()

	// After edge deliberately opposite the stacking: dep-1 stacks on base-1,
	// but base-1 is declared after dep-1, so reverse topo retires base-1 FIRST
	// while dep-1 is still live and stacking on it.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ex"}); err != nil {
		t.Fatal(err)
	}
	writeLifecycleMember(t, svc, "dep-1", "refs/heads/stave/base-1/repo-a")
	writeLifecycleMember(t, svc, "base-1", "origin/main")
	if err := svc.SagaAdd(ctx, "epic-ex", "dep-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-ex", "base-1", []string{"dep-1"}, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaArchive(ctx, "epic-ex", SagaArchiveOptions{}); err != nil {
		t.Fatalf("SagaArchive with intra-saga stacking error = %v (same-operation members must be exempt)", err)
	}
	archiveRoot := filepath.Join(cfg.AgentWorkDir, ".archive")
	for _, id := range []string{"dep-1", "base-1", "epic-ex"} {
		if !dirExists(t, filepath.Join(archiveRoot, id)) {
			t.Fatalf("space %s missing from .archive", id)
		}
	}

	// External dependent: a NON-member stacking on a member still refuses.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ex2"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-ex2", "base-2")
	writeLifecycleMember(t, svc, "ext-1", "refs/heads/stave/base-2/repo-a")
	err := svc.SagaArchive(ctx, "epic-ex2", SagaArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), `space "ext-1"`) || !strings.Contains(err.Error(), "stacks on") {
		t.Fatalf("SagaArchive with external dependent error = %v", err)
	}
	if !dirExists(t, svc.SpacePath("base-2")) {
		t.Fatal("member torn down despite external-dependent refusal")
	}
	if err := svc.SagaArchive(ctx, "epic-ex2", SagaArchiveOptions{Force: true}); err != nil {
		t.Fatalf("SagaArchive(force, external dependent) error = %v", err)
	}
	if !dirExists(t, svc.SpacePath("ext-1")) {
		t.Fatal("external dependent must survive the saga archive")
	}
}

// TestSagaTeardownExternalDependentPreflight pins the fail-fast posture of
// the dependent-base guard: an external space stacking on the topologically
// FIRST member (torn down LAST in the reverse-topo walk) must refuse BEFORE
// any teardown — the in-walk check alone would only fire after the later
// members were already archived.
func TestSagaTeardownExternalDependentPreflight(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pre"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-pre", "pre-1")
	createMemberInSaga(t, svc, "epic-pre", "pre-2", "pre-1")
	createMemberInSaga(t, svc, "epic-pre", "pre-3", "pre-2")
	// External (non-member) space stacking on the first-topo member's branch.
	writeLifecycleMember(t, svc, "ext-pre", "refs/heads/stave/pre-1/repo-a")

	fg.calls = nil
	err := svc.SagaArchive(ctx, "epic-pre", SagaArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), `space "ext-pre"`) || !strings.Contains(err.Error(), "stacks on") {
		t.Fatalf("SagaArchive(external dependent on first-topo member) error = %v", err)
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("teardown began despite the preflight refusal: %#v", fg.calls)
	}
	for _, id := range []string{"pre-1", "pre-2", "pre-3", "epic-pre"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s torn down despite the refusal", id)
		}
	}
	for _, id := range []string{"pre-1", "pre-2", "pre-3"} {
		if dirExists(t, filepath.Join(svc.Config.AgentWorkDir, ".archive", id)) {
			t.Fatalf("member %s archived despite the refusal", id)
		}
	}

	// Dry-run: the would-refuse condition prints as a diagnostic and the
	// preview continues instead of erroring.
	var out strings.Builder
	svc.Out = &out
	fg.calls = nil
	if err := svc.SagaArchive(ctx, "epic-pre", SagaArchiveOptions{DryRun: true}); err != nil {
		t.Fatalf("SagaArchive(dry-run, external dependent) error = %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: would refuse:") || !strings.Contains(out.String(), `space "ext-pre"`) {
		t.Fatalf("dry-run would-refuse diagnostic missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "dry-run: saga archive plan for epic-pre") {
		t.Fatalf("dry-run preview did not continue past the guard:\n%s", out.String())
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("dry-run removed worktrees: %#v", fg.calls)
	}
	for _, id := range []string{"pre-1", "pre-2", "pre-3", "epic-pre"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("dry-run tore down %s", id)
		}
	}
}

// TestSagaArchivePartialFailureAndRetry injects a git failure at the second
// member: the first stays archived, the saga stays intact, the report lists
// completed members, and a retry converges via the skip semantics.
func TestSagaArchivePartialFailureAndRetry(t *testing.T) {
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pf"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-pf", "p-1")
	createMemberInSaga(t, svc, "epic-pf", "p-2", "p-1")

	// Reverse topo retires p-2 first; fail the SECOND member's (p-1) removal.
	fg.removeFn = func(bare, path string) error {
		if strings.Contains(path, string(filepath.Separator)+"p-1"+string(filepath.Separator)) {
			return errors.New("worktree removal exploded")
		}
		return nil
	}
	var out strings.Builder
	svc.Out = &out
	err := svc.SagaArchive(ctx, "epic-pf", SagaArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), "member p-1") || !strings.Contains(err.Error(), "worktree removal exploded") {
		t.Fatalf("SagaArchive(partial failure) error = %v", err)
	}
	if !strings.Contains(out.String(), "members already archived this run: p-2") {
		t.Fatalf("completed members not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "retries converge") {
		t.Fatalf("retry guidance missing:\n%s", out.String())
	}
	archiveRoot := filepath.Join(cfg.AgentWorkDir, ".archive")
	if !dirExists(t, filepath.Join(archiveRoot, "p-2")) {
		t.Fatal("first member p-2 should be archived before the failure")
	}
	if !dirExists(t, svc.SpacePath("p-1")) || !dirExists(t, svc.SpacePath("epic-pf")) {
		t.Fatal("failed member or saga torn down despite the stop")
	}

	// Retry converges: p-2 skips as archived, p-1 and the saga follow.
	fg.removeFn = nil
	out.Reset()
	if err := svc.SagaArchive(ctx, "epic-pf", SagaArchiveOptions{}); err != nil {
		t.Fatalf("SagaArchive(retry) error = %v", err)
	}
	if !strings.Contains(out.String(), "skipping member p-2: already archived at") {
		t.Fatalf("retry did not skip the archived member:\n%s", out.String())
	}
	for _, id := range []string{"p-1", "p-2", "epic-pf"} {
		if !dirExists(t, filepath.Join(archiveRoot, id)) {
			t.Fatalf("space %s missing from .archive after retry", id)
		}
	}
}

// TestSagaDestroyReportsArchivedMembers: destroy skips MISSING only; archived
// members are reported with their .archive/ paths and never auto-removed.
func TestSagaDestroyReportsArchivedMembers(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dd"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-dd", "d-1")
	createMemberInSaga(t, svc, "epic-dd", "d-2")
	if err := svc.mutateSagaManifest("epic-dd", func(m *Manifest) error {
		m.Saga.Members = append(m.Saga.Members, SagaMember{ID: "d-gone"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(ctx, ArchiveOptions{SpaceID: "d-1"}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(cfg.AgentWorkDir, ".archive", "d-1")

	var out strings.Builder
	svc.Out = &out
	if err := svc.SagaDestroy(ctx, "epic-dd", SagaDestroyOptions{}); err != nil {
		t.Fatalf("SagaDestroy() error = %v", err)
	}
	if !strings.Contains(out.String(), "member d-1 is archived at "+archivePath) {
		t.Fatalf("archived member not reported with its path:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "skipping member d-gone: missing") {
		t.Fatalf("missing member not skipped with a note:\n%s", out.String())
	}
	if !dirExists(t, archivePath) {
		t.Fatal("destroy auto-removed the member archive")
	}
	for _, id := range []string{"d-2", "epic-dd"} {
		if dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s still present after saga destroy", id)
		}
	}
}

// TestSagaDestroyDenRefcountGuard: a den-destroying fate refuses while a
// NON-member space shares the saga den; members torn down in the same
// operation do not count; Force overrides.
func TestSagaDestroyDenRefcountGuard(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	svc.Memory = &memory.Fake{}

	// A member sharing the den is destroyed in the same operation: no refusal.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-den1", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "m-sh")
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "m-sh", UseID: "epic-den1", Name: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-den1", "m-sh", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaDestroy(ctx, "epic-den1", SagaDestroyOptions{MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("SagaDestroy with member-only den sharing error = %v", err)
	}

	// An external (non-member) sharer refuses any fate != keep, named.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-den2", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "ext-den")
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "ext-den", UseID: "epic-den2", Name: "shared"}); err != nil {
		t.Fatal(err)
	}
	for _, fate := range []memory.MemoryFate{memory.FateDestroy, memory.FateContribute} {
		err := svc.SagaDestroy(ctx, "epic-den2", SagaDestroyOptions{MemoryFate: fate})
		if err == nil || !strings.Contains(err.Error(), `space "ext-den" shares the saga den "epic-den2"`) {
			t.Fatalf("SagaDestroy(fate %s, shared den) error = %v", fate, err)
		}
	}
	if !dirExists(t, svc.SpacePath("epic-den2")) {
		t.Fatal("saga torn down despite den-refcount refusal")
	}
	if err := svc.SagaDestroy(ctx, "epic-den2", SagaDestroyOptions{MemoryFate: memory.FateDestroy, Force: true}); err != nil {
		t.Fatalf("SagaDestroy(force, shared den) error = %v", err)
	}
}

// TestSagaDestroyDenFirstRefusalLeavesSagaIntact: a den-destroying saga
// destroy handles the saga den FIRST; a source_in_use refusal (live agent
// serve) fails the whole operation before ANY member teardown, with the
// members' saga-den MCP wiring stripped-then-restored. Lifting the refusal
// converges, and the final saga-space teardown never re-detaches the den
// handled den-first.
func TestSagaDestroyDenFirstRefusalLeavesSagaIntact(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	refuse := true
	fw := &fakeWirer{Fake: &memory.Fake{}}
	fw.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "epic-df" && refuse {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Destroyed: opts.Owned}, nil
	}
	svc.Memory = fw
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-df", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-df", "df-1")
	createMemberInSaga(t, svc, "epic-df", "df-2", "df-1")

	fw.mu.Lock()
	fw.wrote, fw.removed = nil, nil // drop creation-time wiring records
	fw.mu.Unlock()
	fg.calls = nil
	var out strings.Builder
	svc.Out = &out

	err := svc.SagaDestroy(ctx, "epic-df", SagaDestroyOptions{MemoryFate: memory.FateDestroy})
	var inUse *MemoryInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("SagaDestroy(refused den) error = %v, want MemoryInUseError", err)
	}
	if !strings.Contains(out.String(), "saga destroy stopped at the saga den; the saga record is intact") {
		t.Fatalf("failure report must name the saga den:\n%s", out.String())
	}
	// The refusal precedes ALL member teardown: no worktree removals, every
	// member and the saga space still on disk, roster and attachment intact.
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("member teardown ran despite the den refusal: %#v", fg.calls)
	}
	for _, id := range []string{"df-1", "df-2", "epic-df"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s torn down despite the den refusal", id)
		}
	}
	if members := loadSagaMembers(t, svc, "epic-df"); len(members) != 2 {
		t.Fatalf("roster mutated by the refused destroy: %#v", members)
	}
	manifest, loadErr := LoadManifest(svc.SpacePath("epic-df"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("den attachment spliced despite the refusal: %#v", manifest.Memories)
	}
	if n := countDetachCalls(fw.Fake, "epic-df"); n != 1 {
		t.Fatalf("den detach calls = %d, want exactly 1 (the refusal): %#v", n, fw.Calls)
	}
	// The den survived: each wired member was stripped, then restored.
	fw.mu.Lock()
	removed := append([]string(nil), fw.removed...)
	wrote := append([]string(nil), fw.wrote...)
	fw.mu.Unlock()
	for _, id := range []string{"df-1", "df-2"} {
		path := svc.SpacePath(id)
		if !containsString(removed, path) {
			t.Fatalf("member %s wiring not stripped before the den destroy: %#v", id, removed)
		}
		if !containsString(wrote, path+"|epic-df") {
			t.Fatalf("member %s wiring not restored after the refusal: %#v", id, wrote)
		}
	}

	// Lift the refusal: the retry converges, and the den is detached exactly
	// once more — the final saga-space teardown skips the den handled first.
	refuse = false
	if err := svc.SagaDestroy(ctx, "epic-df", SagaDestroyOptions{MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("SagaDestroy(retry) error = %v", err)
	}
	for _, id := range []string{"df-1", "df-2", "epic-df"} {
		if dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s still present after the converged destroy", id)
		}
	}
	if n := countDetachCalls(fw.Fake, "epic-df"); n != 2 {
		t.Fatalf("den detach calls across runs = %d, want 2 (one refusal + one destroy; never a third from the saga-space teardown): %#v", n, fw.Calls)
	}
}

func containsString(haystack []string, want string) bool {
	for _, s := range haystack {
		if s == want {
			return true
		}
	}
	return false
}

// TestSagaDestroyForceStillFailsFast: --force skips stave's own guards but
// marmot's refusal is NOT force-bypassable — the den-first destroy still
// fails the whole operation before any member teardown.
func TestSagaDestroyForceStillFailsFast(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	fake := &memory.Fake{}
	fake.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.Fate == memory.FateDestroy || opts.Fate == memory.FateContribute {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Kept: true}, nil
	}
	svc.Memory = fake
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ff", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-ff", "ff-1")

	fg.calls = nil
	err := svc.SagaDestroy(ctx, "epic-ff", SagaDestroyOptions{MemoryFate: memory.FateDestroy, Force: true})
	var inUse *MemoryInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("SagaDestroy(force, refused den) error = %v, want MemoryInUseError", err)
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("--force must not reach member teardown past the refusal: %#v", fg.calls)
	}
	for _, id := range []string{"ff-1", "epic-ff"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s torn down despite the refusal under --force", id)
		}
	}
}

// TestSagaDestroyDenFirstStripsMemberWiringBeforeDetach pins the strip order
// via the fake's call log: the member's saga-den MCP wiring is removed BEFORE
// the den Detach is issued (a member client started from a stale config could
// otherwise re-acquire the den mid-destroy).
func TestSagaDestroyDenFirstStripsMemberWiringBeforeDetach(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-so", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-so", "so-1")

	fake.Mu.Lock()
	fake.Calls = nil
	fake.Mu.Unlock()
	if err := svc.SagaDestroy(ctx, "epic-so", SagaDestroyOptions{MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("SagaDestroy() error = %v", err)
	}
	fake.Mu.Lock()
	calls := append([]string(nil), fake.Calls...)
	fake.Mu.Unlock()
	stripIdx, detachIdx := -1, -1
	for i, call := range calls {
		if stripIdx == -1 && strings.Contains(call, "remove-mcp-config") && strings.Contains(call, svc.SpacePath("so-1")) {
			stripIdx = i
		}
		if detachIdx == -1 && strings.Contains(call, "detach epic-so") {
			detachIdx = i
		}
	}
	if stripIdx == -1 || detachIdx == -1 || stripIdx > detachIdx {
		t.Fatalf("member wiring must be stripped before the den detach (strip=%d detach=%d):\n%#v", stripIdx, detachIdx, calls)
	}
}

// TestDetachMemoryDestroySagaDenStripsMemberWiring: `stave memory detach
// <saga> --destroy` — the own-den destroy path outside the saga walk — strips
// the same member wiring, restores it on refusal, and never touches members
// holding their own attachment.
func TestDetachMemoryDestroySagaDenStripsMemberWiring(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	refuse := true
	fw := &fakeWirer{Fake: &memory.Fake{}}
	fw.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "epic-dd2" && refuse {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Destroyed: opts.Owned}, nil
	}
	svc.Memory = fw
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dd2", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-dd2", "dm-1")
	// A member with its OWN attachment keeps its wiring untouched throughout.
	mustInitSpace(t, svc, "dm-own")
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "dm-own", UseID: "own-store", Name: "own"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-dd2", "dm-own", nil, false); err != nil {
		t.Fatal(err)
	}

	// Refused: wiring stripped then restored; the attachment record survives.
	fw.mu.Lock()
	fw.wrote, fw.removed = nil, nil
	fw.mu.Unlock()
	err := svc.DetachMemory(ctx, "epic-dd2", "", memory.FateDestroy, false, false)
	var inUse *MemoryInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("DetachMemory(refused den destroy) error = %v, want MemoryInUseError", err)
	}
	fw.mu.Lock()
	removed := append([]string(nil), fw.removed...)
	wrote := append([]string(nil), fw.wrote...)
	fw.mu.Unlock()
	if len(removed) != 1 || removed[0] != svc.SpacePath("dm-1") {
		t.Fatalf("strip calls = %#v, want exactly dm-1 (dm-own keeps its own wiring)", removed)
	}
	if len(wrote) != 1 || wrote[0] != svc.SpacePath("dm-1")+"|epic-dd2" {
		t.Fatalf("restore calls = %#v, want exactly dm-1 rewired at the saga den", wrote)
	}
	manifest, loadErr := LoadManifest(svc.SpacePath("epic-dd2"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("attachment spliced despite the refusal: %#v", manifest.Memories)
	}

	// Lifted: stripped again, no restore, record spliced.
	refuse = false
	fw.mu.Lock()
	fw.wrote, fw.removed = nil, nil
	fw.mu.Unlock()
	if err := svc.DetachMemory(ctx, "epic-dd2", "", memory.FateDestroy, false, false); err != nil {
		t.Fatalf("DetachMemory(retry) error = %v", err)
	}
	fw.mu.Lock()
	removed = append([]string(nil), fw.removed...)
	wrote = append([]string(nil), fw.wrote...)
	fw.mu.Unlock()
	if len(removed) != 1 || removed[0] != svc.SpacePath("dm-1") {
		t.Fatalf("retry strip calls = %#v", removed)
	}
	if len(wrote) != 0 {
		t.Fatalf("successful destroy must not rewire members: %#v", wrote)
	}
	manifest, loadErr = LoadManifest(svc.SpacePath("epic-dd2"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("attachment record must be spliced: %#v", manifest.Memories)
	}
}

// TestDetachMemoryDestroySagaDenRestoreScopedToNotIssuedFailures pins WHICH
// failures re-wire the stripped members. The restore is authorized by
// memory.DenIntactAfterFailedDestroy, and the interesting cases are the ones
// that carry no den-intact refusal code: the provider now wraps every error it
// raises BEFORE issuing `den destroy` in *memory.DestroyNotIssuedError, so the
// contribute/propose-stage refusal (edit_link_required — a plain refusal that
// used to fall outside the intact set and wrongly leave members unwired) and
// the pre-destroy space-wiring strip failure (a plain non-refusal error) both
// restore. The contrast case is the whole point of the scoping: an ambiguous
// failure with NO wrapper — marmot destroys the den before serializing its
// envelope, so a decode error may follow a COMPLETED destroy — must leave the
// members unwired rather than point them at a possibly-destroyed den.
func TestDetachMemoryDestroySagaDenRestoreScopedToNotIssuedFailures(t *testing.T) {
	cases := []struct {
		name    string
		detach  error
		restore bool
	}{
		{
			name:    "contribute stage refusal",
			detach:  &memory.DestroyNotIssuedError{Err: &memory.RefusalError{Provider: "fake", Code: "edit_link_required", Message: "den has no edit link; nothing to contribute"}},
			restore: true,
		},
		{
			name:    "pre-destroy space wiring strip failure",
			detach:  &memory.DestroyNotIssuedError{Err: fmt.Errorf("space MCP wiring update failed; destroy of den %s not issued (a client could re-acquire it mid-teardown): %w", "epic-ni", errors.New("permission denied"))},
			restore: true,
		},
		{
			name:    "ambiguous decode failure",
			detach:  errors.New("den destroy: decoding envelope: unexpected end of JSON input"),
			restore: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := testService(t)
			ctx := context.Background()
			fw := &fakeWirer{Fake: &memory.Fake{}}
			fw.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
				if opts.StoreID == "epic-ni" {
					return memory.DetachResult{}, tc.detach
				}
				return memory.DetachResult{}, nil
			}
			svc.Memory = fw
			if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ni", Memories: []string{"."}}); err != nil {
				t.Fatal(err)
			}
			createMemberInSaga(t, svc, "epic-ni", "ni-1")

			fw.mu.Lock()
			fw.wrote, fw.removed = nil, nil // drop creation-time wiring records
			fw.mu.Unlock()
			err := svc.DetachMemory(ctx, "epic-ni", "", memory.FateDestroy, false, false)
			if err == nil {
				t.Fatal("DetachMemory(failing den destroy) error = nil")
			}
			fw.mu.Lock()
			removed := append([]string(nil), fw.removed...)
			wrote := append([]string(nil), fw.wrote...)
			fw.mu.Unlock()
			// The strip is unconditional: it must precede every destroy attempt.
			if len(removed) != 1 || removed[0] != svc.SpacePath("ni-1") {
				t.Fatalf("strip calls = %#v, want exactly ni-1", removed)
			}
			if tc.restore {
				if len(wrote) != 1 || wrote[0] != svc.SpacePath("ni-1")+"|epic-ni" {
					t.Fatalf("restore calls = %#v, want ni-1 re-wired at the surviving den (%v)", wrote, err)
				}
			} else if len(wrote) != 0 {
				t.Fatalf("restore calls = %#v, want none: the den may already be destroyed", wrote)
			}
			// Either way the attachment record survives the failure, so a retry
			// still finds the den to finish (or re-attempt) the destroy.
			manifest, loadErr := LoadManifest(svc.SpacePath("epic-ni"))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(manifest.Memories) != 1 {
				t.Fatalf("attachment spliced despite the failed destroy: %#v", manifest.Memories)
			}
		})
	}
}

// TestDestroySagaDirectStripsMemberWiringBeforeDetach covers the OTHER entry
// into a saga den's destroying detach: `stave space destroy <saga-id> --force
// --memory destroy` bypasses the saga walk entirely (Destroy → applyMemoryFate
// → detachDestroyingAndRecord), so it owes members the same pre-destroy strip
// the den-first walk performs — otherwise a member client started from a stale
// MCP config could re-acquire the den mid-destroy. The fake's ordered call log
// is the only place that distinction is observable: strip BEFORE detach.
func TestDestroySagaDirectStripsMemberWiringBeforeDetach(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	refuse := true
	fake := &memory.Fake{}
	fake.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "epic-direct" && refuse {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeSourceInUse, Message: "den busy"}
		}
		return memory.DetachResult{Destroyed: opts.Owned}, nil
	}
	svc.Memory = fake
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-direct", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	// sd-1 holds no attachment of its own: exactly the set wired at the saga
	// den, and so exactly the set the strip targets. sd-own's configs belong to
	// its OWN den and must be left alone in both phases.
	createMemberInSaga(t, svc, "epic-direct", "sd-1")
	mustInitSpace(t, svc, "sd-own")
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "sd-own", UseID: "own-store", Name: "own"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-direct", "sd-own", nil, false); err != nil {
		t.Fatal(err)
	}

	// Refused: stripped before the detach, restored after it (source_in_use is
	// a pre-destroy lease hold, so the den is known intact).
	fake.Mu.Lock()
	fake.Calls = nil // drop creation-time wiring records
	fake.Mu.Unlock()
	err := svc.Destroy(ctx, DestroyOptions{SpaceID: "epic-direct", Force: true, MemoryFate: memory.FateDestroy})
	var inUse *MemoryInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("Destroy(saga, refused den) error = %v, want MemoryInUseError", err)
	}
	stripIdx, detachIdx, restoreIdx := memberWiringCallOrder(t, fake, svc.SpacePath("sd-1"), "detach epic-direct")
	if stripIdx == -1 || detachIdx == -1 || stripIdx > detachIdx {
		t.Fatalf("member wiring must be stripped before the den detach (strip=%d detach=%d):\n%#v", stripIdx, detachIdx, fake.Calls)
	}
	if restoreIdx == -1 || restoreIdx < detachIdx {
		t.Fatalf("member wiring must be restored after the refusal (restore=%d detach=%d):\n%#v", restoreIdx, detachIdx, fake.Calls)
	}
	if idx, _, _ := memberWiringCallOrder(t, fake, svc.SpacePath("sd-own"), "detach epic-direct"); idx != -1 {
		t.Fatalf("member holding its own attachment was stripped at %d:\n%#v", idx, fake.Calls)
	}
	if !dirExists(t, svc.SpacePath("epic-direct")) {
		t.Fatal("saga space torn down despite the den refusal")
	}
	manifest, loadErr := LoadManifest(svc.SpacePath("epic-direct"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("den attachment spliced despite the refusal: %#v", manifest.Memories)
	}

	// Lifted: stripped again, and NOT restored — the den is gone, so members
	// must stay unwired.
	refuse = false
	fake.Mu.Lock()
	fake.Calls = nil
	fake.Mu.Unlock()
	if err := svc.Destroy(ctx, DestroyOptions{SpaceID: "epic-direct", Force: true, MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("Destroy(retry) error = %v", err)
	}
	stripIdx, detachIdx, restoreIdx = memberWiringCallOrder(t, fake, svc.SpacePath("sd-1"), "detach epic-direct")
	if stripIdx == -1 || detachIdx == -1 || stripIdx > detachIdx {
		t.Fatalf("retry strip order wrong (strip=%d detach=%d):\n%#v", stripIdx, detachIdx, fake.Calls)
	}
	if restoreIdx != -1 {
		t.Fatalf("successful destroy must not re-wire members:\n%#v", fake.Calls)
	}
	if dirExists(t, svc.SpacePath("epic-direct")) {
		t.Fatal("saga space still present after the converged destroy")
	}
}

// memberWiringCallOrder returns the indexes of the first MCP strip and re-wire
// recorded for memberPath and of the first call matching detachMatch, in the
// fake's single ordered call log (-1 when absent).
func memberWiringCallOrder(t *testing.T, fake *memory.Fake, memberPath, detachMatch string) (strip, detach, restore int) {
	t.Helper()
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	strip, detach, restore = -1, -1, -1
	for i, call := range fake.Calls {
		switch {
		case strip == -1 && strings.Contains(call, "remove-mcp-config") && strings.Contains(call, memberPath):
			strip = i
		case restore == -1 && strings.Contains(call, "write-mcp-config") && strings.Contains(call, memberPath):
			restore = i
		case detach == -1 && strings.Contains(call, detachMatch):
			detach = i
		}
	}
	return strip, detach, restore
}

// TestSagaDestroyDenAlreadyGoneConverges: the crash window in the den-first
// path — the saga manifest names a den that no longer exists (a previous run
// destroyed it but died before the manifest save). The den_not_found refusal
// is tolerated as already-done, the record spliced, and the whole saga
// teardown converges instead of wedging on every retry.
func TestSagaDestroyDenAlreadyGoneConverges(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	fake := &memory.Fake{}
	fake.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.StoreID == "epic-gone" && opts.Fate == memory.FateDestroy {
			return memory.DetachResult{}, &memory.RefusalError{Provider: "fake", Code: memory.CodeDenNotFound, Message: "den \"epic-gone\" not found"}
		}
		return memory.DetachResult{Kept: true}, nil
	}
	svc.Memory = fake
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-gone", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-gone", "gone-1")

	var out strings.Builder
	svc.Out = &out
	if err := svc.SagaDestroy(ctx, "epic-gone", SagaDestroyOptions{MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("SagaDestroy over a vanished den must converge: %v", err)
	}
	if !strings.Contains(out.String(), "den not found") {
		t.Fatalf("expected den_not_found notice:\n%s", out.String())
	}
	for _, id := range []string{"gone-1", "epic-gone"} {
		if dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("space %s still present after the converged destroy", id)
		}
	}
	if n := countDetachCalls(fake, "epic-gone"); n != 1 {
		t.Fatalf("den detach calls = %d, want exactly 1 (the tolerated den_not_found): %#v", n, fake.Calls)
	}
}

// TestSagaDestroyDryRunDenFirstPlan: for a destroying fate the dry-run plan
// prints the den-destroy step FIRST (members renumbered, saga space last),
// previews exactly one set of provider destroy lines for the owned den (the
// final saga-space preview skips it), still previews keep-fate handling for
// additional unowned attachments, and mutates nothing.
func TestSagaDestroyDryRunDenFirstPlan(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	fake := &memory.Fake{}
	fake.DetachFn = func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		if opts.DryRun && (opts.Fate == memory.FateDestroy || opts.Fate == memory.FateContribute) {
			fmt.Fprintf(opts.Out, "dry-run: marmot den destroy %s\n", opts.StoreID)
		}
		return memory.DetachResult{}, nil
	}
	svc.Memory = fake
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-drm", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	// Additional unowned attachments the saga legally holds.
	for _, name := range []string{"shared-a", "shared-b"} {
		if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "epic-drm", UseID: name, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	createMemberInSaga(t, svc, "epic-drm", "dr-1")

	var out strings.Builder
	svc.Out = &out
	fg.calls = nil
	if err := svc.SagaDestroy(ctx, "epic-drm", SagaDestroyOptions{DryRun: true, MemoryFate: memory.FateDestroy}); err != nil {
		t.Fatalf("SagaDestroy(dry-run) error = %v", err)
	}
	text := out.String()
	for _, line := range []string{
		"dry-run: saga destroy plan for epic-drm",
		"dry-run: 1. destroy the saga den epic-drm first (fate destroy; a live agent serve refuses here with nothing destroyed)",
		"dry-run: 2. destroy member dr-1",
		"dry-run: 3. destroy saga space epic-drm",
	} {
		if !strings.Contains(text, line) {
			t.Fatalf("dry-run plan missing %q:\n%s", line, text)
		}
	}
	// The den's real destroy argv previews right after the numbered plan, and
	// exactly once — the final saga-space preview must not repeat it.
	planEnd := strings.Index(text, "dry-run: 3. destroy saga space epic-drm")
	denLine := strings.Index(text, "dry-run: marmot den destroy epic-drm")
	if denLine == -1 || denLine < planEnd {
		t.Fatalf("den destroy preview must follow the numbered plan (plan end %d, den line %d):\n%s", planEnd, denLine, text)
	}
	if n := strings.Count(text, "dry-run: marmot den destroy epic-drm"); n != 1 {
		t.Fatalf("owned den destroy previewed %d times, want exactly 1:\n%s", n, text)
	}
	// Unowned attachments keep their keep-fate preview (never skipped, never
	// destroyed).
	for _, name := range []string{"shared-a", "shared-b"} {
		if !strings.Contains(text, fmt.Sprintf("notice: memory %q (%s) is not owned; detach-only (never destroy)", name, name)) {
			t.Fatalf("unowned attachment %s lost its keep-fate preview:\n%s", name, text)
		}
	}
	// Nothing mutated: spaces intact, roster intact, all attachments recorded.
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("dry-run removed worktrees: %#v", fg.calls)
	}
	for _, id := range []string{"dr-1", "epic-drm"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("dry-run tore down %s", id)
		}
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-drm"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 3 {
		t.Fatalf("dry-run spliced attachments: %#v", manifest.Memories)
	}
}

// TestSagaDestroyDryRunPlan: dry-run prints the ordered plan (reverse topo,
// saga last, skip/report annotations) and mutates nothing.
func TestSagaDestroyDryRunPlan(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dr"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-dr", "r-1")
	createMemberInSaga(t, svc, "epic-dr", "r-2", "r-1")
	if err := svc.mutateSagaManifest("epic-dr", func(m *Manifest) error {
		m.Saga.Members = append(m.Saga.Members, SagaMember{ID: "r-gone"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	fg.calls = nil
	if err := svc.SagaDestroy(ctx, "epic-dr", SagaDestroyOptions{DryRun: true}); err != nil {
		t.Fatalf("SagaDestroy(dry-run) error = %v", err)
	}
	// Reverse topo of [r-1, r-2(after r-1), r-gone]: r-gone, r-2, r-1, saga.
	for _, line := range []string{
		"dry-run: saga destroy plan for epic-dr",
		"dry-run: 1. skip member r-gone: missing",
		"dry-run: 2. destroy member r-2",
		"dry-run: 3. destroy member r-1",
		"dry-run: 4. destroy saga space epic-dr",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("dry-run plan missing %q:\n%s", line, out.String())
		}
	}
	if callIndex(fg.calls, "remove|") != -1 {
		t.Fatalf("dry-run removed worktrees: %#v", fg.calls)
	}
	for _, id := range []string{"r-1", "r-2", "epic-dr"} {
		if !dirExists(t, svc.SpacePath(id)) {
			t.Fatalf("dry-run tore down %s", id)
		}
	}
	if members := loadSagaMembers(t, svc, "epic-dr"); len(members) != 3 {
		t.Fatalf("dry-run mutated the roster: %#v", members)
	}
}
