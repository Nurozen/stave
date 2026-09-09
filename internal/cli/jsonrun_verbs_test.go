package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

func rowsOf(t *testing.T, payload map[string]any, key string) []map[string]any {
	t.Helper()
	items, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("payload has no %q array: %v", key, payload)
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		rows = append(rows, row)
	}
	return rows
}

func TestCLIJSONSpaceSync(t *testing.T) {
	_, spacePath, _, _ := lifecycleFixture(t, "sy-1")

	synced := decodeJSONObject(t, runCLI(t, "space", "sync", "sy-1", "--json"))
	if synced["spaceId"] != "sy-1" || synced["spacePath"] != spacePath || manifestOf(t, synced)["id"] != "sy-1" {
		t.Fatalf("sync payload = %v", synced)
	}
	rows := rowsOf(t, synced, "repos")
	if len(rows) != 2 {
		t.Fatalf("sync rows = %v", rows)
	}
	// Manifest order: references first, then edit repos.
	if rows[0]["name"] != "repo-b" || rows[0]["mode"] != "reference" || rows[0]["action"] != space.SyncActionUpdated || rows[0]["ahead"] != float64(0) {
		t.Fatalf("reference row = %v", rows[0])
	}
	if rows[1]["name"] != "repo-a" || rows[1]["mode"] != "edit" || rows[1]["action"] != space.SyncActionDriftReported || rows[1]["ahead"] != float64(0) || rows[1]["behind"] != float64(0) {
		t.Fatalf("edit row = %v", rows[1])
	}
	if _, hasNotes := synced["notes"]; hasNotes {
		t.Fatalf("sync emitted notes on a clean run: %v", synced["notes"])
	}

	// A dirty reference is skipped with a note; --references-only drops the edit row.
	if err := os.WriteFile(filepath.Join(spacePath, "references", "repo-b", "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := decodeJSONObject(t, runCLI(t, "space", "sync", "sy-1", "--references-only", "--json"))
	rows = rowsOf(t, refs, "repos")
	if len(rows) != 1 || rows[0]["name"] != "repo-b" || rows[0]["action"] != space.SyncActionSkipped || !strings.Contains(rows[0]["note"].(string), "dirty") {
		t.Fatalf("references-only rows = %v", rows)
	}
	if _, hasNotes := refs["notes"]; hasNotes {
		t.Fatalf("skipped row leaked into notes: %v", refs["notes"])
	}

	// Human output is unchanged without the flag.
	human := runCLI(t, "space", "sync", "sy-1")
	if !strings.Contains(human, "edit repo-a: ahead 0, behind 0 versus origin/main") || !strings.Contains(human, "reference repo-b is dirty; skipped checkout") || strings.Contains(human, "{") {
		t.Fatalf("human sync output = %q", human)
	}

	if code, _ := jsonErrorCode(t, "space", "sync", "sy-missing", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("sync missing space = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "sync", "bad/id", "--json"); code != space.CodeInvalidName {
		t.Fatalf("sync bad id = %s", code)
	}
}

func TestCLIJSONSpaceRetarget(t *testing.T) {
	_, spacePath, _, _ := lifecycleFixture(t, "rt-j")
	runCLI(t, "space", "create", "rt-k", "-e", "repo-a")

	dry := decodeJSONObject(t, runCLI(t, "space", "retarget", "rt-j", "--repo", "repo-a", "--base", "space:rt-k", "--dry-run", "--json"))
	if dry["dryRun"] != true || strings.Join(stringSlice(dry["plan"]), "\n") != "dry-run: retarget rt-j repo repo-a to base refs/heads/stave/rt-k/repo-a" {
		t.Fatalf("retarget dry-run = %v", dry)
	}
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if repo, _, ok := manifest.FindRepo("repo-a"); !ok || repo.Base != "origin/main" {
		t.Fatalf("dry-run rewrote the manifest: %v", manifest.Repos)
	}

	retargeted := decodeJSONObject(t, runCLI(t, "space", "retarget", "rt-j", "--repo", "repo-a", "--base", "space:rt-k", "--json"))
	var repo map[string]any
	for _, item := range rowsOf(t, manifestOf(t, retargeted), "repos") {
		if item["name"] == "repo-a" {
			repo = item
		}
	}
	if retargeted["spaceId"] != "rt-j" || retargeted["spacePath"] != spacePath || repo["base"] != "refs/heads/stave/rt-k/repo-a" {
		t.Fatalf("retarget payload = %v", retargeted)
	}
	if _, hasNotes := retargeted["notes"]; hasNotes {
		t.Fatalf("retarget emitted notes on a clean run: %v", retargeted["notes"])
	}

	// A stave-branch spelling is canonicalized with a notice, which lands in notes.
	canon := decodeJSONObject(t, runCLI(t, "space", "retarget", "rt-j", "--repo", "repo-a", "--base", "origin/stave/rt-k/repo-a", "--json"))
	if notes := stringSlice(canon["notes"]); len(notes) != 1 || !strings.Contains(notes[0], "canonicalized") {
		t.Fatalf("retarget notes = %v", canon["notes"])
	}

	if code, _ := jsonErrorCode(t, "space", "retarget", "rt-j", "--base", "origin/main", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("retarget without --repo = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "retarget", "rt-j", "--repo", "repo-a", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("retarget without --base = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "retarget", "rt-j", "--repo", "repo-b", "--base", "origin/main", "--json"); code != space.CodeRepoNotInSpace {
		t.Fatalf("retarget reference repo = %s", code)
	}
	// Human refusal is unchanged: plain error, no envelope.
	if out, err := runCLIError(t, nil, "space", "retarget", "rt-j", "--base", "origin/main"); err == nil || err.Error() != "--repo is required" || strings.Contains(out, `"error"`) {
		t.Fatalf("human retarget refusal = %v\n%s", err, out)
	}
}

func TestCLIJSONSpaceInit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	spacePath := filepath.Join(home, "stave", "agent-work", "in-1")

	created := decodeJSONObject(t, runCLI(t, "space", "init", "in-1", "-k", "spike", "--json"))
	if created["spaceId"] != "in-1" || created["spacePath"] != spacePath || manifestOf(t, created)["id"] != "in-1" || manifestOf(t, created)["kind"] != "spike" || repoCount(t, created) != 0 {
		t.Fatalf("init payload = %v", created)
	}
	if _, hasNotes := created["notes"]; hasNotes {
		t.Fatalf("init emitted notes: %v", created["notes"])
	}
	// Re-init adopts the existing manifest and still returns the payload.
	again := decodeJSONObject(t, runCLI(t, "space", "init", "in-1", "--json"))
	if again["spaceId"] != "in-1" || manifestOf(t, again)["kind"] != "spike" {
		t.Fatalf("re-init payload = %v", again)
	}

	if code, _ := jsonErrorCode(t, "space", "init", "in-2", "--kind", space.KindSaga, "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("init --kind saga = %s", code)
	}
	if code, details := jsonErrorCode(t, "space", "init", "bad/id", "--json"); code != space.CodeInvalidName || details["name"] != "bad/id" {
		t.Fatalf("init bad id = %s %v", code, details)
	}
	if out := runCLI(t, "space", "init", "in-3"); !strings.Contains(out, "created space in-3 at ") || strings.Contains(out, "{") {
		t.Fatalf("human init output = %q", out)
	}
}

func TestCLIJSONSagaSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "saga", "create", "epic-js")
	runCLI(t, "space", "create", "m-1", "--saga", "epic-js", "-e", "repo-a")
	runCLI(t, "space", "create", "m-2", "--saga", "epic-js", "--after", "m-1", "-e", "repo-a", "-r", "repo-a")
	runCLI(t, "space", "create", "m-3", "--saga", "epic-js", "--after", "m-2", "-e", "repo-a")
	runCLI(t, "space", "archive", "m-3", "--force")
	sagaPath := filepath.Join(home, "stave", "agent-work", "epic-js")

	dry := decodeJSONObject(t, runCLI(t, "saga", "sync", "epic-js", "--dry-run", "--json"))
	if dry["dryRun"] != true || !strings.Contains(strings.Join(stringSlice(dry["plan"]), "\n"), "syncing member m-1") {
		t.Fatalf("saga sync dry-run = %v", dry)
	}

	synced := decodeJSONObject(t, runCLI(t, "saga", "sync", "epic-js", "--json"))
	if synced["sagaId"] != "epic-js" || synced["spacePath"] != sagaPath {
		t.Fatalf("saga sync payload = %v", synced)
	}
	members := rowsOf(t, synced, "members")
	if len(members) != 3 || members[0]["id"] != "m-1" || members[1]["id"] != "m-2" || members[2]["id"] != "m-3" {
		t.Fatalf("saga sync members = %v", members)
	}
	if members[0]["state"] != string(space.MemberLive) || len(members[0]["repos"].([]any)) != 1 {
		t.Fatalf("live member row = %v", members[0])
	}
	m2 := rowsOf(t, members[1], "repos")
	if len(m2) != 2 || m2[0]["action"] != space.SyncActionUpdated || m2[1]["action"] != space.SyncActionDriftReported {
		t.Fatalf("m-2 repo rows = %v", m2)
	}
	if members[2]["state"] != string(space.MemberArchived) || !strings.HasPrefix(members[2]["note"].(string), "archived at ") || len(members[2]["repos"].([]any)) != 0 {
		t.Fatalf("archived member row = %v", members[2])
	}
	if _, hasNotes := synced["notes"]; hasNotes {
		t.Fatalf("saga sync leaked per-member lines into notes: %v", synced["notes"])
	}

	human := runCLI(t, "saga", "sync", "epic-js")
	if !strings.Contains(human, "syncing member m-1") || !strings.Contains(human, "skipping member m-3: archived at") || strings.Contains(human, "{") {
		t.Fatalf("human saga sync output = %q", human)
	}

	if code, _ := jsonErrorCode(t, "saga", "sync", "epic-missing", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("saga sync missing = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "sync", "m-1", "--json"); code != space.CodeNotASaga {
		t.Fatalf("saga sync non-saga = %s", code)
	}
}

func TestCLIJSONReposAdd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	runCLI(t, "setup")
	bareA := filepath.Join(home, "stave", "bare-repos", "repo-a.git")

	dry := decodeJSONObject(t, runCLI(t, "repos", "add", "repo-a", srcA, "--dry-run", "--json"))
	plan := strings.Join(stringSlice(dry["plan"]), "\n")
	if dry["dryRun"] != true || !strings.Contains(plan, "dry-run: create ") || !strings.Contains(plan, "registered repo-a at "+bareA) {
		t.Fatalf("repos add dry-run = %v", dry)
	}
	if _, err := os.Stat(bareA); !os.IsNotExist(err) {
		t.Fatalf("dry-run cloned: %v", err)
	}

	added := decodeJSONObject(t, runCLI(t, "repos", "add", "repo-a", srcA, "--json"))
	if added["name"] != "repo-a" || added["url"] != srcA || added["bareRepoPath"] != bareA || added["defaultBranch"] != "main" || added["adopted"] != false {
		t.Fatalf("repos add payload = %v", added)
	}
	if _, hasNotes := added["notes"]; hasNotes {
		t.Fatalf("repos add emitted notes on a clean run: %v", added["notes"])
	}

	if code, details := jsonErrorCode(t, "repos", "add", "repo-a", srcA, "--json"); code != space.CodeRepoExists || details["repo"] != "repo-a" {
		t.Fatalf("repos add duplicate = %s %v", code, details)
	}

	bareB := seedBareCache(t, home, "repo-b", srcB)
	if code, details := jsonErrorCode(t, "repos", "add", "repo-b", srcB, "--json"); code != space.CodeCacheExists || details["path"] != bareB || details["repo"] != "repo-b" {
		t.Fatalf("repos add over cache = %s %v", code, details)
	}
	adopted := decodeJSONObject(t, runCLI(t, "repos", "add", "repo-b", srcB, "--adopt", "--json"))
	if adopted["adopted"] != true || adopted["bareRepoPath"] != bareB {
		t.Fatalf("repos add --adopt payload = %v", adopted)
	}
	if notes := stringSlice(adopted["notes"]); len(notes) != 1 || !strings.Contains(notes[0], "adopted existing bare repo at "+bareB) {
		t.Fatalf("repos add --adopt notes = %v", adopted["notes"])
	}

	notARepo := t.TempDir()
	code, details := jsonErrorCode(t, "repos", "add", "repo-c", notARepo, "--json")
	if code != space.CodeCloneFailed || details["repo"] != "repo-c" {
		t.Fatalf("repos add clone failure = %s %v", code, details)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "bare-repos", "repo-c.git")); !os.IsNotExist(err) {
		t.Fatalf("failed clone left a cache: %v", err)
	}

	// Human paths keep their messages (and stderr notes stay on stderr).
	if _, err := runCLIError(t, nil, "repos", "add", "repo-a", srcA); err == nil || err.Error() != `repo "repo-a" is already registered` {
		t.Fatalf("human duplicate error = %v", err)
	}
	if _, err := runCLIError(t, nil, "repos", "add", "repo-c", notARepo); err == nil || !strings.HasPrefix(err.Error(), `clone "repo-c": `) {
		t.Fatalf("human clone error = %v", err)
	}
}

func TestCLISetupJSONAndForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "stave")
	configPath := filepath.Join(home, ".config", "stave", "config.yaml")

	first := decodeJSONObject(t, runCLI(t, "setup", "--json"))
	if first["configPath"] != configPath || first["root"] != root || first["bareReposDir"] != filepath.Join(root, "bare-repos") || first["agentWorkDir"] != filepath.Join(root, "agent-work") {
		t.Fatalf("setup payload = %v", first)
	}
	created, existed := stringSlice(first["created"]), stringSlice(first["existed"])
	if len(created) != 4 || len(existed) != 0 || created[3] != configPath {
		t.Fatalf("setup created/existed = %v / %v", created, existed)
	}
	if _, ok := first["existed"].([]any); !ok {
		t.Fatalf("existed must be an empty array, got %v", first["existed"])
	}

	// A second run refuses to rewrite the config: coded envelope with --json,
	// plain exit-1 error without.
	if code, details := jsonErrorCode(t, "setup", "--json"); code != space.CodeConfigExists || details["path"] != configPath {
		t.Fatalf("setup over existing config = %s %v", code, details)
	}
	out, err := runCLIError(t, nil, "setup")
	if err == nil || !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), configPath) || strings.Contains(out, `"error"`) {
		t.Fatalf("human setup refusal = %v\n%s", err, out)
	}
	if _, silent := err.(interface{ Silent() bool }); silent {
		t.Fatalf("human refusal should not be silent: %T", err)
	}

	forced := decodeJSONObject(t, runCLI(t, "setup", "--force", "--json"))
	if created, existed := stringSlice(forced["created"]), stringSlice(forced["existed"]); len(created) != 0 || len(existed) != 4 {
		t.Fatalf("forced setup created/existed = %v / %v", created, existed)
	}
	if human := runCLI(t, "setup", "--force"); human != "initialized stave root at "+root+"\nconfig: "+configPath+"\n" {
		t.Fatalf("forced human setup output = %q", human)
	}
}

// The very first human `setup` prints exactly what it always did.
func TestCLISetupHumanFirstRunUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "stave")
	configPath := filepath.Join(home, ".config", "stave", "config.yaml")
	if out := runCLI(t, "setup"); out != "initialized stave root at "+root+"\nconfig: "+configPath+"\n" {
		t.Fatalf("setup output = %q", out)
	}
	for _, path := range []string{root, filepath.Join(root, "bare-repos"), filepath.Join(root, "agent-work"), configPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("setup did not create %s: %v", path, err)
		}
	}
}
