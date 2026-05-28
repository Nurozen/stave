package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
)

func TestValidatePlan(t *testing.T) {
	cfg := testConfig(t)
	spec := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(spec, []byte("spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Operations: []Operation{{
		Type:       OpSpaceCreate,
		SpaceID:    "ex-2",
		Kind:       "ticket",
		SpecPath:   spec,
		Edits:      []RepoRef{{Name: "api"}},
		References: []RepoRef{{Name: "api", Ref: "main"}},
	}}}
	if err := ValidatePlan(cfg, plan); err != nil {
		t.Fatalf("ValidatePlan() error = %v", err)
	}
}

func TestValidatePlanRejectsUnknownRepoExistingSpaceAndUnsupported(t *testing.T) {
	cfg := testConfig(t)
	tests := []Plan{
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "missing"}}}}},
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-1"}}},
		{Operations: []Operation{{Type: "space_destroy", SpaceID: "ex-1"}}},
	}
	for _, plan := range tests {
		if err := ValidatePlan(cfg, plan); err == nil {
			t.Fatalf("ValidatePlan(%#v) succeeded unexpectedly", plan)
		}
	}
}

func TestValidatePlanRejectsInvalidRefsAndPathConflicts(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:        "ex-1",
		Kind:      "ticket",
		CreatedAt: time.Now().UTC(),
		Repos: []space.RepoManifest{{
			Name:         "api",
			Mode:         space.ModeEdit,
			Path:         "api",
			BareRepoPath: cfg.Repos["api"].BareRepoPath,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	tests := []Plan{
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "api", Ref: "bad..ref"}}}}},
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "api"}, {Name: "api"}}}}},
		{Operations: []Operation{{Type: OpSpaceAdd, SpaceID: "ex-1", Repo: "api", Mode: "edit"}}},
	}
	for _, plan := range tests {
		if err := ValidatePlan(cfg, plan); err == nil {
			t.Fatalf("ValidatePlan(%#v) succeeded unexpectedly", plan)
		}
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"api": {Name: "api", URL: "https://example.test/api.git", BareRepoPath: filepath.Join(root, "bare-repos", "api.git")},
		},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "ex-1", Kind: "ticket", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return cfg
}
