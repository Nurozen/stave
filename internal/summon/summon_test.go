package summon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
)

func TestPromptIncludesSpecPath(t *testing.T) {
	spacePath := filepath.Join(t.TempDir(), "agent-work", "ex-1")
	prompt := Prompt(spacePath, "spec")
	if !strings.Contains(prompt, "Using a team of agents") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, filepath.Join(spacePath, "spec")) {
		t.Fatalf("prompt missing spec path: %q", prompt)
	}
}

func TestBuildInvocation(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	prompt := "hello"

	codex, err := BuildInvocation(cfg, spacePath, Codex, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if codex.Command != "codex" || codex.Dir != spacePath || len(codex.Args) != 3 || codex.Args[0] != "--cd" || codex.Args[1] != spacePath || codex.Args[2] != prompt {
		t.Fatalf("codex invocation = %#v", codex)
	}

	cursor, err := BuildInvocation(cfg, spacePath, Cursor, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Command != "cursor-agent" || cursor.Dir != spacePath || len(cursor.Args) != 1 || cursor.Args[0] != prompt {
		t.Fatalf("cursor invocation = %#v", cursor)
	}
}

func TestServicePrintsCommandWhenNonInteractive(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", CreatedAt: time.Now().UTC(), SpecPath: "spec"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	launcher := &fakeLauncher{}
	svc := NewService(cfg, launcher, &out)
	svc.Interactive = false

	if err := svc.Summon(context.Background(), Options{SpaceID: "ex-1", Summoner: Codex}); err != nil {
		t.Fatal(err)
	}
	if launcher.called {
		t.Fatal("launcher was called")
	}
	if !strings.Contains(out.String(), "cd ") || !strings.Contains(out.String(), "codex --cd") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestServiceLaunchesInteractive(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	svc := NewService(cfg, launcher, nil)
	svc.Interactive = true

	if err := svc.Summon(context.Background(), Options{SpaceID: "ex-1", Summoner: Claude}); err != nil {
		t.Fatal(err)
	}
	if !launcher.called || launcher.invocation.Summoner != Claude || launcher.invocation.Dir != spacePath {
		t.Fatalf("launcher = %#v", launcher)
	}
}

type fakeLauncher struct {
	called     bool
	invocation Invocation
}

func (f *fakeLauncher) Launch(ctx context.Context, invocation Invocation) error {
	f.called = true
	f.invocation = invocation
	return nil
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
	}
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.AgentWorkDir, "ex-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg
}
