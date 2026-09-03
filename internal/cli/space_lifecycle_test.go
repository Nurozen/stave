package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

// lifecycleGit runs git and returns trimmed combined output; a non-zero exit
// is returned as ok=false so callers can probe (show-ref) without failing.
func lifecycleGit(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func lifecycleFixture(t *testing.T, id string) (home, spacePath, bareA, bareB string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "space", "create", id, "-e", "repo-a", "-r", "repo-b")
	root := filepath.Join(home, "stave")
	return home, filepath.Join(root, "agent-work", id), filepath.Join(root, "bare-repos", "repo-a.git"), filepath.Join(root, "bare-repos", "repo-b.git")
}

func TestCLISpaceRemove(t *testing.T) {
	_, spacePath, bareA, bareB := lifecycleFixture(t, "rm-1")
	branch := "refs/heads/" + space.DefaultBranch("rm-1", "repo-a")

	// Unknown repo: typed refusal surfaces as a clear message.
	if _, err := runCLIError(t, nil, "space", "remove", "rm-1", "repo-zz"); err == nil || !strings.Contains(err.Error(), `has no repo "repo-zz"`) {
		t.Fatalf("unknown repo err = %v", err)
	}

	// Dry-run: prints the plan, changes nothing.
	before, err := os.ReadFile(filepath.Join(spacePath, space.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	dry := runCLI(t, "space", "remove", "rm-1", "repo-a", "--dry-run")
	if !strings.Contains(dry, "dry-run: remove worktree") || !strings.Contains(dry, "never deletes branches") {
		t.Fatalf("dry-run output = %s", dry)
	}
	after, err := os.ReadFile(filepath.Join(spacePath, space.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run rewrote the manifest")
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatalf("dry-run removed the worktree: %v", err)
	}

	// Dirty edit worktree refuses; --force proceeds.
	if err := os.WriteFile(filepath.Join(spacePath, "repo-a", "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLIError(t, nil, "space", "remove", "rm-1", "repo-a"); err == nil || !strings.Contains(err.Error(), "dirty editable worktrees") {
		t.Fatalf("dirty remove err = %v", err)
	}
	out := runCLI(t, "space", "remove", "rm-1", "repo-a", "--force")
	if !strings.Contains(out, "removed repo-a from rm-1") {
		t.Fatalf("remove output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); !os.IsNotExist(err) {
		t.Fatalf("edit worktree still present: %v", err)
	}
	if list, _ := lifecycleGit(t, "", "--git-dir", bareA, "worktree", "list"); strings.Contains(list, spacePath) {
		t.Fatalf("bare repo still registers the worktree:\n%s", list)
	}
	// The branch survives in the bare repo: Stave never deletes branches.
	if _, ok := lifecycleGit(t, "", "--git-dir", bareA, "show-ref", "--verify", "--quiet", branch); !ok {
		t.Fatalf("branch %s was deleted from the bare repo", branch)
	}
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || manifest.Repos[0].Name != "repo-b" {
		t.Fatalf("manifest repos = %#v", manifest.Repos)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, space.AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agents), "`repo-a`") {
		t.Fatalf("AGENTS.md still lists repo-a:\n%s", agents)
	}

	// Reference repo removes cleanly (no dirty guard applies).
	runCLI(t, "space", "remove", "rm-1", "repo-b")
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); !os.IsNotExist(err) {
		t.Fatalf("reference worktree still present: %v", err)
	}
	if list, _ := lifecycleGit(t, "", "--git-dir", bareB, "worktree", "list"); strings.Contains(list, spacePath) {
		t.Fatalf("bare repo still registers the reference worktree:\n%s", list)
	}
	manifest, err = space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 0 {
		t.Fatalf("manifest repos = %#v", manifest.Repos)
	}
	status := runCLI(t, "space", "status", "rm-1")
	if strings.Contains(status, "repo-a") || strings.Contains(status, "repo-b") {
		t.Fatalf("status still lists removed repos:\n%s", status)
	}
}

func TestCLISpaceRestoreRoundTrip(t *testing.T) {
	home, spacePath, bareA, _ := lifecycleFixture(t, "rs-1")
	archiveRoot := filepath.Join(home, "stave", "agent-work", ".archive")
	before, err := os.ReadFile(filepath.Join(spacePath, space.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	// Pin the reference at a specific commit so restore can be checked
	// against it; the recorded ref is origin/main in the bare repo.
	wantRefHead, _ := lifecycleGit(t, "", "--git-dir", filepath.Join(home, "stave", "bare-repos", "repo-b.git"), "rev-parse", "refs/remotes/origin/main")

	runCLI(t, "space", "archive", "rs-1")
	if _, err := os.Stat(filepath.Join(archiveRoot, "rs-1", space.ManifestName)); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	// Dry-run leaves the archive alone.
	dry := runCLI(t, "space", "restore", "rs-1", "--dry-run")
	if !strings.Contains(dry, "dry-run: restore") || !strings.Contains(dry, "dry-run: add edit worktree "+space.DefaultBranch("rs-1", "repo-a")) {
		t.Fatalf("dry-run output = %s", dry)
	}
	if _, err := os.Stat(spacePath); !os.IsNotExist(err) {
		t.Fatalf("dry-run restored the space: %v", err)
	}

	out := runCLI(t, "space", "restore", "rs-1")
	if !strings.Contains(out, "restored rs-1 -> "+spacePath) || !strings.Contains(out, "restored edit repo-a") || !strings.Contains(out, "restored reference repo-b") {
		t.Fatalf("restore output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, "rs-1")); !os.IsNotExist(err) {
		t.Fatalf("archive still present after restore: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(spacePath, space.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("manifest differs after restore:\n%s\n---\n%s", before, after)
	}
	if head, _ := lifecycleGit(t, filepath.Join(spacePath, "repo-a"), "rev-parse", "--abbrev-ref", "HEAD"); head != space.DefaultBranch("rs-1", "repo-a") {
		t.Fatalf("edit worktree HEAD = %q", head)
	}
	if list, _ := lifecycleGit(t, "", "--git-dir", bareA, "worktree", "list"); !strings.Contains(list, filepath.Join(spacePath, "repo-a")) {
		t.Fatalf("bare repo does not register the restored worktree:\n%s", list)
	}
	refDir := filepath.Join(spacePath, "references", "repo-b")
	if head, _ := lifecycleGit(t, refDir, "rev-parse", "HEAD"); head != wantRefHead {
		t.Fatalf("reference HEAD = %q, want %q", head, wantRefHead)
	}
	if sym, ok := lifecycleGit(t, refDir, "symbolic-ref", "-q", "HEAD"); ok {
		t.Fatalf("reference worktree must be detached, HEAD -> %s", sym)
	}
	status := runCLI(t, "space", "status", "rs-1")
	if !strings.Contains(status, "repo-a [edit]") || !strings.Contains(status, "repo-b [reference]") {
		t.Fatalf("status after restore:\n%s", status)
	}

	// A live space with that id refuses a second restore.
	if _, err := runCLIError(t, nil, "space", "restore", "rs-1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("live-space restore err = %v", err)
	}
	if _, err := runCLIError(t, nil, "space", "restore", "never"); err == nil || !strings.Contains(err.Error(), "no archive of space") {
		t.Fatalf("missing-archive err = %v", err)
	}
}

func TestCLISpaceRestoreFromTimestampedArchive(t *testing.T) {
	home, spacePath, _, _ := lifecycleFixture(t, "rs-2")
	archiveRoot := filepath.Join(home, "stave", "agent-work", ".archive")
	runCLI(t, "space", "archive", "rs-2")
	stamped := filepath.Join(archiveRoot, "rs-2-20260101000000")
	if err := os.Rename(filepath.Join(archiveRoot, "rs-2"), stamped); err != nil {
		t.Fatal(err)
	}
	// Single timestamped candidate: auto-picked.
	runCLI(t, "space", "restore", "rs-2")
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a", "README.md")); err != nil {
		t.Fatalf("restored worktree missing: %v", err)
	}

	// Two candidates: ambiguity refusal, then --from picks.
	runCLI(t, "space", "archive", "rs-2")
	if err := os.Rename(filepath.Join(archiveRoot, "rs-2"), filepath.Join(archiveRoot, "rs-2-20260101000000")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(archiveRoot, "rs-2-20260202000000")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := space.LoadManifest(stamped)
	if err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(other, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLIError(t, nil, "space", "restore", "rs-2"); err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("ambiguity err = %v", err)
	}
	if _, err := runCLIError(t, nil, "space", "restore", "rs-2", "--from", "../rs-2-20260101000000"); err == nil || !strings.Contains(err.Error(), "archive name") {
		t.Fatalf("traversal --from err = %v", err)
	}
	runCLI(t, "space", "restore", "rs-2", "--from", "rs-2-20260101000000")
	if _, err := os.Stat(stamped); !os.IsNotExist(err) {
		t.Fatalf("named archive still present: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other archive must be untouched: %v", err)
	}
}

func TestCLISpaceRestoreMissingBranchLeavesArchive(t *testing.T) {
	home, spacePath, bareA, _ := lifecycleFixture(t, "rs-3")
	archiveRoot := filepath.Join(home, "stave", "agent-work", ".archive")
	runCLI(t, "space", "archive", "rs-3")
	// Simulate an out-of-band branch deletion (Stave itself never does this).
	if out, ok := lifecycleGit(t, "", "--git-dir", bareA, "branch", "-D", space.DefaultBranch("rs-3", "repo-a")); !ok {
		t.Fatalf("branch -D: %s", out)
	}
	_, err := runCLIError(t, nil, "space", "restore", "rs-3")
	if err == nil || !strings.Contains(err.Error(), "no longer exists in the bare repo") {
		t.Fatalf("missing branch err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, "rs-3", space.ManifestName)); err != nil {
		t.Fatalf("archive must stay intact: %v", err)
	}
	if _, err := os.Stat(spacePath); !os.IsNotExist(err) {
		t.Fatalf("space must not be materialized: %v", err)
	}
}
