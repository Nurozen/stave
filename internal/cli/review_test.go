package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
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

func TestClassifyRegisteredURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		wantTier2 bool
		wantNear  bool
	}{
		{name: "https github", url: "https://github.com/o/r.git", wantTier2: true},
		{name: "scp github", url: "git@github.com:o/r.git", wantTier2: true},
		{name: "ssh url github", url: "ssh://git@github.com/o/r", wantTier2: true},
		{name: "https with userinfo", url: "https://git@github.com/o/r.git", wantTier2: true},
		{name: "ssh url with user:pass", url: "ssh://user:pass@github.com/o/r", wantTier2: true},
		{name: "case-differing owner/repo", url: "https://github.com/O/R", wantTier2: true},
		{name: "without .git", url: "https://github.com/o/r", wantTier2: true},
		{name: "ssh alias", url: "git@hl_external:o/r.git", wantNear: true},
		{name: "github enterprise host", url: "https://github.example.com/o/r", wantNear: true},
		{name: "github.com with port", url: "ssh://git@github.com:2222/o/r", wantNear: true},
		// The scheme's default port spelled out is still plainly github.com;
		// the other scheme's default port is not.
		{name: "https with explicit 443", url: "https://github.com:443/o/r.git", wantTier2: true},
		{name: "http with explicit 443", url: "http://github.com:443/o/r", wantTier2: true},
		{name: "ssh url with explicit 22", url: "ssh://git@github.com:22/o/r", wantTier2: true},
		{name: "https with port 22", url: "https://github.com:22/o/r", wantNear: true},
		{name: "ssh url with port 443", url: "ssh://git@github.com:443/o/r", wantNear: true},
		// An scp-like alias literally named github.com is indistinguishable
		// from the real host; documented as tier 2.
		{name: "scp alias named github.com", url: "git@github.com:o/r", wantTier2: true},
		{name: "wrong owner", url: "https://github.com/other/r"},
		{name: "wrong repo", url: "https://github.com/o/other"},
		{name: "local path", url: "/srv/x.git"},
		{name: "file url", url: "file:///srv/o/r.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tier2, near := classifyRegisteredURL(tc.url, "o", "r")
			if tier2 != tc.wantTier2 || near != tc.wantNear {
				t.Fatalf("classifyRegisteredURL(%q, o, r) = (tier2 %v, near %v), want (tier2 %v, near %v)", tc.url, tier2, near, tc.wantTier2, tc.wantNear)
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
	// The registered URL is a local path, so no owner/repo can be proposed:
	// the remedy is a placeholder the user fills in, never the same
	// ownerless command that would fail identically.
	if !strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("review output missing degraded-metadata note:\n%s", out)
	}
	if want := "Re-run as 'stave review <owner>/<repo>#7 --repo repo-a --refresh' with the GitHub owner/repo"; !strings.Contains(out, want) {
		t.Fatalf("review output missing owner placeholder remedy %q:\n%s", want, out)
	}
	if strings.Contains(out, "stave review repo-a#7 --refresh") {
		t.Fatalf("remedy must not repeat the ownerless command:\n%s", out)
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
		return []byte(reviewTestGHJSON), nil
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
	if strings.Contains(got, "could not be fetched") || strings.Contains(got, "--refresh") {
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

	_, err := runCLIError(t, nil, "review", "repo-a#7")
	assertErrContainsAll(t, err, "already exists", "--refresh")
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
	specFile, err := stageReviewSpec("hello-world", ref, meta, true, "origin/main", "After fixing gh auth ('gh auth status'), run 'stave review octocat/hello-world#42 --refresh' to fill it in.", nil)
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

// TestStageReviewSpecQuotesHostileBaseRef pins that a PR base branch name
// carrying shell metacharacters (git accepts "main;touch${IFS}/tmp/PWN" as a
// branch name) is single-quoted in every pasteable quickstart command, so
// copying the spec's commands cannot run a second command.
func TestStageReviewSpecQuotesHostileBaseRef(t *testing.T) {
	const hostile = "main;touch${IFS}/tmp/PWN"
	meta := prMetadata{Title: "Hostile", State: "OPEN", BaseRefName: hostile, HeadRefName: "feature"}
	ref := prRef{Owner: "octocat", Repo: "hello-world", Number: 9}
	baseRef := "origin/" + hostile
	specFile, err := stageReviewSpec("hello-world", ref, meta, true, baseRef, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specFile)) }()

	body, err := os.ReadFile(specFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	quoted := shellQuote(baseRef)
	if !strings.HasPrefix(quoted, "'") {
		t.Fatalf("shellQuote(%q) = %q, expected a single-quoted form", baseRef, quoted)
	}
	for _, want := range []string{
		"git diff " + quoted + "...HEAD",
		"git log " + quoted + "..HEAD --oneline",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review spec missing quoted command %q:\n%s", want, got)
		}
	}
	// No pasteable line may carry the bare metacharacter sequence: every
	// occurrence of ";touch" must sit inside the quoted form.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "git ") && strings.Contains(line, ";touch") && !strings.Contains(line, quoted) {
			t.Fatalf("command line embeds an unquoted hostile ref: %q", line)
		}
	}
	if strings.Contains(got, "git diff origin/main;touch") || strings.Contains(got, "git log origin/main;touch") {
		t.Fatalf("review spec embeds the bare hostile ref in a command:\n%s", got)
	}
}

