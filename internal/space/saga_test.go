package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/memory"
)

// Compile-time seam check: the real marmot provider must satisfy the
// space-side mcpWirer interface, so any signature drift between
// memory.MCPWirer and space's structural copy breaks the build here.
var _ mcpWirer = (*memory.Marmot)(nil)

// fakeWirer is a memory.Fake that also satisfies the space-side mcpWirer seam,
// recording member wiring calls for assertions.
type fakeWirer struct {
	*memory.Fake
	mu      sync.Mutex
	wrote   []string // "<spacePath>|<storeID>"
	removed []string // spacePath
}

func (f *fakeWirer) WriteMCPConfig(_ context.Context, spacePath, storeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wrote = append(f.wrote, spacePath+"|"+storeID)
	return nil
}

func (f *fakeWirer) RemoveMCPConfig(_ context.Context, spacePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, spacePath)
	return nil
}

func mustInitSpace(t *testing.T, svc Service, id string) {
	t.Helper()
	if err := svc.InitSpace(context.Background(), InitOptions{ID: id}); err != nil {
		t.Fatalf("InitSpace(%s) error = %v", id, err)
	}
}

func loadSagaMembers(t *testing.T, svc Service, sagaID string) []SagaMember {
	t.Helper()
	manifest, err := LoadManifest(svc.SpacePath(sagaID))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Saga == nil {
		t.Fatalf("space %s is not a saga: %#v", sagaID, manifest)
	}
	return manifest.Saga.Members
}

func TestCreateSagaWritesV2ManifestAndLocks(t *testing.T) {
	svc, _, cfg := testService(t)
	if err := svc.CreateSaga(context.Background(), SagaCreateOptions{ID: "epic-0"}); err != nil {
		t.Fatalf("CreateSaga() error = %v", err)
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-0"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 || manifest.Kind != KindSaga || manifest.Saga == nil {
		t.Fatalf("manifest = %+v, want version 2 kind saga with saga block", manifest)
	}
	if len(manifest.Saga.Members) != 0 {
		t.Fatalf("members = %#v, want empty roster", manifest.Saga.Members)
	}
	if manifest.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not stamped")
	}
	// First mutation from a pristine tree creates the .locks/ spine.
	for _, lock := range []string{
		filepath.Join(cfg.AgentWorkDir, ".locks", "membership.lock"),
		filepath.Join(cfg.AgentWorkDir, ".locks", "sagas", "epic-0.lock"),
	} {
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("lock file %s missing: %v", lock, err)
		}
	}
	if _, err := os.Stat(filepath.Join(svc.SpacePath("epic-0"), AgentsName)); err != nil {
		t.Fatalf("AGENTS.md missing: %v", err)
	}
	// The .locks dir must not surface as a space.
	spaces, err := svc.ListSpaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range spaces {
		if entry.ID == ".locks" {
			t.Fatal("ListSpaces surfaced .locks as a space")
		}
	}
}

