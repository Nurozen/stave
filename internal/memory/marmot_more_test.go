package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
)

// writeFakeMarmot installs a shell script that pretends to be marmot.
// behavior is a map from a substring of argv joined by space → stdout + exit code.
// Special keys:
//
//	"den --help" / "--version" / "den create" / "den status" / "den destroy" /
//	"den contribute" / "warren sync" / "warren propose" / "route set-project" / "route rm"
func writeFakeMarmot(t *testing.T, responses map[string]fakeResp) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "marmot")
	// Script: match first known key contained in "$*", print body, exit code.
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("args=\"$*\"\n")
	// Order matters: longer / more specific first.
	keys := []string{
		"den create", "den destroy", "den contribute", "den status", "den link", "den --help",
		"warren --help", "warren sync", "warren propose", "route set-project", "route rm", "route add", "--version",
	}
	for _, k := range keys {
		r, ok := responses[k]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "case \"$args\" in *%q*)\n", k)
		if r.Stdout != "" {
			// Use printf %s to avoid echo -e quirks; embed via heredoc-ish.
			escaped := strings.ReplaceAll(r.Stdout, `'`, `'\''`)
			fmt.Fprintf(&b, "  printf '%%s' '%s'\n", escaped)
		}
		if r.Stderr != "" {
			escaped := strings.ReplaceAll(r.Stderr, `'`, `'\''`)
			fmt.Fprintf(&b, "  printf '%%s' '%s' >&2\n", escaped)
		}
		fmt.Fprintf(&b, "  exit %d\n", r.Code)
		b.WriteString("  ;;\nesac\n")
	}
	// default: if den --help not matched earlier, succeed empty for capability probe fallback
	b.WriteString("echo \"unhandled: $args\" >&2\nexit 1\n")
	if err := os.WriteFile(bin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

type fakeResp struct {
	Stdout string
	Stderr string
	Code   int
}

func okEnv(denID string) string {
	return fmt.Sprintf(`{"schema":1,"den_id":%q,"den_path":"/tmp/dens/%s","pointer_written":false,"warnings":[]}`, denID, denID)
}

func TestNewMarmotDefaults(t *testing.T) {
	m := NewMarmot("")
	if m.Binary != "marmot" {
		t.Fatalf("Binary = %q", m.Binary)
	}
	if m.Name() != "marmot" {
		t.Fatalf("Name = %q", m.Name())
	}
	m2 := NewMarmot("custom")
	if m2.Binary != "custom" || m2.binary() != "custom" {
		t.Fatalf("custom binary: %#v", m2)
	}
	// Empty Binary field still defaults via binary().
	m3 := &Marmot{}
	if m3.binary() != "marmot" {
		t.Fatalf("empty binary() = %q", m3.binary())
	}
}

func TestProbeBinaryMissing(t *testing.T) {
	m := &Marmot{
		Binary: "no-such-marmot-xyz",
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
	res, err := m.Probe(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	if res.Available || res.Capable {
		t.Fatalf("res = %#v", res)
	}
	if !strings.Contains(res.Message, "not found") {
		t.Fatalf("message = %q", res.Message)
	}
	_ = ue.Error()
	_ = ue.Unwrap()
}

func TestProbeLacksDenVerbs(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den --help": {Stderr: "unknown command den", Code: 1},
		"--version":  {Stdout: "marmot 0.1\n", Code: 0},
	})
	m := NewMarmot(bin)
	res, err := m.Probe(context.Background())
	if err == nil {
		t.Fatal("expected UnsupportedError")
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnsupportedError, got %T %v", err, err)
	}
	if !res.Available || res.Capable {
		t.Fatalf("res = %#v", res)
	}
	_ = ue.Error()
	// With empty Hint still formats.
	_ = (&UnsupportedError{Provider: "marmot", Feature: "x"}).Error()
}

func TestProbeOK(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den --help": {Stdout: "usage: den\n", Code: 0},
		"--version":  {Stdout: "marmot 9.9.9\nextra\n", Code: 0},
	})
	m := NewMarmot(bin)
	res, err := m.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || !res.Capable {
		t.Fatalf("res = %#v", res)
	}
	if !strings.Contains(res.Version, "marmot 9.9.9") {
		t.Fatalf("version = %q", res.Version)
	}
	if !strings.Contains(res.Message, "marmot ok") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestDenCreateArgsRefsAndLinks(t *testing.T) {
	// S2 (passthrough=false): DenCreateArgs deliberately ignores
	// edit/link/refSpecs/opts when the installed marmot does not accept them.
	// Still always emits --no-pointer/--json and default lifetime task.
	args := DenCreateArgs("id", "/proj", "", []string{"a"}, []string{"b"}, []ReferenceSpec{
		{Name: "repo", URL: "https://example.com/r.git", Ref: "main", MarmotVault: "vault-1"},
	}, map[string]string{"embedding-provider": "mock"}, false)
	joined := strings.Join(args, " ")
	for _, need := range []string{"den", "create", "id", "--no-pointer", "--json", "--lifetime", "task", "--project", "/proj"} {
		if !containsAll(args, need) && !strings.Contains(joined, need) {
			// containsAll for tokens; joined for multi-token already covered
			_ = need
		}
	}
	if !containsAll(args, "den", "create", "id", "--lifetime", "task", "--project", "/proj", "--no-pointer", "--json") {
		t.Fatalf("args = %v", args)
	}
	// Must not pass-through S4 flags yet (would break attach on older marmot).
	for _, ban := range []string{"--edit", "--link", "--ref", "--opt", ".marmot-vault"} {
		if strings.Contains(joined, ban) {
			t.Fatalf("S2 must not include %q: %v", ban, args)
		}
	}
}

