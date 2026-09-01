package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	bin  string
	args []string
	dir  string
}

type fakeRunner struct {
	results []Result
	errs    []error
	calls   []call
}

func (f *fakeRunner) Run(ctx context.Context, bin string, args []string, opts RunOptions) (Result, error) {
	f.calls = append(f.calls, call{bin: bin, args: append([]string(nil), args...), dir: opts.Dir})
	var result Result
	if len(f.results) > 0 {
		result = f.results[0]
		f.results = f.results[1:]
	}
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	return result, err
}

func TestCommands(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	ctx := context.Background()

	_ = client.CloneBare(ctx, "https://example.test/repo.git", "/tmp/repo.git")
	_ = client.FetchAllPrune(ctx, "/tmp/repo.git")
	_ = client.ConfigureBareRemoteTracking(ctx, "/tmp/repo.git")
	_ = client.WorktreeAddBranch(ctx, "/tmp/repo.git", "/tmp/wt", "stave/x/repo", "origin/main")
	_ = client.WorktreeAddExisting(ctx, "/tmp/repo.git", "/tmp/wt-existing", "stave/x/existing")
	_ = client.WorktreeAddDetached(ctx, "/tmp/repo.git", "/tmp/ref", "origin/main")
	_ = client.WorktreeRemove(ctx, "/tmp/repo.git", "/tmp/wt", true)
	_ = client.WorktreeRemove(ctx, "/tmp/repo.git", "/tmp/wt", false)
	_ = client.WorktreePrune(ctx, "/tmp/repo.git")
	_ = client.CheckoutDetached(ctx, "/tmp/wt", "origin/main")
	out, _ := client.Output(ctx, "status", "--short")

	wants := [][]string{
		{"clone", "--bare", "https://example.test/repo.git", "/tmp/repo.git"},
		{"--git-dir", "/tmp/repo.git", "fetch", "--all", "--prune"},
		{"--git-dir", "/tmp/repo.git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "--no-track", "-b", "stave/x/repo", "/tmp/wt", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "/tmp/wt-existing", "stave/x/existing"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "--detach", "/tmp/ref", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "remove", "--force", "/tmp/wt"},
		{"--git-dir", "/tmp/repo.git", "worktree", "remove", "/tmp/wt"},
		{"--git-dir", "/tmp/repo.git", "worktree", "prune"},
		{"checkout", "--detach", "origin/main"},
		{"status", "--short"},
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d = %#v, want %#v", i, runner.calls[i].args, want)
		}
	}
	if runner.calls[9].dir != "/tmp/wt" {
		t.Fatalf("CheckoutDetached dir = %q", runner.calls[9].dir)
	}
	if out != "" {
		t.Fatalf("Output() = %q", out)
	}
}

func TestWorktreeAddBranchDoesNotTrackStartPoint(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "repo.git")
	worktree := filepath.Join(tmp, "worktree")

	runGitTestCommand(t, "", "init", "--initial-branch=main", source)
	runGitTestCommand(t, source, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, source, "config", "user.name", "Stave Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "initial")

	client := New()
	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	runGitTestCommand(t, "", "--git-dir", bare, "config", "branch.autoSetupMerge", "true")
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}
	if err := client.WorktreeAddBranch(ctx, bare, worktree, "stave/test/repo", "origin/main"); err != nil {
		t.Fatalf("WorktreeAddBranch() error = %v", err)
	}

	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("new worktree branch unexpectedly tracks %q", strings.TrimSpace(string(out)))
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestDirtyAndAheadBehind(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: " M file.go\n"}, {Stdout: "2\t3\n"}}}
	client := New(WithRunner(runner))
	dirty, output, err := client.IsDirty(context.Background(), "/tmp/wt")
	if err != nil || !dirty || output == "" {
		t.Fatalf("IsDirty() = %v %q %v", dirty, output, err)
	}
	ahead, behind, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main")
	if err != nil {
		t.Fatalf("AheadBehind() error = %v", err)
	}
	if ahead != 3 || behind != 2 {
		t.Fatalf("ahead/behind = %d/%d", ahead, behind)
	}
}

func TestBranchExistsHandlesMissingRef(t *testing.T) {
	runner := &fakeRunner{errs: []error{&GitError{ExitCode: 1}}}
	client := New(WithRunner(runner))
	exists, err := client.BranchExists(context.Background(), "/tmp/repo.git", "missing")
	if err != nil {
		t.Fatalf("BranchExists() error = %v", err)
	}
	if exists {
		t.Fatal("missing branch reported as existing")
	}
}

