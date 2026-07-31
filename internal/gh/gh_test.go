package gh

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// fakeRunner returns canned output after asserting the invocation shape.
// Every invocation must carry --repo: an unqualified gh call would resolve
// against the caller's environment (GH_HOST, cwd repo) instead of the clone's
// actual host. It never execs anything — CI ships a real gh binary and tests
// must not touch it.
func fakeRunner(t *testing.T, output []byte, err error) (Runner, *[][]string) {
	t.Helper()
	var calls [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			t.Fatalf("runner invoked with binary %q, want \"gh\"", name)
		}
		if !slices.Contains(args, "--repo") {
			t.Fatalf("gh invoked without --repo: %v", args)
		}
		calls = append(calls, args)
		return output, err
	}
	return runner, &calls
}

func TestListPRsByHeadArgv(t *testing.T) {
	runner, calls := fakeRunner(t, []byte(`[]`), nil)
	client := &Client{Runner: runner}
	if _, err := client.ListPRsByHead(context.Background(), "github.example.com/acme/widgets", "feature/x"); err != nil {
		t.Fatalf("ListPRsByHead: %v", err)
	}
	want := []string{
		"pr", "list",
		"--repo", "github.example.com/acme/widgets",
		"--head", "feature/x",
		"--state", "all",
		"--json", "number,state,mergedAt,mergeCommit,baseRefName",
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Fatalf("argv = %v, want %v", *calls, want)
	}
}

func TestListPRsByHeadParsesMergedAndOpen(t *testing.T) {
	payload := []byte(`[
		{"number": 42, "state": "MERGED", "mergedAt": "2026-07-01T12:00:00Z",
		 "mergeCommit": {"oid": "abc123def456"}, "baseRefName": "main"},
		{"number": 43, "state": "OPEN", "mergedAt": null,
		 "mergeCommit": null, "baseRefName": "develop"}
	]`)
	runner, _ := fakeRunner(t, payload, nil)
	client := &Client{Runner: runner}
	prs, err := client.ListPRsByHead(context.Background(), "github.com/acme/widgets", "feature/x")
	if err != nil {
		t.Fatalf("ListPRsByHead: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs, want 2", len(prs))
	}
	merged := prs[0]
	if merged.Number != 42 || merged.State != "MERGED" || merged.MergedAt != "2026-07-01T12:00:00Z" {
		t.Fatalf("merged PR = %+v", merged)
	}
	if merged.MergeCommit.OID != "abc123def456" {
		t.Fatalf("merged PR oid = %q, want abc123def456", merged.MergeCommit.OID)
	}
	if merged.BaseRefName != "main" {
		t.Fatalf("merged PR base = %q, want main", merged.BaseRefName)
	}
	open := prs[1]
	if open.Number != 43 || open.State != "OPEN" || open.MergedAt != "" || open.MergeCommit.OID != "" {
		t.Fatalf("open PR = %+v", open)
	}
}