func TestAttachCreateSuccess(t *testing.T) {
	env := okEnv("demo-space")
	// pointer_written true path also covered via second call if needed — use true once.
	envPtr := `{"schema":1,"den_id":"demo-space","den_path":"/tmp/dens/demo-space","pointer_written":true,"warnings":["w1"]}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: envPtr, Code: 0},
	})
	m := NewMarmot(bin)
	dir := t.TempDir()
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "demo-space",
		SpacePath: dir,
		Name:      "",
		Lifetime:  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Owned || res.StoreID != "demo-space" || !res.MCPConfigWritten {
		t.Fatalf("res = %#v", res)
	}
	if res.StorePath == "" {
		t.Fatal("expected den path")
	}
	// pointer_written warning + envelope warning
	foundPtr := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "pointer_written") {
			foundPtr = true
		}
	}
	if !foundPtr {
		t.Fatalf("warnings = %#v", res.Warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault must not exist")
	}
	_ = env
}

func TestAttachUseIDExisting(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stdout: okEnv("existing-den"), Code: 0},
		"route add":  {Stdout: `{"schema":1}`, Code: 0},
	})
	m := NewMarmot(bin)
	dir := t.TempDir()
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "space",
		SpacePath: dir,
		UseID:     "existing-den",
		Name:      "shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Owned || res.StoreID != "existing-den" || !res.MCPConfigWritten {
		t.Fatalf("res = %#v", res)
	}
}

func TestAttachUseIDRegistersReverseRoute(t *testing.T) {
	// G2: attach-existing must register the reverse route (space path → den id)
	// so later space-level route ops and cwd-based resolution work.
	var calls [][]string
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			calls = append(calls, args)
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
		},
	}
	dir := t.TempDir()
	if _, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "space",
		SpacePath: dir,
		UseID:     "shared-den",
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, args := range calls {
		if containsAll(args, "route", "add", "--project", dir, "shared-den", "--json") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected route add --project %s shared-den; calls = %#v", dir, calls)
	}
	// Attach fails hard when route registration fails (no half-attached state).
	m2 := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if len(args) > 0 && args[0] == "route" {
				return exec.CommandContext(ctx, "sh", "-c", `echo route boom >&2; exit 1`)
			}
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
		},
	}
	if _, err := m2.Attach(context.Background(), AttachOptions{
		SpaceID: "space", SpacePath: t.TempDir(), UseID: "shared-den",
	}); err == nil || !strings.Contains(err.Error(), "reverse route") {
		t.Fatalf("expected reverse route error, got %v", err)
	}
}

func TestAttachUseIDDryRun(t *testing.T) {
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not lookPath")
			return "", nil
		},
	}
	var buf strings.Builder
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "s",
		SpacePath: "/tmp/s",
		UseID:     "existing",
		DryRun:    true,
		Out:       &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Owned || res.StoreID != "existing" {
		t.Fatalf("res = %#v", res)
	}
	if !strings.Contains(buf.String(), "den status") {
		t.Fatalf("buf = %s", buf.String())
	}
	if !strings.Contains(buf.String(), "route add") || !strings.Contains(buf.String(), "--project") {
		t.Fatalf("dry-run must plan reverse-route registration: %s", buf.String())
	}
}

func TestAttachLookPathFail(t *testing.T) {
	m := &Marmot{
		Binary: "missing",
		LookPath: func(string) (string, error) {
			return "", errors.New("gone")
		},
	}
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestAttachCreateRefusalError(t *testing.T) {
	refusal := `{"schema":1,"error":{"code":"refused","message":"nope","hint":"try later"}}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: refusal, Code: 2},
	})
	m := NewMarmot(bin)
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil {
		t.Fatal("expected RefusalError")
	}
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("got %T %v", err, err)
	}
	if re.Code != "refused" || re.Message != "nope" {
		t.Fatalf("re = %#v", re)
	}
	if !strings.Contains(re.Error(), "refused") || !strings.Contains(re.Error(), "try later") {
		t.Fatalf("Error() = %q", re.Error())
	}
}

func TestAttachSuccessSchemaTooNew(t *testing.T) {
	// exit 0 with high schema → UnsupportedError
	body := `{"schema":99,"den_id":"x"}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: body, Code: 0},
	})
	m := NewMarmot(bin)
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil {
		t.Fatal("expected UnsupportedError")
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestAttachSchemaMissingAndTooOld(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: `{"den_id":"x"}`, Code: 0},
	})
	_, err := NewMarmot(bin).Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "missing schema") {
		t.Fatalf("got %v", err)
	}
	// schema present but lower than supported (and not 0) — if SupportedJSONSchema is 1,
	// use a negative or future-proof path: only > is Unsupported; < non-zero returns unsupported schema msg.
	// With SupportedJSONSchema==1, only 0 and >1 are special; invent schema via direct runJSON.
	m := &Marmot{
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":-1,"den_id":"x"}'`)
		},
		LookPath: func(string) (string, error) { return "sh", nil },
	}
	_, err = m.runJSON(context.Background(), "sh", []string{"x"}, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatalf("got %v", err)
	}
}

