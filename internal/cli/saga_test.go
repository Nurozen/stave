package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// TestCLISagaCreateMembersListStatus drives the full saga surface end to end:
// saga create (chdir handoff + embedded skill), member creation via
// space create --saga/--after, list/status rendering of the DAG, sync, and
// the add/remove roster verbs.
func TestCLISagaCreateMembersListStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	var out string
	requestedDir := captureShellChdir(t, func() {
		out = runCLI(t, "saga", "create", "epic-1")
	})
	sagaPath := filepath.Join(home, "stave", "agent-work", "epic-1")
	if requestedDir != sagaPath {
		t.Fatalf("saga create shell chdir request = %q, want saga root %q", requestedDir, sagaPath)
	}
	if !strings.Contains(out, "created space epic-1") {
		t.Fatalf("saga create output missing created line:\n%s", out)
	}
	skill, err := os.ReadFile(filepath.Join(sagaPath, ".claude", "skills", "stave-saga", "SKILL.md"))
	if err != nil {
		t.Fatalf("embedded saga skill not installed: %v", err)
	}
	if !strings.Contains(string(skill), "name: stave-saga") {
		t.Fatalf("installed skill content unexpected:\n%.200s", skill)
	}
	manifest, err := space.LoadManifest(sagaPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != space.KindSaga || manifest.Saga == nil {
		t.Fatalf("saga manifest = %#v", manifest)
	}

	runCLI(t, "space", "create", "step-1", "--saga", "epic-1", "-e", "repo-a")
	runCLI(t, "space", "create", "step-2", "--saga", "epic-1", "--after", "step-1", "-e", "repo-a")

	list := runCLI(t, "saga", "list")
	if !strings.Contains(list, "epic-1\tsaga\tmembers: step-1,step-2") {
		t.Fatalf("saga list missing roster row:\n%s", list)
	}
	if !strings.Contains(list, "step-2\t-\tsaga: epic-1") {
		t.Fatalf("saga list missing member row:\n%s", list)
	}

	var rows []sagaListRow
	if err := json.Unmarshal([]byte(runCLI(t, "saga", "list", "--json")), &rows); err != nil {
		t.Fatalf("saga list --json is not valid JSON: %v", err)
	}
	byID := map[string]sagaListRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if row := byID["epic-1"]; !row.IsSaga || len(row.Members) != 2 || row.Members[0] != "step-1" || row.Members[1] != "step-2" {
		t.Fatalf("saga list --json epic-1 row = %#v", row)
	}
	if row := byID["step-1"]; row.IsSaga || row.MemberOf != "epic-1" {
		t.Fatalf("saga list --json step-1 row = %#v", row)
	}

	// step-2's edit stacks on step-1's persisted branch via the --after
	// default-base rule.
	memberManifest, err := space.LoadManifest(filepath.Join(home, "stave", "agent-work", "step-2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(memberManifest.Repos) != 1 || memberManifest.Repos[0].Base != "refs/heads/stave/step-1/repo-a" {
		t.Fatalf("step-2 repos = %#v", memberManifest.Repos)
	}

	status := runCLI(t, "saga", "status", "epic-1")
	for _, want := range []string{
		"saga epic-1 (2 members)",
		"step-1 [live]",
		"step-2 [live] after: step-1",
		"repo-a [edit] branch stave/step-2/repo-a",
		"base: refs/heads/stave/step-1/repo-a (ok)",
		"drift: ahead 0, behind 0",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("saga status missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "note:") {
		t.Fatalf("saga status has unexpected topology notes:\n%s", status)
	}

	sync := runCLI(t, "saga", "sync", "epic-1")
	if !strings.Contains(sync, "syncing member step-1") || !strings.Contains(sync, "syncing member step-2") {
		t.Fatalf("saga sync output:\n%s", sync)
	}

	// Roster verbs: dry-run previews, add registers, remove drops.
	runCLI(t, "space", "create", "step-3")
	dry := runCLI(t, "saga", "add", "epic-1", "step-3", "--after", "step-2", "--dry-run")
	if !strings.Contains(dry, "dry-run: add step-3 to saga epic-1") {
		t.Fatalf("saga add dry-run output:\n%s", dry)
	}
	runCLI(t, "saga", "add", "epic-1", "step-3", "--after", "step-2")
	if list := runCLI(t, "saga", "list"); !strings.Contains(list, "members: step-1,step-2,step-3") {
		t.Fatalf("saga list missing added member:\n%s", list)
	}
	dry = runCLI(t, "saga", "remove", "epic-1", "step-3", "--dry-run")
	if !strings.Contains(dry, "dry-run: remove step-3 from saga epic-1") {
		t.Fatalf("saga remove dry-run output:\n%s", dry)
	}
	runCLI(t, "saga", "remove", "epic-1", "step-3")
	if list := runCLI(t, "saga", "list"); strings.Contains(list, "step-3\t-\tsaga: epic-1") {
		t.Fatalf("saga list still shows removed member:\n%s", list)
	}
}

// TestCLISagaStatusMergedAncestryE2E drives layer-1 merge detection against
// real git end to end: a member's committed branch is merged into the source
// repo's main with a true merge commit, saga sync refetches the bare repo,
// and status reports the base merged (ancestry) — while a squash-merged
// sibling stays ok, because a squash never makes the branch an ancestor of
// its base. PRLookup stays inert throughout: the registered clone URL is a
// local path, which the CLI wiring skips without touching gh.
func TestCLISagaStatusMergedAncestryE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "saga", "create", "epic-mg")
	runCLI(t, "space", "create", "mg-1", "--saga", "epic-mg", "-e", "repo-a")
	runCLI(t, "space", "create", "mg-2", "--saga", "epic-mg", "-e", "repo-a")

	commit := func(member, file string) {
		t.Helper()
		wt := filepath.Join(home, "stave", "agent-work", member, "repo-a")
		if err := os.WriteFile(filepath.Join(wt, file), []byte(member+" work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, wt, "add", file)
		runGit(t, wt, "-c", "user.name=Test User", "-c", "user.email=test@example.test", "commit", "-m", member+" work")
	}
	commit("mg-1", "feature-1.txt")
	commit("mg-2", "feature-2.txt")

	// Land mg-1 in the source repo's main as a true merge commit, mg-2 as a
	// squash: the content lands, the ancestry never does.
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a")
	runGit(t, src, "fetch", bare, "refs/heads/stave/mg-1/repo-a")
	runGit(t, src, "merge", "--no-ff", "-m", "merge mg-1", "FETCH_HEAD")
	runGit(t, src, "fetch", bare, "refs/heads/stave/mg-2/repo-a")
	runGit(t, src, "merge", "--squash", "FETCH_HEAD")
	runGit(t, src, "commit", "-m", "squash mg-2")

	// saga sync fetches the bare repo from src and reports the merge finding.
	sync := runCLI(t, "saga", "sync", "epic-mg")
	if !strings.Contains(sync, "member mg-1 repo repo-a: base origin/main merged (ancestry)") {
		t.Fatalf("saga sync missing merge finding:\n%s", sync)
	}

	status := runCLI(t, "saga", "status", "epic-mg")
	if !strings.Contains(status, "(merged (ancestry))") {
		t.Fatalf("saga status missing merged (ancestry):\n%s", status)
	}

	statusJSON := runCLI(t, "saga", "status", "epic-mg", "--json")
	if !strings.Contains(statusJSON, `"base_health"`) || strings.Contains(statusJSON, `"BaseHealth"`) {
		t.Fatalf("saga status --json keys = %s", statusJSON)
	}
	var typed space.SagaStatus
	if err := json.Unmarshal([]byte(statusJSON), &typed); err != nil {
		t.Fatalf("saga status --json invalid: %v\n%s", err, statusJSON)
	}
	rows := map[string]space.SagaRepoStatus{}
	for _, member := range typed.Members {
		for _, repo := range member.Repos {
			rows[member.ID] = repo
		}
	}
	if r := rows["mg-1"]; r.BaseHealth != space.BaseHealthMerged || r.MergedVia != space.MergedViaAncestry {
		t.Fatalf("mg-1 row = %#v\n%s", r, statusJSON)
	}
	// Squash divergence: layer 1 alone keeps the row ok — only a PR probe
	// could call it merged.
	if r := rows["mg-2"]; r.BaseHealth != space.BaseHealthOK || r.MergedVia != "" {
		t.Fatalf("mg-2 row = %#v\n%s", r, statusJSON)
	}
}

func TestCLISagaCreateDryRunTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	out := runCLI(t, "saga", "create", "epic-dry", "--dry-run")
	if !strings.Contains(out, "dry-run: create space directory") {
		t.Fatalf("saga create dry-run output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "epic-dry")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the saga directory: %v", err)
	}
}

// TestCLISagaCreateRejectsEditFlags: -e/--edit before a literal "--" is
// refused on both sides of --summon, in every pflag spelling.
func TestCLISagaCreateRejectsEditFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"saga", "create", "epic-x", "-e", "repo-a"},
		{"saga", "create", "epic-x", "--edit", "repo-a"},
		{"saga", "create", "epic-x", "--edit=repo-a"},
		{"saga", "create", "epic-x", "-erepo-a"},
		{"saga", "create", "epic-x", "--summon", "codex", "-e", "repo-a"},
		{"saga", "create", "epic-x", "-e", "repo-a", "--summon", "codex"},
	} {
		out, err := runCLIError(t, nil, args...)
		if err == nil || !strings.Contains(err.Error(), "sagas hold no edit worktrees") {
			t.Fatalf("stave %v error = %v\n%s", args, err, out)
		}
	}
}

