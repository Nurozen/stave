package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingConfigUsesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if path != filepath.Join(home, ".config", "stave", "config.yaml") {
		t.Fatalf("config path = %q", path)
	}
	if cfg.Root != filepath.Join(home, "stave") {
		t.Fatalf("Root = %q", cfg.Root)
	}
	if cfg.BareReposDir != filepath.Join(home, "stave", "bare-repos") {
		t.Fatalf("BareReposDir = %q", cfg.BareReposDir)
	}
	if cfg.AgentWorkDir != filepath.Join(home, "stave", "agent-work") {
		t.Fatalf("AgentWorkDir = %q", cfg.AgentWorkDir)
	}
	if cfg.DefaultBase != "main" {
		t.Fatalf("DefaultBase = %q", cfg.DefaultBase)
	}
	if cfg.Agent.DefaultProvider != DefaultAgentProvider {
		t.Fatalf("Agent.DefaultProvider = %q", cfg.Agent.DefaultProvider)
	}
	if cfg.Agent.AutoIncant {
		t.Fatal("Agent.AutoIncant defaulted to true")
	}
	if cfg.Agent.Providers["openai"].Model != DefaultAgentModelOpenAI {
		t.Fatalf("openai model = %q", cfg.Agent.Providers["openai"].Model)
	}
	if cfg.Summon.Default != "codex" {
		t.Fatalf("Summon.Default = %q", cfg.Summon.Default)
	}
	if cfg.Summon.Commands["cursor"] != "cursor-agent" {
		t.Fatalf("cursor command = %q", cfg.Summon.Commands["cursor"])
	}
	if cfg.Memory.Provider != "marmot" {
		t.Fatalf("Memory.Provider = %q", cfg.Memory.Provider)
	}
	if cfg.Memory.Default {
		t.Fatal("Memory.Default defaulted to true")
	}
	if cfg.Memory.Binary != "marmot" {
		t.Fatalf("Memory.Binary = %q", cfg.Memory.Binary)
	}
	if cfg.Tethers.Enabled == nil || !*cfg.Tethers.Enabled {
		t.Fatalf("Tethers.Enabled = %v, want non-nil true", cfg.Tethers.Enabled)
	}
	if !cfg.Tethers.IsEnabled() {
		t.Fatal("Tethers.IsEnabled() = false, want true")
	}
	if cfg.Tethers.StrongThreshold != 3 {
		t.Fatalf("Tethers.StrongThreshold = %d, want 3", cfg.Tethers.StrongThreshold)
	}
}

func TestTethersKillSwitchRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "stave", "config.yaml")

	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	disabled := false
	cfg.Tethers.Enabled = &disabled
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Tethers.Enabled == nil {
		t.Fatal("Tethers.Enabled = nil after load, kill switch did not persist")
	}
	if *loaded.Tethers.Enabled {
		t.Fatal("Tethers.Enabled = true after load, kill switch did not survive")
	}
	if loaded.Tethers.IsEnabled() {
		t.Fatal("Tethers.IsEnabled() = true after load, kill switch did not survive")
	}
}

func TestRepositoryDescriptionRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "stave", "config.yaml")

	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if _, err := cfg.RegisterRepository("api", "https://example.test/api.git", "main"); err != nil {
		t.Fatalf("RegisterRepository() error = %v", err)
	}
	repo := cfg.Repos["api"]
	repo.Description = "the API service"
	cfg.Repos["api"] = repo
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Repos["api"].Description != "the API service" {
		t.Fatalf("Description = %q, want %q", loaded.Repos["api"].Description, "the API service")
	}
}

func TestTethersStrongThresholdClamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config.yaml")

	for _, raw := range []string{"tethers:\n  strongThreshold: 0\n", "tethers:\n  strongThreshold: -5\n"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, _, err := Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if loaded.Tethers.StrongThreshold != 3 {
			t.Fatalf("StrongThreshold = %d after clamp, want 3 (input %q)", loaded.Tethers.StrongThreshold, raw)
		}
	}
}

