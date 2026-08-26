package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/tether"
)

// tetherFilePath returns the repo-tethers.yaml sidecar location for the test HOME.
func tetherFilePath(home string) string {
	return filepath.Join(home, "stave", "repo-tethers.yaml")
}

// setupThreeRepos runs setup and registers repo-a/b/c against fresh git repos.
func setupThreeRepos(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	srcC := createGitRepo(t, "repo-c")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "repos", "add", "repo-c", srcC)
	return home
}

func TestCLIReposDescribeSetPrintAndUnset(t *testing.T) {
	setupThreeRepos(t)

	// Unset description errors.
	if out, err := runCLIError(t, nil, "repos", "describe", "repo-a"); err == nil || !strings.Contains(err.Error(), "no description") {
		t.Fatalf("describe unset error = %v\n%s", err, out)
	}
	// Set (multi-word) then print.
	runCLI(t, "repos", "describe", "repo-a", "the", "API", "service")
	out := runCLI(t, "repos", "describe", "repo-a")
	if strings.TrimSpace(out) != "the API service" {
		t.Fatalf("describe print = %q", out)
	}
	// Unknown repo errors.
	if _, err := runCLIError(t, nil, "repos", "describe", "nope", "x"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("describe unknown repo error = %v", err)
	}
}

func TestCLIReposListVerbose(t *testing.T) {
	setupThreeRepos(t)
	runCLI(t, "repos", "describe", "repo-a", "the api")
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")

	out := runCLI(t, "repos", "list", "--verbose")
	if !strings.Contains(out, "the api") {
		t.Fatalf("verbose list missing description:\n%s", out)
	}
	if !strings.Contains(out, "tethers=1") {
		t.Fatalf("verbose list missing tether count:\n%s", out)
	}
	// Plain list is unaffected.
	plain := runCLI(t, "repos", "list")
	if strings.Contains(plain, "tethers=") {
		t.Fatalf("plain list leaked verbose columns:\n%s", plain)
	}
}

func TestCLIReposTetherPinAndForget(t *testing.T) {
	home := setupThreeRepos(t)

	// Default pin is strong + reference mode (OQ-D).
	out := runCLI(t, "repos", "tether", "repo-a", "repo-b")
	if !strings.Contains(out, "pinned repo-a -> repo-b as strong") {
		t.Fatalf("default pin output = %q", out)
	}
	// Mutual exclusion errors.
	if _, err := runCLIError(t, nil, "repos", "tether", "repo-a", "repo-c", "--strong", "--weak"); err == nil || !strings.Contains(err.Error(), "at most one of --strong") {
		t.Fatalf("strong+weak error = %v", err)
	}
	if _, err := runCLIError(t, nil, "repos", "tether", "repo-a", "repo-c", "--edit", "--reference"); err == nil || !strings.Contains(err.Error(), "at most one of --edit") {
		t.Fatalf("edit+reference error = %v", err)
	}
	// Unregistered repo rejected.
	if _, err := runCLIError(t, nil, "repos", "tether", "repo-a", "ghost"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered to error = %v", err)
	}
	// Self-tether rejected (from == to is invalid).
	if _, err := runCLIError(t, nil, "repos", "tether", "repo-a", "repo-a"); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("self-tether error = %v", err)
	}

	// Verify the default pin recorded reference mode.
	rows := reposTethersJSON(t, "repo-a")
	if len(rows) != 1 || rows[0].To != "repo-b" || rows[0].ToMode != "reference" || rows[0].Strength != "strong" {
		t.Fatalf("rows after default pin = %+v", rows)
	}

	// forget requires <to> or --all.
	if _, err := runCLIError(t, nil, "repos", "forget", "repo-a"); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("forget without to error = %v", err)
	}
	// forget single.
	runCLI(t, "repos", "tether", "repo-a", "repo-c", "--weak")
	runCLI(t, "repos", "forget", "repo-a", "repo-b")
	rows = reposTethersJSON(t, "repo-a")
	if len(rows) != 1 || rows[0].To != "repo-c" {
		t.Fatalf("rows after single forget = %+v", rows)
	}
	// forget --all.
	runCLI(t, "repos", "forget", "repo-a", "--all")
	rows = reposTethersJSON(t, "repo-a")
	if len(rows) != 0 {
		t.Fatalf("rows after forget --all = %+v", rows)
	}
	_ = home
}

