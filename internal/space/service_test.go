package space

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
)

type fakeGit struct {
	calls        []string
	branchExists bool
	dirty        map[string]bool
	ahead        int
	behind       int
	fetchErr     error
	branchErr    error
	dirtyErr     error
	driftErr     error
	addErr       error
	detachErr    error
	removeErr    error
	pruneErr     error
	checkoutErr  error
}

func (f *fakeGit) record(parts ...string) {
	f.calls = append(f.calls, strings.Join(parts, "|"))
}

func (f *fakeGit) FetchAllPrune(ctx context.Context, bare string) error {
	f.record("fetch", bare)
	return f.fetchErr
}

func (f *fakeGit) WorktreeAddBranch(ctx context.Context, bare, path, branch, start string) error {
	f.record("add-branch", bare, path, branch, start)
	if f.addErr != nil {
		return f.addErr
	}
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeAddExisting(ctx context.Context, bare, path, branch string) error {
	f.record("add-existing", bare, path, branch)
	if f.addErr != nil {
		return f.addErr
	}
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeAddDetached(ctx context.Context, bare, path, ref string) error {
	f.record("add-detached", bare, path, ref)
	if f.detachErr != nil {
		return f.detachErr
	}
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeRemove(ctx context.Context, bare, path string, force bool) error {
	f.record("remove", bare, path)
	return f.removeErr
}

func (f *fakeGit) WorktreePrune(ctx context.Context, bare string) error {
	f.record("prune", bare)
	return f.pruneErr
}

func (f *fakeGit) CheckoutDetached(ctx context.Context, path, ref string) error {
	f.record("checkout-detached", path, ref)
	return f.checkoutErr
}

func (f *fakeGit) BranchExists(ctx context.Context, bare, branch string) (bool, error) {
	f.record("branch-exists", bare, branch)
	if f.branchErr != nil {
		return false, f.branchErr
	}
	return f.branchExists, nil
}

func (f *fakeGit) IsDirty(ctx context.Context, path string) (bool, string, error) {
	f.record("dirty", path)
	if f.dirtyErr != nil {
		return false, "", f.dirtyErr
	}
	if f.dirty != nil && f.dirty[path] {
		return true, " M file.go\n", nil
	}
	return false, "", nil
}

func (f *fakeGit) AheadBehind(ctx context.Context, path, base string) (int, int, error) {
	f.record("ahead-behind", path, base)
	if f.driftErr != nil {
		return 0, 0, f.driftErr
	}
	return f.ahead, f.behind, nil
}

func testService(t *testing.T) (Service, *fakeGit, config.Config) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"repo-a": {Name: "repo-a", URL: "file:///repo-a", BareRepoPath: filepath.Join(root, "bare-repos", "repo-a.git")},
			"repo-b": {Name: "repo-b", URL: "file:///repo-b", BareRepoPath: filepath.Join(root, "bare-repos", "repo-b.git"), DefaultBranch: "trunk"},
		},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{}
	svc := NewService(cfg, fg, nil)
	svc.Now = func() time.Time { return time.Date(2026, 5, 27, 1, 2, 3, 0, time.UTC) }
	return svc, fg, cfg
}

func TestParseRepoSpec(t *testing.T) {
	tests := map[string]RepoSpec{
		"repo-a":      {Name: "repo-a"},
		"repo-a:main": {Name: "repo-a", Ref: "main"},
	}
	for raw, want := range tests {
		got, err := ParseRepoSpec(raw)
		if err != nil {
			t.Fatalf("ParseRepoSpec(%q) error = %v", raw, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseRepoSpec(%q) = %#v", raw, got)
		}
	}
	if _, err := ParseRepoSpec("repo-a:"); err == nil {
		t.Fatal("empty ref accepted")
	}
	if _, err := ParseRepoSpec("../repo"); err == nil {
		t.Fatal("unsafe repo name accepted")
	}
}

func TestManifestFindRepoAndDirtyError(t *testing.T) {
	manifest := Manifest{Repos: []RepoManifest{
		{Name: "api", Path: "api"},
		{Name: "web", Path: "references/web"},
	}}
	repo, idx, ok := manifest.FindRepo("web")
	if !ok || idx != 1 || repo.Path != "references/web" {
		t.Fatalf("FindRepo(web) = %#v, %d, %v", repo, idx, ok)
	}
	if _, idx, ok := manifest.FindRepo("missing"); ok || idx != -1 {
		t.Fatalf("FindRepo(missing) = idx %d ok %v", idx, ok)
	}
	err := (&DirtyWorktreeError{SpaceID: "work-1", Repos: []string{"api", "web"}}).Error()
	if !strings.Contains(err, "work-1") || !strings.Contains(err, "api, web") {
		t.Fatalf("DirtyWorktreeError = %q", err)
	}
}

func TestCreateAddsEditAndReference(t *testing.T) {
	svc, fg, cfg := testService(t)
	spec := filepath.Join(t.TempDir(), "ticket.md")
	if err := os.WriteFile(spec, []byte("ticket"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := svc.Create(context.Background(), CreateOptions{
		ID:         "ex-1234",
		Kind:       "ticket",
		SpecPath:   spec,
		Edits:      []RepoSpec{{Name: "repo-a"}},
		References: []RepoSpec{{Name: "repo-b", Ref: "release"}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1234")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SpecPath != "spec" || len(manifest.Repos) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "spec", "ticket.md")); err != nil {
		t.Fatalf("spec file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatalf("edit worktree missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); err != nil {
		t.Fatalf("reference worktree missing: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "references/") {
		t.Fatalf("AGENTS.md missing reference guidance:\n%s", agents)
	}
	want := "add-branch|" + filepath.Join(cfg.BareReposDir, "repo-a.git") + "|" + filepath.Join(spacePath, "repo-a") + "|stave/ex-1234/repo-a|origin/main"
	if !containsCall(fg.calls, want) {
		t.Fatalf("missing call %q in %#v", want, fg.calls)
	}
}

func TestWriteAgentsLinksRepositoryInstructions(t *testing.T) {
	svc, _, cfg := testService(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "docs-1")
	manifest := Manifest{ID: "docs-1", CreatedAt: svc.now(), Repos: []RepoManifest{
		{Name: "repo-a", Mode: ModeEdit, Path: "repo-a"},
		{Name: "repo-b", Mode: ModeReference, Path: filepath.Join("references", "repo-b")},
		{Name: "repo-c", Mode: ModeEdit, Path: "repo-c"},
	}}
	for _, repoPath := range []string{"repo-a", filepath.Join("references", "repo-b"), "repo-c"} {
		if err := os.MkdirAll(filepath.Join(spacePath, repoPath), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, repoPath := range []string{"repo-a", filepath.Join("references", "repo-b")} {
		if err := os.WriteFile(filepath.Join(spacePath, repoPath, AgentsName), []byte("nested"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.writeAgents(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(agents)
	for _, want := range []string{"[`repo-a/AGENTS.md`](repo-a/AGENTS.md)", "[`references/repo-b/AGENTS.md`](references/repo-b/AGENTS.md)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("AGENTS.md missing nested instruction link %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "repo-c/AGENTS.md") {
		t.Fatalf("AGENTS.md linked missing repository instructions:\n%s", got)
	}
}

func TestInitCreatesIdempotentClaudeSymlink(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "claude-1"}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "claude-1")
	linkPath := filepath.Join(spacePath, ClaudeName)
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s mode = %v, want symlink", ClaudeName, info.Mode())
	}
	if target, err := os.Readlink(linkPath); err != nil || target != AgentsName {
		t.Fatalf("Readlink(%s) = %q, %v; want %q", ClaudeName, target, err, AgentsName)
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "claude-1"}); err != nil {
		t.Fatalf("InitSpace(existing) error = %v", err)
	}
	throughLink, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(throughLink), "Stave Workspace Instructions") {
		t.Fatalf("%s did not resolve current %s content:\n%s", ClaudeName, AgentsName, throughLink)
	}
}

func TestInitPreservesConflictingClaudeFile(t *testing.T) {
	svc, _, cfg := testService(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "claude-conflict")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(spacePath, ClaudeName)
	if err := os.WriteFile(linkPath, []byte("user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := svc.InitSpace(context.Background(), InitOptions{ID: "claude-conflict"})
	if err == nil || !strings.Contains(err.Error(), "is not a symlink") {
		t.Fatalf("InitSpace conflict error = %v", err)
	}
	got, readErr := os.ReadFile(linkPath)
	if readErr != nil || string(got) != "user instructions" {
		t.Fatalf("conflicting %s changed: content=%q err=%v", ClaudeName, got, readErr)
	}
	if _, manifestErr := os.Stat(filepath.Join(spacePath, ManifestName)); !os.IsNotExist(manifestErr) {
		t.Fatalf("conflicting %s still wrote a manifest: %v", ClaudeName, manifestErr)
	}
}

func TestAddRepoClaudeConflictDoesNotMutate(t *testing.T) {
	svc, fg, cfg := testService(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "legacy-claude")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "legacy-claude", CreatedAt: svc.now(), Repos: []RepoManifest{}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spacePath, ClaudeName), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "legacy-claude", RepoName: "repo-a", Mode: ModeEdit})
	if err == nil || !strings.Contains(err.Error(), "is not a symlink") {
		t.Fatalf("AddRepo conflict error = %v", err)
	}
	if len(fg.calls) != 0 {
		t.Fatalf("AddRepo mutated Git before CLAUDE conflict: %v", fg.calls)
	}
	got, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repos) != 0 {
		t.Fatalf("AddRepo mutated manifest before CLAUDE conflict: %#v", got.Repos)
	}
}

func TestSyncRefreshesRepositoryInstructionLinks(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "sync-docs", References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "sync-docs")
	nested := filepath.Join(spacePath, "references", "repo-b", AgentsName)
	if err := os.WriteFile(nested, []byte("new instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "sync-docs", ReferencesOnly: true}); err != nil {
		t.Fatal(err)
	}
	rootAgents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rootAgents), "references/repo-b/AGENTS.md") {
		t.Fatalf("sync did not add nested instruction link:\n%s", rootAgents)
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "sync-docs", ReferencesOnly: true}); err != nil {
		t.Fatal(err)
	}
	rootAgents, err = os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootAgents), "references/repo-b/AGENTS.md") {
		t.Fatalf("sync left stale nested instruction link:\n%s", rootAgents)
	}
}

func TestCreateAllowsSameRepoAsEditAndReference(t *testing.T) {
	svc, _, cfg := testService(t)

	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "ex-1234",
		Edits:      []RepoSpec{{Name: "repo-a"}},
		References: []RepoSpec{{Name: "repo-a", Ref: "main"}},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1234")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 2 {
		t.Fatalf("manifest repos = %#v", manifest.Repos)
	}
	if !manifest.HasPath("repo-a") || !manifest.HasPath(filepath.Join("references", "repo-a")) {
		t.Fatalf("manifest paths = %#v", manifest.Repos)
	}
}

func TestInitExistingManifestRewritesAgentsAndRejectsMismatchedID(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "audit-1", Kind: "audit"}); err != nil {
		t.Fatalf("InitSpace() error = %v", err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "audit-1")
	if err := os.WriteFile(filepath.Join(spacePath, AgentsName), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "audit-1", Kind: "audit"}); err != nil {
		t.Fatalf("InitSpace(existing) error = %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agents), "stale") || !strings.Contains(string(agents), "Stave Workspace") {
		t.Fatalf("AGENTS.md was not rewritten:\n%s", agents)
	}

	mismatched := svc.SpacePath("wrong-id")
	if err := os.MkdirAll(mismatched, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(mismatched, Manifest{ID: "other-id", CreatedAt: svc.now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "wrong-id"}); err == nil {
		t.Fatal("InitSpace accepted existing manifest with mismatched id")
	}
}

func TestInitCopiesSpecDirectory(t *testing.T) {
	svc, _, cfg := testService(t)
	specDir := filepath.Join(t.TempDir(), "spec-source")
	if err := os.MkdirAll(filepath.Join(specDir, "details"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "overview.md"), []byte("overview"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "details", "plan.md"), []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.InitSpace(context.Background(), InitOptions{ID: "audit-1", Kind: "audit", SpecPath: specDir}); err != nil {
		t.Fatalf("InitSpace() error = %v", err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "audit-1")
	for _, rel := range []string{"spec/overview.md", "spec/details/plan.md"} {
		if _, err := os.Stat(filepath.Join(spacePath, rel)); err != nil {
			t.Fatalf("missing copied spec path %s: %v", rel, err)
		}
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SpecPath != "spec" {
		t.Fatalf("SpecPath = %q", manifest.SpecPath)
	}
}

func TestInitValidationAndMissingSpecErrors(t *testing.T) {
	svc, _, _ := testService(t)
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "../bad"}); err == nil {
		t.Fatal("InitSpace accepted invalid id")
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "missing-spec", SpecPath: filepath.Join(t.TempDir(), "missing.md")}); err == nil {
		t.Fatal("InitSpace accepted missing spec")
	}
}

func TestDestroyRequiresForceForDirtyEdits(t *testing.T) {
	svc, fg, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "ex-1234", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1234")
	fg.dirty = map[string]bool{filepath.Join(spacePath, "repo-a"): true}

	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "ex-1234"}); err == nil {
		t.Fatal("Destroy() succeeded with dirty editable repo")
	}
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "ex-1234", Force: true}); err != nil {
		t.Fatalf("Destroy(force) error = %v", err)
	}
	if _, err := os.Stat(spacePath); !os.IsNotExist(err) {
		t.Fatalf("space still exists: %v", err)
	}
}

