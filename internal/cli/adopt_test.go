package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
)

func TestSameRepoURL(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		existing string
		given    string
		want     bool
		reason   string
	}{
		{"same https", "https://github.com/acme/repo.git", "https://github.com/acme/repo.git", true, ""},
		{"same ssh", "git@github.com:acme/repo.git", "git@github.com:acme/repo.git", true, ""},
		{"https vs scp", "https://github.com/acme/repo.git", "git@github.com:acme/repo.git", true, ""},
		{"https vs ssh url", "https://github.com/acme/repo", "ssh://git@github.com/acme/repo.git", true, ""},
		{"ssh alias vs https", "git@hl_external:acme/repo.git", "https://github.com/acme/repo.git", true, ""},
		{"owner case", "https://github.com/Acme/Repo.git", "git@github.com:acme/repo.git", true, ""},
		{"trailing slash", "https://github.com/acme/repo/", "https://github.com/acme/repo", true, ""},
		{"different owner", "git@github.com:other/repo.git", "https://github.com/acme/repo.git", false, `origin remote "git@github.com:other/repo.git" points at other/repo, not acme/repo`},
		{"different repo", "https://github.com/acme/repo.git", "https://github.com/acme/other.git", false, "points at acme/repo, not acme/other"},
		{"path vs path.git", "/srv/x", "/srv/x.git", false, `origin remote "/srv/x" does not match "/srv/x.git"`},
		{"file url vs path", "file:///tmp/x", "/tmp/x", true, ""},
		{"path vs file url", "/tmp/x", "file:///tmp/x", true, ""},
		{"unclean path", "/tmp//x/./", "/tmp/x", true, ""},
		{"symlink vs real", link, real, true, ""},
		{"real vs symlink", real, link, true, ""},
		{"path vs github", "/srv/x", "https://github.com/acme/repo.git", false, "does not match"},
		{"github vs path", "https://github.com/acme/repo.git", "/srv/x", false, "does not match"},
		{"junk equal", "weird://thing", "weird://thing", true, ""},
		{"junk differs", "weird://thing", "weird://other", false, `origin remote "weird://thing" does not match "weird://other"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := sameRepoURL(tc.existing, tc.given)
			if ok != tc.want {
				t.Fatalf("sameRepoURL(%q, %q) = %v (%q), want %v", tc.existing, tc.given, ok, reason, tc.want)
			}
			if ok && reason != "" {
				t.Fatalf("match returned reason %q", reason)
			}
			if !ok && !strings.Contains(reason, tc.reason) {
				t.Fatalf("reason = %q, want substring %q", reason, tc.reason)
			}
		})
	}
}

func TestSameRepoURLRelativePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if ok, reason := sameRepoURL("./repo", dir); !ok {
		t.Fatalf("relative vs absolute did not match: %s", reason)
	}
	if ok, reason := sameRepoURL(dir, "./repo/"); !ok {
		t.Fatalf("absolute vs relative did not match: %s", reason)
	}
	if ok, _ := sameRepoURL("./other", dir); ok {
		t.Fatal("different relative path matched")
	}
}

func bareGitOutput(t *testing.T, bare string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", bare}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git --git-dir %s %v error = %v\n%s", bare, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedBareCache registers and unregisters a repo so its bare cache is left
// behind at the derived path, which is exactly the state --adopt targets.
func seedBareCache(t *testing.T, home, name, src string) string {
	t.Helper()
	runCLI(t, "repos", "add", name, src)
	runCLI(t, "repos", "remove", name)
	bare := filepath.Join(home, "stave", "bare-repos", name+".git")
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare cache missing after remove: %v", err)
	}
	return bare
}

func TestCLIReposAddAdoptReusesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)

	out := runCLI(t, "repos", "add", "repo-a", src, "--adopt")
	if !strings.Contains(out, "adopted existing bare repo") || !strings.Contains(out, "registered repo-a at "+bare) {
		t.Fatalf("adopt output = %s", out)
	}
	if head := bareOriginHead(t, bare); head != "origin/main" {
		t.Fatalf("origin/HEAD = %q, want origin/main", head)
	}
	if fetch := bareGitOutput(t, bare, "config", "remote.origin.fetch"); fetch != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("remote.origin.fetch = %q", fetch)
	}
	if origin := bareGitOutput(t, bare, "remote", "get-url", "origin"); origin != src {
		t.Fatalf("origin url = %q, want %q", origin, src)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := cfg.Repos["repo-a"]
	if !ok {
		t.Fatal("repo-a not registered after adopt")
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", repo.DefaultBranch)
	}
	if repo.BareRepoPath != bare {
		t.Fatalf("bareRepoPath = %q, want %q", repo.BareRepoPath, bare)
	}
}

func TestCLIReposAddAdoptSetsOriginURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", "file://"+src)

	out := runCLI(t, "repos", "add", "repo-a", src, "--adopt")
	if !strings.Contains(out, "adopted existing bare repo") {
		t.Fatalf("adopt output = %s", out)
	}
	if origin := bareGitOutput(t, bare, "remote", "get-url", "origin"); origin != src {
		t.Fatalf("origin url = %q, want %q", origin, src)
	}
}

func TestCLIReposAddAdoptRejectsNonBare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")

	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	reset := "delete it and re-run 'stave repos add repo-a " + shellQuote(src) + "' to clone"
	_, err := runCLIError(t, nil, "repos", "add", "repo-a", src, "--adopt")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") || !strings.Contains(err.Error(), reset) {
		t.Fatalf("plain dir: err = %v, want not a git repository + %q", err, reset)
	}

	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "-b", "main", bare)
	_, err = runCLIError(t, nil, "repos", "add", "repo-a", src, "--adopt")
	if err == nil || !strings.Contains(err.Error(), "not a bare repository") || !strings.Contains(err.Error(), reset) {
		t.Fatalf("non-bare repo: err = %v, want not a bare repository + %q", err, reset)
	}

	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite rejected adopt")
	}
}

func TestCLIReposAddAdoptRejectsWrongRemote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", "git@github.com:other/repo.git")

	_, err := runCLIError(t, nil, "repos", "add", "repo-a", "https://github.com/acme/repo.git", "--adopt")
	if err == nil || !strings.Contains(err.Error(), "cannot adopt "+bare) || !strings.Contains(err.Error(), "points at other/repo, not acme/repo") {
		t.Fatalf("err = %v, want wrong-remote rejection", err)
	}
	if reset := "delete it and re-run 'stave repos add repo-a https://github.com/acme/repo.git' to clone"; !strings.Contains(err.Error(), reset) {
		t.Fatalf("err = %v, want reset path %q", err, reset)
	}
	if origin := bareGitOutput(t, bare, "remote", "get-url", "origin"); origin != "git@github.com:other/repo.git" {
		t.Fatalf("origin url was rewritten to %q", origin)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite rejected adopt")
	}
}

func TestCLIReposAddAdoptWithoutExistingClones(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")

	out := runCLI(t, "repos", "add", "repo-a", src, "--adopt")
	if !strings.Contains(out, "registered repo-a") || strings.Contains(out, "adopted") {
		t.Fatalf("adopt without cache output = %s", out)
	}
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	if head := bareOriginHead(t, bare); head != "origin/main" {
		t.Fatalf("origin/HEAD = %q, want origin/main", head)
	}
}

func TestCLIReposAddCollisionHintNamesFullCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)

	_, err := runCLIError(t, nil, "repos", "add", "repo-a", src)
	if err == nil {
		t.Fatal("expected collision error")
	}
	for _, want := range []string{"bare repo path already exists: " + bare, "stave repos add repo-a " + shellQuote(src) + " --adopt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want substring %q", err, want)
		}
	}
}

func TestCLIReposAddAdoptDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	seeded := "file://" + src
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", seeded)

	out := runCLI(t, "repos", "add", "repo-a", src, "--adopt", "--dry-run")
	if !strings.Contains(out, "would adopt existing bare repo at "+bare) || !strings.Contains(out, "registered repo-a") {
		t.Fatalf("dry-run adopt output = %s", out)
	}
	if !strings.Contains(out, "note: would set origin of "+bare+" to "+src+"\n") {
		t.Fatalf("dry-run adopt should announce the planned origin rewrite:\n%s", out)
	}
	if strings.Contains(out, "note: adopted") || strings.Contains(out, "could not discover") {
		t.Fatalf("dry-run adopt performed work or emitted a false note:\n%s", out)
	}
	if origin := bareGitOutput(t, bare, "remote", "get-url", "origin"); origin != seeded {
		t.Fatalf("dry-run rewrote origin url to %q", origin)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("dry-run adopt persisted repo-a")
	}

	// Without --adopt a dry-run still skips the collision stat and plans a clone.
	plain := runCLI(t, "repos", "add", "repo-a", src, "--dry-run")
	if !strings.Contains(plain, "registered repo-a") || strings.Contains(plain, "adopt") {
		t.Fatalf("plain dry-run output = %s", plain)
	}
}

func TestCLIReposAddAdoptRejectsMissingOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	runGit(t, "", "--git-dir", bare, "remote", "remove", "origin")

	_, err := runCLIError(t, nil, "repos", "add", "repo-a", src, "--adopt")
	reset := "delete it and re-run 'stave repos add repo-a " + shellQuote(src) + "' to clone"
	if err == nil || !strings.Contains(err.Error(), "cannot adopt "+bare) || !strings.Contains(err.Error(), "could not read origin remote") || !strings.Contains(err.Error(), reset) {
		t.Fatalf("err = %v, want missing-origin rejection with reset path", err)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite rejected adopt")
	}
}

// writeGlobalGitConfig points GIT_CONFIG_GLOBAL at a per-test file so git's
// URL rewriting / extra remotes can be scripted without touching the network
// or the developer's real config. Returns the path so a test can neutralise
// it later by rewriting the file.
func writeGlobalGitConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	return path
}

func TestCLIReposAddAdoptRestoresOriginWhenFetchFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	seeded := "git@github.com:acme/x.git"
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", seeded)
	// The https spelling names the same repo, so adoption re-points origin
	// to it; the rewrite then sends the fetch to a path that does not exist.
	writeGlobalGitConfig(t, "[url \"/nonexistent/\"]\n\tinsteadOf = https://github.com/acme/\n")

	out, err := runCLIError(t, nil, "repos", "add", "repo-a", "https://github.com/acme/x.git", "--adopt")
	if err == nil || !strings.Contains(err.Error(), `fetch "repo-a"`) || !strings.Contains(err.Error(), "origin restored to "+seeded) {
		t.Fatalf("err = %v, want fetch failure with origin restored\n%s", err, out)
	}
	if origin := bareGitOutput(t, bare, "remote", "get-url", "origin"); origin != seeded {
		t.Fatalf("origin url = %q after failed adopt, want %q", origin, seeded)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite failed adopt")
	}
}

func TestCLIReposAddKeepsFreshCloneWhenFetchFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	// A global extra remote leaves the clone itself untouched but makes the
	// post-clone 'fetch --all --prune' fail.
	gc := writeGlobalGitConfig(t, "[remote \"broken\"]\n\turl = /nonexistent/x\n\tfetch = +refs/heads/*:refs/remotes/broken/*\n")

	out, err := runCLIError(t, nil, "repos", "add", "repo-a", src)
	if err == nil {
		t.Fatalf("expected fetch failure, got success:\n%s", out)
	}
	for _, want := range []string{`fetch "repo-a"`, "the clone was kept at " + shellQuote(bare), "stave repos add repo-a " + shellQuote(src) + " --adopt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want substring %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(bare, "HEAD")); statErr != nil {
		t.Fatalf("clone should be kept: %v", statErr)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite failed finalize")
	}

	// The suggested retry works once the fetch problem is gone.
	if err := os.WriteFile(gc, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	retry := runCLI(t, "repos", "add", "repo-a", src, "--adopt")
	if !strings.Contains(retry, "adopted existing bare repo") || !strings.Contains(retry, "registered repo-a") {
		t.Fatalf("retry output = %s", retry)
	}
}

// restoreRunner records each git invocation together with the state of the
// context it was handed, so a test can prove a rollback still runs after the
// caller's context was canceled.
type restoreRunner struct {
	calls   [][]string
	ctxErrs []error
	outputs map[string]string // argv substring -> stdout
	fail    map[string]bool   // argv substring -> exit 1
}

func (r *restoreRunner) Run(ctx context.Context, bin string, args []string, opts git.RunOptions) (git.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
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

func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ctx.Err() == nil {
		t.Fatal("context not canceled")
	}
	return ctx
}

func TestRestoreOriginSurvivesCanceledContext(t *testing.T) {
	runner := &restoreRunner{}
	client := git.New(git.WithRunner(runner))
	cause := errors.New("fetch failed")

	err := restoreOrigin(canceledContext(t), client, "/tmp/repo.git", "git@github.com:acme/x.git", cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "origin restored to git@github.com:acme/x.git") {
		t.Fatalf("err = %v, want cause wrapped with restore note", err)
	}
	want := []string{"--git-dir", "/tmp/repo.git", "remote", "set-url", "origin", "git@github.com:acme/x.git"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("calls = %#v, want exactly %#v", runner.calls, want)
	}
	if runner.ctxErrs[0] != nil {
		t.Fatalf("set-url ran under a canceled context (%v); rollback must ignore cancellation", runner.ctxErrs[0])
	}

	runner = &restoreRunner{fail: map[string]bool{"set-url": true}}
	client = git.New(git.WithRunner(runner))
	err = restoreOrigin(canceledContext(t), client, "/tmp/repo.git", "https://tok3n@github.com/acme/x.git", cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "could not restore origin to https://***@github.com/acme/x.git") {
		t.Fatalf("err = %v, want restore failure naming the redacted origin", err)
	}
	if runner.ctxErrs[0] != nil {
		t.Fatalf("set-url ran under a canceled context (%v)", runner.ctxErrs[0])
	}
}

func TestRestoreFetchRefspec(t *testing.T) {
	cause := errors.New("fetch failed")
	custom := "+refs/heads/main:refs/remotes/origin/main"
	getAll := "config --get-all remote.origin.fetch"
	set := func(v string) []string {
		return []string{"--git-dir", "/tmp/repo.git", "config", "remote.origin.fetch", v}
	}
	cases := []struct {
		name      string
		prev      []string
		current   string // scripted --get-all stdout
		failSet   bool
		wantCalls int
		wantSet   []string
		wantMsg   string
		wantNoMsg string
	}{
		{name: "no prior refspec keeps standard", prev: nil, current: git.StandardFetchRefspec + "\n", wantCalls: 0},
		{name: "prior already standard", prev: []string{git.StandardFetchRefspec}, current: git.StandardFetchRefspec + "\n", wantCalls: 0},
		{name: "tracking never ran", prev: []string{custom}, current: custom + "\n", wantCalls: 1},
		{name: "custom restored", prev: []string{custom}, current: git.StandardFetchRefspec + "\n", wantCalls: 2, wantSet: set(custom), wantMsg: "remote.origin.fetch restored to " + custom},
		{name: "custom restore fails", prev: []string{custom}, current: git.StandardFetchRefspec + "\n", failSet: true, wantCalls: 2, wantSet: set(custom), wantMsg: "could not restore remote.origin.fetch to " + custom},
		{name: "multiple replaced is only noted", prev: []string{custom, "+refs/tags/*:refs/tags/*"}, current: git.StandardFetchRefspec + "\n", wantCalls: 1, wantMsg: "note: remote.origin.fetch was replaced by stave's standard refspec"},
		{name: "multiple untouched", prev: []string{custom, "+refs/tags/*:refs/tags/*"}, current: custom + "\n+refs/tags/*:refs/tags/*\n", wantCalls: 1, wantNoMsg: "replaced"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &restoreRunner{outputs: map[string]string{getAll: tc.current}}
			if tc.failSet {
				runner.fail = map[string]bool{"config remote.origin.fetch": true}
			}
			client := git.New(git.WithRunner(runner))
			err := restoreFetchRefspec(canceledContext(t), client, "/tmp/repo.git", tc.prev, cause)
			if !errors.Is(err, cause) {
				t.Fatalf("err = %v, must wrap cause", err)
			}
			if len(runner.calls) != tc.wantCalls {
				t.Fatalf("calls = %#v, want %d", runner.calls, tc.wantCalls)
			}
			for i, ctxErr := range runner.ctxErrs {
				if ctxErr != nil {
					t.Fatalf("call %d ran under a canceled context (%v)", i, ctxErr)
				}
			}
			if tc.wantSet != nil && !reflect.DeepEqual(runner.calls[len(runner.calls)-1], tc.wantSet) {
				t.Fatalf("last call = %#v, want %#v", runner.calls[len(runner.calls)-1], tc.wantSet)
			}
			if tc.wantSet == nil {
				for _, call := range runner.calls {
					if !strings.Contains(strings.Join(call, " "), "--get-all") {
						t.Fatalf("unexpected write %#v", call)
					}
				}
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantMsg)
			}
			if tc.wantMsg == "" && err != cause {
				t.Fatalf("err = %v, want cause returned unchanged", err)
			}
			if tc.wantNoMsg != "" && strings.Contains(err.Error(), tc.wantNoMsg) {
				t.Fatalf("err = %v, must not contain %q", err, tc.wantNoMsg)
			}
		})
	}
}

func TestCLIReposAddAdoptRestoresFetchRefspecWhenFetchFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	custom := "+refs/heads/main:refs/remotes/origin/main"
	runGit(t, "", "--git-dir", bare, "config", "remote.origin.fetch", custom)
	// A global extra remote leaves tracking configuration alone but makes the
	// post-adopt 'fetch --all --prune' fail.
	gc := writeGlobalGitConfig(t, "[remote \"broken\"]\n\turl = /nonexistent/x\n\tfetch = +refs/heads/*:refs/remotes/broken/*\n")

	out, err := runCLIError(t, nil, "repos", "add", "repo-a", src, "--adopt")
	if err == nil || !strings.Contains(err.Error(), `fetch "repo-a"`) || !strings.Contains(err.Error(), "remote.origin.fetch restored to "+custom) {
		t.Fatalf("err = %v, want fetch failure with refspec restored\n%s", err, out)
	}
	if strings.Contains(err.Error(), "origin restored") {
		t.Fatalf("origin was never rewritten, so it must not be reported as restored: %v", err)
	}
	if got := bareGitOutput(t, bare, "config", "--get-all", "remote.origin.fetch"); got != custom {
		t.Fatalf("remote.origin.fetch = %q after failed adopt, want %q", got, custom)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite failed adopt")
	}

	// Once the fetch problem is gone, adoption replaces the custom refspec
	// with stave's standard one for good.
	if err := os.WriteFile(gc, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "repos", "add", "repo-a", src, "--adopt")
	if got := bareGitOutput(t, bare, "config", "--get-all", "remote.origin.fetch"); got != git.StandardFetchRefspec {
		t.Fatalf("remote.origin.fetch = %q after adopt, want %q", got, git.StandardFetchRefspec)
	}
}

func TestCLIReposAddAdoptRefusesMultiValuedFetchRefspec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	runGit(t, "", "--git-dir", bare, "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main")
	runGit(t, "", "--git-dir", bare, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
	before := bareGitOutput(t, bare, "config", "--get-all", "remote.origin.fetch")

	_, err := runCLIError(t, nil, "repos", "add", "repo-a", src, "--adopt")
	if err == nil || !strings.Contains(err.Error(), `configure tracking for "repo-a"`) {
		t.Fatalf("err = %v, want tracking configuration failure", err)
	}
	if strings.Contains(err.Error(), "replaced") || strings.Contains(err.Error(), "restored") {
		t.Fatalf("nothing was changed, so nothing should be reported restored or replaced: %v", err)
	}
	if after := bareGitOutput(t, bare, "config", "--get-all", "remote.origin.fetch"); after != before {
		t.Fatalf("remote.origin.fetch changed from %q to %q", before, after)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Repos["repo-a"]; ok {
		t.Fatal("repo-a was registered despite failed adopt")
	}
}

// TestCLIReposAddAdoptUsesRawOriginUnderInsteadOf pins that adoption compares
// and rolls back the origin URL as configured, not as git rewrites it: with
// both spellings rewritten to a bogus path, an effective-URL comparison would
// reject the cache outright and a rollback would persist the rewritten path.
func TestCLIReposAddAdoptUsesRawOriginUnderInsteadOf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	bare := seedBareCache(t, home, "repo-a", src)
	seeded := "git@github.com:acme/x.git"
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", seeded)
	writeGlobalGitConfig(t, "[url \"/nonexistent/\"]\n\tinsteadOf = https://github.com/acme/\n\tinsteadOf = git@github.com:acme/\n")
	if effective := bareGitOutput(t, bare, "remote", "get-url", "origin"); effective != "/nonexistent/x.git" {
		t.Fatalf("test setup: effective origin = %q, want insteadOf rewrite", effective)
	}

	out, err := runCLIError(t, nil, "repos", "add", "repo-a", "https://github.com/acme/x.git", "--adopt")
	if err == nil || !strings.Contains(err.Error(), `fetch "repo-a"`) || !strings.Contains(err.Error(), "origin restored to "+seeded) {
		t.Fatalf("err = %v, want fetch failure with raw origin restored\n%s", err, out)
	}
	if strings.Contains(err.Error(), "cannot adopt") {
		t.Fatalf("identity check used the rewritten URL: %v", err)
	}
	if raw := bareGitOutput(t, bare, "config", "--get", "remote.origin.url"); raw != seeded {
		t.Fatalf("configured origin = %q after rollback, want raw %q", raw, seeded)
	}
}