func TestBranchExistsReturnsUnexpectedErrors(t *testing.T) {
	wantErr := errors.New("boom")
	runner := &fakeRunner{errs: []error{wantErr}}
	client := New(WithRunner(runner))
	_, err := client.BranchExists(context.Background(), "/tmp/repo.git", "main")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestBranchExistsTrimsFullRefAndFindsBranch(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	exists, err := client.BranchExists(context.Background(), "/tmp/repo.git", "refs/heads/main")
	if err != nil {
		t.Fatalf("BranchExists() error = %v", err)
	}
	if !exists {
		t.Fatal("existing branch reported missing")
	}
	want := []string{"--git-dir", "/tmp/repo.git", "show-ref", "--verify", "--quiet", "refs/heads/main"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].args, want)
	}
}

func TestIsAncestorHandlesNonAncestor(t *testing.T) {
	runner := &fakeRunner{errs: []error{&GitError{ExitCode: 1}}}
	client := New(WithRunner(runner))
	ok, err := client.IsAncestor(context.Background(), "/tmp/repo.git", "child", "parent")
	if err != nil {
		t.Fatalf("IsAncestor() error = %v", err)
	}
	if ok {
		t.Fatal("non-ancestor reported as ancestor")
	}
}

func TestIsAncestorReturnsUnexpectedErrors(t *testing.T) {
	wantErr := errors.New("boom")
	runner := &fakeRunner{errs: []error{wantErr}}
	client := New(WithRunner(runner))
	_, err := client.IsAncestor(context.Background(), "/tmp/repo.git", "parent", "child")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestIsAncestorReportsAncestorWithExactArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	ok, err := client.IsAncestor(context.Background(), "/tmp/repo.git", "origin/main", "refs/heads/stave/x/repo")
	if err != nil {
		t.Fatalf("IsAncestor() error = %v", err)
	}
	if !ok {
		t.Fatal("ancestor reported as non-ancestor")
	}
	want := []string{"--git-dir", "/tmp/repo.git", "merge-base", "--is-ancestor", "origin/main", "refs/heads/stave/x/repo"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].args, want)
	}
}

func TestRefExistsHandlesMissingRef(t *testing.T) {
	runner := &fakeRunner{errs: []error{&GitError{ExitCode: 1}}}
	client := New(WithRunner(runner))
	exists, err := client.RefExists(context.Background(), "/tmp/repo.git", "refs/remotes/origin/missing")
	if err != nil {
		t.Fatalf("RefExists() error = %v", err)
	}
	if exists {
		t.Fatal("missing ref reported as existing")
	}
}

func TestRefExistsReturnsUnexpectedErrors(t *testing.T) {
	wantErr := errors.New("boom")
	runner := &fakeRunner{errs: []error{wantErr}}
	client := New(WithRunner(runner))
	_, err := client.RefExists(context.Background(), "/tmp/repo.git", "refs/heads/main")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestRefExistsPassesFullRefVerbatim(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	exists, err := client.RefExists(context.Background(), "/tmp/repo.git", "refs/remotes/origin/pr/7")
	if err != nil {
		t.Fatalf("RefExists() error = %v", err)
	}
	if !exists {
		t.Fatal("existing ref reported missing")
	}
	want := []string{"--git-dir", "/tmp/repo.git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/pr/7"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].args, want)
	}
}

func TestIsAncestorAndRefExistsAgainstRealRepo(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "repo.git")

	runGitTestCommand(t, "", "init", "--initial-branch=main", source)
	runGitTestCommand(t, source, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, source, "config", "user.name", "Stave Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "parent")
	client := New()
	parent, err := client.OutputIn(ctx, source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse parent error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "child")
	child, err := client.OutputIn(ctx, source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse child error = %v", err)
	}

	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}

	parent = strings.TrimSpace(parent)
	child = strings.TrimSpace(child)
	if ok, err := client.IsAncestor(ctx, bare, parent, child); err != nil || !ok {
		t.Fatalf("IsAncestor(parent, child) = %v %v, want true", ok, err)
	}
	if ok, err := client.IsAncestor(ctx, bare, child, parent); err != nil || ok {
		t.Fatalf("IsAncestor(child, parent) = %v %v, want false", ok, err)
	}
	if exists, err := client.RefExists(ctx, bare, "refs/heads/main"); err != nil || !exists {
		t.Fatalf("RefExists(refs/heads/main) = %v %v, want true", exists, err)
	}
	if exists, err := client.RefExists(ctx, bare, "refs/heads/absent"); err != nil || exists {
		t.Fatalf("RefExists(refs/heads/absent) = %v %v, want false", exists, err)
	}
	if sha, err := client.RevParse(ctx, bare, "refs/heads/main"); err != nil || sha != child {
		t.Fatalf("RevParse(refs/heads/main) = %q %v, want %q", sha, err, child)
	}
	if sha, err := client.RevParse(ctx, bare, "refs/heads/absent"); err == nil || sha != "" {
		t.Fatalf("RevParse(refs/heads/absent) = %q %v, want error", sha, err)
	} else if !IsExitCode(err, 1) {
		t.Fatalf("RevParse(refs/heads/absent) error = %v, want git exit 1", err)
	}
}