func TestArchiveMissingSpaceAndCollision(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "missing"}); err == nil {
		t.Fatal("Archive(missing) succeeded")
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "archive-1"}); err != nil {
		t.Fatal(err)
	}
	archiveDest := filepath.Join(cfg.AgentWorkDir, ".archive", "archive-1")
	if err := os.MkdirAll(archiveDest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "archive-1", Force: true}); err != nil {
		t.Fatalf("Archive(collision) error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.AgentWorkDir, ".archive", "archive-1-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("archive collision matches = %#v", matches)
	}
}

func TestCreateDryRunPrintsPlanWithoutMutating(t *testing.T) {
	svc, _, cfg := testService(t)
	var out strings.Builder
	svc.Out = &out
	spec := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(spec, []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := svc.Create(context.Background(), CreateOptions{
		ID:         "dry-1",
		Kind:       "ticket",
		SpecPath:   spec,
		Edits:      []RepoSpec{{Name: "repo-a", Ref: "feature"}},
		References: []RepoSpec{{Name: "repo-b"}},
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Create(dry-run) error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"dry-run: create space directory",
		"dry-run: link " + filepath.Join(cfg.AgentWorkDir, "dry-1", ClaudeName) + " -> " + AgentsName,
		"dry-run: copy spec",
		"dry-run: fetch " + filepath.Join(cfg.BareReposDir, "repo-a.git"),
		"dry-run: add edit worktree stave/dry-1/repo-a from origin/feature",
		"dry-run: add reference worktree origin/trunk",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentWorkDir, "dry-1", ManifestName)); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote manifest: %v", err)
	}
}