func TestWriteSpaceMCPConfigMARMOT_HOME(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("MARMOT_HOME", home)
	if err := WriteSpaceMCPConfig(dir, "marmot", "den-1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "MARMOT_HOME") {
		t.Fatalf("mcp missing MARMOT_HOME env: %s", raw)
	}
	codex, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codex), "MARMOT_HOME") {
		t.Fatalf("codex missing MARMOT_HOME: %s", codex)
	}
	// never vault
	if _, err := os.Stat(filepath.Join(dir, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault must not exist")
	}
}

func TestAttachSuccessErrorFieldInEnvelope(t *testing.T) {
	body := `{"schema":1,"error":{"code":"soft","message":"bad","hint":""}}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: body, Code: 0},
	})
	m := NewMarmot(bin)
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("got %T %v", err, err)
	}
	// Error() without hint, with code
	if re.Error() != "soft: bad" {
		t.Fatalf("Error() = %q", re.Error())
	}
	// without code
	re2 := &RefusalError{Message: "only"}
	if re2.Error() != "only" {
		t.Fatalf("Error() = %q", re2.Error())
	}
}

func TestAttachInvalidJSON(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: "not-json", Code: 0},
	})
	m := NewMarmot(bin)
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "decode marmot json") {
		t.Fatalf("got %v", err)
	}
}

func TestAttachNonJSONFailure(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stderr: "boom", Code: 1},
	})
	m := NewMarmot(bin)
	_, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestStatusAndSyncAndPropose(t *testing.T) {
	syncEnv := `{"schema":1,"warrens":[{"id":"w","fetched":true,"previous_commit":"0123456789ab","pinned_commit":"89abcdef0123","updated":true}],"warnings":[]}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stdout: okEnv("sid"), Code: 0},
		// New-binary warren usage (probe: contains "sync"), exit 1 like marmot's.
		"warren --help":  {Stderr: "usage: marmot warren <init|add|sync|propose> [flags]", Code: 1},
		"warren sync":    {Stdout: syncEnv, Code: 0},
		"den contribute": {Stdout: `{"schema":1,"ok":true}`, Code: 0},
		"warren propose": {Stdout: `{"schema":1,"ok":true}`, Code: 0},
	})
	m := NewMarmot(bin)
	st, err := m.Status(context.Background(), StatusOptions{StoreID: "sid"})
	if err != nil || st.StoreID != "sid" || st.RawJSON == "" {
		t.Fatalf("status = %#v err=%v", st, err)
	}
	var out strings.Builder
	sy, err := m.Sync(context.Background(), SyncOptions{StoreID: "sid", Out: &out})
	if err != nil || sy.Summary != "synced 1 warren(s)" || len(sy.Warrens) != 1 {
		t.Fatalf("sync = %#v err=%v", sy, err)
	}
	if !strings.Contains(out.String(), "synced w (updated 0123456 → 89abcde)") {
		t.Fatalf("sync rendering: %s", out.String())
	}
	pr, err := m.Propose(context.Background(), ProposeOptions{StoreID: "sid"})
	if err != nil || !strings.Contains(pr.Summary, "proposed") {
		t.Fatalf("propose = %#v err=%v", pr, err)
	}
}

