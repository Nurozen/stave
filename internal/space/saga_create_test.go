package space

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/memory"
)

func TestCreateInSagaRegistersAndWiresDen(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	fw := &fakeWirer{Fake: &memory.Fake{}}
	svc.Memory = fw
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-cs", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "cs-1", SagaID: "epic-cs", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatalf("Create(--saga) error = %v", err)
	}
	members := loadSagaMembers(t, svc, "epic-cs")
	if len(members) != 1 || members[0].ID != "cs-1" {
		t.Fatalf("members = %#v", members)
	}
	memberManifest, err := LoadManifest(svc.SpacePath("cs-1"))
	if err != nil {
		t.Fatal(err)
	}
	if members[0].CreatedAt.IsZero() || !members[0].CreatedAt.Equal(memberManifest.CreatedAt) {
		t.Fatalf("member CreatedAt = %v, want %v", members[0].CreatedAt, memberManifest.CreatedAt)
	}
	// Deviation 18: the attachmentless member is MCP-wired to the saga den.
	want := svc.SpacePath("cs-1") + "|epic-cs"
	if !reflect.DeepEqual(fw.wrote, []string{want}) {
		t.Fatalf("wirer calls = %#v, want exactly [%s]", fw.wrote, want)
	}
	// A member attaching its own memory keeps its own MCP configs.
	if err := svc.Create(ctx, CreateOptions{ID: "cs-2", SagaID: "epic-cs", Memories: []string{"own-store"}}); err != nil {
		t.Fatal(err)
	}
	if len(fw.wrote) != 1 {
		t.Fatalf("member with own attachment was wired to the saga den: %#v", fw.wrote)
	}
	// --after edges land in the roster.
	if err := svc.Create(ctx, CreateOptions{ID: "cs-3", SagaID: "epic-cs", After: []string{"cs-1"}}); err != nil {
		t.Fatal(err)
	}
	members = loadSagaMembers(t, svc, "epic-cs")
	if len(members) != 3 || !reflect.DeepEqual(members[2].After, []string{"cs-1"}) {
		t.Fatalf("members = %#v", members)
	}
}

func TestCreateInSagaPreflightRejectsBeforeCreate(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pf"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "pf-1")
	if err := svc.SagaAdd(ctx, "epic-pf", "pf-1", nil, false); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	cases := []struct {
		name string
		opts CreateOptions
		want string
	}{
		{"after-non-member", CreateOptions{ID: "pf-2", SagaID: "epic-pf", After: []string{"ghost-1"}}, "not a saga member"},
		{"self-cycle", CreateOptions{ID: "pf-3", SagaID: "epic-pf", After: []string{"pf-3"}}, "cycle"},
		{"duplicate", CreateOptions{ID: "pf-1", SagaID: "epic-pf"}, "duplicate member"},
		{"bad-charset", CreateOptions{ID: "bad id", SagaID: "epic-pf"}, "space id"},
		{"saga-self-member", CreateOptions{ID: "epic-pf", SagaID: "epic-pf"}, "its own member"},
		{"after-without-saga", CreateOptions{ID: "pf-4", After: []string{"pf-1"}}, "--after requires --saga"},
		{"not-a-saga", CreateOptions{ID: "pf-5", SagaID: "pf-1"}, "is not a saga"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Create(ctx, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Create error = %v, want %q", err, tc.want)
			}
		})
	}
	for _, id := range []string{"pf-2", "pf-3", "pf-4", "pf-5"} {
		if _, err := os.Stat(svc.SpacePath(id)); !os.IsNotExist(err) {
			t.Fatalf("space %s created despite preflight rejection: %v", id, err)
		}
	}
	if len(fg.calls) != 0 {
		t.Fatalf("git touched despite rejections: %#v", fg.calls)
	}
	if members := loadSagaMembers(t, svc, "epic-pf"); len(members) != 1 {
		t.Fatalf("roster mutated: %#v", members)
	}
}

