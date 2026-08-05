package portal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigureRejectsUnknownAuthMode pins that an unsupported --auth value is
// rejected with the allowed-set message and leaves the on-disk manifest
// unchanged.
func TestConfigureRejectsUnknownAuthMode(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "default"}); err != nil {
		t.Fatal(err)
	}
	before, err := LoadManifest(svc.SpacePath("ex-1"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Configure(ConfigureOptions{SpaceID: "ex-1", PortalID: "default", AuthMode: AuthMode("bogusauth")})
	if err == nil || !strings.Contains(err.Error(), "native, env, volume, ssh-forward, or remote-login") {
		t.Fatalf("expected allowed-set auth error, got %v", err)
	}
	after, err := LoadManifest(svc.SpacePath("ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Portals["default"].Auth.Mode != before.Portals["default"].Auth.Mode {
		t.Fatalf("rejected configure mutated manifest: before=%q after=%q", before.Portals["default"].Auth.Mode, after.Portals["default"].Auth.Mode)
	}
}

// TestInitContainerRejectsUnknownPreset pins that an unsupported --preset lists
// the supported presets.
func TestInitContainerRejectsUnknownPreset(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	_, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "default", Preset: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "local-codex, local-claude, claude-devcontainer, ssh-codex, or ssh-claude") {
		t.Fatalf("expected supported-preset list, got %v", err)
	}
}

// TestPlanSummonAndExecRejectUnknownEnums pins the enum guards on summon mode,
// summon permission, and tty mode.
func TestPlanSummonAndExecRejectUnknownEnums(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "default"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "default", Mode: "nope"}); err == nil || !strings.Contains(err.Error(), "foreground, tmux, headless, or print") {
		t.Fatalf("expected summon mode error, got %v", err)
	}
	if _, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "default", Permission: "bogus"}); err == nil || !strings.Contains(err.Error(), "read-only or workspace-write") {
		t.Fatalf("expected summon permission error, got %v", err)
	}
	if _, err := svc.PlanExec(ctx, ExecOptions{SpaceID: "ex-1", PortalID: "default", Command: []string{"true"}, TTY: TTYMode("banana")}); err == nil || !strings.Contains(err.Error(), "auto, always, or never") {
		t.Fatalf("expected exec tty error, got %v", err)
	}
	if _, err := svc.PlanShell(ctx, ShellOptions{SpaceID: "ex-1", PortalID: "default", TTY: TTYMode("banana")}); err == nil || !strings.Contains(err.Error(), "auto, always, or never") {
		t.Fatalf("expected shell tty error, got %v", err)
	}
}

// TestPlanPreviewEC2PlaceholderInvariant extends the PlanUp placeholder
// guarantee to exec, shell, and logs: a dry-run against an ec2 portal with an
// unresolved host must succeed and surface the placeholder rather than failing
// on the aws probe.
func TestPlanPreviewEC2PlaceholderInvariant(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	// strict runner errors on the aws describe-instances probe so host
	// resolution fails on every path.
	svc := NewService(cfg, fakeRunner{strict: true}, nil)
	ctx := context.Background()
	if _, err := svc.AttachEC2(ctx, AttachEC2Options{SpaceID: "ex-1", PortalID: "aws", InstanceID: "i-123", Region: "us-west-2"}); err != nil {
		t.Fatal(err)
	}

	previews := map[string]func() (Plan, error){
		"exec": func() (Plan, error) {
			return svc.PlanExec(ctx, ExecOptions{SpaceID: "ex-1", PortalID: "aws", Command: []string{"true"}, DryRun: true})
		},
		"shell": func() (Plan, error) {
			return svc.PlanShell(ctx, ShellOptions{SpaceID: "ex-1", PortalID: "aws", DryRun: true})
		},
		"logs": func() (Plan, error) {
			return svc.PlanLogs(ctx, LogsOptions{SpaceID: "ex-1", PortalID: "aws", DryRun: true})
		},
	}
	for name, build := range previews {
		plan, err := build()
		if err != nil {
			t.Fatalf("%s dry-run must not fail when aws is unavailable: %v", name, err)
		}
		joined := strings.Join(plan.EquivalentCommands(), "\n")
		if !strings.Contains(joined, UnresolvedEC2Host) {
			t.Fatalf("%s dry-run preview missing %q placeholder:\n%s", name, UnresolvedEC2Host, joined)
		}
	}
}

// TestPlanSummonRemoteUsesCurrentDir pins that a remote (ssh) portal summon
// points codex at the current directory (the exec wrapper already cd's there),
// while a local docker portal keeps an absolute --cd path.
func TestPlanSummonRemoteUsesCurrentDir(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	ctx := context.Background()
	if _, err := svc.InitContainer(ctx, InitContainerOptions{SpaceID: "ex-1", PortalID: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "remote", Host: "devbox.example"}); err != nil {
		t.Fatal(err)
	}

	remote, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "remote", With: "codex", Mode: "print", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(remote.EquivalentCommands(), "\n"); !strings.Contains(joined, "codex --cd .") {
		t.Fatalf("remote summon missing 'codex --cd .':\n%s", joined)
	}

	local, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "default", With: "codex", Mode: "print", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(local.EquivalentCommands(), "\n")
	if !strings.Contains(joined, "codex --cd /workspace/") {
		t.Fatalf("local summon missing absolute --cd path:\n%s", joined)
	}
	if strings.Contains(joined, "codex --cd .") {
		t.Fatalf("local summon must not use current-dir --cd:\n%s", joined)
	}
}

// TestLoadPortalAndStatusRejectMissingSpace pins the actionable error when a
// space directory does not exist.
func TestLoadPortalAndStatusRejectMissingSpace(t *testing.T) {
	cfg := testConfig(t)
	svc := NewService(cfg, fakeRunner{}, nil)
	const want = "does not exist; create it with stave space create"

	if _, _, err := svc.LoadPortal(SelectOptions{SpaceID: "ghost"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadPortal missing space error = %v", err)
	}
	if _, err := svc.Status(context.Background(), SelectOptions{SpaceID: "ghost"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Status missing space error = %v", err)
	}
}

// TestDiagnosticJSONUsesSnakeCaseNextAction guards the machine-readable JSON
// contract: next_action, never nextAction.
func TestDiagnosticJSONUsesSnakeCaseNextAction(t *testing.T) {
	data, err := json.Marshal(Diagnostic{Component: "auth", Code: "auth.missing", Message: "m", NextAction: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"next_action"`) {
		t.Fatalf("Diagnostic JSON missing next_action key: %s", data)
	}
	if strings.Contains(string(data), `"nextAction"`) {
		t.Fatalf("Diagnostic JSON leaked camelCase key: %s", data)
	}
}
