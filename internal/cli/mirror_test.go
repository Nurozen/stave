package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/spf13/cobra"
)

// mirrorRunner is a scripted git runner: it records argv and answers by
// matching a substring of the joined command line.
type mirrorRunner struct {
	calls   [][]string
	fail    map[string]bool   // substring -> return exit 1
	outputs map[string]string // substring -> stdout
}

func (r *mirrorRunner) Run(ctx context.Context, bin string, args []string, opts git.RunOptions) (git.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	for sub := range r.fail {
		if strings.Contains(joined, sub) {
			return git.Result{}, &git.GitError{Args: args, ExitCode: 1, Stderr: "scripted failure"}
		}
	}
	for sub, out := range r.outputs {
		if strings.Contains(joined, sub) {
			return git.Result{Stdout: out}, nil
		}
	}
	return git.Result{}, nil
}

func (r *mirrorRunner) joined() []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func newMirrorCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "test"}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

func TestFinalizeBareMirrorHappyPathWithTracking(t *testing.T) {
	runner := &mirrorRunner{outputs: map[string]string{"symbolic-ref": "origin/main\n"}}
	client := git.New(git.WithRunner(runner))
	cmd, stdout, stderr := newMirrorCmd()

	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "main", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	want := []string{
		"--git-dir /bare/api.git config remote.origin.fetch +refs/heads/*:refs/remotes/origin/*",
		"--git-dir /bare/api.git fetch --all --prune",
		"--git-dir /bare/api.git remote set-head origin --auto",
		"--git-dir /bare/api.git symbolic-ref --quiet --short refs/remotes/origin/HEAD",
	}
	got := runner.joined()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("git calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestFinalizeBareMirrorSkipsTrackingWhenAsked(t *testing.T) {
	runner := &mirrorRunner{outputs: map[string]string{"symbolic-ref": "origin/main\n"}}
	client := git.New(git.WithRunner(runner))
	cmd, _, _ := newMirrorCmd()

	if _, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "main", "main", false, false); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.joined() {
		if strings.Contains(call, "remote.origin.fetch") {
			t.Fatalf("sync path must not reconfigure tracking: %v", runner.joined())
		}
	}
}

