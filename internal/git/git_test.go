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
