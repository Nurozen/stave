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