func TestAddRepoDryRunAndValidationBranches(t *testing.T) {
	svc, _, _ := testService(t)
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "dry-add"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "dry-add", RepoName: "repo-a", Mode: ModeEdit, NoFetch: true, DryRun: true}); err != nil {
		t.Fatalf("AddRepo(edit dry-run) error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "dry-run: fetch") {
		t.Fatalf("NoFetch dry-run still fetched:\n%s", got)
	}
	if !strings.Contains(got, "add edit worktree") {
		t.Fatalf("unexpected edit dry-run output:\n%s", got)
	}
	out.Reset()

	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "dry-add", RepoName: "repo-b", Mode: ModeReference, Ref: "refs/tags/v1", DryRun: true}); err != nil {
		t.Fatalf("AddRepo(reference dry-run) error = %v", err)
	}
	got = out.String()
	if !strings.Contains(got, "dry-run: fetch") || !strings.Contains(got, "refs/tags/v1") {
		t.Fatalf("unexpected reference dry-run output:\n%s", got)
	}
	if _, err := svc.Status(context.Background(), "dry-add"); err != nil {
		t.Fatalf("Status after dry-run add error = %v", err)
	}

	for name, opts := range map[string]AddOptions{
		"bad-space": {SpaceID: "../bad", RepoName: "repo-a", Mode: ModeEdit},
		"bad-repo":  {SpaceID: "dry-add", RepoName: "../bad", Mode: ModeEdit},
		"bad-mode":  {SpaceID: "dry-add", RepoName: "repo-a", Mode: "copy"},
		"unknown":   {SpaceID: "dry-add", RepoName: "missing", Mode: ModeEdit},
	} {
		if err := svc.AddRepo(context.Background(), opts); err == nil {
			t.Fatalf("%s validation accepted", name)
		}
	}
}

