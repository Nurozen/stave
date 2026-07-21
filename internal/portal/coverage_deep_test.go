package portal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
)

func TestManifestDefaultsAndValidationErrors(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")

	manifest := Manifest{SpaceID: "ex-1", Portals: map[string]Portal{
		"remote": {Driver: DriverSSH, Target: Target{Host: "devbox"}},
	}}
	if err := manifest.ApplyDefaults(spacePath); err != nil {
		t.Fatal(err)
	}
	remote := manifest.Portals["remote"]
	if manifest.Version != 1 || remote.ID != "remote" || remote.Workspace.LocalPath != spacePath {
		t.Fatalf("defaults not applied: %#v", manifest)
	}
	if remote.Workspace.RemoteRoot == "" || remote.Workspace.SyncMode != SyncRsync || remote.Auth.Providers[0].Target != "portal" {
		t.Fatalf("remote defaults = %#v", remote)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("defaulted manifest should validate: %v", err)
	}

	tests := []struct {
		name   string
		portal Portal
		want   string
	}{
		{name: "bad driver", portal: Portal{ID: "p", Driver: "magic"}, want: "not supported"},
		{name: "bad sync", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: "teleport"}, Runtime: Runtime{ContainerName: "c"}}, want: "sync mode"},
		{name: "unsafe local path", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: "bad\npath", ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount}, Runtime: Runtime{ContainerName: "c"}}, want: "unsafe character"},
		{name: "glob path", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/*", SyncMode: SyncMount}, Runtime: Runtime{ContainerName: "c"}}, want: "glob"},
		{name: "escape remote", portal: Portal{ID: "p", Driver: DriverSSH, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "../outside", SyncMode: SyncRsync}, Target: Target{Host: "devbox"}}, want: "escape upward"},
		{name: "bad port", portal: Portal{ID: "p", Driver: DriverSSH, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncRsync}, Target: Target{Host: "devbox", Port: 70000}}, want: "outside"},
		{name: "docker missing container", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount}}, want: "container name"},
		{name: "docker bad sync", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncRsync}, Runtime: Runtime{ContainerName: "c"}}, want: "mount sync"},
		{name: "devcontainer missing root", portal: Portal{ID: "p", Driver: DriverDevcontainer, Workspace: Workspace{LocalPath: spacePath, SyncMode: SyncMount}}, want: "container root"},
		{name: "ssh missing host", portal: Portal{ID: "p", Driver: DriverSSH, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncRsync}}, want: "ssh host"},
		{name: "ssh mount sync", portal: Portal{ID: "p", Driver: DriverSSH, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncMount}, Target: Target{Host: "devbox"}}, want: "cannot use mount"},
		{name: "bad strict host key", portal: Portal{ID: "p", Driver: DriverSSH, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncRsync}, Target: Target{Host: "devbox", StrictHostKey: "sometimes"}}, want: "strict host key"},
		{name: "ec2 missing instance", portal: Portal{ID: "p", Driver: DriverEC2Attach, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncRsync}}, want: "instance id"},
		{name: "ec2 mount sync", portal: Portal{ID: "p", Driver: DriverEC2Attach, Workspace: Workspace{LocalPath: spacePath, RemoteRoot: "/workspace", SyncMode: SyncMount}, Target: Target{InstanceID: "i-123"}}, want: "cannot use mount"},
		{name: "copy cache default", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount}, Runtime: Runtime{ContainerName: "c", Labels: map[string]string{"stave.space": "ex-1", "stave.portal": "p"}}, Auth: Auth{Mode: AuthCopyCache}}, want: "copy-cache"},
		{name: "copy cache provider", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount}, Runtime: Runtime{ContainerName: "c", Labels: map[string]string{"stave.space": "ex-1", "stave.portal": "p"}}, Auth: Auth{Mode: AuthNative, Providers: []AuthProvider{{Provider: "codex", Mode: AuthCopyCache}}}}, want: "copy-cache"},
		{name: "bad portal label", portal: Portal{ID: "p", Driver: DriverDocker, Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount}, Runtime: Runtime{ContainerName: "c", Labels: map[string]string{"stave.space": "ex-1", "stave.portal": "other"}}}, want: "stave.portal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.name, "copy cache") {
				applyPortalDefaults("ex-1", &tt.portal)
			}
			if err := ValidatePortal("ex-1", tt.portal); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidatePortal error = %v, want %q", err, tt.want)
			}
		})
	}

	mismatch := Manifest{Version: 1, SpaceID: "ex-1", Portals: map[string]Portal{"key": {ID: "other", Driver: DriverDocker}}}
	if err := mismatch.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected key mismatch error, got %v", err)
	}
}

func TestListDriversAndLoadErrorBranches(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	writeSpace(t, cfg, "ex-2")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-2", PortalID: "z"}); err != nil {
		t.Fatal(err)
	}

	one, err := svc.List("ex-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{one[0].PortalID, one[1].PortalID}; got[0] != "a" || got[1] != "b" {
		t.Fatalf("single-space list not sorted: %#v", one)
	}
	all, err := svc.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].SpaceID != "ex-1" || all[2].SpaceID != "ex-2" {
		t.Fatalf("all list = %#v", all)
	}
	if _, err := svc.List("missing"); err == nil {
		t.Fatal("expected missing manifest error")
	}
	missingRoot := svc
	missingRoot.Config.AgentWorkDir = filepath.Join(cfg.Root, "missing-agent-work")
	if _, err := missingRoot.List(""); err == nil {
		t.Fatal("expected missing agent work dir error")
	}
	if err := os.WriteFile(filepath.Join(cfg.AgentWorkDir, "ex-2", ManifestName), []byte("not: [yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(""); err == nil {
		t.Fatal("expected invalid portal manifest error")
	}

	drivers := svc.Drivers()
	if len(drivers) != len(SupportedDrivers()) || !drivers[0].CreateCapable || !drivers[2].AttachOnly || drivers[3].Binary != "aws" {
		t.Fatalf("drivers = %#v", drivers)
	}
}

func TestStatusBranchesForRunnerAndDrivers(t *testing.T) {
	ctx := context.Background()

	t.Run("no runner leaves status unqueried", func(t *testing.T) {
		cfg, svc := serviceWithContainer(t, fakeRunner{})
		svc.Runner = nil
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.Overall != OverallUnknown || status.Diagnostics[0].Code != "runtime.no_runner" {
			t.Fatalf("status = %#v cfg=%#v", status, cfg)
		}
	})

	t.Run("container inspect failure records warning evidence", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{
			errors:  map[string]error{"docker container inspect stave-ex-1-default": errors.New("inspect failed")},
			outputs: map[string]RunResult{"docker container inspect stave-ex-1-default": {Stderr: "missing container"}},
		})
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "missing" || status.Diagnostics[0].Code != "runtime.container_inspect_failed" || status.Diagnostics[0].Evidence != "missing container" {
			t.Fatalf("status = %#v", status)
		}
	})

	t.Run("container invalid inspect output warns not ready", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{outputs: map[string]RunResult{
			"docker container inspect stave-ex-1-default": {Stdout: "not-json"},
		}})
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "unknown" || status.Health != "unknown" || status.Diagnostics[0].Code != "runtime.container_not_ready" {
			t.Fatalf("status = %#v", status)
		}
	})

	t.Run("devcontainer reachable and missing binary", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{outputs: map[string]RunResult{
			"devcontainer exec --workspace-folder " + filepath.Join(cfg.AgentWorkDir, "ex-1") + " true": {},
		}}, nil)
		if _, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1"}); err != nil {
			t.Fatal(err)
		}
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "reachable" || status.Overall != OverallOK {
			t.Fatalf("status = %#v", status)
		}
		missing := svc
		missing.Runner = fakeRunner{missing: map[string]bool{"devcontainer": true}}
		status, err = missing.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.Overall != OverallWarn || status.Diagnostics[0].Code != "driver.binary_missing" {
			t.Fatalf("missing binary status = %#v", status)
		}
	})

	t.Run("ssh unreachable", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{
			errors:  map[string]error{"ssh -n -o BatchMode=yes -o ConnectTimeout=10 -p 22 devbox true": errors.New("network down")},
			outputs: map[string]RunResult{"ssh -n -o BatchMode=yes -o ConnectTimeout=10 -p 22 devbox true": {Stdout: "no route"}},
		}, nil)
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox"}); err != nil {
			t.Fatal(err)
		}
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "unreachable" || status.Diagnostics[0].Code != "ssh.unreachable" || status.Diagnostics[0].Evidence != "no route" {
			t.Fatalf("status = %#v", status)
		}
	})

	t.Run("ec2 running invalid and error", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{outputs: map[string]RunResult{
			"aws ec2 describe-instances --instance-ids i-123": {Stdout: `{"Reservations":[{"Instances":[{"State":{"Name":"running"}}]}]}`},
		}}, nil)
		if _, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", InstanceID: "i-123"}); err != nil {
			t.Fatal(err)
		}
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "running" || status.Overall != OverallOK {
			t.Fatalf("running status = %#v", status)
		}

		svc.Runner = fakeRunner{outputs: map[string]RunResult{
			"aws ec2 describe-instances --instance-ids i-123": {Stdout: `{"Reservations":[]}`},
		}}
		status, err = svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "reachable" || status.Overall != OverallOK {
			t.Fatalf("invalid-but-successful ec2 status = %#v", status)
		}

		svc.Runner = fakeRunner{errors: map[string]error{
			"aws ec2 describe-instances --instance-ids i-123": errors.New("aws failed"),
		}}
		status, err = svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if status.State != "unreachable" || status.Diagnostics[0].Code != "ec2.describe_failed" {
			t.Fatalf("error ec2 status = %#v", status)
		}
	})
}

func TestManageBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("auth inherit allowed and validation errors", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{})
		inherit, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthEnv, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(inherit.Commands) != 0 || len(inherit.Diagnostics) == 0 || !strings.Contains(inherit.Diagnostics[0].Code, "inherit") {
			t.Fatalf("inherit = %#v commands=%v", inherit, inherit.EquivalentCommands())
		}
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex"}); err == nil || !strings.Contains(err.Error(), "method is required") {
			t.Fatalf("expected missing method error, got %v", err)
		}
		if _, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "bad"}); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Fatalf("expected provider error, got %v", err)
		}
		if _, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: "nonsense"}); err == nil || !strings.Contains(err.Error(), "auth login method") {
			t.Fatalf("expected login method error, got %v", err)
		}
		device, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: "device"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(device.EquivalentCommands()[0], "--device-auth") {
			t.Fatalf("device login = %v", device.EquivalentCommands())
		}
		cursor, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "cursor"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cursor.EquivalentCommands()[0], "cursor-agent status") {
			t.Fatalf("cursor login = %v", cursor.EquivalentCommands())
		}
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: "nonsense", DryRun: true}); err == nil || !strings.Contains(err.Error(), "auth inherit method") {
			t.Fatalf("expected inherit method error, got %v", err)
		}
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthEnv}); err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("expected inherit confirmation error, got %v", err)
		}
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthEnv, Yes: true}); err != nil {
			t.Fatal(err)
		}
		summon, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "print"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(summon.EquivalentCommands()[0], "-e OPENAI_API_KEY") {
			t.Fatalf("inherited env summon = %v", summon.EquivalentCommands())
		}
		if len(providerEnvNames("claude")) == 0 || len(providerEnvNames("cursor")) == 0 || len(providerEnvNames("unknown")) != 0 {
			t.Fatalf("provider env names not covered")
		}
		revoke, err := svc.PlanAuthRevoke(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Target: "all", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(revoke.Commands) != 2 || !strings.Contains(revoke.EquivalentCommands()[0], "codex logout") || !strings.Contains(revoke.EquivalentCommands()[1], "docker exec") {
			t.Fatalf("revoke all = %v", revoke.EquivalentCommands())
		}
		if _, err := svc.PlanAuthRevoke(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Target: "nonsense", DryRun: true}); err == nil || !strings.Contains(err.Error(), "auth target") {
			t.Fatalf("expected revoke target error, got %v", err)
		}
		if _, err := svc.PlanAuthRevoke(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex"}); err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("expected revoke confirmation error, got %v", err)
		}
	})

	t.Run("logs local follow and remote default session", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{})
		logs, err := svc.PlanLogs(ctx, LogsOptions{SpaceID: "ex-1", Follow: true, Tail: 25})
		if err != nil {
			t.Fatal(err)
		}
		if got := logs.EquivalentCommands()[0]; !strings.Contains(got, "docker logs --tail 25 --follow stave-ex-1-default") {
			t.Fatalf("local logs = %s", got)
		}

		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		remoteSvc := NewService(cfg, fakeRunner{}, nil)
		if _, err := remoteSvc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox"}); err != nil {
			t.Fatal(err)
		}
		remoteLogs, err := remoteSvc.PlanLogs(ctx, LogsOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		if got := remoteLogs.EquivalentCommands()[0]; !strings.Contains(got, "stave-ex-1-default-agent") || !strings.Contains(got, "-S -100") {
			t.Fatalf("remote logs = %s", got)
		}
	})

	t.Run("down devcontainer", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{}, nil)
		if _, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev"}); err != nil {
			t.Fatal(err)
		}
		down, err := svc.PlanDown(ctx, DownOptions{SpaceID: "ex-1", PortalID: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(down.EquivalentCommands()[0], "docker stop") || !strings.Contains(down.EquivalentCommands()[0], "devcontainer.local_folder") {
			t.Fatalf("down = %v", down.EquivalentCommands())
		}
	})

	t.Run("destroy devcontainer removes labeled container", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{}, nil)
		if _, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev"}); err != nil {
			t.Fatal(err)
		}
		destroy, err := svc.PlanDestroy(ctx, DestroyOptions{SpaceID: "ex-1", PortalID: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		commands := destroy.EquivalentCommands()
		if len(commands) != 1 || !strings.Contains(commands[0], "docker rm -f") || !strings.Contains(commands[0], "devcontainer.local_folder") || strings.Contains(commands[0], "rm -f ''") {
			t.Fatalf("destroy = %v", commands)
		}
	})

	t.Run("detach deletes only remote metadata", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{}, nil)
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", PortalID: "ssh"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Detach(DetachOptions{SpaceID: "ex-1", PortalID: "ssh", DryRun: true}); err != nil {
			t.Fatal(err)
		}
		manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := manifest.Portals["ssh"]; !ok {
			t.Fatal("dry-run detach removed metadata")
		}
		if _, err := svc.Detach(DetachOptions{SpaceID: "ex-1", PortalID: "ssh"}); err != nil {
			t.Fatal(err)
		}
		manifest, err = LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := manifest.Portals["ssh"]; ok {
			t.Fatal("detach did not remove metadata")
		}
		if _, err := svc.Detach(DetachOptions{SpaceID: "ex-1", PortalID: "ssh"}); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("expected detach missing error, got %v", err)
		}
	})

	t.Run("destroy volumes and guardrails", func(t *testing.T) {
		cfg, svc := serviceWithContainer(t, fakeRunner{})
		spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
		manifest, err := LoadManifest(spacePath)
		if err != nil {
			t.Fatal(err)
		}
		portal := manifest.Portals["default"]
		portal.Ownership.CreatedVolumes = []string{"stave-data", "stave-cache"}
		manifest.Portals["default"] = portal
		if err := SaveManifest(spacePath, manifest); err != nil {
			t.Fatal(err)
		}
		destroy, err := svc.PlanDestroy(ctx, DestroyOptions{SpaceID: "ex-1", DeleteVolumes: true})
		if err != nil {
			t.Fatal(err)
		}
		commands := strings.Join(destroy.EquivalentCommands(), "\n")
		if !strings.Contains(commands, "docker rm -f stave-ex-1-default") || !strings.Contains(commands, "docker volume rm stave-data") || !strings.Contains(commands, "docker volume rm stave-cache") {
			t.Fatalf("destroy commands = %s", commands)
		}
		portal.Ownership.CreatedContainer = false
		manifest.Portals["default"] = portal
		if err := SaveManifest(spacePath, manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PlanDestroy(ctx, DestroyOptions{SpaceID: "ex-1"}); err == nil || !strings.Contains(err.Error(), "no recorded") {
			t.Fatalf("expected ownership guard, got %v", err)
		}
	})
}

func TestSummonAuthParsersAndCommandQuoting(t *testing.T) {
	portal := Portal{Driver: DriverDocker, Workspace: Workspace{LocalPath: "/host/work", ContainerRoot: "/workspace/ex-1"}, Runtime: Runtime{ContainerName: "stave-ex-1-default", Engine: "docker"}}
	for _, tt := range []struct {
		name     string
		with     string
		mode     string
		contains []string
	}{
		{name: "codex interactive", with: "codex", contains: []string{"codex", "--cd", "/workspace/ex-1"}},
		{name: "codex headless", with: "codex", mode: "headless", contains: []string{"codex", "exec", "--sandbox", "workspace-write", "--json"}},
		{name: "claude interactive", with: "claude", contains: []string{"claude"}},
		{name: "claude headless", with: "claude", mode: "headless", contains: []string{"claude", "-p", "--output-format", "stream-json"}},
		{name: "cursor", with: "cursor", contains: []string{"cursor-agent"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := summonCommand(tt.with, portal, SummonOptions{Mode: tt.mode})
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(argv, " ")
			for _, needle := range tt.contains {
				if !strings.Contains(joined, needle) {
					t.Fatalf("summon argv = %v, missing %q", argv, needle)
				}
			}
		})
	}
	if _, err := summonCommand("bad", portal, SummonOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported summoner error, got %v", err)
	}
	if normalizeSummoner("") != "codex" || normalizeSummoner("cursor-agent") != "cursor" || normalizeSummoner("claude") != "claude" {
		t.Fatal("normalizeSummoner mismatch")
	}
	if !authProviderOK([]AuthProvider{{Provider: "codex", Status: AuthOK}}, "codex") || authProviderOK([]AuthProvider{{Provider: "codex", Status: AuthMissing}}, "codex") {
		t.Fatal("authProviderOK mismatch")
	}

	parserCases := []struct {
		name string
		got  AuthStatus
		want AuthStatus
	}{
		{name: "codex unknown", got: ParseCodexAuthStatus("ready maybe"), want: AuthUnknown},
		{name: "codex error", got: ParseCodexAuthStatus("failed to read token"), want: AuthError},
		{name: "claude missing", got: ParseClaudeAuthStatus("login required"), want: AuthMissing},
		{name: "claude unknown", got: ParseClaudeAuthStatus("hello"), want: AuthUnknown},
		{name: "cursor missing", got: ParseCursorAuthStatus("not authenticated"), want: AuthMissing},
		{name: "cursor unknown", got: ParseCursorAuthStatus("hello"), want: AuthUnknown},
	}
	for _, tt := range parserCases {
		if tt.got != tt.want {
			t.Fatalf("%s = %s, want %s", tt.name, tt.got, tt.want)
		}
	}

	cmd := Command{
		Env:     []string{"A=hello world"},
		Dir:     "/tmp/has space",
		Program: "codex",
		Args:    []string{"say", "it's ok", ""},
	}
	got := cmd.String()
	for _, needle := range []string{"'A=hello world'", "cd '/tmp/has space' && codex", `'it'"'"'s ok'`, "''"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("quoted command = %s, missing %s", got, needle)
		}
	}
	if quoteShell("abcXYZ012@%_+=:,./-") != "abcXYZ012@%_+=:,./-" || quoteShell("") != "''" {
		t.Fatal("quoteShell safe/empty mismatch")
	}
}

func TestLifecyclePlanningAndInspectionBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("init attach dry-runs duplicates and invalid load paths", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, nil, nil)
		if svc.Runner == nil {
			t.Fatal("NewService should install local runner when nil")
		}
		svc.Runner = fakeRunner{}

		devDry, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !devDry.DryRun || len(devDry.Diagnostics) == 0 {
			t.Fatalf("dev dry-run = %#v", devDry)
		}
		if _, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dev dry-run wrote manifest: %v", err)
		}
		if _, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev", Preset: "claude-devcontainer", DevcontainerPath: ".devcontainer/devcontainer.json", ComposeFiles: []string{"compose.yaml"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.InitDevcontainer(ctx, InitDevcontainerOptions{SpaceID: "ex-1", PortalID: "dev"}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected duplicate devcontainer error, got %v", err)
		}
		if _, err := svc.InitContainer(ctx, InitContainerOptions{SpaceID: "ex-1", PortalID: "bad", ContainerRoot: "/"}); err == nil || !strings.Contains(err.Error(), "too broad") {
			t.Fatalf("expected invalid init container root, got %v", err)
		}

		sshDry, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh-dry", Host: "devbox", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !sshDry.DryRun || len(sshDry.Diagnostics) == 0 {
			t.Fatalf("ssh dry-run = %#v", sshDry)
		}
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox", Port: 2222, IdentityPath: "~/.ssh/id", RemoteRoot: "/srv/work", SyncMode: SyncReconstruct, Preset: "custom"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "other"}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected duplicate ssh error, got %v", err)
		}
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "bad-ssh"}); err == nil || !strings.Contains(err.Error(), "ssh host") {
			t.Fatalf("expected missing host error, got %v", err)
		}

		ec2Dry, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", PortalID: "ec2-dry", InstanceID: "i-dry", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !ec2Dry.DryRun || len(ec2Dry.Diagnostics) == 0 {
			t.Fatalf("ec2 dry-run = %#v", ec2Dry)
		}
		if _, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", PortalID: "ec2", InstanceID: "i-123", Profile: "dev", SSHUser: "ubuntu", IdentityPath: "~/.ssh/aws"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", PortalID: "ec2", InstanceID: "i-456"}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected duplicate ec2 error, got %v", err)
		}
		if _, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", PortalID: "bad-ec2"}); err == nil || !strings.Contains(err.Error(), "instance id") {
			t.Fatalf("expected missing instance error, got %v", err)
		}

		spaceManifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if err != nil {
			t.Fatal(err)
		}
		if spaceManifest.Portals["dev"].Auth.Providers[0].Provider != "claude" || spaceManifest.Portals["ssh"].Workspace.SyncMode != SyncReconstruct {
			t.Fatalf("manifest = %#v", spaceManifest)
		}
		if _, _, err := svc.LoadPortal(SelectOptions{SpaceID: "bad name"}); err == nil {
			t.Fatal("expected invalid space id")
		}
		if _, _, err := svc.LoadPortal(SelectOptions{SpaceID: "ex-1", PortalID: "missing"}); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("expected missing portal error, got %v", err)
		}
		spaceManifest.SpaceID = "other"
		if err := SaveManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"), spaceManifest); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.LoadPortal(SelectOptions{SpaceID: "ex-1", PortalID: "dev"}); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected manifest mismatch, got %v", err)
		}
	})

	t.Run("configure dry-run invalid and inspect resources", func(t *testing.T) {
		cfg, svc := serviceWithContainer(t, fakeRunner{})
		if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "missing", DryRun: true}); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("expected missing configure error, got %v", err)
		}
		dry, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "default", Agent: "claude", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !dry.DryRun || len(dry.Diagnostics) == 0 {
			t.Fatalf("configure dry-run = %#v", dry)
		}
		if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "default", SyncMode: SyncRsync}); err == nil || !strings.Contains(err.Error(), "mount sync") {
			t.Fatalf("expected invalid configure sync, got %v", err)
		}

		spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
		manifest, err := LoadManifest(spacePath)
		if err != nil {
			t.Fatal(err)
		}
		p := manifest.Portals["default"]
		p.Ownership.CreatedVolumes = []string{"vol1"}
		p.Ownership.CreatedNetworks = []string{"net1"}
		manifest.Portals["default"] = p
		manifest.Portals["manual"] = Portal{
			ID:        "manual",
			Driver:    DriverDevcontainer,
			Workspace: Workspace{LocalPath: spacePath, ContainerRoot: "/workspace/ex-1", SyncMode: SyncMount},
			Runtime:   Runtime{Engine: string(DriverDevcontainer), Labels: map[string]string{"stave.space": "ex-1", "stave.portal": "manual"}},
			Auth:      Auth{Mode: AuthNative, Providers: []AuthProvider{{Provider: "codex", Mode: AuthNative, Target: "portal", Status: AuthUnknown}}},
		}
		if err := SaveManifest(spacePath, manifest); err != nil {
			t.Fatal(err)
		}
		inspect, err := svc.Inspect(ctx, SelectOptions{SpaceID: "ex-1"})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(append(inspect.OwnedResources, inspect.DestroyDryRunNotes...), "\n")
		if !strings.Contains(joined, "volume:vol1") || !strings.Contains(joined, "network:net1") {
			t.Fatalf("inspect = %#v", inspect)
		}
		manual, err := svc.Inspect(ctx, SelectOptions{SpaceID: "ex-1", PortalID: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		if len(manual.OwnedResources) != 0 || manual.DestroyDryRunNotes[0] != "no Stave-owned runtime resources are recorded" {
			t.Fatalf("manual inspect = %#v", manual)
		}
	})
}

func TestPlanSyncAndHelperBranches(t *testing.T) {
	ctx := context.Background()
	cfg, svc := serviceWithContainer(t, fakeRunner{})

	if _, err := svc.PlanSync(ctx, SyncOptions{SpaceID: "ex-1", Direction: "sideways"}); err == nil || !strings.Contains(err.Error(), "direction") {
		t.Fatalf("expected direction error, got %v", err)
	}
	if _, err := svc.PlanSync(ctx, SyncOptions{SpaceID: "ex-1", Mode: SyncRsync}); err == nil || !strings.Contains(err.Error(), "requires ssh") {
		t.Fatalf("expected local rsync error, got %v", err)
	}
	if _, err := svc.PlanSync(ctx, SyncOptions{SpaceID: "ex-1", Mode: SyncReconstruct}); err == nil || !strings.Contains(err.Error(), "requires ssh") {
		t.Fatalf("expected local reconstruct error, got %v", err)
	}
	mount, err := svc.PlanSync(ctx, SyncOptions{SpaceID: "ex-1", Mode: SyncMount, MaxDelete: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(mount.Commands) != 0 || len(mount.Diagnostics) != 2 || mount.Diagnostics[1].Code != "sync.max_delete" {
		t.Fatalf("mount sync = %#v", mount)
	}

	if _, err := svc.PlanShell(ctx, ShellOptions{SpaceID: "ex-1", TTY: TTYAlways, User: "root", CWD: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	noTTY, err := svc.PlanExec(ctx, ExecOptions{SpaceID: "ex-1", Command: []string{"true"}, TTY: TTYNever})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noTTY.EquivalentCommands()[0], "-it") {
		t.Fatalf("non-interactive exec allocated tty: %v", noTTY.EquivalentCommands())
	}

	writeSpaceWithRepos(t, cfg, "ex-2")
	spacePath2 := filepath.Join(cfg.AgentWorkDir, "ex-2")
	if err := os.WriteFile(filepath.Join(spacePath2, space.AgentsName), []byte("instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(space.AgentsName, filepath.Join(spacePath2, space.ClaudeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spacePath2, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	remoteSvc := NewService(cfg, fakeRunner{}, nil)
	remoteSvc.Git = &dirtyGit{dirty: false}
	if _, err := remoteSvc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-2", Host: "devbox", PortalID: "ssh"}); err != nil {
		t.Fatal(err)
	}
	both, err := remoteSvc.PlanSync(ctx, SyncOptions{SpaceID: "ex-2", PortalID: "ssh", Direction: SyncBoth, Delete: true, Yes: true, MaxDelete: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Commands) != 2 || both.Diagnostics[0].Code != "sync.max_delete" {
		t.Fatalf("both sync = %#v commands=%v", both, both.EquivalentCommands())
	}
	reconstruct, err := remoteSvc.PlanSync(ctx, SyncOptions{SpaceID: "ex-2", PortalID: "ssh", Mode: SyncReconstruct, Include: []string{"src/**"}, Exclude: []string{"tmp/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reconstruct.EquivalentCommands()[0], "--relative") || !strings.Contains(reconstruct.EquivalentCommands()[0], space.ClaudeName) || reconstruct.Commands[0].Dir != spacePath2 {
		t.Fatalf("reconstruct = %v", reconstruct.EquivalentCommands())
	}
	if err := os.Remove(filepath.Join(spacePath2, space.ClaudeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(spacePath2, "spec")); err != nil {
		t.Fatal(err)
	}
	legacyReconstruct, err := remoteSvc.PlanSync(ctx, SyncOptions{SpaceID: "ex-2", PortalID: "ssh", Mode: SyncReconstruct})
	if err != nil {
		t.Fatal(err)
	}
	legacyCommand := legacyReconstruct.EquivalentCommands()[0]
	if strings.Contains(legacyCommand, space.ClaudeName) || strings.Contains(legacyCommand, "spec/") {
		t.Fatalf("legacy reconstruct required missing optional metadata: %s", legacyCommand)
	}

	remotePortal, _, err := remoteSvc.LoadPortal(SelectOptions{SpaceID: "ex-2", PortalID: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if portalCWD(Portal{Workspace: Workspace{LocalPath: "/host"}}, "") != "/host" || firstTTY(TTYAlways, TTYNever) != TTYAlways || firstTTY("", TTYAlways) != TTYAlways {
		t.Fatal("cwd or tty fallback mismatch")
	}
	if firstSyncMode(SyncReconstruct, SyncRsync) != SyncReconstruct || firstSyncMode("", SyncRsync) != SyncRsync {
		t.Fatal("firstSyncMode mismatch")
	}
	ttySvc := Service{IsTerminal: func() bool { return true }}
	sshCmd := ttySvc.portalExecCommand(remotePortal, []string{"echo", "hello world"}, "", "ignored", TTYAuto, true).String()
	if !strings.Contains(sshCmd, "cd") || !strings.Contains(sshCmd, "'hello world'") {
		t.Fatalf("remote exec command = %s", sshCmd)
	}
}

func TestDoctorStatusAuthAndLocalRunnerBranches(t *testing.T) {
	ctx := context.Background()
	cfg, svc := serviceWithContainer(t, fakeRunner{})
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	portal := manifest.Portals["default"]
	portal.Auth.Providers = []AuthProvider{
		{Provider: "codex", Mode: AuthNative, Target: "portal", Status: AuthOK},
		{Provider: "claude", Mode: AuthNative, Target: "portal", Status: AuthMissing},
	}
	manifest.Portals["default"] = portal
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Overall != OverallWarn || status.Diagnostics[len(status.Diagnostics)-1].Code != "auth.missing" {
		t.Fatalf("auth missing status = %#v", status)
	}
	if _, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1", PortalID: "missing"}); err == nil {
		t.Fatal("expected status load error")
	}

	healthySvc := svc
	healthySvc.Runner = fakeRunner{outputs: map[string]RunResult{
		"docker container inspect stave-ex-1-default": {Stdout: `[{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}]`},
	}}
	doctor, err := healthySvc.Doctor(ctx, SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if doctor.Overall != OverallOK {
		t.Fatalf("doctor = %#v", doctor)
	}
	unreachableSvc := svc
	unreachableSvc.Runner = fakeRunner{errors: map[string]error{
		"docker container inspect stave-ex-1-default": errors.New("cannot connect to the docker daemon"),
	}}
	doctor, err = unreachableSvc.Doctor(ctx, SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if doctor.Overall != OverallWarn {
		t.Fatalf("doctor must flag an unreachable runtime like status does, got %#v", doctor)
	}
	os.Remove(filepath.Join(spacePath, ".stave.yaml"))
	doctor, err = healthySvc.Doctor(ctx, SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if doctor.Overall != OverallError || doctor.Diagnostics[0].Code != "space.manifest_missing" {
		t.Fatalf("doctor missing space manifest = %#v", doctor)
	}
	if _, err := svc.Doctor(ctx, SelectOptions{SpaceID: "ex-1", PortalID: "missing"}); err == nil {
		t.Fatal("expected doctor load error")
	}

	if requiredBinaries(Portal{Driver: "unknown"}) != nil || statusBinaries(Portal{Driver: "unknown"}) != nil {
		t.Fatal("unknown driver should not require binaries")
	}
	if summarizeAuth(nil) != "unknown" || !authMissing([]AuthProvider{{Status: AuthError}}) {
		t.Fatal("auth summary/missing mismatch")
	}
	if overallFromDiagnostics([]Diagnostic{{Severity: SeverityWarn}}) != OverallWarn || overallFromDiagnostics([]Diagnostic{{Severity: SeverityError}}) != OverallError {
		t.Fatal("overall diagnostics mismatch")
	}

	result, err := (localRunner{}).Run(ctx, Command{Program: "sh", Args: []string{"-c", "printf err >&2; exit 7"}})
	if err == nil || result.ExitCode != 7 || result.Stderr != "err" {
		t.Fatalf("local runner failure result=%#v err=%v", result, err)
	}
}

func TestManageRemainingBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("validate provider and login method helpers", func(t *testing.T) {
		if err := ValidateProviderName("codex"); err != nil {
			t.Fatalf("ValidateProviderName(codex) = %v", err)
		}
		if err := ValidateProviderName("nope"); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Fatalf("ValidateProviderName(nope) = %v", err)
		}
		// device login is only valid for codex; any other provider is rejected.
		if err := validateAuthLoginMethod("claude", "device"); err == nil || !strings.Contains(err.Error(), "only supported for codex") {
			t.Fatalf("device login for claude = %v", err)
		}
		if cmd := commandFromArgv(nil); cmd.Program != "" || len(cmd.Args) != 0 {
			t.Fatalf("commandFromArgv(nil) = %#v", cmd)
		}
	})

	t.Run("configure updates remote root", func(t *testing.T) {
		cfg := testConfig(t)
		writeSpace(t, cfg, "ex-1")
		svc := NewService(cfg, fakeRunner{}, nil)
		if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", PortalID: "ssh"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "ssh", RemoteRoot: "/srv/custom"}); err != nil {
			t.Fatal(err)
		}
		manifest, err := LoadManifest(filepath.Join(cfg.AgentWorkDir, "ex-1"))
		if err != nil {
			t.Fatal(err)
		}
		if got := manifest.Portals["ssh"].Workspace.RemoteRoot; got != "/srv/custom" {
			t.Fatalf("RemoteRoot not applied: %q", got)
		}
	})

	t.Run("down docker honors timeout", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{})
		down, err := svc.PlanDown(ctx, DownOptions{SpaceID: "ex-1", Timeout: 12})
		if err != nil {
			t.Fatal(err)
		}
		if got := down.EquivalentCommands()[0]; !strings.Contains(got, "docker stop --time 12") {
			t.Fatalf("down with timeout = %s", got)
		}
	})

	t.Run("devcontainer command includes config file filter", func(t *testing.T) {
		portal := Portal{
			Workspace: Workspace{LocalPath: "/host/work"},
			Runtime:   Runtime{DevcontainerPath: ".devcontainer/devcontainer.json"},
		}
		cmd := devcontainerDockerContainerCommand(portal, "stop").String()
		if !strings.Contains(cmd, "devcontainer.config_file") || !strings.Contains(cmd, "docker ps -q") {
			t.Fatalf("devcontainer stop command = %s", cmd)
		}
	})

	t.Run("auth inherit rejects invalid provider after load", func(t *testing.T) {
		_, svc := serviceWithContainer(t, fakeRunner{})
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "nope", Method: AuthEnv, Yes: true}); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Fatalf("expected provider error after load, got %v", err)
		}
	})

	t.Run("auth inherit appends new provider entry", func(t *testing.T) {
		cfg, svc := serviceWithContainer(t, fakeRunner{})
		spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
		before, err := LoadManifest(spacePath)
		if err != nil {
			t.Fatal(err)
		}
		if hasProvider(before.Portals["default"].Auth.Providers, "claude") {
			t.Skip("default portal already has a claude provider")
		}
		if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "claude", Method: AuthEnv, Yes: true}); err != nil {
			t.Fatal(err)
		}
		after, err := LoadManifest(spacePath)
		if err != nil {
			t.Fatal(err)
		}
		var provider *AuthProvider
		for i := range after.Portals["default"].Auth.Providers {
			if after.Portals["default"].Auth.Providers[i].Provider == "claude" {
				provider = &after.Portals["default"].Auth.Providers[i]
			}
		}
		if provider == nil || provider.Status != AuthOK || provider.Target != "portal" {
			t.Fatalf("claude provider not appended correctly: %#v", after.Portals["default"].Auth.Providers)
		}
	})

	t.Run("update auth provider load and missing portal errors", func(t *testing.T) {
		cfg, svc := serviceWithContainer(t, fakeRunner{})
		spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
		// Unknown portal id inside an existing manifest is rejected.
		if err := svc.updateAuthProvider(spacePath, "ghost", "codex", AuthEnv, AuthOK); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("expected missing-portal error, got %v", err)
		}
		// A directory that exists but holds no manifest fails inside the lock,
		// exercising the LoadManifest error path in updateAuthProviderLocked.
		empty := filepath.Join(cfg.AgentWorkDir, "no-manifest")
		if err := os.MkdirAll(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := svc.updateAuthProvider(empty, "default", "codex", AuthEnv, AuthOK); err == nil {
			t.Fatal("expected manifest load error for missing manifest")
		}
	})
}

func serviceWithContainer(t *testing.T, runner fakeRunner) (config.Config, Service) {
	t.Helper()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, runner, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatal(err)
	}
	return cfg, svc
}
