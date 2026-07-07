package portal

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

func TestManifestRoundTripDefaultsAndSecretValidation(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	svc.Now = func() time.Time { return time.Date(2026, 6, 2, 1, 2, 3, 0, time.UTC) }

	plan, err := svc.InitContainer(context.Background(), InitContainerOptions{
		SpaceID:  "ex-1",
		PortalID: "default",
		Engine:   DriverDocker,
		Preset:   "local-codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Mutates || plan.DryRun {
		t.Fatalf("plan = %#v", plan)
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	portal := manifest.Portals["default"]
	if manifest.SpaceID != "ex-1" || portal.Driver != DriverDocker {
		t.Fatalf("manifest = %#v", manifest)
	}
	if portal.Workspace.ContainerRoot != "/workspace/ex-1" {
		t.Fatalf("container root = %q", portal.Workspace.ContainerRoot)
	}
	if portal.Runtime.ContainerName != "stave-ex-1-default" {
		t.Fatalf("container name = %q", portal.Runtime.ContainerName)
	}
	if portal.Runtime.Labels["stave.space"] != "ex-1" || portal.Runtime.Labels["stave.portal"] != "default" {
		t.Fatalf("labels = %#v", portal.Runtime.Labels)
	}
	if len(portal.Auth.Providers) != 1 || portal.Auth.Providers[0].Provider != "codex" {
		t.Fatalf("auth providers = %#v", portal.Auth.Providers)
	}

	portal.Auth.Providers[0].SecretRef = "env:CODEX_TOKEN"
	manifest.Portals["default"] = portal
	if err := SaveManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"), manifest); err == nil || !strings.Contains(err.Error(), "secretRef") {
		t.Fatalf("expected secret ref rejection, got %v", err)
	}
}

func TestDryRunDoesNotWritePortalManifest(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)

	plan, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || len(plan.Diagnostics) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
	if !strings.Contains(plan.ManifestPreview, "spaceID: ex-1") || !strings.Contains(plan.ManifestPreview, "containerName: stave-ex-1-default") {
		t.Fatalf("manifest preview = %s", plan.ManifestPreview)
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentWorkDir, "ex-1", ManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote manifest: %v", err)
	}
}

func TestAttachSSHAndEC2AreAttachOnly(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)

	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox.example", Preset: "ssh-codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", PortalID: "aws", InstanceID: "i-123", Host: "203.0.113.10", Port: 2222, Region: "us-west-2", SSHUser: "ec2-user", IdentityPath: "~/.ssh/aws", KnownHostsPath: "/tmp/aws_known_hosts", StrictHostKey: "yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", PortalID: "aws-claude", InstanceID: "i-456", Preset: "ssh-claude", SyncMode: SyncReconstruct}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["default"].Ownership.CreatedContainer {
		t.Fatalf("ssh attach owns container: %#v", manifest.Portals["default"].Ownership)
	}
	if manifest.Portals["default"].Workspace.SyncMode != SyncRsync {
		t.Fatalf("ssh sync = %q", manifest.Portals["default"].Workspace.SyncMode)
	}
	if manifest.Portals["aws"].Driver != DriverEC2Attach ||
		manifest.Portals["aws"].Target.Region != "us-west-2" ||
		manifest.Portals["aws"].Target.Port != 2222 ||
		manifest.Portals["aws"].Target.KnownHostsPath != "/tmp/aws_known_hosts" ||
		manifest.Portals["aws"].Target.StrictHostKey != "yes" {
		t.Fatalf("ec2 portal = %#v", manifest.Portals["aws"])
	}
	if manifest.Portals["aws-claude"].Driver != DriverEC2Attach || manifest.Portals["aws-claude"].Workspace.SyncMode != SyncReconstruct || manifest.Portals["aws-claude"].Auth.Providers[0].Provider != "claude" {
		t.Fatalf("ec2 preset portal = %#v", manifest.Portals["aws-claude"])
	}
}