func TestRevParsePassesExactArgsAndTrims(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "0123456789abcdef0123456789abcdef01234567\n"}}}
	client := New(WithRunner(runner))
	sha, err := client.RevParse(context.Background(), "/repo.git", "refs/remotes/origin/pr/7")
	if err != nil {
		t.Fatalf("RevParse() error = %v", err)
	}
	if sha != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("RevParse() = %q, want trimmed SHA", sha)
	}
	want := []string{"--git-dir", "/repo.git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/pr/7"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("RevParse() calls = %#v, want %v", runner.calls, want)
	}

	wantErr := &GitError{ExitCode: 1}
	client = New(WithRunner(&fakeRunner{errs: []error{wantErr}}))
	if sha, err := client.RevParse(context.Background(), "/repo.git", "refs/heads/absent"); err != wantErr || sha != "" {
		t.Fatalf("RevParse(absent) = %q %v, want error %v", sha, err, wantErr)
	}

	// RevParse is a probe: it runs even under DryRun.
	runner = &fakeRunner{results: []Result{{Stdout: "abc\n"}}}
	client = New(WithRunner(runner), WithDryRun(true, func(string, ...any) { t.Fatal("probe must not be logged as a dry-run mutation") }))
	if sha, err := client.RevParse(context.Background(), "/repo.git", "refs/heads/main"); err != nil || sha != "abc" {
		t.Fatalf("RevParse() under DryRun = %q %v, want abc", sha, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("RevParse() under DryRun made %d calls, want 1", len(runner.calls))
	}
}

func TestRemoteDefaultBranchParsesSymbolicRefAndRemoteShowFallback(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "origin/trunk\n"}}}
	client := New(WithRunner(runner))
	branch, err := client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git")
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(symbolic-ref) error = %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("symbolic-ref branch = %q", branch)
	}

	runner = &fakeRunner{
		errs:    []error{&GitError{ExitCode: 1}, nil},
		results: []Result{{}, {Stdout: "* remote origin\n  HEAD branch: main\n"}},
	}
	client = New(WithRunner(runner))
	branch, err = client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git")
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(fallback) error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("fallback branch = %q", branch)
	}

	runner = &fakeRunner{
		errs:    []error{&GitError{ExitCode: 1}, nil},
		results: []Result{{}, {Stdout: "no head here\n"}},
	}
	client = New(WithRunner(runner))
	if _, err := client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git"); err == nil {
		t.Fatal("RemoteDefaultBranch accepted remote show output without HEAD branch")
	}
}

func TestAheadBehindAndDirtyErrors(t *testing.T) {
	client := New(WithRunner(&fakeRunner{errs: []error{errors.New("status failed")}}))
	if dirty, output, err := client.IsDirty(context.Background(), "/tmp/wt"); err == nil || dirty || output != "" {
		t.Fatalf("IsDirty(error) = %v %q %v", dirty, output, err)
	}

	for _, stdout := range []string{"only-one-field\n", "bad\t2\n", "1\tbad\n"} {
		client = New(WithRunner(&fakeRunner{results: []Result{{Stdout: stdout}}}))
		if _, _, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main"); err == nil {
			t.Fatalf("AheadBehind accepted %q", stdout)
		}
	}
}

