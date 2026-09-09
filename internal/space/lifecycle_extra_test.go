package space

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/memory"
)

// lifecycleSpace builds a live space with one edit repo (repo-a) and one
// reference repo (repo-b) through the real AddRepo path on the fake git.
func lifecycleSpace(t *testing.T, svc Service, id string) string {
	t.Helper()
	ctx := context.Background()
	if err := svc.InitSpace(ctx, InitOptions{ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: id, RepoName: "repo-a", Mode: ModeEdit, NoFetch: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: id, RepoName: "repo-b", Mode: ModeReference, NoFetch: true}); err != nil {
		t.Fatal(err)
	}
	return svc.SpacePath(id)
}

func loadRepoNames(t *testing.T, spacePath string) []string {
	t.Helper()
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, repo := range manifest.Repos {
		names = append(names, repo.Name)
	}
	return names
}

func TestRemoveRepoEditClean(t *testing.T) {
	svc, fg, cfg := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	spacePath := lifecycleSpace(t, svc, "rm-1")
	fg.calls = nil

	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-1", RepoName: "repo-a"}); err != nil {
		t.Fatal(err)
	}
	bare := cfg.Repos["repo-a"].BareRepoPath
	if !containsCall(fg.calls, "remove|"+bare+"|"+filepath.Join(spacePath, "repo-a")) {
		t.Fatalf("worktree remove not issued: %v", fg.calls)
	}
	if !containsCall(fg.calls, "prune|"+bare) {
		t.Fatalf("worktree prune not issued: %v", fg.calls)
	}
	if got := loadRepoNames(t, spacePath); !reflect.DeepEqual(got, []string{"repo-b"}) {
		t.Fatalf("manifest repos after remove = %v", got)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agents), "`repo-a`") {
		t.Fatalf("AGENTS.md still advertises repo-a:\n%s", agents)
	}
	if !strings.Contains(out.String(), "removed repo-a from rm-1") {
		t.Fatalf("output = %q", out.String())
	}
	// Stave never deletes branches: no fake verb exists for it, so the call
	// log must contain nothing but the worktree remove/prune pair.
	for _, call := range fg.calls {
		if !strings.HasPrefix(call, "remove|") && !strings.HasPrefix(call, "prune|") && !strings.HasPrefix(call, "dirty|") {
			t.Fatalf("unexpected git call during remove: %s", call)
		}
	}
}

func TestRemoveRepoDirtyRefusesUnlessForce(t *testing.T) {
	svc, fg, _ := testService(t)
	spacePath := lifecycleSpace(t, svc, "rm-2")
	fg.dirty = map[string]bool{filepath.Join(spacePath, "repo-a"): true}

	err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-2", RepoName: "repo-a"})
	var dirty *DirtyWorktreeError
	if !errors.As(err, &dirty) || !reflect.DeepEqual(dirty.Repos, []string{"repo-a"}) {
		t.Fatalf("expected DirtyWorktreeError for repo-a, got %v", err)
	}
	if got := loadRepoNames(t, spacePath); len(got) != 2 {
		t.Fatalf("manifest changed on refusal: %v", got)
	}
	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-2", RepoName: "repo-a", Force: true}); err != nil {
		t.Fatalf("--force remove: %v", err)
	}
	if got := loadRepoNames(t, spacePath); !reflect.DeepEqual(got, []string{"repo-b"}) {
		t.Fatalf("manifest repos after forced remove = %v", got)
	}
}