func TestFinalizeBareMirrorDryRunNeverProbes(t *testing.T) {
	runner := &mirrorRunner{}
	var logged []string
	client := git.New(git.WithRunner(runner), git.WithDryRun(true, func(format string, args ...any) {
		logged = append(logged, format)
	}))
	cmd, _, stderr := newMirrorCmd()

	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/never/created.git", "", "main", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("dry-run must not report a discovered branch, got %q", branch)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run executed git: %v", runner.joined())
	}
	if len(logged) != 3 {
		t.Fatalf("expected 3 dry-run mutation logs (config, fetch, set-head), got %d", len(logged))
	}
	if !strings.Contains(stderr.String(), `note: would discover default branch for "api"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "could not discover") {
		t.Fatalf("dry-run emitted a false discovery failure: %q", stderr.String())
	}
}

func TestFinalizeBareMirrorFatalOnConfigureAndFetch(t *testing.T) {
	cmd, _, _ := newMirrorCmd()

	client := git.New(git.WithRunner(&mirrorRunner{fail: map[string]bool{"remote.origin.fetch": true}}))
	if _, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "main", true, false); err == nil || !strings.Contains(err.Error(), `configure tracking for "api"`) {
		t.Fatalf("configure error = %v", err)
	}

	client = git.New(git.WithRunner(&mirrorRunner{fail: map[string]bool{"fetch --all": true}}))
	if _, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "main", false, false); err == nil || !strings.Contains(err.Error(), `fetch "api"`) {
		t.Fatalf("fetch error = %v", err)
	}
}

func TestFinalizeBareMirrorSetHeadFailureIsANote(t *testing.T) {
	// origin/HEAD is stale (says main) but the remote now defaults to trunk;
	// after a failed set-head only the remote may be believed.
	runner := &mirrorRunner{
		fail: map[string]bool{"set-head": true},
		outputs: map[string]string{
			"symbolic-ref":       "origin/main\n",
			"remote show origin": "* remote origin\n  HEAD branch: trunk\n",
		},
	}
	client := git.New(git.WithRunner(runner))
	cmd, _, stderr := newMirrorCmd()

	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "main", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "trunk" {
		t.Fatalf("branch = %q, want trunk from the remote (never the stale origin/HEAD)", branch)
	}
	calls := runner.joined()
	for _, call := range calls {
		if strings.Contains(call, "symbolic-ref") {
			t.Fatalf("stale origin/HEAD was consulted after set-head failure: %v", calls)
		}
	}
	if want := "--git-dir /bare/api.git remote show origin"; !strings.Contains(strings.Join(calls, "\n"), want) {
		t.Fatalf("remote was not queried after set-head failure: %v", calls)
	}
	if !strings.Contains(stderr.String(), `note: could not set origin/HEAD for "api"`) {
		t.Fatalf("stderr missing set-head note:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "could not discover") {
		t.Fatalf("discovery succeeded via the remote yet a failure note was emitted:\n%s", stderr.String())
	}
}

func TestFinalizeBareMirrorSetHeadAndRemoteFailureIsANote(t *testing.T) {
	runner := &mirrorRunner{
		fail:    map[string]bool{"set-head": true, "remote show": true},
		outputs: map[string]string{"symbolic-ref": "origin/main\n"},
	}
	client := git.New(git.WithRunner(runner))
	cmd, _, stderr := newMirrorCmd()

	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "main", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q; a stale origin/HEAD must not be trusted after set-head failure", branch)
	}
	for _, call := range runner.joined() {
		if strings.Contains(call, "symbolic-ref") {
			t.Fatalf("stale origin/HEAD was consulted after set-head failure: %v", runner.joined())
		}
	}
	for _, want := range []string{
		`note: could not set origin/HEAD for "api"`,
		`note: could not discover default branch for "api" (origin/HEAD was not refreshed and the remote could not be queried: git --git-dir /bare/api.git remote show origin failed`,
		`fall back to "main"`,
		"stave repos sync",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestApplyBackfill(t *testing.T) {
	orig := map[string]config.Repository{
		"same":      {Name: "same", URL: "u1", BareRepoPath: "/b/same.git"},
		"repoint":   {Name: "repoint", URL: "u2", BareRepoPath: "/b/repoint.git"},
		"moved":     {Name: "moved", URL: "u3", BareRepoPath: "/b/moved.git"},
		"gone":      {Name: "gone", URL: "u4", BareRepoPath: "/b/gone.git"},
		"filled":    {Name: "filled", URL: "u5", BareRepoPath: "/b/filled.git"},
		"untouched": {Name: "untouched", URL: "u6", BareRepoPath: "/b/untouched.git"},
	}
	fresh := &config.Config{Repos: map[string]config.Repository{
		"same":      {Name: "same", URL: "u1", BareRepoPath: "/b/same.git"},
		"repoint":   {Name: "repoint", URL: "u2-other", BareRepoPath: "/b/repoint.git"},
		"moved":     {Name: "moved", URL: "u3", BareRepoPath: "/elsewhere/moved.git"},
		"filled":    {Name: "filled", URL: "u5", BareRepoPath: "/b/filled.git", DefaultBranch: "trunk"},
		"untouched": {Name: "untouched", URL: "u6", BareRepoPath: "/b/untouched.git"},
		"new":       {Name: "new", URL: "u7", BareRepoPath: "/b/new.git"},
	}}
	backfill := map[string]string{"same": "main", "repoint": "main", "moved": "main", "gone": "main", "filled": "main", "new": "main"}

	changed, skipped := applyBackfill(fresh, orig, backfill)
	if want := []string{"same"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if want := []string{"gone", "moved", "new", "repoint"}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %v, want %v", skipped, want)
	}
	if fresh.Repos["same"].DefaultBranch != "main" {
		t.Fatalf("same.DefaultBranch = %q, want main", fresh.Repos["same"].DefaultBranch)
	}
	for _, name := range []string{"repoint", "moved", "untouched", "new"} {
		if got := fresh.Repos[name].DefaultBranch; got != "" {
			t.Fatalf("%s.DefaultBranch = %q, want untouched", name, got)
		}
	}
	if fresh.Repos["filled"].DefaultBranch != "trunk" {
		t.Fatalf("filled.DefaultBranch = %q, want trunk preserved", fresh.Repos["filled"].DefaultBranch)
	}
	if fresh.Repos["repoint"].URL != "u2-other" || fresh.Repos["moved"].BareRepoPath != "/elsewhere/moved.git" {
		t.Fatal("skipped entries were modified")
	}
}

func TestFinalizeBareMirrorDiscoveryFailureNamesFallback(t *testing.T) {
	// Both discovery strategies fail: symbolic-ref and remote show.
	runner := &mirrorRunner{fail: map[string]bool{"symbolic-ref": true, "remote show": true}}
	client := git.New(git.WithRunner(runner))

	// Stored default wins as the fallback when present.
	cmd, _, stderr := newMirrorCmd()
	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "trunk", "main", false, false)
	if err != nil || branch != "" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
	if !strings.Contains(stderr.String(), `could not discover default branch for "api"`) || !strings.Contains(stderr.String(), `fall back to "trunk"`) || !strings.Contains(stderr.String(), "stave repos sync") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	// Config default base is the fallback when nothing is stored.
	cmd, _, stderr = newMirrorCmd()
	if _, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "", "develop", false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), `fall back to "develop"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestFinalizeBareMirrorDriftIsInformational(t *testing.T) {
	runner := &mirrorRunner{outputs: map[string]string{"symbolic-ref": "origin/main\n"}}
	client := git.New(git.WithRunner(runner))
	cmd, _, stderr := newMirrorCmd()

	branch, err := finalizeBareMirror(context.Background(), cmd, client, "api", "/bare/api.git", "nope", "main", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q", branch)
	}
	if !strings.Contains(stderr.String(), `note: remote default branch for "api" is now "main"; registry has "nope"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