func TestAddRepoUsesExistingBranchAndRejectsDuplicatePath(t *testing.T) {
	svc, fg, _ := testService(t)
	fg.branchExists = true
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "branchy"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "branchy", RepoName: "repo-a", Mode: ModeEdit, Branch: "topic"}); err != nil {
		t.Fatalf("AddRepo(existing branch) error = %v", err)
	}
	if !containsCallPrefix(fg.calls, "add-existing|") {
		t.Fatalf("existing branch was not reused: %#v", fg.calls)
	}
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "branchy", RepoName: "repo-a", Mode: ModeEdit}); err == nil {
		t.Fatal("duplicate edit repo path accepted")
	}
}

func TestArchivePreservesMetadataAndRequiresForceForDirtyEdits(t *testing.T) {
	svc, fg, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "ex-1234", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1234")
	fg.dirty = map[string]bool{filepath.Join(spacePath, "repo-a"): true}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "ex-1234"}); err == nil {
		t.Fatal("Archive() succeeded with dirty editable repo")
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "ex-1234", Force: true}); err != nil {
		t.Fatalf("Archive(force) error = %v", err)
	}
	archivePath := filepath.Join(cfg.AgentWorkDir, ".archive", "ex-1234")
	if _, err := os.Stat(filepath.Join(archivePath, ManifestName)); err != nil {
		t.Fatalf("archived manifest missing: %v", err)
	}
	if _, err := os.Stat(spacePath); !os.IsNotExist(err) {
		t.Fatalf("space still exists: %v", err)
	}
}

