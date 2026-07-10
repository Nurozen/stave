package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
)

func TestBuildContextSortsReposAndReturnsEmptyWhenWorkDirMissing(t *testing.T) {
	cfg := executorConfig(t)
	cfg.Repos["web"] = config.Repository{Name: "web", URL: "https://example.test/web.git", DefaultBranch: "trunk"}
	cfg.Repos["api"] = config.Repository{Name: "api", URL: "https://example.test/api.git", DefaultBranch: "main"}
	// AgentWorkDir does not exist yet -> ReadDir returns IsNotExist and BuildContext returns cleanly.

	ctx, err := BuildContext(cfg)
	if err != nil {
		t.Fatalf("BuildContext error = %v", err)
	}
	if ctx.DefaultBase != "main" {
		t.Fatalf("DefaultBase = %q", ctx.DefaultBase)
	}
	if len(ctx.Repos) != 2 || ctx.Repos[0].Name != "api" || ctx.Repos[1].Name != "web" {
		t.Fatalf("repos not sorted: %#v", ctx.Repos)
	}
	if ctx.Repos[0].URL != "https://example.test/api.git" || ctx.Repos[0].DefaultBranch != "main" {
		t.Fatalf("repo detail lost: %#v", ctx.Repos[0])
	}
	if len(ctx.Spaces) != 0 {
		t.Fatalf("expected no spaces, got %#v", ctx.Spaces)
	}
}

func TestBuildContextReadDirNonIsNotExistError(t *testing.T) {
	cfg := executorConfig(t)
	// Make AgentWorkDir a regular file so ReadDir fails with a non-IsNotExist error (ENOTDIR).
	if err := os.MkdirAll(filepath.Dir(cfg.AgentWorkDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.AgentWorkDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildContext(cfg); err == nil {
		t.Fatal("expected error when AgentWorkDir is a file")
	}
}

func TestBuildContextSkipsArchiveNonDirAndUnreadableManifest(t *testing.T) {
	cfg := executorConfig(t)
	if err := os.MkdirAll(cfg.AgentWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// .archive directory must be skipped.
	if err := os.MkdirAll(filepath.Join(cfg.AgentWorkDir, ".archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file (not a dir) must be skipped.
	if err := os.WriteFile(filepath.Join(cfg.AgentWorkDir, "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without a manifest must be skipped (LoadManifest error path).
	if err := os.MkdirAll(filepath.Join(cfg.AgentWorkDir, "no-manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A valid space must be included.
	writeExecutorSpaceWithRepos(t, cfg, "beta", []space.RepoManifest{{
		Name:   "api",
		Mode:   space.ModeEdit,
		Path:   "api",
		Base:   "origin/main",
		Ref:    "origin/main",
		Branch: "stave/beta/api",
	}})

	ctx, err := BuildContext(cfg)
	if err != nil {
		t.Fatalf("BuildContext error = %v", err)
	}
	if len(ctx.Spaces) != 1 {
		t.Fatalf("expected exactly one space, got %#v", ctx.Spaces)
	}
	sp := ctx.Spaces[0]
	if sp.ID != "beta" {
		t.Fatalf("space id = %q", sp.ID)
	}
	if len(sp.Repos) != 1 || sp.Repos[0].Name != "api" || sp.Repos[0].Mode != string(space.ModeEdit) || sp.Repos[0].Branch != "stave/beta/api" {
		t.Fatalf("space repo not mapped: %#v", sp.Repos)
	}
}

func TestBuildContextSortsSpacesAndSummarizesPortals(t *testing.T) {
	cfg := executorConfig(t)
	// Create spaces out of order to exercise the sort.Slice comparator.
	writeExecutorSpace(t, cfg, "zeta")
	writeExecutorSpace(t, cfg, "alpha")
	// Give alpha a portal so portalSummaries walks the manifest.
	saveExecutorPortal(t, cfg, "alpha", "dev", portal.DriverDocker)

	ctx, err := BuildContext(cfg)
	if err != nil {
		t.Fatalf("BuildContext error = %v", err)
	}
	if len(ctx.Spaces) != 2 || ctx.Spaces[0].ID != "alpha" || ctx.Spaces[1].ID != "zeta" {
		t.Fatalf("spaces not sorted: %#v", ctx.Spaces)
	}
	alpha := ctx.Spaces[0]
	if len(alpha.Portals) != 1 {
		t.Fatalf("expected one portal, got %#v", alpha.Portals)
	}
	p := alpha.Portals[0]
	if p.ID != "dev" || p.Driver != string(portal.DriverDocker) || !p.ManifestExists {
		t.Fatalf("portal summary wrong: %#v", p)
	}
	if len(p.Providers) != 1 || p.Providers[0] != "codex" {
		t.Fatalf("providers not summarized: %#v", p.Providers)
	}
	if !p.Ownership.CreatedContainer {
		t.Fatalf("ownership not carried: %#v", p.Ownership)
	}
	// zeta has no portal manifest -> portalSummaries returns nil.
	if ctx.Spaces[1].Portals != nil {
		t.Fatalf("expected nil portals for zeta, got %#v", ctx.Spaces[1].Portals)
	}
}

func TestPortalSummariesSortsPortalsAndProviders(t *testing.T) {
	cfg := executorConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "multi")
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{ID: "multi", Kind: "ticket", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	manifest := portal.Manifest{SpaceID: "multi", Portals: map[string]portal.Portal{}}
	manifest.Portals["zzz"] = portal.Portal{ID: "zzz", Driver: portal.DriverDocker, Auth: portal.Auth{
		Providers: []portal.AuthProvider{{Provider: "codex"}, {Provider: "claude"}},
	}}
	manifest.Portals["aaa"] = portal.Portal{ID: "aaa", Driver: portal.DriverDocker}
	if err := portal.SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	got := portalSummaries(spacePath)
	if len(got) != 2 || got[0].ID != "aaa" || got[1].ID != "zzz" {
		t.Fatalf("portals not sorted by id: %#v", got)
	}
	if len(got[1].Providers) != 2 || got[1].Providers[0] != "claude" || got[1].Providers[1] != "codex" {
		t.Fatalf("providers not sorted: %#v", got[1].Providers)
	}
}
