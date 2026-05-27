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
