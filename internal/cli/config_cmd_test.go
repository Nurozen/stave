package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"gopkg.in/yaml.v3"
)

func TestCLIConfigShowJSONWithoutFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := decodeJSONObject(t, runCLI(t, "config", "show", "--json"))
	if payload["configPath"] != filepath.Join(home, ".config", "stave", "config.yaml") || payload["exists"] != false {
		t.Fatalf("path/exists: %v", payload)
	}
	if payload["root"] != filepath.Join(home, "stave") ||
		payload["bareReposDir"] != filepath.Join(home, "stave", "bare-repos") ||
		payload["agentWorkDir"] != filepath.Join(home, "stave", "agent-work") ||
		payload["defaultBase"] != "main" {
		t.Fatalf("defaults: %v", payload)
	}
	if repos, ok := payload["repos"].(map[string]any); !ok || len(repos) != 0 {
		t.Fatalf("repos should be an empty object: %v", payload["repos"])
	}
	mem, _ := payload["memory"].(map[string]any)
	if mem["provider"] != "marmot" || mem["binary"] != "marmot" || mem["default"] != false {
		t.Fatalf("memory: %v", payload["memory"])
	}
	tethers, _ := payload["tethers"].(map[string]any)
	if tethers["enabled"] != true || tethers["strongThreshold"] != float64(3) {
		t.Fatalf("tethers: %v", payload["tethers"])
	}
	summon, _ := payload["summon"].(map[string]any)
	if summon["default"] != "codex" {
		t.Fatalf("summon: %v", payload["summon"])
	}
	if cmds, _ := summon["commands"].(map[string]any); cmds["cursor"] != "cursor-agent" {
		t.Fatalf("summon commands: %v", summon["commands"])
	}
	if _, has := payload["agent"]; has {
		t.Fatalf("agent section must be omitted: %v", payload)
	}
}

func TestCLIConfigShowJSONExpandsTildeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "stave", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "root: ~/custom\nrepos:\n  api:\n    url: https://example.com/api.git\n    description: the api\ntethers:\n  enabled: false\n  strongThreshold: 5\nmemory:\n  default: true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The document must match config.Load's resolution exactly (the same
	// values every other verb runs with), including its derived directories.
	loaded, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeJSONObject(t, runCLI(t, "config", "show", "--json"))
	if payload["exists"] != true || payload["configPath"] != path {
		t.Fatalf("exists/path: %v", payload)
	}
	if payload["root"] != filepath.Join(home, "custom") || strings.Contains(payload["root"].(string), "~") {
		t.Fatalf("~ expansion: %v", payload)
	}
	if payload["bareReposDir"] != loaded.BareReposDir || payload["agentWorkDir"] != loaded.AgentWorkDir {
		t.Fatalf("derived dirs must match config.Load: %v vs %+v", payload, loaded)
	}
	repos, _ := payload["repos"].(map[string]any)
	api, _ := repos["api"].(map[string]any)
	if api["name"] != "api" || api["url"] != "https://example.com/api.git" || api["bareRepoPath"] != loaded.Repos["api"].BareRepoPath || api["description"] != "the api" {
		t.Fatalf("repo record: %v", api)
	}
	if !filepath.IsAbs(api["bareRepoPath"].(string)) {
		t.Fatalf("bareRepoPath must be absolute: %v", api)
	}
	if _, has := api["defaultBranch"]; has {
		t.Fatalf("empty defaultBranch must be omitted: %v", api)
	}
	tethers, _ := payload["tethers"].(map[string]any)
	if tethers["enabled"] != false || tethers["strongThreshold"] != float64(5) {
		t.Fatalf("tethers: %v", payload["tethers"])
	}
	if mem, _ := payload["memory"].(map[string]any); mem["default"] != true || mem["provider"] != "marmot" {
		t.Fatalf("memory: %v", payload["memory"])
	}
}

func TestCLIConfigShowAndPathHonorConfigFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	override := filepath.Join(home, "elsewhere", "stave.yaml")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("defaultBase: develop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := decodeJSONObject(t, runCLI(t, "--config", override, "config", "show", "--json"))
	if payload["configPath"] != override || payload["exists"] != true || payload["defaultBase"] != "develop" {
		t.Fatalf("override: %v", payload)
	}
	if out := runCLI(t, "--config", override, "config", "path"); out != override+"\n" {
		t.Fatalf("config path with override = %q", out)
	}
	if out := runCLI(t, "config", "path"); out != filepath.Join(home, ".config", "stave", "config.yaml")+"\n" {
		t.Fatalf("default config path = %q", out)
	}
	// A missing override still shows defaults with exists:false.
	payload = decodeJSONObject(t, runCLI(t, "--config", filepath.Join(home, "nope.yaml"), "config", "show", "--json"))
	if payload["exists"] != false || payload["defaultBase"] != "main" {
		t.Fatalf("missing override: %v", payload)
	}
}

func TestCLIConfigShowHumanIsYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	out := runCLI(t, "config", "show")
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("human output is not YAML: %v\n%s", err, out)
	}
	if doc["root"] != filepath.Join(home, "stave") || doc["exists"] != true {
		t.Fatalf("yaml doc: %v", doc)
	}
	if strings.Contains(out, "apiKeyRef") || strings.Contains(out, "agent:") {
		t.Fatalf("agent section leaked:\n%s", out)
	}
	if !strings.HasPrefix(out, "configPath: ") {
		t.Fatalf("field order should start with configPath:\n%s", out)
	}
}

func TestCLIConfigShowJSONMalformedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "stave", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("root: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ := jsonErrorCode(t, "config", "show", "--json")
	if code != "unknown" {
		t.Fatalf("malformed config code = %q", code)
	}
}
