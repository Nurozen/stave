package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/portal"
	"github.com/spf13/cobra"
)

// TestCLIPortalUpSurfacesSuppressedStartStderr pins the fallback error
// surfacing: `docker start` runs with ContinueOnError so its stderr is held
// back while the `docker run` fallback is attempted, but when the fallback
// also fails the final error must include the start command's suppressed
// stderr instead of dropping it.
func TestCLIPortalUpSurfacesSuppressedStartStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")

	// First command (docker start) fails with distinctive stderr; second
	// command (docker run fallback) also fails. The loop must surface the
	// first command's held-back stderr in the returned error.
	runner := &sequencePortalRunner{
		results: []portal.RunResult{{Stderr: "docker start: daemon connection refused\n"}, {Stderr: ""}},
		errors:  []error{errors.New("start exited 1"), errors.New("run exited 125")},
	}
	application := &app{portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return true }}

	out, err := runCLIError(t, application, "portal", "up", "ex-1234")
	if err == nil {
		t.Fatalf("expected portal up to fail; out=%s", out)
	}
	if len(runner.runs) != 2 {
		t.Fatalf("expected start+run fallback (2 commands), got %#v", runner.runs)
	}
	if !strings.Contains(err.Error(), "daemon connection refused") {
		t.Fatalf("final error dropped the suppressed start stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "run exited 125") {
		t.Fatalf("final error missing the fallback failure: %v", err)
	}
}
