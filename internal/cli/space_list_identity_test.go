package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nurozen/stave/internal/space"
)

// TestCLISpaceListIdentityFields pins the host identity fields on `space list
// --json`: logicalId + manifestCreatedAt (RFC3339Nano, round-tripping the
// manifest's fractional stamp), archiveBasename on archived rows, null
// logicalId on error rows, manifestVersion and memories. The human output
// does not change.
func TestCLISpaceListIdentityFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	agentWork := filepath.Join(home, "stave", "agent-work")

	// A live space created through the service stamps time.Now() with
	// fractional seconds; a hand-written one pins a known fractional stamp
	// and a memory attachment.
	runCLI(t, "space", "init", "live-1")
	stamp := time.Date(2026, 5, 27, 1, 2, 3, 123456789, time.UTC)
	pinned := filepath.Join(agentWork, "pinned-1")
	if err := os.MkdirAll(pinned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(pinned, space.Manifest{
		ID:        "pinned-1",
		CreatedAt: stamp,
		Memories:  []space.MemoryManifest{{Name: "den", Provider: "marmot", ID: "den-p1", Owned: true}},
	}); err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(agentWork, "bad-1")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, space.ManifestName), []byte("id: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	human := runCLI(t, "space", "list")
	if !strings.Contains(human, "pinned-1\t-\t"+pinned+"\n") || strings.Contains(human, "logicalId") {
		t.Fatalf("human space list changed:\n%s", human)
	}

	raw := runCLI(t, "space", "list", "--json")
	rows := decodeListRows(t, raw)
	live := rows["live-1"]
	liveManifest, err := space.LoadManifest(filepath.Join(agentWork, "live-1"))
	if err != nil {
		t.Fatal(err)
	}
	if live.LogicalID == nil || *live.LogicalID != "live-1" || live.ArchiveBasename != "" || live.Archived {
		t.Fatalf("live-1 row = %#v", live)
	}
	if live.ManifestCreatedAt != liveManifest.CreatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("live-1 manifestCreatedAt %q does not round-trip the manifest stamp %s", live.ManifestCreatedAt, liveManifest.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	if parsed, err := time.Parse(time.RFC3339Nano, live.ManifestCreatedAt); err != nil || !parsed.Equal(liveManifest.CreatedAt) {
		t.Fatalf("live-1 manifestCreatedAt %q does not parse back to the manifest stamp: %v", live.ManifestCreatedAt, err)
	}
	if live.ManifestVersion != liveManifest.Version || live.ManifestVersion < 1 {
		t.Fatalf("live-1 manifestVersion = %d, manifest %d", live.ManifestVersion, liveManifest.Version)
	}

	pin := rows["pinned-1"]
	if pin.LogicalID == nil || *pin.LogicalID != "pinned-1" {
		t.Fatalf("pinned-1 logicalId = %v", pin.LogicalID)
	}
	if pin.ManifestCreatedAt != "2026-05-27T01:02:03.123456789Z" || pin.CreatedAt != "2026-05-27T01:02:03Z" {
		t.Fatalf("pinned-1 stamps = createdAt %q manifestCreatedAt %q", pin.CreatedAt, pin.ManifestCreatedAt)
	}
	if len(pin.Memories) != 1 || pin.Memories[0] != (spaceListMemoryJSON{Name: "den", Provider: "marmot", ID: "den-p1", Owned: true}) {
		t.Fatalf("pinned-1 memories = %#v", pin.Memories)
	}
	if pin.ManifestVersion != 1 {
		t.Fatalf("pinned-1 manifestVersion = %d", pin.ManifestVersion)
	}

	bad := rows["bad-1"]
	if bad.Error == "" || bad.LogicalID != nil || bad.ManifestCreatedAt != "" || bad.ManifestVersion != 0 || bad.Memories == nil || len(bad.Memories) != 0 {
		t.Fatalf("bad-1 row = %#v", bad)
	}
	if !strings.Contains(raw, `"logicalId": null`) || !strings.Contains(raw, `"memories": []`) {
		t.Fatalf("error row must emit null logicalId and an empty memories array:\n%s", raw)
	}

	// Archive pinned-1 twice under the same id: the second archive lands on
	// the collision shape, so the archive basename and the logical id diverge.
	runCLI(t, "space", "archive", "pinned-1", "--force")
	if err := os.MkdirAll(pinned, 0o755); err != nil {
		t.Fatal(err)
	}
	second := stamp.Add(90 * time.Minute)
	if err := space.SaveManifest(pinned, space.Manifest{ID: "pinned-1", CreatedAt: second}); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "space", "archive", "pinned-1", "--force")

	archived := decodeListRows(t, runCLI(t, "space", "list", "--archived", "--json"))
	if len(archived) != 2 {
		t.Fatalf("archived rows = %#v", archived)
	}
	for basename, row := range archived {
		if !row.Archived || row.ArchiveBasename != basename || row.ID != basename || row.Path != filepath.Join(agentWork, ".archive", basename) {
			t.Fatalf("archived row %s = %#v", basename, row)
		}
		if row.LogicalID == nil || *row.LogicalID != "pinned-1" {
			t.Fatalf("archived row %s logicalId = %v, want the manifest id", basename, row.LogicalID)
		}
		manifest, err := space.LoadManifest(row.Path)
		if err != nil {
			t.Fatal(err)
		}
		if row.ManifestCreatedAt != manifest.CreatedAt.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("archived row %s manifestCreatedAt %q != manifest %s", basename, row.ManifestCreatedAt, manifest.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
	}
	exact, ok := archived["pinned-1"]
	if !ok || exact.ManifestCreatedAt != "2026-05-27T01:02:03.123456789Z" {
		t.Fatalf("exact-name archive row = %#v", exact)
	}
	var suffixed spaceListRow
	for basename, row := range archived {
		if basename != "pinned-1" {
			suffixed = row
		}
	}
	if !strings.HasPrefix(suffixed.ArchiveBasename, "pinned-1-") || suffixed.ManifestCreatedAt != second.Format(time.RFC3339Nano) {
		t.Fatalf("collision archive row = %#v", suffixed)
	}
}

func decodeListRows(t *testing.T, raw string) map[string]spaceListRow {
	t.Helper()
	var rows []spaceListRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("space list --json invalid: %v\n%s", err, raw)
	}
	byID := make(map[string]spaceListRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

// TestCLISagaListJSONIdentity: saga list rows carry path and logicalId
// (null on error rows), additively to the v0.3 shape.
func TestCLISagaListJSONIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	agentWork := filepath.Join(home, "stave", "agent-work")
	captureShellChdir(t, func() { runCLI(t, "saga", "create", "epic-id") })
	badDir := filepath.Join(agentWork, "bad-2")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, space.ManifestName), []byte("id: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := runCLI(t, "saga", "list", "--json")
	var rows []sagaListRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("saga list --json invalid: %v\n%s", err, raw)
	}
	byID := map[string]sagaListRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if row := byID["epic-id"]; row.Path != filepath.Join(agentWork, "epic-id") || row.LogicalID == nil || *row.LogicalID != "epic-id" || !row.IsSaga {
		t.Fatalf("epic-id row = %#v", row)
	}
	if row := byID["bad-2"]; row.Error == "" || row.LogicalID != nil || row.Path != badDir {
		t.Fatalf("bad-2 row = %#v", row)
	}
	if !strings.Contains(raw, `"logicalId": null`) {
		t.Fatalf("error row must emit null logicalId:\n%s", raw)
	}
}

