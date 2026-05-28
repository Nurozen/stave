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
}

func (f *fakeGit) record(parts ...string) {
	f.calls = append(f.calls, strings.Join(parts, "|"))
}

func (f *fakeGit) FetchAllPrune(ctx context.Context, bare string) error {
	f.record("fetch", bare)
	return nil
}

func (f *fakeGit) WorktreeAddBranch(ctx context.Context, bare, path, branch, start string) error {
	f.record("add-branch", bare, path, branch, start)
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeAddExisting(ctx context.Context, bare, path, branch string) error {
	f.record("add-existing", bare, path, branch)
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeAddDetached(ctx context.Context, bare, path, ref string) error {
	f.record("add-detached", bare, path, ref)
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeRemove(ctx context.Context, bare, path string, force bool) error {
	f.record("remove", bare, path)
	return nil
}

func (f *fakeGit) WorktreePrune(ctx context.Context, bare string) error {
	f.record("prune", bare)
	return nil
}

func (f *fakeGit) CheckoutDetached(ctx context.Context, path, ref string) error {
	f.record("checkout-detached", path, ref)
	return nil
}

func (f *fakeGit) BranchExists(ctx context.Context, bare, branch string) (bool, error) {
	f.record("branch-exists", bare, branch)
	return f.branchExists, nil
}

func (f *fakeGit) IsDirty(ctx context.Context, path string) (bool, string, error) {
	f.record("dirty", path)
	if f.dirty != nil && f.dirty[path] {
		return true, " M file.go\n", nil
	}
	return false, "", nil
}

func (f *fakeGit) AheadBehind(ctx context.Context, path, base string) (int, int, error) {
	f.record("ahead-behind", path, base)
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