func TestStageReviewSpecMinimalNotesGhUnavailable(t *testing.T) {
	ref := prRef{Owner: "", Repo: "my-repo", Number: 7}
	specFile, err := stageReviewSpec("my-repo", ref, prMetadata{}, false, "origin/main", "After fixing gh auth ('gh auth status'), run 'stave review my-repo#7 --refresh' to fill it in.", nil)
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
	if !strings.Contains(got, "After fixing gh auth ('gh auth status'), run 'stave review my-repo#7 --refresh' to fill it in.") {
		t.Fatalf("minimal spec missing refresh remedy:\n%s", got)
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

// loadTestConfig loads the config.yaml that stave setup wrote under the
// test's HOME.
func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// setRegisteredURL rewrites only the recorded URL of a registered repo. The
// bare mirror's real git origin still points at the local fixture, so fetches
// keep working while resolution sees the substituted URL.
func setRegisteredURL(t *testing.T, name, url string) {
	t.Helper()
	cfg, cfgPath, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	r, ok := cfg.Repos[name]
	if !ok {
		t.Fatalf("repo %q not registered", name)
	}
	r.URL = url
	cfg.Repos[name] = r
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func assertErrContainsAll(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", wants)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

// TestCLIReviewNearMatchRefusalIncidentReplay replays the incident where a
// repo registered under an SSH alias was silently shadowed by an
// auto-registered public clone: the near match must now be refused with a
// hint, nothing may be auto-registered, and --repo resolves it.
func TestCLIReviewNearMatchRefusalIncidentReplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	src := createGitRepo(t, "hiddenlayer-nemo-guardrails")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/1/head", prSHA)
	runCLI(t, "repos", "add", "hl-nemo-guardrails", src)
	setRegisteredURL(t, "hl-nemo-guardrails", "git@hl_external:hiddenlayerai/hiddenlayer-nemo-guardrails.git")

	prURL := "https://github.com/hiddenlayerai/hiddenlayer-nemo-guardrails/pull/1"
	_, err := runCLIError(t, nil, "review", prURL)
	assertErrContainsAll(t, err, "hl-nemo-guardrails", "hl_external", "--repo")

	bareRepos := filepath.Join(home, "stave", "bare-repos")
	if _, ok := loadTestConfig(t).Repos["hiddenlayer-nemo-guardrails"]; ok {
		t.Fatalf("near-match refusal must not auto-register the PR repo")
	}
	if _, err := os.Stat(filepath.Join(bareRepos, "hiddenlayer-nemo-guardrails.git")); !os.IsNotExist(err) {
		t.Fatalf("near-match refusal must not clone a bare repo: %v", err)
	}

	// --repo selects the mirror; PR metadata still comes from the PR as typed.
	var gotArgs []string
	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(reviewTestGHJSON), nil
	}}
	var out string
	captureShellChdir(t, func() {
		out = runCLIWithApp(t, application, "review", prURL, "--repo", "hl-nemo-guardrails")
	})
	if !strings.Contains(out, "review space review-hl-nemo-guardrails-1 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	if strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("--repo must not degrade metadata for a typed PR URL:\n%s", out)
	}
	if len(gotArgs) < 4 || gotArgs[3] != prURL {
		t.Fatalf("gh invocation = %#v, want argv[3] == typed URL %q", gotArgs, prURL)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "review-hl-nemo-guardrails-1", "hl-nemo-guardrails")); err != nil {
		t.Fatalf("review worktree missing: %v", err)
	}
}

// TestCLIReviewTier1MismatchGuard covers a registered repo whose name matches
// the PR's repo but whose URL points at a different owner/repo.
func TestCLIReviewTier1MismatchGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	src := createGitRepo(t, "api")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/3/head", prSHA)
	runCLI(t, "repos", "add", "api", src)
	setRegisteredURL(t, "api", "git@github.com:other/api.git")

	prURL := "https://github.com/acme/api/pull/3"
	_, err := runCLIError(t, nil, "review", prURL)
	// A plain remove keeps the bare cache and the re-add would collide on
	// it, so the recipe purges.
	assertErrContainsAll(t, err, "other/api", "acme/api", `--repo "api"`, "'stave repos remove api --purge' + 'stave repos add api <url>'")

	// Variant B: a differently named registration matches the PR's
	// owner/repo, so the guard suggests it.
	runCLI(t, "repos", "add", "acme-api", src)
	setRegisteredURL(t, "acme-api", "git@github.com:acme/api.git")
	_, err = runCLIError(t, nil, "review", prURL)
	assertErrContainsAll(t, err, `did you mean --repo "acme-api"`)
}

func TestCLIReviewRepoOverrideUnregisteredErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	_, err := runCLIError(t, nil, "review", "https://github.com/acme/api/pull/3", "--repo", "nope")
	assertErrContainsAll(t, err, "not registered", `"nope"`)

	if _, ok := loadTestConfig(t).Repos["api"]; ok {
		t.Fatalf("--repo with an unknown name must not auto-register the PR repo")
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "bare-repos", "api.git")); !os.IsNotExist(err) {
		t.Fatalf("--repo with an unknown name must not clone a bare repo: %v", err)
	}
}

func TestCLIReviewTier2AmbiguityErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	src := createGitRepo(t, "widgets")
	runCLI(t, "repos", "add", "w1", src)
	runCLI(t, "repos", "add", "w2", src)
	setRegisteredURL(t, "w1", "https://github.com/acme/widgets.git")
	setRegisteredURL(t, "w2", "https://github.com/acme/widgets.git")

	_, err := runCLIError(t, nil, "review", "https://github.com/acme/widgets/pull/1")
	assertErrContainsAll(t, err, "2 registered repos match github.com/acme/widgets", "w1", "w2", "--repo")
}