func TestCreateSagaRejectsExistingSpaces(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()

	// Existing plain space.
	mustInitSpace(t, svc, "plain-1")
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "plain-1"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateSaga over plain space error = %v", err)
	}

	// Existing pseudo-saga (Kind set, no saga block).
	mustInitSpace(t, svc, "pseudo-1")
	manifest, err := LoadManifest(svc.SpacePath("pseudo-1"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Kind = KindSaga
	if err := SaveManifest(svc.SpacePath("pseudo-1"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "pseudo-1"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateSaga over pseudo-saga error = %v", err)
	}

	// Double saga create.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-1"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("double CreateSaga error = %v", err)
	}
}

func TestCreateAndInitRejectKindSaga(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.Create(ctx, CreateOptions{ID: "imp-1", Kind: KindSaga}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Create(kind=saga) error = %v", err)
	}
	if err := svc.InitSpace(ctx, InitOptions{ID: "imp-2", Kind: KindSaga}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("InitSpace(kind=saga) error = %v", err)
	}
	for _, id := range []string{"imp-1", "imp-2"} {
		if _, err := os.Stat(svc.SpacePath(id)); !os.IsNotExist(err) {
			t.Fatalf("space %s dir created despite rejection, stat err = %v", id, err)
		}
	}
}

func TestCreateSagaRejectsMultipleOwnedStores(t *testing.T) {
	svc, _, _ := testService(t)
	err := svc.CreateSaga(context.Background(), SagaCreateOptions{ID: "epic-multi", Memories: []string{".", "other:."}})
	if err == nil || !strings.Contains(err.Error(), "at most one owned memory store") {
		t.Fatalf("CreateSaga with two fresh specs error = %v", err)
	}
	if _, err := os.Stat(svc.SpacePath("epic-multi")); !os.IsNotExist(err) {
		t.Fatalf("space dir created despite rejection, stat err = %v", err)
	}
}

func TestCreateSagaAttachesDurableOwnedDen(t *testing.T) {
	svc, _, _ := testService(t)
	var mu sync.Mutex
	var attaches []memory.AttachOptions
	fake := &memory.Fake{AttachFn: func(_ context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
		mu.Lock()
		attaches = append(attaches, opts)
		mu.Unlock()
		id := opts.UseID
		owned := false
		if id == "" {
			id = opts.SpaceID
			owned = true
		}
		return memory.AttachResult{Provider: "fake", StoreID: id, Name: opts.Name, Owned: owned}, nil
	}}
	svc.Memory = fake

	if err := svc.CreateSaga(context.Background(), SagaCreateOptions{ID: "epic-lt", Memories: []string{".", "ext-store"}}); err != nil {
		t.Fatalf("CreateSaga() error = %v", err)
	}
	if len(attaches) != 2 {
		t.Fatalf("attach calls = %d, want 2", len(attaches))
	}
	byUse := map[string]string{} // UseID → Lifetime
	for _, opts := range attaches {
		byUse[opts.UseID] = opts.Lifetime
	}
	if byUse[""] != "durable" {
		t.Fatalf("fresh (owned) attach lifetime = %q, want durable", byUse[""])
	}
	if byUse["ext-store"] != "" {
		t.Fatalf("attach-existing lifetime = %q, want empty", byUse["ext-store"])
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-lt"))
	if err != nil {
		t.Fatal(err)
	}
	owned := 0
	for _, mem := range manifest.Memories {
		if mem.Owned {
			owned++
		}
	}
	if len(manifest.Memories) != 2 || owned != 1 {
		t.Fatalf("memories = %#v, want 2 attachments with exactly 1 owned", manifest.Memories)
	}
}

func TestAddRepoOnSagaRejectsEditAllowsReference(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-repo"}); err != nil {
		t.Fatal(err)
	}
	err := svc.AddRepo(ctx, AddOptions{SpaceID: "epic-repo", RepoName: "repo-a", Mode: ModeEdit})
	if err == nil || !strings.Contains(err.Error(), "no edit worktrees") {
		t.Fatalf("edit AddRepo on saga error = %v", err)
	}
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "epic-repo", RepoName: "repo-b", Mode: ModeReference}); err != nil {
		t.Fatalf("reference AddRepo on saga error = %v", err)
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-repo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || manifest.Repos[0].Mode != ModeReference {
		t.Fatalf("repos = %#v, want one reference", manifest.Repos)
	}
	if manifest.Version != 2 || manifest.Saga == nil {
		t.Fatalf("saga manifest degraded by AddRepo: %+v", manifest)
	}
}