func TestEC2HostResolutionAndConfigureHost(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	runner := fakeRunner{outputs: map[string]RunResult{
		"aws ec2 describe-instances --instance-ids i-123 --region us-west-2": {Stdout: `{"Reservations":[{"Instances":[{"State":{"Name":"running"},"PublicDnsName":"ec2.example.com","PublicIpAddress":"203.0.113.9","PrivateIpAddress":"10.0.0.9"}]}]}`},
	}}
	svc := NewService(cfg, runner, nil)
	if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", PortalID: "aws", InstanceID: "i-123", Region: "us-west-2", SSHUser: "ubuntu"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["aws"].Target.Host != "ec2.example.com" {
		t.Fatalf("resolved host = %#v", manifest.Portals["aws"].Target)
	}
	execPlan, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", PortalID: "aws", Command: []string{"hostname"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(execPlan.EquivalentCommands()[0], "i-123") || !strings.Contains(execPlan.EquivalentCommands()[0], "ubuntu@ec2.example.com") {
		t.Fatalf("ec2 exec command = %v", execPlan.EquivalentCommands())
	}
	if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "aws", Host: "10.0.0.9"}); err != nil {
		t.Fatal(err)
	}
	manifest, err = LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["aws"].Target.Host != "10.0.0.9" {
		t.Fatalf("configured host = %#v", manifest.Portals["aws"].Target)
	}
}

func TestLoadPortalForRuntimeResolvesExistingEC2Host(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	manifest := Manifest{Version: 1, SpaceID: "ex-1", Portals: map[string]Portal{
		"aws": {
			ID:        "aws",
			Driver:    DriverEC2Attach,
			CreatedAt: time.Now().UTC(),
			Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/home/ubuntu/stave/ex-1", SyncMode: SyncRsync},
			Target:    Target{InstanceID: "i-123", Region: "us-west-2", SSHUser: "ubuntu"},
			Runtime:   Runtime{Engine: string(DriverEC2Attach)},
			Auth:      Auth{Mode: AuthNative, Providers: []AuthProvider{defaultAuthProvider("codex", AuthNative)}},
		},
	}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	runner := fakeRunner{outputs: map[string]RunResult{
		"aws ec2 describe-instances --instance-ids i-123 --region us-west-2": {Stdout: `{"Reservations":[{"Instances":[{"PrivateIpAddress":"10.0.0.9"}]}]}`},
	}}
	svc := NewService(cfg, runner, nil)
	plan, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", PortalID: "aws", Command: []string{"pwd"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.EquivalentCommands()[0], "ubuntu@10.0.0.9") {
		t.Fatalf("resolved exec = %v", plan.EquivalentCommands())
	}
	loaded, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Portals["aws"].Target.Host != "10.0.0.9" {
		t.Fatalf("persisted host = %#v", loaded.Portals["aws"].Target)
	}

	runner = fakeRunner{outputs: map[string]RunResult{
		"aws ec2 describe-instances --instance-ids i-123 --region us-west-2": {Stdout: `{"Reservations":[{"Instances":[{}]}]}`},
	}}
	loaded.Portals["aws"] = manifest.Portals["aws"]
	if err := SaveManifest(spacePath, loaded); err != nil {
		t.Fatal(err)
	}
	svc = NewService(cfg, runner, nil)
	if _, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", PortalID: "aws", Command: []string{"pwd"}}); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("expected unresolved host guidance, got %v", err)
	}
}

