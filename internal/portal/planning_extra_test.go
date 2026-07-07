package portal

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

func TestConfigureAuthLogsExecAndDriverBranches(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitDevcontainer(context.Background(), InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev", Service: "api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", PortalID: "aws", InstanceID: "i-123", Host: "203.0.113.10", Region: "us-west-2", SSHUser: "ec2-user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "dev", Agent: "cursor", AuthMode: AuthVolume, ContainerRoot: "/workspace/dev"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["dev"].Auth.Mode != AuthVolume || !hasProvider(manifest.Portals["dev"].Auth.Providers, "cursor") {
		t.Fatalf("configured portal = %#v", manifest.Portals["dev"])
	}
	if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "dev", Agent: "bad"}); err == nil {
		t.Fatal("expected bad provider error")
	}

	up, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", PortalID: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(up.EquivalentCommands()[0], "devcontainer up") {
		t.Fatalf("devcontainer up = %v", up.EquivalentCommands())
	}
	awsUp, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", PortalID: "aws"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(awsUp.EquivalentCommands()[0], "aws ec2 describe-instances") {
		t.Fatalf("aws up = %v", awsUp.EquivalentCommands())
	}
	execPlan, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", PortalID: "aws", Command: []string{"echo", "hello world"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execPlan.EquivalentCommands()[0], "ssh") || !strings.Contains(execPlan.EquivalentCommands()[0], "hello world") {
		t.Fatalf("remote exec = %v", execPlan.EquivalentCommands())
	}
	if _, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", PortalID: "aws"}); err == nil {
		t.Fatal("expected missing exec command error")
	}
	logs, err := svc.PlanLogs(context.Background(), LogsOptions{SpaceID: "ex-1", PortalID: "aws", Agent: "codex", Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.EquivalentCommands()[0], "tmux capture-pane") {
		t.Fatalf("remote logs = %v", logs.EquivalentCommands())
	}
	revoke, err := svc.PlanAuthRevoke(context.Background(), AuthCommandOptions{SpaceID: "ex-1", PortalID: "dev", Provider: "claude", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(revoke.EquivalentCommands()[0], "claude auth logout") {
		t.Fatalf("revoke = %v", revoke.EquivalentCommands())
	}
}

func TestReconstructDirtySyncAndPresetBranches(t *testing.T) {
	cfg := testConfig(t)
	writeSpaceWithRepos(t, cfg, "ex-1")
	git := &dirtyGit{dirty: true}
	svc := NewService(cfg, fakeRunner{}, nil)
	svc.Git = git
	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", PortalID: "ssh", Preset: "ssh-claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanSync(context.Background(), SyncOptions{SpaceID: "ex-1", PortalID: "ssh", Direction: SyncFrom}); err == nil || !strings.Contains(err.Error(), "dirty editable") {
		t.Fatalf("expected dirty error, got %v", err)
	}
	reconstruct, err := svc.PlanSync(context.Background(), SyncOptions{SpaceID: "ex-1", PortalID: "ssh", Mode: SyncReconstruct, AllowDirty: true, ReferencesOnly: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reconstruct.EquivalentCommands()[0], "--relative") || !strings.Contains(reconstruct.Diagnostics[0].Code, "reconstruct") {
		t.Fatalf("reconstruct = %#v commands=%v", reconstruct, reconstruct.EquivalentCommands())
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["ssh"].Auth.Mode != AuthRemoteLogin || manifest.Portals["ssh"].Auth.Providers[0].Provider != "claude" {
		t.Fatalf("preset auth = %#v", manifest.Portals["ssh"].Auth)
	}
}

func TestManifestValidationBranchesAndLocalRunner(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	manifest := Manifest{Version: 2, SpaceID: "ex-1", Portals: map[string]Portal{}}
	if err := SaveManifest(spacePath, manifest); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version error, got %v", err)
	}
	bad := Portal{ID: "bad", Driver: DriverDocker}
	applyPortalDefaults("ex-1", &bad)
	bad.Runtime.Labels["stave.space"] = "other"
	if err := ValidatePortal("ex-1", bad); err == nil || !strings.Contains(err.Error(), "stave.space") {
		t.Fatalf("expected label error, got %v", err)
	}
	for _, driver := range []Driver{DriverDocker, DriverDevcontainer, DriverSSH, DriverEC2Attach} {
		if len(requiredBinaries(Portal{Driver: driver})) == 0 {
			t.Fatalf("no required binaries for %s", driver)
		}
	}
	if _, err := (localRunner{}).LookPath("definitely-missing-stave-test-binary"); err == nil {
		t.Fatal("expected missing binary")
	}
	result, err := (localRunner{}).Run(context.Background(), Command{Program: "sh", Args: []string{"-c", "printf ok"}})
	if err != nil || result.Stdout != "ok" {
		t.Fatalf("local runner result=%#v err=%v", result, err)
	}
}

type dirtyGit struct {
	dirty bool
}

func (g *dirtyGit) IsDirty(context.Context, string) (bool, string, error) {
	return g.dirty, "dirty", nil
}

func writeSpaceWithRepos(t *testing.T, cfg config.Config, id string) {
	t.Helper()
	spacePath := filepath.Join(cfg.AgentWorkDir, id)
	if err := os.MkdirAll(filepath.Join(spacePath, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:        id,
		Kind:      "ticket",
		CreatedAt: time.Now().UTC(),
		Repos: []space.RepoManifest{{
			Name: "api",
			Mode: space.ModeEdit,
			Path: "api",
		}},
	}); err != nil {
		t.Fatal(err)
	}
}