// TestCLIReviewAutoRegisterCloneFailure forces the auto-register clone to
// fail by rewriting the github.com clone URL to a missing local path through
// git's insteadOf, then checks that the registration and bare repo roll back.
func TestCLIReviewAutoRegisterCloneFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	gitConfig := filepath.Join(home, "gitconfig-test")
	content := "[url \"" + filepath.Join(home, "does-not-exist") + "/\"]\n\tinsteadOf = https://github.com/acme/\n"
	if err := os.WriteFile(gitConfig, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	_, err := runCLIError(t, nil, "review", "https://github.com/acme/widgets/pull/1")
	assertErrContainsAll(t, err, `auto-registration of "widgets" failed`, "could not clone", "stave repos list")
	// The %w wrap must keep the underlying git failure classifiable.
	var gitErr *git.GitError
	if !errors.As(err, &gitErr) || gitErr.ExitCode == 0 {
		t.Fatalf("clone failure did not preserve *git.GitError through the wrap: %#v", err)
	}
	if !git.IsExitCode(err, gitErr.ExitCode) {
		t.Fatalf("git.IsExitCode(err, %d) = false for %v", gitErr.ExitCode, err)
	}

	if _, ok := loadTestConfig(t).Repos["widgets"]; ok {
		t.Fatalf("failed auto-registration must not persist the repo")
	}
	bareRepos := filepath.Join(home, "stave", "bare-repos")
	if _, err := os.Stat(filepath.Join(bareRepos, "widgets.git")); !os.IsNotExist(err) {
		t.Fatalf("failed auto-registration must not leave a bare repo: %v", err)
	}
	partials, err := filepath.Glob(filepath.Join(bareRepos, "widgets.git.partial-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("failed auto-registration left partial clones: %v", partials)
	}
}

// TestCLIReviewNameRefFetchesMetadataForGitHubURL covers the <name>#N form
// when the registered URL is unambiguously github.com/<owner>/<repo>: the
// owner is derived from the registration and gh is asked for the PR under
// that identity.
func TestCLIReviewNameRefFetchesMetadataForGitHubURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/42/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	setRegisteredURL(t, "repo-a", "git@github.com:octocat/repo-a.git")

	var gotArgs []string
	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(reviewTestGHJSON), nil
	}}
	var out string
	captureShellChdir(t, func() {
		out = runCLIWithApp(t, application, "review", "repo-a#42")
	})
	if !strings.Contains(out, "review space review-repo-a-42 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	if strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("github.com registration must fetch metadata, not degrade:\n%s", out)
	}
	wantURL := "https://github.com/octocat/repo-a/pull/42"
	if len(gotArgs) < 4 || gotArgs[3] != wantURL {
		t.Fatalf("gh invocation = %#v, want argv[3] == %q", gotArgs, wantURL)
	}

	spec, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "review-repo-a-42", "spec", "pr-42.md"))
	if err != nil {
		t.Fatalf("review spec missing: %v", err)
	}
	got := string(spec)
	for _, want := range []string{
		"- URL: " + wantURL,
		"# Review: PR #42 — Add widget",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review spec missing %q:\n%s", want, got)
		}
	}
}

// TestCLIReviewNameRefWithRepoOverrideDerivesFromOverrideURL covers the
// <name>#N form combined with --repo: the name carries no owner, so the
// identity comes from the resolved (override) repo's registered URL when it
// is unambiguously github.com, exactly as it would without --repo.
func TestCLIReviewNameRefWithRepoOverrideDerivesFromOverrideURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srcA := createGitRepo(t, "repo-a")
	srcOther := createGitRepo(t, "other")
	prSHA := gitOutput(t, srcOther, "rev-parse", "main")
	runGit(t, srcOther, "update-ref", "refs/pull/5/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "other", srcOther)
	setRegisteredURL(t, "other", "git@github.com:octocat/other.git")

	var gotArgs []string
	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(reviewTestGHJSON), nil
	}}
	var out string
	captureShellChdir(t, func() {
		out = runCLIWithApp(t, application, "review", "repo-a#5", "--repo", "other")
	})
	if !strings.Contains(out, "review space review-other-5 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	if strings.Contains(out, "PR metadata unavailable") {
		t.Fatalf("--repo with a github.com registration must fetch metadata, not degrade:\n%s", out)
	}
	wantURL := "https://github.com/octocat/other/pull/5"
	if len(gotArgs) < 4 || gotArgs[3] != wantURL {
		t.Fatalf("gh invocation = %#v, want argv[3] == %q", gotArgs, wantURL)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "review-other-5", "other")); err != nil {
		t.Fatalf("review worktree missing: %v", err)
	}
}

// TestCLIReviewNameRefAliasURLSkipsMetadata covers the <name>#N form when the
// registered URL is an SSH alias: the host may not be GitHub, so no owner is
// guessed and gh is never invoked. The remedy must not repeat the ownerless
// command (it would fail identically); it proposes the alias URL's
// owner/repo for the user to confirm, pinned to this mirror with --repo.
func TestCLIReviewNameRefAliasURLSkipsMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/42/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	setRegisteredURL(t, "repo-a", "git@hl_external:octocat/repo-a.git")

	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("gh must not be invoked for an SSH-alias registration; got %s %v", name, args)
		return nil, nil
	}}
	var out string
	captureShellChdir(t, func() {
		out = runCLIWithApp(t, application, "review", "repo-a#42")
	})
	if !strings.Contains(out, "review space review-repo-a-42 is ready") {
		t.Fatalf("review output missing ready line:\n%s", out)
	}
	for _, want := range []string{
		"PR metadata unavailable",
		"If this repo lives at github.com/octocat/repo-a, run 'stave review octocat/repo-a#42 --repo repo-a --refresh' to fill it in.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("review output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stave review repo-a#42 --refresh") {
		t.Fatalf("remedy must not repeat the ownerless command:\n%s", out)
	}

	spec, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "review-repo-a-42", "spec", "pr-42.md"))
	if err != nil {
		t.Fatalf("review spec missing: %v", err)
	}
	if strings.Contains(string(spec), "https://github.com") {
		t.Fatalf("alias registration must not guess a GitHub URL:\n%s", spec)
	}
	if !strings.Contains(string(spec), "run 'stave review octocat/repo-a#42 --repo repo-a --refresh'") {
		t.Fatalf("minimal spec missing the owner-qualified remedy:\n%s", spec)
	}
}