func TestProposeParsesBothEnvelopes(t *testing.T) {
	// G4: contribute carries branch/commit/counts/warnings; propose carries
	// push_command / nothing_to_propose / warnings. All must survive into
	// ProposeResult — the user is told what was contributed and what to push.
	contribute := `{"schema":1,"den_id":"t1","branch":"marmot/edit/t1/proj","commit":"abc123","committed":true,` +
		`"contributed":{"added":2,"updated":1,"superseded":0,"noop":3},"warnings":["contrib warn"]}`
	propose := `{"schema":1,"branch":"marmot/propose/x","commit":"def456","committed":true,"nothing_to_propose":false,` +
		`"push_command":"git -C /w push -u origin marmot/edit/t1/proj","warnings":["prop warn"]}`
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den contribute": {Stdout: contribute, Code: 0},
		"warren propose": {Stdout: propose, Code: 0},
	})
	pr, err := NewMarmot(bin).Propose(context.Background(), ProposeOptions{StoreID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Branch != "marmot/edit/t1/proj" || pr.Commit != "abc123" || !pr.Committed {
		t.Fatalf("branch/commit = %#v", pr)
	}
	if pr.Contributed == nil || pr.Contributed.Added != 2 || pr.Contributed.Updated != 1 || pr.Contributed.Noop != 3 {
		t.Fatalf("contributed = %#v", pr.Contributed)
	}
	if pr.PushCommand != "git -C /w push -u origin marmot/edit/t1/proj" {
		t.Fatalf("push command = %q", pr.PushCommand)
	}
	if len(pr.Warnings) != 2 {
		t.Fatalf("warnings = %#v", pr.Warnings)
	}
	if pr.RawJSON == "" || pr.ProposeRawJSON == "" {
		t.Fatalf("raw envelopes must be kept: %#v", pr)
	}

	// nothing_to_propose without a push command → "nothing new to push".
	proposeNoop := `{"schema":1,"nothing_to_propose":true,"warnings":[]}`
	bin2 := writeFakeMarmot(t, map[string]fakeResp{
		"den contribute": {Stdout: `{"schema":1}`, Code: 0},
		"warren propose": {Stdout: proposeNoop, Code: 0},
	})
	pr2, err := NewMarmot(bin2).Propose(context.Background(), ProposeOptions{StoreID: "t1"})
	if err != nil || !pr2.NothingToPropose || pr2.PushCommand != "" {
		t.Fatalf("%#v %v", pr2, err)
	}
	var buf strings.Builder
	PrintProposeOutcome(&buf, pr2)
	if !strings.Contains(buf.String(), "nothing new to push") {
		t.Fatalf("outcome = %s", buf.String())
	}
	buf.Reset()
	PrintProposeOutcome(&buf, pr)
	for _, need := range []string{"contributed: 2 added, 1 updated, 0 superseded, 3 noop", "warning: contrib warn", "warning: prop warn", "push with: git -C /w push"} {
		if !strings.Contains(buf.String(), need) {
			t.Fatalf("outcome missing %q:\n%s", need, buf.String())
		}
	}
}

func TestStatusLookPathFail(t *testing.T) {
	m := &Marmot{LookPath: func(string) (string, error) { return "", errors.New("x") }}
	if _, err := m.Status(context.Background(), StatusOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestStatusRunRawFail(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stderr: "missing den", Code: 1},
	})
	if _, err := NewMarmot(bin).Status(context.Background(), StatusOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestAttachWriteMCPConfigFail(t *testing.T) {
	// SpacePath is a file → WriteSpaceMCPConfig fails after successful den create.
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: okEnv("s"), Code: 0},
		"den status": {Stdout: okEnv("exist"), Code: 0},
	})
	m := NewMarmot(bin)
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: filePath}); err == nil {
		t.Fatal("expected MCP write failure on create path")
	}
	if _, err := m.Attach(context.Background(), AttachOptions{SpaceID: "s", SpacePath: filePath, UseID: "exist"}); err == nil {
		t.Fatal("expected MCP write failure on use-id path")
	}
}

func TestAttachUseIDStatusFail(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den status": {Stderr: "nope", Code: 1},
	})
	if _, err := NewMarmot(bin).Attach(context.Background(), AttachOptions{
		SpaceID: "s", SpacePath: t.TempDir(), UseID: "missing",
	}); err == nil {
		t.Fatal("expected status failure")
	}
}

func TestSyncDryRunAndFallback(t *testing.T) {
	m := NewMarmot("marmot")
	var buf strings.Builder
	sy, err := m.Sync(context.Background(), SyncOptions{StoreID: "s", DryRun: true, Out: &buf})
	if err != nil || len(sy.DryRunCommands) != 1 {
		t.Fatalf("%#v %v", sy, err)
	}
	if !strings.Contains(buf.String(), "warren sync") {
		t.Fatalf("buf=%s", buf.String())
	}

	// LookPath fail
	m2 := &Marmot{LookPath: func(string) (string, error) { return "", errors.New("x") }}
	if _, err := m2.Sync(context.Background(), SyncOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}

	// warren sync fails → den status fallback succeeds
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"warren sync": {Stderr: "no warren", Code: 1},
		"den status":  {Stdout: okEnv("s"), Code: 0},
	})
	m3 := NewMarmot(bin)
	sy, err = m3.Sync(context.Background(), SyncOptions{StoreID: "s"})
	if err != nil || len(sy.Warnings) == 0 {
		t.Fatalf("%#v %v", sy, err)
	}

	// both fail
	bin2 := writeFakeMarmot(t, map[string]fakeResp{
		"warren sync": {Stderr: "no warren", Code: 1},
		"den status":  {Stderr: "no den", Code: 1},
	})
	m4 := NewMarmot(bin2)
	if _, err := m4.Sync(context.Background(), SyncOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}
}

func TestProposeDryRunAndErrors(t *testing.T) {
	m := NewMarmot("marmot")
	var buf strings.Builder
	pr, err := m.Propose(context.Background(), ProposeOptions{StoreID: "s", DryRun: true, Out: &buf})
	if err != nil || len(pr.DryRunCommands) != 2 {
		t.Fatalf("%#v %v", pr, err)
	}
	if !strings.Contains(buf.String(), "den contribute") || !strings.Contains(buf.String(), "warren propose") {
		t.Fatalf("buf=%s", buf.String())
	}

	m2 := &Marmot{LookPath: func(string) (string, error) { return "", errors.New("x") }}
	if _, err := m2.Propose(context.Background(), ProposeOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}

	// contribute fails
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den contribute": {Stderr: "nope", Code: 1},
	})
	if _, err := NewMarmot(bin).Propose(context.Background(), ProposeOptions{StoreID: "s"}); err == nil {
		t.Fatal("expected err")
	}

	// contribute ok, propose fails
	bin2 := writeFakeMarmot(t, map[string]fakeResp{
		"den contribute": {Stdout: `{"schema":1}`, Code: 0},
		"warren propose": {Stderr: "refuse", Code: 1},
	})
	pr, err = NewMarmot(bin2).Propose(context.Background(), ProposeOptions{StoreID: "s"})
	if err == nil || len(pr.Warnings) == 0 {
		t.Fatalf("%#v %v", pr, err)
	}
}

