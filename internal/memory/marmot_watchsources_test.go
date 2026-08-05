package memory

// Tests for disableDenSourceWatch: the owned-create write of
// `watch_sources: false` into the den vault _config.md frontmatter.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDisableDenSourceWatchPreservesFrontmatter(t *testing.T) {
	denPath := t.TempDir()
	configPath := writeDenVaultFixture(t, denPath, denVaultFrontmatter)
	if err := disableDenSourceWatch(denPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	assertWatchSourcesOff(t, string(got), denVaultFrontmatter)
}

// withFrontmatterLines returns denVaultFrontmatter with extra lines spliced in
// just before its closing `---` delimiter, so fixtures keep the realistic
// marmot key set (vault_id and friends) that assertWatchSourcesOff checks.
func withFrontmatterLines(lines string) string {
	idx := strings.LastIndex(denVaultFrontmatter, "---\n")
	return denVaultFrontmatter[:idx] + lines + denVaultFrontmatter[idx:]
}

func TestDisableDenSourceWatchDashValueIsNotTheDelimiter(t *testing.T) {
	// The closing delimiter is the first LINE that is exactly `---`, not the
	// first `---` SUBSTRING: YAML values legitimately contain dashes, and a
	// substring scan would splice `watch_sources: false` into the middle of
	// such a value — corrupting the value AND leaving the key outside the
	// frontmatter, where marmot would never read it (a silently
	// source-watching den, the exact failure disableDenSourceWatch exists to
	// prevent).
	cases := []struct {
		name     string
		injected string
	}{
		{name: "plain value opening with dashes", injected: "summary: --- draft ---\n"},
		{name: "quoted dashes value", injected: "summary: \"---\"\n"},
		{name: "block scalar containing a dashes line", injected: "notes: |\n  --- not a delimiter ---\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := withFrontmatterLines(tc.injected)
			denPath := t.TempDir()
			configPath := writeDenVaultFixture(t, denPath, original)
			if err := disableDenSourceWatch(denPath); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			// Bytes preserved + key inserted before the REAL closing delimiter,
			// frontmatter still well-formed YAML.
			assertWatchSourcesOff(t, got, original)
			// Stated positively: the dashes-bearing lines survive verbatim and
			// the new key lands AFTER them, i.e. still inside the frontmatter.
			if !strings.Contains(got, tc.injected) {
				t.Fatalf("dash-bearing value not byte-identical:\n%s", got)
			}
			if strings.Index(got, tc.injected) > strings.Index(got, "watch_sources: false") {
				t.Fatalf("watch_sources inserted BEFORE the dash-bearing value (substring scan bug):\n%s", got)
			}
		})
	}
}

func TestDisableDenSourceWatchCRLFClosingDelimiter(t *testing.T) {
	// CRLF-authored config: the closing delimiter line is `---\r`, which the
	// line scan tolerates by trimming the \r. Without that tolerance the
	// frontmatter would read as unterminated and the attach would abort.
	// The dashes value doubles as the substring-scan trap.
	original := "---\nvault_id: v1\r\nsummary: --- draft ---\r\n---\r\nBody notes stay put.\r\n"
	want := "---\nvault_id: v1\r\nsummary: --- draft ---\r\nwatch_sources: false\n---\r\nBody notes stay put.\r\n"
	denPath := t.TempDir()
	configPath := writeDenVaultFixture(t, denPath, original)
	if err := disableDenSourceWatch(denPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("CRLF rewrite:\ngot:\n%q\nwant:\n%q", raw, want)
	}
	// Closing delimiter located as the LAST `---\r\n` (the summary value ends
	// in one too), then the frontmatter body must still parse.
	closeAt := strings.LastIndex(string(raw), "---\r\n")
	if closeAt < 0 {
		t.Fatalf("frontmatter delimiters lost:\n%q", raw)
	}
	fm := string(raw[len("---\n"):closeAt])
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(fm), &parsed); err != nil {
		t.Fatalf("frontmatter no longer valid YAML: %v\n%q", err, fm)
	}
	if v, isBool := parsed["watch_sources"].(bool); !isBool || v {
		t.Fatalf("watch_sources = %#v, want false", parsed["watch_sources"])
	}
	if parsed["vault_id"] != "v1" {
		t.Fatalf("vault_id lost: %#v", parsed)
	}
}