func TestTethersApplyDefaults(t *testing.T) {
	c := TethersConfig{}
	c.ApplyDefaults()
	if c.Enabled == nil || !*c.Enabled {
		t.Fatalf("Enabled = %v, want non-nil true", c.Enabled)
	}
	if c.StrongThreshold != 3 {
		t.Fatalf("StrongThreshold = %d, want 3", c.StrongThreshold)
	}

	disabled := false
	c2 := TethersConfig{Enabled: &disabled, StrongThreshold: 7}
	c2.ApplyDefaults()
	if c2.Enabled == nil || *c2.Enabled {
		t.Fatalf("Enabled = %v, want non-nil false (preserved)", c2.Enabled)
	}
	if c2.StrongThreshold != 7 {
		t.Fatalf("StrongThreshold = %d, want 7 (preserved)", c2.StrongThreshold)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "stave", "config.yaml")

	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if _, err := cfg.RegisterRepository("api", "https://example.test/api.git", "main"); err != nil {
		t.Fatalf("RegisterRepository() error = %v", err)
	}
	cfg.Agent.DefaultProvider = "anthropic"
	cfg.Agent.AutoIncant = true
	cfg.Agent.Providers["anthropic"] = AgentProviderConfig{
		Model:     "claude-opus-4-7",
		APIKeyRef: "keychain:stave/agent/anthropic",
	}
	cfg.Summon.Default = "cursor"
	cfg.Summon.Commands["cursor"] = "/opt/bin/cursor-agent"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != DefaultConfigMode {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	repo := loaded.Repos["api"]
	if repo.Name != "api" || repo.URL != "https://example.test/api.git" || repo.DefaultBranch != "main" {
		t.Fatalf("repo = %#v", repo)
	}
	if repo.BareRepoPath != filepath.Join(home, "stave", "bare-repos", "api.git") {
		t.Fatalf("BareRepoPath = %q", repo.BareRepoPath)
	}
	if loaded.Agent.DefaultProvider != "anthropic" {
		t.Fatalf("agent provider = %q", loaded.Agent.DefaultProvider)
	}
	if !loaded.Agent.AutoIncant {
		t.Fatal("agent autoIncant did not round-trip")
	}
	if loaded.Agent.Providers["anthropic"].APIKeyRef != "keychain:stave/agent/anthropic" {
		t.Fatalf("agent anthropic config = %#v", loaded.Agent.Providers["anthropic"])
	}
	if loaded.Summon.Default != "cursor" || loaded.Summon.Commands["cursor"] != "/opt/bin/cursor-agent" {
		t.Fatalf("summon config = %#v", loaded.Summon)
	}
}

func TestApplyDefaultsExpandsPathsAndFillsNestedDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{
		Root:         "~/custom-stave",
		BareReposDir: "~/bare",
		AgentWorkDir: "~/work",
		Repos: map[string]Repository{
			"api": {URL: "file:///api.git"},
			"web": {Name: "web", URL: "file:///web.git", BareRepoPath: "~/bare/web.git"},
		},
		Agent: AgentConfig{
			Providers: map[string]AgentProviderConfig{
				"openai": {Model: "gpt-custom"},
			},
		},
		Summon: SummonConfig{
			Commands: map[string]string{"codex": "/opt/bin/codex"},
		},
	}

	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatalf("ApplyDefaults() error = %v", err)
	}
	if cfg.Root != filepath.Join(home, "custom-stave") {
		t.Fatalf("Root = %q", cfg.Root)
	}
	if cfg.BareReposDir != filepath.Join(home, "bare") || cfg.AgentWorkDir != filepath.Join(home, "work") {
		t.Fatalf("dirs = %q %q", cfg.BareReposDir, cfg.AgentWorkDir)
	}
	if cfg.DefaultBase != DefaultBase {
		t.Fatalf("DefaultBase = %q", cfg.DefaultBase)
	}
	if cfg.Repos["api"].Name != "api" || cfg.Repos["api"].BareRepoPath != filepath.Join(home, "bare", "api.git") {
		t.Fatalf("api repo defaults = %#v", cfg.Repos["api"])
	}
	if cfg.Repos["web"].BareRepoPath != filepath.Join(home, "bare", "web.git") {
		t.Fatalf("web bare path = %q", cfg.Repos["web"].BareRepoPath)
	}
	if cfg.Agent.DefaultProvider != DefaultAgentProvider {
		t.Fatalf("agent default provider = %q", cfg.Agent.DefaultProvider)
	}
	if cfg.Agent.Providers["openai"].Model != "gpt-custom" || cfg.Agent.Providers["openai"].APIKeyRef == "" {
		t.Fatalf("openai provider = %#v", cfg.Agent.Providers["openai"])
	}
	if cfg.Agent.Providers["anthropic"].Model != DefaultAgentModelAnthropic {
		t.Fatalf("anthropic provider = %#v", cfg.Agent.Providers["anthropic"])
	}
	if cfg.Summon.Commands["codex"] != "/opt/bin/codex" || cfg.Summon.Commands["cursor"] != "cursor-agent" {
		t.Fatalf("summon defaults = %#v", cfg.Summon)
	}
}

