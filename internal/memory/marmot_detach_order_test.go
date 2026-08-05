package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sourceInUseEnv is marmot's structured refusal when a `serve --den` (or
// another lifecycle holder) keeps the den's source lease alive; --force does
// not override it and the den survives intact.
const sourceInUseEnv = `{"schema":1,"error":{"code":"source_in_use","message":"cannot freeze source den for destruction","hint":"stop processes serving the den and retry"}}`

func TestDetachDestroyStripsMCPBeforeDestroy(t *testing.T) {
	// Last-attachment destroy: the space MCP configs must ALREADY be stripped
	// when `den destroy` is issued — a client started from a stale config
	// could otherwise re-acquire the den mid-teardown.
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot", "den-a"); err != nil {
		t.Fatal(err)
	}
	strippedAtDestroy := false
	sawDestroy := false
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if strings.Contains(strings.Join(args, " "), "den destroy") {
				sawDestroy = true
				_, mcpErr := os.Stat(filepath.Join(dir, ".mcp.json"))
				_, codexErr := os.Stat(filepath.Join(dir, ".codex", "config.toml"))
				strippedAtDestroy = os.IsNotExist(mcpErr) && os.IsNotExist(codexErr)
				return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1,"destroyed":true}'`)
			}
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
		},
	}
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true,
	})
	if err != nil || !res.Destroyed {
		t.Fatalf("%#v %v", res, err)
	}
	if !sawDestroy {
		t.Fatal("den destroy was never issued")
	}
	if !strippedAtDestroy {
		t.Fatal("MCP configs must be stripped BEFORE den destroy runs")
	}
}

func TestDetachDestroyRefusalRestoreScope(t *testing.T) {
	// Restore-on-refusal is SCOPED: only structured refusals known to leave
	// the den intact re-point the wiring back. den_not_found means there is no
	// den to point at; ambiguous failures could rewire clients at a destroyed
	// den (marmot destroys before serializing success).
	cases := []struct {
		name    string
		code    string
		force   bool
		restore bool
	}{
		{name: "source_in_use live serve", code: "source_in_use", force: false, restore: true},
		{name: "source_in_use with --force", code: "source_in_use", force: true, restore: true},
		{name: "unpushed_edits multi-link den", code: "unpushed_edits", force: false, restore: true},
		{name: "unpushed_unknown degraded git", code: "unpushed_unknown", force: false, restore: true},
		{name: "den_not_found never restores", code: "den_not_found", force: false, restore: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			refusal := fmt.Sprintf(`{"schema":1,"error":{"code":%q,"message":"refused","hint":"h"}}`, tc.code)
			bin := writeFakeMarmot(t, map[string]fakeResp{
				"den destroy": {Stdout: refusal, Code: 2},
			})
			// Seed wiring with the same binary the provider resolves so a
			// restore is functionally equivalent (same binary + den entries).
			if err := WriteSpaceMCPConfig(dir, bin, "den-a"); err != nil {
				t.Fatal(err)
			}
			_, err := NewMarmot(bin).Detach(context.Background(), DetachOptions{
				StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true, Force: tc.force,
			})
			var re *RefusalError
			if !errors.As(err, &re) || re.Code != tc.code {
				t.Fatalf("want RefusalError %s, got %T %v", tc.code, err, err)
			}
			raw, rerr := os.ReadFile(filepath.Join(dir, ".mcp.json"))
			if !tc.restore {
				if !os.IsNotExist(rerr) {
					t.Fatalf(".mcp.json must stay stripped for %s: err=%v raw=%s", tc.code, rerr, raw)
				}
				return
			}
			if rerr != nil {
				t.Fatalf("wiring must be restored after %s: %v", tc.code, rerr)
			}
			for _, need := range []string{"context-marmot", "den-a", bin} {
				if !strings.Contains(string(raw), need) {
					t.Fatalf("restored .mcp.json missing %q: %s", need, raw)
				}
			}
			codex, rerr := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
			if rerr != nil || !strings.Contains(string(codex), "den-a") {
				t.Fatalf("restored codex config: %v %s", rerr, codex)
			}
		})
	}
}

