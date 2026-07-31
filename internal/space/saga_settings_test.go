package space

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSagaAgentSettingsMaintained(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-set"}); err != nil {
		t.Fatal(err)
	}
	sagaPath := svc.SpacePath("epic-set")
	// Pre-existing harness settings carrying foreign content that must survive.
	claudePath := filepath.Join(sagaPath, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"model":"opus","permissions":{"allow":["Bash"],"additionalDirectories":["/stale"]}}`
	if err := os.WriteFile(claudePath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(sagaPath, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[profile]\nname = \"x\"\n\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [\n  \"/stale\",\n]\n\n[other]\nkey = 1\n"
	if err := os.WriteFile(codexPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	mustInitSpace(t, svc, "set-2")
	mustInitSpace(t, svc, "set-1")
	if err := svc.SagaAdd(ctx, "epic-set", "set-2", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SagaAdd(ctx, "epic-set", "set-1", nil, false); err != nil {
		t.Fatal(err)
	}

	readSettings := func(t *testing.T) map[string]any {
		t.Helper()
		data, err := os.ReadFile(claudePath)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	doc := readSettings(t)
	if doc["model"] != "opus" {
		t.Fatalf("foreign top-level key lost: %#v", doc)
	}
	permissions, _ := doc["permissions"].(map[string]any)
	if !reflect.DeepEqual(permissions["allow"], []any{"Bash"}) {
		t.Fatalf("permissions.allow lost: %#v", permissions)
	}
	if !reflect.DeepEqual(permissions["additionalDirectories"], []any{"../set-1", "../set-2"}) {
		t.Fatalf("additionalDirectories = %#v, want sorted member paths", permissions["additionalDirectories"])
	}

	got, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	tomlGot := string(got)
	for _, want := range []string{
		"[profile]", "name = \"x\"", "[other]", "key = 1", "[sandbox_workspace_write]",
		"network_access = true",
		fmt.Sprintf("writable_roots = [%q, %q]", svc.SpacePath("set-1"), svc.SpacePath("set-2")),
	} {
		if !strings.Contains(tomlGot, want) {
			t.Fatalf("config.toml missing %q:\n%s", want, tomlGot)
		}
	}
	if strings.Contains(tomlGot, "/stale") {
		t.Fatalf("stale writable root survived:\n%s", tomlGot)
	}
	if strings.Count(tomlGot, "[sandbox_workspace_write]") != 1 {
		t.Fatalf("duplicated section:\n%s", tomlGot)
	}

	// A membership change updates both files.
	if err := svc.SagaRemove(ctx, "epic-set", "set-1"); err != nil {
		t.Fatal(err)
	}
	doc = readSettings(t)
	permissions, _ = doc["permissions"].(map[string]any)
	if !reflect.DeepEqual(permissions["additionalDirectories"], []any{"../set-2"}) {
		t.Fatalf("post-remove additionalDirectories = %#v", permissions["additionalDirectories"])
	}
	got, err = os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), svc.SpacePath("set-1")) || !strings.Contains(string(got), svc.SpacePath("set-2")) {
		t.Fatalf("post-remove writable_roots wrong:\n%s", got)
	}
	if !strings.Contains(string(got), "network_access = true") {
		t.Fatalf("post-remove network_access setting lost:\n%s", got)
	}

	// A saga without pre-existing files gets minimal ones on first change.
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-min"}); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "min-1")
	if err := svc.SagaAdd(ctx, "epic-min", "min-1", nil, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(svc.SpacePath("epic-min"), ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var minimal map[string]any
	if err := json.Unmarshal(data, &minimal); err != nil {
		t.Fatal(err)
	}
	minimalPerms, _ := minimal["permissions"].(map[string]any)
	if !reflect.DeepEqual(minimalPerms["additionalDirectories"], []any{"../min-1"}) {
		t.Fatalf("minimal settings = %#v", minimal)
	}
	minToml, err := os.ReadFile(filepath.Join(svc.SpacePath("epic-min"), ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(minToml), "[sandbox_workspace_write]\n") || !strings.Contains(string(minToml), svc.SpacePath("min-1")) {
		t.Fatalf("minimal config.toml = %q", minToml)
	}
}

// TestStripTOMLSectionArrayOfTables: array-of-tables headers of the section
// ([[section]] and [[section.x]]) strip together with their parent table so
// the regenerated document cannot carry stale duplicates.
func TestStripTOMLSectionArrayOfTables(t *testing.T) {
	src := "[profile]\nname = \"x\"\n\n" +
		"[sandbox_workspace_write]\nwritable_roots = [\"/stale\"]\n\n" +
		"[[sandbox_workspace_write.extra]]\npath = \"/stale-2\"\n\n" +
		"[[sandbox_workspace_write]]\npath = \"/stale-3\"\n\n" +
		"[other]\nkey = 1\n"
	got := stripTOMLSection(src, codexWritableRootsSection)
	for _, gone := range []string{"sandbox_workspace_write", "/stale", "/stale-2", "/stale-3"} {
		if strings.Contains(got, gone) {
			t.Fatalf("stripTOMLSection left %q:\n%s", gone, got)
		}
	}
	for _, keep := range []string{"[profile]", "name = \"x\"", "[other]", "key = 1"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("stripTOMLSection dropped %q:\n%s", keep, got)
		}
	}
}

// TestSagaAgentSettingsFailureIsPostCommitNotice: an unparseable settings file
// fails the settings refresh AFTER the roster commit, so the verb succeeds
// with a printed notice, the file stays unclobbered, and the member-side
// effects still run.
func TestSagaAgentSettingsFailureIsPostCommitNotice(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-bad"}); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(svc.SpacePath("epic-bad"), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatal(err)
	}
	const garbage = "{not json"
	if err := os.WriteFile(claudePath, []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}
	mustInitSpace(t, svc, "bad-1")
	var out strings.Builder
	svc.Out = &out
	if err := svc.SagaAdd(ctx, "epic-bad", "bad-1", nil, false); err != nil {
		t.Fatalf("SagaAdd with unparseable settings error = %v (must succeed post-commit)", err)
	}
	if !strings.Contains(out.String(), "notice: could not update saga agent settings:") {
		t.Fatalf("missing settings notice:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "added bad-1 to saga epic-bad") {
		t.Fatalf("member effects skipped:\n%s", out.String())
	}
	if members := loadSagaMembers(t, svc, "epic-bad"); len(members) != 1 || members[0].ID != "bad-1" {
		t.Fatalf("roster not committed: %#v", members)
	}
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != garbage {
		t.Fatalf("unparseable settings clobbered: %q", data)
	}
}