func TestLocalRunnerCapturesStdoutStderrAndExitCode(t *testing.T) {
	runner := localRunner{}
	result, err := runner.Run(context.Background(), command("sh", "-c", "echo out; echo err >&2"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Stdout) != "out" || strings.TrimSpace(result.Stderr) != "err" {
		t.Fatalf("captured result = %#v", result)
	}
	result, err = runner.Run(context.Background(), command("sh", "-c", "echo bad >&2; exit 7"))
	if err == nil {
		t.Fatal("expected command error")
	}
	if result.ExitCode != 7 || !strings.Contains(result.Stderr, "bad") {
		t.Fatalf("error result = %#v err=%v", result, err)
	}
	interactive := command("sh", "-c", "exit 0")
	interactive.Interactive = true
	if result, err := runner.Run(context.Background(), interactive); err != nil || result.ExitCode != 0 {
		t.Fatalf("interactive success result = %#v err=%v", result, err)
	}
	interactive = command("sh", "-c", "exit 9")
	interactive.Interactive = true
	if result, err := runner.Run(context.Background(), interactive); err == nil || result.ExitCode != 9 {
		t.Fatalf("interactive error result = %#v err=%v", result, err)
	}
}

func TestManageHelperBranches(t *testing.T) {
	if got := strings.Join(authLoginArgv("claude", ""), " "); got != "claude auth login" {
		t.Fatalf("claude login argv = %s", got)
	}
	if got := strings.Join(authLoginArgv("cursor", ""), " "); got != "cursor-agent status" {
		t.Fatalf("cursor login argv = %s", got)
	}
	if got := strings.Join(authLogoutArgv("claude"), " "); got != "claude auth logout" {
		t.Fatalf("claude logout argv = %s", got)
	}
	if got := strings.Join(authLogoutArgv("cursor"), " "); got != "cursor-agent logout" {
		t.Fatalf("cursor logout argv = %s", got)
	}
	if got := firstString("", " fallback ", "later"); got != " fallback " {
		t.Fatalf("firstString = %q", got)
	}
}

func TestMarshalManifestErrors(t *testing.T) {
	if _, err := MarshalManifest(t.TempDir(), Manifest{Version: 2, SpaceID: "ex-1"}); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("MarshalManifest version error = %v", err)
	}
	if _, err := MarshalManifest(t.TempDir(), Manifest{Version: 1, SpaceID: "../bad"}); err == nil || !strings.Contains(err.Error(), "space id") {
		t.Fatalf("MarshalManifest space error = %v", err)
	}
}

func TestPlanDownDevcontainerAndAttachOnlyError(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitDevcontainer(context.Background(), InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev"}); err != nil {
		t.Fatal(err)
	}
	down, err := svc.PlanDown(context.Background(), DownOptions{SpaceID: "ex-1", PortalID: "dev", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(down.Commands) != 1 || !strings.Contains(down.EquivalentCommands()[0], "docker stop") || !strings.Contains(down.EquivalentCommands()[0], "devcontainer.local_folder") {
		t.Fatalf("devcontainer down = %#v", down)
	}
	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", PortalID: "remote", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanDown(context.Background(), DownOptions{SpaceID: "ex-1", PortalID: "remote"}); err == nil || !strings.Contains(err.Error(), "attach-only") {
		t.Fatalf("attach-only down error = %v", err)
	}
}

func TestValidationRejectsUnsafeAndUnsupportedPortalState(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)

	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", RemoteRoot: "/"}); err == nil || !strings.Contains(err.Error(), "too broad") {
		t.Fatalf("expected unsafe root error, got %v", err)
	}
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", Engine: DriverSSH}); err == nil || !strings.Contains(err.Error(), "container engine") {
		t.Fatalf("expected engine error, got %v", err)
	}
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "missing"}); err == nil {
		t.Fatal("expected missing space error")
	}
}