func TestDetachContributeRefusalWiringScope(t *testing.T) {
	// The contribute stage refuses BEFORE `den destroy` is ever issued, so the
	// error is always wrapped in DestroyNotIssuedError — but den_not_found is
	// the exception that decides the wiring. den_not_found means the den is
	// ALREADY gone (the destroy-retry crash window), and the service tolerates
	// it by splicing the attachment away; the space-local MCP config must
	// converge in the same pass or the space is left wired to a den that no
	// longer exists, with no attachment record left to clean it up. Every
	// other contribute-stage refusal leaves a live den: the wiring stays put
	// (the pre-destroy strip never ran) and DenIntactAfterFailedDestroy
	// authorizes callers to restore any wiring they stripped themselves.
	cases := []struct {
		name      string
		code      string
		stripped  bool
		denIntact bool
	}{
		{name: "den_not_found strips wiring for the vanished den", code: "den_not_found", stripped: true, denIntact: false},
		{name: "edit_link_required keeps wiring for the live den", code: "edit_link_required", stripped: false, denIntact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			refusal := fmt.Sprintf(`{"schema":1,"error":{"code":%q,"message":"refused","hint":"h"}}`, tc.code)
			// `den contribute` is the first command Propose issues, so
			// refusing it fails the contribute/propose stage outright.
			bin := writeFakeMarmot(t, map[string]fakeResp{
				"den contribute": {Stdout: refusal, Code: 2},
			})
			if err := WriteSpaceMCPConfig(dir, bin, "den-a"); err != nil {
				t.Fatal(err)
			}
			_, err := NewMarmot(bin).Detach(context.Background(), DetachOptions{
				StoreID: "den-a", SpacePath: dir, Fate: FateContribute, Owned: true,
			})
			// The wrapper is transparent: the underlying refusal (code and
			// all) still matches through it.
			var re *RefusalError
			if !errors.As(err, &re) || re.Code != tc.code {
				t.Fatalf("want RefusalError %s, got %T %v", tc.code, err, err)
			}
			var notIssued *DestroyNotIssuedError
			if !errors.As(err, &notIssued) {
				t.Fatalf("contribute-stage failure must wrap in DestroyNotIssuedError (no destroy ran): %T %v", err, err)
			}
			if got := IsDenNotFound(err); got != (tc.code == CodeDenNotFound) {
				t.Fatalf("IsDenNotFound = %v for %s", got, tc.code)
			}
			if got := DenIntactAfterFailedDestroy(err); got != tc.denIntact {
				t.Fatalf("DenIntactAfterFailedDestroy = %v, want %v for %s", got, tc.denIntact, tc.code)
			}
			_, mcpErr := os.Stat(filepath.Join(dir, ".mcp.json"))
			_, codexErr := os.Stat(filepath.Join(dir, ".codex", "config.toml"))
			if tc.stripped {
				if !os.IsNotExist(mcpErr) || !os.IsNotExist(codexErr) {
					t.Fatalf("space wiring must be stripped after %s: mcp=%v codex=%v", tc.code, mcpErr, codexErr)
				}
				return
			}
			if mcpErr != nil || codexErr != nil {
				t.Fatalf("space wiring must survive a %s refusal (den still alive): mcp=%v codex=%v", tc.code, mcpErr, codexErr)
			}
			raw, rerr := os.ReadFile(filepath.Join(dir, ".mcp.json"))
			if rerr != nil || !strings.Contains(string(raw), "den-a") {
				t.Fatalf("surviving .mcp.json must still point at den-a: %v %s", rerr, raw)
			}
		})
	}
}