// TestCLIReviewAliasRemedyPastedCommandFetchesMetadata pastes the command
// the alias remedy proposes: the typed owner/repo is used for metadata
// verbatim, --repo lands the refresh in the same space, and the spec fills
// in.
func TestCLIReviewAliasRemedyPastedCommandFetchesMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := createGitRepo(t, "repo-a")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/42/head", prSHA)

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	setRegisteredURL(t, "repo-a", "git@hl_external:octocat/repo-a.git")
	reviewTestCreateSpace(t, reviewTestFailingGHApp(), "review", "repo-a#42")

	var gotArgs []string
	application := &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(reviewTestGHJSON), nil
	}}
	specFile := filepath.Join(home, "stave", "agent-work", "review-repo-a-42", "spec", "pr-42.md")
	out := runCLIWithApp(t, application, "review", "octocat/repo-a#42", "--repo", "repo-a", "--refresh")
	if !strings.Contains(out, "refreshed spec at "+specFile) {
		t.Fatalf("refresh output missing spec line:\n%s", out)
	}
	wantURL := "https://github.com/octocat/repo-a/pull/42"
	if len(gotArgs) < 4 || gotArgs[3] != wantURL {
		t.Fatalf("gh invocation = %#v, want argv[3] == typed %q", gotArgs, wantURL)
	}
	spec := string(reviewTestReadFile(t, specFile))
	for _, want := range []string{"# Review: PR #42 — Add widget", "- URL: " + wantURL} {
		if !strings.Contains(spec, want) {
			t.Fatalf("refreshed spec missing %q:\n%s", want, spec)
		}
	}
	if strings.Contains(spec, "could not be fetched") {
		t.Fatalf("refreshed spec still carries the degrade note:\n%s", spec)
	}
}

// TestCLIReviewRefreshNameRefAliasURLErrorNamesRemedy covers --refresh with
// the ownerless form on an alias registration: the failure must say what to
// type instead of leaving the user with the same dead-end command.
func TestCLIReviewRefreshNameRefAliasURLErrorNamesRemedy(t *testing.T) {
	_, _, _ = reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")
	setRegisteredURL(t, "repo-a", "git@hl_external:octocat/repo-a.git")

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "repo-a#7", "--refresh")
	assertErrContainsAll(t, err, "could not fetch PR metadata for repo-a#7", "existing spec left unchanged",
		"If this repo lives at github.com/octocat/repo-a, run 'stave review octocat/repo-a#7 --repo repo-a --refresh'")
}

// reviewTestGHJSON is the canned `gh pr view --json` payload shared by the
// review tests (title "Add widget", base "main").
const reviewTestGHJSON = `{
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
}`

// reviewTestFakeGHApp returns an app whose gh runner answers every call
// with the canned metadata.
func reviewTestFakeGHApp() *app {
	return &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(reviewTestGHJSON), nil
	}}
}

// reviewTestFailingGHApp returns an app whose gh runner always fails, as an
// unauthenticated gh would.
func reviewTestFailingGHApp() *app {
	return &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("gh: not logged in")
	}}
}

// reviewTestReadFile reads a file or fails the test.
func reviewTestReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// reviewTestFixture prepares an isolated HOME with repo-a registered and
// refs/pull/7/head pointing at main. It returns the home, the source repo,
// and the PR head SHA.
func reviewTestFixture(t *testing.T) (home, src, prSHA string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	src = createGitRepo(t, "repo-a")
	prSHA = gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	return home, src, prSHA
}

// reviewTestCreateSpace runs a space-creating review command inside the
// shell-chdir capture the command requires and returns its output.
func reviewTestCreateSpace(t *testing.T, application *app, args ...string) string {
	t.Helper()
	var out string
	captureShellChdir(t, func() {
		out = runCLIWithApp(t, application, args...)
	})
	return out
}

func TestCLIReviewRefreshRewritesSpecOnly(t *testing.T) {
	home, _, prSHA := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	specFile := filepath.Join(spacePath, "spec", "pr-7.md")
	manifestFile := filepath.Join(spacePath, ".stave.yaml")
	skillFile := filepath.Join(spacePath, ".claude", "skills", "pr-teach", "SKILL.md")
	worktree := filepath.Join(spacePath, "repo-a")

	specBefore := reviewTestReadFile(t, specFile)
	if strings.Contains(string(specBefore), "Add widget") {
		t.Fatalf("spec before refresh unexpectedly has metadata:\n%s", specBefore)
	}
	manifestBefore := reviewTestReadFile(t, manifestFile)
	skillBefore := reviewTestReadFile(t, skillFile)
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != prSHA {
		t.Fatalf("worktree HEAD before refresh = %q, want %q", head, prSHA)
	}

	out := runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "--refresh")
	if !strings.Contains(out, "refreshed spec at "+specFile) {
		t.Fatalf("refresh output missing spec line:\n%s", out)
	}

	specAfter := reviewTestReadFile(t, specFile)
	if !strings.Contains(string(specAfter), "# Review: PR #7 — Add widget") {
		t.Fatalf("refreshed spec missing fetched title:\n%s", specAfter)
	}
	if bytes.Equal(specBefore, specAfter) {
		t.Fatalf("refresh did not rewrite the spec")
	}
	if !bytes.Equal(manifestBefore, reviewTestReadFile(t, manifestFile)) {
		t.Fatalf("refresh modified %s", manifestFile)
	}
	if !bytes.Equal(skillBefore, reviewTestReadFile(t, skillFile)) {
		t.Fatalf("refresh modified %s", skillFile)
	}
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != prSHA {
		t.Fatalf("worktree HEAD after refresh = %q, want unchanged %q", head, prSHA)
	}
}

