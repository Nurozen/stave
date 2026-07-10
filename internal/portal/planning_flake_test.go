package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestQuoteRemotePathTilde pins the tilde-aware quoting so a leading ~ expands
// via the remote shell ($HOME) instead of being single-quoted into a literal
// directory named "~".
func TestQuoteRemotePathTilde(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "bare tilde", input: "~", want: `"$HOME"`},
		{name: "tilde with safe path", input: "~/stave/agent-work/x", want: `"$HOME"/stave/agent-work/x`},
		{name: "tilde with unsafe rest", input: "~/weird dir/x", want: `"$HOME"/'weird dir/x'`},
		{name: "absolute safe unchanged", input: "/abs/path", want: "/abs/path"},
		{name: "absolute unsafe unchanged", input: "/abs path", want: "'/abs path'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteRemotePath(tt.input); got != tt.want {
				t.Fatalf("quoteRemotePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// quoteRemotePath must agree with quoteShell for non-tilde inputs.
			if !strings.HasPrefix(tt.input, "~") {
				if got := quoteRemotePath(tt.input); got != quoteShell(tt.input) {
					t.Fatalf("non-tilde quoteRemotePath(%q) = %q, want quoteShell %q", tt.input, got, quoteShell(tt.input))
				}
			}
		})
	}
}

// TestRemoteRootTildeConsistencyEndToEnd asserts the mkdir prepare command and
// the exec cd fragment both expand a default remote root's leading tilde the
// same way, and that no single-quoted '~' leaks into any planned command.
func TestRemoteRootTildeConsistencyEndToEnd(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)

	attach, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox.example"})
	if err != nil {
		t.Fatal(err)
	}
	const wantFragment = `"$HOME"/stave/agent-work/ex-1`

	mkdir := attach.EquivalentCommands()
	if len(mkdir) == 0 {
		t.Fatalf("attach produced no prepare commands: %#v", attach)
	}
	if !strings.Contains(mkdir[0], wantFragment) {
		t.Fatalf("mkdir prepare command missing %q: %s", wantFragment, mkdir[0])
	}

	execPlan, err := svc.PlanExec(context.Background(), ExecOptions{SpaceID: "ex-1", Command: []string{"pwd"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execPlan.EquivalentCommands()[0], wantFragment) {
		t.Fatalf("exec cd fragment missing %q: %s", wantFragment, execPlan.EquivalentCommands()[0])
	}

	// No planned command anywhere may still carry an un-expanded tilde.
	for _, cmd := range append(append([]string{}, mkdir...), execPlan.EquivalentCommands()...) {
		if strings.Contains(cmd, "~") {
			t.Fatalf("planned command still contains a literal tilde: %s", cmd)
		}
	}
}

// TestSSHCommandProbeFlags pins the non-interactive probe hardening: BatchMode
// fails fast instead of hanging on a prompt, -n keeps ssh off stdin, and a
// connect timeout bounds the dial.
func TestSSHCommandProbeFlags(t *testing.T) {
	portal := Portal{Driver: DriverSSH, Target: Target{Host: "devbox.example"}}
	got := sshCommand(portal, "true").String()
	const prefix = "ssh -n -o BatchMode=yes -o ConnectTimeout=10"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("sshCommand = %q, want prefix %q", got, prefix)
	}
}

// TestSSHStatusProbeUsesHardenedFlags drives a real Status probe through a
// strict runner keyed on the exact expected command string; if the probe
// dropped the BatchMode/-n/ConnectTimeout flags the strict runner would not
// match and the portal would degrade to warn instead of ok.
func TestSSHStatusProbeUsesHardenedFlags(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", Host: "devbox.example"}); err != nil {
		t.Fatal(err)
	}
	// Derive the exact probe string from the persisted portal (which applies
	// defaults such as -p 22) and assert it carries the hardened flags.
	loaded, _, err := svc.LoadPortal(SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	probe := sshCommand(loaded, "true").String()
	if !strings.HasPrefix(probe, "ssh -n -o BatchMode=yes -o ConnectTimeout=10") {
		t.Fatalf("probe command lacks hardened flags: %q", probe)
	}
	svc.Runner = fakeRunner{strict: true, outputs: map[string]RunResult{probe: {}}}
	status, err := svc.Status(context.Background(), SelectOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Overall != OverallOK || status.State != "reachable" {
		t.Fatalf("ssh status = %#v (probe command did not match the hardened flags)", status)
	}
}

// blockingRunner blocks every Run until the context is cancelled, modelling an
// unreachable target that would otherwise hang a status probe forever.
type blockingRunner struct{}

func (blockingRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }

func (blockingRunner) Run(ctx context.Context, _ Command) (RunResult, error) {
	<-ctx.Done()
	return RunResult{}, ctx.Err()
}

// TestStatusProbeTimesOutInsteadOfHanging asserts a blocking runner cannot hang
// Status: the probe honours the caller's context deadline and returns with a
// warn/error overall rather than blocking indefinitely.
func TestStatusProbeTimesOutInsteadOfHanging(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, blockingRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatal(err)
	}

	// A short caller deadline is honoured by the probe's derived context, so we
	// do not have to wait out the full internal probeTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	type result struct {
		status Status
		err    error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		status, err := svc.Status(ctx, SelectOptions{SpaceID: "ex-1"})
		done <- result{status, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Status returned an error instead of a degraded status: %v", r.err)
		}
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Fatalf("Status took %s; it did not honour the caller deadline", elapsed)
		}
		if r.status.Overall != OverallWarn && r.status.Overall != OverallError {
			t.Fatalf("overall = %q, want warn or error for an unreachable probe; status=%#v", r.status.Overall, r.status)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Status hung past the 60s watchdog deadline")
	}
}

