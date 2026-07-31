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
		"settings": map[string]any{"preserve": true},
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
		"inputs": []any{map[string]any{"id": "keep-input"}},
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

func TestWriteSpaceMCPConfigReplacesOnlyMarmotEntries(t *testing.T) {
	dir := t.TempDir()
	seedMultiServerMCPConfigs(t, dir)

	if err := WriteSpaceMCPConfig(dir, "/opt/marmot", "den-new"); err != nil {
		t.Fatal(err)
	}
	// Exercise re-add/update as a second write: the generated entry must be
	// replaced while every unrelated server and setting survives both writes.
	if err := WriteSpaceMCPConfig(dir, "/opt/marmot", "den-newer"); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		rel       string
		mapKey    string
		otherName string
	}{
		{rel: ".mcp.json", mapKey: "mcpServers", otherName: "other"},
		{rel: filepath.Join(".cursor", "mcp.json"), mapKey: "mcpServers", otherName: "other"},
		{rel: filepath.Join(".vscode", "mcp.json"), mapKey: "servers", otherName: "keep-me"},
	}
	for _, check := range checks {
		raw, err := os.ReadFile(filepath.Join(dir, check.rel))
		if err != nil {
			t.Fatalf("%s: %v", check.rel, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", check.rel, err)
		}
		servers, ok := doc[check.mapKey].(map[string]any)
		if !ok {
			t.Fatalf("%s missing %s object: %#v", check.rel, check.mapKey, doc)
		}
		if _, ok := servers[check.otherName]; !ok {
			t.Fatalf("%s lost unrelated server %q: %#v", check.rel, check.otherName, doc)
		}
		if check.mapKey == "mcpServers" {
			settings, ok := doc["settings"].(map[string]any)
			if !ok || settings["preserve"] != true {
				t.Fatalf("%s lost unrelated top-level settings: %#v", check.rel, doc)
			}
		} else if inputs, ok := doc["inputs"].([]any); !ok || len(inputs) != 1 {
			t.Fatalf("%s lost unrelated top-level inputs: %#v", check.rel, doc)
		}
		marmot, ok := servers["context-marmot"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing context-marmot: %#v", check.rel, doc)
		}
		args, _ := marmot["args"].([]any)
		if len(args) != 3 || args[2] != "den-newer" {
			t.Fatalf("%s context-marmot was not replaced: %#v", check.rel, marmot)
		}
		if strings.Contains(string(raw), "gone") {
			t.Fatalf("%s retained an old den binding: %s", check.rel, raw)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(raw), "[mcp_servers.other]") || !strings.Contains(string(raw), "den-newer") {
		t.Fatalf("codex update lost unrelated config or new binding: %s", raw)
	}
}

func TestWriteSpaceMCPConfigRejectsMalformedJSONWithoutPartialWrites(t *testing.T) {
	for _, malformedRel := range []string{
		".mcp.json",
		filepath.Join(".cursor", "mcp.json"),
		filepath.Join(".vscode", "mcp.json"),
	} {
		t.Run(malformedRel, func(t *testing.T) {
			dir := t.TempDir()
			seedMultiServerMCPConfigs(t, dir)
			malformedPath := filepath.Join(dir, malformedRel)
			malformed := []byte("{not-json\n")
			if err := os.WriteFile(malformedPath, malformed, 0o644); err != nil {
				t.Fatal(err)
			}
			before := make(map[string][]byte)
			for _, rel := range []string{
				".mcp.json",
				filepath.Join(".cursor", "mcp.json"),
				filepath.Join(".vscode", "mcp.json"),
				filepath.Join(".codex", "config.toml"),
			} {
				raw, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				before[rel] = raw
			}

			err := WriteSpaceMCPConfig(dir, "/opt/marmot", "den-new")
			if err == nil || !strings.Contains(err.Error(), malformedRel) {
				t.Fatalf("expected path-specific malformed JSON error, got %v", err)
			}
			for rel, want := range before {
				got, readErr := os.ReadFile(filepath.Join(dir, rel))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != string(want) {
					t.Fatalf("%s changed after preflight failure\nwant: %s\n got: %s", rel, want, got)
				}
			}
		})
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
