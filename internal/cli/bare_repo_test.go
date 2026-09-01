package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Nurozen/stave/internal/git"
)

func partialSiblings(t *testing.T, dest string) []string {
	t.Helper()
	matches, err := filepath.Glob(dest + ".partial-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestCloneBareFreshSuccessLeavesNoPartial(t *testing.T) {
	src := createGitRepo(t, "source")
	dest := filepath.Join(t.TempDir(), "source.git")

	if err := cloneBareFresh(context.Background(), git.New(), src, dest, false); err != nil {
		t.Fatalf("cloneBareFresh error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "HEAD")); err != nil {
		t.Fatalf("bare repo missing at %s: %v", dest, err)
	}
	if got := partialSiblings(t, dest); len(got) != 0 {
		t.Fatalf("partial clone dirs left behind: %v", got)
	}
	if head := gitOutput(t, "", "--git-dir", dest, "rev-parse", "HEAD"); head != gitOutput(t, src, "rev-parse", "HEAD") {
		t.Fatalf("bare clone HEAD = %q, want source HEAD", head)
	}
	if runtime.GOOS != "windows" {
		// The clone was staged in a MkdirTemp directory (0700); the cache
		// must not inherit that private mode.
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o755 {
			t.Fatalf("cache dir mode = %o, want 755", perm)
		}
	}
}

// TestCloneBareFreshSameDestConcurrently races two clones of the same
// destination: exactly one may win the rename, the loser must report the
// collision and clean up after itself, and the winner's clone must be intact.
func TestCloneBareFreshSameDestConcurrently(t *testing.T) {
	src := createGitRepo(t, "source")
	dest := filepath.Join(t.TempDir(), "source.git")

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = cloneBareFresh(context.Background(), git.New(), src, dest, false)
		}(i)
	}
	wg.Wait()

	var okCount int
	for i, err := range errs {
		if err == nil {
			okCount++
			continue
		}
		if !strings.Contains(err.Error(), "appeared concurrently at "+dest) {
			t.Fatalf("clone %d error = %v, want concurrent-collision report", i, err)
		}
	}
	if okCount != 1 {
		t.Fatalf("%d of 2 concurrent clones succeeded, want exactly 1: %v", okCount, errs)
	}
	if isBare, err := git.New().IsBareRepo(context.Background(), dest); err != nil || !isBare {
		t.Fatalf("dest is not a valid bare repo after the race: %v %v", isBare, err)
	}
	if head := gitOutput(t, "", "--git-dir", dest, "rev-parse", "HEAD"); head != gitOutput(t, src, "rev-parse", "HEAD") {
		t.Fatalf("bare clone HEAD = %q, want source HEAD", head)
	}
	if got := partialSiblings(t, dest); len(got) != 0 {
		t.Fatalf("partial clone dirs left behind by the loser: %v", got)
	}
}

func TestCloneBareFreshFailureLeavesNothing(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "missing.git")
	url := filepath.Join(root, "does-not-exist")

	err := cloneBareFresh(context.Background(), git.New(), url, dest, false)
	if err == nil {
		t.Fatal("cloneBareFresh succeeded cloning a nonexistent source")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest exists after failed clone (stat err = %v)", statErr)
	}
	if got := partialSiblings(t, dest); len(got) != 0 {
		t.Fatalf("partial clone dirs left behind: %v", got)
	}
}

func TestCloneBareFreshDryRunTouchesNothing(t *testing.T) {
	src := createGitRepo(t, "source")
	dest := filepath.Join(t.TempDir(), "source.git")
	var logged []string
	client := git.New(git.WithDryRun(true, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}))

	if err := cloneBareFresh(context.Background(), client, src, dest, true); err != nil {
		t.Fatalf("cloneBareFresh dry-run error = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dry-run created %s (stat err = %v)", dest, err)
	}
	if got := partialSiblings(t, dest); len(got) != 0 {
		t.Fatalf("dry-run created partial dirs: %v", got)
	}
	if len(logged) != 1 {
		t.Fatalf("dry-run logged %d lines, want 1: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], "clone --bare "+src+" "+dest) || strings.Contains(logged[0], ".partial-") {
		t.Fatalf("dry-run plan must name the real destination, got %q", logged[0])
	}
}

func TestCloneBareFreshConcurrentCallsUseDistinctTempDirs(t *testing.T) {
	src := createGitRepo(t, "source")
	root := t.TempDir()
	destA := filepath.Join(root, "a.git")
	destB := filepath.Join(root, "b.git")
	// A stale sibling from an earlier interrupted run must neither be reused
	// nor removed by a later clone of the same destination.
	stale := destA + ".partial-stale"
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, dest := range []string{destA, destB} {
		if err := cloneBareFresh(context.Background(), git.New(), src, dest, false); err != nil {
			t.Fatalf("cloneBareFresh(%s) error = %v", dest, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "HEAD")); err != nil {
			t.Fatalf("bare repo missing at %s: %v", dest, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stale, "marker")); err != nil {
		t.Fatalf("stale sibling from another run was touched: %v", err)
	}
	if got := partialSiblings(t, destA); len(got) != 1 || got[0] != stale {
		t.Fatalf("partial siblings of a.git = %v, want only the stale one", got)
	}
	if got := partialSiblings(t, destB); len(got) != 0 {
		t.Fatalf("partial clone dirs left behind: %v", got)
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://user:tok3n@github.com/acme/x.git", "https://***@github.com/acme/x.git"},
		{"https://tok3n@github.com/acme/x.git", "https://***@github.com/acme/x.git"},
		{"http://u:p@ghe.example.com:8443/acme/x", "http://***@ghe.example.com:8443/acme/x"},
		{"ssh://git:secret@github.com:22/acme/x.git", "ssh://***@github.com:22/acme/x.git"},
		{"ssh://git@github.com/acme/x.git", "ssh://***@github.com/acme/x.git"},
		{"user:pass@github.com:acme/x.git", "***@github.com:acme/x.git"},
		{"git@github.com:acme/x.git", "git@github.com:acme/x.git"},
		{"git@hl_external:acme/x.git", "git@hl_external:acme/x.git"},
		{"https://github.com/acme/x.git", "https://github.com/acme/x.git"},
		{"https://github.com/acme/x@v2/y", "https://github.com/acme/x@v2/y"},
		{"/srv/repos/x.git", "/srv/repos/x.git"},
		{"/srv/repos/me@host:x.git", "/srv/repos/me@host:x.git"},
		{"file:///srv/x.git", "file:///srv/x.git"},
		{"host:path@x", "host:path@x"},
		{"", ""},
	} {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPasteableURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://user:tok3n@github.com/acme/x.git", "<url>"},
		{"https://tok3n@github.com/acme/x.git", "<url>"},
		{"user:pass@github.com:acme/x.git", "<url>"},
		{"git@github.com:acme/x.git", "git@github.com:acme/x.git"},
		{"https://github.com/acme/x.git", "https://github.com/acme/x.git"},
		{"/srv/my repos/x.git", "'/srv/my repos/x.git'"},
		{"", "''"},
	} {
		if got := pasteableURL(tc.in); got != tc.want {
			t.Errorf("pasteableURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := adoptRetryHint("x", "https://user:tok3n@github.com/acme/x.git"); got != "stave repos add x <url> --adopt" {
		t.Fatalf("adoptRetryHint = %q", got)
	}
}
