package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/Nurozen/stave/internal/tether"
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

func TestExecutorSpaceCreateExpandsCommonReferences(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Tethers = config.TethersConfig{StrongThreshold: 3}
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git"), DefaultBranch: "main"}
	cfg.Repos["web"] = config.Repository{Name: "web", URL: "https://example.test/web.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "web.git"), DefaultBranch: "main"}
	// Seed a strong api->web tether so -c/Common expands web as a reference.
	if err := tether.Update(tether.Path(cfg), func(f *tether.File) error {
		now := time.Now()
		for i := 0; i < 3; i++ {
			tether.Bump(f, "api", "web", tether.ModeReference, now)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(&executorGitRunner{})), Out: &out}

	if err := executor.executeOperation(context.Background(), Operation{
		Type:    OpSpaceCreate,
		SpaceID: "ex-c",
		Edits:   []RepoRef{{Name: "api"}},
		Common:  true,
	}); err != nil {
		t.Fatalf("executeOperation error = %v", err)
	}
	manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-c"))
	if err != nil {
		t.Fatal(err)
	}
	var foundRef bool
	for _, repo := range manifest.Repos {
		if repo.Name == "web" && repo.Mode == space.ModeReference {
			foundRef = true
		}
	}
	if !foundRef {
		t.Fatalf("expected web expanded as reference worktree, manifest = %#v", manifest.Repos)
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
		{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "agent-ssh", Driver: string(portal.DriverSSH), Host: "devbox.example", KnownHostsPath: "/tmp/known_hosts", StrictHostKey: "yes", RemoteRoot: "/home/ubuntu/stave/ex-1", SyncMode: string(portal.SyncRsync)},
		{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "agent-ec2", Driver: string(portal.DriverEC2Attach), InstanceID: "i-123", Host: "203.0.113.10", Port: 2222, SSHUser: "ubuntu", IdentityPath: "~/.ssh/aws", KnownHostsPath: "/tmp/aws_known_hosts", StrictHostKey: "yes", RemoteRoot: "/home/ubuntu/stave/ex-1", SyncMode: string(portal.SyncRsync)},
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
	for _, needle := range []string{"api\thttps://example.test/api.git", "space ex-1", "ssh -n -o BatchMode=yes -o ConnectTimeout=10 -p 22 -o UserKnownHostsFile=/tmp/known_hosts -o StrictHostKeyChecking=yes devbox.example", "ssh -n -o BatchMode=yes -o ConnectTimeout=10 -p 2222", "UserKnownHostsFile=/tmp/aws_known_hosts", "StrictHostKeyChecking=yes ubuntu@203.0.113.10", "portal runner stdout", "portal runner stderr", "docker exec", "docker stop"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("executor output missing %q:\n%s", needle, got)
		}
	}
	if len(gitRunner.calls) < 4 {
		t.Fatalf("git calls = %#v", gitRunner.calls)
	}
	if len(runner.commands) < 4 {
		t.Fatalf("portal runner commands = %#v", runner.commands)
	}
	if _, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1")); err == nil {
		manifest, loadErr := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if manifest.Portals["local"].Auth.Mode != portal.AuthEnv || manifest.Portals["local"].Auth.Providers[0].Status != portal.AuthOK {
			t.Fatalf("local portal auth was not inherited: %#v", manifest.Portals["local"].Auth)
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
		{Type: "portal_teleport", SpaceID: "ex-1", PortalID: "local"},
		{Type: "not_real"},
	}
	for _, op := range tests {
		if err := executor.executeOperation(context.Background(), op); err == nil {
			t.Fatalf("executeOperation(%s) succeeded unexpectedly", op.Type)
		}
	}
}

// TestExecutePlanRunsPortalReadOps guards preflight/validation parity: every
// read-only portal operation ValidatePlan accepts must clear the capability
// preflight and execute, not be refused wholesale.
func TestExecutePlanRunsPortalReadOps(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	saveExecutorPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	runner := &executorPortalRunner{}
	var out bytes.Buffer
	executor := Executor{Config: cfg, PortalRunner: runner, Out: &out}

	plan := Plan{Operations: []Operation{
		{Type: OpPortalList, SpaceID: "ex-1"},
		{Type: OpPortalStatus, SpaceID: "ex-1", PortalID: "local"},
		{Type: OpPortalDoctor, SpaceID: "ex-1", PortalID: "local"},
		{Type: OpPortalInspect, SpaceID: "ex-1", PortalID: "local"},
		{Type: OpPortalAuthStatus, SpaceID: "ex-1", PortalID: "local", Provider: "codex"},
		{Type: OpPortalLogs, SpaceID: "ex-1", PortalID: "local", Tail: 10},
	}}
	if err := ValidatePlan(cfg, plan); err != nil {
		t.Fatalf("ValidatePlan error = %v", err)
	}
	results, err := executor.ExecutePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecutePlan error = %v", err)
	}
	if len(results) != len(plan.Operations) {
		t.Fatalf("results = %#v", results)
	}
	for _, result := range results {
		if !result.Executed {
			t.Fatalf("operation %s was not executed: %#v", result.Operation.Type, result)
		}
	}
	got := out.String()
	for _, needle := range []string{`"portals"`, `"status"`, `"doctor"`, `"inspect"`, `"auth"`, "docker logs --tail 10"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("portal read output missing %q:\n%s", needle, got)
		}
	}
}