// A manual --strong pin on a low-count (count 0) tether must persist a
// recomputed Strength of "strong" in the sidecar file, not the weak
// classification the low count would otherwise derive (recompute-on-write, D6).
func TestCLIReposTetherPinRecomputesPersistedStrength(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")

	f, err := tether.Load(tetherFilePath(home))
	if err != nil {
		t.Fatalf("load tether file: %v", err)
	}
	if len(f.Tethers) != 1 {
		t.Fatalf("expected one persisted tether, got %+v", f.Tethers)
	}
	tt := f.Tethers[0]
	if tt.Count != 0 {
		t.Fatalf("expected low-count pin (count 0), got %+v", tt)
	}
	if tt.Pinned != tether.Strong {
		t.Fatalf("expected Pinned=strong, got %+v", tt)
	}
	if tt.Strength != tether.Strong {
		t.Fatalf("persisted Strength should reflect the pin (strong), got %q", tt.Strength)
	}
}

func TestCLIReposTethersTextAndJSON(t *testing.T) {
	setupThreeRepos(t)
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")
	runCLI(t, "repos", "tether", "repo-a", "repo-c", "--weak", "--edit")

	text := runCLI(t, "repos", "tethers", "repo-a")
	// Strong-first ordering.
	bIdx := strings.Index(text, "repo-b")
	cIdx := strings.Index(text, "repo-c")
	if bIdx < 0 || cIdx < 0 || bIdx > cIdx {
		t.Fatalf("tethers text ordering wrong:\n%s", text)
	}
	if !strings.Contains(text, "(pinned strong)") {
		t.Fatalf("tethers text missing pinned marker:\n%s", text)
	}
	// Text output must surface last-seen so it matches the --json payload.
	if !strings.Contains(text, "last-seen=") {
		t.Fatalf("tethers text missing last-seen:\n%s", text)
	}

	rows := reposTethersJSON(t, "repo-a")
	if len(rows) != 2 {
		t.Fatalf("json rows = %+v", rows)
	}
	if rows[0].To != "repo-b" || rows[0].Strength != "strong" {
		t.Fatalf("json row0 = %+v", rows[0])
	}
	if rows[1].To != "repo-c" || rows[1].Strength != "weak" || rows[1].ToMode != "edit" {
		t.Fatalf("json row1 = %+v", rows[1])
	}

	// Unknown repo errors.
	if _, err := runCLIError(t, nil, "repos", "tethers", "ghost"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("tethers unknown repo error = %v", err)
	}
}

func reposTethersJSON(t *testing.T, repo string) []tetherRow {
	t.Helper()
	out := runCLI(t, "repos", "tethers", repo, "--json")
	var rows []tetherRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal tethers json: %v\n%s", err, out)
	}
	return rows
}

func TestCLICreateCommonExpandsStrongTethers(t *testing.T) {
	home := setupThreeRepos(t)
	// Strong a->b, weak a->c.
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")
	runCLI(t, "repos", "tether", "repo-a", "repo-c", "--weak")

	runCLI(t, "space", "create", "cx-1", "-e", "repo-a", "-c")
	spacePath := filepath.Join(home, "stave", "agent-work", "cx-1")
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); err != nil {
		t.Fatalf("strong tether repo-b not expanded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-c")); err == nil {
		t.Fatalf("weak tether repo-c should not be expanded without --include-weak")
	}
}

func TestCLICreateCommonIncludeWeak(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "repos", "tether", "repo-a", "repo-c", "--weak")

	runCLI(t, "space", "create", "cx-2", "-e", "repo-a", "-c", "--include-weak")
	spacePath := filepath.Join(home, "stave", "agent-work", "cx-2")
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-c")); err != nil {
		t.Fatalf("weak tether repo-c not expanded with --include-weak: %v", err)
	}
}