// TestCLISagaCreateForwardsEditAfterDashes: past the literal "--" everything
// forwards to the agent, --edit included, and the Claude session launches
// straight into the embedded saga skill.
func TestCLISagaCreateForwardsEditAfterDashes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	launcher := &fakeSummonLauncher{}
	application := &app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }}
	requestedDir := captureShellChdir(t, func() {
		runCLIWithApp(t, application, "saga", "create", "epic-2", "--summon", "claude", "--", "--edit")
	})
	sagaPath := filepath.Join(home, "stave", "agent-work", "epic-2")
	if requestedDir != sagaPath {
		t.Fatalf("saga create shell chdir request = %q, want %q", requestedDir, sagaPath)
	}
	if !launcher.called || len(launcher.invocation.Args) != 2 || launcher.invocation.Args[0] != "--edit" || launcher.invocation.Args[1] != "/stave-saga" {
		t.Fatalf("saga summon invocation = %#v", launcher.invocation)
	}
}

func TestCLISpaceCreateAndInitRejectSagaKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"space", "create", "bad-1", "-k", "saga"},
		{"space", "init", "bad-1", "--kind", "saga"},
	} {
		out, err := runCLIError(t, nil, args...)
		if err == nil || !strings.Contains(err.Error(), "stave saga create") {
			t.Fatalf("stave %v error = %v\n%s", args, err, out)
		}
	}
}