// TestExecutableOperationsCoverValidatedOps pins the preflight map to the full
// set of operation types ValidatePlan can accept, so validation and the
// execution capability preflight can never disagree again.
func TestExecutableOperationsCoverValidatedOps(t *testing.T) {
	validated := []string{
		OpSpaceCreate, OpSpaceAdd, OpSpaceSync, OpSpaceStatus,
		OpReposList, OpReposTethers, OpReposSync, OpSummon,
		OpSagaCreate, OpSagaStatus, OpSagaAdd,
		OpPortalInit, OpPortalAttach, OpPortalConfigure,
		OpPortalList, OpPortalStatus, OpPortalDoctor, OpPortalInspect, OpPortalAuthStatus, OpPortalLogs,
		OpPortalAuthLogin, OpPortalAuthInherit, OpPortalAuthRevoke,
		OpPortalUp, OpPortalSync, OpPortalSummon, OpPortalDown, OpPortalDetach, OpPortalDestroyPreview,
	}
	for _, op := range validated {
		if !executableOperations[op] {
			t.Errorf("operation %s passes validation but is missing from executableOperations", op)
		}
	}
	if len(executableOperations) != len(validated) {
		t.Errorf("executableOperations has %d entries, validation accepts %d; the sets must match", len(executableOperations), len(validated))
	}
}

func TestExecutePlanPreflightFailsBeforeFirstMutation(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git"), DefaultBranch: "main"}
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(&executorGitRunner{}))}

	// A bogus operation anywhere in the plan fails the capability preflight
	// BEFORE the first mutating operation runs.
	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{
		{Type: OpSpaceCreate, SpaceID: "ex-9", Edits: []RepoRef{{Name: "api"}}},
		{Type: "space_obliterate"},
	}})
	if err == nil || !strings.Contains(err.Error(), "no executor") {
		t.Fatalf("preflight err = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %#v", results)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.AgentWorkDir, "ex-9")); !os.IsNotExist(statErr) {
		t.Fatalf("space ex-9 was created before the preflight failure: %v", statErr)
	}
}