func TestCLIReviewRefreshRejectsIncompatibleFlags(t *testing.T) {
	reviewTestFixture(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "summon", args: []string{"review", "repo-a#7", "--refresh", "--summon", "claude"}},
		{name: "reference", args: []string{"review", "repo-a#7", "--refresh", "-r", "repo-a"}},
		{name: "memory", args: []string{"review", "repo-a#7", "--refresh", "--memory", "."}},
		{name: "prompt", args: []string{"review", "repo-a#7", "--refresh", "--prompt", "/pr-teach"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLIError(t, reviewTestFakeGHApp(), tc.args...)
			assertErrContainsAll(t, err, "--refresh cannot be combined with", "--prompt")
		})
	}
}

func TestCLIReviewRefreshMissingSpace(t *testing.T) {
	home, _, _ := reviewTestFixture(t)

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "repo-a#7", "--refresh")
	assertErrContainsAll(t, err, `no review space "review-repo-a-7" to refresh`)

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	if _, statErr := os.Stat(spacePath); !os.IsNotExist(statErr) {
		t.Fatalf("refresh of a missing space must not create it: %v", statErr)
	}
}

// TestCLIReviewRefreshRejectsTraversalSpaceID uses an UNREGISTERED repo so
// the failure proves ordering: resolution would have failed with "cannot
// refresh" first if the space id were validated afterwards.
func TestCLIReviewRefreshRejectsTraversalSpaceID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "https://github.com/acme/widgets/pull/1", "../x", "--refresh")
	assertErrContainsAll(t, err, "space id", `"../x"`)
	if strings.Contains(err.Error(), "cannot refresh") {
		t.Fatalf("space id must be validated before repo resolution: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(home, "stave", "x")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal space id must not touch the filesystem: %v", statErr)
	}
}

// TestCLIReviewRejectsTraversalSpaceIDBeforeRegistering is the creation-path
// twin: an explicit space id is validated before auto-registration would
// register or clone anything.
func TestCLIReviewRejectsTraversalSpaceIDBeforeRegistering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	_, err := runCLIError(t, nil, "review", "https://github.com/acme/widgets/pull/1", "../x")
	assertErrContainsAll(t, err, "space id", `"../x"`)

	if _, ok := loadTestConfig(t).Repos["widgets"]; ok {
		t.Fatalf("invalid space id must not auto-register the PR repo")
	}
	if _, statErr := os.Stat(filepath.Join(home, "stave", "bare-repos", "widgets.git")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid space id must not clone a bare repo: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, "stave", "x")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal space id must not touch the filesystem: %v", statErr)
	}
}

// TestCLIReviewRefreshUnregisteredNameRef: an ownerless reference to an
// unregistered repo under --refresh gets the refresh-specific message, not
// the auto-register hint (nothing is auto-registered under --refresh).
func TestCLIReviewRefreshUnregisteredNameRef(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "ghost#7", "--refresh")
	assertErrContainsAll(t, err, `repo "ghost" is not registered; cannot refresh`, "run without --refresh to create the review space")
	if strings.Contains(err.Error(), "registered automatically") {
		t.Fatalf("--refresh must not suggest auto-registration: %v", err)
	}
}

func TestCLIReviewRefreshWrongRepoSpace(t *testing.T) {
	_, _, _ = reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	srcB := createGitRepo(t, "repo-b")
	runGit(t, srcB, "update-ref", "refs/pull/7/head", gitOutput(t, srcB, "rev-parse", "main"))
	runCLI(t, "repos", "add", "repo-b", srcB)

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "repo-b#7", "review-repo-a-7", "--refresh")
	assertErrContainsAll(t, err, "refusing to refresh", `reviews repo "repo-a"`)
}

func TestCLIReviewRefreshMetadataFailurePreservesSpec(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	specFile := filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "spec", "pr-7.md")
	before := reviewTestReadFile(t, specFile)

	_, err := runCLIError(t, reviewTestFailingGHApp(), "review", "octocat/repo-a#7", "--refresh")
	assertErrContainsAll(t, err, "could not fetch PR metadata", "existing spec left unchanged")

	if !bytes.Equal(before, reviewTestReadFile(t, specFile)) {
		t.Fatalf("metadata failure must leave the spec untouched")
	}
}

func TestCLIReviewRefreshStalenessNote(t *testing.T) {
	home, src, prSHA := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	// The PR author pushes a new commit after the space was created.
	runGit(t, src, "checkout", "-b", "pr-branch")
	if err := os.WriteFile(filepath.Join(src, "feature.txt"), []byte("newer change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "feature.txt")
	runGit(t, src, "commit", "-m", "newer change")
	newSHA := gitOutput(t, src, "rev-parse", "pr-branch")
	runGit(t, src, "checkout", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", newSHA)
	if newSHA == prSHA {
		t.Fatalf("fixture did not move the PR head")
	}

	out := runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "--refresh")
	want := fmt.Sprintf("worktree is checked out at %s; PR head is now %s", prSHA[:12], newSHA[:12])
	if !strings.Contains(out, want) {
		t.Fatalf("refresh output missing staleness note %q:\n%s", want, out)
	}

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	spec := string(reviewTestReadFile(t, filepath.Join(spacePath, "spec", "pr-7.md")))
	idx := strings.Index(spec, "## Refresh notes")
	if idx < 0 {
		t.Fatalf("refreshed spec missing refresh notes section:\n%s", spec)
	}
	if !strings.Contains(spec[idx:], want) {
		t.Fatalf("refresh notes missing staleness note %q:\n%s", want, spec)
	}
	if head := gitOutput(t, filepath.Join(spacePath, "repo-a"), "rev-parse", "HEAD"); head != prSHA {
		t.Fatalf("refresh must not move the worktree: HEAD = %q, want %q", head, prSHA)
	}
}

func TestCLIReviewRefreshNoDriftNoNote(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	out := runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "--refresh")
	for _, unwanted := range []string{"PR head is now", "PR base is now"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("unchanged PR must not produce %q:\n%s", unwanted, out)
		}
	}
	spec := string(reviewTestReadFile(t, filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "spec", "pr-7.md")))
	if strings.Contains(spec, "## Refresh notes") {
		t.Fatalf("unchanged PR must not render refresh notes:\n%s", spec)
	}
}