// TestCLISagaTeardownJSONPaths: saga archive/destroy --json carry per-member
// live paths, archived paths, and the saga space's own paths; a mid-walk
// failure's envelope lists the completed steps and the failed member.
func TestCLISagaTeardownJSONPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	work := filepath.Join(home, "stave", "agent-work")
	archiveRoot := filepath.Join(work, ".archive")

	// Archive: js-2 stacks on js-1; js-0 is pre-archived and skipped.
	runCLI(t, "saga", "create", "epic-js")
	runCLI(t, "space", "create", "js-0", "--saga", "epic-js", "-e", "repo-a")
	runCLI(t, "space", "create", "js-1", "--saga", "epic-js", "-e", "repo-a")
	runCLI(t, "space", "create", "js-2", "--saga", "epic-js", "--after", "js-1", "-e", "repo-a")
	runCLI(t, "space", "archive", "js-0", "--force")

	payload := decodeJSONObject(t, runCLI(t, "saga", "archive", "epic-js", "--json"))
	if payload["sagaPath"] != filepath.Join(work, "epic-js") || payload["sagaArchivedPath"] != filepath.Join(archiveRoot, "epic-js") || payload["action"] != "archived" {
		t.Fatalf("saga archive --json header = %v", payload)
	}
	members := memberRows(t, payload)
	if len(members) != 3 {
		t.Fatalf("members = %v", members)
	}
	// Teardown order: js-2 before js-1; js-0 (archived) is skipped with its
	// existing archive path.
	if members[0]["id"] != "js-2" || members[0]["action"] != "archived" || members[0]["path"] != filepath.Join(work, "js-2") || members[0]["archivedPath"] != filepath.Join(archiveRoot, "js-2") {
		t.Fatalf("js-2 row = %v", members[0])
	}
	if members[1]["id"] != "js-1" || members[1]["archivedPath"] != filepath.Join(archiveRoot, "js-1") || members[1]["path"] != filepath.Join(work, "js-1") {
		t.Fatalf("js-1 row = %v", members[1])
	}
	if members[2]["id"] != "js-0" || members[2]["action"] != "skipped" || members[2]["path"] != filepath.Join(work, "js-0") || members[2]["archivedPath"] != filepath.Join(archiveRoot, "js-0") {
		t.Fatalf("js-0 row = %v", members[2])
	}

	// Destroy: paths present, no archived paths.
	runCLI(t, "saga", "create", "epic-jd")
	runCLI(t, "space", "create", "jd-1", "--saga", "epic-jd", "-e", "repo-a")
	payload = decodeJSONObject(t, runCLI(t, "saga", "destroy", "epic-jd", "--json"))
	if payload["sagaPath"] != filepath.Join(work, "epic-jd") || payload["action"] != "destroyed" {
		t.Fatalf("saga destroy --json header = %v", payload)
	}
	if _, present := payload["sagaArchivedPath"]; present {
		t.Fatalf("destroy must omit sagaArchivedPath: %v", payload)
	}
	members = memberRows(t, payload)
	if len(members) != 1 || members[0]["id"] != "jd-1" || members[0]["action"] != "destroyed" || members[0]["path"] != filepath.Join(work, "jd-1") {
		t.Fatalf("jd-1 row = %v", members)
	}
	if _, present := members[0]["archivedPath"]; present {
		t.Fatalf("destroy row must omit archivedPath: %v", members[0])
	}

	// Mid-walk failure: jf-2 (torn down first) archives fine; jf-1's worktree
	// has lost its .git link so `git worktree remove` fails. --force skips the
	// dirty preflight (which would otherwise refuse before any teardown).
	runCLI(t, "saga", "create", "epic-jf")
	runCLI(t, "space", "create", "jf-1", "--saga", "epic-jf", "-e", "repo-a")
	runCLI(t, "space", "create", "jf-2", "--saga", "epic-jf", "--after", "jf-1", "-e", "repo-a")
	if err := os.Remove(filepath.Join(work, "jf-1", "repo-a", ".git")); err != nil {
		t.Fatal(err)
	}
	code, details := jsonErrorCode(t, "saga", "archive", "epic-jf", "--json", "--force")
	if code != space.CodeUnknown {
		t.Fatalf("code = %q, details %v", code, details)
	}
	if details["failedMember"] != "jf-1" || details["failedAt"] != "member" {
		t.Fatalf("failure locator = %v", details)
	}
	completed, ok := details["completed"].([]any)
	if !ok || len(completed) != 1 {
		t.Fatalf("details.completed = %#v", details["completed"])
	}
	step, _ := completed[0].(map[string]any)
	if step["id"] != "jf-2" || step["action"] != "archived" || step["path"] != filepath.Join(work, "jf-2") || step["archivedPath"] != filepath.Join(archiveRoot, "jf-2") {
		t.Fatalf("completed step = %v", step)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, "jf-2")); err != nil {
		t.Fatalf("jf-2 should be archived before the stop: %v", err)
	}
	for _, id := range []string{"jf-1", "epic-jf"} {
		if _, err := os.Stat(filepath.Join(work, id)); err != nil {
			t.Fatalf("%s torn down despite the stop: %v", id, err)
		}
	}
}

func memberRows(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["members"].([]any)
	if !ok {
		t.Fatalf("payload has no members array: %v", payload)
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("member row is not an object: %v", item)
		}
		rows = append(rows, row)
	}
	return rows
}