func TestRemoveRepoDependentStackRefusesUnlessForce(t *testing.T) {
	svc, fg, _ := testService(t)
	spacePath := lifecycleSpace(t, svc, "rm-3")
	// A sibling stacks on rm-3's repo-a branch via the space: sugar spelling
	// (the sugar path verifies the base branch exists in the bare repo).
	fg.branchExists = true
	ctx := context.Background()
	if err := svc.InitSpace(ctx, InitOptions{ID: "rm-3-next"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "rm-3-next", RepoName: "repo-a", Mode: ModeEdit, Base: "space:rm-3", NoFetch: true, DryRun: false}); err != nil {
		t.Fatal(err)
	}
	err := svc.RemoveRepo(ctx, RemoveOptions{SpaceID: "rm-3", RepoName: "repo-a"})
	if err == nil || !strings.Contains(err.Error(), "stacks on branch") {
		t.Fatalf("expected dependent-stack refusal, got %v", err)
	}
	// The reference repo has no branch, so nothing stacks on it: removable.
	if err := svc.RemoveRepo(ctx, RemoveOptions{SpaceID: "rm-3", RepoName: "repo-b"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveRepo(ctx, RemoveOptions{SpaceID: "rm-3", RepoName: "repo-a", Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := loadRepoNames(t, spacePath); len(got) != 0 {
		t.Fatalf("manifest repos = %v", got)
	}
}

func TestRemoveRepoReferenceAndMemoryNotice(t *testing.T) {
	svc, fg, cfg := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	svc.Memory = &memory.Fake{}
	spacePath := lifecycleSpace(t, svc, "rm-4")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Memories = []MemoryManifest{{Name: "den", Provider: "fake", ID: "den-1", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	out.Reset()
	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-4", RepoName: "repo-b"}); err != nil {
		t.Fatal(err)
	}
	if !containsCall(fg.calls, "remove|"+cfg.Repos["repo-b"].BareRepoPath+"|"+filepath.Join(spacePath, "references", "repo-b")) {
		t.Fatalf("reference worktree remove not issued: %v", fg.calls)
	}
	if got := loadRepoNames(t, spacePath); !reflect.DeepEqual(got, []string{"repo-a"}) {
		t.Fatalf("manifest repos = %v", got)
	}
	if !strings.Contains(out.String(), "memory link for reference repo-b is left in place") {
		t.Fatalf("expected memory-link notice, got %q", out.String())
	}
}

func TestRemoveRepoUnknownAndNotLive(t *testing.T) {
	svc, _, _ := testService(t)
	lifecycleSpace(t, svc, "rm-5")
	err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-5", RepoName: "repo-zz"})
	var notIn *RepoNotInSpaceError
	if !errors.As(err, &notIn) || notIn.SpaceID != "rm-5" || notIn.Repo != "repo-zz" {
		t.Fatalf("expected RepoNotInSpaceError, got %v", err)
	}
	err = svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "ghost", RepoName: "repo-a"})
	if err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("expected not-live error, got %v", err)
	}
	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "../x", RepoName: "repo-a"}); err == nil {
		t.Fatal("traversal id accepted")
	}
}

func TestRemoveRepoDryRunChangesNothing(t *testing.T) {
	svc, fg, _ := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	spacePath := lifecycleSpace(t, svc, "rm-6")
	before, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "rm-6", RepoName: "repo-a", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dry-run rewrote the manifest")
	}
	if containsCallPrefix(fg.calls, "remove|") || containsCallPrefix(fg.calls, "prune|") {
		t.Fatalf("dry-run issued mutating git calls: %v", fg.calls)
	}
	for _, want := range []string{"dry-run: remove worktree", "dry-run: drop repo-a from", "stave never deletes branches"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

// archivedLifecycleSpace builds and archives a two-repo space, returning the
// archived path and the pre-archive manifest bytes.
func archivedLifecycleSpace(t *testing.T, svc Service, id string) (string, []byte) {
	t.Helper()
	spacePath := lifecycleSpace(t, svc, id)
	before, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: id}); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(svc.Config.AgentWorkDir, ".archive", id)
	if !dirExists(t, archived) {
		t.Fatalf("archive missing at %s", archived)
	}
	return archived, before
}