func TestCLIReviewRefreshCustomSpaceIDRemedy(t *testing.T) {
	home, _, _ := reviewTestFixture(t)

	remedy := "run 'stave review octocat/repo-a#7 my-review --repo repo-a --refresh' to fill it in."
	out := reviewTestCreateSpace(t, reviewTestFailingGHApp(), "review", "octocat/repo-a#7", "my-review", "--repo", "repo-a")
	if !strings.Contains(out, remedy) {
		t.Fatalf("create output missing remedy %q:\n%s", remedy, out)
	}
	specFile := filepath.Join(home, "stave", "agent-work", "my-review", "spec", "pr-7.md")
	if spec := string(reviewTestReadFile(t, specFile)); !strings.Contains(spec, remedy) {
		t.Fatalf("minimal spec missing remedy %q:\n%s", remedy, spec)
	}

	out = runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "my-review", "--repo", "repo-a", "--refresh")
	if !strings.Contains(out, "refreshed spec at "+specFile) {
		t.Fatalf("refresh output missing spec line:\n%s", out)
	}
	spec := string(reviewTestReadFile(t, specFile))
	if !strings.Contains(spec, "# Review: PR #7 — Add widget") {
		t.Fatalf("refreshed spec missing fetched title:\n%s", spec)
	}
	if strings.Contains(spec, "could not be fetched") {
		t.Fatalf("refreshed spec still carries the degrade note:\n%s", spec)
	}
}

func TestCLIReviewRefreshForbidsAutoRegister(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "https://github.com/acme/widgets/pull/1", "--refresh")
	assertErrContainsAll(t, err, "cannot refresh")

	if _, ok := loadTestConfig(t).Repos["widgets"]; ok {
		t.Fatalf("--refresh must not auto-register the PR repo")
	}
	if _, statErr := os.Stat(filepath.Join(home, "stave", "bare-repos", "widgets.git")); !os.IsNotExist(statErr) {
		t.Fatalf("--refresh must not clone a bare repo: %v", statErr)
	}
}

// TestCLIReviewAutoRegisterCollisionHint covers the review-side bare-path
// collision: a leftover cache at the derived path blocks auto-registration
// with the same full-command --adopt recipe `repos add` prints.
func TestCLIReviewAutoRegisterCollisionHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	src := createGitRepo(t, "widgets")
	runCLI(t, "repos", "add", "widgets", src)
	runCLI(t, "repos", "remove", "widgets")
	bare := filepath.Join(home, "stave", "bare-repos", "widgets.git")
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("bare cache missing after remove: %v", err)
	}

	_, err := runCLIError(t, nil, "review", "https://github.com/acme/widgets/pull/1")
	assertErrContainsAll(t, err,
		"bare repo path already exists: "+bare,
		"stave repos add widgets https://github.com/acme/widgets.git --adopt")

	if _, ok := loadTestConfig(t).Repos["widgets"]; ok {
		t.Fatalf("collision must not persist a registration")
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("collision must leave the existing cache untouched: %v", err)
	}
}

func TestCLIReviewRefreshRejectsNonReviewSpace(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	runCLI(t, "space", "create", "plain", "-e", "repo-a")

	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "repo-a#7", "plain", "--refresh")
	assertErrContainsAll(t, err, `space "plain"`, "not a review space")

	if _, statErr := os.Stat(filepath.Join(home, "stave", "agent-work", "plain", "spec", "pr-7.md")); !os.IsNotExist(statErr) {
		t.Fatalf("refresh of a non-review space must not write a spec: %v", statErr)
	}
}

func TestCLIReviewRefreshMissingSpecFile(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	before := reviewTestReadFile(t, filepath.Join(spacePath, "spec", "pr-7.md"))

	// Same repo, same space, different PR number: the spec for #8 is absent.
	_, err := runCLIError(t, reviewTestFakeGHApp(), "review", "repo-a#8", "review-repo-a-7", "--refresh")
	assertErrContainsAll(t, err, "has no spec for PR #8", "refusing to refresh")

	if _, statErr := os.Stat(filepath.Join(spacePath, "spec", "pr-8.md")); !os.IsNotExist(statErr) {
		t.Fatalf("refresh must not create a spec for another PR: %v", statErr)
	}
	if !bytes.Equal(before, reviewTestReadFile(t, filepath.Join(spacePath, "spec", "pr-7.md"))) {
		t.Fatalf("refresh rejection must leave the existing spec untouched")
	}
}

