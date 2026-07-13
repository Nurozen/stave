package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

func TestParsePRRef(t *testing.T) {
	valid := []struct {
		name string
		in   string
		want prRef
	}{
		{name: "full url", in: "https://github.com/octocat/hello-world/pull/42", want: prRef{Owner: "octocat", Repo: "hello-world", Number: 42}},
		{name: "url with trailing segment", in: "https://github.com/octocat/hello-world/pull/42/files", want: prRef{Owner: "octocat", Repo: "hello-world", Number: 42}},
		{name: "url with .git", in: "https://github.com/octocat/hello-world.git/pull/42", want: prRef{Owner: "octocat", Repo: "hello-world", Number: 42}},
		{name: "owner/repo#n", in: "octocat/hello-world#123", want: prRef{Owner: "octocat", Repo: "hello-world", Number: 123}},
		{name: "registered repo#n", in: "my-repo#7", want: prRef{Owner: "", Repo: "my-repo", Number: 7}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePRRef(tc.in)
			if err != nil {
				t.Fatalf("parsePRRef(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parsePRRef(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}

	invalid := []struct {
		name string
		in   string
	}{
		{name: "garbage", in: "not-a-pull-request"},
		{name: "missing number", in: "octocat/hello-world#"},
		{name: "plain url without pull", in: "https://github.com/octocat/hello-world"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parsePRRef(tc.in); err == nil {
				t.Fatalf("parsePRRef(%q) = %#v, want error", tc.in, got)
			}
		})
	}
}

func TestSameCloneURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{name: "https vs ssh", a: "https://github.com/o/r.git", b: "git@github.com:o/r.git", want: true},
		{name: "trailing .git", a: "https://github.com/o/r", b: "https://github.com/o/r.git", want: true},
		{name: "case insensitive", a: "https://github.com/O/R", b: "https://github.com/o/r", want: true},
		{name: "http vs https", a: "http://github.com/o/r", b: "https://github.com/o/r", want: true},
		{name: "different repo", a: "https://github.com/o/r", b: "https://github.com/o/other", want: false},
		{name: "different owner", a: "https://github.com/o/r", b: "https://github.com/other/r", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameCloneURL(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameCloneURL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestCLIReviewEndToEnd drives `stave review <repo>#N` against a local git
// fixture standing in for GitHub: the source repo is the bare mirror's origin,
// and refs/pull/N/head is created there so FetchRefspec resolves it. The PR is
// referenced by registered repo name, so gh metadata is never touched and the
// review spec takes its degraded path.
func TestCLIReviewEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	runGit(t, src, "checkout", "-b", "pr-branch")
	if err := os.WriteFile(filepath.Join(src, "feature.txt"), []byte("pr change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "feature.txt")
	runGit(t, src, "commit", "-m", "pr change")
	prSHA := gitOutput(t, src, "rev-parse", "pr-branch")
	runGit(t, src, "checkout", "main")
	// GitHub exposes PR heads at refs/pull/N/head; simulate that on origin.
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	out := runCLI(t, "review", "repo-a#7")
	if !strings.Contains(out, "review space review-repo-a-7 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	// The bare-name path cannot reach gh, so the spec degrades gracefully.
	if !strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("review output missing degraded-metadata note:\n%s", out)
	}

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	worktree := filepath.Join(spacePath, "repo-a")
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("review worktree missing: %v", err)
	}
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != prSHA {
		t.Fatalf("worktree HEAD = %q, want PR head %q", head, prSHA)
	}

	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "review" {
		t.Fatalf("manifest kind = %q, want review", manifest.Kind)
	}
	if len(manifest.Repos) != 1 {
		t.Fatalf("manifest repos = %#v", manifest.Repos)
	}
	if manifest.Repos[0].Base != "origin/main" {
		t.Fatalf("drift base = %q, want origin/main", manifest.Repos[0].Base)
	}

	specFile := filepath.Join(spacePath, "spec", "pr-7.md")
	specBytes, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatalf("review spec missing: %v", err)
	}
	if !strings.Contains(string(specBytes), "git diff origin/main...HEAD") {
		t.Fatalf("review spec missing quickstart diff command:\n%s", specBytes)
	}

	diff := gitOutput(t, worktree, "diff", "origin/main...HEAD")
	if !strings.Contains(diff, "feature.txt") || !strings.Contains(diff, "pr change") {
		t.Fatalf("PR diff missing change:\n%s", diff)
	}
}

func TestCLIReviewSpaceCollisionErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "review", "repo-a#7")

	if _, err := runCLIError(t, nil, "review", "repo-a#7"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected space-collision error, got %v", err)
	}
}

func TestCLIReviewUnregisteredBareNameErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	if _, err := runCLIError(t, nil, "review", "ghost#7"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected unregistered-repo error, got %v", err)
	}
}

func TestStageReviewSpecRendersFullMetadata(t *testing.T) {
	meta := prMetadata{
		Title:        "Add widget",
		Body:         "This adds a widget.\n\n- verify the loader",
		State:        "OPEN",
		IsDraft:      true,
		BaseRefName:  "main",
		HeadRefName:  "feature",
		Additions:    10,
		Deletions:    2,
		ChangedFiles: 3,
	}
	meta.Author.Login = "octocat"
	meta.StatusCheckRollup = append(meta.StatusCheckRollup, struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"})

	ref := prRef{Owner: "octocat", Repo: "hello-world", Number: 42}
	specFile, err := stageReviewSpec("hello-world", ref, meta, true, "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specFile)) }()

	body, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"# Review: PR #42 — Add widget",
		"- URL: https://github.com/octocat/hello-world/pull/42",
		"- Author: octocat",
		"- State: open (draft)",
		"- Branches: feature -> main",
		"- Size: 3 files changed, +10/-2",
		"- Checks:",
		"  - build: success",
		"git diff origin/main...HEAD",
		"## Author's description (claims, not facts — verify against the code)",
		"This adds a widget.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review spec missing %q:\n%s", want, got)
		}
	}
}

func TestStageReviewSpecMinimalNotesGhUnavailable(t *testing.T) {
	ref := prRef{Owner: "", Repo: "my-repo", Number: 7}
	specFile, err := stageReviewSpec("my-repo", ref, prMetadata{}, false, "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specFile)) }()

	body, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "# Review: PR #7") {
		t.Fatalf("minimal spec missing header:\n%s", got)
	}
	if !strings.Contains(got, "git diff origin/main...HEAD") {
		t.Fatalf("minimal spec missing quickstart:\n%s", got)
	}
	if !strings.Contains(got, "PR title/description could not be fetched (gh CLI unavailable or unauthenticated)") {
		t.Fatalf("minimal spec missing gh-unavailable note:\n%s", got)
	}
	// Without an owner there is no PR URL to render.
	if strings.Contains(got, "https://github.com") {
		t.Fatalf("minimal spec unexpectedly rendered a URL:\n%s", got)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
