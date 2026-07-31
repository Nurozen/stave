package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedMultiServerMCPConfigs writes all four harness configs with a
// context-marmot entry alongside unrelated servers/settings so removal tests
// can assert only the marmot entries are stripped.
func seedMultiServerMCPConfigs(t *testing.T, dir string) {
	t.Helper()
	mcp := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{"command": "marmot", "args": []string{"serve", "--den", "gone"}},
			"other":          map[string]any{"command": "other"},
		},
	}
	raw, _ := json.MarshalIndent(mcp, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cursor", "mcp.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	vs := map[string]any{
		"servers": map[string]any{
			"context-marmot": map[string]any{"command": "marmot"},
			"keep-me":        map[string]any{"command": "x"},
		},
	}
	vsRaw, _ := json.MarshalIndent(vs, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vscode", "mcp.json"), append(vsRaw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := "model = \"gpt\"\n\n[mcp_servers.context-marmot]\nenabled = true\ncommand = \"marmot\"\n\n[mcp_servers.context-marmot.env]\nMARMOT_HOME = \"/tmp\"\n\n[mcp_servers.other]\ncommand = \"y\"\n"
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(codex), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertMarmotEntriesStripped checks the seeded fixture after cleanup: every
// context-marmot entry gone, every unrelated server/setting preserved.
func assertMarmotEntriesStripped(t *testing.T, dir string) {
	t.Helper()
	// .mcp.json keeps other
	got, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "context-marmot") {
		t.Fatalf("context-marmot still present: %s", got)
	}
	if !strings.Contains(string(got), "other") {
		t.Fatalf("other server removed: %s", got)
	}
	// vscode
	got, err = os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "context-marmot") || !strings.Contains(string(got), "keep-me") {
		t.Fatalf("vscode cleanup wrong: %s", got)
	}
	// codex
	got, err = os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "context-marmot") {
		t.Fatalf("codex still has context-marmot: %s", got)
	}
	if !strings.Contains(string(got), "[mcp_servers.other]") || !strings.Contains(string(got), "model") {
		t.Fatalf("codex lost unrelated config: %s", got)
	}
}

func TestMarmotWriteMCPConfigWritesAllFourWithDenID(t *testing.T) {
	t.Setenv("MARMOT_HOME", filepath.Join(t.TempDir(), "dens"))
	dir := t.TempDir()
	var p Provider = &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "/opt/marmot/bin/marmot", nil },
	}
	w, ok := p.(MCPWirer)
	if !ok {
		t.Fatal("Marmot must implement the optional MCPWirer seam")
	}
	if err := w.WriteMCPConfig(context.Background(), dir, "den-w1"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".mcp.json", filepath.Join(".cursor", "mcp.json"), filepath.Join(".vscode", "mcp.json"), filepath.Join(".codex", "config.toml")} {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		s := string(raw)
		if !strings.Contains(s, "context-marmot") || !strings.Contains(s, "den-w1") {
			t.Fatalf("%s missing den binding: %s", rel, s)
		}
		if !strings.Contains(s, "/opt/marmot/bin/marmot") {
			t.Fatalf("%s missing resolved binary: %s", rel, s)
		}
		if !strings.Contains(s, "MARMOT_HOME") {
			t.Fatalf("%s missing MARMOT_HOME embed: %s", rel, s)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault must never be created")
	}
}

func TestMarmotRemoveMCPConfigPreservesUnrelatedServers(t *testing.T) {
	dir := t.TempDir()
	seedMultiServerMCPConfigs(t, dir)
	m := NewMarmot("marmot")
	if err := m.RemoveMCPConfig(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	assertMarmotEntriesStripped(t, dir)
}

func TestFakeMCPWirerRecordsAndWrites(t *testing.T) {
	dir := t.TempDir()
	f := &Fake{}
	var w MCPWirer = f
	if err := w.WriteMCPConfig(context.Background(), dir, "fake-den"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil || !strings.Contains(string(raw), "fake-den") {
		t.Fatalf("default write missing den id: %v %s", err, raw)
	}
	if err := w.RemoveMCPConfig(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("emptied .mcp.json should be deleted: %v", err)
	}
	if len(f.Calls) != 2 ||
		!strings.Contains(f.Calls[0], "write-mcp-config") || !strings.Contains(f.Calls[0], dir) || !strings.Contains(f.Calls[0], "fake-den") ||
		!strings.Contains(f.Calls[1], "remove-mcp-config") || !strings.Contains(f.Calls[1], dir) {
		t.Fatalf("calls = %#v", f.Calls)
	}
}

func TestFakeMCPWirerFnOverride(t *testing.T) {
	wantW := errors.New("write boom")
	wantR := errors.New("remove boom")
	f := &Fake{
		WriteMCPConfigFn:  func(context.Context, string, string) error { return wantW },
		RemoveMCPConfigFn: func(context.Context, string) error { return wantR },
	}
	if err := f.WriteMCPConfig(context.Background(), "/sp", "d1"); !errors.Is(err, wantW) {
		t.Fatalf("err = %v", err)
	}
	if err := f.RemoveMCPConfig(context.Background(), "/sp"); !errors.Is(err, wantR) {
		t.Fatalf("err = %v", err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %#v", f.Calls)
	}
}

func TestFakeAttachRecordsLifetime(t *testing.T) {
	f := &Fake{}
	if _, err := f.Attach(context.Background(), AttachOptions{SpaceID: "s1", Lifetime: "durable"}); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || !strings.Contains(f.Calls[0], "lifetime=durable") {
		t.Fatalf("calls = %#v", f.Calls)
	}
}