func TestCreateInSagaAfterDefaultBases(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-db"}); err != nil {
		t.Fatal(err)
	}
	// The predecessor edits repo-a; its reference entry on repo-b must never match.
	if err := svc.Create(ctx, CreateOptions{ID: "db-p1", SagaID: "epic-db", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "db-n1", SagaID: "epic-db", After: []string{"db-p1"}, Edits: []RepoSpec{{Name: "repo-a"}, {Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(svc.SpacePath("db-n1"))
	if err != nil {
		t.Fatal(err)
	}
	repoA, _, _ := manifest.FindRepo("repo-a")
	if repoA.Base != "refs/heads/stave/db-p1/repo-a" {
		t.Fatalf("repo-a Base = %q, want the predecessor branch", repoA.Base)
	}
	repoB, _, _ := manifest.FindRepo("repo-b")
	if repoB.Base != "origin/trunk" {
		t.Fatalf("repo-b Base = %q, want default chain (reference-only predecessor entries never match)", repoB.Base)
	}
	// An explicit base wins over the default rule.
	if err := svc.Create(ctx, CreateOptions{ID: "db-n2", SagaID: "epic-db", After: []string{"db-p1"}, Edits: []RepoSpec{{Name: "repo-a", Ref: "origin/main"}}}); err != nil {
		t.Fatal(err)
	}
	manifest, err = LoadManifest(svc.SpacePath("db-n2"))
	if err != nil {
		t.Fatal(err)
	}
	repoA, _, _ = manifest.FindRepo("repo-a")
	if repoA.Base != "origin/main" {
		t.Fatalf("explicit base overridden: %q", repoA.Base)
	}
	// Two predecessors editing the same repo → hard error naming both.
	if err := svc.Create(ctx, CreateOptions{ID: "db-p2", SagaID: "epic-db", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	err = svc.Create(ctx, CreateOptions{ID: "db-n3", SagaID: "epic-db", After: []string{"db-p1", "db-p2"}, Edits: []RepoSpec{{Name: "repo-a"}}})
	if err == nil || !strings.Contains(err.Error(), "db-p1") || !strings.Contains(err.Error(), "db-p2") || !strings.Contains(err.Error(), "-e repo-a:space:") {
		t.Fatalf("ambiguous predecessors error = %v", err)
	}
	if _, statErr := os.Stat(svc.SpacePath("db-n3")); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguity did not stop creation: %v", statErr)
	}
}

func TestCreateInSagaDryRunRegistersNothing(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dry"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "dry-p1", SagaID: "epic-dry", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	fg.calls = nil
	if err := svc.Create(ctx, CreateOptions{ID: "dry-m1", SagaID: "epic-dry", After: []string{"dry-p1"}, Edits: []RepoSpec{{Name: "repo-a"}}, DryRun: true}); err != nil {
		t.Fatalf("Create(--saga dry-run) error = %v", err)
	}
	if !strings.Contains(out.String(), "from refs/heads/stave/dry-p1/repo-a") {
		t.Fatalf("dry-run did not resolve the --after default base:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "dry-run: add dry-m1 to saga epic-dry") {
		t.Fatalf("dry-run missing registration line:\n%s", out.String())
	}
	if mutating := mutatingCalls(fg.calls); len(mutating) != 0 {
		t.Fatalf("dry-run issued mutating git calls: %#v", mutating)
	}
	if _, err := os.Stat(svc.SpacePath("dry-m1")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the space: %v", err)
	}
	if members := loadSagaMembers(t, svc, "epic-dry"); len(members) != 1 {
		t.Fatalf("dry-run mutated the roster: %#v", members)
	}
}

func TestCreateInSagaRegistrationFailurePrintsRecovery(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-rf"}); err != nil {
		t.Fatal(err)
	}
	// An unreadable sibling fails the single-saga membership scan at
	// registration time — AFTER the preflight (manifest-level only) passed and
	// the space was created.
	badPath := filepath.Join(cfg.AgentWorkDir, "corrupt")
	if err := os.MkdirAll(badPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badPath, ManifestName), []byte("id: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	err := svc.Create(ctx, CreateOptions{ID: "rf-1", SagaID: "epic-rf"})
	if err == nil || !strings.Contains(err.Error(), "unreadable manifest") {
		t.Fatalf("Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(svc.SpacePath("rf-1"), ManifestName)); statErr != nil {
		t.Fatalf("member space missing after registration failure: %v", statErr)
	}
	got := out.String()
	if !strings.Contains(got, "created but NOT registered") || !strings.Contains(got, "stave saga add epic-rf rf-1") {
		t.Fatalf("missing recovery message:\n%s", got)
	}
	if members := loadSagaMembers(t, svc, "epic-rf"); len(members) != 0 {
		t.Fatalf("roster = %#v, want empty", members)
	}
}

func TestWriteSagaAgentsTemplate(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	svc.Memory = &memory.Fake{}
	spec := filepath.Join(t.TempDir(), "epic.md")
	if err := os.WriteFile(spec, []byte("epic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ag", SpecPath: spec, Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "ag-1")
	mustInitSpace(t, svc, "ag-2")
	if err := svc.SagaAdd(ctx, "epic-ag", "ag-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-ag", "ag-2", []string{"ag-1"}, false); err != nil {
		t.Fatal(err)
	}
	agents, err := os.ReadFile(filepath.Join(svc.SpacePath("epic-ag"), AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(agents)
	for _, want := range []string{
		"# Stave Saga: epic-ag",
		"coordinates the saga's member spaces",
		"Read `spec/` before starting",
		"(den: epic-ag)",
		"1. `ag-1` (../ag-1)",
		"2. `ag-2` (../ag-2) — after: ag-1",
		"For live state run: stave saga status epic-ag --json",
		"separate per-member summons",
		"read that member's AGENTS.md",
		"Never rebase or retarget",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("saga AGENTS.md missing %q:\n%s", want, got)
		}
	}
	// Volatile data stays out: no branches, no stack bases.
	for _, banned := range []string{"stave/ag-1", "stave/ag-2", "refs/heads", "origin/"} {
		if strings.Contains(got, banned) {
			t.Fatalf("saga AGENTS.md leaked volatile ref %q:\n%s", banned, got)
		}
	}
}

func TestMemberAgentsSagaSection(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ms"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "ms-1", SagaID: "epic-ms", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	agents, err := os.ReadFile(filepath.Join(svc.SpacePath("ms-1"), AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(agents)
	for _, want := range []string{
		"# Stave Workspace Instructions",
		"## Repositories",
		"## Saga",
		"member of saga `epic-ms` (`../epic-ms`)",
		"`repo-a` stacks on base `origin/main`",
		"Coordinate via `stave saga status epic-ms --json`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("member AGENTS.md missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "## Repositories") > strings.Index(got, "## Saga") {
		t.Fatalf("'## Saga' must follow the Repositories block:\n%s", got)
	}
	// Removal refreshes the member and drops the section.
	if err := svc.SagaRemove(ctx, "epic-ms", "ms-1"); err != nil {
		t.Fatal(err)
	}
	agents, err = os.ReadFile(filepath.Join(svc.SpacePath("ms-1"), AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agents), "## Saga") {
		t.Fatalf("removed member still carries the saga section:\n%s", agents)
	}
}