func TestExecutorRunsSagaCreateAddAndStatus(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "m-1")
	var out bytes.Buffer
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(&executorGitRunner{})), Out: &out}

	results, err := executor.ExecutePlan(context.Background(), Plan{Operations: []Operation{
		{Type: OpSagaCreate, SagaID: "story"},
		{Type: OpSagaAdd, SagaID: "story", SpaceID: "m-1"},
		{Type: OpSagaStatus, SagaID: "story"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || !results[0].Executed || !results[1].Executed || !results[2].Executed {
		t.Fatalf("results = %#v", results)
	}
	manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, "story"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Saga == nil || len(manifest.Saga.Members) != 1 || manifest.Saga.Members[0].ID != "m-1" {
		t.Fatalf("saga manifest = %#v", manifest)
	}
	got := out.String()
	for _, needle := range []string{"created space story", "added m-1 to saga story", "saga story (1 members)", "m-1 [live]"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("executor output missing %q:\n%s", needle, got)
		}
	}
}

func TestExecuteOperationSummonLaunchesInteractively(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	launcher := &fakeSummonLauncher{}
	var out bytes.Buffer
	executor := Executor{Config: cfg, SummonLauncher: launcher, AllowInteractive: true, Out: &out}

	err := executor.executeOperation(context.Background(), Operation{
		Type:     OpSummon,
		SpaceID:  "ex-1",
		Summoner: "codex",
	})
	if err != nil {
		t.Fatalf("executeOperation(summon) error = %v", err)
	}
	if launcher.calls != 1 {
		t.Fatalf("expected launcher to be invoked once, got %d", launcher.calls)
	}
	if launcher.lastSummoner != "codex" {
		t.Fatalf("summoner = %q", launcher.lastSummoner)
	}
}

func TestExecuteOperationSpaceAddReferenceMode(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git"), DefaultBranch: "main"}
	writeExecutorSpace(t, cfg, "ex-1")
	gitRunner := &executorGitRunner{}
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(gitRunner))}

	err := executor.executeOperation(context.Background(), Operation{
		Type:    OpSpaceAdd,
		SpaceID: "ex-1",
		Repo:    "api",
		Mode:    string(space.ModeReference),
		Ref:     "origin/main",
	})
	if err != nil {
		t.Fatalf("executeOperation(space_add reference) error = %v", err)
	}
	manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || manifest.Repos[0].Mode != space.ModeReference {
		t.Fatalf("repo was not added in reference mode: %#v", manifest.Repos)
	}
}

func TestExecuteOperationSpaceStatusErrorsForMissingSpace(t *testing.T) {
	cfg := executorConfig(t)
	executor := Executor{Config: cfg}
	err := executor.executeOperation(context.Background(), Operation{Type: OpSpaceStatus, SpaceID: "does-not-exist"})
	if err == nil {
		t.Fatal("expected status of a missing space to error")
	}
}

func TestExecuteOperationReposSyncAllPropagatesFetchError(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["api"] = config.Repository{Name: "api", BareRepoPath: filepath.Join(cfg.BareReposDir, "api.git")}
	cfg.Repos["web"] = config.Repository{Name: "web", BareRepoPath: filepath.Join(cfg.BareReposDir, "web.git")}
	executor := Executor{Config: cfg, Git: git.New(git.WithRunner(&failingFetchRunner{}))}

	err := executor.executeOperation(context.Background(), Operation{Type: OpReposSync})
	if err == nil {
		t.Fatal("expected repos_sync to propagate the fetch error")
	}
}

func TestExecuteOperationPortalInitDevcontainerAndDefaultDriver(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "dc-1")
	writeExecutorSpace(t, cfg, "def-1")
	executor := Executor{Config: cfg}

	// Devcontainer driver path records a devcontainer portal.
	if err := executor.executeOperation(context.Background(), Operation{
		Type:          OpPortalInit,
		SpaceID:       "dc-1",
		PortalID:      "dev",
		Driver:        string(portal.DriverDevcontainer),
		ContainerRoot: "/workspaces/dc-1",
	}); err != nil {
		t.Fatalf("executeOperation(devcontainer init) error = %v", err)
	}
	dcManifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "dc-1"))
	if err != nil {
		t.Fatal(err)
	}
	if dcManifest.Portals["dev"].Driver != portal.DriverDevcontainer {
		t.Fatalf("devcontainer portal not recorded: %#v", dcManifest.Portals)
	}

	// Empty driver defaults to docker.
	if err := executor.executeOperation(context.Background(), Operation{
		Type:          OpPortalInit,
		SpaceID:       "def-1",
		PortalID:      "dev",
		Driver:        "",
		Image:         "ubuntu:latest",
		ContainerRoot: "/workspace/def-1",
	}); err != nil {
		t.Fatalf("executeOperation(default driver init) error = %v", err)
	}
	defManifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, "def-1"))
	if err != nil {
		t.Fatal(err)
	}
	if defManifest.Portals["dev"].Driver != portal.DriverDocker {
		t.Fatalf("empty driver did not default to docker: %#v", defManifest.Portals)
	}
}