func TestRestoreRoundTrip(t *testing.T) {
	svc, fg, cfg := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	fg.branchExists = true
	archived, before := archivedLifecycleSpace(t, svc, "rs-1")
	spacePath := svc.SpacePath("rs-1")
	fg.calls = nil
	out.Reset()

	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-1"}); err != nil {
		t.Fatal(err)
	}
	if dirExists(t, archived) {
		t.Fatal("archive directory still present after restore")
	}
	if !dirExists(t, spacePath) {
		t.Fatal("space directory not restored")
	}
	if !containsCall(fg.calls, "add-existing|"+cfg.Repos["repo-a"].BareRepoPath+"|"+filepath.Join(spacePath, "repo-a")+"|"+DefaultBranch("rs-1", "repo-a")) {
		t.Fatalf("edit worktree not re-created at recorded branch: %v", fg.calls)
	}
	if !containsCall(fg.calls, "add-detached|"+cfg.Repos["repo-b"].BareRepoPath+"|"+filepath.Join(spacePath, "references", "repo-b")+"|origin/trunk") {
		t.Fatalf("reference worktree not re-created at recorded ref: %v", fg.calls)
	}
	if containsCallPrefix(fg.calls, "add-branch|") {
		t.Fatalf("restore must never mint a new branch: %v", fg.calls)
	}
	after, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("manifest changed across archive/restore:\n%s\n---\n%s", before, after)
	}
	status, err := svc.Status(context.Background(), "rs-1")
	if err != nil || len(status.Repos) != 2 {
		t.Fatalf("status after restore = %#v, %v", status, err)
	}
	for _, want := range []string{"restored edit repo-a", "restored reference repo-b", "restored rs-1 -> " + spacePath} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "note:") {
		t.Fatalf("plain space must not print a saga note:\n%s", out.String())
	}
}

func TestRestoreTimestampedCandidates(t *testing.T) {
	svc, fg, _ := testService(t)
	fg.branchExists = true
	archived, _ := archivedLifecycleSpace(t, svc, "rs-2")
	archiveRoot := filepath.Dir(archived)
	one := filepath.Join(archiveRoot, "rs-2-20260101000000")
	if err := os.Rename(archived, one); err != nil {
		t.Fatal(err)
	}
	// A sibling whose name merely shares the prefix must never be a candidate.
	if err := os.MkdirAll(filepath.Join(archiveRoot, "rs-2-web"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := svc.Restore(ctx, RestoreOptions{SpaceID: "rs-2", DryRun: true}); err != nil {
		t.Fatalf("single timestamped candidate must auto-pick: %v", err)
	}

	two := filepath.Join(archiveRoot, "rs-2-20260202000000")
	if err := os.MkdirAll(two, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(one)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(two, manifest); err != nil {
		t.Fatal(err)
	}
	err = svc.Restore(ctx, RestoreOptions{SpaceID: "rs-2"})
	var ambiguous *AmbiguousArchiveError
	if !errors.As(err, &ambiguous) || !reflect.DeepEqual(ambiguous.Candidates, []string{"rs-2-20260101000000", "rs-2-20260202000000"}) {
		t.Fatalf("expected AmbiguousArchiveError listing both, got %v", err)
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Fatalf("ambiguity error must point at --from: %v", err)
	}
	if !dirExists(t, one) || !dirExists(t, two) {
		t.Fatal("ambiguity refusal must leave archives intact")
	}

	if err := svc.Restore(ctx, RestoreOptions{SpaceID: "rs-2", From: "rs-2-20260202000000"}); err != nil {
		t.Fatalf("--from restore: %v", err)
	}
	if dirExists(t, two) || !dirExists(t, one) || !dirExists(t, svc.SpacePath("rs-2")) {
		t.Fatal("--from must move exactly the named archive")
	}
	// --from rejects traversal and unknown names.
	if err := svc.Restore(ctx, RestoreOptions{SpaceID: "rs-9", From: "../rs-2"}); err == nil || !strings.Contains(err.Error(), "archive name") {
		t.Fatalf("traversal --from accepted: %v", err)
	}
	if err := svc.Restore(ctx, RestoreOptions{SpaceID: "rs-9", From: "nope"}); err == nil {
		t.Fatal("unknown --from accepted")
	}
	// --from must hold the requested space.
	if err := svc.Restore(ctx, RestoreOptions{SpaceID: "rs-9", From: "rs-2-20260101000000"}); err == nil || !strings.Contains(err.Error(), "holds space") {
		t.Fatalf("mismatched --from accepted: %v", err)
	}
}

func TestRestoreRefusesLiveSpaceAndMissingArchive(t *testing.T) {
	svc, fg, _ := testService(t)
	fg.branchExists = true
	lifecycleSpace(t, svc, "rs-3")
	err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-3"})
	var exists *SpaceExistsError
	if !errors.As(err, &exists) || exists.SpaceID != "rs-3" {
		t.Fatalf("expected SpaceExistsError, got %v", err)
	}
	err = svc.Restore(context.Background(), RestoreOptions{SpaceID: "never-archived"})
	var notFound *ArchiveNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ArchiveNotFoundError, got %v", err)
	}
	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "../x"}); err == nil {
		t.Fatal("traversal id accepted")
	}
}

