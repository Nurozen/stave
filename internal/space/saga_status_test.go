package space

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/gh"
)

// writeStatusMember writes a live member fixture: a manifest with one edit
// repo-a on the given base plus its worktree directory.
func writeStatusMember(t *testing.T, svc Service, id, base string) {
	t.Helper()
	path := svc.SpacePath(id)
	if err := os.MkdirAll(filepath.Join(path, "repo-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: id, CreatedAt: svc.now(), Repos: []RepoManifest{{
		Name: "repo-a", Mode: ModeEdit, Path: "repo-a", Base: base,
		Branch: DefaultBranch(id, "repo-a"), BareRepoPath: svc.Config.Repos["repo-a"].BareRepoPath,
	}}}
	if err := SaveManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
}

// repoRows indexes a status result: member id → repo name → row.
func repoRows(status SagaStatus) map[string]map[string]SagaRepoStatus {
	rows := map[string]map[string]SagaRepoStatus{}
	for _, member := range status.Members {
		rows[member.ID] = map[string]SagaRepoStatus{}
		for _, repo := range member.Repos {
			rows[member.ID][repo.Name] = repo
		}
	}
	return rows
}

func TestCanonicalBaseRef(t *testing.T) {
	tests := map[string]string{
		"":                       "",
		"origin/main":            "refs/remotes/origin/main",
		"origin/stave/x/repo":    "refs/remotes/origin/stave/x/repo",
		"main":                   "refs/heads/main",
		"stave/x/repo":           "refs/heads/stave/x/repo",
		"refs/heads/main":        "refs/heads/main",
		"refs/remotes/origin/m":  "refs/remotes/origin/m",
		"refs/tags/v1":           "refs/tags/v1",
		" origin/main ":          "refs/remotes/origin/main",
		"refs/heads/stave/a/b-c": "refs/heads/stave/a/b-c",
	}
	for raw, want := range tests {
		if got := canonicalBaseRef(raw); got != want {
			t.Errorf("canonicalBaseRef(%q) = %q, want %q", raw, got, want)
		}
	}
	// gh baseRefName values canonicalize via the origin/ spelling.
	if got := canonicalBaseRef("origin/" + "release-1"); got != "refs/remotes/origin/release-1" {
		t.Errorf("gh baseRefName canonicalization = %q", got)
	}
}

