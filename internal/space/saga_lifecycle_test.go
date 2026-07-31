package space

import (
	"context"
	"errors"
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