func TestRestoreMissingBranchRefusesBeforeMutation(t *testing.T) {
	svc, fg, _ := testService(t)
	fg.branchExists = true
	archived, before := archivedLifecycleSpace(t, svc, "rs-4")
	fg.branchExists = false
	fg.calls = nil

	err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-4"})
	var missing *BranchMissingError
	if !errors.As(err, &missing) || missing.Repo != "repo-a" || missing.Branch != DefaultBranch("rs-4", "repo-a") {
		t.Fatalf("expected BranchMissingError for repo-a, got %v", err)
	}
	if !dirExists(t, archived) || dirExists(t, svc.SpacePath("rs-4")) {
		t.Fatal("refusal must leave the archive in place and the space absent")
	}
	after, err := os.ReadFile(filepath.Join(archived, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("archived manifest changed on refusal")
	}
	if containsCallPrefix(fg.calls, "add-") {
		t.Fatalf("refusal issued worktree adds: %v", fg.calls)
	}
}

func TestRestoreMissingRefSkipsReferenceWithWarning(t *testing.T) {
	svc, fg, _ := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	fg.branchExists = true
	archivedLifecycleSpace(t, svc, "rs-5")
	fg.refMissing = true
	fg.calls = nil
	out.Reset()

	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-5"}); err != nil {
		t.Fatal(err)
	}
	if containsCallPrefix(fg.calls, "add-detached|") {
		t.Fatalf("missing ref must skip the reference worktree: %v", fg.calls)
	}
	if !containsCallPrefix(fg.calls, "add-existing|") {
		t.Fatalf("edit worktree must still restore: %v", fg.calls)
	}
	if !strings.Contains(out.String(), "warning: reference repo-b skipped") {
		t.Fatalf("expected skip warning:\n%s", out.String())
	}
	// The manifest keeps the reference entry so `space sync` / re-add can heal it.
	if got := loadRepoNames(t, svc.SpacePath("rs-5")); !reflect.DeepEqual(got, []string{"repo-b", "repo-a"}) {
		t.Fatalf("manifest repos = %v", got)
	}
}

func TestRestoreDryRunChangesNothing(t *testing.T) {
	svc, fg, _ := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	fg.branchExists = true
	archived, _ := archivedLifecycleSpace(t, svc, "rs-6")
	fg.calls = nil
	out.Reset()

	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-6", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, archived) || dirExists(t, svc.SpacePath("rs-6")) {
		t.Fatal("dry-run moved the archive")
	}
	if containsCallPrefix(fg.calls, "add-") {
		t.Fatalf("dry-run issued worktree adds: %v", fg.calls)
	}
	for _, want := range []string{"dry-run: restore " + archived, "dry-run: add edit worktree " + DefaultBranch("rs-6", "repo-a"), "dry-run: add reference worktree origin/trunk"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRestoreRelocatesMemoryRouteBack(t *testing.T) {
	svc, fg, _ := testService(t)
	fg.branchExists = true
	fake := &memory.Fake{}
	var detachCalls []memory.DetachOptions
	fake.DetachFn = func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		detachCalls = append(detachCalls, opts)
		return memory.DetachResult{Kept: true}, nil
	}
	svc.Memory = fake
	spacePath := lifecycleSpace(t, svc, "rs-7")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Memories = []MemoryManifest{{Name: "den", Provider: "fake", ID: "den-7", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "rs-7"}); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(svc.Config.AgentWorkDir, ".archive", "rs-7")
	resolvedArchived, err := filepath.EvalSymlinks(archived)
	if err != nil {
		t.Fatal(err)
	}
	detachCalls = nil
	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-7"}); err != nil {
		t.Fatal(err)
	}
	resolvedSpace, err := filepath.EvalSymlinks(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(detachCalls) != 1 {
		t.Fatalf("expected one route relocation, got %d", len(detachCalls))
	}
	if detachCalls[0].SpacePath != resolvedArchived || detachCalls[0].NewSpacePath != resolvedSpace || detachCalls[0].Fate != memory.FateKeep {
		t.Fatalf("route relocation = %+v", detachCalls[0])
	}
}