// TestCLIReviewRefreshBaseDriftNote covers the positive base-drift case: the
// space was created against the registry default (main) and gh now reports a
// different base branch.
func TestCLIReviewRefreshBaseDriftNote(t *testing.T) {
	home, src, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")
	// The new base exists upstream, so the refresh fetch brings it into the
	// mirror and the quickstart it points at will work.
	runGit(t, src, "branch", "develop")

	out := runCLIWithApp(t, reviewTestRetargetedGHApp("develop"), "review", "octocat/repo-a#7", "--refresh")
	want := `PR base is now "develop"; this space was created against "main"`
	if !strings.Contains(out, want) {
		t.Fatalf("refresh output missing base drift note %q:\n%s", want, out)
	}
	if strings.Contains(out, "PR head is now") {
		t.Fatalf("unchanged head must not produce a staleness note:\n%s", out)
	}
	if strings.Contains(out, "was not found in the mirror") {
		t.Fatalf("fetched base must not produce a missing-base note:\n%s", out)
	}
	spec := string(reviewTestReadFile(t, filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "spec", "pr-7.md")))
	idx := strings.Index(spec, "## Refresh notes")
	if idx < 0 || !strings.Contains(spec[idx:], want) {
		t.Fatalf("refreshed spec missing base drift note under refresh notes:\n%s", spec)
	}
	if !strings.Contains(spec, "git diff origin/develop...HEAD") {
		t.Fatalf("refreshed spec quickstart should target the new base:\n%s", spec)
	}
}

// reviewTestRetargetedGHApp answers gh with the canned metadata, but with
// the PR's base branch set to base.
func reviewTestRetargetedGHApp(base string) *app {
	return &app{ghRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(strings.Replace(reviewTestGHJSON, `"baseRefName": "main"`, `"baseRefName": "`+base+`"`, 1)), nil
	}}
}

// TestCLIReviewRefreshMissingBaseNote is the negative twin of the base-drift
// test: gh reports a base the mirror does not have, and the refresh says so
// instead of silently writing quickstart commands that cannot run.
func TestCLIReviewRefreshMissingBaseNote(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	out := runCLIWithApp(t, reviewTestRetargetedGHApp("develop"), "review", "octocat/repo-a#7", "--refresh")
	want := "base branch origin/develop was not found in the mirror; quickstart commands referencing it will fail until it is fetched"
	if !strings.Contains(out, "note: "+want) {
		t.Fatalf("refresh output missing missing-base note:\n%s", out)
	}
	spec := string(reviewTestReadFile(t, filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "spec", "pr-7.md")))
	idx := strings.Index(spec, "## Refresh notes")
	if idx < 0 || !strings.Contains(spec[idx:], "- "+want) {
		t.Fatalf("refreshed spec missing missing-base note under refresh notes:\n%s", spec)
	}
}

// TestCLIReviewRefreshStalenessNoteDetachedWorktree: the staleness check
// must read the worktree's actual HEAD. A detached checkout inside the
// worktree leaves the bare repo's branch ref at the PR head, so comparing
// the branch ref would miss it.
func TestCLIReviewRefreshStalenessNoteDetachedWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	olderSHA := gitOutput(t, src, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(src, "feature.txt"), []byte("pr change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "feature.txt")
	runGit(t, src, "commit", "-m", "pr change")
	prSHA := gitOutput(t, src, "rev-parse", "main")
	runGit(t, src, "update-ref", "refs/pull/7/head", prSHA)
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	spacePath := filepath.Join(home, "stave", "agent-work", "review-repo-a-7")
	worktree := filepath.Join(spacePath, "repo-a")
	runGit(t, worktree, "checkout", "--detach", olderSHA)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	if got := gitOutput(t, "", "--git-dir", bare, "rev-parse", "refs/heads/"+manifest.Repos[0].Branch); got != prSHA {
		t.Fatalf("fixture: branch ref = %q, want it to stay at PR head %q", got, prSHA)
	}

	out := runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "--refresh")
	want := fmt.Sprintf("worktree is checked out at %s; PR head is now %s", olderSHA[:12], prSHA[:12])
	if !strings.Contains(out, want) {
		t.Fatalf("refresh output missing staleness note %q:\n%s", want, out)
	}
	spec := string(reviewTestReadFile(t, filepath.Join(spacePath, "spec", "pr-7.md")))
	idx := strings.Index(spec, "## Refresh notes")
	if idx < 0 || !strings.Contains(spec[idx:], want) {
		t.Fatalf("refreshed spec missing staleness note %q:\n%s", want, spec)
	}
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != olderSHA {
		t.Fatalf("refresh must not move the worktree: HEAD = %q, want %q", head, olderSHA)
	}
}

// reviewTestGitRunner is a scripted git runner whose bare clone creates the
// destination directory (so cloneBareFresh's rename succeeds) and whose
// other commands fail when their argv contains a configured substring.
type reviewTestGitRunner struct {
	fail map[string]bool
}

func (r *reviewTestGitRunner) Run(ctx context.Context, bin string, args []string, opts git.RunOptions) (git.Result, error) {
	joined := strings.Join(args, " ")
	for sub := range r.fail {
		if strings.Contains(joined, sub) {
			return git.Result{}, &git.GitError{Args: args, ExitCode: 1, Stderr: "scripted failure"}
		}
	}
	if len(args) >= 4 && args[0] == "clone" && args[1] == "--bare" {
		if err := os.MkdirAll(args[3], 0o755); err != nil {
			return git.Result{}, err
		}
	}
	return git.Result{}, nil
}

