package space

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
)

// TestAddRepoStartPointBranchesAtStartPointWhileBaseTracksBase verifies the
// review-space contract: the edit branch is created at StartPoint (the PR head)
// while the manifest Base records the merge target, so drift reports against the
// base. It runs against a real bare mirror so git rev-parse can confirm the head.
func TestAddRepoStartPointBranchesAtStartPointWhileBaseTracksBase(t *testing.T) {
	root := t.TempDir()

	src := filepath.Join(t.TempDir(), "src")
	startGit(t, "", "init", "-b", "main", src)
	startGit(t, src, "config", "user.name", "Test User")
	startGit(t, src, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(src, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, src, "add", "base.txt")
	startGit(t, src, "commit", "-m", "base")
	baseSHA := startGitOutput(t, src, "rev-parse", "HEAD")

	startGit(t, src, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(src, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, src, "add", "feature.txt")
	startGit(t, src, "commit", "-m", "feature change")
	featureSHA := startGitOutput(t, src, "rev-parse", "HEAD")
	if featureSHA == baseSHA {
		t.Fatal("feature and base commits must differ for a meaningful drift check")
	}

	ctx := context.Background()
	client := git.New()
	bare := filepath.Join(root, "bare-repos", "repo-a.git")
	if err := client.CloneBare(ctx, src, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}

	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"repo-a": {Name: "repo-a", URL: src, BareRepoPath: bare, DefaultBranch: "main"},
		},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, client, nil)
	if err := svc.InitSpace(ctx, InitOptions{ID: "review-1", Kind: "review"}); err != nil {
		t.Fatalf("InitSpace() error = %v", err)
	}
	if err := svc.AddRepo(ctx, AddOptions{
		SpaceID:    "review-1",
		RepoName:   "repo-a",
		Mode:       ModeEdit,
		Base:       "main",
		StartPoint: "feature",
		NoFetch:    true,
	}); err != nil {
		t.Fatalf("AddRepo() error = %v", err)
	}

	spacePath := filepath.Join(cfg.AgentWorkDir, "review-1")
	worktree := filepath.Join(spacePath, "repo-a")
	if head := startGitOutput(t, worktree, "rev-parse", "HEAD"); head != featureSHA {
		t.Fatalf("worktree HEAD = %q, want StartPoint (feature) %q", head, featureSHA)
	}
	if resolvedBase := startGitOutput(t, worktree, "rev-parse", "origin/main"); resolvedBase != baseSHA {
		t.Fatalf("origin/main = %q, want base %q", resolvedBase, baseSHA)
	}

	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 {
		t.Fatalf("manifest repos = %#v", manifest.Repos)
	}
	entry := manifest.Repos[0]
	if entry.Base != "origin/main" {
		t.Fatalf("manifest Base = %q, want origin/main", entry.Base)
	}
	if entry.Branch != DefaultBranch("review-1", "repo-a") {
		t.Fatalf("manifest Branch = %q, want %q", entry.Branch, DefaultBranch("review-1", "repo-a"))
	}
}

// TestAddRepoSpaceSugarStacksOnSiblingBranch verifies the stacking contract
// against real git: a second space whose base is "space:<id>" branches from the
// first space's edit branch (refs/heads/stave/<id>/<repo>), and drift tracks
// that branch as the first space commits. A missing target branch errors
// cleanly instead of minting a dead ref.
func TestAddRepoSpaceSugarStacksOnSiblingBranch(t *testing.T) {
	root := t.TempDir()

	src := filepath.Join(t.TempDir(), "src")
	startGit(t, "", "init", "-b", "main", src)
	startGit(t, src, "config", "user.name", "Test User")
	startGit(t, src, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(src, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, src, "add", "base.txt")
	startGit(t, src, "commit", "-m", "base")

	ctx := context.Background()
	client := git.New()
	bare := filepath.Join(root, "bare-repos", "repo-a.git")
	if err := client.CloneBare(ctx, src, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}

	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"repo-a": {Name: "repo-a", URL: src, BareRepoPath: bare, DefaultBranch: "main"},
		},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, client, nil)

	// Space A owns refs/heads/stave/space-a/repo-a and commits on it.
	if err := svc.Create(ctx, CreateOptions{ID: "space-a", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatalf("Create(space-a) error = %v", err)
	}
	worktreeA := filepath.Join(cfg.AgentWorkDir, "space-a", "repo-a")
	startGit(t, worktreeA, "config", "user.name", "Test User")
	startGit(t, worktreeA, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(worktreeA, "layer.txt"), []byte("layer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, worktreeA, "add", "layer.txt")
	startGit(t, worktreeA, "commit", "-m", "layer one")
	headA := startGitOutput(t, worktreeA, "rev-parse", "HEAD")

	// Space B stacks on space A.
	if err := svc.Create(ctx, CreateOptions{ID: "space-b", Edits: []RepoSpec{{Name: "repo-a", Ref: "space:space-a"}}}); err != nil {
		t.Fatalf("Create(space-b stacked) error = %v", err)
	}
	worktreeB := filepath.Join(cfg.AgentWorkDir, "space-b", "repo-a")
	if head := startGitOutput(t, worktreeB, "rev-parse", "HEAD"); head != headA {
		t.Fatalf("stacked worktree HEAD = %q, want space A head %q", head, headA)
	}
	if branch := startGitOutput(t, worktreeB, "rev-parse", "--abbrev-ref", "HEAD"); branch != DefaultBranch("space-b", "repo-a") {
		t.Fatalf("stacked worktree branch = %q", branch)
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "space-b"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Repos[0].Base != "refs/heads/"+DefaultBranch("space-a", "repo-a") {
		t.Fatalf("stacked manifest Base = %q", manifest.Repos[0].Base)
	}

	// Drift tracks space A's branch: another commit in A puts B one behind.
	if ahead, behind, err := client.AheadBehind(ctx, worktreeB, manifest.Repos[0].Base); err != nil || ahead != 0 || behind != 0 {
		t.Fatalf("fresh stack drift = %d/%d, %v", ahead, behind, err)
	}
	if err := os.WriteFile(filepath.Join(worktreeA, "layer2.txt"), []byte("layer two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, worktreeA, "add", "layer2.txt")
	startGit(t, worktreeA, "commit", "-m", "layer two")
	if ahead, behind, err := client.AheadBehind(ctx, worktreeB, manifest.Repos[0].Base); err != nil || ahead != 0 || behind != 1 {
		t.Fatalf("post-commit drift = %d/%d, %v; want 0 ahead, 1 behind", ahead, behind, err)
	}

	// Stacking on a space that has no branch for the repo fails cleanly.
	err = svc.Create(ctx, CreateOptions{ID: "space-c", Edits: []RepoSpec{{Name: "repo-a", Ref: "space:ghost"}}})
	if err == nil || !strings.Contains(err.Error(), `space "ghost" has no branch "stave/ghost/repo-a"`) {
		t.Fatalf("Create(missing stack target) error = %v", err)
	}
}

func startGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
}

func startGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
