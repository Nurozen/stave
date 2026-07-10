package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHomeResolutionErrorPaths exercises every branch that surfaces an
// os.UserHomeDir failure. Setting HOME to empty makes UserHomeDir return an
// error on this platform, so each helper below must propagate it rather than
// silently returning a path built from an empty home.
func TestHomeResolutionErrorPaths(t *testing.T) {
	t.Setenv("HOME", "")

	if _, err := DefaultRoot(); err == nil {
		t.Fatal("DefaultRoot() succeeded with no HOME")
	}
	if _, err := DefaultConfigPath(); err == nil {
		t.Fatal("DefaultConfigPath() succeeded with no HOME")
	}
	if _, err := Default(); err == nil {
		t.Fatal("Default() succeeded with no HOME")
	}
	// ExpandPath must fail when it needs to resolve ~ but home is unavailable.
	if _, err := ExpandPath("~/x"); err == nil {
		t.Fatal("ExpandPath(~/x) succeeded with no HOME")
	}
	if _, err := ExpandPath("~"); err == nil {
		t.Fatal("ExpandPath(~) succeeded with no HOME")
	}
}

func TestLoadHomeErrorBranches(t *testing.T) {
	t.Setenv("HOME", "")

	// Empty path forces DefaultConfigPath(), which fails without HOME.
	if _, _, err := Load(""); err == nil {
		t.Fatal("Load(\"\") succeeded with no HOME")
	}

	// A non-empty path skips DefaultConfigPath but still calls Default(),
	// which fails without HOME.
	if _, _, err := Load("/nonexistent/config.yaml"); err == nil {
		t.Fatal("Load(path) succeeded with no HOME")
	}
}

// TestApplyDefaultsExpandErrors drives each ExpandPath call inside
// ApplyDefaults to its error branch by leaving a tilde path unresolvable.
func TestApplyDefaultsExpandErrors(t *testing.T) {
	base := t.TempDir()

	t.Run("root empty falls back to DefaultRoot error", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{}
		if err := cfg.ApplyDefaults(); err == nil {
			t.Fatal("ApplyDefaults() with empty Root and no HOME succeeded")
		}
	})

	t.Run("root empty falls back to DefaultRoot success", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		cfg := Config{}
		if err := cfg.ApplyDefaults(); err != nil {
			t.Fatalf("ApplyDefaults() error = %v", err)
		}
		if cfg.Root != filepath.Join(home, AppName) {
			t.Fatalf("Root = %q, want default root", cfg.Root)
		}
	})

	t.Run("root tilde", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{Root: "~/x"}
		if err := cfg.ApplyDefaults(); err == nil {
			t.Fatal("ApplyDefaults() with tilde Root and no HOME succeeded")
		}
	})

	t.Run("bareReposDir tilde", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{Root: base, BareReposDir: "~/bare"}
		if err := cfg.ApplyDefaults(); err == nil {
			t.Fatal("ApplyDefaults() with tilde BareReposDir and no HOME succeeded")
		}
	})

	t.Run("agentWorkDir tilde", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{Root: base, BareReposDir: base, AgentWorkDir: "~/work"}
		if err := cfg.ApplyDefaults(); err == nil {
			t.Fatal("ApplyDefaults() with tilde AgentWorkDir and no HOME succeeded")
		}
	})

	t.Run("repo bare path tilde", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{
			Root:         base,
			BareReposDir: base,
			AgentWorkDir: base,
			Repos: map[string]Repository{
				"api": {Name: "api", BareRepoPath: "~/bare/api.git"},
			},
		}
		if err := cfg.ApplyDefaults(); err == nil {
			t.Fatal("ApplyDefaults() with tilde repo BareRepoPath and no HOME succeeded")
		}
	})
}

func TestSaveHomeAndApplyDefaultsErrors(t *testing.T) {
	t.Run("empty path resolves default which fails without HOME", func(t *testing.T) {
		t.Setenv("HOME", "")
		cfg := Config{Root: "/abs/root", BareReposDir: "/abs/bare", AgentWorkDir: "/abs/work"}
		if err := cfg.Save(""); err == nil {
			t.Fatal("Save(\"\") succeeded with no HOME")
		}
	})

	t.Run("applyDefaults failure propagates", func(t *testing.T) {
		t.Setenv("HOME", "")
		// Non-empty target path avoids DefaultConfigPath; the tilde Root makes
		// the internal ApplyDefaults fail before any write.
		cfg := Config{Root: "~/x"}
		if err := cfg.Save(filepath.Join(t.TempDir(), ConfigFileName)); err == nil {
			t.Fatal("Save() succeeded despite ApplyDefaults failure")
		}
	})
}

// TestSaveWriteFileError forces the fsio.WriteFileAtomic path to fail by
// pointing Save at an existing directory: the temp file writes fine but the
// final rename over a directory cannot succeed.
func TestSaveWriteFileError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := filepath.Join(home, "config-as-dir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: filepath.Join(home, "root")}
	if err := cfg.Save(target); err == nil || !strings.Contains(err.Error(), "write config") {
		t.Fatalf("Save(dir target) error = %v, want write config", err)
	}
}

// TestRegisterRepositoryInitializesNilMap covers the nil-map guard: registering
// into a Config whose Repos map was never allocated must lazily create it.
func TestRegisterRepositoryInitializesNilMap(t *testing.T) {
	cfg := &Config{BareReposDir: "/tmp/bare"}
	if cfg.Repos != nil {
		t.Fatal("precondition: Repos should start nil")
	}
	repo, err := cfg.RegisterRepository("api", "https://example.test/api.git", "main")
	if err != nil {
		t.Fatalf("RegisterRepository() error = %v", err)
	}
	if cfg.Repos == nil {
		t.Fatal("RegisterRepository did not initialize Repos map")
	}
	if got := cfg.Repos["api"]; got.Name != "api" || got.URL != repo.URL {
		t.Fatalf("stored repo = %#v", got)
	}
}
