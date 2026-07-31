package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/portal"
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

func TestValidatePlanAcceptsSummonAfterCreate(t *testing.T) {
	cfg := testConfig(t)
	plan := Plan{Operations: []Operation{
		{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "api"}}},
		{Type: OpSummon, SpaceID: "ex-2", Summoner: "codex"},
	}}
	if err := ValidatePlan(cfg, plan); err != nil {
		t.Fatalf("ValidatePlan() error = %v", err)
	}
}

func TestValidatePlanRejectsUnknownRepoExistingSpaceAndUnsupported(t *testing.T) {
	cfg := testConfig(t)
	tests := []Plan{
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "missing"}}}}},
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-1"}}},
		{Operations: []Operation{{Type: OpSummon, SpaceID: "missing", Summoner: "codex"}}},
		{Operations: []Operation{{Type: OpSummon, SpaceID: "ex-1", Summoner: "bad"}}},
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

func TestValidatePlanAcceptsSamePlanPortalChain(t *testing.T) {
	cfg := testConfig(t)
	plan := Plan{Operations: []Operation{
		{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "dev", Driver: "docker", ContainerRoot: "/workspace/ex-1"},
		{Type: OpPortalAuthLogin, SpaceID: "ex-1", PortalID: "dev", Provider: "codex", Method: "native"},
		{Type: OpPortalUp, SpaceID: "ex-1", PortalID: "dev", AttachMode: "none"},
		{Type: OpPortalSummon, SpaceID: "ex-1", PortalID: "dev", Summoner: "codex", Mode: "tmux"},
	}}
	if err := ValidatePlan(cfg, plan); err != nil {
		t.Fatalf("ValidatePlan() error = %v", err)
	}
}

func TestValidatePlanRejectsBadPortalPlans(t *testing.T) {
	cfg := testConfig(t)
	saveTestPortal(t, cfg, "ex-1", "ssh-dev", portal.DriverSSH)
	saveTestPortal(t, cfg, "ex-1", "local-dev", portal.DriverDocker)
	tests := []Plan{
		{Operations: []Operation{{Type: OpPortalStatus, SpaceID: "ex-1", PortalID: "missing"}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "bad", Driver: "ssh"}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "bad", Driver: "docker", ContainerRoot: "/"}}},
		{Operations: []Operation{{Type: OpPortalAuthInherit, SpaceID: "ex-1", PortalID: "local-dev", Provider: "codex", Method: "copy-cache"}}},
		{Operations: []Operation{{Type: OpPortalSummon, SpaceID: "ex-1", PortalID: "local-dev", Summoner: "codex", Mode: "tmux"}}},
		{Operations: []Operation{{Type: OpPortalDown, SpaceID: "ex-1", PortalID: "ssh-dev"}}},
		{Operations: []Operation{{Type: OpPortalDetach, SpaceID: "ex-1", PortalID: "local-dev"}}},
		{Operations: []Operation{{Type: OpPortalSync, SpaceID: "ex-1", PortalID: "local-dev", Delete: true}}},
	}
	for _, plan := range tests {
		if err := ValidatePlan(cfg, plan); err == nil {
			t.Fatalf("ValidatePlan(%#v) succeeded unexpectedly", plan)
		}
	}
}

