package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

// TestCLISpaceListTextAndJSON covers `space list` in both output modes over a
// plain space, a saga with one member, and a corrupt manifest, then flips one
// space into .archive/ and checks --archived picks it up (and the live list
// drops it).
func TestCLISpaceListTextAndJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	agentWork := filepath.Join(home, "stave", "agent-work")

	if out := runCLI(t, "space", "list"); strings.TrimSpace(out) != "" {
		t.Fatalf("space list on empty root = %q, want nothing", out)
	}
	var empty []spaceListRow
	if err := json.Unmarshal([]byte(runCLI(t, "space", "list", "--json")), &empty); err != nil || len(empty) != 0 {
		t.Fatalf("space list --json on empty root = %v (err %v)", empty, err)
	}
	if out := runCLI(t, "space", "list", "--archived"); strings.TrimSpace(out) != "" {
		t.Fatalf("space list --archived without .archive/ = %q, want nothing", out)
	}

	runCLI(t, "space", "init", "plain-1")
	captureShellChdir(t, func() { runCLI(t, "saga", "create", "epic-1") })
	captureShellChdir(t, func() { runCLI(t, "space", "create", "step-1", "--saga", "epic-1", "-e", "repo-a") })
	badDir := filepath.Join(agentWork, "bad-1")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, space.ManifestName), []byte("id: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without a manifest is not a space and must not be listed.
	if err := os.MkdirAll(filepath.Join(agentWork, "not-a-space"), 0o755); err != nil {
		t.Fatal(err)
	}

	text := runCLI(t, "space", "list")
	for _, want := range []string{
		"plain-1\t-\t" + filepath.Join(agentWork, "plain-1") + "\n",
		"epic-1\tsaga\t" + filepath.Join(agentWork, "epic-1") + "\n",
		"step-1\t-\t" + filepath.Join(agentWork, "step-1") + "\n",
		"bad-1\terror: ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("space list missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "not-a-space") {
		t.Fatalf("space list listed a manifest-less directory:\n%s", text)
	}

	var rows []spaceListRow
	raw := runCLI(t, "space", "list", "--json")
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("space list --json invalid: %v\n%s", err, raw)
	}
	byID := map[string]spaceListRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if len(byID) != 4 {
		t.Fatalf("space list --json rows = %#v", rows)
	}
	if row := byID["plain-1"]; row.Path != filepath.Join(agentWork, "plain-1") || row.Kind != "" || row.IsSaga || row.MemberOf != "" || row.CreatedAt == "" || len(row.Repos) != 0 || row.Archived || row.Error != "" {
		t.Fatalf("plain-1 row = %#v", row)
	}
	if row := byID["epic-1"]; row.Kind != space.KindSaga || !row.IsSaga || row.MemberOf != "" {
		t.Fatalf("epic-1 row = %#v", row)
	}
	if row := byID["step-1"]; row.IsSaga || row.MemberOf != "epic-1" || len(row.Repos) != 1 || row.Repos[0].Name != "repo-a" || row.Repos[0].Mode != space.ModeEdit {
		t.Fatalf("step-1 row = %#v", row)
	}
	if row := byID["bad-1"]; row.Error == "" || row.Path != badDir || row.Kind != "" {
		t.Fatalf("bad-1 row = %#v", row)
	}
	// Rows never emit null for repos: an empty space carries [].
	if !strings.Contains(raw, `"repos": []`) {
		t.Fatalf("space list --json should emit an empty repos array:\n%s", raw)
	}

	runCLI(t, "space", "archive", "plain-1", "--force")
	if out := runCLI(t, "space", "list"); strings.Contains(out, "plain-1\t") {
		t.Fatalf("space list still shows archived space:\n%s", out)
	}
	archivedText := runCLI(t, "space", "list", "--archived")
	if want := "plain-1\t-\t" + filepath.Join(agentWork, ".archive", "plain-1") + "\n"; archivedText != want {
		t.Fatalf("space list --archived = %q, want %q", archivedText, want)
	}
	var archivedRows []spaceListRow
	if err := json.Unmarshal([]byte(runCLI(t, "space", "list", "--archived", "--json")), &archivedRows); err != nil {
		t.Fatalf("space list --archived --json invalid: %v", err)
	}
	if len(archivedRows) != 1 || archivedRows[0].ID != "plain-1" || !archivedRows[0].Archived || archivedRows[0].Path != filepath.Join(agentWork, ".archive", "plain-1") || archivedRows[0].Error != "" {
		t.Fatalf("space list --archived --json = %#v", archivedRows)
	}
}