func TestDryRunLogsCommandsWithoutCallingRunner(t *testing.T) {
	runner := &fakeRunner{}
	var logs []string
	client := New(
		WithRunner(runner),
		WithDryRun(true, func(format string, args ...any) {
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		}),
	)
	if _, err := client.OutputIn(context.Background(), "/tmp/wt", "status", "--short"); err != nil {
		t.Fatalf("OutputIn(dry-run) error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run called runner: %#v", runner.calls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "dry-run: (cd /tmp/wt && git status --short)") {
		t.Fatalf("logs = %#v", logs)
	}

	client = New(WithRunner(runner), WithDryRun(true, nil))
	if err := client.FetchAllPrune(context.Background(), "/tmp/repo.git"); err != nil {
		t.Fatalf("FetchAllPrune(dry-run no log) error = %v", err)
	}
}

func TestDryRunExecutesReadOnlyProbesWithoutLoggingThem(t *testing.T) {
	runner := &fakeRunner{results: []Result{
		{Stdout: " M file.txt\n"},
		{Stdout: "2\t3\n"},
	}}
	var logs []string
	client := New(
		WithRunner(runner),
		WithDryRun(true, func(format string, args ...any) {
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		}),
	)

	dirty, output, err := client.IsDirty(context.Background(), "/tmp/wt")
	if err != nil || !dirty || !strings.Contains(output, "file.txt") {
		t.Fatalf("IsDirty(dry-run probe) = %v %q %v", dirty, output, err)
	}
	ahead, behind, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main")
	if err != nil || ahead != 3 || behind != 2 {
		t.Fatalf("AheadBehind(dry-run probe) = ahead %d behind %d err %v", ahead, behind, err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("read-only probes did not execute: %#v", runner.calls)
	}
	if len(logs) != 0 {
		t.Fatalf("read-only probes were presented as planned mutations: %#v", logs)
	}

	if err := client.FetchAllPrune(context.Background(), "/tmp/repo.git"); err != nil {
		t.Fatalf("FetchAllPrune(dry-run) error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("mutating dry-run command executed: %#v", runner.calls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "fetch --all --prune") {
		t.Fatalf("mutating dry-run command log = %#v", logs)
	}
}

func TestDryRunTreatsBareRepoAndRemoteURLAsProbesAndRemoteWritesAsMutations(t *testing.T) {
	runner := &fakeRunner{results: []Result{
		{Stdout: "true\n"},
		{Stdout: "https://example.test/repo.git\n"},
	}}
	var logs []string
	client := New(
		WithRunner(runner),
		WithDryRun(true, func(format string, args ...any) {
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		}),
	)

	bare, err := client.IsBareRepo(context.Background(), "/tmp/repo.git")
	if err != nil || !bare {
		t.Fatalf("IsBareRepo(dry-run probe) = %v %v", bare, err)
	}
	url, err := client.RemoteURL(context.Background(), "/tmp/repo.git", "origin")
	if err != nil || url != "https://example.test/repo.git" {
		t.Fatalf("RemoteURL(dry-run probe) = %q %v", url, err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("read-only probes did not execute: %#v", runner.calls)
	}
	if len(logs) != 0 {
		t.Fatalf("read-only probes were presented as planned mutations: %#v", logs)
	}

	if err := client.SetRemoteHead(context.Background(), "/tmp/repo.git"); err != nil {
		t.Fatalf("SetRemoteHead(dry-run) error = %v", err)
	}
	if err := client.SetRemoteURL(context.Background(), "/tmp/repo.git", "https://example.test/moved.git"); err != nil {
		t.Fatalf("SetRemoteURL(dry-run) error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("mutating dry-run command executed: %#v", runner.calls)
	}
	if len(logs) != 2 ||
		!strings.Contains(logs[0], "dry-run: git --git-dir /tmp/repo.git remote set-head origin --auto") ||
		!strings.Contains(logs[1], "dry-run: git --git-dir /tmp/repo.git remote set-url origin https://example.test/moved.git") {
		t.Fatalf("mutating dry-run command logs = %#v", logs)
	}
}

func TestGitErrorFormattingAndExecRunner(t *testing.T) {
	cause := errors.New("exit")
	err := &GitError{Args: []string{"status"}, ExitCode: 7, Stderr: " fatal\n", Err: cause}
	if !strings.Contains(err.Error(), "exit code 7") || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("GitError message = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("GitError did not unwrap cause")
	}
	if !IsExitCode(err, 7) || IsExitCode(cause, 7) {
		t.Fatalf("IsExitCode mismatch")
	}

	client := &Client{bin: "sh"}
	out, runErr := client.Output(context.Background(), "-c", "printf ok")
	if runErr != nil || out != "ok" {
		t.Fatalf("exec success = %q %v", out, runErr)
	}
	_, runErr = client.Output(context.Background(), "-c", "printf err >&2; exit 6")
	var gitErr *GitError
	if !errors.As(runErr, &gitErr) || gitErr.ExitCode != 6 || strings.TrimSpace(gitErr.Stderr) != "err" {
		t.Fatalf("exec error = %#v", runErr)
	}
}

func TestBareRepoAndRemoteCommandsPassExactArgs(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "true\n"}, {Stdout: "git@example.test:org/repo.git\n"}}}
	client := New(WithRunner(runner))
	ctx := context.Background()

	bare, err := client.IsBareRepo(ctx, "/tmp/repo.git")
	if err != nil || !bare {
		t.Fatalf("IsBareRepo() = %v %v", bare, err)
	}
	url, err := client.RemoteURL(ctx, "/tmp/repo.git", "upstream")
	if err != nil || url != "git@example.test:org/repo.git" {
		t.Fatalf("RemoteURL() = %q %v", url, err)
	}
	if err := client.SetRemoteHead(ctx, "/tmp/repo.git"); err != nil {
		t.Fatalf("SetRemoteHead() error = %v", err)
	}
	if err := client.SetRemoteURL(ctx, "/tmp/repo.git", "https://example.test/moved.git"); err != nil {
		t.Fatalf("SetRemoteURL() error = %v", err)
	}

	wants := [][]string{
		{"--git-dir", "/tmp/repo.git", "rev-parse", "--is-bare-repository"},
		{"--git-dir", "/tmp/repo.git", "remote", "get-url", "upstream"},
		{"--git-dir", "/tmp/repo.git", "remote", "set-head", "origin", "--auto"},
		{"--git-dir", "/tmp/repo.git", "remote", "set-url", "origin", "https://example.test/moved.git"},
	}
	if len(runner.calls) != len(wants) {
		t.Fatalf("calls = %d, want %d: %#v", len(runner.calls), len(wants), runner.calls)
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d = %#v, want %#v", i, runner.calls[i].args, want)
		}
		if runner.calls[i].dir != "" {
			t.Fatalf("call %d ran in dir %q, want --git-dir addressing", i, runner.calls[i].dir)
		}
	}
}

func TestIsBareRepoParsesOutput(t *testing.T) {
	for _, tc := range []struct {
		stdout string
		want   bool
	}{
		{"true\n", true},
		{"false", false},
		{"  true  \n", true},
	} {
		client := New(WithRunner(&fakeRunner{results: []Result{{Stdout: tc.stdout}}}))
		got, err := client.IsBareRepo(context.Background(), "/tmp/repo.git")
		if err != nil || got != tc.want {
			t.Fatalf("IsBareRepo(%q) = %v %v, want %v", tc.stdout, got, err, tc.want)
		}
	}

	for _, stdout := range []string{"", "maybe\n", "true false\n"} {
		client := New(WithRunner(&fakeRunner{results: []Result{{Stdout: stdout}}}))
		if got, err := client.IsBareRepo(context.Background(), "/tmp/repo.git"); err == nil || got {
			t.Fatalf("IsBareRepo(%q) = %v %v, want error", stdout, got, err)
		}
	}
}

func TestIsBareRepoPropagatesGitError(t *testing.T) {
	wantErr := &GitError{Args: []string{"rev-parse"}, ExitCode: 128, Stderr: "fatal: not a git repository"}
	client := New(WithRunner(&fakeRunner{errs: []error{wantErr}}))
	got, err := client.IsBareRepo(context.Background(), "/tmp/not-a-repo")
	if got {
		t.Fatal("not-a-repo reported as bare")
	}
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr != wantErr {
		t.Fatalf("error = %#v, want the runner's GitError unchanged", err)
	}
	if !IsExitCode(err, 128) {
		t.Fatalf("IsExitCode(err, 128) = false for %v", err)
	}

	client = New(WithRunner(&fakeRunner{errs: []error{errors.New("boom")}}))
	if _, err := client.RemoteURL(context.Background(), "/tmp/repo.git", "origin"); err == nil {
		t.Fatal("RemoteURL swallowed runner error")
	}
}

func TestRemoteHeadAndBareProbesAgainstRealRepo(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "repo.git")
	plain := filepath.Join(tmp, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	runGitTestCommand(t, "", "init", "--initial-branch=main", source)
	runGitTestCommand(t, source, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, source, "config", "user.name", "Stave Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "initial")

	client := New()
	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}
	if err := client.SetRemoteHead(ctx, bare); err != nil {
		t.Fatalf("SetRemoteHead() error = %v", err)
	}

	if branch, err := client.RemoteDefaultBranch(ctx, bare); err != nil || branch != "main" {
		t.Fatalf("RemoteDefaultBranch() = %q %v, want main", branch, err)
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", bare, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("symbolic-ref fast path did not resolve after SetRemoteHead: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "origin/main" {
		t.Fatalf("refs/remotes/origin/HEAD = %q, want origin/main", got)
	}

	if isBare, err := client.IsBareRepo(ctx, bare); err != nil || !isBare {
		t.Fatalf("IsBareRepo(bare) = %v %v, want true", isBare, err)
	}
	if isBare, err := client.IsBareRepo(ctx, filepath.Join(source, ".git")); err != nil || isBare {
		t.Fatalf("IsBareRepo(worktree .git) = %v %v, want false", isBare, err)
	}
	// A worktree's top-level directory is not itself a git dir; git rejects it
	// the same way it rejects any non-repository path.
	for _, path := range []string{source, plain} {
		isBare, err := client.IsBareRepo(ctx, path)
		if err == nil || isBare {
			t.Fatalf("IsBareRepo(%q) = %v %v, want error", path, isBare, err)
		}
		if !IsExitCode(err, 128) {
			t.Fatalf("IsBareRepo(%q) error = %v, want git exit 128", path, err)
		}
	}

	if url, err := client.RemoteURL(ctx, bare, "origin"); err != nil || url != source {
		t.Fatalf("RemoteURL(origin) = %q %v, want %q", url, err, source)
	}
	moved := filepath.Join(tmp, "moved")
	if err := client.SetRemoteURL(ctx, bare, moved); err != nil {
		t.Fatalf("SetRemoteURL() error = %v", err)
	}
	if url, err := client.RemoteURL(ctx, bare, "origin"); err != nil || url != moved {
		t.Fatalf("RemoteURL(origin) after SetRemoteURL = %q %v, want %q", url, err, moved)
	}
	if _, err := client.RemoteURL(ctx, bare, "nonexistent"); err == nil {
		t.Fatal("RemoteURL(nonexistent) succeeded")
	}
}

func TestHeadCommitProbesWorktreeHead(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "0123456789abcdef0123456789abcdef01234567\n"}}}
	client := New(WithRunner(runner))
	sha, err := client.HeadCommit(context.Background(), "/space/repo")
	if err != nil {
		t.Fatalf("HeadCommit() error = %v", err)
	}
	if sha != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("HeadCommit() = %q, want trimmed SHA", sha)
	}
	want := []string{"rev-parse", "--verify", "--quiet", "HEAD"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) || runner.calls[0].dir != "/space/repo" {
		t.Fatalf("HeadCommit() calls = %#v, want %v in dir /space/repo", runner.calls, want)
	}

	wantErr := &GitError{ExitCode: 128}
	client = New(WithRunner(&fakeRunner{errs: []error{wantErr}}))
	if sha, err := client.HeadCommit(context.Background(), "/space/gone"); err != wantErr || sha != "" {
		t.Fatalf("HeadCommit(gone) = %q %v, want error %v", sha, err, wantErr)
	}

	// HeadCommit is a probe: it runs even under DryRun.
	runner = &fakeRunner{results: []Result{{Stdout: "abc\n"}}}
	client = New(WithRunner(runner), WithDryRun(true, func(string, ...any) { t.Fatal("probe must not be logged as a dry-run mutation") }))
	if sha, err := client.HeadCommit(context.Background(), "/space/repo"); err != nil || sha != "abc" {
		t.Fatalf("HeadCommit() under DryRun = %q %v, want abc", sha, err)
	}
}