func TestExecutePortalPlanPropagatesBuildError(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	// No portal named "ghost" exists, so PlanUp's build func returns an error.
	executor := Executor{Config: cfg, PortalRunner: &executorPortalRunner{}}
	err := executor.executeOperation(context.Background(), Operation{Type: OpPortalUp, SpaceID: "ex-1", PortalID: "ghost"})
	if err == nil {
		t.Fatal("expected portal up on a missing portal to error")
	}
}

func TestExecutePortalPlanPropagatesRunnerError(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	saveExecutorPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	var out bytes.Buffer
	executor := Executor{Config: cfg, PortalRunner: &failingPortalRunner{}, Out: &out}

	err := executor.executeOperation(context.Background(), Operation{Type: OpPortalUp, SpaceID: "ex-1", PortalID: "local"})
	if err == nil {
		t.Fatal("expected runner failure to propagate from executePortalPlan")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
}

// TestExecutorSurfacesPortalWarnDiagnostics pins that the executor prints
// warn-severity diagnostics (summon.cursor_partial) before running a portal
// plan, so agents see actionable warnings that only rode on plan.Diagnostics.
func TestExecutorSurfacesPortalWarnDiagnostics(t *testing.T) {
	cfg := executorConfig(t)
	writeExecutorSpace(t, cfg, "ex-1")
	saveExecutorPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	var out bytes.Buffer
	executor := Executor{Config: cfg, PortalRunner: &executorPortalRunner{}, AllowInteractive: true, Out: &out}

	err := executor.executeOperation(context.Background(), Operation{
		Type:     OpPortalSummon,
		SpaceID:  "ex-1",
		PortalID: "local",
		Summoner: "cursor",
		Mode:     "foreground",
	})
	if err != nil {
		t.Fatalf("executeOperation(summon cursor) error = %v", err)
	}
	if !strings.Contains(out.String(), "summon.cursor_partial") {
		t.Fatalf("executor did not surface cursor warn diagnostic:\n%s", out.String())
	}
}

func TestWriteStatusRendersSpecDirtyAndMissingRepos(t *testing.T) {
	var out bytes.Buffer
	status := space.Status{
		Manifest: space.Manifest{ID: "ex-1", Kind: "ticket", SpecPath: "spec.md"},
		Repos: []space.RepoStatus{
			{Repo: space.RepoManifest{Name: "api", Mode: space.ModeEdit}, Exists: true, Dirty: true},
			{Repo: space.RepoManifest{Name: "web", Mode: space.ModeReference}, Exists: false, Dirty: false},
		},
	}
	writeStatus(&out, "/work/ex-1", status)

	got := out.String()
	for _, needle := range []string{
		"space ex-1 (ticket)",
		"path: /work/ex-1",
		"spec: /work/ex-1/spec.md",
		"api [edit] present dirty",
		"web [reference] missing clean",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("status output missing %q:\n%s", needle, got)
		}
	}
}

type fakeSummonLauncher struct {
	calls        int
	lastSummoner string
}

func (l *fakeSummonLauncher) Launch(ctx context.Context, invocation summon.Invocation) error {
	l.calls++
	l.lastSummoner = invocation.Summoner
	return nil
}

type failingFetchRunner struct{}

func (r *failingFetchRunner) Run(ctx context.Context, bin string, args []string, opts git.RunOptions) (git.Result, error) {
	return git.Result{}, &git.GitError{Args: args, ExitCode: 1}
}

type failingPortalRunner struct{}

func (r *failingPortalRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }

func (r *failingPortalRunner) Run(ctx context.Context, command portal.Command) (portal.RunResult, error) {
	return portal.RunResult{}, fmt.Errorf("boom")
}

type executorPortalRunner struct {
	commands []portal.Command
}

func (r *executorPortalRunner) LookPath(name string) (string, error) {
	return "/bin/" + name, nil
}

func (r *executorPortalRunner) Run(ctx context.Context, command portal.Command) (portal.RunResult, error) {
	r.commands = append(r.commands, command)
	return portal.RunResult{Stdout: "portal runner stdout\n", Stderr: "portal runner stderr\n"}, nil
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