// TestPlanUpDryRunEC2WithoutAWSUsesPlaceholder pins that a dry-run up on an
// ec2 portal whose host is empty and whose aws CLI is unavailable succeeds and
// substitutes the UnresolvedEC2Host placeholder rather than failing.
func TestPlanUpDryRunEC2WithoutAWSUsesPlaceholder(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	// strict runner returns an error for the aws describe-instances probe, so
	// host resolution fails on both attach and the dry-run preview.
	runner := fakeRunner{strict: true}
	svc := NewService(cfg, runner, nil)
	if _, err := svc.AttachEC2(context.Background(), AttachEC2Options{SpaceID: "ex-1", PortalID: "aws", InstanceID: "i-123", Region: "us-west-2"}); err != nil {
		t.Fatal(err)
	}
	// The manifest must keep an empty host (never persist a resolved address).
	manifest, err := LoadManifest(svc.SpacePath("ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["aws"].Target.Host != "" {
		t.Fatalf("attach persisted a host: %#v", manifest.Portals["aws"].Target)
	}

	plan, err := svc.PlanUp(context.Background(), UpOptions{SpaceID: "ex-1", PortalID: "aws", DryRun: true})
	if err != nil {
		t.Fatalf("dry-run up must not fail when aws is unavailable: %v", err)
	}
	joined := strings.Join(plan.EquivalentCommands(), "\n")
	if !strings.Contains(joined, UnresolvedEC2Host) {
		t.Fatalf("dry-run up preview missing %q placeholder:\n%s", UnresolvedEC2Host, joined)
	}
}

// TestPlanShellTTYResolution pins TTY selection against the real stdio state:
// auto only requests a TTY when a terminal is attached, and always forces one
// regardless.
func TestPlanShellTTYResolution(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.InitContainer(context.Background(), InitContainerOptions{SpaceID: "ex-1", PortalID: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachSSH(context.Background(), AttachSSHOptions{SpaceID: "ex-1", PortalID: "remote", Host: "devbox.example"}); err != nil {
		t.Fatal(err)
	}

	shell := func(portalID string, tty TTYMode) string {
		plan, err := svc.PlanShell(context.Background(), ShellOptions{SpaceID: "ex-1", PortalID: portalID, TTY: tty})
		if err != nil {
			t.Fatal(err)
		}
		return plan.EquivalentCommands()[0]
	}

	// Non-terminal stdio: auto must not allocate a TTY.
	svc.IsTerminal = func() bool { return false }
	if got := shell("default", ""); !strings.Contains(got, "docker exec -i ") || strings.Contains(got, "docker exec -it") {
		t.Fatalf("non-terminal docker shell = %q, want -i (not -it)", got)
	}
	if got := shell("remote", ""); strings.Contains(got, " -t ") {
		t.Fatalf("non-terminal ssh shell = %q, want no -t", got)
	}

	// Terminal stdio: auto allocates a TTY for interactive shells.
	svc.IsTerminal = func() bool { return true }
	if got := shell("default", ""); !strings.Contains(got, "docker exec -it") {
		t.Fatalf("terminal docker shell = %q, want -it", got)
	}
	if got := shell("remote", ""); !strings.Contains(got, " -t ") {
		t.Fatalf("terminal ssh shell = %q, want -t", got)
	}

	// TTYAlways forces a TTY even when stdio is not a terminal.
	svc.IsTerminal = func() bool { return false }
	if got := shell("default", TTYAlways); !strings.Contains(got, "docker exec -it") {
		t.Fatalf("always docker shell = %q, want -it", got)
	}
	if got := shell("remote", TTYAlways); !strings.Contains(got, " -t ") {
		t.Fatalf("always ssh shell = %q, want -t", got)
	}
}
