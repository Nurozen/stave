package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

func TestCLIHelpCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"repos", "--help"},
		{"space", "--help"},
		{"space", "create", "--help"},
	} {
		cmd := NewRootCommand()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("stave %v error = %v\n%s", args, err, out.String())
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("help output missing Usage for %v:\n%s", args, out.String())
		}
	}
}

func TestCLISetupReposAddAndCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "space", "create", "ex-1234", "--kind", "ticket", "--edit", "repo-a", "--reference", "repo-b")

	root := filepath.Join(home, "stave")
	spacePath := filepath.Join(root, "agent-work", "ex-1234")
	if _, err := os.Stat(filepath.Join(root, "bare-repos", "repo-a.git")); err != nil {
		t.Fatalf("bare repo missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatalf("edit worktree missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); err != nil {
		t.Fatalf("reference worktree missing: %v", err)
	}
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "ex-1234" || len(manifest.Repos) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	status := runCLI(t, "space", "status", "ex-1234")
	if !strings.Contains(status, "repo-a [edit]") || !strings.Contains(status, "repo-b [reference]") {
		t.Fatalf("status output missing repos:\n%s", status)
	}
}

func TestCLICreateDryRunDoesNotCreateSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)

	out := runCLI(t, "space", "create", "ex-1234", "--edit", "repo-a", "--dry-run")
	if !strings.Contains(out, "dry-run: create space directory") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
}

func TestCLISpaceCommandsAreNotTopLevel(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("top-level create unexpectedly succeeded:\n%s", out.String())
	}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	cmd := NewRootCommand()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("stave %v error = %v\n%s", args, err, out.String())
	}
	return out.String()
}

func createGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "-b", "main", dir)
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
}
