package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Client struct {
	bin    string
	runner Runner
	DryRun bool
	Logf   func(format string, args ...any)
}

type Runner interface {
	Run(context.Context, string, []string, RunOptions) (Result, error)
}

type RunOptions struct {
	Dir      string
	ReadOnly bool
}

type Result struct {
	Stdout string
	Stderr string
}

type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *GitError) Error() string {
	msg := "git " + strings.Join(e.Args, " ") + " failed"
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" with exit code %d", e.ExitCode)
	}
	if strings.TrimSpace(e.Stderr) != "" {
		msg += ": " + strings.TrimSpace(e.Stderr)
	}
	return msg
}

func (e *GitError) Unwrap() error {
	return e.Err
}

type Option func(*Client)

func New(opts ...Option) *Client {
	c := &Client{bin: "git", runner: execRunner{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func WithRunner(runner Runner) Option {
	return func(c *Client) {
		if runner != nil {
			c.runner = runner
		}
	}
}

func WithDryRun(dryRun bool, logf func(format string, args ...any)) Option {
	return func(c *Client) {
		c.DryRun = dryRun
		c.Logf = logf
	}
}

func (c *Client) CloneBare(ctx context.Context, url, dest string) error {
	_, err := c.run(ctx, "clone", "--bare", url, dest)
	return err
}

func (c *Client) ConfigureBareRemoteTracking(ctx context.Context, bareRepo string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	return err
}

func (c *Client) FetchAllPrune(ctx context.Context, bareRepo string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "fetch", "--all", "--prune")
	return err
}

// FetchRefspec fetches an explicit refspec from origin, e.g. pull-request
// head refs (+refs/pull/N/head:refs/remotes/origin/pr/N) that the mirror's
// standard refs/heads/* tracking never picks up.
func (c *Client) FetchRefspec(ctx context.Context, bareRepo, refspec string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "fetch", "origin", refspec)
	return err
}

func (c *Client) WorktreeAddBranch(ctx context.Context, bareRepo, path, branch, startPoint string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "worktree", "add", "--no-track", "-b", branch, path, startPoint)
	return err
}

func (c *Client) WorktreeAddExisting(ctx context.Context, bareRepo, path, branch string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "worktree", "add", path, branch)
	return err
}

func (c *Client) WorktreeAddDetached(ctx context.Context, bareRepo, path, ref string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "worktree", "add", "--detach", path, ref)
	return err
}

func (c *Client) WorktreeRemove(ctx context.Context, bareRepo, path string, force bool) error {
	args := []string{"--git-dir", bareRepo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := c.run(ctx, args...)
	return err
}

func (c *Client) WorktreePrune(ctx context.Context, bareRepo string) error {
	_, err := c.run(ctx, "--git-dir", bareRepo, "worktree", "prune")
	return err
}

func (c *Client) CheckoutDetached(ctx context.Context, worktreePath, ref string) error {
	_, err := c.runIn(ctx, worktreePath, "checkout", "--detach", ref)
	return err
}

func (c *Client) BranchExists(ctx context.Context, bareRepo, branch string) (bool, error) {
	_, err := c.probe(ctx, "--git-dir", bareRepo, "show-ref", "--verify", "--quiet", "refs/heads/"+strings.TrimPrefix(branch, "refs/heads/"))
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

// IsAncestor reports whether ancestor is an ancestor of descendant in the
// bare repo. A missing ref (git exit 128) is returned as an error, not false.
func (c *Client) IsAncestor(ctx context.Context, bareRepo, ancestor, descendant string) (bool, error) {
	_, err := c.probe(ctx, "--git-dir", bareRepo, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

// RefExists reports whether fullRef exists in the bare repo. It takes a full
// ref (refs/heads/..., refs/remotes/origin/...) verbatim; unlike BranchExists
// it does not force the refs/heads/ namespace.
func (c *Client) RefExists(ctx context.Context, bareRepo, fullRef string) (bool, error) {
	_, err := c.probe(ctx, "--git-dir", bareRepo, "show-ref", "--verify", "--quiet", fullRef)
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

func (c *Client) RemoteDefaultBranch(ctx context.Context, bareRepo string) (string, error) {
	out, err := c.probeOutput(ctx, "--git-dir", bareRepo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
	}
	out, err = c.probeOutput(ctx, "--git-dir", bareRepo, "remote", "show", "origin")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:")), nil
		}
	}
	return "", fmt.Errorf("could not determine remote default branch")
}

func (c *Client) IsDirty(ctx context.Context, worktreePath string) (bool, string, error) {
	out, err := c.probeOutputIn(ctx, worktreePath, "status", "--porcelain=v1")
	if err != nil {
		return false, "", err
	}
	return strings.TrimSpace(out) != "", out, nil
}

func (c *Client) AheadBehind(ctx context.Context, worktreePath, baseRef string) (ahead, behind int, err error) {
	out, err := c.probeOutputIn(ctx, worktreePath, "rev-list", "--left-right", "--count", baseRef+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out)
	}
	behind, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	ahead, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

func (c *Client) Output(ctx context.Context, args ...string) (string, error) {
	result, err := c.run(ctx, args...)
	return result.Stdout, err
}

func (c *Client) OutputIn(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := c.runIn(ctx, dir, args...)
	return result.Stdout, err
}

func (c *Client) runIn(ctx context.Context, dir string, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{Dir: dir})
}

func (c *Client) run(ctx context.Context, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{})
}

// probe and probeIn execute read-only git queries even when DryRun is set.
// A dry-run must suppress mutations, not the observations used to produce an
// accurate plan (dirty guards, drift, ref existence, and ancestry).
func (c *Client) probe(ctx context.Context, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{ReadOnly: true})
}

func (c *Client) probeIn(ctx context.Context, dir string, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{Dir: dir, ReadOnly: true})
}

func (c *Client) probeOutput(ctx context.Context, args ...string) (string, error) {
	result, err := c.probe(ctx, args...)
	return result.Stdout, err
}

func (c *Client) probeOutputIn(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := c.probeIn(ctx, dir, args...)
	return result.Stdout, err
}

func (c *Client) runWithOptions(ctx context.Context, args []string, opts RunOptions) (Result, error) {
	if c == nil {
		c = New()
	}
	if c.bin == "" {
		c.bin = "git"
	}
	if c.runner == nil {
		c.runner = execRunner{}
	}
	if c.DryRun && !opts.ReadOnly {
		if c.Logf != nil {
			prefix := ""
			if opts.Dir != "" {
				prefix = "(cd " + opts.Dir + " && "
			}
			msg := "git " + strings.Join(args, " ")
			if prefix != "" {
				msg += ")"
			}
			c.Logf("dry-run: %s%s", prefix, msg)
		}
		return Result{}, nil
	}
	return c.runner.Run(ctx, c.bin, args, opts)
}

func IsExitCode(err error, code int) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.ExitCode == code
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, bin string, args []string, opts RunOptions) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Dir
	// Fail fast instead of hanging on a credential prompt, and pin the
	// locale so output parsing is not localization-dependent.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return result, &GitError{Args: append([]string(nil), args...), ExitCode: exitCode, Stderr: result.Stderr, Err: err}
}