func TestDetachDestroyDecodeErrorNoRestore(t *testing.T) {
	// Garbage stdout with exit 0 is AMBIGUOUS: the destroy may have completed
	// before the envelope was mangled, so restoring could rewire clients at a
	// destroyed den. No restore.
	dir := t.TempDir()
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den destroy": {Stdout: "not-json-at-all", Code: 0},
	})
	if err := WriteSpaceMCPConfig(dir, bin, "den-a"); err != nil {
		t.Fatal(err)
	}
	_, err := NewMarmot(bin).Detach(context.Background(), DetachOptions{
		StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true,
	})
	if err == nil || !strings.Contains(err.Error(), "decode marmot json") {
		t.Fatalf("want decode error, got %v", err)
	}
	if _, rerr := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(rerr) {
		t.Fatalf(".mcp.json must NOT be restored after a decode error: %v", rerr)
	}
}

func TestDetachDestroyLookPathFailRestoresWiring(t *testing.T) {
	// A missing binary after the strip is the one non-refusal restore case:
	// nothing ran, the den is certainly intact. Restore uses m.binary()
	// since lookPath failed.
	dir := t.TempDir()
	if err := WriteSpaceMCPConfig(dir, "marmot-nonexistent-xyz", "den-a"); err != nil {
		t.Fatal(err)
	}
	m := &Marmot{
		Binary:   "marmot-nonexistent-xyz",
		LookPath: func(string) (string, error) { return "", errors.New("gone") },
	}
	_, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true,
	})
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnavailableError, got %T %v", err, err)
	}
	raw, rerr := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if rerr != nil {
		t.Fatalf("wiring must be restored when the binary is missing: %v", rerr)
	}
	if !strings.Contains(string(raw), "den-a") || !strings.Contains(string(raw), "marmot-nonexistent-xyz") {
		t.Fatalf("restored .mcp.json: %s", raw)
	}
}

func TestDetachDestroyStripFailureAbortsDestroy(t *testing.T) {
	// A FAILED strip is a fatal prerequisite: warn-and-proceed would let a
	// client re-acquire the den mid-teardown, so the destroy is never issued.
	dir := t.TempDir()
	malformed := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(malformed, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			calls = append(calls, args)
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
		},
	}
	_, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true, Force: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not issued") {
		t.Fatalf("want fatal wiring error, got %v", err)
	}
	for _, args := range calls {
		if strings.Contains(strings.Join(args, " "), "den destroy") {
			t.Fatalf("destroy must not be issued after a failed strip: %v", calls)
		}
	}
	// The malformed user config is left alone, not overwritten.
	raw, rerr := os.ReadFile(malformed)
	if rerr != nil || string(raw) != "{not json" {
		t.Fatalf("malformed config must survive untouched: %v %s", rerr, raw)
	}
}

func TestDetachDestroyRestoreFailureWarns(t *testing.T) {
	// Strip succeeds, destroy refuses, restore write fails (space dir made
	// read-only mid-flight): the refusal still surfaces, and the warning with
	// the explicit remediation prints eagerly on the refusal path.
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := WriteSpaceMCPConfig(dir, "marmot", "den-a"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if strings.Contains(strings.Join(args, " "), "den destroy") {
				// The wiring is already stripped; make its restore impossible.
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Errorf("chmod: %v", err)
				}
				return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(`printf '%%s' '%s'; exit 2`, sourceInUseEnv))
			}
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"schema":1}'`)
		},
	}
	res, err := m.Detach(context.Background(), DetachOptions{
		StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true, Out: &out,
	})
	var re *RefusalError
	if !errors.As(err, &re) || re.Code != "source_in_use" {
		t.Fatalf("want source_in_use refusal, got %T %v", err, err)
	}
	const remediation = "memory wiring for this space was removed; re-run the destroy, or re-attach to restore it"
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, remediation) {
			found = true
		}
	}
	if !found {
		t.Fatalf("result.Warnings missing restore-failure remediation: %#v", res.Warnings)
	}
	if !strings.Contains(out.String(), "warning: "+remediation) {
		t.Fatalf("warning must print eagerly on the refusal path:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(statErr) {
		t.Fatalf(".mcp.json unexpectedly present after failed restore: %v", statErr)
	}
}

