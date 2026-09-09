package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

// decodeJSONObject decodes a --json stdout payload into a generic map.
func decodeJSONObject(t *testing.T, out string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	return value
}

// jsonErrorCode runs a failing --json command and returns the envelope's code
// and details, asserting the exit contract (silent, exit 1) on the way.
func jsonErrorCode(t *testing.T, args ...string) (string, map[string]any) {
	t.Helper()
	out, err := runCLIError(t, nil, args...)
	if err == nil {
		t.Fatalf("stave %v unexpectedly succeeded:\n%s", args, out)
	}
	exit, ok := err.(interface {
		ExitCode() int
		Silent() bool
	})
	if !ok || exit.ExitCode() != 1 || !exit.Silent() {
		t.Fatalf("stave %v error is not a silent exit-1 error: %T %v", args, err, err)
	}
	payload := decodeJSONObject(t, out)
	envelope, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("stave %v: missing error envelope:\n%s", args, out)
	}
	if msg, _ := envelope["message"].(string); msg == "" || msg != err.Error() {
		t.Fatalf("stave %v: envelope message %q != error %q", args, msg, err.Error())
	}
	details, _ := envelope["details"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code, details
}

func manifestOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	manifest, ok := payload["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("payload has no manifest: %v", payload)
	}
	return manifest
}