func TestDetachDestroyAndContribute(t *testing.T) {
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den destroy":    {Stdout: `{"schema":1,"destroyed":true}`, Code: 0},
		"den contribute": {Stdout: `{"schema":1}`, Code: 0},
		"warren propose": {Stdout: `{"schema":1}`, Code: 0},
	})
	m := NewMarmot(bin)

	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "d1", Fate: FateDestroy, Owned: true, Force: true,
	})
	if err != nil || !res.Destroyed || res.Kept {
		t.Fatalf("%#v %v", res, err)
	}

	res, err = m.Detach(context.Background(), DetachOptions{
		StoreID: "d2", Fate: FateContribute, Owned: true, Force: true,
	})
	if err != nil || !res.Destroyed {
		t.Fatalf("%#v %v", res, err)
	}

	// dry-run destroy without force
	var buf strings.Builder
	res, err = m.Detach(context.Background(), DetachOptions{
		StoreID: "d3", Fate: FateDestroy, Owned: true, DryRun: true, Out: &buf,
	})
	if err != nil || len(res.DryRunCommands) != 1 {
		t.Fatalf("%#v %v", res, err)
	}
	if !strings.Contains(res.DryRunCommands[0], "den destroy") || strings.Contains(res.DryRunCommands[0], "--force") {
		t.Fatalf("line = %q", res.DryRunCommands[0])
	}

	// dry-run contribute with force injects --force on destroy
	res, err = m.Detach(context.Background(), DetachOptions{
		StoreID: "d4", Fate: FateContribute, Owned: true, Force: true, DryRun: true,
	})
	if err != nil || len(res.DryRunCommands) != 3 {
		t.Fatalf("%#v %v", res, err)
	}
	last := res.DryRunCommands[2]
	if !strings.Contains(last, "--force") {
		t.Fatalf("destroy line = %q", last)
	}
}

func TestDetachOwnedFalseNeverDestroy(t *testing.T) {
	// owned:false never destroy — even with FateDestroy / FateContribute.
	// Dry-run: no binary. Expect keep path, no destroy command.
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { t.Fatal("no look"); return "", nil },
	}
	for _, fate := range []MemoryFate{FateDestroy, FateContribute} {
		res, err := m.Detach(context.Background(), DetachOptions{
			StoreID: "shared", Fate: fate, Owned: false, DryRun: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Dry-run keep path may or may not set Kept; the hard rule is no destroy cmds.
		for _, line := range res.DryRunCommands {
			if strings.Contains(line, "destroy") {
				t.Fatalf("owned:false must not destroy (fate=%s): %q", fate, line)
			}
		}
		if len(res.Warnings) == 0 {
			t.Fatalf("expected warning for fate=%s", fate)
		}
	}
}

func TestDetachKeepDefaultAndLookPathFail(t *testing.T) {
	// empty fate → keep; no NewSpacePath/RemoveRoute → no cmds, prints summary
	bin := writeFakeMarmot(t, map[string]fakeResp{})
	m := NewMarmot(bin)
	var buf strings.Builder
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "k", Owned: true, Out: &buf,
	})
	if err != nil || !res.Kept || res.Destroyed {
		t.Fatalf("%#v %v", res, err)
	}
	if !strings.Contains(buf.String(), "kept") {
		t.Fatalf("buf=%s", buf.String())
	}

	// keep with route rewrite non-dry-run
	bin2 := writeFakeMarmot(t, map[string]fakeResp{
		"route set-project": {Stdout: `{"schema":1}`, Code: 0},
	})
	m2 := NewMarmot(bin2)
	res, err = m2.Detach(context.Background(), DetachOptions{
		StoreID: "k", SpacePath: "/old", NewSpacePath: "/new", Fate: FateKeep, Owned: true,
	})
	if err != nil || !res.Kept {
		t.Fatalf("%#v %v", res, err)
	}

	// lookPath fail on non-dry-run with cmds
	m3 := &Marmot{
		Binary:   "x",
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	}
	_, err = m3.Detach(context.Background(), DetachOptions{
		StoreID: "k", SpacePath: "/p", RemoveRoute: true, Fate: FateKeep, Owned: true,
	})
	if err == nil {
		t.Fatal("expected err")
	}

	// Route rm failure (e.g. missing route) is TOLERATED: warning, not error,
	// and the detach still reports kept (G1/G2 — den state is untouched).
	bin3 := writeFakeMarmot(t, map[string]fakeResp{
		"route rm": {Stderr: "project not found", Code: 1},
	})
	var warnBuf strings.Builder
	res, err = NewMarmot(bin3).Detach(context.Background(), DetachOptions{
		StoreID: "k", SpacePath: "/p", RemoveRoute: true, Fate: FateKeep, Owned: true, Out: &warnBuf,
	})
	if err != nil || !res.Kept {
		t.Fatalf("route rm failure must warn, not abort: %#v %v", res, err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(warnBuf.String(), "warning") {
		t.Fatalf("expected route warning: %#v out=%s", res.Warnings, warnBuf.String())
	}
}

func TestWriteSpaceMCPConfigErrorsAndCodexIdempotent(t *testing.T) {
	if err := WriteSpaceMCPConfig("", "marmot", "id"); err == nil {
		t.Fatal("expected space path required")
	}
	dir := t.TempDir()
	// Pre-seed codex with section; reattach should replace, not duplicate.
	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pre := "x=1\n[mcp_servers.context-marmot]\nenabled = true\ncommand = \"old\"\n"
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSpaceMCPConfig(dir, "marmot", "id"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if strings.Count(string(raw), "[mcp_servers.context-marmot]") != 1 {
		t.Fatalf("codex not idempotent: %s", raw)
	}
	if !strings.Contains(string(raw), `"serve"`) && !strings.Contains(string(raw), "serve") {
		t.Fatalf("codex should rewrite command/args: %s", raw)
	}
	// Append path when existing file has no trailing newline and no section
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, ".codex", "config.toml"), []byte("foo=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSpaceMCPConfig(dir2, "marmot", "id"); err != nil {
		t.Fatal(err)
	}
}

func TestShellQuoteAndHelpers(t *testing.T) {
	if shellQuote("") != "''" {
		t.Fatal(shellQuote(""))
	}
	if shellQuote("plain") != "plain" {
		t.Fatal(shellQuote("plain"))
	}
	q := shellQuote("a b'c")
	if !strings.HasPrefix(q, "'") || !strings.Contains(q, `'\''`) {
		t.Fatalf("q=%q", q)
	}
	if firstLine("", "fb") != "fb" {
		t.Fatal(firstLine("", "fb"))
	}
	if firstLine("one\ntwo", "fb") != "one" {
		t.Fatal(firstLine("one\ntwo", "fb"))
	}
	if firstLine("  only  ", "fb") != "only" {
		t.Fatal(firstLine("  only  ", "fb"))
	}
	if firstNonEmpty("", "  ", "x") != "x" {
		t.Fatal(firstNonEmpty("", "  ", "x"))
	}
	if firstNonEmpty() != "" {
		t.Fatal("empty")
	}
}

func TestRunRawEmptyMessages(t *testing.T) {
	// Command that exits 1 with empty stdout/stderr → err.Error() used
	m := &Marmot{
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "false")
		},
	}
	_, err := m.runRaw(context.Background(), "false", []string{"x"}, "")
	if err == nil {
		t.Fatal("expected err")
	}
	// stdout has body but non-json parse failure path for runJSON already covered;
	// runJSON with exit fail and invalid json falls through to err
	m2 := &Marmot{
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "echo notjson; exit 1")
		},
	}
	_, err = m2.runJSON(context.Background(), "sh", []string{"x"}, "")
	if err == nil {
		t.Fatal("expected err")
	}
}