func TestValidatePlanAdditionalBranches(t *testing.T) {
	cfg := testConfig(t)
	if err := ValidatePlan(cfg, Plan{}); err != nil {
		t.Fatalf("empty plan error = %v", err)
	}
	saveTestPortal(t, cfg, "ex-1", "ssh-dev", portal.DriverSSH)
	saveTestPortal(t, cfg, "ex-1", "local-dev", portal.DriverDocker)

	okPlans := []Plan{
		{Operations: []Operation{{Type: OpPortalList}}},
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2"}, {Type: OpPortalInit, SpaceID: "ex-2", PortalID: "dev", Driver: "docker", ContainerRoot: "/workspace/ex-2"}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "new-ec2", Driver: string(portal.DriverEC2Attach), InstanceID: "i-123", RemoteRoot: "~/stave/ex-1"}}},
		{Operations: []Operation{{Type: OpPortalSync, SpaceID: "ex-1", PortalID: "local-dev", Delete: true, MaxDelete: 1}}},
		{Operations: []Operation{{Type: OpPortalDetach, SpaceID: "ex-1", PortalID: "ssh-dev"}}},
	}
	for _, plan := range okPlans {
		if err := ValidatePlan(cfg, plan); err != nil {
			t.Fatalf("ValidatePlan(%#v) error = %v", plan, err)
		}
	}

	specMissing := filepath.Join(t.TempDir(), "missing.md")
	badPlans := []Plan{
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2"}, {Type: OpSpaceCreate, SpaceID: "ex-2"}}},
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", SpecPath: specMissing}}},
		{Operations: []Operation{{Type: OpSpaceAdd, SpaceID: "missing", Repo: "api", Mode: "edit"}}},
		{Operations: []Operation{{Type: OpSpaceAdd, SpaceID: "ex-1", Repo: "missing", Mode: "edit"}}},
		{Operations: []Operation{{Type: OpSpaceAdd, SpaceID: "ex-1", Repo: "api", Mode: "bad"}}},
		{Operations: []Operation{{Type: OpSpaceAdd, SpaceID: "ex-1", Repo: "api", Mode: "edit", Branch: "bad branch"}}},
		{Operations: []Operation{{Type: OpSpaceSync, SpaceID: "../x"}}},
		{Operations: []Operation{{Type: OpSpaceStatus, SpaceID: "../x"}}},
		{Operations: []Operation{{Type: OpReposSync, Repo: "missing"}}},
		{Operations: []Operation{{Type: OpPortalList, SpaceID: "missing"}}},
		{Operations: []Operation{{Type: OpPortalLogs, SpaceID: "ex-1", PortalID: "local-dev", Tail: 0}}},
		{Operations: []Operation{{Type: OpPortalLogs, SpaceID: "ex-1", PortalID: "local-dev", Tail: 10, Follow: true}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "bad/name", Driver: "docker"}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "bad-driver", Driver: "ssh"}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "local-dev", Driver: "docker"}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "new", Driver: "docker", Repo: "missing"}}},
		{Operations: []Operation{{Type: OpPortalInit, SpaceID: "ex-1", PortalID: "new", Driver: "docker", ComposeFiles: []string{"*.yaml"}}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "ssh-missing-host", Driver: "ssh"}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "ec2-missing-id", Driver: string(portal.DriverEC2Attach)}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "new", Driver: "docker"}}},
		{Operations: []Operation{{Type: OpPortalAttach, SpaceID: "ex-1", PortalID: "ssh-dev", Driver: "ssh", Host: "example.test"}}},
		{Operations: []Operation{{Type: OpPortalConfigure, SpaceID: "ex-1", PortalID: "missing"}}},
		{Operations: []Operation{{Type: OpPortalConfigure, SpaceID: "ex-1", PortalID: "local-dev", Workdir: "bad\npath"}}},
	}
	for _, plan := range badPlans {
		if err := ValidatePlan(cfg, plan); err == nil {
			t.Fatalf("ValidatePlan(%#v) succeeded unexpectedly", plan)
		}
	}
}

func TestValidateRefishRejectsSpecificInvalidForms(t *testing.T) {
	for _, ref := range []string{"@", "feature@{1}", "/main", "main/", "main.", "feature~1", ".hidden", "refs/heads/bad.lock", "bad//ref"} {
		if err := validateRefish("ref", ref); err == nil || !strings.Contains(err.Error(), "ref") {
			t.Fatalf("validateRefish(%q) err = %v", ref, err)
		}
	}
	if err := validateSafePortalPath("workdir", "/workspace/ex-1"); err != nil {
		t.Fatalf("absolute workdir should be accepted: %v", err)
	}
}