func TestSagaAddValidations(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-v"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-w"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "mem-1")
	mustInitSpace(t, svc, "plain-2")

	if err := svc.SagaAdd(ctx, "plain-2", "mem-1", nil, false); err == nil || !strings.Contains(err.Error(), "is not a saga") {
		t.Fatalf("SagaAdd on plain space error = %v", err)
	}
	if err := svc.SagaAdd(ctx, "epic-v", "ghost-1", nil, false); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("SagaAdd of missing member error = %v", err)
	}
	if err := svc.SagaAdd(ctx, "epic-v", "epic-w", nil, false); err == nil || !strings.Contains(err.Error(), "itself a saga") {
		t.Fatalf("SagaAdd of a saga error = %v", err)
	}
	if err := svc.SagaAdd(ctx, "epic-v", "epic-v", nil, false); err == nil || !strings.Contains(err.Error(), "its own member") {
		t.Fatalf("SagaAdd of self error = %v", err)
	}
	if err := svc.SagaAdd(ctx, "epic-v", "mem-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-w", "mem-1", nil, false); err == nil || !strings.Contains(err.Error(), `already a member of saga "epic-v"`) {
		t.Fatalf("double-membership SagaAdd error = %v", err)
	}

	// A directory whose manifest carries a DIFFERENT id is not that space and
	// must never be registered under the directory name.
	imposterPath := svc.SpacePath("imposter-1")
	if err := os.MkdirAll(imposterPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(imposterPath, Manifest{ID: "someone-else", CreatedAt: svc.now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-v", "imposter-1", nil, false); err == nil || !strings.Contains(err.Error(), `existing manifest id "someone-else" does not match "imposter-1"`) {
		t.Fatalf("SagaAdd(manifest id mismatch) error = %v", err)
	}
	if members := loadSagaMembers(t, svc, "epic-v"); len(members) != 1 {
		t.Fatalf("mismatched-id add mutated the roster: %#v", members)
	}
}

func TestSagaAddUpsertAndClearAfter(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-u"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "a-1")
	mustInitSpace(t, svc, "b-1")

	if err := svc.SagaAdd(ctx, "epic-u", "a-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-u", "b-1", []string{"a-1"}, false); err != nil {
		t.Fatal(err)
	}
	members := loadSagaMembers(t, svc, "epic-u")
	if len(members) != 2 || !reflect.DeepEqual(members[1].After, []string{"a-1"}) {
		t.Fatalf("members = %#v", members)
	}
	// CreatedAt captured from the member manifest.
	memberManifest, err := LoadManifest(svc.SpacePath("b-1"))
	if err != nil {
		t.Fatal(err)
	}
	if members[1].CreatedAt.IsZero() || !members[1].CreatedAt.Equal(memberManifest.CreatedAt) {
		t.Fatalf("member CreatedAt = %v, want %v", members[1].CreatedAt, memberManifest.CreatedAt)
	}

	// Re-add with empty after preserves edges.
	if err := svc.SagaAdd(ctx, "epic-u", "b-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if members = loadSagaMembers(t, svc, "epic-u"); !reflect.DeepEqual(members[1].After, []string{"a-1"}) {
		t.Fatalf("after edges not preserved: %#v", members[1])
	}
	// clearAfter resets.
	if err := svc.SagaAdd(ctx, "epic-u", "b-1", nil, true); err != nil {
		t.Fatal(err)
	}
	if members = loadSagaMembers(t, svc, "epic-u"); len(members[1].After) != 0 {
		t.Fatalf("after edges not cleared: %#v", members[1])
	}
	// Non-empty after replaces.
	if err := svc.SagaAdd(ctx, "epic-u", "b-1", []string{"a-1"}, false); err != nil {
		t.Fatal(err)
	}
	if members = loadSagaMembers(t, svc, "epic-u"); !reflect.DeepEqual(members[1].After, []string{"a-1"}) {
		t.Fatalf("after edges not replaced: %#v", members[1])
	}

	// Unknown after target fails on save and mutates nothing.
	if err := svc.SagaAdd(ctx, "epic-u", "b-1", []string{"nope-1"}, false); err == nil || !strings.Contains(err.Error(), "not a saga member") {
		t.Fatalf("unknown after target error = %v", err)
	}
	if members = loadSagaMembers(t, svc, "epic-u"); !reflect.DeepEqual(members[1].After, []string{"a-1"}) {
		t.Fatalf("failed add mutated the roster: %#v", members)
	}
	// A self-edge is a cycle.
	mustInitSpace(t, svc, "c-1")
	if err := svc.SagaAdd(ctx, "epic-u", "c-1", []string{"c-1"}, false); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("self-edge error = %v", err)
	}
}