func TestDisableDenSourceWatchIdempotent(t *testing.T) {
	// Second write is a no-op; an existing key — whatever its value — is
	// never overwritten.
	denPath := t.TempDir()
	configPath := writeDenVaultFixture(t, denPath, denVaultFrontmatter)
	if err := disableDenSourceWatch(denPath); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := disableDenSourceWatch(denPath); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("second write changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	explicit := "---\nvault_id: v1\nwatch_sources: true\n---\n"
	denPath2 := t.TempDir()
	configPath2 := writeDenVaultFixture(t, denPath2, explicit)
	if err := disableDenSourceWatch(denPath2); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != explicit {
		t.Fatalf("explicit watch_sources value must be left untouched:\n%s", got)
	}
}

func TestDisableDenSourceWatchNoVaultSkips(t *testing.T) {
	// --no-vault dens / old-binary stubs: absent vault (or absent den path
	// entirely) is a silent skip, never an error and never a created file.
	denPath := t.TempDir() // den exists, no vault/ inside
	if err := disableDenSourceWatch(denPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(denPath, "vault")); !os.IsNotExist(err) {
		t.Fatal("skip must not create vault/")
	}
	if err := disableDenSourceWatch(filepath.Join(denPath, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := disableDenSourceWatch(""); err != nil {
		t.Fatal(err)
	}
}

func TestDisableDenSourceWatchBrokenConfigErrors(t *testing.T) {
	for name, content := range map[string]string{
		"no-frontmatter": "just markdown, no delimiters\n",
		"unterminated":   "---\nvault_id: v1\n",
	} {
		t.Run(name, func(t *testing.T) {
			denPath := t.TempDir()
			writeDenVaultFixture(t, denPath, content)
			if err := disableDenSourceWatch(denPath); err == nil {
				t.Fatal("present-but-unparseable config must fail (warn-and-continue would yield a source-watching den)")
			}
		})
	}
}

func TestDisableDenSourceWatchUnwritableErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	denPath := t.TempDir()
	writeDenVaultFixture(t, denPath, denVaultFrontmatter)
	vaultDir := filepath.Join(denPath, "vault")
	if err := os.Chmod(vaultDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(vaultDir, 0o755) })
	if err := disableDenSourceWatch(denPath); err == nil {
		t.Fatal("unwritable vault dir must fail the write")
	}
}

func TestAttachFailsOnBrokenVaultConfig(t *testing.T) {
	// End-to-end: a present-but-unparseable _config.md aborts the attach
	// BEFORE the MCP config is written (cleanly abortable, no serve races).
	denPath := t.TempDir()
	writeDenVaultFixture(t, denPath, "no frontmatter here\n")
	env := fmt.Sprintf(`{"schema":1,"den_id":"bad","den_path":%q,"pointer_written":false,"warnings":[]}`, denPath)
	bin := writeFakeMarmot(t, map[string]fakeResp{
		"den create": {Stdout: env, Code: 0},
	})
	m := NewMarmot(bin)
	dir := t.TempDir()
	res, err := m.Attach(context.Background(), AttachOptions{SpaceID: "bad", SpacePath: dir})
	if err == nil || !strings.Contains(err.Error(), "watch_sources") {
		t.Fatalf("expected watch_sources write failure, got %v", err)
	}
	if res.MCPConfigWritten {
		t.Fatalf("MCP config must not be written on abort: %#v", res)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(statErr) {
		t.Fatal(".mcp.json must not exist after aborted attach")
	}
}
