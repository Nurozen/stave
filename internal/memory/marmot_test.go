package memory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDenCreateArgsAlwaysNoPointerAndJSON(t *testing.T) {
	args := DenCreateArgs("demo-space", "/Users/x/stave/agent-work/demo-space", "task", nil, nil, nil, nil, false)
	joined := strings.Join(args, " ")
	if !containsAll(args, "--no-pointer", "--json", "--lifetime", "task", "--project") {
		t.Fatalf("args missing required flags: %v", args)
	}
	if strings.Contains(joined, ".marmot-vault") {
		t.Fatalf("must not mention .marmot-vault: %v", args)
	}
	line := FormatCommand("marmot", args)
	if !strings.Contains(line, "--no-pointer") || !strings.Contains(line, "--json") {
		t.Fatalf("formatted line: %s", line)
	}
	// Capture dry-run line for evidence artifact.
	t.Logf("dry-run line: %s", line)
}

func TestAttachDryRunDoesNotInvokeBinary(t *testing.T) {
	// Dry-run path never calls lookPath — verify via Attach.
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not look up binary")
			return "", nil
		},
	}
	var buf strings.Builder
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "demo-space",
		SpacePath: "/tmp/demo-space",
		DryRun:    true,
		Out:       &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DryRunCommands) != 1 {
		t.Fatalf("DryRunCommands = %#v", res.DryRunCommands)
	}
	line := res.DryRunCommands[0]
	if !strings.Contains(line, "--no-pointer") {
		t.Fatalf("expected --no-pointer in %q", line)
	}
	if !strings.Contains(line, "--json") {
		t.Fatalf("expected --json in %q", line)
	}
	if !strings.Contains(line, "den create") {
		t.Fatalf("expected den create in %q", line)
	}
	if !strings.Contains(line, "--lifetime task") {
		t.Fatalf("expected lifetime task in %q", line)
	}
	if !strings.Contains(buf.String(), "dry-run:") {
		t.Fatalf("stdout missing dry-run: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "dry-run: set watch_sources: false in den vault config (under MARMOT_HOME; keeps the den agent-authored)") {
		t.Fatalf("owned-create dry-run must plan the watch_sources write: %s", buf.String())
	}
	if res.Owned != true {
		t.Fatal("fresh create should be owned")
	}
}

func TestContractFixturesUnmarshal(t *testing.T) {
	dir := filepath.Join("testdata", "contracts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read contracts: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if env.Schema != SupportedJSONSchema {
			// warren_status may still be schema 1; error is schema 1.
			if env.Schema == 0 {
				t.Fatalf("%s: missing schema", e.Name())
			}
		}
	}
	// Pin no-pointer fixture shape.
	data, err := os.ReadFile(filepath.Join(dir, "den_create_no_pointer.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.PointerWritten {
		t.Fatal("den_create_no_pointer must have pointer_written:false")
	}
	if env.DenID == "" {
		t.Fatal("expected den_id")
	}
}

func TestWriteSpaceMCPConfigNoVaultPointer(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot", "demo-space"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatalf(".marmot-vault must not exist: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "context-marmot") {
		t.Fatalf("mcp config: %s", raw)
	}
	if !strings.Contains(string(raw), "demo-space") {
		t.Fatalf("mcp config should reference den: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursor", "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vscode", "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestParseMemorySpec(t *testing.T) {
	tests := []struct {
		raw  string
		want MemorySpec
	}{
		{".", MemorySpec{Provider: "marmot", Spec: ".", Fresh: true}},
		{"marmot", MemorySpec{Provider: "marmot", Spec: ".", Fresh: true}},
		{"marmot:.", MemorySpec{Provider: "marmot", Spec: ".", Fresh: true}},
		{"marmot:existing-den", MemorySpec{Provider: "marmot", Spec: "existing-den", Fresh: false}},
		{"existing-den", MemorySpec{Provider: "marmot", Spec: "existing-den", Fresh: false}},
	}
	for _, tc := range tests {
		got, err := ParseMemorySpec(tc.raw, "marmot")
		if err != nil {
			t.Fatalf("ParseMemorySpec(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseMemorySpec(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
	if _, err := ParseMemorySpec("../bad", "marmot"); err == nil {
		t.Fatal("expected invalid id")
	}
}

func TestParseMemoryFate(t *testing.T) {
	f, err := ParseMemoryFate("")
	if err != nil || f != FateKeep {
		t.Fatalf("default = %q %v", f, err)
	}
	if _, err := ParseMemoryFate("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDetachArchiveRouteUsesFromToFlags(t *testing.T) {
	// D6: archive must call `marmot route set-project --from <old> --to <new> --json`
	// (not positional args). Dry-run never invokes the binary.
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not look up binary")
			return "", nil
		},
	}
	var buf strings.Builder
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID:      "arch-den",
		SpacePath:    "/work/agent-work/arch",
		NewSpacePath: "/work/agent-work/.archive/arch",
		Fate:         FateKeep,
		Owned:        true,
		DryRun:       true,
		Out:          &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DryRunCommands) != 1 {
		t.Fatalf("DryRunCommands = %#v (archive must not strip MCP)", res.DryRunCommands)
	}
	line := res.DryRunCommands[0]
	for _, need := range []string{"route set-project", "--from", "--to", "--json", "/work/agent-work/arch", "/work/agent-work/.archive/arch"} {
		if !strings.Contains(line, need) {
			t.Fatalf("expected %q in %q", need, line)
		}
	}
	// Must not use broken positional form: set-project <new> <id>
	if strings.Contains(line, "set-project /work/agent-work/.archive/arch arch-den") {
		t.Fatalf("positional set-project form is wrong: %q", line)
	}
}

func TestDetachRemoveRouteUsesProjectFlag(t *testing.T) {
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not look up binary")
			return "", nil
		},
	}
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID:     "keep-den",
		SpacePath:   "/work/agent-work/keep",
		RemoveRoute: true,
		Fate:        FateKeep,
		Owned:       true,
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// route rm + MCP cleanup dry-run lines
	if len(res.DryRunCommands) != 2 {
		t.Fatalf("DryRunCommands = %#v", res.DryRunCommands)
	}
	line := res.DryRunCommands[0]
	if !strings.Contains(line, "route rm") || !strings.Contains(line, "--project") {
		t.Fatalf("expected route rm --project: %q", line)
	}
	if !strings.Contains(res.DryRunCommands[1], "MCP") {
		t.Fatalf("expected MCP cleanup dry-run line: %#v", res.DryRunCommands)
	}
}