func TestCLICreateCommonDedupsAgainstExplicit(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")
	runCLI(t, "repos", "tether", "repo-a", "repo-c", "--strong")

	// repo-b is explicit -r (dedup), repo-c is explicit -e (dedup); expansion
	// should add neither a duplicate repo-b nor repo-c-as-reference.
	runCLI(t, "space", "create", "cx-3", "-e", "repo-a", "-e", "repo-c", "-r", "repo-b", "-c")
	spacePath := filepath.Join(home, "stave", "agent-work", "cx-3")
	// repo-c stays an edit worktree (not a reference).
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-c")); err == nil {
		t.Fatalf("repo-c wrongly added as reference despite being an -e edit")
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-c")); err != nil {
		t.Fatalf("repo-c edit worktree missing: %v", err)
	}
}

func TestCLICreateCommonSkipsUnregistered(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "repos", "tether", "repo-a", "repo-b", "--strong")
	// Unregister repo-b after pinning; its tether persists but the repo is gone.
	runCLI(t, "repos", "remove", "repo-b")

	out := runCLI(t, "space", "create", "cx-4", "-e", "repo-a", "-c")
	if !strings.Contains(out, "skipping tethered repo") || !strings.Contains(out, "repo-b") {
		t.Fatalf("expected skip notice for unregistered repo-b:\n%s", out)
	}
	spacePath := filepath.Join(home, "stave", "agent-work", "cx-4")
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); err == nil {
		t.Fatalf("unregistered repo-b should not be materialized")
	}
}

func TestCLICreateNoLearnSuppressesCapture(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "space", "create", "nl-1", "-e", "repo-a", "-r", "repo-b", "--no-learn")
	if _, err := os.Stat(tetherFilePath(home)); !os.IsNotExist(err) {
		t.Fatalf("--no-learn should not write a tether file (stat err=%v)", err)
	}
}

func TestCLICreateLearnsCoOccurrence(t *testing.T) {
	home := setupThreeRepos(t)
	// Without --no-learn, an edit + reference create records repo-a -> repo-b.
	runCLI(t, "space", "create", "ln-1", "-e", "repo-a", "-r", "repo-b")
	if _, err := os.Stat(tetherFilePath(home)); err != nil {
		t.Fatalf("expected tether file after learning create: %v", err)
	}
	rows := reposTethersJSON(t, "repo-a")
	if len(rows) != 1 || rows[0].To != "repo-b" {
		t.Fatalf("learned rows = %+v", rows)
	}
}

func TestCLICreateDryRunWritesNoTetherFile(t *testing.T) {
	home := setupThreeRepos(t)
	runCLI(t, "space", "create", "dr-1", "-e", "repo-a", "-r", "repo-b", "--dry-run")
	if _, err := os.Stat(tetherFilePath(home)); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write a tether file (stat err=%v)", err)
	}
}

func TestCLISagaCreateRejectsCommonFlag(t *testing.T) {
	setupThreeRepos(t)
	if out, err := runCLIError(t, nil, "saga", "create", "sg-1", "-c"); err == nil {
		t.Fatalf("saga create -c should be rejected:\n%s", out)
	}
}

// TestCLIReviewLearnsPrHeadToSibling exercises OQ-A: a review that adds a
// sibling reference records the delta tether editRepo -> sibling.
func TestCLIReviewLearnsPrHeadToSibling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)
	ctxSrc := createGitRepo(t, "ctx-repo")

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "repos", "add", "ctx-repo", ctxSrc)

	runCLI(t, "review", "repo-a#7", "-r", "ctx-repo")

	rows := reposTethersJSON(t, "repo-a")
	if len(rows) != 1 || rows[0].To != "ctx-repo" {
		t.Fatalf("review did not learn repo-a -> ctx-repo: %+v", rows)
	}
}

func TestCLIPortalDetachHasNoYesFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runCLIError(t, nil, "portal", "detach", "sp-1", "--yes")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("portal detach --yes should be an unknown flag, got err=%v\n%s", err, out)
	}
}

// The tmux session name no longer carries an agent segment, so PlanLogs ignores
// LogsOptions.Agent; the dead --agent flag must be gone from `portal logs`.
func TestCLIPortalLogsHasNoAgentFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runCLIError(t, nil, "portal", "logs", "sp-1", "--agent", "foo")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("portal logs --agent should be an unknown flag, got err=%v\n%s", err, out)
	}
}