func TestSagaAddWiresMCPOnlyForAttachmentlessMembers(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	fw := &fakeWirer{Fake: &memory.Fake{}}
	svc.Memory = fw

	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-mcp", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "bare-1")
	mustInitSpace(t, svc, "own-1")
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "own-1", UseID: "own-store", Name: "own"}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SagaAdd(ctx, "epic-mcp", "bare-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-mcp", "own-1", nil, false); err != nil {
		t.Fatal(err)
	}
	want := svc.SpacePath("bare-1") + "|epic-mcp"
	if !reflect.DeepEqual(fw.wrote, []string{want}) {
		t.Fatalf("wirer calls = %#v, want exactly [%s] (members with own attachments keep their configs)", fw.wrote, want)
	}
	// Member AGENTS.md re-touched.
	if _, err := os.Stat(filepath.Join(svc.SpacePath("bare-1"), AgentsName)); err != nil {
		t.Fatalf("member AGENTS.md missing: %v", err)
	}
}

func TestSagaRemoveDropsEdgesAndUnwires(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	fw := &fakeWirer{Fake: &memory.Fake{}}
	svc.Memory = fw

	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-rm", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "r-1")
	mustInitSpace(t, svc, "r-2")
	if err := svc.SagaAdd(ctx, "epic-rm", "r-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-rm", "r-2", []string{"r-1"}, false); err != nil {
		t.Fatal(err)
	}

	if err := svc.SagaRemove(ctx, "epic-rm", "r-1"); err != nil {
		t.Fatalf("SagaRemove() error = %v", err)
	}
	members := loadSagaMembers(t, svc, "epic-rm")
	if len(members) != 1 || members[0].ID != "r-2" || len(members[0].After) != 0 {
		t.Fatalf("members after remove = %#v, want r-2 with dropped after-edge", members)
	}
	if !reflect.DeepEqual(fw.removed, []string{svc.SpacePath("r-1")}) {
		t.Fatalf("unwire calls = %#v", fw.removed)
	}

	// A member with its own attachment keeps its MCP configs on removal.
	if err := svc.AttachMemory(ctx, AttachMemoryOptions{SpaceID: "r-2", UseID: "own-store", Name: "own"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaRemove(ctx, "epic-rm", "r-2"); err != nil {
		t.Fatal(err)
	}
	if len(fw.removed) != 1 {
		t.Fatalf("unwire ran for a member with its own attachment: %#v", fw.removed)
	}

	if err := svc.SagaRemove(ctx, "epic-rm", "r-1"); err == nil || !strings.Contains(err.Error(), "is not a member") {
		t.Fatalf("removing a non-member error = %v", err)
	}
}

func TestSagaListJoinsKindAndMembership(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ls"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "ls-1")
	mustInitSpace(t, svc, "ls-2")
	if err := svc.SagaAdd(ctx, "epic-ls", "ls-1", nil, false); err != nil {
		t.Fatal(err)
	}

	entries, err := svc.SagaList()
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]SagaListEntry{}
	for _, entry := range entries {
		rows[entry.ID] = entry
	}
	saga := rows["epic-ls"]
	if !saga.IsSaga || saga.Kind != KindSaga || !reflect.DeepEqual(saga.Members, []string{"ls-1"}) {
		t.Fatalf("saga row = %#v", saga)
	}
	if member := rows["ls-1"]; member.IsSaga || member.MemberOf != "epic-ls" {
		t.Fatalf("member row = %#v", member)
	}
	if loner := rows["ls-2"]; loner.IsSaga || loner.MemberOf != "" {
		t.Fatalf("non-member row = %#v", loner)
	}
}

func TestResolveMemberState(t *testing.T) {
	svc, _, cfg := testService(t)
	archiveRoot := filepath.Join(cfg.AgentWorkDir, ".archive")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 5, 27, 1, 2, 3, 0, time.UTC)
	writeSpace := func(t *testing.T, path, id string, created time.Time) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := SaveManifest(path, Manifest{ID: id, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}

	// live
	writeSpace(t, svc.SpacePath("m-live"), "m-live", ts)
	// corrupt: malformed YAML (tab indentation is invalid)
	if err := os.MkdirAll(svc.SpacePath("m-bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.SpacePath("m-bad"), ManifestName), []byte("\t- not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	// archived: exact-name archive of the same incarnation
	writeSpace(t, filepath.Join(archiveRoot, "m-arch"), "m-arch", ts)
	// archived: collision-suffix archive shape
	writeSpace(t, filepath.Join(archiveRoot, "m-suf-20260101120000"), "m-suf", ts)
	// dash-named sibling archives that must NOT claim member "pay":
	// a different space whose id shares the prefix…
	writeSpace(t, filepath.Join(archiveRoot, "pay-web"), "pay-web", ts)
	// …and a collision-suffix dir holding a DIFFERENT space's manifest.
	writeSpace(t, filepath.Join(archiveRoot, "pay-20260101120000"), "other-1", ts)
	// reused-ID stale archive: CreatedAt mismatch
	writeSpace(t, filepath.Join(archiveRoot, "m-re"), "m-re", ts.Add(-time.Hour))
	// wrong suffix length (13 digits) is not the collision shape
	writeSpace(t, filepath.Join(archiveRoot, "m-len-2026010112000"), "m-len", ts)

	tests := []struct {
		name   string
		member SagaMember
		want   MemberState
		detail string
	}{
		{"live", SagaMember{ID: "m-live", CreatedAt: ts}, MemberLive, ""},
		{"corrupt", SagaMember{ID: "m-bad", CreatedAt: ts}, MemberCorrupt, ""},
		{"missing", SagaMember{ID: "m-gone", CreatedAt: ts}, MemberMissing, ""},
		{"archived-exact", SagaMember{ID: "m-arch", CreatedAt: ts}, MemberArchived, filepath.Join(archiveRoot, "m-arch")},
		{"archived-suffix", SagaMember{ID: "m-suf", CreatedAt: ts}, MemberArchived, filepath.Join(archiveRoot, "m-suf-20260101120000")},
		{"dash-sibling-discriminated", SagaMember{ID: "pay", CreatedAt: ts}, MemberMissing, ""},
		{"reused-id-stale-archive", SagaMember{ID: "m-re", CreatedAt: ts}, MemberMissing, ""},
		{"zero-created-at-mismatch", SagaMember{ID: "m-arch"}, MemberMissing, ""},
		{"wrong-suffix-length", SagaMember{ID: "m-len", CreatedAt: ts}, MemberMissing, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, detail := svc.resolveMemberState(tc.member)
			if state != tc.want {
				t.Fatalf("state = %s (detail %q), want %s", state, detail, tc.want)
			}
			if tc.want == MemberArchived && detail != tc.detail {
				t.Fatalf("detail = %q, want %q", detail, tc.detail)
			}
			if tc.want == MemberCorrupt && detail == "" {
				t.Fatal("corrupt state must carry the read error")
			}
		})
	}

	// The dash-named sibling archive still resolves for its OWN id.
	if state, _ := svc.resolveMemberState(SagaMember{ID: "pay-web", CreatedAt: ts}); state != MemberArchived {
		t.Fatalf("pay-web state = %s, want archived", state)
	}

	// A live directory whose manifest matches the id but NOT the incarnation
	// stamp is a stranger reusing the id — teardown must not destroy it.
	writeSpace(t, svc.SpacePath("m-relive"), "m-relive", ts.Add(2*time.Hour))
	state, detail := svc.resolveMemberState(SagaMember{ID: "m-relive", CreatedAt: ts})
	if state != MemberMissing || !strings.Contains(detail, "id reused by a different space") {
		t.Fatalf("reused live id = %s (%q), want missing with a reused-id detail", state, detail)
	}
	// A zero stamp on either side keeps the pre-stamp live semantics.
	if state, _ := svc.resolveMemberState(SagaMember{ID: "m-relive"}); state != MemberLive {
		t.Fatalf("zero-stamp roster entry = %s, want live", state)
	}
}

func TestResolveBaseOwner(t *testing.T) {
	siblings := []SpaceEntry{
		{ID: "dep-1", Manifest: &Manifest{Repos: []RepoManifest{
			{Name: "repo-a", Mode: ModeEdit, Branch: "stave/dep-1/repo-a", BareRepoPath: "/repos/a.git"},
		}}},
		{ID: "dep-2", Manifest: &Manifest{Repos: []RepoManifest{
			{Name: "repo-a", Mode: ModeEdit, Branch: "custom-line", BareRepoPath: "/repos/a.git"},
		}}},
		{ID: "dep-3", Manifest: &Manifest{Repos: []RepoManifest{
			{Name: "repo-b", Mode: ModeEdit, Branch: "feature-x", BareRepoPath: "/repos/b.git"},
		}}},
		{ID: "ref-1", Manifest: &Manifest{Repos: []RepoManifest{
			{Name: "repo-a", Mode: ModeReference, Ref: "origin/main", BareRepoPath: "/repos/a.git"},
		}}},
		{ID: "broken-1", Err: fmt.Errorf("unreadable")},
	}
	tests := []struct {
		base  string
		bare  string
		owner string
		ok    bool
	}{
		{"refs/heads/stave/dep-1/repo-a", "/repos/a.git", "dep-1", true},
		{"origin/stave/dep-1/repo-a", "/repos/a.git", "dep-1", true},
		{"stave/dep-1/repo-a", "/repos/a.git", "dep-1", true},
		{"origin/custom-line", "/repos/a.git", "dep-2", true},
		{"custom-line", "/repos/a.git", "dep-2", true},
		// Same-named branch in a DIFFERENT repo must not alias.
		{"custom-line", "/repos/b.git", "", false},
		{"feature-x", "/repos/a.git", "", false},
		// Convention fallback for owners no longer listed.
		{"refs/heads/stave/gone-9/repo-a", "/repos/a.git", "gone-9", true},
		{"origin/main", "/repos/a.git", "", false},
		{"", "/repos/a.git", "", false},
	}
	for _, tc := range tests {
		owner, ok := resolveBaseOwner(tc.base, tc.bare, siblings)
		if owner != tc.owner || ok != tc.ok {
			t.Fatalf("resolveBaseOwner(%q, %q) = (%q, %v), want (%q, %v)", tc.base, tc.bare, owner, ok, tc.owner, tc.ok)
		}
	}
}

func TestSagaAddWarnsOnStackedBases(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	bare := cfg.Repos["repo-a"].BareRepoPath
	writeMember := func(t *testing.T, id, base string) {
		t.Helper()
		path := svc.SpacePath(id)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := Manifest{ID: id, CreatedAt: time.Now().UTC()}
		if base != "" {
			manifest.Repos = []RepoManifest{{
				Name: "repo-a", Mode: ModeEdit, Path: "repo-a",
				Base: base, Branch: DefaultBranch(id, "repo-a"), BareRepoPath: bare,
			}}
		}
		if err := SaveManifest(path, manifest); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-warn"}); err != nil {
		t.Fatal(err)
	}
	writeMember(t, "w-1", "origin/main")
	writeMember(t, "w-2", "refs/heads/stave/w-1/repo-a")
	writeMember(t, "w-3", "refs/heads/stave/out-1/repo-a")
	writeMember(t, "out-1", "origin/main") // stays outside the saga

	var out strings.Builder
	svc.Out = &out

	if err := svc.SagaAdd(ctx, "epic-warn", "w-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "warning:") {
		t.Fatalf("unexpected warning for plain base:\n%s", out.String())
	}

	// In-saga owner but not an after-predecessor → warning.
	out.Reset()
	if err := svc.SagaAdd(ctx, "epic-warn", "w-2", nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not among its after-predecessors") {
		t.Fatalf("missing non-predecessor warning:\n%s", out.String())
	}

	// Declaring the edge silences it.
	out.Reset()
	if err := svc.SagaAdd(ctx, "epic-warn", "w-2", []string{"w-1"}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "warning:") {
		t.Fatalf("unexpected warning once edge is declared:\n%s", out.String())
	}

	// Out-of-saga owner → warning.
	out.Reset()
	if err := svc.SagaAdd(ctx, "epic-warn", "w-3", nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `outside saga epic-warn`) {
		t.Fatalf("missing out-of-saga warning:\n%s", out.String())
	}
}

func TestSagaAddConcurrentMembersAllPresent(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-race"}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	for i := 0; i < n; i++ {
		mustInitSpace(t, svc, fmt.Sprintf("rc-%d", i))
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.SagaAdd(ctx, "epic-race", fmt.Sprintf("rc-%d", i), nil, false)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("SagaAdd rc-%d error = %v", i, err)
		}
	}
	if members := loadSagaMembers(t, svc, "epic-race"); len(members) != n {
		t.Fatalf("roster = %#v, want %d members (lost update)", members, n)
	}
}

