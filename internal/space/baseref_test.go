package space

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
)

func TestResolveRefSpelling(t *testing.T) {
	remotes := []string{"origin", "fork"}
	tests := []struct {
		ref      string
		remotes  []string
		full     string
		spelling string
	}{
		// A non-origin remote resolves against that remote, and keeps the
		// unambiguous full spelling.
		{"fork/main", remotes, "refs/remotes/fork/main", "refs/remotes/fork/main"},
		{"fork/release/2.0", remotes, "refs/remotes/fork/release/2.0", "refs/remotes/fork/release/2.0"},
		// origin keeps its short spelling: that is what manifests already carry.
		{"origin/main", remotes, "refs/remotes/origin/main", "origin/main"},
		{"main", remotes, "refs/remotes/origin/main", "origin/main"},
		// A first segment that names no remote is still a branch on origin.
		{"fork/main", []string{"origin"}, "refs/remotes/origin/fork/main", "origin/fork/main"},
		{"feature/login", remotes, "refs/remotes/origin/feature/login", "origin/feature/login"},
		// Full refs pass through untouched.
		{"refs/heads/stave/x/repo-a", remotes, "refs/heads/stave/x/repo-a", "refs/heads/stave/x/repo-a"},
		{"refs/remotes/fork/main", remotes, "refs/remotes/fork/main", "refs/remotes/fork/main"},
		{" main ", remotes, "refs/remotes/origin/main", "origin/main"},
		// A mirror with a single, differently-named remote resolves bare
		// branches against it; an explicit origin/ is left to fail honestly.
		{"main", []string{"fork"}, "refs/remotes/fork/main", "refs/remotes/fork/main"},
		{"origin/main", []string{"fork"}, "refs/remotes/origin/main", "origin/main"},
		{"main", []string{"fork", "upstream"}, "refs/remotes/origin/main", "origin/main"},
	}
	for _, tt := range tests {
		full, spelling := resolveRefSpelling(tt.ref, tt.remotes)
		if full != tt.full || spelling != tt.spelling {
			t.Fatalf("resolveRefSpelling(%q, %v) = %q, %q; want %q, %q", tt.ref, tt.remotes, full, spelling, tt.full, tt.spelling)
		}
	}
}