// TestCLISpaceStatusJSON drives `space status --json` over a space with one
// edit and one reference repo and checks the typed document, including the
// dirty flip after a worktree edit. The human output is unchanged by the
// flag's absence.
func TestCLISpaceStatusJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	captureShellChdir(t, func() { runCLI(t, "space", "create", "ex-1234", "-e", "repo-a", "-r", "repo-b") })
	spacePath := filepath.Join(home, "stave", "agent-work", "ex-1234")

	human := runCLI(t, "space", "status", "ex-1234")
	if !strings.HasPrefix(human, "space ex-1234 ()\npath: "+spacePath+"\n") || strings.Contains(human, "{") {
		t.Fatalf("human space status output changed:\n%s", human)
	}

	raw := runCLI(t, "space", "status", "ex-1234", "--json")
	var doc spaceStatusJSON
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("space status --json invalid: %v\n%s", err, raw)
	}
	if doc.SpaceID != "ex-1234" || doc.SpacePath != spacePath || doc.Manifest.ID != "ex-1234" || len(doc.Manifest.Repos) != 2 || doc.Manifest.CreatedAt.IsZero() {
		t.Fatalf("space status --json header = %#v", doc)
	}
	if doc.Memories == nil || len(doc.Memories) != 0 || !strings.Contains(raw, `"memories": []`) {
		t.Fatalf("space status --json memories should be an empty array:\n%s", raw)
	}
	if len(doc.Repos) != 2 {
		t.Fatalf("space status --json repos = %#v", doc.Repos)
	}
	repoRows := func(d spaceStatusJSON) map[string]spaceRepoStatusJSON {
		m := map[string]spaceRepoStatusJSON{}
		for _, r := range d.Repos {
			m[r.Name] = r
		}
		return m
	}
	rows := repoRows(doc)
	edit, ref := rows["repo-a"], rows["repo-b"]
	if edit.Name != "repo-a" || edit.Mode != space.ModeEdit || edit.Path != filepath.Join(spacePath, "repo-a") || !edit.Exists || edit.Dirty || edit.DirtyOutput != "" || edit.Ahead != 0 || edit.Behind != 0 || edit.DriftError != "" || edit.Branch == "" || edit.Base == "" || edit.Ref != "" {
		t.Fatalf("edit repo row = %#v", edit)
	}
	if ref.Name != "repo-b" || ref.Mode != space.ModeReference || ref.Path != filepath.Join(spacePath, "references", "repo-b") || !ref.Exists || ref.Dirty || ref.Ref == "" || ref.Branch != "" || ref.ReferenceWarn != "" {
		t.Fatalf("reference repo row = %#v", ref)
	}
	// Manifest keys use the yaml spellings, and repo rows expose the probe.
	for _, key := range []string{`"spaceId"`, `"spacePath"`, `"manifest"`, `"createdAt"`, `"bareRepoPath"`, `"exists"`, `"dirty"`, `"ahead"`, `"behind"`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("space status --json missing key %s:\n%s", key, raw)
		}
	}

	if err := os.WriteFile(filepath.Join(spacePath, "repo-a", "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, "space", "status", "ex-1234", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	rows = repoRows(doc)
	if !rows["repo-a"].Dirty || !strings.Contains(rows["repo-a"].DirtyOutput, "scratch.txt") || rows["repo-b"].Dirty {
		t.Fatalf("dirty flip not reflected: %#v", doc.Repos)
	}
}

// TestCLIReposListJSON checks `repos list --json` mirrors the --verbose text
// path: registry fields, description, default branch, and tether counts.
func TestCLIReposListJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "describe", "repo-a", "the", "API", "service")
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")

	verbose := runCLI(t, "repos", "list", "--verbose")
	if !strings.Contains(verbose, "repo-a\t"+srcA+"\t"+filepath.Join(home, "stave", "bare-repos", "repo-a.git")+"\tthe API service\ttethers=1\n") {
		t.Fatalf("repos list --verbose text changed:\n%s", verbose)
	}

	raw := runCLI(t, "repos", "list", "--json")
	var rows []reposListRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("repos list --json invalid: %v\n%s", err, raw)
	}
	if len(rows) != 2 || rows[0].Name != "repo-a" || rows[1].Name != "repo-b" {
		t.Fatalf("repos list --json rows (want sorted by name) = %#v", rows)
	}
	a, b := rows[0], rows[1]
	if a.URL != srcA || a.BareRepoPath != filepath.Join(home, "stave", "bare-repos", "repo-a.git") || a.DefaultBranch != "main" || a.Description != "the API service" || a.TetherCount != 1 {
		t.Fatalf("repo-a row = %#v", a)
	}
	if b.URL != srcB || b.Description != "" || b.TetherCount != 0 || b.DefaultBranch != "main" {
		t.Fatalf("repo-b row = %#v", b)
	}
	if strings.Contains(raw, `"description": ""`) || !strings.Contains(raw, `"tetherCount": 0`) {
		t.Fatalf("repos list --json omitempty shape wrong:\n%s", raw)
	}
}