func TestDefaultLookPathAndCommand(t *testing.T) {
	// Cover default lookPath/command branches via a real PATH binary (true/false).
	m := &Marmot{Binary: "true"}
	// lookPath default
	path, err := m.lookPath("true")
	if err != nil || path == "" {
		t.Fatalf("lookPath true: %v %q", err, path)
	}
	cmd := m.command(context.Background(), path)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestParseMemorySpecMore(t *testing.T) {
	if _, err := ParseMemorySpec("", "marmot"); err == nil {
		t.Fatal("empty")
	}
	// empty provider after cut
	if _, err := ParseMemorySpec(":x", "marmot"); err == nil {
		t.Fatal("empty provider")
	}
	// default provider empty → marmot
	got, err := ParseMemorySpec(".", "")
	if err != nil || got.Provider != "marmot" {
		t.Fatalf("%#v %v", got, err)
	}
	// invalid provider name
	if _, err := ParseMemorySpec("../bad:.", "marmot"); err == nil {
		t.Fatal("bad provider")
	}
	// provider: with empty/dot fresh
	got, err = ParseMemorySpec("marmot:", "x")
	if err != nil || !got.Fresh {
		t.Fatalf("%#v %v", got, err)
	}
	// invalid id after provider
	if _, err := ParseMemorySpec("marmot:../bad", "marmot"); err == nil {
		t.Fatal("bad id")
	}
}

func TestParseMemoryFateAll(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want MemoryFate
	}{
		{"keep", FateKeep},
		{"DESTROY", FateDestroy},
		{" Contribute ", FateContribute},
	} {
		got, err := ParseMemoryFate(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("%q → %q %v", tc.in, got, err)
		}
	}
}