// TestCLISagaLifecycleGuard: single-space archive/destroy still refuse sagas
// and members without --force, redirecting to the saga-aware verbs
// ('stave saga archive' / 'stave saga remove'); --force overrides.
func TestCLISagaLifecycleGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "saga", "create", "epic-1")
	runCLI(t, "space", "create", "step-1", "--saga", "epic-1", "-e", "repo-a")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"space", "archive", "epic-1"}, `space "epic-1" is a saga; use 'stave saga archive epic-1' or 'stave saga destroy epic-1' to tear it down with its members, or --force to override`},
		{[]string{"space", "destroy", "epic-1"}, `space "epic-1" is a saga; use 'stave saga archive epic-1' or 'stave saga destroy epic-1' to tear it down with its members, or --force to override`},
		{[]string{"space", "archive", "step-1"}, `space "step-1" is a member of saga "epic-1"; use 'stave saga remove epic-1 step-1' to drop it from the roster first, or --force to override`},
		{[]string{"space", "destroy", "step-1"}, `space "step-1" is a member of saga "epic-1"; use 'stave saga remove epic-1 step-1' to drop it from the roster first, or --force to override`},
	} {
		out, err := runCLIError(t, nil, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("stave %v error = %v, want %q\n%s", tc.args, err, tc.want, out)
		}
	}

	// Untouched ordinary spaces stay unguarded.
	runCLI(t, "space", "create", "loner-1")
	runCLI(t, "space", "destroy", "loner-1")

	// --force overrides: the member first, then the saga itself.
	runCLI(t, "space", "destroy", "step-1", "--force")
	runCLI(t, "space", "archive", "epic-1", "--force")
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "epic-1")); !os.IsNotExist(err) {
		t.Fatalf("saga space still present after forced archive: %v", err)
	}
}