func TestRestorePrintsSagaNotes(t *testing.T) {
	svc, fg, _ := testService(t)
	var out bytes.Buffer
	svc.Out = &out
	fg.branchExists = true
	// A member of a live saga: restoring prints the roster-staleness note.
	lifecycleSpace(t, svc, "rs-8")
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "epic", Kind: KindSaga, Saga: &SagaManifest{Members: []SagaMember{{ID: "rs-8"}}}, viaSaga: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "rs-8", Force: true}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "rs-8"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "member of saga epic") || !strings.Contains(out.String(), "stave saga sync epic") {
		t.Fatalf("expected saga member note:\n%s", out.String())
	}
	// The saga space itself restores too, with its own note.
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "epic", Force: true}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := svc.Restore(context.Background(), RestoreOptions{SpaceID: "epic"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "epic is a saga") {
		t.Fatalf("expected saga note:\n%s", out.String())
	}
}

func TestFullRefName(t *testing.T) {
	cases := map[string]string{
		"origin/main":          "refs/remotes/origin/main",
		"refs/heads/x":         "refs/heads/x",
		"refs/remotes/o/pr/7":  "refs/remotes/o/pr/7",
		"stave/space-1/repo-a": "refs/heads/stave/space-1/repo-a",
	}
	for in, want := range cases {
		if got := fullRefName(in); got != want {
			t.Fatalf("fullRefName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddRepoRefusesDuplicateName(t *testing.T) {
	svc, _, _ := testService(t)
	lifecycleSpace(t, svc, "dup-1")

	err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "dup-1", RepoName: "repo-a", Mode: ModeEdit})
	var dup *RepoAlreadyInSpaceError
	if !errors.As(err, &dup) {
		t.Fatalf("expected RepoAlreadyInSpaceError, got %v", err)
	}
	if dup.Mode != ModeEdit || ErrorCode(err) != CodeRepoAlreadyInSpace {
		t.Fatalf("unexpected error shape: mode=%s code=%s", dup.Mode, ErrorCode(err))
	}
	if got := loadRepoNames(t, svc.SpacePath("dup-1")); len(got) != 2 {
		t.Fatalf("manifest changed on refused add: %v", got)
	}
}

func TestRemoveRepoDualModeNeedsModeSelector(t *testing.T) {
	svc, _, _ := testService(t)
	spacePath := lifecycleSpace(t, svc, "dual-1")
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "dual-1", RepoName: "repo-a", Mode: ModeReference, NoFetch: true}); err != nil {
		t.Fatal(err)
	}

	err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "dual-1", RepoName: "repo-a"})
	var ambiguous *RepoModeAmbiguousError
	if !errors.As(err, &ambiguous) || ErrorCode(err) != CodeRepoModeAmbiguous {
		t.Fatalf("expected RepoModeAmbiguousError, got %v", err)
	}
	if err := svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "dual-1", RepoName: "repo-a", Mode: ModeReference}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	var modes []RepoMode
	for _, repo := range manifest.Repos {
		if repo.Name == "repo-a" {
			modes = append(modes, repo.Mode)
		}
	}
	if !reflect.DeepEqual(modes, []RepoMode{ModeEdit}) {
		t.Fatalf("repo-a modes after removing the reference = %v", modes)
	}
	err = svc.RemoveRepo(context.Background(), RemoveOptions{SpaceID: "dual-1", RepoName: "repo-a", Mode: ModeReference})
	var missing *RepoNotInSpaceError
	if !errors.As(err, &missing) || missing.Mode != ModeReference {
		t.Fatalf("expected mode-qualified RepoNotInSpaceError, got %v", err)
	}
}