// TestAddRepoResolvesNonOriginRemoteBase is the reproduction: a mirror with a
// second remote must resolve "fork/main" against fork, not mint the
// origin/fork/main that never existed. It runs against real git so the
// worktree's actual head proves the ref resolved.
func TestAddRepoResolvesNonOriginRemoteBase(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	client := git.New()

	upstream := filepath.Join(t.TempDir(), "upstream")
	startGit(t, "", "init", "-b", "main", upstream)
	startGit(t, upstream, "config", "user.name", "Test User")
	startGit(t, upstream, "config", "user.email", "test@example.test")
	writeAndCommit(t, upstream, "upstream.txt", "upstream\n", "upstream base")
	upstreamSHA := startGitOutput(t, upstream, "rev-parse", "HEAD")

	// The fork is an independent repo whose main has diverged.
	forkSrc := filepath.Join(t.TempDir(), "fork")
	startGit(t, "", "clone", upstream, forkSrc)
	startGit(t, forkSrc, "config", "user.name", "Test User")
	startGit(t, forkSrc, "config", "user.email", "test@example.test")
	writeAndCommit(t, forkSrc, "fork.txt", "fork\n", "fork-only change")
	forkSHA := startGitOutput(t, forkSrc, "rev-parse", "HEAD")
	if forkSHA == upstreamSHA {
		t.Fatal("fork and upstream heads must differ for a meaningful check")
	}

	bare := filepath.Join(root, "bare-repos", "repo-a.git")
	if err := client.CloneBare(ctx, upstream, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	startGit(t, "", "--git-dir", bare, "remote", "add", "fork", forkSrc)
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}

	cfg := config.Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  "main",
		Repos: map[string]config.Repository{
			"repo-a": {Name: "repo-a", URL: upstream, BareRepoPath: bare, DefaultBranch: "main"},
		},
	}
	if err := cfg.EnsureRootDirs(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc := NewService(cfg, client, &out)

	// The dry-run plan names the ref the real run would use.
	if err := svc.Create(ctx, CreateOptions{ID: "dry-fork", Edits: []RepoSpec{{Name: "repo-a", Ref: "fork/main"}}, DryRun: true}); err != nil {
		t.Fatalf("Create(fork base, dry-run) error = %v", err)
	}
	if !strings.Contains(out.String(), "from refs/remotes/fork/main ") {
		t.Fatalf("dry-run did not resolve against the fork remote:\n%s", out.String())
	}
	if strings.Contains(out.String(), "origin/fork/main") {
		t.Fatalf("dry-run still prefixed the fork remote with origin/:\n%s", out.String())
	}

	if err := svc.Create(ctx, CreateOptions{ID: "fork-1", Edits: []RepoSpec{{Name: "repo-a", Ref: "fork/main"}}}); err != nil {
		t.Fatalf("Create(fork base) error = %v", err)
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, "fork-1")
	if head := startGitOutput(t, filepath.Join(spacePath, "repo-a"), "rev-parse", "HEAD"); head != forkSHA {
		t.Fatalf("worktree HEAD = %q, want fork/main %q", head, forkSHA)
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Repos[0].Base != "refs/remotes/fork/main" {
		t.Fatalf("manifest Base = %q, want refs/remotes/fork/main", manifest.Repos[0].Base)
	}

	// A reference worktree resolves the same way.
	if err := svc.AddRepo(ctx, AddOptions{SpaceID: "fork-1", RepoName: "repo-a", Mode: ModeReference, Ref: "fork/main", NoFetch: true}); err != nil {
		t.Fatalf("AddRepo(fork reference) error = %v", err)
	}
	if head := startGitOutput(t, filepath.Join(spacePath, "references", "repo-a"), "rev-parse", "HEAD"); head != forkSHA {
		t.Fatalf("reference worktree HEAD = %q, want fork/main %q", head, forkSHA)
	}

	// An unresolvable base is refused by stave, naming what it looked for and
	// what the mirror actually has — never as a raw 'git worktree add' failure.
	err = svc.Create(ctx, CreateOptions{ID: "ghost-1", Edits: []RepoSpec{{Name: "repo-a", Ref: "nosuch/main"}}})
	if ErrorCode(err) != CodeRefNotFound {
		t.Fatalf("Create(unknown remote) error = %v (code %q), want %q", err, ErrorCode(err), CodeRefNotFound)
	}
	if !strings.Contains(err.Error(), "refs/remotes/origin/nosuch/main") || !strings.Contains(err.Error(), "fork") {
		t.Fatalf("error does not name the ref tried and the remotes available: %v", err)
	}
	details := ErrorDetails(err)
	if details["tried"] != "refs/remotes/origin/nosuch/main" || details["ref"] != "nosuch/main" {
		t.Fatalf("details = %#v", details)
	}
	if remotes, ok := details["remotes"].([]string); !ok || !reflect.DeepEqual(remotes, []string{"fork", "origin"}) {
		t.Fatalf("details remotes = %#v", details["remotes"])
	}
	if _, statErr := os.Stat(filepath.Join(cfg.AgentWorkDir, "ghost-1", "repo-a")); !os.IsNotExist(statErr) {
		t.Fatalf("worktree materialized despite the unresolvable base: %v", statErr)
	}
}

// TestResolveRefFallsBackWhenRemotesUnreadable pins the degrade path: a mirror
// that cannot be interrogated keeps the offline origin/ spelling rather than
// inventing a resolution failure — but a local ref, which no fetch could
// conjure, is still checked.
func TestResolveRefFallsBackWhenRemotesUnreadable(t *testing.T) {
	svc, fg, cfg := testService(t)
	bare := cfg.Repos["repo-a"].BareRepoPath
	fg.remotesErr = os.ErrNotExist
	got, err := svc.resolveRef(context.Background(), "repo-a", bare, "main", true)
	if err != nil {
		t.Fatalf("resolveRef() error = %v", err)
	}
	if got != "origin/main" {
		t.Fatalf("resolveRef() = %q, want origin/main", got)
	}
	if containsCallPrefix(fg.calls, "ref-exists|") {
		t.Fatalf("existence was probed against an unreadable mirror: %#v", fg.calls)
	}
	fg.refMissing = true
	if _, err := svc.resolveRef(context.Background(), "repo-a", bare, "refs/heads/stave/x/repo-a", false); ErrorCode(err) != CodeRefNotFound {
		t.Fatalf("local ref check skipped on an unreadable mirror: %v", err)
	}
	if _, err := svc.resolveRef(context.Background(), "repo-a", bare, "  ", true); ErrorCode(err) != CodeInvalidArguments {
		t.Fatalf("empty ref error = %v (code %q)", err, ErrorCode(err))
	}
}

// TestResolveRefOnlyRefusesWhatAFetchCannotFix is the guard against making
// --dry-run and --no-fetch stricter than the real, fetching run they preview.
// A missing remote-tracking ref is conclusive only against a mirror the call
// just refreshed; a missing local ref always is.
func TestResolveRefOnlyRefusesWhatAFetchCannotFix(t *testing.T) {
	svc, fg, cfg := testService(t)
	var out strings.Builder
	svc.Out = &out
	bare := cfg.Repos["repo-a"].BareRepoPath
	fg.refMissing = true
	ctx := context.Background()

	// Not fetched this call: the branch may simply not be mirrored yet.
	got, err := svc.resolveRef(ctx, "repo-a", bare, "just-pushed", false)
	if err != nil {
		t.Fatalf("stale-mirror miss refused: %v", err)
	}
	if got != "origin/just-pushed" {
		t.Fatalf("resolveRef() = %q", got)
	}
	if !strings.Contains(out.String(), "warning:") || !strings.Contains(out.String(), "refs/remotes/origin/just-pushed") {
		t.Fatalf("stale-mirror miss was silent:\n%s", out.String())
	}

	// Fetched this call: the same miss is conclusive.
	if _, err := svc.resolveRef(ctx, "repo-a", bare, "just-pushed", true); ErrorCode(err) != CodeRefNotFound {
		t.Fatalf("fresh-mirror miss = %v (code %q)", err, ErrorCode(err))
	}
	// Stave never pushes its own branches, so a missing one is conclusive
	// however stale the mirror is.
	if _, err := svc.resolveRef(ctx, "repo-a", bare, "refs/heads/stave/ghost/repo-a", false); ErrorCode(err) != CodeRefNotFound {
		t.Fatalf("local ref miss = %v (code %q)", err, ErrorCode(err))
	}
}

// TestAddRepoDoesNotRefuseWhatTheRealRunWouldFetch covers the two callers that
// reach resolveRef with an unrefreshed mirror: --dry-run (fetch is only
// printed) and --no-fetch (which `stave review` uses after fetching the PR ref
// itself, for a base branch that may have been deleted on merge).
func TestAddRepoDoesNotRefuseWhatTheRealRunWouldFetch(t *testing.T) {
	svc, fg, _ := testService(t)
	var out strings.Builder
	svc.Out = &out
	ctx := context.Background()
	fg.refMissing = true

	if err := svc.Create(ctx, CreateOptions{ID: "dr-1", Edits: []RepoSpec{{Name: "repo-a", Ref: "unfetched"}}, DryRun: true}); err != nil {
		t.Fatalf("dry-run refused an unfetched base: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: add edit worktree stave/dr-1/repo-a from origin/unfetched") {
		t.Fatalf("dry-run plan:\n%s", out.String())
	}

	out.Reset()
	if err := svc.InitSpace(ctx, InitOptions{ID: "nf-rev"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(ctx, AddOptions{
		SpaceID: "nf-rev", RepoName: "repo-a", Mode: ModeEdit,
		Base: "deleted-on-merge", StartPoint: "pr/7", NoFetch: true,
	}); err != nil {
		t.Fatalf("--no-fetch refused a base the mirror has not seen: %v", err)
	}
	if !strings.Contains(out.String(), "warning:") {
		t.Fatalf("missing base was not reported at all:\n%s", out.String())
	}
	manifest, err := LoadManifest(svc.SpacePath("nf-rev"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Repos[0].Base != "origin/deleted-on-merge" {
		t.Fatalf("manifest Base = %q", manifest.Repos[0].Base)
	}

	// A real (fetching) run still refuses: that is the must-fix case.
	if err := svc.Create(ctx, CreateOptions{ID: "hard-1", Edits: []RepoSpec{{Name: "repo-a", Ref: "unfetched"}}}); ErrorCode(err) != CodeRefNotFound {
		t.Fatalf("fetching create = %v (code %q)", err, ErrorCode(err))
	}
}

// TestMemoryRefHintStripsMirrorLocalSpellings: memory providers resolve against
// the clone URL, so the mirror-local refs/remotes/ spelling is reduced to the
// branch it names and every pre-existing spelling passes through untouched.
func TestMemoryRefHintStripsMirrorLocalSpellings(t *testing.T) {
	for ref, want := range map[string]string{
		"refs/remotes/fork/main":          "main",
		"refs/remotes/origin/release/2.0": "release/2.0",
		"origin/main":                     "origin/main",
		"main":                            "main",
		"refs/heads/stave/x/repo-a":       "refs/heads/stave/x/repo-a",
		"":                                "",
	} {
		if got := memoryRefHint(ref); got != want {
			t.Fatalf("memoryRefHint(%q) = %q, want %q", ref, got, want)
		}
	}
}

func writeAndCommit(t *testing.T, dir, name, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	startGit(t, dir, "add", name)
	startGit(t, dir, "commit", "-m", message)
}