// TestResolveReviewRepoAutoRegisterRollbackKeepsClone drives the
// auto-register branches that follow a successful clone: a finalize failure
// rolls the registration back but keeps the clone and says how to adopt it
// (shell-quoted, here with a space in the path); the collision guard then
// rolls back too instead of leaving a phantom registration behind.
func TestResolveReviewRepoAutoRegisterRollbackKeepsClone(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "bare repos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BareReposDir: root, DefaultBase: "main", Repos: map[string]config.Repository{}}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	ref := prRef{Owner: "acme", Repo: "widgets", Number: 1}
	bare := filepath.Join(root, "widgets.git")
	adopt := "stave repos add widgets https://github.com/acme/widgets.git --adopt"

	cmd, _, _ := newMirrorCmd()
	client := git.New(git.WithRunner(&reviewTestGitRunner{fail: map[string]bool{"remote.origin.fetch": true}}))
	_, _, err := resolveReviewRepo(ctx, cmd, cfg, cfgPath, client, ref, "", true)
	assertErrContainsAll(t, err, `auto-registration of "widgets" failed`, "the clone was kept at "+shellQuote(bare), "retry with '"+adopt+"'")
	if !strings.Contains(err.Error(), "/bare repos/widgets.git'") {
		t.Fatalf("path with a space must be shell-quoted in the hint: %v", err)
	}
	if _, ok := cfg.Repos["widgets"]; ok {
		t.Fatalf("finalize failure must roll back the registration")
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("finalize failure must keep the clone: %v", err)
	}

	cmd, _, _ = newMirrorCmd()
	_, _, err = resolveReviewRepo(ctx, cmd, cfg, cfgPath, git.New(git.WithRunner(&reviewTestGitRunner{})), ref, "", true)
	assertErrContainsAll(t, err, "bare repo path already exists: "+bare, "run '"+adopt+"' to reuse it")
	if _, ok := cfg.Repos["widgets"]; ok {
		t.Fatalf("collision must roll back the in-memory registration")
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("collision must leave the existing clone untouched: %v", err)
	}
}

func TestReviewRefreshRemedyShellQuotes(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		prArg, spaceID, defaultSpaceID, override string
		want                                     string
	}{
		{name: "plain values pass through", prArg: "octocat/repo-a#7", spaceID: "my-review", defaultSpaceID: "review-repo-a-7", override: "repo-a", want: "stave review octocat/repo-a#7 my-review --repo repo-a --refresh"},
		{name: "default space id omitted", prArg: "https://github.com/o/r/pull/1", spaceID: "review-r-1", defaultSpaceID: "review-r-1", want: "stave review https://github.com/o/r/pull/1 --refresh"},
		{name: "shell metacharacters quoted", prArg: "a$b/c#1", spaceID: "review-c-1", defaultSpaceID: "review-c-1", want: "stave review 'a$b/c#1' --refresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewRefreshRemedy(tc.prArg, tc.spaceID, tc.defaultSpaceID, tc.override); got != tc.want {
				t.Fatalf("reviewRefreshRemedy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReviewMetaRemedy(t *testing.T) {
	ref := prRef{Repo: "repo-a", Number: 7}
	for _, tc := range []struct {
		name    string
		metaRef prRef
		url     string
		spaceID string
		want    string
	}{
		{name: "owner known: gh auth", metaRef: prRef{Owner: "octocat", Repo: "repo-a", Number: 7}, url: "git@github.com:octocat/repo-a.git", spaceID: "review-repo-a-7",
			want: "After fixing gh auth ('gh auth status'), run 'stave review repo-a#7 --refresh' to fill it in."},
		{name: "alias url proposes owner/repo", metaRef: ref, url: "git@hl_external:octocat/repo-a.git", spaceID: "review-repo-a-7",
			want: "If this repo lives at github.com/octocat/repo-a, run 'stave review octocat/repo-a#7 --repo repo-a --refresh' to fill it in."},
		{name: "alias url with custom space id", metaRef: ref, url: "git@hl_external:octocat/repo-a.git", spaceID: "my-review",
			want: "If this repo lives at github.com/octocat/repo-a, run 'stave review octocat/repo-a#7 my-review --repo repo-a --refresh' to fill it in."},
		{name: "local path uses placeholder", metaRef: ref, url: "/srv/repo-a.git", spaceID: "review-repo-a-7",
			want: "Re-run as 'stave review <owner>/<repo>#7 --repo repo-a --refresh' with the GitHub owner/repo to fill it in."},
		{name: "file url uses placeholder", metaRef: ref, url: "file:///srv/o/repo-a.git", spaceID: "review-repo-a-7",
			want: "Re-run as 'stave review <owner>/<repo>#7 --repo repo-a --refresh' with the GitHub owner/repo to fill it in."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewMetaRemedy("repo-a#7", ref, tc.metaRef, "repo-a", config.Repository{URL: tc.url}, tc.spaceID, "review-repo-a-7", "")
			if got != tc.want {
				t.Fatalf("reviewMetaRemedy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCLIReviewRefreshLocalCommitsNote(t *testing.T) {
	home, _, prSHA := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")

	// The reviewer commits on top of the PR head inside the worktree.
	worktree := filepath.Join(home, "stave", "agent-work", "review-repo-a-7", "repo-a")
	if err := os.WriteFile(filepath.Join(worktree, "notes.txt"), []byte("review notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "notes.txt")
	runGit(t, worktree, "-c", "user.name=Reviewer", "-c", "user.email=reviewer@example.test", "commit", "-m", "review notes")
	localSHA := gitOutput(t, worktree, "rev-parse", "HEAD")
	if localSHA == prSHA {
		t.Fatalf("fixture did not commit in the worktree")
	}

	out := runCLIWithApp(t, reviewTestFakeGHApp(), "review", "octocat/repo-a#7", "--refresh")
	want := fmt.Sprintf("worktree has local commits on top of PR head %s (worktree at %s)", prSHA[:12], localSHA[:12])
	if !strings.Contains(out, want) {
		t.Fatalf("refresh output missing local-commits note %q:\n%s", want, out)
	}
	if strings.Contains(out, "PR head is now") {
		t.Fatalf("local commits must not be reported as a stale checkout:\n%s", out)
	}
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != localSHA {
		t.Fatalf("refresh must not move the worktree: HEAD = %q, want %q", head, localSHA)
	}
}
