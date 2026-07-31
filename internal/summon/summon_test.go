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

func TestReviewSpaceInvocationIncludesMemoryBulletForNonClaude(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "rev-mem")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:        "rev-mem",
		Kind:      "review",
		CreatedAt: time.Now().UTC(),
		Memories:  []space.MemoryManifest{{Name: "default", Provider: "marmot", ID: "rev-den", Owned: true}},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, &fakeLauncher{}, nil)

	// Non-Claude summoners take defaultPrompt → PromptForKindWithMemories,
	// which must carry both the review stance and the memory bullet.
	codex, err := svc.Invocation(Options{SpaceID: "rev-mem", Summoner: Codex})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, " ")
	if !strings.Contains(joined, "context-marmot MCP tools (den: rev-den)") {
		t.Fatalf("codex review invocation missing memory bullet: %#v", codex.Args)
	}
	if !strings.Contains(joined, "review space") {
		t.Fatalf("codex review invocation missing review stance: %#v", codex.Args)
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

	claude, err := BuildInvocation(cfg, spacePath, Claude, prompt, "--dangerously-skip-permissions", "--model", "opus")
	if err != nil {
		t.Fatal(err)
	}
	wantClaude := []string{"--dangerously-skip-permissions", "--model", "opus", prompt}
	if strings.Join(claude.Args, "\x00") != strings.Join(wantClaude, "\x00") {
		t.Fatalf("claude args = %#v, want %#v", claude.Args, wantClaude)
	}

	codexWithFlags, err := BuildInvocation(cfg, spacePath, Codex, prompt, "--yolo")
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := []string{"--cd", spacePath, "--yolo", prompt}
	if strings.Join(codexWithFlags.Args, "\x00") != strings.Join(wantCodex, "\x00") {
		t.Fatalf("codex args = %#v, want %#v", codexWithFlags.Args, wantCodex)
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

// F13: the Claude review-skill launch prompt must carry the memory bullets
// (after a blank line, as skill arguments) — review agents are otherwise
// never told memory exists. Without memories the bare skill invocation stays.
func TestReviewSkillPromptIncludesMemoryBullets(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "rev-skill-mem")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:        "rev-skill-mem",
		Kind:      "review",
		CreatedAt: time.Now().UTC(),
		Memories: []space.MemoryManifest{
			{Name: "default", Provider: "marmot", ID: "den-a", Owned: true},
			{Name: "extra", Provider: "marmot", ID: "den-b", Owned: false},
		},
	}); err != nil {
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

	claude, err := svc.Invocation(Options{SpaceID: "rev-skill-mem", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.Args) != 1 {
		t.Fatalf("claude args = %#v", claude.Args)
	}
	want := "/" + ReviewSkillName + "\n\n" +
		"- Persistent memory is available via the context-marmot MCP tools (den: den-a).\n" +
		"- Persistent memory is available via the context-marmot MCP tools (den: den-b)."
	if claude.Args[0] != want {
		t.Fatalf("prompt = %q, want %q", claude.Args[0], want)
	}
	if !strings.HasPrefix(claude.Args[0], "/"+ReviewSkillName+"\n") {
		t.Fatalf("prompt must still start with the skill invocation: %q", claude.Args[0])
	}
}

func sagaManifest(id string) space.Manifest {
	return space.Manifest{
		ID:        id,
		Kind:      space.KindSaga,
		CreatedAt: time.Now().UTC(),
		Saga:      &space.SagaManifest{Members: []space.SagaMember{{ID: "member-a"}}},
	}
}

func TestSagaSpaceDefaultsToEmbeddedSkill(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "saga-1")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, sagaManifest("saga-1")); err != nil {
		t.Fatal(err)
	}
	if err := InstallSagaSkill(spacePath); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, &fakeLauncher{}, nil)

	claude, err := svc.Invocation(Options{SpaceID: "saga-1", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.Args) != 1 || claude.Args[0] != "/"+SagaSkillName {
		t.Fatalf("claude saga args = %#v, want the skill invocation", claude.Args)
	}

	// Codex has no skill system: it gets the saga coordinator stance instead
	// of the editable-repos prompt.
	codex, err := svc.Invocation(Options{SpaceID: "saga-1", Summoner: Codex})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, " ")
	if !strings.Contains(joined, "saga space") || !strings.Contains(joined, "stave saga status saga-1 --json") {
		t.Fatalf("codex saga args = %#v, want the saga-stance prompt", codex.Args)
	}
	if strings.Contains(joined, "editable repositories") {
		t.Fatalf("codex saga args = %#v, must not carry the editable-repos bullets", codex.Args)
	}

	// An explicit --prompt override wins over the skill launch.
	override, err := svc.Invocation(Options{SpaceID: "saga-1", Summoner: Claude, Prompt: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if len(override.Args) != 1 || override.Args[0] != "custom" {
		t.Fatalf("override args = %#v, want the override prompt only", override.Args)
	}

	// Without the installed skill (e.g. a pre-existing saga space), claude
	// falls back to the saga-stance prompt instead of a dangling /command.
	if err := os.RemoveAll(filepath.Join(spacePath, ".claude", "skills", SagaSkillName)); err != nil {
		t.Fatal(err)
	}
	claude, err = svc.Invocation(Options{SpaceID: "saga-1", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(claude.Args, " ")
	if strings.Contains(joined, "/"+SagaSkillName) || !strings.Contains(joined, "saga space") {
		t.Fatalf("claude args = %#v, want the saga-stance fallback when skill missing", claude.Args)
	}
}

// The saga detection predicate is the manifest's saga block, not Kind alone:
// a kind:saga space without a roster (pseudo-saga) gets the plain prompt.
func TestPseudoSagaGetsPlainPrompt(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "pseudo-saga")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "pseudo-saga", Kind: space.KindSaga, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := InstallSagaSkill(spacePath); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, &fakeLauncher{}, nil)

	claude, err := svc.Invocation(Options{SpaceID: "pseudo-saga", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(claude.Args, " ")
	if strings.Contains(joined, "/"+SagaSkillName) || strings.Contains(joined, "saga space") {
		t.Fatalf("pseudo-saga args = %#v, want the plain default prompt", claude.Args)
	}
	if !strings.Contains(joined, "editable repositories") {
		t.Fatalf("pseudo-saga args = %#v, want the editable-repos bullets", claude.Args)
	}
}

// F13 for sagas: the Claude saga-skill launch prompt must carry the memory
// bullets, and the non-Claude stance prompt must carry both the saga stance
// and the bullets.
func TestSagaSkillPromptIncludesMemoryBullets(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "saga-mem")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := sagaManifest("saga-mem")
	manifest.Memories = []space.MemoryManifest{
		{Name: "default", Provider: "marmot", ID: "den-a", Owned: true},
		{Name: "extra", Provider: "marmot", ID: "den-b", Owned: false},
	}
	if err := space.SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := InstallSagaSkill(spacePath); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, &fakeLauncher{}, nil)

	claude, err := svc.Invocation(Options{SpaceID: "saga-mem", Summoner: Claude})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.Args) != 1 {
		t.Fatalf("claude args = %#v", claude.Args)
	}
	want := "/" + SagaSkillName + "\n\n" +
		"- Persistent memory is available via the context-marmot MCP tools (den: den-a).\n" +
		"- Persistent memory is available via the context-marmot MCP tools (den: den-b)."
	if claude.Args[0] != want {
		t.Fatalf("prompt = %q, want %q", claude.Args[0], want)
	}

	codex, err := svc.Invocation(Options{SpaceID: "saga-mem", Summoner: Codex})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, " ")
	if !strings.Contains(joined, "context-marmot MCP tools (den: den-a)") || !strings.Contains(joined, "saga space") {
		t.Fatalf("codex saga invocation missing memory bullet or stance: %#v", codex.Args)
	}
}

func TestInstallSagaSkillWritesEmbeddedSkill(t *testing.T) {
	spacePath := t.TempDir()
	if err := InstallSagaSkill(spacePath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(spacePath, ".claude", "skills", SagaSkillName, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: "+SagaSkillName) {
		t.Fatalf("installed skill missing frontmatter name: %q", string(data)[:120])
	}
}