func containsAll(args []string, need ...string) bool {
	set := map[string]bool{}
	for _, a := range args {
		set[a] = true
	}
	for _, n := range need {
		if !set[n] {
			return false
		}
	}
	return true
}

// captureDirMarmot builds a Marmot whose Command stub records each spawned
// *exec.Cmd (after runRaw sets Dir) and always prints a schema-1 envelope.
func captureDirMarmot(t *testing.T, cmds *[]*exec.Cmd) *Marmot {
	t.Helper()
	return &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
			*cmds = append(*cmds, cmd)
			return cmd
		},
	}
}

func TestProposeRunsSubprocessInSpacePath(t *testing.T) {
	// The load-bearing fix: `marmot warren propose` resolves the workspace
	// from cwd via reverse routes, so Propose must run den contribute AND
	// warren propose with cwd = the space path, not stave's cwd.
	spacePath := t.TempDir()
	var cmds []*exec.Cmd
	m := captureDirMarmot(t, &cmds)
	if _, err := m.Propose(context.Background(), ProposeOptions{StoreID: "t1", SpacePath: spacePath}); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 subprocesses (contribute, propose), got %d", len(cmds))
	}
	for i, cmd := range cmds {
		if cmd.Dir != spacePath {
			t.Fatalf("cmd[%d].Dir = %q, want space path %q", i, cmd.Dir, spacePath)
		}
	}
}

func TestProposeWithoutSpacePathLeavesDirUnset(t *testing.T) {
	var cmds []*exec.Cmd
	m := captureDirMarmot(t, &cmds)
	if _, err := m.Propose(context.Background(), ProposeOptions{StoreID: "t1"}); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range cmds {
		if cmd.Dir != "" {
			t.Fatalf("cmd[%d].Dir = %q, want empty (inherit cwd)", i, cmd.Dir)
		}
	}
}

func TestDetachContributeRunsSubprocessInSpacePath(t *testing.T) {
	// Archive/destroy fate=contribute shells out den contribute + warren
	// propose + den destroy; all must run with cwd = the space path.
	spacePath := t.TempDir()
	var cmds []*exec.Cmd
	m := captureDirMarmot(t, &cmds)
	if _, err := m.Detach(context.Background(), DetachOptions{
		StoreID:   "t1",
		SpacePath: spacePath,
		Fate:      FateContribute,
		Owned:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 {
		t.Fatalf("expected 3 subprocesses (contribute, propose, destroy), got %d", len(cmds))
	}
	for i, cmd := range cmds {
		if cmd.Dir != spacePath {
			t.Fatalf("cmd[%d].Dir = %q, want space path %q", i, cmd.Dir, spacePath)
		}
	}
}

func TestAttachRunsSubprocessInSpacePath(t *testing.T) {
	spacePath := t.TempDir()
	var cmds []*exec.Cmd
	m := captureDirMarmot(t, &cmds)
	m.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1,"den_id":"t1"}'`)
		cmds = append(cmds, cmd)
		return cmd
	}
	if _, err := m.Attach(context.Background(), AttachOptions{SpaceID: "t1", SpacePath: spacePath}); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 subprocess, got %d", len(cmds))
	}
	if cmds[0].Dir != spacePath {
		t.Fatalf("den create Dir = %q, want %q", cmds[0].Dir, spacePath)
	}
}