// TestHeadCommitAgainstRealRepo checks that a detached checkout is reported
// as the worktree's HEAD even though the branch ref did not move.
func TestHeadCommitAgainstRealRepo(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")

	runGitTestCommand(t, "", "init", "--initial-branch=main", source)
	runGitTestCommand(t, source, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, source, "config", "user.name", "Stave Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "parent")
	client := New()
	parent, err := client.OutputIn(ctx, source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse parent error = %v", err)
	}
	parent = strings.TrimSpace(parent)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "README.md")
	runGitTestCommand(t, source, "commit", "-m", "child")
	child, err := client.OutputIn(ctx, source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse child error = %v", err)
	}
	child = strings.TrimSpace(child)

	if sha, err := client.HeadCommit(ctx, source); err != nil || sha != child {
		t.Fatalf("HeadCommit() = %q %v, want %q", sha, err, child)
	}
	runGitTestCommand(t, source, "checkout", "--detach", parent)
	if sha, err := client.HeadCommit(ctx, source); err != nil || sha != parent {
		t.Fatalf("HeadCommit() after detach = %q %v, want %q", sha, err, parent)
	}
	branch, err := client.OutputIn(ctx, source, "rev-parse", "refs/heads/main")
	if err != nil || strings.TrimSpace(branch) != child {
		t.Fatalf("branch ref moved with the detach: %q %v, want %q", branch, err, child)
	}
	if _, err := client.HeadCommit(ctx, filepath.Join(tmp, "not-a-repo")); err == nil {
		t.Fatalf("HeadCommit(not-a-repo) = nil error, want failure")
	}
}

