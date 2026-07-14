package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

func TestCLIMemoryAttachDryRunPrintsNoPointer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "demo-space")

	out := runCLI(t, "memory", "attach", "demo-space", "--dry-run")
	if !strings.Contains(out, "marmot den create") {
		t.Fatalf("expected den create:\n%s", out)
	}
	if !strings.Contains(out, "--no-pointer") {
		t.Fatalf("expected --no-pointer:\n%s", out)
	}
	if !strings.Contains(out, "--json") {
		t.Fatalf("expected --json:\n%s", out)
	}
	// No memories written on dry-run.
	manifest, err := space.LoadManifest(filepath.Join(home, "stave", "agent-work", "demo-space"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("dry-run must not write memories: %#v", manifest.Memories)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "demo-space", ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault must not exist")
	}
}

func TestCLISpaceCreateMemoryDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	out := runCLI(t, "space", "create", "demo-space", "--memory", ".", "--dry-run")
	if !strings.Contains(out, "--no-pointer") || !strings.Contains(out, "den create") {
		t.Fatalf("dry-run output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "demo-space")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create space")
	}
}

func TestCLIDestroyMemoryFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "x")
	// Default keep should work with no attachments.
	runCLI(t, "space", "destroy", "x", "--memory", "keep")
}

func TestCLIMemoryProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	out := runCLI(t, "memory", "providers")
	if !strings.Contains(out, "marmot") {
		t.Fatalf("providers:\n%s", out)
	}
}
