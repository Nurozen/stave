package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
)

func TestExecutorRunsPortalInitAndLifecyclePlan(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	runner := &executorPortalRunner{}
	executor := Executor{Config: cfg, PortalRunner: runner, AllowInteractive: true}

	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{{
		Type:          OpPortalInit,
		SpaceID:       "ex-1",
		PortalID:      "dev",
		Driver:        "docker",
		Image:         "ubuntu:latest",
		ContainerRoot: "/workspace/ex-1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Executed {
		t.Fatalf("results = %#v", results)
	}
	manifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["dev"].Runtime.Image != "ubuntu:latest" {
		t.Fatalf("portal = %#v", manifest.Portals["dev"])
	}

	results, err = executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{{
		Type:     OpPortalUp,
		SpaceID:  "ex-1",
		PortalID: "dev",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Executed || len(runner.commands) != 2 {
		t.Fatalf("results = %#v runner = %#v", results, runner.commands)
	}
}

func TestExecutorSkipsPortalSummonWhenInteractiveDisabled(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	executor := Executor{Config: cfg, AllowInteractive: false}
	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{{
		Type:     OpPortalSummon,
		SpaceID:  "ex-1",
		PortalID: "dev",
		Summoner: "codex",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Executed || results[0].Message == "" {
		t.Fatalf("results = %#v", results)
	}
}

func TestExecutorSkipsSummonWhenInteractiveDisabled(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	executor := Executor{Config: cfg, AllowInteractive: false}

	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{{
		Type:     OpSummon,
		SpaceID:  "ex-1",
		Summoner: "codex",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Executed || !strings.Contains(results[0].Message, "interactive launch is disabled") {
		t.Fatalf("results = %#v", results)
	}
}

func TestExecutorRunsSpaceCreateAddAndRepoSyncAll(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git"), DefaultBranch: "main"}
	cfg.Repos["web"] = config.Repository{Name: "web", URL: "https://example.test/web.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "web.git"), DefaultBranch: "trunk"}
	spec := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(spec, []byte("spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunner := &executorGitRunner{}
	var out bytes.Buffer
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(gitRunner)), Out: &out}

	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{
		{Type: OpSpaceCreate, SpaceID: "ex-2", Kind: "ticket", SpecPath: spec, Edits: []RepoRef{{Name: "api", Ref: "feature"}}, References: []RepoRef{{Name: "web", Ref: "release"}}},
		{Type: OpSpaceAdd, SpaceID: "ex-2", Repo: "web", Mode: string(space.ModeEdit), Base: "topic", Branch: "custom/web"},
		{Type: OpReposSync},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 3 || manifest.SpecPath != "spec" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if !containsGitCall(gitRunner.calls, "fetch", cfg.Repos["api"].BareRepoPath) || !containsGitCall(gitRunner.calls, "fetch", cfg.Repos["web"].BareRepoPath) {
		t.Fatalf("git calls = %#v", gitRunner.calls)
	}
	if !strings.Contains(out.String(), "created space ex-2") || !strings.Contains(out.String(), "added edit repo web") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestExecutorHelpers(t *testing.T) {
	if got := firstNonEmpty("", "", "fallback"); got != "fallback" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty(empty) = %q", got)
	}
	specs := repoRefsToSpecs([]RepoRef{{Name: "api", Ref: "main"}, {Name: "web"}})
	if len(specs) != 2 || specs[0].Name != "api" || specs[0].Ref != "main" || specs[1].Name != "web" {
		t.Fatalf("specs = %#v", specs)
	}
}

func TestExecutorExecuteOperationMatrix(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git"), DefaultBranch: "main"}
	writeExecutorSpaceWithRepos(t, cfg, "ex-1", []space.RepoManifest{{
		Name:         "api",
		Mode:         space.ModeEdit,
		Path:         "api",
		Base:         "origin/main",
		Branch:       "stave/ex-1/api",
		BareRepoPath: cfg.Repos["api"].BareRepoPath,
	}})
	if err := os.MkdirAll(filepath.Join(cfg.AgentWorkDir, "ex-1", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveExecutorPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	saveExecutorPortal(t, cfg, "ex-1", "remote", portal.DriverSSH)

	gitRunner := &executorGitRunner{}
	runner := &executorPortalRunner{}
	var out bytes.Buffer
	executor := Executor{
		Config:       cfg,
		Git:          git.New(git.WithRunner(gitRunner)),
		Out:          &out,
		PortalRunner: runner,
	}

	ops := []Operation{
		{Type: OpReposList},
		{Type: OpReposSync, Repo: "api"},
		{Type: OpSpaceStatus, SpaceID: "ex-1"},
		{Type: OpSpaceSync, SpaceID: "ex-1"},
		{Type: OpPortalConfigure, SpaceID: "ex-1", PortalID: "local", Agent: "claude", Method: string(portal.AuthVolume)},
		{Type: OpPortalAuthLogin, SpaceID: "ex-1", PortalID: "local", Provider: "codex", Method: string(portal.AuthNative)},
		{Type: OpPortalAuthInherit, SpaceID: "ex-1", PortalID: "local", Provider: "codex", Method: string(portal.AuthEnv)},
		{Type: OpPortalAuthRevoke, SpaceID: "ex-1", PortalID: "local", Provider: "codex", Target: "portal"},
		{Type: OpPortalSync, SpaceID: "ex-1", PortalID: "local", Direction: "to", SyncMode: string(portal.SyncMount)},
		{Type: OpPortalSummon, SpaceID: "ex-1", PortalID: "local", Summoner: "codex", Mode: "print"},
		{Type: OpPortalDown, SpaceID: "ex-1", PortalID: "local", Timeout: 5, Force: true},
		{Type: OpPortalDetach, SpaceID: "ex-1", PortalID: "remote"},
		{Type: OpPortalDestroyPreview, SpaceID: "ex-1", PortalID: "local"},
	}
	for _, op := range ops {
		if err := executor.executeOperation(context.Background(), op); err != nil {
			t.Fatalf("executeOperation(%s) error = %v", op.Type, err)
		}
	}

	got := out.String()
	for _, needle := range []string{"api\thttps://example.test/api.git", "space ex-1", "docker exec", "docker stop"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("executor output missing %q:\n%s", needle, got)
		}
	}
	if len(gitRunner.calls) < 4 {
		t.Fatalf("git calls = %#v", gitRunner.calls)
	}
	if len(runner.commands) < 5 {
		t.Fatalf("portal runner commands = %#v", runner.commands)
	}
	if _, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1")); err == nil {
		manifest, loadErr := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if _, ok := manifest.Portals["remote"]; ok {
			t.Fatalf("remote portal was not detached: %#v", manifest.Portals)
		}
	}
}

func TestExecutorExecuteOperationErrors(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	saveExecutorPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	executor := Executor{Config: cfg}

	tests := []Operation{
		{Type: OpPortalStatus, SpaceID: "ex-1", PortalID: "local"},
		{Type: "not_real"},
	}
	for _, op := range tests {
		if err := executor.executeOperation(context.Background(), op); err == nil {
			t.Fatalf("executeOperation(%s) succeeded unexpectedly", op.Type)
		}
	}
}

type executorPortalRunner struct {
	commands []portal.Command
}

func (r *executorPortalRunner) LookPath(name string) (string, error) {
	return "/bin/" + name, nil
}

func (r *executorPortalRunner) Run(ctx context.Context, command portal.Command) (portal.RunResult, error) {
	r.commands = append(r.commands, command)
	return portal.RunResult{}, nil
}

type executorGitRunner struct {
	calls [][]string
}

func (r *executorGitRunner) Run(ctx context.Context, bin string, args []string, opts git.RunOptions) (git.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "show-ref"):
		return git.Result{}, &git.GitError{Args: args, ExitCode: 1}
	case strings.Contains(joined, "status --porcelain"):
		return git.Result{Stdout: ""}, nil
	case strings.Contains(joined, "rev-list"):
		return git.Result{Stdout: "1 2\n"}, nil
	default:
		return git.Result{}, nil
	}
}

func containsGitCall(calls [][]string, wantParts ...string) bool {
	for _, call := range calls {
		joined := strings.Join(call, " ")
		matched := true
		for _, part := range wantParts {
			if !strings.Contains(joined, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func executorConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	return config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos:        map[string]config.Repository{},
		Agent:        config.DefaultAgentConfig(),
		Summon:       config.DefaultSummonConfig(),
	}
}

func writeExecutorSpace(t *testing.T, cfg config.Config, id string) {
	t.Helper()
	writeExecutorSpaceWithRepos(t, cfg, id, nil)
}

func writeExecutorSpaceWithRepos(t *testing.T, cfg config.Config, id string, repos []space.RepoManifest) {
	t.Helper()
	spacePath := filepath.Join(cfg.AgentWorkDir, id)
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{ID: id, Kind: "ticket", CreatedAt: time.Now().UTC(), Repos: repos}); err != nil {
		t.Fatal(err)
	}
}

func saveExecutorPortal(t *testing.T, cfg config.Config, spaceID, portalID string, driver portal.Driver) {
	t.Helper()
	item := portal.Portal{ID: portalID, Driver: driver}
	switch driver {
	case portal.DriverDocker:
		item.Runtime.Engine = string(driver)
		item.Runtime.ContainerName = "stave-" + spaceID + "-" + portalID
		item.Ownership.CreatedContainer = true
	case portal.DriverSSH:
		item.Target.Host = "example.test"
		item.Workspace.RemoteRoot = "~/stave/" + spaceID
	}
	item.Workspace.LocalPath = filepath.Join(cfg.AgentWorkDir, spaceID)
	item.Workspace.ContainerRoot = "/workspace/" + spaceID
	item.Auth.Providers = []portal.AuthProvider{{Provider: "codex", Mode: portal.AuthNative, Status: portal.AuthOK, Target: "portal"}}
	spacePath := filepath.Join(cfg.AgentWorkDir, spaceID)
	manifest, err := portal.LoadManifest(spacePath)
	if err != nil {
		manifest = portal.Manifest{SpaceID: spaceID, Portals: map[string]portal.Portal{}}
	}
	manifest.Portals[portalID] = item
	if err := portal.SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
}