func TestSyncUpdatesReferencesAndReportsEdits(t *testing.T) {
	svc, fg, _ := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "ex-1234", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil

	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "ex-1234"}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !containsCallPrefix(fg.calls, "ahead-behind|") {
		t.Fatalf("editable drift not checked: %#v", fg.calls)
	}
	if !containsCallPrefix(fg.calls, "checkout-detached|") {
		t.Fatalf("reference not updated: %#v", fg.calls)
	}
}

func TestSyncReferencesOnlySkipsDirtyReferencesAndContinuesOnDriftError(t *testing.T) {
	svc, fg, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "sync-1", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "sync-1")
	fg.calls = nil
	fg.dirty = map[string]bool{filepath.Join(spacePath, "references", "repo-b"): true}
	fg.driftErr = os.ErrInvalid
	var out strings.Builder
	svc.Out = &out

	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "sync-1", ReferencesOnly: true}); err != nil {
		t.Fatalf("Sync(references only) error = %v", err)
	}
	if containsCallPrefix(fg.calls, "ahead-behind|") {
		t.Fatalf("edit repo was checked during references-only sync: %#v", fg.calls)
	}
	if containsCallPrefix(fg.calls, "checkout-detached|") {
		t.Fatalf("dirty reference was checked out: %#v", fg.calls)
	}
	if !strings.Contains(out.String(), "reference repo-b is dirty; skipped checkout") {
		t.Fatalf("missing dirty reference message:\n%s", out.String())
	}

	fg.calls = nil
	fg.dirty = nil
	out.Reset()
	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "sync-1"}); err != nil {
		t.Fatalf("Sync(all) error = %v", err)
	}
	if !strings.Contains(out.String(), "edit repo-a drift unknown") {
		t.Fatalf("missing drift warning:\n%s", out.String())
	}
	if !containsCallPrefix(fg.calls, "checkout-detached|") {
		t.Fatalf("clean reference was not checked out: %#v", fg.calls)
	}
}

