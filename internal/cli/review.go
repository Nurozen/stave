package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// prRef is a parsed pull-request coordinate.
type prRef struct {
	Owner  string // GitHub owner; empty when referenced via a registered repo name
	Repo   string // GitHub repo name or registered repo name
	Number int
}

var (
	prURLPattern   = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	prShortPattern = regexp.MustCompile(`^([^/#\s]+)/([^/#\s]+)#(\d+)$`)
	prLocalPattern = regexp.MustCompile(`^([^/#\s]+)#(\d+)$`)
)

// parsePRRef accepts a GitHub PR URL, owner/repo#N, or <registered-repo>#N.
func parsePRRef(value string) (prRef, error) {
	value = strings.TrimSpace(value)
	if m := prURLPattern.FindStringSubmatch(value); m != nil {
		return prRef{Owner: m[1], Repo: strings.TrimSuffix(m[2], ".git"), Number: atoiSafe(m[3])}, nil
	}
	if m := prShortPattern.FindStringSubmatch(value); m != nil {
		return prRef{Owner: m[1], Repo: m[2], Number: atoiSafe(m[3])}, nil
	}
	if m := prLocalPattern.FindStringSubmatch(value); m != nil {
		return prRef{Repo: m[1], Number: atoiSafe(m[2])}, nil
	}
	return prRef{}, fmt.Errorf("could not parse %q as a pull request; use a GitHub PR URL, owner/repo#123, or <registered-repo>#123", value)
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func (r prRef) cloneURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", r.Owner, r.Repo)
}

func (r prRef) webURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.Owner, r.Repo, r.Number)
}