// writeStatefulMarmot installs a marmot fake whose FIRST `den destroy`
// refuses with source_in_use (marker-file trick) and whose second succeeds.
// Every argv line is appended to the returned log path.
func writeStatefulMarmot(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "marmot")
	marker := filepath.Join(dir, "refused-once")
	logPath = filepath.Join(dir, "argv.log")
	script := fmt.Sprintf(`#!/bin/sh
args="$*"
echo "$args" >> %q
case "$args" in *"den destroy"*)
  if [ ! -f %q ]; then
    : > %q
    printf '%%s' '%s'
    exit 2
  fi
  printf '%%s' '{"schema":1,"destroyed":true}'
  exit 0
  ;;
esac
printf '%%s' '{"schema":1}'
exit 0
`, logPath, marker, marker, sourceInUseEnv)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func countLogLines(t *testing.T, logPath, substr string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func TestDetachDestroyRefuseThenSucceedRetry(t *testing.T) {
	// First Detach: destroy refused (source_in_use) → wiring restored and the
	// refusal surfaces. Second Detach retries cleanly: strip, then destroy.
	dir := t.TempDir()
	bin, logPath := writeStatefulMarmot(t)
	if err := WriteSpaceMCPConfig(dir, bin, "den-a"); err != nil {
		t.Fatal(err)
	}
	m := NewMarmot(bin)
	opts := DetachOptions{StoreID: "den-a", SpacePath: dir, Fate: FateDestroy, Owned: true}

	_, err := m.Detach(context.Background(), opts)
	var re *RefusalError
	if !errors.As(err, &re) || re.Code != "source_in_use" {
		t.Fatalf("first detach: want source_in_use refusal, got %T %v", err, err)
	}
	raw, rerr := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if rerr != nil || !strings.Contains(string(raw), "den-a") {
		t.Fatalf("first detach must restore wiring: %v %s", rerr, raw)
	}

	res, err := m.Detach(context.Background(), opts)
	if err != nil || !res.Destroyed {
		t.Fatalf("second detach: %#v %v", res, err)
	}
	if _, rerr := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(rerr) {
		t.Fatalf("second detach must strip wiring for good: %v", rerr)
	}
	if got := countLogLines(t, logPath, "den destroy"); got != 2 {
		t.Fatalf("destroy attempts = %d, want 2", got)
	}
}

func TestDetachContributeRefuseThenSucceedRetry(t *testing.T) {
	// Contribute-fate retry: the refused first attempt restores wiring, and
	// the retry re-runs contribute + propose before the destroy. Every
	// destroy attempt carries --force even with Force:false (unconditional
	// for the contribute fate).
	dir := t.TempDir()
	bin, logPath := writeStatefulMarmot(t)
	if err := WriteSpaceMCPConfig(dir, bin, "den-a"); err != nil {
		t.Fatal(err)
	}
	m := NewMarmot(bin)
	opts := DetachOptions{StoreID: "den-a", SpacePath: dir, Fate: FateContribute, Owned: true, Force: false}

	_, err := m.Detach(context.Background(), opts)
	var re *RefusalError
	if !errors.As(err, &re) || re.Code != "source_in_use" {
		t.Fatalf("first detach: want source_in_use refusal, got %T %v", err, err)
	}
	if _, rerr := os.Stat(filepath.Join(dir, ".mcp.json")); rerr != nil {
		t.Fatalf("first detach must restore wiring: %v", rerr)
	}

	res, err := m.Detach(context.Background(), opts)
	if err != nil || !res.Destroyed {
		t.Fatalf("second detach: %#v %v", res, err)
	}
	if got := countLogLines(t, logPath, "den contribute"); got != 2 {
		t.Fatalf("contribute runs = %d, want 2 (retry must re-contribute)", got)
	}
	if got := countLogLines(t, logPath, "warren propose"); got != 2 {
		t.Fatalf("propose runs = %d, want 2 (retry must re-propose)", got)
	}
	if got := countLogLines(t, logPath, "den destroy"); got != 2 {
		t.Fatalf("destroy attempts = %d, want 2", got)
	}
	if got := countLogLines(t, logPath, "den destroy den-a --force"); got != 2 {
		t.Fatalf("contribute destroys without unconditional --force: %d/2", got)
	}
}