func TestRemoteDefaultBranchFromRemoteNeverReadsSymbolicRef(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "* remote origin\n  Fetch URL: x\n  HEAD branch: trunk\n"}}}
	client := New(WithRunner(runner))
	branch, err := client.RemoteDefaultBranchFromRemote(context.Background(), "/tmp/repo.git")
	if err != nil {
		t.Fatalf("RemoteDefaultBranchFromRemote error = %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("branch = %q, want trunk", branch)
	}
	want := []string{"--git-dir", "/tmp/repo.git", "remote", "show", "origin"}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0].args, " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %v, want exactly %v", runner.calls, want)
	}

	wantErr := &GitError{ExitCode: 128}
	client = New(WithRunner(&fakeRunner{errs: []error{wantErr}}))
	if _, err := client.RemoteDefaultBranchFromRemote(context.Background(), "/tmp/repo.git"); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	client = New(WithRunner(&fakeRunner{results: []Result{{Stdout: "no head here\n"}}}))
	if _, err := client.RemoteDefaultBranchFromRemote(context.Background(), "/tmp/repo.git"); err == nil {
		t.Fatal("accepted remote show output without HEAD branch")
	}
}

func TestRemoteConfigAndFetchRefspecCommandsPassExactArgs(t *testing.T) {
	runner := &fakeRunner{results: []Result{
		{Stdout: "git@github.com:org/repo.git\n"},
		{Stdout: "+refs/heads/a:refs/remotes/origin/a\n+refs/heads/b:refs/remotes/origin/b\n"},
	}}
	client := New(WithRunner(runner))
	ctx := context.Background()

	url, err := client.RemoteConfigURL(ctx, "/tmp/repo.git", "upstream")
	if err != nil || url != "git@github.com:org/repo.git" {
		t.Fatalf("RemoteConfigURL() = %q %v", url, err)
	}
	refspecs, err := client.RemoteFetchRefspecs(ctx, "/tmp/repo.git", "upstream")
	if err != nil || !reflect.DeepEqual(refspecs, []string{"+refs/heads/a:refs/remotes/origin/a", "+refs/heads/b:refs/remotes/origin/b"}) {
		t.Fatalf("RemoteFetchRefspecs() = %#v %v", refspecs, err)
	}
	if err := client.SetRemoteFetchRefspec(ctx, "/tmp/repo.git", "+refs/heads/main:refs/remotes/origin/main"); err != nil {
		t.Fatalf("SetRemoteFetchRefspec() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, "/tmp/repo.git"); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}

	wants := [][]string{
		{"--git-dir", "/tmp/repo.git", "config", "--get", "remote.upstream.url"},
		{"--git-dir", "/tmp/repo.git", "config", "--get-all", "remote.upstream.fetch"},
		{"--git-dir", "/tmp/repo.git", "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main"},
		{"--git-dir", "/tmp/repo.git", "config", "remote.origin.fetch", StandardFetchRefspec},
	}
	if len(runner.calls) != len(wants) {
		t.Fatalf("calls = %d, want %d: %#v", len(runner.calls), len(wants), runner.calls)
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d = %#v, want %#v", i, runner.calls[i].args, want)
		}
		if runner.calls[i].dir != "" {
			t.Fatalf("call %d ran in dir %q, want --git-dir addressing", i, runner.calls[i].dir)
		}
	}

	// No refspec configured: git exits 1, which is "none", not an error.
	client = New(WithRunner(&fakeRunner{errs: []error{&GitError{ExitCode: 1}}}))
	if refspecs, err := client.RemoteFetchRefspecs(ctx, "/tmp/repo.git", "origin"); err != nil || refspecs != nil {
		t.Fatalf("RemoteFetchRefspecs(none) = %#v %v, want nil, nil", refspecs, err)
	}
	wantErr := &GitError{ExitCode: 128, Stderr: "not a git repository"}
	client = New(WithRunner(&fakeRunner{errs: []error{wantErr}}))
	if _, err := client.RemoteFetchRefspecs(ctx, "/tmp/repo.git", "origin"); !errors.Is(err, wantErr) {
		t.Fatalf("RemoteFetchRefspecs(exit 128) error = %v, want %v", err, wantErr)
	}
	client = New(WithRunner(&fakeRunner{errs: []error{&GitError{ExitCode: 1}}}))
	if _, err := client.RemoteConfigURL(ctx, "/tmp/repo.git", "origin"); !IsExitCode(err, 1) {
		t.Fatalf("RemoteConfigURL(missing) error = %v, want git exit 1", err)
	}
}

func TestDryRunTreatsRemoteConfigReadsAsProbesAndFetchRefspecWriteAsMutation(t *testing.T) {
	runner := &fakeRunner{results: []Result{
		{Stdout: "https://example.test/repo.git\n"},
		{Stdout: StandardFetchRefspec + "\n"},
	}}
	var logs []string
	client := New(
		WithRunner(runner),
		WithDryRun(true, func(format string, args ...any) {
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		}),
	)
	ctx := context.Background()

	if url, err := client.RemoteConfigURL(ctx, "/tmp/repo.git", "origin"); err != nil || url != "https://example.test/repo.git" {
		t.Fatalf("RemoteConfigURL(dry-run probe) = %q %v", url, err)
	}
	if refspecs, err := client.RemoteFetchRefspecs(ctx, "/tmp/repo.git", "origin"); err != nil || !reflect.DeepEqual(refspecs, []string{StandardFetchRefspec}) {
		t.Fatalf("RemoteFetchRefspecs(dry-run probe) = %#v %v", refspecs, err)
	}
	if len(runner.calls) != 2 || len(logs) != 0 {
		t.Fatalf("read-only probes must execute unlogged under dry-run: calls=%#v logs=%#v", runner.calls, logs)
	}
	if err := client.SetRemoteFetchRefspec(ctx, "/tmp/repo.git", "+refs/heads/x:refs/remotes/origin/x"); err != nil {
		t.Fatalf("SetRemoteFetchRefspec(dry-run) error = %v", err)
	}
	if len(runner.calls) != 2 || len(logs) != 1 || !strings.Contains(logs[0], "dry-run: git --git-dir /tmp/repo.git config remote.origin.fetch +refs/heads/x:refs/remotes/origin/x") {
		t.Fatalf("mutating dry-run command: calls=%#v logs=%#v", runner.calls, logs)
	}
}

// TestRemoteConfigURLIsRawWhileRemoteURLIsRewritten pins the distinction the
// adopt/purge code relies on: 'remote get-url' expands url.<base>.insteadOf,
// 'config --get remote.origin.url' does not.
func TestRemoteConfigURLIsRawWhileRemoteURLIsRewritten(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "repo.git")

	runGitTestCommand(t, "", "init", "--initial-branch=main", source)
	runGitTestCommand(t, source, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, source, "config", "user.name", "Stave Test")
	runGitTestCommand(t, source, "commit", "--allow-empty", "-m", "initial")

	client := New()
	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}

	// Rewrite every URL under tmp to a path that does not exist. The clone
	// above ran before the rewrite, so the configured origin is still source.
	rewritten := filepath.Join(t.TempDir(), "elsewhere")
	global := filepath.Join(t.TempDir(), "gitconfig")
	contents := fmt.Sprintf("[url %q]\n\tinsteadOf = %s\n", rewritten+string(filepath.Separator), tmp+string(filepath.Separator))
	if err := os.WriteFile(global, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	effective, err := client.RemoteURL(ctx, bare, "origin")
	if err != nil {
		t.Fatalf("RemoteURL() error = %v", err)
	}
	if want := filepath.Join(rewritten, "source"); effective != want {
		t.Fatalf("RemoteURL() = %q, want insteadOf-rewritten %q", effective, want)
	}
	raw, err := client.RemoteConfigURL(ctx, bare, "origin")
	if err != nil {
		t.Fatalf("RemoteConfigURL() error = %v", err)
	}
	if raw != source {
		t.Fatalf("RemoteConfigURL() = %q, want raw configured %q", raw, source)
	}
	if _, err := client.RemoteConfigURL(ctx, bare, "nonexistent"); !IsExitCode(err, 1) {
		t.Fatalf("RemoteConfigURL(nonexistent) error = %v, want git exit 1", err)
	}

	// Fetch refspecs against the real repo: a bare clone starts with none.
	if refspecs, err := client.RemoteFetchRefspecs(ctx, bare, "origin"); err != nil || refspecs != nil {
		t.Fatalf("RemoteFetchRefspecs(fresh bare clone) = %#v %v, want nil, nil", refspecs, err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	if refspecs, err := client.RemoteFetchRefspecs(ctx, bare, "origin"); err != nil || !reflect.DeepEqual(refspecs, []string{StandardFetchRefspec}) {
		t.Fatalf("RemoteFetchRefspecs(after tracking) = %#v %v", refspecs, err)
	}
	custom := "+refs/heads/main:refs/remotes/origin/main"
	if err := client.SetRemoteFetchRefspec(ctx, bare, custom); err != nil {
		t.Fatalf("SetRemoteFetchRefspec() error = %v", err)
	}
	if refspecs, err := client.RemoteFetchRefspecs(ctx, bare, "origin"); err != nil || !reflect.DeepEqual(refspecs, []string{custom}) {
		t.Fatalf("RemoteFetchRefspecs(after custom) = %#v %v", refspecs, err)
	}
	// Several values are reported in order, and a single-value write refuses
	// to collapse them (git exit 5) rather than silently dropping one.
	runGitTestCommand(t, "", "--git-dir", bare, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
	if refspecs, err := client.RemoteFetchRefspecs(ctx, bare, "origin"); err != nil || !reflect.DeepEqual(refspecs, []string{custom, "+refs/tags/*:refs/tags/*"}) {
		t.Fatalf("RemoteFetchRefspecs(multi) = %#v %v", refspecs, err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); !IsExitCode(err, 5) {
		t.Fatalf("ConfigureBareRemoteTracking(multi-valued) error = %v, want git exit 5", err)
	}
}