// TestCreateInSagaVsCompetingSagaAdd races `space create --saga` (Create with
// SagaID, which registers the member under the membership + per-saga locks)
// against a concurrent `saga add` of a different member. Both must land in the
// roster — a lost update means the create-side registration bypassed the locks.
func TestCreateInSagaVsCompetingSagaAdd(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-cvsa"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "cvsa-add")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = svc.Create(ctx, CreateOptions{ID: "cvsa-new", SagaID: "epic-cvsa"})
	}()
	go func() {
		defer wg.Done()
		errs[1] = svc.SagaAdd(ctx, "epic-cvsa", "cvsa-add", nil, false)
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent op %d error = %v", i, err)
		}
	}
	members := loadSagaMembers(t, svc, "epic-cvsa")
	seen := map[string]bool{}
	for _, member := range members {
		seen[member.ID] = true
	}
	if len(members) != 2 || !seen["cvsa-new"] || !seen["cvsa-add"] {
		t.Fatalf("roster = %#v, want both cvsa-new and cvsa-add (lost update)", members)
	}
}

func TestSagaAddVsAttachMemoryInterleaved(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	svc.Memory = &memory.Fake{}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-mix"}); err != nil {
		t.Fatal(err)
	}
	const n = 5
	for i := 0; i < n; i++ {
		mustInitSpace(t, svc, fmt.Sprintf("mx-%d", i))
	}
	var wg sync.WaitGroup
	errs := make([]error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.SagaAdd(ctx, "epic-mix", fmt.Sprintf("mx-%d", i), nil, false)
		}(i)
		go func(i int) {
			defer wg.Done()
			errs[n+i] = svc.AttachMemory(ctx, AttachMemoryOptions{
				SpaceID: "epic-mix",
				UseID:   fmt.Sprintf("store-%d", i),
				Name:    fmt.Sprintf("mem-%d", i),
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("interleaved op %d error = %v", i, err)
		}
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-mix"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Saga.Members) != n || len(manifest.Memories) != n {
		t.Fatalf("members = %d memories = %d, want %d each (lost update)", len(manifest.Saga.Members), len(manifest.Memories), n)
	}
}

