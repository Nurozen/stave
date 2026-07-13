package summon

import (
	"context"
	"errors"
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

func TestSummonPromptOverride(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	launcher := &fakeLauncher{}
	svc := NewService(cfg, launcher, &out)
	svc.Interactive = true

	if err := svc.Summon(context.Background(), Options{SpaceID: "ex-1", Summoner: Claude, Prompt: "/pr-teach"}); err != nil {
		t.Fatal(err)
	}
	if !launcher.called {
		t.Fatal("launcher was not called")
	}
	if len(launcher.invocation.Args) != 1 || launcher.invocation.Args[0] != "/pr-teach" {
		t.Fatalf("invocation args = %#v, want the override prompt only", launcher.invocation.Args)
	}
}

func TestReviewSpaceDefaultsToEmbeddedSkill(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", Kind: "review", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(spacePath, ".claude", "skills", ReviewSkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, &fakeLauncher{}, nil)

	claude, err := svc.Invocation(Options{SpaceID: "ex-1", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.Args) != 1 || claude.Args[0] != "/"+ReviewSkillName {
		t.Fatalf("claude review args = %#v, want the skill invocation", claude.Args)
	}

	// Codex has no skill system: it keeps the review-stance prompt.
	codex, err := svc.Invocation(Options{SpaceID: "ex-1", Summoner: Codex})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(codex.Args, " "), "review space") {
		t.Fatalf("codex review args = %#v, want the review-stance prompt", codex.Args)
	}

	// Without the installed skill (e.g. a pre-existing review space), claude
	// falls back to the review-stance prompt instead of a dangling /command.
	if err := os.RemoveAll(skillDir); err != nil {
		t.Fatal(err)
	}
	claude, err = svc.Invocation(Options{SpaceID: "ex-1", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(claude.Args, " "), "/"+ReviewSkillName) {
		t.Fatalf("claude args = %#v, want fallback prompt when skill missing", claude.Args)
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

func TestExecLauncherLaunchesCommandAndReportsErrors(t *testing.T) {
	dir := t.TempDir()
	err := (ExecLauncher{}).Launch(context.Background(), Invocation{
		Command: "/bin/echo",
		Args:    []string{"hello"},
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Launch(echo) error = %v", err)
	}
	err = (ExecLauncher{}).Launch(context.Background(), Invocation{
		Command: "/definitely/missing/stave-summoner",
		Dir:     dir,
	})
	if err == nil {
		t.Fatal("Launch(missing) succeeded")
	}
}

func TestSummonPrintCommandOptionAndErrors(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	launcher := &fakeLauncher{}
	svc := NewService(cfg, launcher, &out)
	if err := svc.Summon(context.Background(), Options{SpaceID: "ex-1", Summoner: Cursor, PrintCommand: true}); err != nil {
		t.Fatal(err)
	}
	if launcher.called {
		t.Fatal("launcher called for print-command summon")
	}
	if !strings.Contains(out.String(), "cursor-agent") {
		t.Fatalf("print command output = %q", out.String())
	}

	launcher.err = errors.New("launch failed")
	if err := svc.Summon(context.Background(), Options{SpaceID: "ex-1", Summoner: Claude}); err == nil || !strings.Contains(err.Error(), "launch claude") {
		t.Fatalf("launch error = %v", err)
	}
	if err := svc.Summon(context.Background(), Options{SpaceID: "../bad", Summoner: Codex}); err == nil {
		t.Fatal("unsafe space id accepted")
	}
	if err := svc.Summon(context.Background(), Options{SpaceID: "missing", Summoner: Codex}); err == nil {
		t.Fatal("missing space accepted")
	}
}

func TestInvocationAndConfigBranches(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", CreatedAt: time.Now().UTC(), SpecPath: "/abs/spec.md"}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, nil, nil)
	invocation, err := svc.Invocation(Options{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Summoner != Codex || !strings.Contains(invocation.Prompt, "/abs/spec.md") {
		t.Fatalf("invocation = %#v", invocation)
	}

	cfg.Summon.Default = Claude
	if ResolveName(cfg, "") != Claude || ResolveName(cfg, Cursor) != Cursor {
		t.Fatal("ResolveName mismatch")
	}
	cfg.Summon.Default = ""
	if ResolveName(cfg, "") != Codex {
		t.Fatal("ResolveName did not fall back to codex")
	}
	if err := ValidateSummoner("bad"); err == nil {
		t.Fatal("bad summoner accepted")
	}
	cfg.Summon.Commands[Claude] = ""
	if _, err := BuildInvocation(cfg, spacePath, Claude, "prompt"); err == nil || !strings.Contains(err.Error(), "no configured command") {
		t.Fatalf("missing command err = %v", err)
	}
	if _, err := BuildInvocation(cfg, spacePath, "bad", "prompt"); err == nil || !strings.Contains(err.Error(), "summoner") {
		t.Fatalf("bad summoner err = %v", err)
	}

	quoted := CommandString(Invocation{Command: "codex", Dir: "/tmp/has space", Args: []string{"it's", ""}})
	if !strings.Contains(quoted, "'/tmp/has space'") || !strings.Contains(quoted, `'it'"'"'s'`) || !strings.Contains(quoted, "''") {
		t.Fatalf("quoted command = %s", quoted)
	}
}

type fakeLauncher struct {
	called     bool
	invocation Invocation
	err        error
}

func (f *fakeLauncher) Launch(ctx context.Context, invocation Invocation) error {
	f.called = true
	f.invocation = invocation
	return f.err
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
