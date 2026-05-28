package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/agent"
	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/spf13/cobra"
)

func TestCLIHelpCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"repos", "--help"},
		{"space", "--help"},
		{"space", "create", "--help"},
		{"agent", "--help"},
		{"summon", "--help"},
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
	spec := filepath.Join(t.TempDir(), "ticket.md")
	if err := os.WriteFile(spec, []byte("ticket"), 0o644); err != nil {
		t.Fatal(err)
	}

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "space", "create", "ex-1234", "-k", "ticket", "-s", spec, "-e", "repo-a", "-r", "repo-b")

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
	if manifest.SpecPath != "spec" {
		t.Fatalf("SpecPath = %q", manifest.SpecPath)
	}
	status := runCLI(t, "space", "status", "ex-1234")
	if !strings.Contains(status, "spec:") || !strings.Contains(status, "repo-a [edit]") || !strings.Contains(status, "repo-b [reference]") {
		t.Fatalf("status output missing repos:\n%s", status)
	}
}

func TestCLICreateDryRunDoesNotCreateSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)

	out := runCLI(t, "space", "create", "ex-1234", "-e", "repo-a", "--dry-run")
	if !strings.Contains(out, "dry-run: create space directory") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
}

func TestCLICreateDryRunPrintsSummon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)

	out := runCLI(t, "space", "create", "ex-1234", "-e", "repo-a", "--summon", "codex", "--dry-run")
	if !strings.Contains(out, "dry-run: summon ex-1234 with codex") || !strings.Contains(out, "codex --cd") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
}

func TestCLISummonPrintCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	out := runCLI(t, "summon", "ex-1234", "--with", "codex", "--print-command")
	if !strings.Contains(out, filepath.Join(home, "stave", "agent-work", "ex-1234")) || !strings.Contains(out, "codex --cd") {
		t.Fatalf("summon output = %s", out)
	}
}

func TestCLICreateSummonLaunchesAfterCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	cmd := newRootCommand(&app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--summon", "claude"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("space create --summon error = %v\n%s", err, out.String())
	}
	if !launcher.called || launcher.invocation.Summoner != summon.Claude {
		t.Fatalf("launcher = %#v", launcher)
	}
	if launcher.invocation.Dir != filepath.Join(home, "stave", "agent-work", "ex-1234") {
		t.Fatalf("launcher dir = %q", launcher.invocation.Dir)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", space.ManifestName)); err != nil {
		t.Fatalf("space was not created: %v", err)
	}
}

func TestCLICreateSummonFailureKeepsSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Summon.Commands["codex"] = filepath.Join(t.TempDir(), "missing-codex")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--summon", "codex"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("space create --summon unexpectedly succeeded:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", space.ManifestName)); err != nil {
		t.Fatalf("space was not kept: %v", err)
	}
}

func TestCLIAgentConfigureWithFakeSecretStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := &fakeSecretStore{available: true}
	cmd := newRootCommand(&app{secretStore: store})
	cmd.SetArgs([]string{"agent", "configure"})
	cmd.SetIn(strings.NewReader("openai\ngpt-test\nsk-test\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent configure error = %v\n%s", err, out.String())
	}
	if store.values["keychain:stave/agent/openai"] != "sk-test" {
		t.Fatalf("secret store = %#v", store.values)
	}
	configBytes, err := os.ReadFile(filepath.Join(home, ".config", "stave", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(configBytes)
	if !strings.Contains(text, "model: gpt-test") || !strings.Contains(text, "apiKeyRef: keychain:stave/agent/openai") {
		t.Fatalf("config missing agent settings:\n%s", text)
	}
}

func TestCLIAgentPlanOnlyNonTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent query error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "stave repos list") || !strings.Contains(out.String(), "no operations were executed") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestCLIAgentJSONAndIncantExecutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant --json error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if !result.Executed || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentAutoIncantExecutesWithoutFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.AutoIncant = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent auto-incant error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if !result.Executed || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentNoIncantOverridesAutoIncant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.AutoIncant = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--no-incant", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --no-incant auto-incant error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Executed {
		t.Fatalf("--no-incant did not override autoIncant: %#v", result)
	}
}

func TestCLIAgentJSONDoesNotPromptOnTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--json", "list repos"})
	cmd.SetIn(strings.NewReader("y\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --json error = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "Proceed?") {
		t.Fatalf("--json prompted unexpectedly:\n%s", out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Executed {
		t.Fatalf("--json without --incant executed unexpectedly: %#v", result)
	}
}

func TestCLIAgentJSONIncantSkipsSummonLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "create and summon", Operations: []agent.Operation{
			{Type: agent.OpSpaceCreate, SpaceID: "ex-2"},
			{Type: agent.OpSummon, SpaceID: "ex-2", Summoner: "codex"},
		}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "create ex-2 and summon codex"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant --json summon error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if launcher.called {
		t.Fatal("summon launcher was called during JSON output")
	}
	if len(result.Results) != 2 || !result.Results[0].Executed || result.Results[1].Executed {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentIncantLaunchesSummonOnTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "create and summon", Operations: []agent.Operation{
			{Type: agent.OpSpaceCreate, SpaceID: "ex-2"},
			{Type: agent.OpSummon, SpaceID: "ex-2", Summoner: "cursor"},
		}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--incant", "create ex-2 and summon cursor"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant summon error = %v\n%s", err, out.String())
	}
	if !launcher.called || launcher.invocation.Summoner != summon.Cursor {
		t.Fatalf("launcher = %#v", launcher)
	}
}

func TestCLITicketFlagIsRemoved(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--ticket", "ticket.md"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("--ticket unexpectedly succeeded:\n%s", out.String())
	}
}

type fakeProvider struct {
	plan agent.Plan
}

func (f fakeProvider) Run(ctx context.Context, request agent.ProviderRequest) (agent.RunResult, error) {
	return agent.RunResult{Plan: f.plan, Commands: f.plan.Commands()}, nil
}

type fakeSecretStore struct {
	available bool
	values    map[string]string
}

type fakeSummonLauncher struct {
	called     bool
	invocation summon.Invocation
}

func (f *fakeSummonLauncher) Launch(ctx context.Context, invocation summon.Invocation) error {
	f.called = true
	f.invocation = invocation
	return nil
}

func (f *fakeSecretStore) Available() bool {
	return f.available
}

func (f *fakeSecretStore) Put(ctx context.Context, ref string, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[ref] = value
	return nil
}

func (f *fakeSecretStore) Get(ctx context.Context, ref string) (string, error) {
	return f.values[ref], nil
}

func (f *fakeSecretStore) Delete(ctx context.Context, ref string) error {
	delete(f.values, ref)
	return nil
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