func TestTwoSagasConcurrentAddSameMemberOneWins(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-a"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-b"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "contested-1")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, saga := range []string{"epic-a", "epic-b"} {
		wg.Add(1)
		go func(i int, saga string) {
			defer wg.Done()
			errs[i] = svc.SagaAdd(ctx, saga, "contested-1", nil, false)
		}(i, saga)
	}
	wg.Wait()
	var failures int
	for _, err := range errs {
		if err != nil {
			failures++
			if !strings.Contains(err.Error(), "already a member") {
				t.Fatalf("loser error = %v", err)
			}
		}
	}
	if failures != 1 {
		t.Fatalf("failures = %d, want exactly 1 (single-saga membership)", failures)
	}
	listed := 0
	for _, saga := range []string{"epic-a", "epic-b"} {
		for _, member := range loadSagaMembers(t, svc, saga) {
			if member.ID == "contested-1" {
				listed++
			}
		}
	}
	if listed != 1 {
		t.Fatalf("member listed by %d sagas, want exactly 1", listed)
	}
}

func TestConcurrentPlainCreateVsCreateSagaOneWinner(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	var plainErr, sagaErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		plainErr = svc.Create(ctx, CreateOptions{ID: "clash-1"})
	}()
	go func() {
		defer wg.Done()
		sagaErr = svc.CreateSaga(ctx, SagaCreateOptions{ID: "clash-1"})
	}()
	wg.Wait()

	if (plainErr == nil) == (sagaErr == nil) {
		t.Fatalf("want exactly one winner, got plainErr=%v sagaErr=%v", plainErr, sagaErr)
	}
	manifest, err := LoadManifest(svc.SpacePath("clash-1"))
	if err != nil {
		t.Fatal(err)
	}
	if plainErr == nil {
		if manifest.Saga != nil || manifest.Kind == KindSaga {
			t.Fatalf("plain create won but manifest is a saga: %+v", manifest)
		}
	} else {
		if manifest.Saga == nil || manifest.Kind != KindSaga || manifest.Version != 2 {
			t.Fatalf("saga create won but manifest is not a clean saga: %+v", manifest)
		}
	}
}