func TestRegistry(t *testing.T) {
	// Names includes marmot
	names := Names()
	found := false
	for _, n := range names {
		if n == "marmot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("names = %v", names)
	}

	p, err := Lookup("marmot", config.MemoryConfig{Binary: "mybin"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "marmot" {
		t.Fatalf("name = %s", p.Name())
	}
	mm, ok := p.(*Marmot)
	if !ok || mm.Binary != "mybin" {
		t.Fatalf("%#v", p)
	}

	// empty name uses config provider / default
	p, err = Lookup("", config.MemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "marmot" {
		t.Fatal(p.Name())
	}

	if _, err := Lookup("nope", config.MemoryConfig{}); err == nil {
		t.Fatal("expected unknown")
	}

	// Register a test factory
	Register("testprov", func(mc config.MemoryConfig) Provider {
		return &Fake{}
	})
	t.Cleanup(func() {
		// leave testprov registered is fine for process; re-register nil not needed
	})
	p, err = Lookup("testprov", config.MemoryConfig{})
	if err != nil || p.Name() != "fake" {
		t.Fatalf("%v %v", p, err)
	}

	cfg := config.Config{}
	p, err = FromConfig(cfg)
	if err != nil || p.Name() != "marmot" {
		t.Fatalf("%v %v", p, err)
	}
}

func TestFakeProviderAllMethods(t *testing.T) {
	f := &Fake{}
	if f.Name() != "fake" {
		t.Fatal(f.Name())
	}
	ctx := context.Background()
	pr, err := f.Probe(ctx)
	if err != nil || !pr.Capable {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ar, err := f.Attach(ctx, AttachOptions{SpaceID: "s", SpacePath: dir})
	if err != nil || !ar.Owned || ar.StoreID != "s" {
		t.Fatalf("%#v %v", ar, err)
	}
	// dry-run attach
	var buf strings.Builder
	ar, err = f.Attach(ctx, AttachOptions{SpaceID: "s", SpacePath: dir, DryRun: true, Out: &buf})
	if err != nil || len(ar.DryRunCommands) != 1 {
		t.Fatalf("%#v %v", ar, err)
	}
	// use existing
	ar, err = f.Attach(ctx, AttachOptions{SpaceID: "s", UseID: "exist", SpacePath: dir})
	if err != nil || ar.Owned || ar.StoreID != "exist" {
		t.Fatalf("%#v %v", ar, err)
	}
	// store id override
	ar, err = f.Attach(ctx, AttachOptions{StoreID: "sid", Name: "n"})
	if err != nil || ar.StoreID != "sid" || ar.Name != "n" {
		t.Fatalf("%#v %v", ar, err)
	}

	st, err := f.Status(ctx, StatusOptions{StoreID: "s"})
	if err != nil || st.Summary == "" {
		t.Fatal(err)
	}
	sy, err := f.Sync(ctx, SyncOptions{StoreID: "s"})
	if err != nil || sy.Summary == "" {
		t.Fatal(err)
	}
	po, err := f.Propose(ctx, ProposeOptions{StoreID: "s"})
	if err != nil || po.Summary == "" {
		t.Fatal(err)
	}
	de, err := f.Detach(ctx, DetachOptions{StoreID: "s", Fate: FateDestroy, Owned: true})
	if err != nil || !de.Destroyed {
		t.Fatalf("%#v %v", de, err)
	}
	de, err = f.Detach(ctx, DetachOptions{StoreID: "s", Fate: FateDestroy, Owned: false})
	if err != nil || !de.Kept {
		t.Fatalf("unowned destroy should keep: %#v", de)
	}

	// custom fns
	f2 := &Fake{
		ProbeFn:   func(context.Context) (ProbeResult, error) { return ProbeResult{Message: "p"}, nil },
		AttachFn:  func(context.Context, AttachOptions) (AttachResult, error) { return AttachResult{StoreID: "a"}, nil },
		StatusFn:  func(context.Context, StatusOptions) (StatusResult, error) { return StatusResult{Summary: "st"}, nil },
		SyncFn:    func(context.Context, SyncOptions) (SyncResult, error) { return SyncResult{Summary: "sy"}, nil },
		ProposeFn: func(context.Context, ProposeOptions) (ProposeResult, error) { return ProposeResult{Summary: "pr"}, nil },
		DetachFn:  func(context.Context, DetachOptions) (DetachResult, error) { return DetachResult{Summary: "de"}, nil },
	}
	if r, _ := f2.Probe(ctx); r.Message != "p" {
		t.Fatal(r)
	}
	if r, _ := f2.Attach(ctx, AttachOptions{}); r.StoreID != "a" {
		t.Fatal(r)
	}
	if r, _ := f2.Status(ctx, StatusOptions{}); r.Summary != "st" {
		t.Fatal(r)
	}
	if r, _ := f2.Sync(ctx, SyncOptions{}); r.Summary != "sy" {
		t.Fatal(r)
	}
	if r, _ := f2.Propose(ctx, ProposeOptions{}); r.Summary != "pr" {
		t.Fatal(r)
	}
	if r, _ := f2.Detach(ctx, DetachOptions{}); r.Summary != "de" {
		t.Fatal(r)
	}
	if len(f2.Calls) < 6 {
		t.Fatalf("calls = %#v", f2.Calls)
	}
}

func TestUnavailableErrorFormatting(t *testing.T) {
	e := &UnavailableError{Provider: "marmot"}
	if !strings.Contains(e.Error(), "unavailable") {
		t.Fatal(e.Error())
	}
	e2 := &UnavailableError{Provider: "marmot", Err: errors.New("e"), Hint: "h"}
	if !strings.Contains(e2.Error(), "e") || !strings.Contains(e2.Error(), "h") {
		t.Fatal(e2.Error())
	}
}

func TestContractErrorFixtureIsRefusalShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "contracts", "error.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Code == "" {
		t.Fatalf("%#v", env)
	}
	// Simulate runJSON success-path error field
	re := &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
	if !strings.Contains(re.Error(), "den_not_found") {
		t.Fatal(re.Error())
	}
}

func TestFormatCommandQuotes(t *testing.T) {
	line := FormatCommand("marmot", []string{"den", "create", "a b", "--json"})
	if !strings.Contains(line, "'a b'") {
		t.Fatalf("line=%s", line)
	}
}

func TestPrintfNil(t *testing.T) {
	printf(nil, "hi %s", "x") // no panic
	var buf bytes.Buffer
	printf(&buf, "hi %s", "x")
	if buf.String() != "hi x" {
		t.Fatal(buf.String())
	}
}

func TestRemoveSpaceMCPConfigPreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	seedMultiServerMCPConfigs(t, dir)

	if err := RemoveSpaceMCPConfig(dir); err != nil {
		t.Fatal(err)
	}

	assertMarmotEntriesStripped(t, dir)
}

func TestDetachKeepSpaceWiringRepointsRouteAndMCP(t *testing.T) {
	// One-of-many detach (fate keep): route rm must NOT run, the reverse
	// route is re-pointed at a surviving den via route add, and the MCP
	// configs survive rewritten to target the survivor.
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot", "den-a"); err != nil {
		t.Fatal(err)
	}
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"route add": {Stdout: `{"schema":1}`, Code: 0},
	})
	m := NewMarmot(bin)
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID:             "den-a",
		SpacePath:           dir,
		Fate:                FateKeep,
		Owned:               true,
		KeepSpaceWiring:     true,
		RepointRouteStoreID: "den-b",
	})
	if err != nil || !res.Kept {
		t.Fatalf("%#v %v", res, err)
	}
	sawAdd := false
	for _, c := range res.DryRunCommands {
		if strings.Contains(c, "route rm") {
			t.Fatalf("route rm must not run while siblings remain: %#v", res.DryRunCommands)
		}
		if strings.Contains(c, "route add") && strings.Contains(c, "den-b") {
			sawAdd = true
		}
	}
	if !sawAdd {
		t.Fatalf("expected route add re-point at den-b: %#v", res.DryRunCommands)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("MCP config must survive a non-last detach: %v", err)
	}
	if !strings.Contains(string(raw), "context-marmot") || !strings.Contains(string(raw), "den-b") {
		t.Fatalf(".mcp.json must target surviving den-b: %s", raw)
	}
	if strings.Contains(string(raw), "den-a") {
		t.Fatalf(".mcp.json still targets detached den-a: %s", raw)
	}
}