// prMetadata is the subset of gh pr view --json output the review spec uses.
type prMetadata struct {
	Title        string `json:"title"`
	Body         string `json:"body"`
	State        string `json:"state"`
	IsDraft      bool   `json:"isDraft"`
	BaseRefName  string `json:"baseRefName"`
	HeadRefName  string `json:"headRefName"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	URL          string `json:"url"`
	Author       struct {
		Login string `json:"login"`
	} `json:"author"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

func (a *app) reviewCommand() *cobra.Command {
	var summonName string
	var summonPrompt string
	var repoOverride string
	var references []string
	var noSummon bool
	cmd := &cobra.Command{
		Use:   "review <pr> [space-id]",
		Short: "Create a review space for a pull request in one step",
		Long: `Create a workspace ready for reviewing a pull request: registers the
repository when needed, fetches the PR head into the bare mirror, checks it
out as an editable worktree (drift is reported against the PR's base branch),
and records PR metadata under spec/ for agents and skills to consume.

The <pr> argument accepts a GitHub PR URL, owner/repo#123, or
<registered-repo>#123.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parsePRRef(args[0])
			if err != nil {
				return err
			}
			if repoOverride != "" {
				ref.Repo = repoOverride
				ref.Owner = ""
			}
			spaceID := ""
			if len(args) == 2 {
				spaceID = args[1]
			}
			cfg, cfgPath, err := a.loadConfig()
			if err != nil {
				return err
			}
			if err := cfg.EnsureRootDirs(); err != nil {
				return err
			}
			refSpecs, err := parseRepoSpecs(references)
			if err != nil {
				return err
			}
			result, err := a.setUpReviewSpace(cmd, cfg, cfgPath, ref, spaceID, refSpecs)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\nreview space %s is ready\n", result.SpaceID)
			fmt.Fprintf(out, "  path:  %s\n", result.WorktreePath)
			fmt.Fprintf(out, "  head:  %s (PR #%d)\n", result.HeadRef, ref.Number)
			fmt.Fprintf(out, "  base:  %s\n", result.BaseRef)
			fmt.Fprintf(out, "  spec:  %s\n", result.SpecFile)
			fmt.Fprintf(out, "  diff:  git -C %s diff %s...HEAD\n", result.WorktreePath, result.BaseRef)
			if summonName == "" || noSummon {
				fmt.Fprintf(out, "  next:  stave summon %s --with claude\n", result.SpaceID)
				return nil
			}
			return a.runSummon(cmd, *cfg, result.SpaceID, summonName, summonPrompt, false)
		},
	}
	cmd.Flags().StringVar(&summonName, "summon", "", "launch a summoner in the review space (codex, claude, or cursor)")
	cmd.Flags().StringVar(&summonPrompt, "prompt", "", "launch prompt for --summon (e.g. a skill invocation like \"/pr-teach\")")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "registered repo to add as read-only context, optionally repo:ref (repeatable)")
	cmd.Flags().StringVar(&repoOverride, "repo", "", "registered repo name to use instead of resolving from the PR URL")
	cmd.Flags().BoolVar(&noSummon, "no-summon", false, "never launch a summoner, even if --summon is set")
	return cmd
}

type reviewResult struct {
	SpaceID      string
	WorktreePath string
	HeadRef      string
	BaseRef      string
	SpecFile     string
}

func (a *app) setUpReviewSpace(cmd *cobra.Command, cfg *config.Config, cfgPath string, ref prRef, spaceID string, references []space.RepoSpec) (reviewResult, error) {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	client := git.New()

	repoName, repoCfg, err := resolveReviewRepo(ctx, cmd, cfg, cfgPath, client, ref)
	if err != nil {
		return reviewResult{}, err
	}

	// Fetch base branches and the PR head. GitHub exposes PR heads (fork or
	// not) read-only at refs/pull/N/head, which the standard refs/heads/*
	// mirror tracking never fetches.
	if err := client.FetchAllPrune(ctx, repoCfg.BareRepoPath); err != nil {
		return reviewResult{}, err
	}
	prHeadRef := fmt.Sprintf("pr/%d", ref.Number)
	refspec := fmt.Sprintf("+refs/pull/%d/head:refs/remotes/origin/%s", ref.Number, prHeadRef)
	if err := client.FetchRefspec(ctx, repoCfg.BareRepoPath, refspec); err != nil {
		return reviewResult{}, fmt.Errorf("fetch PR #%d head: %w", ref.Number, err)
	}

	meta, metaErr := fetchPRMetadata(ctx, ref)
	if metaErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: PR metadata unavailable (%v); the review spec will be minimal\n", metaErr)
	}
	baseBranch := firstNonEmpty(meta.BaseRefName, repoCfg.DefaultBranch, cfg.DefaultBase)

	if spaceID == "" {
		spaceID = fmt.Sprintf("review-%s-%d", repoName, ref.Number)
	}
	if err := config.ValidateName("space id", spaceID); err != nil {
		return reviewResult{}, err
	}
	spacePath := filepath.Join(cfg.AgentWorkDir, spaceID)
	if _, err := os.Stat(spacePath); err == nil {
		return reviewResult{}, fmt.Errorf("space %q already exists; pass a different space id or run stave space destroy %s first", spaceID, spaceID)
	}

	// Route the PR context through InitSpace's spec plumbing so the manifest
	// records SpecPath and AGENTS.md/summon prompts point agents at it.
	specSrc, err := stageReviewSpec(repoName, ref, meta, metaErr == nil, "origin/"+baseBranch)
	if err != nil {
		return reviewResult{}, err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specSrc)) }()

	svc := space.NewService(*cfg, client, out)
	if err := svc.InitSpace(ctx, space.InitOptions{ID: spaceID, Kind: "review", SpecPath: specSrc}); err != nil {
		return reviewResult{}, err
	}
	if err := svc.AddRepo(ctx, space.AddOptions{
		SpaceID:    spaceID,
		RepoName:   repoName,
		Mode:       space.ModeEdit,
		Base:       baseBranch,
		StartPoint: prHeadRef,
		NoFetch:    true, // fetched above, including the PR ref
	}); err != nil {
		return reviewResult{}, err
	}
	for _, spec := range references {
		if err := svc.AddRepo(ctx, space.AddOptions{SpaceID: spaceID, RepoName: spec.Name, Mode: space.ModeReference, Ref: spec.Ref}); err != nil {
			return reviewResult{}, err
		}
	}

	return reviewResult{
		SpaceID:      spaceID,
		WorktreePath: filepath.Join(spacePath, repoName),
		HeadRef:      "origin/" + prHeadRef,
		BaseRef:      "origin/" + baseBranch,
		SpecFile:     filepath.Join(spacePath, "spec", filepath.Base(specSrc)),
	}, nil
}

// resolveReviewRepo maps the PR coordinate onto a registered repo, matching
// by name first and clone URL second, and registers the repository
// automatically when the PR names one stave has not seen before.
func resolveReviewRepo(ctx context.Context, cmd *cobra.Command, cfg *config.Config, cfgPath string, client *git.Client, ref prRef) (string, config.Repository, error) {
	if repoCfg, ok := cfg.Repos[ref.Repo]; ok {
		return ref.Repo, repoCfg, nil
	}
	if ref.Owner == "" {
		return "", config.Repository{}, fmt.Errorf("repo %q is not registered; use owner/repo#%d or a PR URL so it can be registered automatically", ref.Repo, ref.Number)
	}
	cloneURL := ref.cloneURL()
	for name, repoCfg := range cfg.Repos {
		if sameCloneURL(repoCfg.URL, cloneURL) {
			return name, repoCfg, nil
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "registering %s from %s\n", ref.Repo, cloneURL)
	repoCfg, err := cfg.RegisterRepository(ref.Repo, cloneURL, "")
	if err != nil {
		return "", config.Repository{}, err
	}
	if _, err := os.Stat(repoCfg.BareRepoPath); err == nil {
		return "", config.Repository{}, fmt.Errorf("bare repo path already exists: %s", repoCfg.BareRepoPath)
	} else if !os.IsNotExist(err) {
		return "", config.Repository{}, err
	}
	if err := client.CloneBare(ctx, cloneURL, repoCfg.BareRepoPath); err != nil {
		return "", config.Repository{}, err
	}
	if err := client.ConfigureBareRemoteTracking(ctx, repoCfg.BareRepoPath); err != nil {
		return "", config.Repository{}, err
	}
	if err := client.FetchAllPrune(ctx, repoCfg.BareRepoPath); err != nil {
		return "", config.Repository{}, err
	}
	if branch, err := client.RemoteDefaultBranch(ctx, repoCfg.BareRepoPath); err == nil && branch != "" {
		repoCfg.DefaultBranch = branch
		cfg.Repos[ref.Repo] = repoCfg
	}
	if err := cfg.Save(cfgPath); err != nil {
		return "", config.Repository{}, err
	}
	return ref.Repo, repoCfg, nil
}

func sameCloneURL(a, b string) bool {
	normalize := func(u string) string {
		u = strings.TrimSuffix(strings.TrimSpace(u), ".git")
		u = strings.TrimPrefix(u, "https://")
		u = strings.TrimPrefix(u, "http://")
		u = strings.TrimPrefix(u, "git@")
		u = strings.ReplaceAll(u, ":", "/")
		return strings.ToLower(u)
	}
	return normalize(a) == normalize(b)
}

// fetchPRMetadata reads PR details via the gh CLI when it is available and
// authenticated; the review flow degrades gracefully without it.
func fetchPRMetadata(ctx context.Context, ref prRef) (prMetadata, error) {
	var meta prMetadata
	if ref.Owner == "" {
		return meta, fmt.Errorf("PR referenced by registered repo name; owner unknown")
	}
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return meta, fmt.Errorf("gh CLI not found in PATH")
	}
	cmd := exec.CommandContext(ctx, ghPath, "pr", "view", ref.webURL(),
		"--json", "title,body,state,isDraft,baseRefName,headRefName,additions,deletions,changedFiles,url,author,statusCheckRollup")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return meta, fmt.Errorf("gh pr view: %s", detail)
	}
	if err := json.Unmarshal(output, &meta); err != nil {
		return meta, fmt.Errorf("parse gh pr view output: %w", err)
	}
	return meta, nil
}

// stageReviewSpec renders the PR's context to a temp file that InitSpace
// copies into spec/, so a summoned agent (or a review skill such as
// pr-teach) starts with the full picture.
func stageReviewSpec(repoName string, ref prRef, meta prMetadata, haveMeta bool, baseRef string) (string, error) {
	stageDir, err := os.MkdirTemp("", "stave-review-spec-*")
	if err != nil {
		return "", err
	}
	specFile := filepath.Join(stageDir, fmt.Sprintf("pr-%d.md", ref.Number))

	var b strings.Builder
	fmt.Fprintf(&b, "# Review: PR #%d", ref.Number)
	if haveMeta && meta.Title != "" {
		fmt.Fprintf(&b, " — %s", meta.Title)
	}
	b.WriteString("\n\n")
	if ref.Owner != "" {
		fmt.Fprintf(&b, "- URL: %s\n", ref.webURL())
	}
	if haveMeta {
		fmt.Fprintf(&b, "- Author: %s\n", meta.Author.Login)
		state := strings.ToLower(meta.State)
		if meta.IsDraft {
			state += " (draft)"
		}
		fmt.Fprintf(&b, "- State: %s\n", state)
		fmt.Fprintf(&b, "- Branches: %s -> %s\n", meta.HeadRefName, meta.BaseRefName)
		fmt.Fprintf(&b, "- Size: %d files changed, +%d/-%d\n", meta.ChangedFiles, meta.Additions, meta.Deletions)
		if len(meta.StatusCheckRollup) > 0 {
			fmt.Fprintf(&b, "- Checks:\n")
			for _, check := range meta.StatusCheckRollup {
				outcome := firstNonEmpty(check.Conclusion, check.Status)
				fmt.Fprintf(&b, "  - %s: %s\n", check.Name, strings.ToLower(outcome))
			}
		}
	}
	fmt.Fprintf(&b, "\n## Review quickstart\n\n")
	fmt.Fprintf(&b, "The editable worktree `%s/` is checked out at the PR head; `%s` is the merge target.\n\n", repoName, baseRef)
	fmt.Fprintf(&b, "```sh\ncd %s\ngit diff %s...HEAD           # the full PR diff\ngit log %s..HEAD --oneline   # the PR's commits\n```\n", repoName, baseRef, baseRef)
	if haveMeta && strings.TrimSpace(meta.Body) != "" {
		fmt.Fprintf(&b, "\n## Author's description (claims, not facts — verify against the code)\n\n%s\n", strings.TrimSpace(meta.Body))
	}
	if !haveMeta {
		fmt.Fprintf(&b, "\n> PR title/description could not be fetched (gh CLI unavailable or unauthenticated); review from the diff and commits above.\n")
	}
	if err := os.WriteFile(specFile, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return specFile, nil
}
