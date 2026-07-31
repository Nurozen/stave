package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
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

	var out string
	requestedDir := captureShellChdir(t, func() {
		out = runCLI(t, "review", "repo-a#7")
	})
	if !strings.Contains(out, "review space review-repo-a-7 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	// The bare-name path cannot reach gh, so the spec degrades gracefully.
	if !strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("review output missing degraded-metadata note:\n%s", out)
	}

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	if requestedDir != spacePath {
		t.Fatalf("review shell chdir request = %q, want space root %q", requestedDir, spacePath)
	}
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

// TestCLIReviewRendersFetchedMetadata is the gh success path: the PR is
// referenced with an owner so metadata is fetched, but through an injected
// fake runner returning canned gh JSON, and the rendered spec carries it.
// The repo resolves by registered name first, so no GitHub clone happens.
func TestCLIReviewRendersFetchedMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/42/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	var gotArgs []string
	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(`{
			"title": "Add widget",
			"body": "This adds a widget.",
			"state": "OPEN",
			"isDraft": false,
			"baseRefName": "main",
			"headRefName": "feature",
			"additions": 10,
			"deletions": 2,
			"changedFiles": 3,
			"author": {"login": "octocat"},
			"statusCheckRollup": [{"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"}]
		}`), nil
	}}
	out := runCLIWithApp(t, application, "review", "octocat/repo-a#42")
	if !strings.Contains(out, "review space review-repo-a-42 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	if strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("success path must not degrade:\n%s", out)
	}
	wantArgs := []string{"gh", "pr", "view", "https://github.com/octocat/repo-a/pull/42",
		"--json", "title,body,state,isDraft,baseRefName,headRefName,additions,deletions,changedFiles,url,author,statusCheckRollup"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("gh invocation = %#v, want %#v", gotArgs, wantArgs)
	}
	for i, want := range wantArgs {
		if gotArgs[i] != want {
			t.Fatalf("gh invocation = %#v, want %#v", gotArgs, wantArgs)
		}
	}

	spec, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "review-repo-a-42", "spec", "pr-42.md"))
	if err != nil {
		t.Fatalf("review spec missing: %v", err)
	}
	got := string(spec)
	for _, want := range []string{
		"# Review: PR #42 — Add widget",
		"- Author: octocat",
		"- State: open",
		"- Branches: feature -> main",
		"- Size: 3 files changed, +10/-2",
		"  - build: success",
		"This adds a widget.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review spec missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "could not be fetched") {
		t.Fatalf("review spec unexpectedly carries the degrade note:\n%s", got)
	}
}

func TestCLIReviewSummonForwardsAgentFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	launcher := &fakeSummonLauncher{}
	application := &app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }}
	requestedDir := captureShellChdir(t, func() {
		runCLIWithApp(t, application, "review", "repo-a#7", "--summon", "claude", "--dangerously-skip-permissions")
	})
	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	if requestedDir != spacePath {
		t.Fatalf("review shell chdir request = %q, want %q", requestedDir, spacePath)
	}
	if !launcher.called || len(launcher.invocation.Args) != 2 || launcher.invocation.Args[0] != "--dangerously-skip-permissions" || launcher.invocation.Args[1] != "/pr-teach" {
		t.Fatalf("review summon invocation = %#v", launcher.invocation)
	}
	if launcher.chdirEnv != "" {
		t.Fatalf("summoned agent inherited %s=%q", shellChdirFDEnv, launcher.chdirEnv)
	}
}