// TestSagaMembershipLockNamespacing pins the structural lock namespacing: a
// saga literally named "saga-membership" must not collide with the global
// membership lock.
func TestSagaMembershipLockNamespacing(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "saga-membership"}); err != nil {
		t.Fatalf("CreateSaga(saga-membership) error = %v", err)
	}
	mustInitSpace(t, svc, "ns-1")
	if err := svc.SagaAdd(ctx, "saga-membership", "ns-1", nil, false); err != nil {
		t.Fatalf("SagaAdd error = %v", err)
	}
	if members := loadSagaMembers(t, svc, "saga-membership"); len(members) != 1 || members[0].ID != "ns-1" {
		t.Fatalf("members = %#v", members)
	}
	for _, lock := range []string{
		filepath.Join(cfg.AgentWorkDir, ".locks", "membership.lock"),
		filepath.Join(cfg.AgentWorkDir, ".locks", "sagas", "saga-membership.lock"),
	} {
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("lock file %s missing: %v", lock, err)
		}
	}
}

func TestMutateSagaManifestConcurrent(t *testing.T) {
	svc, _, _ := testService(t)
	if err := svc.CreateSaga(context.Background(), SagaCreateOptions{ID: "epic-mut"}); err != nil {
		t.Fatal(err)
	}
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = svc.mutateSagaManifest("epic-mut", func(manifest *Manifest) error {
				manifest.Saga.Members = append(manifest.Saga.Members, SagaMember{ID: fmt.Sprintf("gm-%d", i)})
				return nil
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("mutateSagaManifest %d error = %v", i, err)
		}
	}
	if members := loadSagaMembers(t, svc, "epic-mut"); len(members) != n {
		t.Fatalf("members = %d, want %d (lost update)", len(members), n)
	}
	// The inner verify rejects non-sagas.
	mustInitSpace(t, svc, "plain-3")
	err := svc.mutateSagaManifest("plain-3", func(*Manifest) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "is not a saga") {
		t.Fatalf("mutateSagaManifest on plain space error = %v", err)
	}
}

func TestDetachMemoryOnSagaTakesLockedPath(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	svc.Memory = &memory.Fake{}
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-det", Memories: []string{"."}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DetachMemory(ctx, "epic-det", "default", memory.FateKeep, false, false); err != nil {
		t.Fatalf("DetachMemory on saga error = %v", err)
	}
	manifest, err := LoadManifest(svc.SpacePath("epic-det"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("memories = %#v, want none", manifest.Memories)
	}
	if manifest.Saga == nil || manifest.Version != 2 {
		t.Fatalf("saga manifest degraded by detach: %+v", manifest)
	}
}