func TestListStatusDoctorAndInspect(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	runner := fakeRunner{missing: map[string]bool{"docker": true}}
	svc := NewService(cfg, runner, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatal(err)
	}

	list, err := svc.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].SpaceID != "ex-1" || list[0].PortalID != "default" {
		t.Fatalf("list = %#v", list)
	}
	status, err := svc.Status(context.Background(), SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Overall != OverallWarn || status.Driver != DriverDocker || status.Diagnostics[0].Code != "driver.binary_missing" {
		t.Fatalf("status = %#v", status)
	}
	doctor, err := svc.Doctor(context.Background(), SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if doctor.Overall != OverallWarn || len(doctor.Diagnostics) == 0 {
		t.Fatalf("doctor = %#v", doctor)
	}
	inspect, err := svc.Inspect(context.Background(), SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspect.OwnedResources) != 1 || !strings.Contains(inspect.DestroyDryRunNotes[0], "container") {
		t.Fatalf("inspect = %#v", inspect)
	}
}

func TestStatusNormalizesRunnerOutputs(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*testing.T, Service)
		runner    fakeRunner
		selectID  string
		wantState string
		wantOK    bool
	}{
		{
			name: "docker running",
			setup: func(t *testing.T, svc Service) {
				if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", Engine: DriverDocker}); err != nil {
					t.Fatal(err)
				}
			},
			runner: fakeRunner{outputs: map[string]RunResult{
				"docker container inspect stave-ex-1-default": {Stdout: `[{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}]`},
			}},
			wantState: "running",
			wantOK:    true,
		},
		{
			name: "ssh reachable",
			setup: func(t *testing.T, svc Service) {
				if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox.example"}); err != nil {
					t.Fatal(err)
				}
			},
			runner: fakeRunner{outputs: map[string]RunResult{
				"ssh devbox.example true": {},
			}},
			wantState: "reachable",
			wantOK:    true,
		},
		{
			name: "ec2 stopped",
			setup: func(t *testing.T, svc Service) {
				if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", InstanceID: "i-123", Region: "us-west-2"}); err != nil {
					t.Fatal(err)
				}
			},
			runner: fakeRunner{outputs: map[string]RunResult{
				"aws ec2 describe-instances --instance-ids i-123 --region us-west-2": {Stdout: `{"Reservations":[{"Instances":[{"State":{"Name":"stopped"}}]}]}`},
			}},
			wantState: "stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			writeSpace(t, cfg, "ex-1")
			svc := NewService(cfg, tt.runner, nil)
			tt.setup(t, svc)
			status, err := svc.Status(context.Background(), SelectOptions{SpaceID: "ex-1", PortalID: tt.selectID})
			if err != nil {
				t.Fatal(err)
			}
			if status.State != tt.wantState {
				t.Fatalf("state = %q, want %q; status=%#v", status.State, tt.wantState, status)
			}
			if tt.wantOK && status.Overall != OverallOK {
				t.Fatalf("overall = %q, want ok; status=%#v", status.Overall, status)
			}
			if !tt.wantOK && status.Overall != OverallWarn {
				t.Fatalf("overall = %q, want warn; status=%#v", status.Overall, status)
			}
		})
	}
}

