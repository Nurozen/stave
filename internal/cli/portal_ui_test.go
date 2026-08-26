package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/portal"
	"github.com/spf13/cobra"
)

// TestCLIUnknownSubcommandsError pins the group-command guard: a typo'd
// subcommand under any container must fail loudly instead of cobra's default
// print-help-and-exit-0, while a bare group still prints help successfully.
func TestCLIUnknownSubcommandsError(t *testing.T) {
	for _, args := range [][]string{
		{"portal", "bogus"},
		{"space", "bogus"},
		{"repos", "bogus"},
		{"portal", "attach", "bogus"},
		{"portal", "auth", "bogus"},
	} {
		out, err := runCLIError(t, nil, args...)
		if err == nil || !strings.Contains(err.Error(), `unknown command "bogus"`) {
			t.Fatalf("stave %v: err=%v out=%s", args, err, out)
		}
	}

	// A bare group with no args prints help and exits zero.
	if _, err := runCLIError(t, nil, "portal"); err != nil {
		t.Fatalf("bare portal error = %v", err)
	}
}

// TestCLIPortalExecRequiresDashDash confirms the RunE path (not just the
// splitPortalExecArgs helper) rejects a command given without the "--"
// separator, so a mistyped portal id cannot silently become the command.
func TestCLIPortalExecRequiresDashDash(t *testing.T) {
	out, err := runCLIError(t, nil, "portal", "exec", "ex-1", "dev", "echo")
	if err == nil || !strings.Contains(err.Error(), `requires "--"`) {
		t.Fatalf("expected exec dash-dash error, err=%v out=%s", err, out)
	}
}

// TestCLIPortalLogsExecutesAndPreviews pins that logs runs the docker logs
// command by default and that both preview flags suppress execution while
// still printing the plan.
func TestCLIPortalLogsExecutesAndPreviews(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "log line\n"}}
	application := &app{portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return true }}

	runCLIWithApp(t, application, "portal", "logs", "ex-1234", "--tail", "5")
	if len(runner.runs) != 1 {
		t.Fatalf("logs did not execute exactly once: %#v", runner.runs)
	}
	if got := runner.runs[0].String(); !strings.Contains(got, "docker logs") {
		t.Fatalf("logs command = %q, want docker logs", got)
	}

	for _, flag := range []string{"--dry-run", "--print-command"} {
		before := len(runner.runs)
		out := runCLIWithApp(t, application, "portal", "logs", "ex-1234", flag)
		if len(runner.runs) != before {
			t.Fatalf("logs %s executed runner: %#v", flag, runner.runs[before:])
		}
		if !strings.Contains(out, "show logs for portal default") {
			t.Fatalf("logs %s preview output = %s", flag, out)
		}
	}
}

// TestCLIPortalSummonCursorWarnsOnExecutionPath pins that warn-severity
// diagnostics (summon.cursor_partial) reach stderr even on the execution path,
// where the full plan preview is not printed. It must run the commands (not
// dry-run) and still surface the warning before doing so.
func TestCLIPortalSummonCursorWarnsOnExecutionPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")

	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "ok\n"}}
	cmd := newRootCommand(&app{portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"portal", "summon", "ex-1234", "--with", "cursor"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("portal summon error = %v\nstdout=%s\nstderr=%s", err, out.String(), errBuf.String())
	}
	if len(runner.runs) == 0 {
		t.Fatalf("summon did not execute (expected execution path): stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "summon.cursor_partial") {
		t.Fatalf("cursor warn missing from stderr on execution path: %s", errBuf.String())
	}
	// The plan preview (Plan:/Commands:) must NOT be printed on execution.
	if strings.Contains(out.String(), "Plan:") {
		t.Fatalf("execution path leaked full plan preview to stdout: %s", out.String())
	}
}

// TestCLIPortalShellNonTTYNoticeGoesToStderr verifies the non-interactive
// notice is written to stderr while stdout carries only the copy-pasteable
// Plan/Commands preview.
func TestCLIPortalShellNonTTYNoticeGoesToStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLIWithApp(t, &app{portalRunner: &fakePortalRunner{}}, "portal", "attach", "ssh", "ex-1234", "devbox.example")

	cmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"portal", "shell", "ex-1234"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("portal shell error = %v\nstdout=%s\nstderr=%s", err, out.String(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "Non-interactive terminal detected") {
		t.Fatalf("notice missing from stderr: %s", errBuf.String())
	}
	if strings.Contains(out.String(), "Non-interactive terminal detected") {
		t.Fatalf("notice leaked to stdout: %s", out.String())
	}
	if !strings.Contains(out.String(), "Plan:") || !strings.Contains(out.String(), "Commands:") {
		t.Fatalf("stdout missing plan preview: %s", out.String())
	}
}

// TestCLIPortalUpExecuteAppendsOutcomeLine pins that a successful up execution
// appends the "ok: <summary>" outcome line so a bare container id is not the
// only feedback.
func TestCLIPortalUpExecuteAppendsOutcomeLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "container-id\n"}}
	application := &app{portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return true }}

	out := runCLIWithApp(t, application, "portal", "up", "ex-1234")
	if len(runner.runs) == 0 {
		t.Fatal("up did not execute runner")
	}
	if !strings.Contains(out, "ok: start or validate portal default") {
		t.Fatalf("up output missing outcome line: %s", out)
	}
}

// TestCLIPortalAttachSSHDuplicateEchoesParsedHost confirms attach failures wrap
// the error with how the positionals were parsed, so a portal id mistakenly
// passed as the host is visible.
func TestCLIPortalAttachSSHDuplicateEchoesParsedHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	application := &app{portalRunner: &fakePortalRunner{}}
	runCLIWithApp(t, application, "portal", "attach", "ssh", "ex-1234", "devbox.example")

	out, err := runCLIError(t, application, "portal", "attach", "ssh", "ex-1234", "devbox.example")
	if err == nil || !strings.Contains(err.Error(), "parsed host") {
		t.Fatalf("expected parsed-host echo, err=%v out=%s", err, out)
	}
}

// TestCLIPortalStatusBareSpaceHintsMultiplePortals confirms that querying
// status with only a space id and more than one portal prints the disambiguation
// hint on stderr, leaving stdout clean for the shown portal.
func TestCLIPortalStatusBareSpaceHintsMultiplePortals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: `[{"State":{"Status":"running","Health":{"Status":"healthy"}}}]`}}
	application := &app{portalRunner: runner}
	runCLIWithApp(t, application, "portal", "init", "container", "ex-1234")
	runCLIWithApp(t, application, "portal", "attach", "ssh", "ex-1234", "devbox.example", "remote")

	cmd := newRootCommand(application)
	cmd.SetArgs([]string{"portal", "status", "ex-1234"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("portal status error = %v\nstdout=%s\nstderr=%s", err, out.String(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "note: space has 2 portals") {
		t.Fatalf("hint missing from stderr: %s", errBuf.String())
	}
	if strings.Contains(out.String(), "note: space has") {
		t.Fatalf("hint leaked to stdout: %s", out.String())
	}
}
