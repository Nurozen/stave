package config

import (
	"os"
	"path/filepath"
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