func TestValidatePlanSagaOperations(t *testing.T) {
	cfg := testConfig(t)
	saveTestSaga(t, cfg, "story-1", space.SagaMember{ID: "member-0"})

	okPlans := []Plan{
		{Operations: []Operation{{Type: OpSagaCreate, SagaID: "story-2"}}},
		// Create-then-add in the same plan: the add consults the planned record.
		{Operations: []Operation{
			{Type: OpSpaceCreate, SpaceID: "ex-2"},
			{Type: OpSpaceAdd, SpaceID: "ex-2", Repo: "api", Mode: "edit"},
		}},
		// Full same-plan saga chain with after edges between planned members.
		{Operations: []Operation{
			{Type: OpSagaCreate, SagaID: "story-2"},
			{Type: OpSpaceCreate, SpaceID: "m-1", Edits: []RepoRef{{Name: "api"}}},
			{Type: OpSagaAdd, SagaID: "story-2", SpaceID: "m-1"},
			{Type: OpSpaceCreate, SpaceID: "m-2"},
			{Type: OpSagaAdd, SagaID: "story-2", SpaceID: "m-2", After: []string{"m-1"}},
		}},
		{Operations: []Operation{{Type: OpSagaAdd, SagaID: "story-1", SpaceID: "ex-1"}}},
		{Operations: []Operation{{Type: OpSagaStatus, SagaID: "story-1"}}},
		{Operations: []Operation{
			{Type: OpSagaCreate, SagaID: "story-2"},
			{Type: OpSagaStatus, SagaID: "story-2"},
		}},
		// Reference context may be added to a planned saga; edits may not.
		{Operations: []Operation{
			{Type: OpSagaCreate, SagaID: "story-2"},
			{Type: OpSpaceAdd, SpaceID: "story-2", Repo: "api", Mode: "reference"},
		}},
	}
	for _, plan := range okPlans {
		if err := ValidatePlan(cfg, plan); err != nil {
			t.Fatalf("ValidatePlan(%#v) error = %v", plan, err)
		}
	}

	badPlans := []Plan{
		// space_create must never mint a saga; that is stave_saga_create's job.
		{Operations: []Operation{{Type: OpSpaceCreate, SpaceID: "ex-2", Kind: "saga"}}},
		// Bogus destructive saga op types are rejected outright.
		{Operations: []Operation{{Type: "saga_destroy", SagaID: "story-1"}}},
		{Operations: []Operation{{Type: OpSagaCreate, SagaID: "story-1"}}},
		{Operations: []Operation{{Type: OpSagaCreate, SagaID: "../x"}}},
		{Operations: []Operation{{Type: OpSagaCreate, SagaID: "story-2", Edits: []RepoRef{{Name: "api"}}}}},
		{Operations: []Operation{{Type: OpSagaAdd, SagaID: "story-1", SpaceID: "missing"}}},
		{Operations: []Operation{{Type: OpSagaAdd, SagaID: "ex-1", SpaceID: "story-1"}}},
		{Operations: []Operation{{Type: OpSagaAdd, SagaID: "story-1", SpaceID: "story-1"}}},
		{Operations: []Operation{{Type: OpSagaAdd, SagaID: "story-1", SpaceID: "ex-1", After: []string{"../x"}}}},
		{Operations: []Operation{
			{Type: OpSagaCreate, SagaID: "story-2"},
			{Type: OpSagaCreate, SagaID: "story-3"},
			{Type: OpSagaAdd, SagaID: "story-2", SpaceID: "story-3"},
		}},
		{Operations: []Operation{{Type: OpSagaStatus, SagaID: "missing"}}},
		{Operations: []Operation{
			{Type: OpSpaceCreate, SpaceID: "ex-2"},
			{Type: OpSagaStatus, SagaID: "ex-2"},
		}},
		{Operations: []Operation{
			{Type: OpSagaCreate, SagaID: "story-2"},
			{Type: OpSpaceAdd, SpaceID: "story-2", Repo: "api", Mode: "edit"},
		}},
		// Same-plan path conflict: the create already occupies the repo path.
		{Operations: []Operation{
			{Type: OpSpaceCreate, SpaceID: "ex-2", Edits: []RepoRef{{Name: "api"}}},
			{Type: OpSpaceAdd, SpaceID: "ex-2", Repo: "api", Mode: "edit"},
		}},
	}
	for _, plan := range badPlans {
		if err := ValidatePlan(cfg, plan); err == nil {
			t.Fatalf("ValidatePlan(%#v) succeeded unexpectedly", plan)
		}
	}
}

func saveTestSaga(t *testing.T, cfg config.Config, sagaID string, members ...space.SagaMember) {
	t.Helper()
	spacePath := filepath.Join(cfg.AgentWorkDir, sagaID)
	if err := os.MkdirAll(spacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:        sagaID,
		Kind:      space.KindSaga,
		CreatedAt: time.Now().UTC(),
		Saga:      &space.SagaManifest{Members: members},
	}); err != nil {
		t.Fatal(err)
	}
}

func saveTestPortal(t *testing.T, cfg config.Config, spaceID string, portalID string, driver portal.Driver) {
	t.Helper()
	item := portal.Portal{ID: portalID, Driver: driver}
	switch driver {
	case portal.DriverSSH:
		item.Target.Host = "example.test"
	case portal.DriverEC2Attach:
		item.Target.InstanceID = "i-123"
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, spaceID)
	manifest, err := portal.LoadManifest(spacePath)
	if err != nil {
		manifest = portal.Manifest{SpaceID: spaceID, Portals: map[string]portal.Portal{}}
	}
	manifest.Portals[portalID] = item
	if err := portal.SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
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
	if err := cfg.ApplyDefaults(); err != nil {
		t.Fatal(err)
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