func TestCLIReviewWithReferenceRepo(t *testing.T) {
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
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)
	ctxSrc := createGitRepo(t, "ctx-repo")

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "repos", "add", "ctx-repo", ctxSrc)

	out := runCLI(t, "review", "repo-a#7", "-r", "ctx-repo")
	if !strings.Contains(out, "review space review-repo-a-7 is ready") {
		t.Fatalf("review output = %s", out)
	}
	refPath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "references", "ctx-repo")
	if _, err := os.Stat(refPath); err != nil {
		t.Fatalf("reference worktree missing: %v", err)
	}
	skill, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "review-repo-a-7", ".claude", "skills", "pr-teach", "SKILL.md"))
	if err != nil {
		t.Fatalf("embedded review skill not installed: %v", err)
	}
	if !strings.Contains(string(skill), "name: pr-teach") {
		t.Fatalf("installed skill content unexpected:\n%.200s", skill)
	}
	agents, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "reference at `references/ctx-repo`") {
		t.Fatalf("AGENTS.md missing reference entry:\n%s", agents)
	}
}

// writeStubMarmot installs a shell script standing in for the marmot binary:
// den create answers with a schema-1 envelope echoing the requested den id.
func writeStubMarmot(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(binDir, "marmot")
	script := `#!/bin/sh
if [ "$1" = "den" ] && [ "$2" = "create" ]; then
  printf '{"schema":1,"den_id":"%s","pointer_written":false}\n' "$3"
  exit 0
fi
printf '{"schema":1}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// rewriteMemoryConfig edits the memory block `stave setup` wrote to config.yaml.
func rewriteMemoryConfig(t *testing.T, home string, old, new string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".config", "stave", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	replaced := strings.Replace(string(data), old, new, 1)
	if replaced == string(data) {
		t.Fatalf("config.yaml missing %q:\n%s", old, data)
	}
	if err := os.WriteFile(cfgPath, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCLIReviewWithMemoryFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	stub := writeStubMarmot(t, home)
	rewriteMemoryConfig(t, home, "binary: marmot", "binary: "+stub)

	out := runCLI(t, "review", "repo-a#7", "--memory", ".")
	if !strings.Contains(out, "review space review-repo-a-7 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	if !strings.Contains(out, "attached memory default") {
		t.Fatalf("review output missing attach line:\n%s", out)
	}

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	mem := manifest.Memories[0]
	if mem.Name != "default" || mem.Provider != "marmot" || mem.ID != "review-repo-a-7" || !mem.Owned {
		t.Fatalf("attachment = %#v", mem)
	}

	// Attach rewrote AGENTS.md so the /pr-teach Claude path (which bypasses
	// the memory-aware prompt) still surfaces the den to the agent.
	agents, err := os.ReadFile(filepath.Join(spacePath, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "context-marmot MCP tools (den: review-repo-a-7)") {
		t.Fatalf("AGENTS.md missing memory line:\n%s", agents)
	}

	// space status renders a compact memory row.
	status := runCLI(t, "space", "status", "review-repo-a-7")
	if !strings.Contains(status, "[memory] default marmot den=review-repo-a-7 owned") {
		t.Fatalf("status missing memory row:\n%s", status)
	}

	// Non-Claude summoners get the memory bullet in the generated prompt.
	summonOut := runCLI(t, "summon", "review-repo-a-7", "--with", "codex", "--print-command")
	if !strings.Contains(summonOut, "context-marmot MCP tools (den: review-repo-a-7)") {
		t.Fatalf("codex summon command missing memory bullet:\n%s", summonOut)
	}
}

func TestCLIReviewAmbientMemoryDegrades(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	rewriteMemoryConfig(t, home, "binary: marmot", "default: true\n    binary: "+filepath.Join(home, "no-such-marmot"))

	out := runCLI(t, "review", "repo-a#7")
	if !strings.Contains(out, "review space review-repo-a-7 is ready") {
		t.Fatalf("ambient degrade must not fail review:\n%s", out)
	}
	if !strings.Contains(out, "notice: ambient memory attach failed") {
		t.Fatalf("expected ambient degrade notice:\n%s", out)
	}
	manifest, err := space.LoadManifest(filepath.Join(home, "stave", "agent-work", "review-repo-a-7"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("ambient degrade must leave no memories: %#v", manifest.Memories)
	}
	if len(manifest.Repos) != 1 {
		t.Fatalf("space must survive with its repo: %#v", manifest.Repos)
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
