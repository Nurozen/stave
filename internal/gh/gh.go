// Package gh wraps the GitHub CLI behind an injected runner so callers can
// query pull-request state without executing a real gh binary in tests.
package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrGHUnavailable reports that the gh binary is not installed (or not on
// PATH). Callers degrade to commit-ancestry heuristics with a single stderr
// note instead of failing.
var ErrGHUnavailable = errors.New("gh CLI not found in PATH")

// Runner executes a command and returns its stdout. The default
// implementation execs the real binary; tests inject fakes.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the production Runner: it soft-detects the binary via
// LookPath (missing gh maps to ErrGHUnavailable so callers can degrade) and
// folds trimmed stderr into the returned error, matching the gh degrade
// semantics used elsewhere in the CLI.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, ErrGHUnavailable
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", name, argvSummary(args), detail)
	}
	return output, nil
}

func argvSummary(args []string) string {
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}

// PR mirrors the JSON shapes gh emits for the fields this package requests.
// mergeCommit arrives as an object ({"oid": "..."}) or null; mergedAt is an
// RFC 3339 timestamp or null. Nulls simply leave zero values, so parsing
// stays tolerant of unmerged PRs.
type PR struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	MergedAt string `json:"mergedAt"`
	// MergeCommit is decoded but currently unconsumed — reserved for the
	// cached-identity `gh pr view` follow-up (the requested field set, and
	// therefore the frozen argv, must not change).
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	BaseRefName string `json:"baseRefName"`
}

// Client issues gh commands through Runner. A nil Runner uses ExecRunner.
type Client struct {
	Runner Runner
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	runner := c.Runner
	if runner == nil {
		runner = ExecRunner
	}
	return runner(ctx, "gh", args...)
}

// ParseOwnerRepo extracts the host, owner, and repo from a GitHub clone URL
// in https (https://github.com/o/r[.git]), scp-like ssh
// (git@github.com:o/r[.git]), or ssh URL (ssh://git@github.com/o/r[.git])
// form. The host is preserved so GitHub Enterprise clones address their own
// host rather than leaking to github.com via GH_HOST; pass
// host+"/"+owner+"/"+repo as the gh --repo selector. Non-GitHub or
// unparseable URLs return ok=false.
func ParseOwnerRepo(cloneURL string) (host, owner, repo string, ok bool) {
	host, owner, repo, ok = parseOwnerRepo(cloneURL)
	if !ok || !isGitHubHost(host) {
		return "", "", "", false
	}
	return host, owner, repo, true
}

// ParseAnyOwnerRepo is the lenient form of ParseOwnerRepo: it accepts the
// same https, scp-like ssh, and ssh URL shapes but applies no GitHub host
// gate. The host comes back verbatim, which for scp-like clones may be an SSH
// config alias rather than a DNS name (git@myalias:owner/repo.git yields host
// "myalias"). Callers must not assume the host is GitHub — or even a real
// hostname — and should use ParseOwnerRepo when they need that guarantee.
// Owner and repo case is preserved. Local paths, bare "owner/repo" strings,
// and other unparseable forms return ok=false.
func ParseAnyOwnerRepo(cloneURL string) (host, owner, repo string, ok bool) {
	return parseOwnerRepo(cloneURL)
}

// parseOwnerRepo splits cloneURL into host/owner/repo without judging the
// host. It is the shared body of ParseOwnerRepo and ParseAnyOwnerRepo.
func parseOwnerRepo(cloneURL string) (host, owner, repo string, ok bool) {
	s := strings.TrimSpace(cloneURL)
	var path string
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		rest := strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		host, path, _ = strings.Cut(rest, "/")
		// Drop a userinfo prefix (user@ or user:pass@) so credentialed
		// clone URLs like https://git@github.com/o/r resolve to the bare
		// host. host is already the pre-slash portion, so the last "@" is
		// the userinfo delimiter.
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	case strings.HasPrefix(s, "ssh://"):
		rest := strings.TrimPrefix(s, "ssh://")
		host, path, _ = strings.Cut(rest, "/")
		// Same userinfo rule as https: host is the authority (everything
		// before the first "/"), so the last "@" in it delimits userinfo
		// whether that is "git@" or "user:pass@". A port lives after the
		// host ("github.com:2222"), never inside the userinfo, so it stays
		// glued to the host.
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	case looksSCPLike(s):
		userHost, scpPath, _ := strings.Cut(s, ":")
		if _, h, found := strings.Cut(userHost, "@"); found {
			host = h
		} else {
			host = userHost
		}
		path = scpPath
	default:
		return "", "", "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	owner, repo, found := strings.Cut(path, "/")
	if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", "", false
	}
	return host, owner, repo, true
}

// looksSCPLike reports whether s has the scp-ish user@host:path form (a
// colon before any slash) rather than a URL scheme.
func looksSCPLike(s string) bool {
	colon := strings.Index(s, ":")
	if colon <= 0 {
		return false
	}
	slash := strings.Index(s, "/")
	return slash == -1 || colon < slash
}

// isGitHubHost distinguishes GitHub hosts (github.com, GitHub Enterprise
// installs conventionally named github.<company>.<tld> or *.ghe.com) from
// other forges. Heuristic by necessity: an enterprise host is any domain, so
// this errs toward recognizing hosts with a "github" label.
func isGitHubHost(host string) bool {
	host = strings.ToLower(host)
	if host == "github.com" || strings.HasSuffix(host, ".ghe.com") {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "github" {
			return true
		}
	}
	return false
}

// ListPRsByHead lists pull requests whose head is headBranch in the
// host-qualified repo (<host>/<owner>/<repo>). It passes --state all
// deliberately: gh defaults to open PRs only, and the very case this lookup
// exists to detect — a PR that has already merged — would be invisible
// without it.
func (c *Client) ListPRsByHead(ctx context.Context, hostOwnerRepo, headBranch string) ([]PR, error) {
	output, err := c.run(ctx, "pr", "list",
		"--repo", hostOwnerRepo,
		"--head", headBranch,
		"--state", "all",
		"--json", "number,state,mergedAt,mergeCommit,baseRefName")
	if err != nil {
		return nil, err
	}
	var prs []PR
	if err := json.Unmarshal(output, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	return prs, nil
}

// ViewPRJSON runs gh pr view for selector (a number, branch, or PR URL)
// requesting the comma-separated --json fields, and returns gh's raw JSON so
// callers with field sets beyond PR can decode into their own shapes.
func (c *Client) ViewPRJSON(ctx context.Context, selector, fields string) ([]byte, error) {
	return c.run(ctx, "pr", "view", selector, "--json", fields)
}

// ViewPR fetches a single pull request by number from the host-qualified
// repo (<host>/<owner>/<repo>).
func (c *Client) ViewPR(ctx context.Context, hostOwnerRepo string, number int) (PR, error) {
	var pr PR
	output, err := c.run(ctx, "pr", "view", strconv.Itoa(number),
		"--repo", hostOwnerRepo,
		"--json", "state,mergedAt,mergeCommit,baseRefName")
	if err != nil {
		return pr, err
	}
	if err := json.Unmarshal(output, &pr); err != nil {
		return pr, fmt.Errorf("parse gh pr view output: %w", err)
	}
	pr.Number = number
	return pr, nil
}
