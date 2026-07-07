package portal

import (
	"context"
	"strings"
	"testing"
)

type recordingRunner struct {
	lookups []string
	runs    []string
}

func (r *recordingRunner) LookPath(name string) (string, error) {
	r.lookups = append(r.lookups, name)
	return "/bin/" + name, nil
}

func (r *recordingRunner) Run(ctx context.Context, command Command) (RunResult, error) {
	r.runs = append(r.runs, command.String())
	return RunResult{Stdout: command.Program}, nil
}

func TestRuntimeRunnersDelegateAllowedCommands(t *testing.T) {
	tests := []struct {
		name    string
		driver  Driver
		program string
	}{
		{"docker", DriverDocker, "docker"},
		{"devcontainer", DriverDevcontainer, "devcontainer"},
		{"devcontainer cleanup shell", DriverDevcontainer, "sh"},
		{"ssh", DriverSSH, "ssh"},
		{"ec2 ssh", DriverEC2Attach, "ssh"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := &recordingRunner{}
			runner, err := NewRuntimeRunner(tc.driver, base)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.LookPath(tc.program); err != nil {
				t.Fatalf("LookPath() error = %v", err)
			}
			result, err := runner.Run(context.Background(), command(tc.program, "version"))
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.Stdout != tc.program || len(base.runs) != 1 {
				t.Fatalf("result=%#v base=%#v", result, base)
			}
		})
	}
}

func TestRuntimeRunnersRejectWrongCommands(t *testing.T) {
	runner := NewDockerRunner(&recordingRunner{})
	if _, err := runner.LookPath("devcontainer"); err == nil || !strings.Contains(err.Error(), "docker runner") {
		t.Fatalf("LookPath wrong program error = %v", err)
	}
	if _, err := runner.Run(context.Background(), command("ssh", "host", "true")); err == nil || !strings.Contains(err.Error(), "docker runner") {
		t.Fatalf("Run wrong program error = %v", err)
	}
	if _, err := NewRuntimeRunner("bogus", nil); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported runtime runner error = %v", err)
	}
}

func TestRuntimeRunnerUsesLocalRunnerByDefault(t *testing.T) {
	runner := NewSSHRunner(nil)
	if _, err := runner.Run(context.Background(), command("docker", "version")); err == nil || !strings.Contains(err.Error(), "ssh runner") {
		t.Fatalf("default runner rejection error = %v", err)
	}
}