func repoCount(t *testing.T, payload map[string]any) int {
	t.Helper()
	repos, _ := manifestOf(t, payload)["repos"].([]any)
	return len(repos)
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func TestCLIJSONSpaceCreateAddRemove(t *testing.T) {
	home := setupThreeRepos(t)
	spacePath := filepath.Join(home, "stave", "agent-work", "js-1")

	created := decodeJSONObject(t, runCLI(t, "space", "create", "js-1", "-e", "repo-a", "--json"))
	if created["spaceId"] != "js-1" || created["spacePath"] != spacePath {
		t.Fatalf("create payload = %v", created)
	}
	if manifestOf(t, created)["id"] != "js-1" || repoCount(t, created) != 1 {
		t.Fatalf("create manifest = %v", created["manifest"])
	}
	if _, hasNotes := created["notes"]; hasNotes {
		t.Fatalf("create emitted notes on a clean run: %v", created["notes"])
	}

	added := decodeJSONObject(t, runCLI(t, "space", "add", "js-1", "repo-b", "--reference", "--json"))
	if added["spaceId"] != "js-1" || repoCount(t, added) != 2 {
		t.Fatalf("add payload = %v", added)
	}

	dry := decodeJSONObject(t, runCLI(t, "space", "remove", "js-1", "repo-b", "--dry-run", "--json"))
	if dry["dryRun"] != true || len(stringSlice(dry["plan"])) == 0 || !strings.Contains(strings.Join(stringSlice(dry["plan"]), "\n"), "dry-run: remove worktree") {
		t.Fatalf("dry-run payload = %v", dry)
	}

	removed := decodeJSONObject(t, runCLI(t, "space", "remove", "js-1", "repo-b", "--json"))
	if removed["spaceId"] != "js-1" || repoCount(t, removed) != 1 {
		t.Fatalf("remove payload = %v", removed)
	}

	// Error envelopes.
	if code, details := jsonErrorCode(t, "space", "remove", "js-1", "repo-zz", "--json"); code != space.CodeRepoNotInSpace || details["repo"] != "repo-zz" {
		t.Fatalf("remove unknown repo = %s %v", code, details)
	}
	if err := os.WriteFile(filepath.Join(spacePath, "repo-a", "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, details := jsonErrorCode(t, "space", "remove", "js-1", "repo-a", "--json"); code != space.CodeDirtyWorktrees || strings.Join(stringSlice(details["repos"]), ",") != "repo-a" {
		t.Fatalf("dirty remove = %s %v", code, details)
	}
	if code, _ := jsonErrorCode(t, "space", "add", "js-1", "repo-c", "--edit", "--reference", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("add both modes = %s", code)
	}
	if code, details := jsonErrorCode(t, "space", "add", "js-1", "nope", "--reference", "--json"); code != space.CodeRepoNotFound || details["repo"] != "nope" {
		t.Fatalf("add unregistered = %s %v", code, details)
	}
	if code, _ := jsonErrorCode(t, "space", "add", "js-missing", "repo-c", "--reference", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("add to missing space = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "create", "js-9", "--summon", "claude", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("create --summon --json = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "create", "js-9", "--kind", space.KindSaga, "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("create --kind saga = %s", code)
	}
	if code, details := jsonErrorCode(t, "space", "create", "bad/id", "--json"); code != space.CodeInvalidName || details["name"] != "bad/id" {
		t.Fatalf("create invalid id = %s %v", code, details)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "js-9")); !os.IsNotExist(err) {
		t.Fatalf("refused create left a space behind: %v", err)
	}
}

func TestCLIJSONSpaceArchiveRestoreDestroy(t *testing.T) {
	home, spacePath, _, _ := lifecycleFixture(t, "js-2")
	archiveRoot := filepath.Join(home, "stave", "agent-work", ".archive")

	dry := decodeJSONObject(t, runCLI(t, "space", "archive", "js-2", "--dry-run", "--json"))
	if dry["dryRun"] != true || !strings.Contains(strings.Join(stringSlice(dry["plan"]), "\n"), "dry-run: archive") {
		t.Fatalf("archive dry-run payload = %v", dry)
	}

	archived := decodeJSONObject(t, runCLI(t, "space", "archive", "js-2", "--json"))
	if archived["spaceId"] != "js-2" || archived["archivedPath"] != filepath.Join(archiveRoot, "js-2") || archived["memory"] != "keep" {
		t.Fatalf("archive payload = %v", archived)
	}

	restored := decodeJSONObject(t, runCLI(t, "space", "restore", "js-2", "--json"))
	if restored["spaceId"] != "js-2" || restored["spacePath"] != spacePath || repoCount(t, restored) != 2 {
		t.Fatalf("restore payload = %v", restored)
	}
	if _, hasNotes := restored["notes"]; hasNotes {
		t.Fatalf("restore emitted notes on a clean run: %v", restored["notes"])
	}

	// Two archives of the same id: the second lands timestamped (the exact
	// entry is taken) and archivedPath reports the new entry, not the old one.
	runCLI(t, "space", "archive", "js-2")
	runCLI(t, "space", "create", "js-2", "-e", "repo-a")
	second := decodeJSONObject(t, runCLI(t, "space", "archive", "js-2", "--json"))
	dest, _ := second["archivedPath"].(string)
	if !strings.HasPrefix(dest, filepath.Join(archiveRoot, "js-2-")) {
		t.Fatalf("second archive path = %q", dest)
	}
	// With no exact entry and two timestamped ones, restore needs --from.
	if err := os.Rename(filepath.Join(archiveRoot, "js-2"), filepath.Join(archiveRoot, "js-2-20000101000000")); err != nil {
		t.Fatal(err)
	}
	code, details := jsonErrorCode(t, "space", "restore", "js-2", "--json")
	if code != space.CodeAmbiguousArchive || len(stringSlice(details["candidates"])) != 2 {
		t.Fatalf("ambiguous restore = %s %v", code, details)
	}

	// A live space blocks restore with space_exists.
	runCLI(t, "space", "create", "js-2", "-e", "repo-a")
	if code, details := jsonErrorCode(t, "space", "restore", "js-2", "--from", "js-2-20000101000000", "--json"); code != space.CodeSpaceExists || details["path"] != spacePath {
		t.Fatalf("restore over live = %s %v", code, details)
	}

	destroyed := decodeJSONObject(t, runCLI(t, "space", "destroy", "js-2", "--json"))
	if destroyed["spaceId"] != "js-2" || destroyed["destroyed"] != true || destroyed["memory"] != "keep" || destroyed["spacePath"] != spacePath {
		t.Fatalf("destroy payload = %v", destroyed)
	}
	if code, _ := jsonErrorCode(t, "space", "destroy", "js-2", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("destroy missing = %s", code)
	}
	if code, _ := jsonErrorCode(t, "space", "archive", "js-2", "--memory", "destroy", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("archive --memory destroy = %s", code)
	}

	// Untyped failures fall back to "unknown" with the message preserved.
	runCLI(t, "space", "create", "js-bad", "-e", "repo-a")
	if err := os.WriteFile(filepath.Join(home, "stave", "agent-work", "js-bad", space.ManifestName), []byte(":: not yaml ::\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := jsonErrorCode(t, "space", "destroy", "js-bad", "--json"); code != space.CodeUnknown {
		t.Fatalf("corrupt manifest destroy = %s", code)
	}
}

func TestCLIJSONSagaLifecycle(t *testing.T) {
	home := setupThreeRepos(t)
	sagaPath := filepath.Join(home, "stave", "agent-work", "epic-j")

	created := decodeJSONObject(t, runCLI(t, "saga", "create", "epic-j", "--json"))
	if created["sagaId"] != "epic-j" || created["spacePath"] != sagaPath || manifestOf(t, created)["saga"] == nil {
		t.Fatalf("saga create payload = %v", created)
	}
	runCLI(t, "space", "create", "m-1", "--saga", "epic-j", "-e", "repo-a")
	runCLI(t, "space", "create", "m-2", "-e", "repo-b")

	dry := decodeJSONObject(t, runCLI(t, "saga", "add", "epic-j", "m-2", "--dry-run", "--json"))
	if dry["dryRun"] != true || strings.Join(stringSlice(dry["plan"]), "\n") != "dry-run: add m-2 to saga epic-j" {
		t.Fatalf("saga add dry-run = %v", dry)
	}
	added := decodeJSONObject(t, runCLI(t, "saga", "add", "epic-j", "m-2", "--json"))
	saga, _ := manifestOf(t, added)["saga"].(map[string]any)
	if members, _ := saga["members"].([]any); added["sagaId"] != "epic-j" || len(members) != 2 {
		t.Fatalf("saga add payload = %v", added)
	}

	// Single-space verbs refuse saga state with typed codes.
	if code, details := jsonErrorCode(t, "space", "archive", "m-1", "--json"); code != space.CodeSagaMember || details["saga"] != "epic-j" {
		t.Fatalf("archive member = %s %v", code, details)
	}
	if code, _ := jsonErrorCode(t, "space", "destroy", "epic-j", "--json"); code != space.CodeSagaSpace {
		t.Fatalf("destroy saga = %s", code)
	}
	runCLI(t, "saga", "create", "epic-z")
	if code, _ := jsonErrorCode(t, "saga", "add", "epic-j", "epic-z", "--json"); code != space.CodeSagaSpace {
		t.Fatalf("saga add saga = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "add", "epic-j", "epic-j", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("saga self-add = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "add", "epic-j", "ghost", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("saga add missing = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "create", "m-1", "--json"); code != space.CodeSpaceExists {
		t.Fatalf("saga create over live = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "create", "--json", "epic-x", "-e", "repo-a"); code != space.CodeInvalidArguments {
		t.Fatalf("saga create -e = %s", code)
	}
	if code, _ := jsonErrorCode(t, "saga", "create", "epic-x", "--summon", "claude", "--json"); code != space.CodeInvalidArguments {
		t.Fatalf("saga create --summon --json = %s", code)
	}

	removed := decodeJSONObject(t, runCLI(t, "saga", "remove", "epic-j", "m-2", "--json"))
	saga, _ = manifestOf(t, removed)["saga"].(map[string]any)
	if members, _ := saga["members"].([]any); len(members) != 1 {
		t.Fatalf("saga remove payload = %v", removed)
	}

	plan := decodeJSONObject(t, runCLI(t, "saga", "archive", "epic-j", "--dry-run", "--json"))
	if plan["dryRun"] != true || !strings.Contains(strings.Join(stringSlice(plan["plan"]), "\n"), "dry-run: 1. archive member m-1") {
		t.Fatalf("saga archive dry-run = %v", plan)
	}
	if err := os.WriteFile(filepath.Join(home, "stave", "agent-work", "m-1", "repo-a", "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, details := jsonErrorCode(t, "saga", "archive", "epic-j", "--json"); code != space.CodeDirtyWorktrees || strings.Join(stringSlice(details["repos"]), ",") != "repo-a" {
		t.Fatalf("saga archive dirty = %s %v", code, details)
	}
	archived := decodeJSONObject(t, runCLI(t, "saga", "archive", "epic-j", "--force", "--json"))
	members, _ := archived["members"].([]any)
	if archived["sagaId"] != "epic-j" || archived["action"] != "archived" || archived["memory"] != "keep" || len(members) != 1 {
		t.Fatalf("saga archive payload = %v", archived)
	}
	if row, _ := members[0].(map[string]any); row["id"] != "m-1" || row["action"] != "archived" {
		t.Fatalf("saga archive member row = %v", members[0])
	}
	if code, _ := jsonErrorCode(t, "saga", "destroy", "epic-j", "--json"); code != space.CodeSpaceNotFound {
		t.Fatalf("saga destroy archived saga = %s", code)
	}

	runCLI(t, "saga", "create", "epic-k")
	runCLI(t, "space", "create", "k-1", "--saga", "epic-k", "-e", "repo-c")
	runCLI(t, "space", "archive", "k-1", "--force")
	destroyed := decodeJSONObject(t, runCLI(t, "saga", "destroy", "epic-k", "--json"))
	members, _ = destroyed["members"].([]any)
	if destroyed["action"] != "destroyed" || len(members) != 1 {
		t.Fatalf("saga destroy payload = %v", destroyed)
	}
	if row, _ := members[0].(map[string]any); row["id"] != "k-1" || row["action"] != "skipped" || !strings.Contains(row["note"].(string), "archived at") {
		t.Fatalf("saga destroy member row = %v", members[0])
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "epic-k")); !os.IsNotExist(err) {
		t.Fatalf("saga space survived destroy: %v", err)
	}
}

// Without --json nothing about the human path changes: no envelope, the plain
// error propagates, and the success line prints as before.
func TestCLIJSONFlagAbsentKeepsHumanOutput(t *testing.T) {
	_, _, _, _ = lifecycleFixture(t, "js-3")
	out, err := runCLIError(t, nil, "space", "remove", "js-3", "repo-zz")
	if err == nil || strings.Contains(out, `"error"`) || !strings.Contains(err.Error(), `has no repo "repo-zz"`) {
		t.Fatalf("human remove error = %v\n%s", err, out)
	}
	if _, silent := err.(interface{ Silent() bool }); silent {
		t.Fatalf("human error should not be silent: %T", err)
	}
	if out := runCLI(t, "space", "remove", "js-3", "repo-b"); strings.TrimSpace(out) != "removed repo-b from js-3" {
		t.Fatalf("human remove output = %q", out)
	}
}
