package portal

import (
	"context"
	"strings"
	"testing"
)

func diagCodes(plan Plan) []string {
	codes := make([]string, 0, len(plan.Diagnostics))
	for _, d := range plan.Diagnostics {
		codes = append(codes, d.Code)
	}
	return codes
}

func hasDiag(plan Plan, code string) bool {
	for _, d := range plan.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func hasWarnDiag(plan Plan, code string) bool {
	for _, d := range plan.Diagnostics {
		if d.Code == code && d.Severity == SeverityWarn {
			return true
		}
	}
	return false
}

// BF1 (D15/D16/D18): tmux summon emits an idempotent detached create that
// preserves the summoner's provider auth env, and only attaches when a real
// terminal is present.
func TestSummonTmuxDockerDetachedCarriesAuthEnv(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})
	if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthEnv, Yes: true}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }

	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	cmds := plan.EquivalentCommands()
	if len(cmds) != 1 {
		t.Fatalf("non-tty tmux must emit only the detached create, got %v", cmds)
	}
	if !strings.Contains(cmds[0], "docker exec") {
		t.Fatalf("expected docker exec row, got %s", cmds[0])
	}
	// P1: docker has no shell in the exec path, so the has-session guard is
	// wrapped in `sh -c`. The guard makes the detached create idempotent — a
	// re-summon is a true no-op that needs no TTY and never errors.
	if !strings.Contains(cmds[0], "sh -c") {
		t.Fatalf("expected sh -c wrap for docker guard, got %s", cmds[0])
	}
	if !strings.Contains(cmds[0], "tmux has-session -t stave-ex-1-default 2>/dev/null || tmux new-session -d -s stave-ex-1-default") {
		t.Fatalf("expected idempotent has-session||new-session guard, got %s", cmds[0])
	}
	// -A must be gone: it does not keep an existing session detached.
	if strings.Contains(cmds[0], "new-session -A") {
		t.Fatalf("new-session -A must not be used (not detach-safe on reuse): %s", cmds[0])
	}
	// D16 regression: argv[0] is now sh/tmux, but the codex env must still survive.
	if !strings.Contains(cmds[0], "-e OPENAI_API_KEY") {
		t.Fatalf("auth env dropped after tmux wrap: %s", cmds[0])
	}
	if !hasDiag(plan, "summon.tmux_requires_tmux") {
		t.Fatalf("missing tmux-required diagnostic: %v", diagCodes(plan))
	}
	if !hasDiag(plan, "summon.tmux_detached") {
		t.Fatalf("missing detached-attach notice: %v", diagCodes(plan))
	}
}

func TestSummonTmuxDockerAppendsAttachWhenInteractive(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})
	svc.IsTerminal = func() bool { return true }

	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	cmds := plan.EquivalentCommands()
	if len(cmds) != 2 {
		t.Fatalf("interactive tmux must emit create+attach, got %v", cmds)
	}
	if !strings.Contains(cmds[0], "tmux has-session -t stave-ex-1-default 2>/dev/null || tmux new-session -d -s stave-ex-1-default") {
		t.Fatalf("expected guarded create first, got %s", cmds[0])
	}
	if !strings.Contains(cmds[1], "tmux attach-session -t stave-ex-1-default") {
		t.Fatalf("expected attach second, got %s", cmds[1])
	}
	if !strings.Contains(cmds[1], "-it") {
		t.Fatalf("attach should request a tty: %s", cmds[1])
	}
	if hasDiag(plan, "summon.tmux_detached") {
		t.Fatalf("interactive summon should not emit the detached notice: %v", diagCodes(plan))
	}
}

// D16 + quoting: ssh tmux summon keeps SendEnv and round-trips a
// spaces/quotes prompt through the remote-command serialization.
func TestSummonTmuxSSHSendEnvAndPromptQuoting(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", PortalID: "ssh", Preset: "ssh-codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", PortalID: "ssh", Provider: "codex", Method: AuthEnv, Yes: true}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }

	prompt := `look at "spec" it's here`
	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "ssh", With: "codex", Mode: "tmux", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	cmds := plan.EquivalentCommands()
	if len(cmds) != 1 {
		t.Fatalf("non-tty ssh tmux must emit only the detached create, got %v", cmds)
	}
	if !strings.Contains(cmds[0], "ssh") || !strings.Contains(cmds[0], "SendEnv=OPENAI_API_KEY") {
		t.Fatalf("ssh SendEnv dropped after tmux wrap: %s", cmds[0])
	}
	remote := plan.Commands[0].Args[len(plan.Commands[0].Args)-1]
	// P1: ssh already has a remote shell, so the || guard is embedded directly
	// in the remote command string (no sh -c wrap needed).
	if !strings.Contains(remote, "tmux has-session -t stave-ex-1-ssh 2>/dev/null || exec tmux new-session -d -s stave-ex-1-ssh") {
		t.Fatalf("ssh remote tmux guard malformed: %s", remote)
	}
	if strings.Contains(remote, "new-session -A") {
		t.Fatalf("new-session -A must not be used on ssh (not detach-safe on reuse): %s", remote)
	}
	if !strings.Contains(remote, `look at "spec"`) || !strings.Contains(remote, `s here`) {
		t.Fatalf("prompt was lost while serializing nested login shells: %s", remote)
	}
	if strings.Count(remote, `${SHELL:-/bin/sh}`) < 2 {
		t.Fatalf("ssh tmux summon must initialize both client and pane login shells: %s", remote)
	}
}