func TestStatusReportsMissingDirtyReferenceAndDriftErrors(t *testing.T) {
	svc, fg, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "status-1", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "status-1")
	if err := os.RemoveAll(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatal(err)
	}
	fg.dirty = map[string]bool{filepath.Join(spacePath, "references", "repo-b"): true}
	fg.ahead = 4
	fg.behind = 5

	status, err := svc.Status(context.Background(), "status-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status.Repos) != 2 {
		t.Fatalf("status repos = %#v", status.Repos)
	}
	var sawMissingEdit, sawDirtyReference bool
	for _, repo := range status.Repos {
		switch repo.Repo.Mode {
		case ModeEdit:
			sawMissingEdit = !repo.Exists
		case ModeReference:
			sawDirtyReference = repo.Exists && repo.Dirty && repo.ReferenceWarn == "reference worktree is dirty"
		}
	}
	if !sawMissingEdit || !sawDirtyReference {
		t.Fatalf("status = %#v", status.Repos)
	}

	fg.driftErr = os.ErrPermission
	if err := os.MkdirAll(filepath.Join(spacePath, "repo-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err = svc.Status(context.Background(), "status-1")
	if err != nil {
		t.Fatalf("Status(drift error) error = %v", err)
	}
	if status.Repos[0].DriftError == "" && status.Repos[1].DriftError == "" {
		t.Fatalf("missing drift error in %#v", status.Repos)
	}
}

func TestHelpersAndManifestErrors(t *testing.T) {
	if normalizeRemoteRef(" origin/main ") != "origin/main" {
		t.Fatal("normalizeRemoteRef changed origin ref")
	}
	if firstNonEmpty(" ", "\t", " main ") != "main" {
		t.Fatal("firstNonEmpty did not trim fallback")
	}
	dir := t.TempDir()
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("LoadManifest missing file succeeded")
	}
	if err := SaveManifest(filepath.Join(dir, "missing"), Manifest{ID: "x"}); err == nil {
		t.Fatal("SaveManifest missing directory succeeded")
	}
	bad := filepath.Join(dir, ManifestName)
	if err := os.WriteFile(bad, []byte("id: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("LoadManifest bad yaml succeeded")
	}
}

func TestDestroyDryRunKeepsSpaceAndPrintsPlan(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.Create(context.Background(), CreateOptions{ID: "dry-destroy", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "dry-destroy", DryRun: true}); err != nil {
		t.Fatalf("Destroy(dry-run) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentWorkDir, "dry-destroy", ManifestName)); err != nil {
		t.Fatalf("dry-run removed space: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: remove worktree") || !strings.Contains(out.String(), "dry-run: remove directory") {
		t.Fatalf("unexpected dry-run output:\n%s", out.String())
	}
}

func TestNewServiceDefaultsGitAndClock(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{AgentWorkDir: filepath.Join(root, "agent-work")}
	svc := NewService(cfg, nil, nil)
	if svc.Git == nil {
		t.Fatal("NewService left Git nil instead of installing default client")
	}
	// now() must fall back to the real clock when Now is unset.
	before := time.Now().UTC().Add(-time.Second)
	got := svc.now()
	if got.Before(before) || got.Location() != time.UTC {
		t.Fatalf("now() fallback = %v", got)
	}
}

func TestFirstNonEmptyAllBlank(t *testing.T) {
	if got := firstNonEmpty("", " ", "\t"); got != "" {
		t.Fatalf("firstNonEmpty(all blank) = %q", got)
	}
}

func TestCreatePropagatesInitAndAddErrors(t *testing.T) {
	svc, _, _ := testService(t)
	// InitSpace fails on an invalid id, so Create returns before touching repos.
	if err := svc.Create(context.Background(), CreateOptions{ID: "../bad"}); err == nil {
		t.Fatal("Create accepted invalid space id")
	}
	// An unregistered edit repo makes the AddRepo pass fail.
	if err := svc.Create(context.Background(), CreateOptions{ID: "prop-edit", Edits: []RepoSpec{{Name: "ghost"}}}); err == nil {
		t.Fatal("Create accepted unregistered edit repo")
	}
	// An unregistered reference repo fails the reference pass after edits succeed.
	err := svc.Create(context.Background(), CreateOptions{
		ID:         "prop-ref",
		Edits:      []RepoSpec{{Name: "repo-a"}},
		References: []RepoSpec{{Name: "ghost"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("Create reference propagation = %v", err)
	}
}

func TestCreateDryRunRejectsUnknownReposAndMissingSpec(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Out = &strings.Builder{}
	if err := svc.Create(context.Background(), CreateOptions{ID: "dry", Edits: []RepoSpec{{Name: "ghost"}}, DryRun: true}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("dry-run edit unknown repo = %v", err)
	}
	if err := svc.Create(context.Background(), CreateOptions{ID: "dry", References: []RepoSpec{{Name: "ghost"}}, DryRun: true}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("dry-run reference unknown repo = %v", err)
	}
	if err := svc.Create(context.Background(), CreateOptions{ID: "dry", SpecPath: filepath.Join(t.TempDir(), "nope.md"), DryRun: true}); err == nil {
		t.Fatal("dry-run accepted missing spec path")
	}
}

func TestAddRepoGitErrorPaths(t *testing.T) {
	// LoadManifest failure: the space was never initialized.
	svc, _, _ := testService(t)
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "no-space", RepoName: "repo-a", Mode: ModeEdit}); err == nil {
		t.Fatal("AddRepo accepted a space with no manifest")
	}

	// Fetch failure aborts before any worktree work.
	fetchSvc, fetchGit, _ := testService(t)
	if err := fetchSvc.InitSpace(context.Background(), InitOptions{ID: "fetchy"}); err != nil {
		t.Fatal(err)
	}
	fetchGit.fetchErr = os.ErrDeadlineExceeded
	if err := fetchSvc.AddRepo(context.Background(), AddOptions{SpaceID: "fetchy", RepoName: "repo-a", Mode: ModeEdit}); err == nil {
		t.Fatal("AddRepo ignored fetch failure")
	}

	// Branch lookup failure on an edit repo.
	branchSvc, branchGit, _ := testService(t)
	if err := branchSvc.InitSpace(context.Background(), InitOptions{ID: "branchy"}); err != nil {
		t.Fatal(err)
	}
	branchGit.branchErr = os.ErrPermission
	if err := branchSvc.AddRepo(context.Background(), AddOptions{SpaceID: "branchy", RepoName: "repo-a", Mode: ModeEdit, NoFetch: true}); err == nil {
		t.Fatal("AddRepo ignored branch lookup failure")
	}

	// Worktree creation failure on a fresh branch leaves the manifest unwritten.
	addSvc, addGit, cfg := testService(t)
	if err := addSvc.InitSpace(context.Background(), InitOptions{ID: "addy"}); err != nil {
		t.Fatal(err)
	}
	addGit.addErr = os.ErrInvalid
	if err := addSvc.AddRepo(context.Background(), AddOptions{SpaceID: "addy", RepoName: "repo-a", Mode: ModeEdit, NoFetch: true}); err == nil {
		t.Fatal("AddRepo ignored worktree add failure")
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "addy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 0 {
		t.Fatalf("failed edit add still recorded a repo: %#v", manifest.Repos)
	}

	// Detached worktree failure on a reference repo.
	refSvc, refGit, _ := testService(t)
	if err := refSvc.InitSpace(context.Background(), InitOptions{ID: "refy"}); err != nil {
		t.Fatal(err)
	}
	refGit.detachErr = os.ErrInvalid
	if err := refSvc.AddRepo(context.Background(), AddOptions{SpaceID: "refy", RepoName: "repo-a", Mode: ModeReference, NoFetch: true}); err == nil {
		t.Fatal("AddRepo ignored detached worktree failure")
	}
}

func TestSyncErrorPaths(t *testing.T) {
	// LoadManifest failure on a missing space.
	svc, _, _ := testService(t)
	if err := svc.Sync(context.Background(), SyncOptions{SpaceID: "ghost"}); err == nil {
		t.Fatal("Sync accepted a missing space")
	}

	// Fetch failure surfaces from the sync loop.
	fetchSvc, fetchGit, _ := testService(t)
	if err := fetchSvc.Create(context.Background(), CreateOptions{ID: "sync-fetch", References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	fetchGit.fetchErr = os.ErrDeadlineExceeded
	if err := fetchSvc.Sync(context.Background(), SyncOptions{SpaceID: "sync-fetch"}); err == nil {
		t.Fatal("Sync ignored fetch failure")
	}

	// IsDirty failure on a reference worktree.
	dirtySvc, dirtyGit, _ := testService(t)
	if err := dirtySvc.Create(context.Background(), CreateOptions{ID: "sync-dirty", References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	dirtyGit.dirtyErr = os.ErrPermission
	if err := dirtySvc.Sync(context.Background(), SyncOptions{SpaceID: "sync-dirty"}); err == nil {
		t.Fatal("Sync ignored reference IsDirty failure")
	}

	// Checkout failure on a clean reference worktree.
	checkoutSvc, checkoutGit, _ := testService(t)
	if err := checkoutSvc.Create(context.Background(), CreateOptions{ID: "sync-checkout", References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	checkoutGit.checkoutErr = os.ErrInvalid
	if err := checkoutSvc.Sync(context.Background(), SyncOptions{SpaceID: "sync-checkout"}); err == nil {
		t.Fatal("Sync ignored checkout failure")
	}
}

func TestStatusErrorPaths(t *testing.T) {
	// LoadManifest failure on a missing space.
	svc, _, _ := testService(t)
	if _, err := svc.Status(context.Background(), "ghost"); err == nil {
		t.Fatal("Status accepted a missing space")
	}

	// IsDirty failure on an existing worktree aborts the status walk.
	dirtySvc, dirtyGit, _ := testService(t)
	if err := dirtySvc.Create(context.Background(), CreateOptions{ID: "status-dirty", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	dirtyGit.dirtyErr = os.ErrPermission
	if _, err := dirtySvc.Status(context.Background(), "status-dirty"); err == nil {
		t.Fatal("Status ignored IsDirty failure")
	}
}

func TestArchiveAndDestroyGitErrorPaths(t *testing.T) {
	// Archive: worktree removal failure.
	archiveSvc, archiveGit, _ := testService(t)
	if err := archiveSvc.Create(context.Background(), CreateOptions{ID: "arch-rm", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	archiveGit.removeErr = os.ErrInvalid
	if err := archiveSvc.Archive(context.Background(), ArchiveOptions{SpaceID: "arch-rm"}); err == nil {
		t.Fatal("Archive ignored worktree removal failure")
	}

	// Archive: prune failure after a successful removal.
	pruneSvc, pruneGit, _ := testService(t)
	if err := pruneSvc.Create(context.Background(), CreateOptions{ID: "arch-prune", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	pruneGit.pruneErr = os.ErrInvalid
	if err := pruneSvc.Archive(context.Background(), ArchiveOptions{SpaceID: "arch-prune"}); err == nil {
		t.Fatal("Archive ignored prune failure")
	}

	// Archive: ensureNoDirtyEdits surfaces an IsDirty error.
	edSvc, edGit, _ := testService(t)
	if err := edSvc.Create(context.Background(), CreateOptions{ID: "arch-dirty", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	edGit.dirtyErr = os.ErrPermission
	if err := edSvc.Archive(context.Background(), ArchiveOptions{SpaceID: "arch-dirty"}); err == nil {
		t.Fatal("Archive ignored dirty-check failure")
	}

	// Destroy: LoadManifest failure on a missing space.
	missSvc, _, _ := testService(t)
	if err := missSvc.Destroy(context.Background(), DestroyOptions{SpaceID: "ghost"}); err == nil {
		t.Fatal("Destroy accepted a missing space")
	}

	// Destroy: worktree removal failure.
	destSvc, destGit, _ := testService(t)
	if err := destSvc.Create(context.Background(), CreateOptions{ID: "dest-rm", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	destGit.removeErr = os.ErrInvalid
	if err := destSvc.Destroy(context.Background(), DestroyOptions{SpaceID: "dest-rm", Force: true}); err == nil {
		t.Fatal("Destroy ignored worktree removal failure")
	}

	// Destroy: prune failure after removal.
	dpSvc, dpGit, _ := testService(t)
	if err := dpSvc.Create(context.Background(), CreateOptions{ID: "dest-prune", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	dpGit.pruneErr = os.ErrInvalid
	if err := dpSvc.Destroy(context.Background(), DestroyOptions{SpaceID: "dest-prune", Force: true}); err == nil {
		t.Fatal("Destroy ignored prune failure")
	}
}

func TestCopyFileErrorPaths(t *testing.T) {
	dir := t.TempDir()

	// ReadFile fails when the source is a directory, not a file.
	if err := copyFile(dir, filepath.Join(dir, "out")); err == nil {
		t.Fatal("copyFile read a directory as a file")
	}

	// MkdirAll fails when a parent of the destination is an existing file.
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, filepath.Join(blocker, "child", "out.txt")); err == nil {
		t.Fatal("copyFile created a directory under an existing file")
	}
}

func TestCopySpecMissingSourceFails(t *testing.T) {
	if err := copySpec(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest")); err == nil {
		t.Fatal("copySpec accepted a missing source")
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func containsCallPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