func TestSagaStatusBasic(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-st"}); err != nil {
		t.Fatal(err)
	}
	writeStatusMember(t, svc, "st-1", "origin/main")
	writeStatusMember(t, svc, "st-2", "refs/heads/stave/st-1/repo-a")
	writeStatusMember(t, svc, "st-3", "refs/heads/stave/st-1/repo-a")
	// Roster order st-2, st-3, st-1; st-2 gains an after-edge on the LATER
	// roster entry st-1 so topo order must reorder.
	for _, id := range []string{"st-2", "st-3", "st-1"} {
		if err := svc.SagaAdd(ctx, "epic-st", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SagaAdd(ctx, "epic-st", "st-2", []string{"st-1"}, false); err != nil {
		t.Fatal(err)
	}
	// A roster entry whose directory never existed resolves as missing.
	if err := svc.mutateSagaManifest("epic-st", func(m *Manifest) error {
		m.Saga.Members = append(m.Saga.Members, SagaMember{ID: "st-gone"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fg.refExists = true
	fg.ahead, fg.behind = 3, 1
	fg.dirty = map[string]bool{filepath.Join(svc.SpacePath("st-2"), "repo-a"): true}

	status, err := svc.SagaStatus(ctx, "epic-st")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	if status.SagaID != "epic-st" {
		t.Fatalf("SagaID = %q", status.SagaID)
	}
	var order []string
	rows := map[string]SagaMemberStatus{}
	for _, member := range status.Members {
		order = append(order, member.ID)
		rows[member.ID] = member
	}
	// Topo: st-3, st-1 and st-gone are unblocked in roster order; st-2 lands
	// after its predecessor st-1.
	want := []string{"st-3", "st-1", "st-gone", "st-2"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("member order = %v, want %v", order, want)
	}

	st1 := rows["st-1"]
	if st1.State != MemberLive || len(st1.Repos) != 1 || st1.Dirty {
		t.Fatalf("st-1 row = %#v", st1)
	}
	if r := st1.Repos[0]; r.BaseHealth != BaseHealthOK || r.Ahead != 3 || r.Behind != 1 || r.Branch != "stave/st-1/repo-a" {
		t.Fatalf("st-1 repo = %#v", r)
	}
	st2 := rows["st-2"]
	if !st2.Dirty {
		t.Fatalf("st-2 dirty not aggregated to the member row: %#v", st2)
	}
	if r := st2.Repos[0]; r.BaseHealth != BaseHealthOK || r.Note != "" {
		t.Fatalf("st-2 repo = %#v (base owner is a predecessor; expected ok, no note)", r)
	}
	st3 := rows["st-3"]
	if r := st3.Repos[0]; !strings.Contains(r.Note, "not among its after-predecessors") {
		t.Fatalf("st-3 repo note = %q", r.Note)
	}
	if gone := rows["st-gone"]; gone.State != MemberMissing || len(gone.Repos) != 0 || gone.Dirty {
		t.Fatalf("st-gone row = %#v", gone)
	}
	if len(status.Notes) != 0 {
		t.Fatalf("status notes = %#v, want none", status.Notes)
	}

	// A base whose canonicalized ref is gone reports missing — every
	// spelling, origin/ included, now goes through RefExists.
	fg.refExists = false
	fg.calls = nil
	status, err = svc.SagaStatus(ctx, "epic-st")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range status.Members {
		for _, repo := range member.Repos {
			if repo.BaseHealth != BaseHealthMissing {
				t.Fatalf("%s base health = %q, want missing", member.ID, repo.BaseHealth)
			}
		}
	}
	if !containsCallPrefix(fg.calls, "ref-exists|"+svc.Config.Repos["repo-a"].BareRepoPath+"|refs/remotes/origin/main") {
		t.Fatalf("origin/main base was not checked via its canonical remote ref: %#v", fg.calls)
	}

	// Drift degrades to a repo note on AheadBehind failure.
	fg.refExists = true
	fg.driftErr = os.ErrInvalid
	status, err = svc.SagaStatus(ctx, "epic-st")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range status.Members {
		if member.ID == "st-1" && !strings.Contains(member.Repos[0].Note, "drift unknown") {
			t.Fatalf("st-1 drift failure not noted: %#v", member.Repos[0])
		}
	}

	// Non-sagas are refused.
	mustInitSpace(t, svc, "plain-st")
	if _, err := svc.SagaStatus(ctx, "plain-st"); err == nil || !strings.Contains(err.Error(), "is not a saga") {
		t.Fatalf("SagaStatus(plain) error = %v", err)
	}
}

// TestSagaStatusVerdictMatrix drives every BaseHealth verdict through fakeGit:
// ancestry-merged own branch, ancestry-merged stacked base, archived owner,
// corrupt owner, missing ref, unmatched local base, and the fresh-branch
// (tip == target) guard.
func TestSagaStatusVerdictMatrix(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-v"}); err != nil {
		t.Fatal(err)
	}
	members := map[string]string{
		"m-anc":   "origin/main",                    // own branch merged into base by ancestry
		"m-stack": "refs/heads/stave/m-anc/repo-a",  // stacked base merged into the owner's target
		"m-arch":  "refs/heads/stave/gone-x/repo-a", // owner archived/destroyed, ref persists
		"m-corr":  "refs/heads/stave/bad-x/repo-a",  // owner manifest corrupt
		"m-miss":  "origin/feature-z",               // canonical ref absent
		"m-loc":   "refs/heads/feature-local",       // unmatched local base
		"m-fresh": "origin/dev",                     // tip == target: NOT merged
	}
	for _, id := range []string{"m-anc", "m-stack", "m-arch", "m-corr", "m-miss", "m-loc", "m-fresh"} {
		writeStatusMember(t, svc, id, members[id])
		if err := svc.SagaAdd(ctx, "epic-v", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	// The corrupt owner is written AFTER the adds (SagaAdd fails closed on
	// unreadable siblings while scanning membership).
	if err := os.MkdirAll(svc.SpacePath("bad-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.SpacePath("bad-x"), ManifestName), []byte("\t- not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	fg.refExistsFn = func(bare, fullRef string) (bool, error) {
		return fullRef != "refs/remotes/origin/feature-z", nil
	}
	fg.ancestorFn = func(bare, ancestor, descendant string) (bool, error) {
		// origin/dev never diverged from m-fresh's branch: ancestors both ways.
		if ancestor == "refs/remotes/origin/dev" || descendant == "refs/remotes/origin/dev" {
			return true, nil
		}
		// m-anc's branch landed in origin/main (strictly: not vice versa).
		return ancestor == "refs/heads/stave/m-anc/repo-a" && descendant == "refs/remotes/origin/main", nil
	}

	status, err := svc.SagaStatus(ctx, "epic-v")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	rows := repoRows(status)
	assert := func(id, health, via, noteSub string) {
		t.Helper()
		row, ok := rows[id]["repo-a"]
		if !ok {
			t.Fatalf("%s has no repo-a row", id)
		}
		if row.BaseHealth != health || row.MergedVia != via {
			t.Fatalf("%s = health %q via %q, want %q via %q (note %q)", id, row.BaseHealth, row.MergedVia, health, via, row.Note)
		}
		if noteSub != "" && !strings.Contains(row.Note, noteSub) {
			t.Fatalf("%s note = %q, want substring %q", id, row.Note, noteSub)
		}
	}
	assert("m-anc", BaseHealthMerged, MergedViaAncestry, "")
	assert("m-stack", BaseHealthMerged, MergedViaAncestry, "")
	assert("m-arch", BaseHealthOwnerArchived, "", "archived or destroyed")
	assert("m-corr", BaseHealthUnknown, "", "unreadable manifest")
	assert("m-miss", BaseHealthMissing, "", "")
	assert("m-loc", BaseHealthUnknown, "", "matches no space's edit branch")
	assert("m-fresh", BaseHealthOK, "", "")
}

// TestSagaStatusPRLookupOverridesAndSquash covers layer 2: PR identity rows,
// the live-baseRefName target override for retargeted stacked PRs, the
// merged-via-pr verdict for squash merges, and the squash suggestion note.
func TestSagaStatusPRLookupOverridesAndSquash(t *testing.T) {
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pr"}); err != nil {
		t.Fatal(err)
	}
	for id, base := range map[string]string{
		"own-1": "origin/main",
		"sq-1":  "origin/main",
	} {
		writeStatusMember(t, svc, id, base)
		if err := svc.SagaAdd(ctx, "epic-pr", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	writeStatusMember(t, svc, "dep-1", "refs/heads/stave/own-1/repo-a")
	if err := svc.SagaAdd(ctx, "epic-pr", "dep-1", []string{"own-1"}, false); err != nil {
		t.Fatal(err)
	}
	var lookups []string
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		lookups = append(lookups, cloneURL+" "+headBranch)
		switch headBranch {
		case "stave/own-1/repo-a":
			// Live, retargeted PR: overrides own-1's recorded origin/main target.
			return []gh.PR{{Number: 7, State: "OPEN", BaseRefName: "release-1"}}, nil
		case "stave/sq-1/repo-a":
			return []gh.PR{{Number: 9, State: "MERGED", MergedAt: "2026-06-01T00:00:00Z", BaseRefName: "main"}}, nil
		}
		return nil, nil
	}
	fg.refExists = true
	fg.ancestorFn = func(bare, ancestor, descendant string) (bool, error) {
		// Only own-1's branch landed in the OVERRIDDEN target release-1;
		// nothing is an ancestor of the recorded origin/main.
		return ancestor == "refs/heads/stave/own-1/repo-a" && descendant == "refs/remotes/origin/release-1", nil
	}

	status, err := svc.SagaStatus(ctx, "epic-pr")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	rows := repoRows(status)
	if r := rows["dep-1"]["repo-a"]; r.BaseHealth != BaseHealthMerged || r.MergedVia != MergedViaAncestry {
		t.Fatalf("dep-1 = %#v, want merged via ancestry against the overridden target", r)
	}
	if !containsCallPrefix(fg.calls, "is-ancestor|"+cfg.Repos["repo-a"].BareRepoPath+"|refs/heads/stave/own-1/repo-a|refs/remotes/origin/release-1") {
		t.Fatalf("ancestry did not use the gh-overridden target:\n%#v", fg.calls)
	}
	if r := rows["own-1"]["repo-a"]; r.BaseHealth != BaseHealthOK {
		t.Fatalf("own-1 = %#v, want ok (open PR, not an ancestor)", r)
	}
	if r := rows["sq-1"]["repo-a"]; r.BaseHealth != BaseHealthMerged || r.MergedVia != MergedViaPR {
		t.Fatalf("sq-1 = %#v, want merged via pr (squash)", r)
	}
	var members = map[string]SagaMemberStatus{}
	for _, member := range status.Members {
		members[member.ID] = member
	}
	if prs := members["own-1"].PRs; len(prs) != 1 || prs[0] != (SagaPRStatus{Repo: "repo-a", Number: 7, State: "OPEN", BaseRefName: "release-1"}) {
		t.Fatalf("own-1 PRs = %#v", prs)
	}
	if prs := members["sq-1"].PRs; len(prs) != 1 || prs[0].MergedAt != "2026-06-01T00:00:00Z" {
		t.Fatalf("sq-1 PRs = %#v", prs)
	}
	var suggestions []SagaNote
	for _, note := range status.Notes {
		if note.Kind == NoteKindSuggestion {
			suggestions = append(suggestions, note)
		}
		if note.Kind == NoteKindDegraded {
			t.Fatalf("unexpected degraded note: %#v", note)
		}
	}
	if len(suggestions) != 1 || suggestions[0].Member != "sq-1" || !strings.Contains(suggestions[0].Text, "squash") {
		t.Fatalf("squash suggestions = %#v", suggestions)
	}
	// Lookups are cached per clone-URL+branch: own-1's branch serves both its
	// own row and dep-1's stacked base with ONE call.
	if len(lookups) != 3 {
		t.Fatalf("PR lookups = %v, want 3 (one per unique branch)", lookups)
	}
}

// TestSagaStatusStackedTargetIgnoresClosedPRs: only an OPEN PR on a stacked
// base overrides the recorded merge target — the head-branch listing runs with
// --state all, so a CLOSED/abandoned PR (newest first) must not retarget the
// ancestry check.
func TestSagaStatusStackedTargetIgnoresClosedPRs(t *testing.T) {
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-cl"}); err != nil {
		t.Fatal(err)
	}
	writeStatusMember(t, svc, "own-c", "origin/main")
	if err := svc.SagaAdd(ctx, "epic-cl", "own-c", nil, false); err != nil {
		t.Fatal(err)
	}
	writeStatusMember(t, svc, "dep-c", "refs/heads/stave/own-c/repo-a")
	if err := svc.SagaAdd(ctx, "epic-cl", "dep-c", []string{"own-c"}, false); err != nil {
		t.Fatal(err)
	}
	prs := []gh.PR{{Number: 11, State: "CLOSED", BaseRefName: "abandoned"}}
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		if headBranch == "stave/own-c/repo-a" {
			return prs, nil
		}
		return nil, nil
	}
	fg.refExists = true
	bare := cfg.Repos["repo-a"].BareRepoPath

	// CLOSED only: the recorded target origin/main stays.
	status, err := svc.SagaStatus(ctx, "epic-cl")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	if containsCallPrefix(fg.calls, "is-ancestor|"+bare+"|refs/heads/stave/own-c/repo-a|refs/remotes/origin/abandoned") {
		t.Fatalf("closed PR retargeted the ancestry check:\n%#v", fg.calls)
	}
	if !containsCallPrefix(fg.calls, "is-ancestor|"+bare+"|refs/heads/stave/own-c/repo-a|refs/remotes/origin/main") {
		t.Fatalf("recorded target not checked:\n%#v", fg.calls)
	}
	if r := repoRows(status)["dep-c"]["repo-a"]; r.BaseHealth != BaseHealthOK {
		t.Fatalf("dep-c = %#v, want ok against the recorded target", r)
	}

	// A CLOSED PR ahead of an OPEN one: the first OPEN PR's base wins.
	prs = append(prs, gh.PR{Number: 12, State: "OPEN", BaseRefName: "release-2"})
	fg.calls = nil
	if _, err := svc.SagaStatus(ctx, "epic-cl"); err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	if !containsCallPrefix(fg.calls, "is-ancestor|"+bare+"|refs/heads/stave/own-c/repo-a|refs/remotes/origin/release-2") {
		t.Fatalf("open PR did not override the target:\n%#v", fg.calls)
	}
	if containsCallPrefix(fg.calls, "is-ancestor|"+bare+"|refs/heads/stave/own-c/repo-a|refs/remotes/origin/abandoned") {
		t.Fatalf("closed PR still retargeted the ancestry check:\n%#v", fg.calls)
	}
}

// TestSagaStatusSiblingScanFailureDegrades: a failing sibling scan must not
// mislabel a LIVE stacked-base owner as archived — stacked detection
// short-circuits to unknown with one degraded note.
func TestSagaStatusSiblingScanFailureDegrades(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit fixture")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-sf"}); err != nil {
		t.Fatal(err)
	}
	writeStatusMember(t, svc, "sf-owner", "origin/main")
	writeStatusMember(t, svc, "sf-dep", "refs/heads/stave/sf-owner/repo-a")
	writeStatusMember(t, svc, "sf-dep2", "refs/heads/stave/sf-owner/repo-a")
	for _, id := range []string{"sf-owner", "sf-dep", "sf-dep2"} {
		if err := svc.SagaAdd(ctx, "epic-sf", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	fg.refExists = true
	// Execute-only work dir: ListSpaces' ReadDir fails while the manifests
	// inside the space directories stay reachable.
	if err := os.Chmod(cfg.AgentWorkDir, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg.AgentWorkDir, 0o755) })

	status, err := svc.SagaStatus(ctx, "epic-sf")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	r := repoRows(status)["sf-dep"]["repo-a"]
	if r.BaseHealth != BaseHealthUnknown || !strings.Contains(r.Note, "sibling scan failed") {
		t.Fatalf("sf-dep = %#v, want unknown with a sibling-scan note (owner is LIVE, not archived)", r)
	}
	degraded := 0
	for _, note := range status.Notes {
		if note.Kind == NoteKindDegraded && strings.Contains(note.Text, "sibling scan failed") {
			degraded++
		}
	}
	if degraded != 1 {
		t.Fatalf("notes = %#v, want exactly one sibling-scan degraded note", status.Notes)
	}
}

// TestSagaStatusPRLookupDegrades: a failing PRLookup yields ONE aggregated
// degraded note and ancestry-only verdicts — never an error.
func TestSagaStatusPRLookupDegrades(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dg"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dg-1", "dg-2"} {
		writeStatusMember(t, svc, id, "origin/main")
		if err := svc.SagaAdd(ctx, "epic-dg", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		return nil, errors.New("gh CLI not found in PATH")
	}
	fg.refExists = true
	status, err := svc.SagaStatus(ctx, "epic-dg")
	if err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	var degraded []SagaNote
	for _, note := range status.Notes {
		if note.Kind == NoteKindDegraded {
			degraded = append(degraded, note)
		}
	}
	if len(degraded) != 1 || !strings.Contains(degraded[0].Text, "gh CLI not found") {
		t.Fatalf("degraded notes = %#v, want exactly one aggregated note", degraded)
	}
	rows := repoRows(status)
	for _, id := range []string{"dg-1", "dg-2"} {
		if r := rows[id]["repo-a"]; r.BaseHealth != BaseHealthOK {
			t.Fatalf("%s = %#v, want ancestry-only ok", id, r)
		}
	}
}

// TestSagaStatusZeroWrites: status is a pure reader — no manifest in the work
// dir may change bytes or mtime, PR layer active or not.
func TestSagaStatusZeroWrites(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-zw"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"zw-1", "zw-2"} {
		writeStatusMember(t, svc, id, "origin/main")
		if err := svc.SagaAdd(ctx, "epic-zw", id, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		return []gh.PR{{Number: 4, State: "MERGED", MergedAt: "2026-06-02T00:00:00Z", BaseRefName: "main"}}, nil
	}
	fg.refExists = true
	type snapshot struct {
		data    string
		modTime time.Time
	}
	manifests := func() map[string]snapshot {
		snaps := map[string]snapshot{}
		err := filepath.WalkDir(svc.Config.AgentWorkDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || entry.Name() != ManifestName {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			snaps[path] = snapshot{data: string(data), modTime: info.ModTime()}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return snaps
	}
	before := manifests()
	if _, err := svc.SagaStatus(ctx, "epic-zw"); err != nil {
		t.Fatalf("SagaStatus() error = %v", err)
	}
	if after := manifests(); !reflect.DeepEqual(before, after) {
		t.Fatalf("SagaStatus wrote manifests:\nbefore %#v\nafter  %#v", before, after)
	}
}

// TestSagaStatusJSONContract pins the frozen --json shape: snake_case tags,
// omitempty behavior for non-live members, and a lossless round-trip.
func TestSagaStatusJSONContract(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-js"}); err != nil {
		t.Fatal(err)
	}
	writeStatusMember(t, svc, "js-1", "origin/main")
	if err := svc.SagaAdd(ctx, "epic-js", "js-1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.mutateSagaManifest("epic-js", func(m *Manifest) error {
		m.Saga.Members = append(m.Saga.Members, SagaMember{ID: "js-gone", After: []string{"js-1"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fg.refExists = true
	status, err := svc.SagaStatus(ctx, "epic-js")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"saga_id"`, `"members"`, `"base_health"`, `"dirty"`, `"ahead"`, `"behind"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("marshaled status missing %s:\n%s", key, data)
		}
	}
	// Field-level omitempty: the non-live member row has no repos/error/prs
	// keys, keeps state and dirty:false, and keeps its after edges.
	var generic struct {
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	var gone map[string]any
	for _, member := range generic.Members {
		if member["id"] == "js-gone" {
			gone = member
		}
	}
	if gone == nil {
		t.Fatalf("js-gone row absent:\n%s", data)
	}
	for _, key := range []string{"repos", "error", "prs", "merged_via", "note"} {
		if _, present := gone[key]; present {
			t.Fatalf("non-live member row carries %q:\n%s", key, data)
		}
	}
	if gone["state"] != string(MemberMissing) || gone["dirty"] != false || !reflect.DeepEqual(gone["after"], []any{"js-1"}) {
		t.Fatalf("js-gone row = %#v", gone)
	}
	// Round-trip: unmarshal back into the typed struct losslessly.
	var restored SagaStatus
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status, restored) {
		t.Fatalf("round-trip mismatch:\nhave %#v\nwant %#v", restored, status)
	}
}

// TestSagaSyncReportsMergeFindings: sync runs the status merge detection over
// the freshly fetched refs, prints human summaries, and persists nothing.
func TestSagaSyncReportsMergeFindings(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-ms"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "ms-1", SagaID: "epic-ms", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	fg.refExists = true
	fg.ancestorFn = func(bare, ancestor, descendant string) (bool, error) {
		// ms-1's branch landed in origin/main; strictly behind it.
		return ancestor == "refs/heads/stave/ms-1/repo-a" && descendant == "refs/remotes/origin/main", nil
	}
	sagaManifestPath := filepath.Join(svc.SpacePath("ms-1"), ManifestName)
	beforeBytes, err := os.ReadFile(sagaManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		return nil, errors.New("gh exploded")
	}
	if err := svc.SagaSync(ctx, "epic-ms", SagaSyncOptions{}); err != nil {
		t.Fatalf("SagaSync() error = %v", err)
	}
	if !strings.Contains(out.String(), "member ms-1 repo repo-a: base origin/main merged (ancestry)") {
		t.Fatalf("sync missing merge summary:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PR lookup unavailable") {
		t.Fatalf("sync missing degrade note:\n%s", out.String())
	}
	afterBytes, err := os.ReadFile(sagaManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Fatalf("sync persisted detection results into the member manifest:\n%s", afterBytes)
	}
}

// TestSagaSyncCachesPRIdentityOnce: sync persists newly discovered PR
// IDENTITY (repo + number only) into the saga roster, exactly once — a second
// sync with the same PRs writes nothing (bytes AND mtime), and later PRs
// append without duplicating the cached ones.
func TestSagaSyncCachesPRIdentityOnce(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pc"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "pc-1", SagaID: "epic-pc", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	fg.refExists = true
	prs := []gh.PR{{Number: 7, State: "OPEN", BaseRefName: "main"}}
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		if headBranch == "stave/pc-1/repo-a" {
			return prs, nil
		}
		return nil, nil
	}
	if err := svc.SagaSync(ctx, "epic-pc", SagaSyncOptions{}); err != nil {
		t.Fatalf("SagaSync() error = %v", err)
	}
	members := loadSagaMembers(t, svc, "epic-pc")
	if len(members) != 1 || !reflect.DeepEqual(members[0].PRs, []SagaPR{{Repo: "repo-a", Number: 7}}) {
		t.Fatalf("cached PRs = %#v, want exactly [{repo-a 7}]", members)
	}
	// Identity ONLY: live PR state must never reach the manifest.
	manifestPath := filepath.Join(svc.SpacePath("epic-pc"), ManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"OPEN", "state", "mergedAt", "baseRefName"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("manifest persisted live PR state (%q):\n%s", banned, raw)
		}
	}
	// Second sync with the same PRs is a no-op: the manifest is not rewritten.
	past := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(manifestPath, past, past); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaSync(ctx, "epic-pc", SagaSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Fatal("second sync rewrote the saga manifest despite no new PR identities")
	}
	if after, err := os.ReadFile(manifestPath); err != nil || string(after) != string(raw) {
		t.Fatalf("second sync changed manifest bytes (err %v):\n%s", err, after)
	}
	// A later PR appends; the cached identity is not duplicated.
	prs = append(prs, gh.PR{Number: 9, State: "MERGED", MergedAt: "2026-06-03T00:00:00Z", BaseRefName: "main"})
	if err := svc.SagaSync(ctx, "epic-pc", SagaSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	members = loadSagaMembers(t, svc, "epic-pc")
	want := []SagaPR{{Repo: "repo-a", Number: 7}, {Repo: "repo-a", Number: 9}}
	if !reflect.DeepEqual(members[0].PRs, want) {
		t.Fatalf("cached PRs after new PR = %#v, want %#v", members[0].PRs, want)
	}
}

// TestSagaSyncDryRunWritesNoGeneratedFiles: dry-run sync must not write the
// generated files — member and saga AGENTS.md stay byte- and mtime-identical,
// and a missing CLAUDE.md link is not repaired (on top of the PR-identity
// cache suppression pinned elsewhere).
func TestSagaSyncDryRunWritesNoGeneratedFiles(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dsr"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "dsr-1", SagaID: "epic-dsr", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	past := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	agents := []string{
		filepath.Join(svc.SpacePath("dsr-1"), AgentsName),
		filepath.Join(svc.SpacePath("epic-dsr"), AgentsName),
	}
	before := map[string][]byte{}
	for _, path := range agents {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	memberLink := filepath.Join(svc.SpacePath("dsr-1"), ClaudeName)
	if err := os.Remove(memberLink); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaSync(ctx, "epic-dsr", SagaSyncOptions{DryRun: true}); err != nil {
		t.Fatalf("SagaSync(dry-run) error = %v", err)
	}
	for _, path := range agents {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Fatalf("dry-run rewrote %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(before[path]) {
			t.Fatalf("dry-run changed %s bytes", path)
		}
	}
	if _, err := os.Lstat(memberLink); !os.IsNotExist(err) {
		t.Fatalf("dry-run repaired the %s link (err %v)", ClaudeName, err)
	}
}

// TestSagaSyncMergeDetectionDegradeNotice: a sibling-scan failure AFTER the
// per-space syncs degrades to a printed notice instead of silently skipping
// merge detection (the sync itself already succeeded).
func TestSagaSyncMergeDetectionDegradeNotice(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based failure injection needs a non-root POSIX user")
	}
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dg"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "dg-1", SagaID: "epic-dg", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	// Execute-only work dir: per-space paths still traverse, ReadDir fails.
	if err := os.Chmod(cfg.AgentWorkDir, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg.AgentWorkDir, 0o755) })
	var out strings.Builder
	svc.Out = &out
	if err := svc.SagaSync(ctx, "epic-dg", SagaSyncOptions{}); err != nil {
		t.Fatalf("SagaSync(unlistable work dir) error = %v", err)
	}
	if !strings.Contains(out.String(), "notice: merge detection skipped:") {
		t.Fatalf("missing degrade notice:\n%s", out.String())
	}
}

func TestSagaSyncFetchDedupAndStateHandling(t *testing.T) {
	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-sy", References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "sy-1", SagaID: "epic-sy", Edits: []RepoSpec{{Name: "repo-a"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Create(ctx, CreateOptions{ID: "sy-2", SagaID: "epic-sy", Edits: []RepoSpec{{Name: "repo-a"}}, References: []RepoSpec{{Name: "repo-b"}}}); err != nil {
		t.Fatal(err)
	}
	// Roster entry with no directory: skipped with a note.
	if err := svc.mutateSagaManifest("epic-sy", func(m *Manifest) error {
		m.Saga.Members = append(m.Saga.Members, SagaMember{ID: "sy-gone"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	fg.calls = nil
	if err := svc.SagaSync(ctx, "epic-sy", SagaSyncOptions{}); err != nil {
		t.Fatalf("SagaSync() error = %v", err)
	}
	fetches := 0
	for _, call := range fg.calls {
		if strings.HasPrefix(call, "fetch|") {
			fetches++
		}
	}
	// repo-a shared by sy-1 and sy-2, repo-b shared by the saga and sy-2:
	// exactly one fetch per unique bare repo.
	if fetches != 2 {
		t.Fatalf("fetch calls = %d, want 2 unique bares:\n%#v", fetches, fg.calls)
	}
	if !strings.Contains(out.String(), "skipping member sy-gone: missing") {
		t.Fatalf("missing skip note:\n%s", out.String())
	}
	if !containsCallPrefix(fg.calls, "ahead-behind|") || !containsCallPrefix(fg.calls, "checkout-detached|") {
		t.Fatalf("member/saga syncs did not run: %#v", fg.calls)
	}

	// The reference skip-when-dirty message survives the SkipFetch path.
	out.Reset()
	fg.calls = nil
	fg.dirty = map[string]bool{filepath.Join(svc.SpacePath("epic-sy"), "references", "repo-b"): true}
	if err := svc.SagaSync(ctx, "epic-sy", SagaSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reference repo-b is dirty; skipped checkout") {
		t.Fatalf("missing dirty reference message:\n%s", out.String())
	}

	// Archived member: skipped with the archive path in the note.
	fg.dirty = nil
	if err := svc.Archive(ctx, ArchiveOptions{SpaceID: "sy-1", Force: true}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := svc.SagaSync(ctx, "epic-sy", SagaSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipping member sy-1: archived at") {
		t.Fatalf("missing archived skip note:\n%s", out.String())
	}

	// Corrupt member fails closed BEFORE any fetch.
	if err := os.WriteFile(filepath.Join(svc.SpacePath("sy-2"), ManifestName), []byte("\t- not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	err := svc.SagaSync(ctx, "epic-sy", SagaSyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "corrupt manifest") {
		t.Fatalf("SagaSync(corrupt member) error = %v", err)
	}
	if len(fg.calls) != 0 {
		t.Fatalf("corrupt member did not fail before git: %#v", fg.calls)
	}
}