func TestListPRsByHeadEmpty(t *testing.T) {
	runner, _ := fakeRunner(t, []byte(`[]`), nil)
	client := &Client{Runner: runner}
	prs, err := client.ListPRsByHead(context.Background(), "github.com/acme/widgets", "feature/x")
	if err != nil {
		t.Fatalf("ListPRsByHead: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("got %d PRs, want 0", len(prs))
	}
}

func TestViewPRArgvAndParse(t *testing.T) {
	// Squash-merge shape: single-parent merge commit, mergedAt set.
	payload := []byte(`{"state": "MERGED", "mergedAt": "2026-06-15T08:30:00Z",
		"mergeCommit": {"oid": "feedface0123"}, "baseRefName": "release/1.2"}`)
	runner, calls := fakeRunner(t, payload, nil)
	client := &Client{Runner: runner}
	pr, err := client.ViewPR(context.Background(), "github.com/acme/widgets", 7)
	if err != nil {
		t.Fatalf("ViewPR: %v", err)
	}
	want := []string{
		"pr", "view", "7",
		"--repo", "github.com/acme/widgets",
		"--json", "state,mergedAt,mergeCommit,baseRefName",
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Fatalf("argv = %v, want %v", *calls, want)
	}
	if pr.Number != 7 {
		t.Fatalf("Number = %d, want 7 (backfilled from the request)", pr.Number)
	}
	if pr.State != "MERGED" || pr.MergedAt != "2026-06-15T08:30:00Z" ||
		pr.MergeCommit.OID != "feedface0123" || pr.BaseRefName != "release/1.2" {
		t.Fatalf("PR = %+v", pr)
	}
}

func TestRunnerErrorPropagates(t *testing.T) {
	runnerErr := fmt.Errorf("gh pr list: HTTP 404: Not Found")
	runner, _ := fakeRunner(t, nil, runnerErr)
	client := &Client{Runner: runner}
	if _, err := client.ListPRsByHead(context.Background(), "github.com/acme/widgets", "feature/x"); !errors.Is(err, runnerErr) {
		t.Fatalf("ListPRsByHead err = %v, want %v", err, runnerErr)
	}
	if _, err := client.ViewPR(context.Background(), "github.com/acme/widgets", 7); !errors.Is(err, runnerErr) {
		t.Fatalf("ViewPR err = %v, want %v", err, runnerErr)
	}
}

func TestUnavailableSentinel(t *testing.T) {
	runner, _ := fakeRunner(t, nil, ErrGHUnavailable)
	client := &Client{Runner: runner}
	_, err := client.ListPRsByHead(context.Background(), "github.com/acme/widgets", "feature/x")
	if !errors.Is(err, ErrGHUnavailable) {
		t.Fatalf("err = %v, want ErrGHUnavailable", err)
	}
}

func TestInvalidJSON(t *testing.T) {
	runner, _ := fakeRunner(t, []byte(`not json`), nil)
	client := &Client{Runner: runner}
	if _, err := client.ListPRsByHead(context.Background(), "github.com/acme/widgets", "feature/x"); err == nil {
		t.Fatal("ListPRsByHead accepted invalid JSON")
	}
	if _, err := client.ViewPR(context.Background(), "github.com/acme/widgets", 7); err == nil {
		t.Fatal("ViewPR accepted invalid JSON")
	}
}

func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		host     string
		owner    string
		repo     string
		ok       bool
	}{
		{"https", "https://github.com/acme/widgets", "github.com", "acme", "widgets", true},
		{"https .git", "https://github.com/acme/widgets.git", "github.com", "acme", "widgets", true},
		{"https trailing slash", "https://github.com/acme/widgets/", "github.com", "acme", "widgets", true},
		{"scp-like ssh", "git@github.com:acme/widgets.git", "github.com", "acme", "widgets", true},
		{"scp-like ssh no .git", "git@github.com:acme/widgets", "github.com", "acme", "widgets", true},
		{"ssh url", "ssh://git@github.com/acme/widgets", "github.com", "acme", "widgets", true},
		{"ssh url .git", "ssh://git@github.com/acme/widgets.git", "github.com", "acme", "widgets", true},
		{"enterprise https", "https://github.example.com/acme/widgets.git", "github.example.com", "acme", "widgets", true},
		{"enterprise scp-like", "git@github.example.com:acme/widgets.git", "github.example.com", "acme", "widgets", true},
		{"enterprise ghe.com", "https://acme.ghe.com/acme/widgets", "acme.ghe.com", "acme", "widgets", true},
		{"non-GitHub", "https://gitlab.com/acme/widgets.git", "", "", "", false},
		{"non-GitHub scp-like", "git@bitbucket.org:acme/widgets.git", "", "", "", false},
		{"extra path segment", "https://github.com/acme/widgets/tree/main", "", "", "", false},
		{"missing repo", "https://github.com/acme", "", "", "", false},
		{"garbage", "not a url at all", "", "", "", false},
		{"empty", "", "", "", "", false},
		{"local path", "/srv/git/widgets.git", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, owner, repo, ok := ParseOwnerRepo(tt.cloneURL)
			if host != tt.host || owner != tt.owner || repo != tt.repo || ok != tt.ok {
				t.Fatalf("ParseOwnerRepo(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					tt.cloneURL, host, owner, repo, ok, tt.host, tt.owner, tt.repo, tt.ok)
			}
		})
	}
}