func TestDetachDestroyOneOfManyRepointsRoute(t *testing.T) {
	// Destroying one-of-many must still re-point route + MCP at a survivor
	// (the destroyed den would otherwise leave both dangling).
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot", "den-a"); err != nil {
		t.Fatal(err)
	}
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den destroy": {Stdout: `{"schema":1,"destroyed":true}`, Code: 0},
		"route add":   {Stdout: `{"schema":1}`, Code: 0},
	})
	m := NewMarmot(bin)
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID:             "den-a",
		SpacePath:           dir,
		Fate:                FateDestroy,
		Owned:               true,
		KeepSpaceWiring:     true,
		RepointRouteStoreID: "den-b",
	})
	if err != nil || !res.Destroyed {
		t.Fatalf("%#v %v", res, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("MCP config must survive: %v", err)
	}
	if !strings.Contains(string(raw), "den-b") || strings.Contains(string(raw), "den-a") {
		t.Fatalf(".mcp.json must target surviving den-b: %s", raw)
	}
}

func TestDetachKeepSpaceWiringDryRun(t *testing.T) {
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not look up binary")
			return "", nil
		},
	}
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID:             "den-a",
		SpacePath:           "/work/agent-work/multi",
		Fate:                FateKeep,
		Owned:               true,
		DryRun:              true,
		KeepSpaceWiring:     true,
		RepointRouteStoreID: "den-b",
	})
	if err != nil || !res.Kept {
		t.Fatalf("%#v %v", res, err)
	}
	if len(res.DryRunCommands) != 2 {
		t.Fatalf("DryRunCommands = %#v", res.DryRunCommands)
	}
	if !strings.Contains(res.DryRunCommands[0], "route add") || !strings.Contains(res.DryRunCommands[0], "den-b") {
		t.Fatalf("expected route add re-point: %#v", res.DryRunCommands)
	}
	if !strings.Contains(res.DryRunCommands[1], "re-point") || !strings.Contains(res.DryRunCommands[1], "den-b") {
		t.Fatalf("expected MCP re-point line (never removal): %#v", res.DryRunCommands)
	}
}

func TestDetachRemovesMCPConfig(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot", "d-gone"); err != nil {
		t.Fatal(err)
	}
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"route rm": {Stdout: `{"schema":1}`, Code: 0},
	})
	m := NewMarmot(bin)
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "d-gone", SpacePath: dir, RemoveRoute: true, Fate: FateKeep, Owned: true,
	})
	if err != nil || !res.Kept {
		t.Fatalf("%#v %v", res, err)
	}
	// All four configs should no longer reference the den
	for _, rel := range []string{".mcp.json", filepath.Join(".cursor", "mcp.json"), filepath.Join(".vscode", "mcp.json"), filepath.Join(".codex", "config.toml")} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			raw, _ := os.ReadFile(p)
			if strings.Contains(string(raw), "d-gone") || strings.Contains(string(raw), "context-marmot") {
				t.Fatalf("%s still has marmot binding: %s", rel, raw)
			}
		}
	}
}