// TestCLISagaArchiveLifecycleE2E drives saga archive end to end on a 2-member
// stacked saga: a dirty member refuses fail-fast with everything intact,
// dry-run prints the ordered plan without moving anything, and the real run
// archives members in reverse topological order with the saga space last.
func TestCLISagaArchiveLifecycleE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "saga", "create", "epic-lc")
	runCLI(t, "space", "create", "lc-1", "--saga", "epic-lc", "-e", "repo-a")
	runCLI(t, "space", "create", "lc-2", "--saga", "epic-lc", "--after", "lc-1", "-e", "repo-a")
	work := filepath.Join(home, "stave", "agent-work")

	// Refusal path: a dirty member fails fast and leaves the whole saga intact.
	dirtyFile := filepath.Join(work, "lc-1", "repo-a", "wip.txt")
	if err := os.WriteFile(dirtyFile, []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLIError(t, nil, "saga", "archive", "epic-lc")
	if err == nil || !strings.Contains(err.Error(), "dirty editable worktrees") {
		t.Fatalf("saga archive with dirty member error = %v\n%s", err, out)
	}
	for _, id := range []string{"lc-1", "lc-2", "epic-lc"} {
		if _, err := os.Stat(filepath.Join(work, id)); err != nil {
			t.Fatalf("space %s not intact after refusal: %v", id, err)
		}
	}
	if err := os.Remove(dirtyFile); err != nil {
		t.Fatal(err)
	}

	// Dry-run prints the ordered plan (reverse topo, saga last), moves nothing.
	dry := runCLI(t, "saga", "archive", "epic-lc", "--dry-run")
	for _, line := range []string{
		"dry-run: saga archive plan for epic-lc",
		"dry-run: 1. archive member lc-2",
		"dry-run: 2. archive member lc-1",
		"dry-run: 3. archive saga space epic-lc",
	} {
		if !strings.Contains(dry, line) {
			t.Fatalf("dry-run plan missing %q:\n%s", line, dry)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "epic-lc")); err != nil {
		t.Fatalf("dry-run moved the saga space: %v", err)
	}

	out = runCLI(t, "saga", "archive", "epic-lc")
	// Reverse topo in the output: the dependent lc-2 before its base lc-1,
	// the saga space last.
	i2 := strings.Index(out, "archived lc-2 to ")
	i1 := strings.Index(out, "archived lc-1 to ")
	iS := strings.Index(out, "archived epic-lc to ")
	if i2 < 0 || i1 < 0 || iS < 0 || i2 > i1 || i1 > iS {
		t.Fatalf("archive order wrong (lc-2 at %d, lc-1 at %d, epic-lc at %d):\n%s", i2, i1, iS, out)
	}
	archiveRoot := filepath.Join(work, ".archive")
	for _, id := range []string{"lc-1", "lc-2", "epic-lc"} {
		if _, err := os.Stat(filepath.Join(work, id)); !os.IsNotExist(err) {
			t.Fatalf("space %s still live after saga archive: %v", id, err)
		}
		if _, err := space.LoadManifest(filepath.Join(archiveRoot, id)); err != nil {
			t.Fatalf("space %s missing a readable manifest under .archive: %v", id, err)
		}
	}
	// The archived saga keeps its roster (the record is the durable artifact).
	manifest, err := space.LoadManifest(filepath.Join(archiveRoot, "epic-lc"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Saga == nil || len(manifest.Saga.Members) != 2 {
		t.Fatalf("archived saga manifest = %#v, want intact roster", manifest.Saga)
	}
}

// TestCLISagaDestroyDenRefcountE2E: saga destroy with a den-destroying fate
// refuses while a non-member space shares the saga den; --force overrides and
// detaches through the real provider seam (a stub marmot binary on PATH).
func TestCLISagaDestroyDenRefcountE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Stub marmot: every verb answers an empty schema-1 envelope, enough for
	// the forced destroy's den-destroy call.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "marmot")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '{\"schema\":1}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runCLI(t, "setup")
	runCLI(t, "saga", "create", "epic-dn")
	runCLI(t, "space", "init", "sharer-1")

	// Record the attachments directly (as the memory CLI tests do): the saga
	// owns the den, sharer-1 holds an unowned attachment of the same store.
	work := filepath.Join(home, "stave", "agent-work")
	addMemory := func(id string, owned bool) {
		t.Helper()
		path := filepath.Join(work, id)
		manifest, err := space.LoadManifest(path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Memories = []space.MemoryManifest{{Name: "den", Provider: "marmot", ID: "den-dn", Owned: owned}}
		if err := space.SaveManifest(path, manifest); err != nil {
			t.Fatal(err)
		}
	}
	addMemory("epic-dn", true)
	addMemory("sharer-1", false)

	out, err := runCLIError(t, nil, "saga", "destroy", "epic-dn", "--memory", "destroy")
	if err == nil || !strings.Contains(err.Error(), `space "sharer-1" shares the saga den "den-dn"`) {
		t.Fatalf("saga destroy with shared den error = %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "epic-dn")); err != nil {
		t.Fatalf("saga torn down despite den-refcount refusal: %v", err)
	}

	runCLI(t, "saga", "destroy", "epic-dn", "--memory", "destroy", "--force")
	if _, err := os.Stat(filepath.Join(work, "epic-dn")); !os.IsNotExist(err) {
		t.Fatalf("saga space still present after forced destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "sharer-1")); err != nil {
		t.Fatalf("sharer space must remain untouched: %v", err)
	}
}

// TestCLISagaArchiveMemoryDestroyRejected mirrors the space-archive rejection:
// --memory destroy on saga archive redirects to saga destroy without touching
// anything.
func TestCLISagaArchiveMemoryDestroyRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "saga", "create", "epic-mf")
	out, err := runCLIError(t, nil, "saga", "archive", "epic-mf", "--memory", "destroy")
	if err == nil || !strings.Contains(err.Error(), "stave saga destroy --memory destroy") {
		t.Fatalf("saga archive --memory destroy error = %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "epic-mf")); err != nil {
		t.Fatalf("saga space must remain: %v", err)
	}
}

// TestCLIPortalInitSagaNote pins the interim portal-on-saga stance: portal
// init on a saga space prints the sibling-members note (members live outside
// the portal's mounts), and a plain space stays silent.
func TestCLIPortalInitSagaNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "saga", "create", "epic-note")
	runCLI(t, "space", "init", "plain-note")

	const note = "note: members are sibling spaces; this portal covers only the saga directory"
	sagaOut := runCLI(t, "portal", "init", "container", "epic-note", "--image", "ubuntu:latest")
	if !strings.Contains(sagaOut, note) {
		t.Fatalf("portal init on saga missing note:\n%s", sagaOut)
	}
	plainOut := runCLI(t, "portal", "init", "container", "plain-note", "--image", "ubuntu:latest")
	if strings.Contains(plainOut, note) {
		t.Fatalf("portal init on plain space must not print saga note:\n%s", plainOut)
	}
}