func TestPlanningCommandsAndSafeguards(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatal(err)
	}
	up, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Commands) != 2 || !strings.Contains(up.EquivalentCommands()[0], "docker start") || !strings.Contains(up.EquivalentCommands()[1], "docker run") {
		t.Fatalf("up = %#v commands=%v", up, up.EquivalentCommands())
	}
	upAttach, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", Attach: "shell", Workdir: "/workspace/ex-1/references", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(upAttach.Commands) != 3 || !strings.Contains(upAttach.EquivalentCommands()[2], "docker exec -it -w /workspace/ex-1/references") {
		t.Fatalf("up attach = %#v commands=%v", upAttach, upAttach.EquivalentCommands())
	}
	if _, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", Attach: "bogus"}); err == nil || !strings.Contains(err.Error(), "attach mode") {
		t.Fatalf("expected attach mode error, got %v", err)
	}
	shell, err := svc.PlanShell(context.Background(), ShellOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shell.EquivalentCommands()[0], "docker exec -it") {
		t.Fatalf("shell commands = %v", shell.EquivalentCommands())
	}
	summonPlan, err := svc.PlanSummon(context.Background(), SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	if len(summonPlan.Diagnostics) == 0 || !strings.Contains(summonPlan.Diagnostics[0].NextAction, "portal auth login") {
		t.Fatalf("summon diagnostics = %#v", summonPlan.Diagnostics)
	}
	headlessSummon, err := svc.PlanSummon(context.Background(), SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "headless"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(headlessSummon.EquivalentCommands()[0], "docker exec -it") {
		t.Fatalf("headless summon should not request an interactive TTY: %v", headlessSummon.EquivalentCommands())
	}
	if _, err := svc.PlanDown(context.Background(), DownOptions{SpaceID: "ex-1", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanDestroy(context.Background(), DestroyOptions{SpaceID: "ex-1", DryRun: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteSyncAndAttachOnlySafeguards(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox.example", Port: 2222, IdentityPath: "~/.ssh/id_ed25519", KnownHostsPath: "/tmp/known_hosts", StrictHostKey: "yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanSync(context.Background(), SyncOptions{SpaceID: "ex-1", Delete: true}); err == nil || !strings.Contains(err.Error(), "dry-run preview") {
		t.Fatalf("expected delete safeguard, got %v", err)
	}
	autoPlan, err := svc.PlanSync(context.Background(), SyncOptions{SpaceID: "ex-1", Mode: "auto", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(autoPlan.Commands) != 1 || !strings.Contains(autoPlan.EquivalentCommands()[0], "rsync") {
		t.Fatalf("auto sync command = %#v", autoPlan.EquivalentCommands())
	}
	syncPlan, err := svc.PlanSync(context.Background(), SyncOptions{SpaceID: "ex-1", Direction: SyncTo, DryRun: true, Delete: true, Include: []string{"src/**"}, Exclude: []string{"tmp/**"}})
	if err != nil {
		t.Fatal(err)
	}
	command := syncPlan.EquivalentCommands()[0]
	for _, needle := range []string{"rsync", "--dry-run", "--delete-delay", "--exclude=.git", "-e 'ssh -p 2222 -i ~/.ssh/id_ed25519 -o UserKnownHostsFile=/tmp/known_hosts -o StrictHostKeyChecking=yes'", "--include=src/**", "devbox.example:~/stave/agent-work/ex-1/"} {
		if !strings.Contains(command, needle) {
			t.Fatalf("sync command missing %q: %s", needle, command)
		}
	}
	if _, err := svc.PlanDown(context.Background(), DownOptions{SpaceID: "ex-1"}); err == nil || !strings.Contains(err.Error(), "attach-only") {
		t.Fatalf("expected attach-only down guard, got %v", err)
	}
	if _, err := svc.PlanDestroy(context.Background(), DestroyOptions{SpaceID: "ex-1"}); err == nil || !strings.Contains(err.Error(), "attach-only") {
		t.Fatalf("expected attach-only destroy guard, got %v", err)
	}
	if _, err := svc.Detach(DetachOptions{SpaceID: "ex-1", DryRun: true}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthParsingAndCommands(t *testing.T) {
	if ParseCodexAuthStatus("not logged in") != AuthMissing || ParseCodexAuthStatus("logged in") != AuthOK {
		t.Fatal("codex auth parser mismatch")
	}
	if ParseClaudeAuthStatus("expired token") != AuthError || ParseClaudeAuthStatus("valid") != AuthOK {
		t.Fatal("claude auth parser mismatch")
	}
	if ParseCursorAuthStatus("signed in") != AuthOK || ParseCursorAuthStatus("failed") != AuthError {
		t.Fatal("cursor auth parser mismatch")
	}

	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatal(err)
	}
	login, err := svc.PlanAuthLogin(context.Background(), AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthNative, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(login.EquivalentCommands()[0], "codex login") {
		t.Fatalf("login = %v", login.EquivalentCommands())
	}
	if _, err := svc.PlanAuthInherit(context.Background(), AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthCopyCache, Yes: true}); err == nil || !strings.Contains(err.Error(), "copy-cache") {
		t.Fatalf("expected copy-cache rejection, got %v", err)
	}
}

type fakeRunner struct {
	missing map[string]bool
	outputs map[string]RunResult
	errors  map[string]error
}

func (r fakeRunner) LookPath(name string) (string, error) {
	if r.missing[name] {
		return "", os.ErrNotExist
	}
	return "/bin/" + name, nil
}

func (r fakeRunner) Run(_ context.Context, cmd Command) (RunResult, error) {
	key := strings.TrimSpace(cmd.String())
	if err, ok := r.errors[key]; ok {
		return r.outputs[key], err
	}
	if result, ok := r.outputs[key]; ok {
		return result, nil
	}
	return RunResult{}, nil
}

func testConfig(t *testing.T) config.Config {
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

func writeSpace(t *testing.T, cfg config.Config, id string) {
	t.Helper()
	spacePath := filepath.Join(cfg.AgentWorkDir, id)
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{ID: id, Kind: "ticket", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
