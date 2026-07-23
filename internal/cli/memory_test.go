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

func TestCLIArchiveMemoryFlagDefaultKeep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "x")
	// No --memory → keep (default): archive succeeds with no attachments.
	out := runCLI(t, "space", "archive", "x")
	if !strings.Contains(out, "archived x") {
		t.Fatalf("archive output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", ".archive", "x")); err != nil {
		t.Fatalf("archived space missing: %v", err)
	}
}

func TestCLIArchiveMemoryDestroyRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "x")
	out, err := runCLIError(t, nil, "space", "archive", "x", "--memory", "destroy")
	if err == nil {
		t.Fatalf("expected --memory destroy rejection:\n%s", out)
	}
	if !strings.Contains(err.Error(), "stave space destroy --memory destroy") {
		t.Fatalf("error must redirect to space destroy: %v", err)
	}
	// Space untouched.
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "x")); err != nil {
		t.Fatalf("space must remain: %v", err)
	}
}

func TestCLIArchiveMemoryContributeDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "x")
	// Record a marmot attachment directly; dry-run never invokes the binary.
	spacePath := filepath.Join(home, "stave", "agent-work", "x")
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Memories = []space.MemoryManifest{{Name: "default", Provider: "marmot", ID: "x", Owned: true}}
	if err := space.SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, "space", "archive", "x", "--memory", "contribute", "--dry-run")
	// Propose dry-run prints both provider commands (contribute + warren propose)...
	if !strings.Contains(out, "den contribute x") {
		t.Fatalf("missing den contribute:\n%s", out)
	}
	if !strings.Contains(out, "warren propose") {
		t.Fatalf("missing warren propose:\n%s", out)
	}
	// ...composed with archive's own dry-run plan (route rewrite + move).
	if !strings.Contains(out, "route set-project") {
		t.Fatalf("missing route rewrite plan:\n%s", out)
	}
	if !strings.Contains(out, "dry-run: archive") {
		t.Fatalf("missing archive plan line:\n%s", out)
	}
	// No mutation.
	if _, err := os.Stat(spacePath); err != nil {
		t.Fatalf("dry-run must not move space: %v", err)
	}
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