func TestEnsureRootDirsAndRepositoryRegistration(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Root:         filepath.Join(root, "stave"),
		BareReposDir: filepath.Join(root, "stave", "bare-repos"),
		AgentWorkDir: filepath.Join(root, "stave", "agent-work"),
		Repos:        map[string]Repository{},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatalf("EnsureRootDirs() error = %v", err)
	}
	for _, dir := range []string{cfg.Root, cfg.BareReposDir, cfg.AgentWorkDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("dir %s = %v %v", dir, info, err)
		}
	}

	repo, err := cfg.RegisterRepository("api", "git@example.test:api.git", "main")
	if err != nil {
		t.Fatalf("RegisterRepository() error = %v", err)
	}
	if repo.BareRepoPath != filepath.Join(cfg.BareReposDir, "api.git") {
		t.Fatalf("BareRepoPath = %q", repo.BareRepoPath)
	}
	cfg.UnregisterRepository("api")
	if _, ok := cfg.Repos["api"]; ok {
		t.Fatal("UnregisterRepository left repo in config")
	}
	if _, err := cfg.RegisterRepository("../bad", "https://example.test/repo.git", "main"); err == nil {
		t.Fatal("RegisterRepository accepted invalid name")
	}
	if _, err := cfg.RegisterRepository("bad-url", "example.test/repo.git", "main"); err == nil {
		t.Fatal("RegisterRepository accepted unsupported URL")
	}
}

func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ExpandPath("~/custom")
	if err != nil {
		t.Fatalf("ExpandPath() error = %v", err)
	}
	if got != filepath.Join(home, "custom") {
		t.Fatalf("ExpandPath() = %q", got)
	}
	got, err = ExpandPath("~")
	if err != nil {
		t.Fatalf("ExpandPath(~) error = %v", err)
	}
	if got != home {
		t.Fatalf("ExpandPath(~) = %q", got)
	}
	if got, err = ExpandPath(""); err != nil || got != "" {
		t.Fatalf("ExpandPath(empty) = %q %v", got, err)
	}
	if got, err = ExpandPath("./relative"); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("ExpandPath(relative) = %q %v", got, err)
	}
}

func TestValidateNames(t *testing.T) {
	if err := ValidateName("repo", "repo-a_1.2"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	for _, name := range []string{"", "../x", "x/y", "-bad", "x:y"} {
		if err := ValidateName("repo", name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestValidateSpaceID(t *testing.T) {
	for _, id := range []string{"space-a_1.2", "s1", "a.b-c"} {
		if err := ValidateSpaceID(id); err != nil {
			t.Fatalf("valid space id %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", "../x", "x/y", "-bad", ".archive/foo"} {
		if err := ValidateSpaceID(id); err == nil {
			t.Fatalf("invalid space id %q accepted", id)
		}
	}
}

func TestValidateGitURLSchemes(t *testing.T) {
	valid := []string{
		"git@example.test:repo.git",
		"ssh://example.test/repo.git",
		"https://example.test/repo.git",
		"http://example.test/repo.git",
		"file:///tmp/repo.git",
		"/tmp/repo.git",
		"./repo",
		"../repo",
	}
	for _, raw := range valid {
		if err := ValidateGitURL(raw); err != nil {
			t.Fatalf("valid URL %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "example.test/repo.git", "ftp://example.test/repo.git"} {
		if err := ValidateGitURL(raw); err == nil {
			t.Fatalf("invalid URL %q accepted", raw)
		}
	}
}

func TestLoadAndSaveErrorBranches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	badYAML := filepath.Join(home, "bad.yaml")
	if err := os.WriteFile(badYAML, []byte("root: [unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(badYAML); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load(bad yaml) error = %v", err)
	}

	dirPath := filepath.Join(home, "dir-config")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dirPath); err == nil {
		t.Fatal("Load(directory) succeeded")
	}

	cfg := Config{Root: filepath.Join(home, "root")}
	if err := cfg.Save(""); err != nil {
		t.Fatalf("Save(default path) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "stave", ConfigFileName)); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}

	block := filepath.Join(home, "block")
	if err := os.WriteFile(block, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(filepath.Join(block, ConfigFileName)); err == nil || !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("Save(blocked directory) error = %v", err)
	}
	blockedCfg := Config{Root: block, BareReposDir: filepath.Join(block, "bare"), AgentWorkDir: filepath.Join(block, "work")}
	if err := blockedCfg.EnsureRootDirs(); err == nil || !strings.Contains(err.Error(), "create") {
		t.Fatalf("EnsureRootDirs(blocked) error = %v", err)
	}
}
