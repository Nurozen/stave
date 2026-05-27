package git

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type call struct {
	args []string
	dir  string
}

type fakeRunner struct {
	results []Result
	errs    []error
	calls   []call
}

func (f *fakeRunner) Run(ctx context.Context, bin string, args []string, opts RunOptions) (Result, error) {
	f.calls = append(f.calls, call{args: append([]string(nil), args...), dir: opts.Dir})
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
	_ = client.WorktreeAddBranch(ctx, "/tmp/repo.git", "/tmp/wt", "stave/x/repo", "origin/main")
	_ = client.WorktreeAddDetached(ctx, "/tmp/repo.git", "/tmp/ref", "origin/main")
	_ = client.WorktreeRemove(ctx, "/tmp/repo.git", "/tmp/wt", true)

	wants := [][]string{
		{"clone", "--bare", "https://example.test/repo.git", "/tmp/repo.git"},
		{"--git-dir", "/tmp/repo.git", "fetch", "--all", "--prune"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "-b", "stave/x/repo", "/tmp/wt", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "--detach", "/tmp/ref", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "remove", "--force", "/tmp/wt"},
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d = %#v, want %#v", i, runner.calls[i].args, want)
		}
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