// P2: preview (--print-command / --dry-run) must not emit the attach command
// even on a real TTY — only the guarded detached create is shown.
func TestSummonTmuxDryRunTTYEmitsCreateOnly(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})
	svc.IsTerminal = func() bool { return true }

	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "tmux", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	cmds := plan.EquivalentCommands()
	if len(cmds) != 1 {
		t.Fatalf("dry-run tmux on a TTY must emit only the create, got %v", cmds)
	}
	if strings.Contains(cmds[0], "attach-session") {
		t.Fatalf("preview must not emit attach: %s", cmds[0])
	}
	if !strings.Contains(cmds[0], "tmux has-session -t stave-ex-1-default 2>/dev/null || tmux new-session -d -s stave-ex-1-default") {
		t.Fatalf("expected guarded create in preview, got %s", cmds[0])
	}
}

// P1 + quoting: a spaces/quotes prompt round-trips through the docker `sh -c`
// wrap, and the auth env survives the wrap.
func TestSummonTmuxDockerPromptQuoting(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})
	if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: AuthEnv, Yes: true}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }

	prompt := `look at "spec" it's here`
	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: "tmux", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	cmds := plan.EquivalentCommands()
	if len(cmds) != 1 {
		t.Fatalf("non-tty docker tmux must emit only the detached create, got %v", cmds)
	}
	// The guard is a single arg to `sh -c`; within it the prompt is quoted for
	// the inner shell. The guard arg is then re-quoted by Command.String, so the
	// inner single-quotes are escaped as '"'"'. The raw sh -c arg holds the
	// un-re-quoted form, so inspect Command.Args directly for the round-trip.
	// argv is `docker exec ... sh -c <guard>`; the guard compound is the last arg.
	args := plan.Commands[0].Args
	guard := args[len(args)-1]
	if !strings.Contains(guard, `codex --cd /workspace/ex-1 'look at "spec" it'"'"'s here'`) {
		t.Fatalf("prompt did not round-trip through docker sh -c wrap: %s", guard)
	}
	if !strings.Contains(cmds[0], "-e OPENAI_API_KEY") {
		t.Fatalf("auth env dropped through docker sh -c wrap: %s", cmds[0])
	}
}

// D18 regression (guards CI hang): the non-tmux modes never attach and always
// emit exactly one command, even when a terminal is attached.
func TestSummonNonTmuxModesEmitSingleCommand(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})
	svc.IsTerminal = func() bool { return true }

	for _, mode := range []string{"", "foreground", "print", "headless"} {
		plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "codex", Mode: mode})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		cmds := plan.EquivalentCommands()
		if len(cmds) != 1 {
			t.Fatalf("mode %q expected exactly one command, got %v", mode, cmds)
		}
		if strings.Contains(cmds[0], "tmux") {
			t.Fatalf("mode %q must not wrap in tmux: %s", mode, cmds[0])
		}
	}
}

// D17: summon (tmux) and logs pane-capture share the summoner-independent
// session name with no trailing agent segment.
func TestSummonAndLogsSessionParity(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", Host: "devbox", PortalID: "ssh"}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }

	summon, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "ssh", Mode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	logs, err := svc.PlanLogs(ctx, LogsOptions{SpaceID: "ex-1", PortalID: "ssh", Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := "stave-ex-1-ssh"
	if !strings.Contains(summon.EquivalentCommands()[0], want) {
		t.Fatalf("summon session = %s", summon.EquivalentCommands()[0])
	}
	got := logs.EquivalentCommands()[0]
	if !strings.Contains(got, want) || strings.Contains(got, want+"-") {
		t.Fatalf("logs session should be %q with no agent segment: %s", want, got)
	}
}

// BF3: cursor summon and cursor auth login emit an honest partial-support warn.
func TestSummonCursorPartialWarnings(t *testing.T) {
	ctx := context.Background()
	_, svc := serviceWithContainer(t, fakeRunner{})

	summon, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", With: "cursor", Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarnDiag(summon, "summon.cursor_partial") {
		t.Fatalf("missing cursor summon warning: %v", diagCodes(summon))
	}
	// KEEP existing argv: cursor-agent is still launched.
	if !strings.Contains(summon.EquivalentCommands()[0], "cursor-agent") {
		t.Fatalf("cursor argv changed: %s", summon.EquivalentCommands()[0])
	}

	login, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarnDiag(login, "summon.cursor_partial") {
		t.Fatalf("missing cursor auth warning: %v", diagCodes(login))
	}
	if !strings.Contains(login.EquivalentCommands()[0], "cursor-agent status") {
		t.Fatalf("cursor auth argv changed: %s", login.EquivalentCommands()[0])
	}
}
