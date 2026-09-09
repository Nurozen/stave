package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"github.com/Nurozen/stave/internal/gh"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/spf13/cobra"
)

// The pr-teach review skill ships inside the binary and is written into each
// review space as a project-level Claude Code skill, so summoned sessions
// can run the guided review loop on any machine without personal skills.
//
//go:embed assets/skills/pr-teach/SKILL.md
var prTeachSkill []byte

// reviewSpaceKind is the manifest Kind stamped on spaces created by
// `stave review`; --refresh refuses to touch any other kind.
const reviewSpaceKind = "review"

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
	var memories []string
	var noSummon bool
	var refresh bool
	cmd := &cobra.Command{
		Use:   "review <pr> [space-id]",
		Short: "Create (or --refresh) a review space for a pull request; --repo forces a registered repo",
		Long: `Create a workspace ready for reviewing a pull request: registers the
repository when needed, fetches the PR head into the bare mirror, checks it
out as an editable worktree (drift is reported against the PR's base branch),
and records PR metadata under spec/ for agents and skills to consume.

The <pr> argument accepts a GitHub PR URL, owner/repo#123, or
<registered-repo>#123.

--repo <name> forces a registered repo as the mirror to review in (for repos
registered under an SSH alias or another name). PR metadata identity: a PR
typed with an owner (URL or owner/repo#N) is always fetched as typed; for
<registered-repo>#N the owner/repo is derived from the resolved repo's
registered URL, and only when that URL is a plain github.com URL — otherwise
the spec is minimal and the note says how to fill it in.

When a repo is auto-registered from a PR URL, origin/HEAD and default-branch
discovery are best-effort: a failure there is reported as a note, not an
error.

--refresh re-fetches PR metadata into an existing review space and rewrites
only spec/pr-<N>.md — useful after fixing gh auth when the space was created
with a minimal spec. It never touches worktrees; if the PR head moved since
the space was created, it says so and suggests recreating the space.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			positionals, agentArgs, err := parsePassthroughArgs(cmd, args, 1, 2, func() bool { return summonName != "" })
			if err != nil {
				return err
			}
			ref, err := parsePRRef(positionals[0])
			if err != nil {
				return err
			}
			spaceID := ""
			if len(positionals) == 2 {
				spaceID = positionals[1]
				// An explicit id is validated before anything is resolved,
				// registered, cloned, or fetched; a derived id is validated
				// where it is derived.
				if err := config.ValidateName("space id", spaceID); err != nil {
					return err
				}
			}
			// Agent args (after "--") are only collected when --summon is
			// set (parsePassthroughArgs rejects them otherwise), so checking
			// summonName covers them too.
			if refresh && (len(references) > 0 || len(memories) > 0 || summonName != "" || summonPrompt != "") {
				return fmt.Errorf("--refresh cannot be combined with --reference/--memory/--summon/--prompt or agent arguments; it only re-fetches PR metadata into an existing review space")
			}
			cfg, cfgPath, err := a.loadConfig()
			if err != nil {
				return err
			}
			if refresh {
				return a.refreshReviewSpace(cmd, cfg, cfgPath, positionals[0], ref, repoOverride, spaceID)
			}
			if err := cfg.EnsureRootDirs(); err != nil {
				return err
			}
			refSpecs, err := parseRepoSpecs(references)
			if err != nil {
				return err
			}
			result, err := a.setUpReviewSpace(cmd, cfg, cfgPath, positionals[0], ref, repoOverride, spaceID, refSpecs, memories)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\nreview space %s is ready\n", result.SpaceID)
			fmt.Fprintf(out, "  path:  %s\n", result.WorktreePath)
			fmt.Fprintf(out, "  head:  %s (PR #%d)\n", result.HeadRef, ref.Number)
			fmt.Fprintf(out, "  base:  %s\n", result.BaseRef)
			fmt.Fprintf(out, "  spec:  %s\n", result.SpecFile)
			// The base ref is a PR-supplied branch name; git accepts names
			// like "main;touch${IFS}/tmp/x", so it is quoted like any other
			// pasteable operand.
			fmt.Fprintf(out, "  diff:  git -C %s diff %s...HEAD\n", shellQuote(result.WorktreePath), shellQuote(result.BaseRef))
			if summonName == "" || noSummon {
				fmt.Fprintf(out, "  next:  stave summon %s --with claude   # launches the /%s review loop\n", result.SpaceID, summon.ReviewSkillName)
				return a.requestShellChdir(filepath.Join(cfg.AgentWorkDir, result.SpaceID))
			}
			if err := a.requestShellChdir(filepath.Join(cfg.AgentWorkDir, result.SpaceID)); err != nil {
				return err
			}
			return a.summonNotice(cmd, *cfg, result.SpaceID, summonName, summonPrompt, agentArgs)
		},
	}
	cmd.Flags().StringVar(&summonName, "summon", "", "launch a summoner in the review space (codex, claude, or cursor)")
	cmd.Flags().StringVar(&summonPrompt, "prompt", "", "launch prompt for --summon (e.g. a skill invocation like \"/pr-teach\")")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "registered repo to add as read-only context, optionally repo:ref (repeatable)")
	cmd.Flags().StringArrayVar(&memories, "memory", nil, "attach memory: [provider:]<spec>; '.' = fresh task store (repeatable)")
	cmd.Flags().StringVar(&repoOverride, "repo", "", "registered repo name to use instead of resolving from the PR URL")
	cmd.Flags().BoolVar(&noSummon, "no-summon", false, "never launch a summoner, even if --summon is set")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-fetch PR metadata into an existing review space (rewrites spec/pr-<N>.md only)")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

type reviewResult struct {
	SpaceID      string
	WorktreePath string
	HeadRef      string
	BaseRef      string
	SpecFile     string
}

// reviewInputs is everything the create and refresh paths share once the
// repo is resolved and the mirror carries the PR head: the metadata identity,
// the metadata itself (or the error explaining its absence), and the base
// branch the review is measured against.
type reviewInputs struct {
	RepoName   string
	RepoCfg    config.Repository
	MetaRef    prRef
	Meta       prMetadata
	MetaErr    error
	BaseBranch string
}

// reviewFetchOptions parameterizes fetchReviewInputs for its two callers.
type reviewFetchOptions struct {
	// allowAutoRegister lets resolution register an unknown github.com repo;
	// --refresh passes false (see resolveReviewRepo).
	allowAutoRegister bool
	// verb names the phase in fetch-failure wording ("review" or "refresh").
	verb string
	// beforeFetch runs after the repo is resolved and before any network
	// access, so callers finish their local validations (space id, manifest)
	// against the resolved repo without paying for a fetch they may reject.
	beforeFetch func(repoName string, repoCfg config.Repository) error
}

// fetchReviewInputs is the sequence both setUpReviewSpace and
// refreshReviewSpace run: resolve the repo, sync the mirror, fetch the PR
// head, derive the metadata identity, ask gh for metadata, and settle the base
// branch. A metadata failure is returned in MetaErr rather than as an error;
// the caller decides whether to degrade or abort. GitHub exposes PR heads
// (fork or not) read-only at refs/pull/N/head, which the standard
// refs/heads/* mirror tracking never fetches, hence the explicit refspec.
func (a *app) fetchReviewInputs(ctx context.Context, cmd *cobra.Command, cfg *config.Config, cfgPath string, client *git.Client, ref prRef, repoOverride string, opts reviewFetchOptions) (reviewInputs, error) {
	repoName, repoCfg, err := resolveReviewRepo(ctx, cmd, cfg, cfgPath, client, ref, repoOverride, opts.allowAutoRegister)
	if err != nil {
		return reviewInputs{}, err
	}
	if opts.beforeFetch != nil {
		if err := opts.beforeFetch(repoName, repoCfg); err != nil {
			return reviewInputs{}, err
		}
	}
	if err := client.FetchAllPrune(ctx, repoCfg.BareRepoPath); err != nil {
		return reviewInputs{}, fmt.Errorf("fetch %q before %s: %w", repoName, opts.verb, err)
	}
	if err := client.FetchRefspec(ctx, repoCfg.BareRepoPath, reviewPRRefspec(ref.Number)); err != nil {
		return reviewInputs{}, fmt.Errorf("fetch PR #%d head: %w", ref.Number, err)
	}
	metaRef := reviewMetaRef(ref, repoCfg)
	meta, metaErr := fetchPRMetadata(ctx, &gh.Client{Runner: a.ghRunner}, metaRef)
	return reviewInputs{
		RepoName:   repoName,
		RepoCfg:    repoCfg,
		MetaRef:    metaRef,
		Meta:       meta,
		MetaErr:    metaErr,
		BaseBranch: firstNonEmpty(meta.BaseRefName, repoCfg.DefaultBranch, cfg.DefaultBase),
	}, nil
}

// prArg is the PR exactly as the user typed it; it is echoed back in the
// --refresh remedy so the user can re-run the same coordinate.
func (a *app) setUpReviewSpace(cmd *cobra.Command, cfg *config.Config, cfgPath string, prArg string, ref prRef, repoOverride string, spaceID string, references []space.RepoSpec, memories []string) (reviewResult, error) {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	client := git.New()

	var defaultSpaceID, spacePath string
	in, err := a.fetchReviewInputs(ctx, cmd, cfg, cfgPath, client, ref, repoOverride, reviewFetchOptions{
		allowAutoRegister: true,
		verb:              "review",
		beforeFetch: func(repoName string, _ config.Repository) error {
			defaultSpaceID = defaultReviewSpaceID(repoName, ref.Number)
			if spaceID == "" {
				spaceID = defaultSpaceID
			}
			if err := config.ValidateName("space id", spaceID); err != nil {
				return err
			}
			spacePath = filepath.Join(cfg.AgentWorkDir, spaceID)
			if _, err := os.Stat(spacePath); err == nil {
				return fmt.Errorf("space %q already exists; pass a different space id or run stave space destroy %s first; pass --refresh to re-fetch PR metadata into it", spaceID, spaceID)
			}
			return nil
		},
	})
	if err != nil {
		return reviewResult{}, err
	}
	repoName := in.RepoName
	prHeadRef := reviewPRHeadRef(ref.Number)

	remedy := reviewMetaRemedy(prArg, ref, in.MetaRef, repoName, in.RepoCfg, spaceID, defaultSpaceID, repoOverride)
	specFile := reviewSpecPath(spacePath, ref.Number)
	if in.MetaErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: PR metadata unavailable (%v). The review spec at %s is minimal. %s\n", in.MetaErr, specFile, remedy)
	}
	baseBranch := in.BaseBranch

	// Route the PR context through InitSpace's spec plumbing so the manifest
	// records SpecPath and AGENTS.md/summon prompts point agents at it.
	specSrc, err := stageReviewSpec(repoName, in.MetaRef, in.Meta, in.MetaErr == nil, "origin/"+baseBranch, remedy, nil)
	if err != nil {
		return reviewResult{}, err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specSrc)) }()

	svc := space.NewService(*cfg, client, out)
	if err := svc.InitSpace(ctx, space.InitOptions{ID: spaceID, Kind: reviewSpaceKind, SpecPath: specSrc}); err != nil {
		return reviewResult{}, err
	}
	if err := svc.AddRepo(ctx, space.AddOptions{
		SpaceID:      spaceID,
		RepoName:     repoName,
		Mode:         space.ModeEdit,
		Base:         baseBranch,
		StartPoint:   prHeadRef,
		NoFetch:      true, // fetched above, including the PR ref
		CaptureOnAdd: true, // OQ-A: review learns prHead -> sibling references
	}); err != nil {
		return reviewResult{}, err
	}
	for _, spec := range references {
		if err := svc.AddRepo(ctx, space.AddOptions{SpaceID: spaceID, RepoName: spec.Name, Mode: space.ModeReference, Ref: spec.Ref, CaptureOnAdd: true}); err != nil {
			return reviewResult{}, err
		}
	}

	// Explicit --memory specs fail hard; ambient memory.default degrades to a
	// notice — same policy as space create (Service.AttachMemories owns it).
	if err := svc.AttachMemories(ctx, space.AttachMemoriesOptions{
		SpaceID:    spaceID,
		Specs:      memories,
		References: references,
	}); err != nil {
		return reviewResult{}, err
	}

	skillDir := filepath.Join(spacePath, ".claude", "skills", summon.ReviewSkillName)
	if err := os.MkdirAll(skillDir, config.DefaultDirMode); err != nil {
		return reviewResult{}, err
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), prTeachSkill, 0o644); err != nil {
		return reviewResult{}, err
	}

	return reviewResult{
		SpaceID:      spaceID,
		WorktreePath: filepath.Join(spacePath, repoName),
		HeadRef:      "origin/" + prHeadRef,
		BaseRef:      "origin/" + baseBranch,
		SpecFile:     filepath.Join(spacePath, "spec", filepath.Base(specSrc)),
	}, nil
}

// defaultReviewSpaceID derives the space id from the RESOLVED repo name, so
// --repo hl-nemo yields review-hl-nemo-N regardless of the PR's GitHub name.
func defaultReviewSpaceID(repoName string, number int) string {
	return fmt.Sprintf("review-%s-%d", repoName, number)
}

// reviewPRHeadRef is the origin-relative name the PR head is fetched to;
// reviewPRRefspec maps GitHub's refs/pull/N/head onto it.
func reviewPRHeadRef(number int) string {
	return fmt.Sprintf("pr/%d", number)
}

func reviewPRRefspec(number int) string {
	return fmt.Sprintf("+refs/pull/%d/head:refs/remotes/origin/%s", number, reviewPRHeadRef(number))
}

func reviewSpecPath(spacePath string, number int) string {
	return filepath.Join(spacePath, "spec", fmt.Sprintf("pr-%d.md", number))
}

// reviewMetaRef is the identity PR metadata is fetched under. A PR typed
// with an owner (URL or owner/repo#N) keeps that identity even when --repo
// selects a differently named mirror. A <name>#N reference carries no owner,
// so it is derived from the resolved repo's registered URL -- whether that
// repo was picked by --repo or by name lookup -- but only when that URL is
// unambiguously github.com/<owner>/<repo> (see parseGitHubDotComURL): an SSH
// alias or GHE host may not be GitHub at all, and guessing would point gh at
// the wrong repository. Anything else leaves Owner empty, and
// fetchPRMetadata degrades with its owner-unknown note.
func reviewMetaRef(ref prRef, repoCfg config.Repository) prRef {
	if ref.Owner != "" {
		return ref
	}
	owner, repo, ok := parseGitHubDotComURL(repoCfg.URL)
	if !ok {
		return ref
	}
	return prRef{Owner: owner, Repo: repo, Number: ref.Number}
}

// reviewRefreshRemedy renders the exact `stave review ... --refresh` command
// that re-fetches metadata into the space being created: the PR as typed,
// the space id only when it differs from the derived default, and --repo
// when the override was used. Every argument is shell-quoted so the command
// can be pasted verbatim.
func reviewRefreshRemedy(prArg, spaceID, defaultSpaceID, repoOverride string) string {
	parts := []string{"stave review", shellQuote(prArg)}
	if spaceID != defaultSpaceID {
		parts = append(parts, shellQuote(spaceID))
	}
	if repoOverride != "" {
		parts = append(parts, "--repo", shellQuote(repoOverride))
	}
	parts = append(parts, "--refresh")
	return strings.Join(parts, " ")
}

// reviewMetaRemedy is the sentence telling the user how to fill in a minimal
// spec. When the metadata identity carries an owner, the failure is gh
// itself (missing or unauthenticated) and the fix is to re-run the same
// command with --refresh. When the owner is unknown (a <name>#N reference
// whose registered URL is not a plain github.com URL), re-running the same
// ownerless command would fail identically, so the remedy instead spells out
// the owner-qualified coordinate: proposed from the registered URL's
// owner/repo when it parses (the user confirms the host is GitHub), or as a
// placeholder when it does not (local path, file:// URL). --repo pins the
// resolved mirror so the refresh lands in the same space.
func reviewMetaRemedy(prArg string, ref, metaRef prRef, repoName string, repoCfg config.Repository, spaceID, defaultSpaceID, repoOverride string) string {
	if metaRef.Owner != "" {
		return fmt.Sprintf("After fixing gh auth ('gh auth status'), run '%s' to fill it in.", reviewRefreshRemedy(prArg, spaceID, defaultSpaceID, repoOverride))
	}
	command := func(coordinate string) string {
		parts := []string{"stave review", coordinate}
		if spaceID != defaultSpaceID {
			parts = append(parts, shellQuote(spaceID))
		}
		parts = append(parts, "--repo", shellQuote(repoName), "--refresh")
		return strings.Join(parts, " ")
	}
	if _, owner, repo, ok := parseRegisteredURL(repoCfg.URL); ok {
		coordinate := fmt.Sprintf("%s/%s#%d", owner, repo, ref.Number)
		return fmt.Sprintf("If this repo lives at github.com/%s/%s, run '%s' to fill it in.", owner, repo, command(shellQuote(coordinate)))
	}
	return fmt.Sprintf("Re-run as '%s' with the GitHub owner/repo to fill it in.", command(fmt.Sprintf("<owner>/<repo>#%d", ref.Number)))
}

// refreshReviewSpace re-fetches PR metadata into an existing review space and
// rewrites spec/pr-<N>.md. Nothing else in the space changes: no worktree
// checkout, no manifest edit, no skill install. Every validation runs before
// the first write, and a metadata failure leaves the existing spec untouched.
func (a *app) refreshReviewSpace(cmd *cobra.Command, cfg *config.Config, cfgPath string, prArg string, ref prRef, repoOverride string, spaceID string) error {
	ctx := cmd.Context()
	client := git.New()

	var defaultSpaceID, spacePath, specFile string
	var edit space.RepoManifest
	in, err := a.fetchReviewInputs(ctx, cmd, cfg, cfgPath, client, ref, repoOverride, reviewFetchOptions{
		allowAutoRegister: false,
		verb:              "refresh",
		beforeFetch: func(repoName string, repoCfg config.Repository) error {
			defaultSpaceID = defaultReviewSpaceID(repoName, ref.Number)
			if spaceID == "" {
				spaceID = defaultSpaceID
			}
			if err := config.ValidateName("space id", spaceID); err != nil {
				return err
			}
			spacePath = filepath.Join(cfg.AgentWorkDir, spaceID)
			if _, err := os.Stat(spacePath); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no review space %q to refresh; run without --refresh to create it", spaceID)
				}
				return err
			}
			m, err := space.LoadManifest(spacePath)
			if err != nil {
				return fmt.Errorf("load manifest of space %q: %w", spaceID, err)
			}
			if m.ID != spaceID {
				return fmt.Errorf("space %q manifest records id %q; refusing to refresh a mismatched space", spaceID, m.ID)
			}
			if m.Kind != reviewSpaceKind {
				return fmt.Errorf("space %q is kind %q, not a review space; refusing to refresh", spaceID, m.Kind)
			}
			var ok bool
			edit, ok = reviewEditRepo(m)
			if !ok {
				return fmt.Errorf("space %q has no edit worktree; refusing to refresh", spaceID)
			}
			if edit.Name != repoName && !sameCanonicalPath(edit.BareRepoPath, repoCfg.BareRepoPath) {
				return fmt.Errorf("space %q reviews repo %q (bare repo %s), but %s resolved to repo %q (bare repo %s); refusing to refresh", spaceID, edit.Name, edit.BareRepoPath, prArg, repoName, repoCfg.BareRepoPath)
			}
			specFile = reviewSpecPath(spacePath, ref.Number)
			if _, err := os.Stat(specFile); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("space %q has no spec for PR #%d (%s); it was created for a different PR, refusing to refresh", spaceID, ref.Number, specFile)
				}
				return err
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	if in.MetaErr != nil {
		err := fmt.Errorf("could not fetch PR metadata for %s: %w (existing spec left unchanged)", prArg, in.MetaErr)
		if in.MetaRef.Owner == "" {
			// The same ownerless command would fail again; say what to type.
			return fmt.Errorf("%w. %s", err, reviewMetaRemedy(prArg, ref, in.MetaRef, in.RepoName, in.RepoCfg, spaceID, defaultSpaceID, repoOverride))
		}
		return err
	}
	baseBranch := in.BaseBranch
	bareRepo := in.RepoCfg.BareRepoPath

	// Staleness is disclosed, never repaired: the worktree stays where the
	// user (and any uncommitted review notes) left it. The comparison reads
	// the worktree's actual HEAD, not the bare repo's branch ref: a detached
	// checkout inside the worktree leaves the branch ref where it was.
	var notes []string
	prHead, err := client.RevParse(ctx, bareRepo, "refs/remotes/origin/"+reviewPRHeadRef(ref.Number))
	if err != nil {
		return fmt.Errorf("resolve PR #%d head: %w", ref.Number, err)
	}
	worktreePath := filepath.Join(spacePath, edit.Path)
	worktreeHead, err := client.HeadCommit(ctx, worktreePath)
	if err != nil {
		return fmt.Errorf("resolve HEAD of worktree %s: %w", worktreePath, err)
	}
	if prHead != worktreeHead {
		// Both commits live in the bare repo's object store (worktrees share
		// it), so ancestry can be answered there. A worktree that has the PR
		// head in its history is not stale: the reviewer committed on top.
		onTop, err := client.IsAncestor(ctx, bareRepo, prHead, worktreeHead)
		if err != nil {
			return fmt.Errorf("compare worktree HEAD %s with PR #%d head: %w", shortSHA(worktreeHead), ref.Number, err)
		}
		if onTop {
			notes = append(notes, fmt.Sprintf("worktree has local commits on top of PR head %s (worktree at %s)", shortSHA(prHead), shortSHA(worktreeHead)))
		} else {
			notes = append(notes, fmt.Sprintf("worktree is checked out at %s; PR head is now %s — recreate the space to review the latest code", shortSHA(worktreeHead), shortSHA(prHead)))
		}
	}
	recordedBase := strings.TrimPrefix(edit.Base, "origin/")
	if recordedBase != baseBranch {
		notes = append(notes, fmt.Sprintf("PR base is now %q; this space was created against %q", baseBranch, recordedBase))
	}
	baseExists, err := client.RefExists(ctx, bareRepo, "refs/remotes/origin/"+baseBranch)
	if err != nil {
		return fmt.Errorf("check base branch origin/%s: %w", baseBranch, err)
	}
	if !baseExists {
		notes = append(notes, fmt.Sprintf("base branch origin/%s was not found in the mirror; quickstart commands referencing it will fail until it is fetched", baseBranch))
	}
	for _, note := range notes {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", note)
	}

	remedy := reviewMetaRemedy(prArg, ref, in.MetaRef, in.RepoName, in.RepoCfg, spaceID, defaultSpaceID, repoOverride)
	specSrc, err := stageReviewSpec(edit.Name, in.MetaRef, in.Meta, true, "origin/"+baseBranch, remedy, notes)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(specSrc)) }()
	data, err := os.ReadFile(specSrc)
	if err != nil {
		return err
	}
	if err := fsio.WriteFileAtomic(specFile, data, 0o644); err != nil {
		return fmt.Errorf("write refreshed spec: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "refreshed spec at %s\n", specFile)
	return nil
}

// reviewEditRepo returns the space's edit worktree entry; a review space has
// exactly one, created by setUpReviewSpace.
func reviewEditRepo(m space.Manifest) (space.RepoManifest, bool) {
	for _, repo := range m.Repos {
		if repo.Mode == space.ModeEdit {
			return repo, true
		}
	}
	return space.RepoManifest{}, false
}

// shortSHA abbreviates to 12 characters: unique in any realistically sized
// repository, and long enough to paste into git without ambiguity.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// resolveReviewRepo maps the PR coordinate onto a registered repo. ref is
// never mutated. Resolution order:
//
//  1. --repo <name> forces a registered repo and bypasses every check below.
//  2. Tier 1: a registered repo named like the PR's repo, guarded so a
//     same-named registration that points at a different owner/repo is
//     refused rather than silently reviewed.
//  3. Tier 2: a registered repo whose URL is exactly github.com/<owner>/<repo>.
//  4. Near match: a registered repo whose URL names the same owner/repo under
//     another host (SSH alias, GHE, non-default port) — refused with a hint,
//     because stave cannot know whether that host is GitHub.
//  5. Auto-register from the PR's public https clone URL — only when
//     allowAutoRegister is set; --refresh passes false because a space that
//     could be refreshed already implies a registered repo, and registering
//     one as a side effect of a metadata refresh would be a surprise.
//
// Registered names are ValidateName-safe ([alnum][alnum._-]*), so the %q
// spelling used for --repo hints is already pasteable; paths and URLs go
// through shellQuote.
func resolveReviewRepo(ctx context.Context, cmd *cobra.Command, cfg *config.Config, cfgPath string, client *git.Client, ref prRef, repoOverride string, allowAutoRegister bool) (name string, repoCfg config.Repository, err error) {
	if repoOverride != "" {
		entry, ok := cfg.Repos[repoOverride]
		if !ok {
			return "", config.Repository{}, fmt.Errorf("repo %q is not registered; run 'stave repos list' to see registered repos, or omit --repo to resolve from the PR URL", repoOverride)
		}
		return repoOverride, entry, nil
	}

	// Tier 1: registered name matches the PR's repo name.
	if entry, ok := cfg.Repos[ref.Repo]; ok {
		if ref.Owner == "" {
			return ref.Repo, entry, nil
		}
		if _, owner, repo, parsable := parseRegisteredURL(entry.URL); parsable &&
			(!strings.EqualFold(owner, ref.Owner) || !strings.EqualFold(repo, ref.Repo)) {
			var candidates []string
			for _, other := range sortedRepoNames(cfg) {
				if other == ref.Repo {
					continue
				}
				tier2, near := classifyRegisteredURL(cfg.Repos[other].URL, ref.Owner, ref.Repo)
				if tier2 || near {
					candidates = append(candidates, other)
				}
			}
			prefix := fmt.Sprintf("registered repo %q points at %s/%s, not %s/%s", ref.Repo, owner, repo, ref.Owner, ref.Repo)
			switch len(candidates) {
			case 0:
				// --purge: a plain remove keeps the bare cache, and the
				// re-add would then collide on it.
				return "", config.Repository{}, fmt.Errorf("%s; re-run with --repo %q to force it, or fix the registration with 'stave repos remove %s --purge' + 'stave repos add %s <url>'", prefix, ref.Repo, shellQuote(ref.Repo), shellQuote(ref.Repo))
			case 1:
				return "", config.Repository{}, fmt.Errorf("%s; did you mean --repo %q?", prefix, candidates[0])
			default:
				return "", config.Repository{}, fmt.Errorf("%s; candidates: %s (re-run with --repo <name>)", prefix, strings.Join(candidates, ", "))
			}
		}
		// Local paths, file:// URLs, and other unparsable URLs carry no
		// owner/repo to compare against; trust the name.
		return ref.Repo, entry, nil
	}
	if ref.Owner == "" {
		if !allowAutoRegister {
			return "", config.Repository{}, fmt.Errorf("repo %q is not registered; cannot refresh — run without --refresh to create the review space", ref.Repo)
		}
		return "", config.Repository{}, fmt.Errorf("repo %q is not registered; use owner/repo#%d or a PR URL so it can be registered automatically", ref.Repo, ref.Number)
	}

	// Tier 2: exact github.com/<owner>/<repo> URL match; near matches are
	// collected in the same pass and only consulted when tier 2 misses.
	var tier2Names, nearNames []string
	for _, candidate := range sortedRepoNames(cfg) {
		tier2, near := classifyRegisteredURL(cfg.Repos[candidate].URL, ref.Owner, ref.Repo)
		switch {
		case tier2:
			tier2Names = append(tier2Names, candidate)
		case near:
			nearNames = append(nearNames, candidate)
		}
	}
	switch len(tier2Names) {
	case 0:
	case 1:
		return tier2Names[0], cfg.Repos[tier2Names[0]], nil
	default:
		return "", config.Repository{}, fmt.Errorf("%d registered repos match github.com/%s/%s: %s; re-run with --repo <name>", len(tier2Names), ref.Owner, ref.Repo, strings.Join(tier2Names, ", "))
	}
	// Near-match wording shows the registered URL, so both the URL and the
	// host derived from it go through redaction: a credentialed URL must
	// never reach the terminal. gh.ParseAnyOwnerRepo now strips userinfo
	// from every URL shape itself (including ssh://user:pass@host), so
	// deriving the host from the redacted URL is belt-and-suspenders; it is
	// kept so a future parser regression cannot leak credentials here.
	if len(nearNames) == 1 {
		nearName := nearNames[0]
		nearURL := redactURL(cfg.Repos[nearName].URL)
		nearHost, _, _, _ := gh.ParseAnyOwnerRepo(nearURL)
		return "", config.Repository{}, fmt.Errorf("repo %s/%s is not registered under a matching URL, but registered repo %q (%s) points at the same owner/repo under host %q. If that is this repo, re-run with --repo %q; otherwise register it explicitly with 'stave repos add <name> <url>'", ref.Owner, ref.Repo, nearName, nearURL, nearHost, nearName)
	}
	if len(nearNames) > 1 {
		descriptions := make([]string, 0, len(nearNames))
		for _, nearName := range nearNames {
			nearURL := redactURL(cfg.Repos[nearName].URL)
			nearHost, _, _, _ := gh.ParseAnyOwnerRepo(nearURL)
			descriptions = append(descriptions, fmt.Sprintf("%s (%s) host %q", nearName, nearURL, nearHost))
		}
		return "", config.Repository{}, fmt.Errorf("repo %s/%s is not registered under a matching URL, but registered repos point at the same owner/repo under other hosts: %s. If one of them is this repo, re-run with --repo <name>; otherwise register it explicitly with 'stave repos add <name> <url>'", ref.Owner, ref.Repo, strings.Join(descriptions, "; "))
	}

	if !allowAutoRegister {
		return "", config.Repository{}, fmt.Errorf("repo %s/%s is not registered; cannot refresh — run without --refresh to create the review space", ref.Owner, ref.Repo)
	}

	// Auto-register from the public https clone URL. Every failure branch
	// below removes the entry again. Delete-only rollback is safe ONLY
	// because tier 1 above already established that cfg.Repos had no entry
	// named ref.Repo — so the registration we remove is the one we just
	// added. RegisterRepository silently overwrites an existing entry; if the
	// resolution order ever lets a pre-existing entry reach this point,
	// snapshot and restore it here instead of deleting.
	cloneURL := ref.cloneURL()
	fmt.Fprintf(cmd.OutOrStdout(), "registering %s from %s\n", ref.Repo, cloneURL)
	repoCfg, err = cfg.RegisterRepository(ref.Repo, cloneURL, "")
	if err != nil {
		return "", config.Repository{}, err
	}
	if _, err := os.Stat(repoCfg.BareRepoPath); err == nil {
		cfg.UnregisterRepository(ref.Repo)
		return "", config.Repository{}, fmt.Errorf("bare repo path already exists: %s; run '%s' to reuse it, or delete it to re-clone", repoCfg.BareRepoPath, adoptRetryHint(ref.Repo, cloneURL))
	} else if !os.IsNotExist(err) {
		cfg.UnregisterRepository(ref.Repo)
		return "", config.Repository{}, err
	}
	if err := cloneBareFresh(ctx, client, cloneURL, repoCfg.BareRepoPath, false); err != nil {
		cfg.UnregisterRepository(ref.Repo)
		return "", config.Repository{}, fmt.Errorf("auto-registration of %q failed: could not clone %s: %w\nhint: this repo was auto-registered because no registered repo matched %s/%s — check 'stave repos list'; if it is already registered under another name, re-run with --repo <name>; for a private repo, register it manually with 'stave repos add %s <ssh-url>'", ref.Repo, cloneURL, err, ref.Owner, ref.Repo, shellQuote(ref.Repo))
	}
	// From here the clone exists on disk. It is deliberately kept on failure
	// (cloning is the expensive step); the message says how to adopt it,
	// since a bare retry would stop at the collision guard above.
	keptClone := fmt.Sprintf("; the clone was kept at %s — retry with '%s'", shellQuote(repoCfg.BareRepoPath), adoptRetryHint(ref.Repo, cloneURL))
	branch, err := finalizeBareMirror(ctx, cmd, client, ref.Repo, repoCfg.BareRepoPath, "", cfg.DefaultBase, true, false)
	if err != nil {
		cfg.UnregisterRepository(ref.Repo)
		return "", config.Repository{}, fmt.Errorf("auto-registration of %q failed: %w%s", ref.Repo, err, keptClone)
	}
	if branch != "" {
		repoCfg.DefaultBranch = branch
		cfg.Repos[ref.Repo] = repoCfg
	}
	if err := cfg.Save(cfgPath); err != nil {
		cfg.UnregisterRepository(ref.Repo)
		return "", config.Repository{}, fmt.Errorf("save config after registering %q: %w%s", ref.Repo, err, keptClone)
	}
	return ref.Repo, repoCfg, nil
}

// parseRegisteredURL splits a registered clone URL into host/owner/repo on
// any host, treating file:// URLs as unparsable local paths: the lenient
// parser would otherwise read "file:///srv/o/r.git" as host "file", owner
// "srv", which is nonsense to compare against a GitHub coordinate.
func parseRegisteredURL(registeredURL string) (host, owner, repo string, ok bool) {
	if isFileURL(strings.TrimSpace(registeredURL)) {
		return "", "", "", false
	}
	return gh.ParseAnyOwnerRepo(registeredURL)
}

// parseGitHubDotComURL returns the owner/repo of a registered URL that is
// unambiguously github.com: any scheme, optional userinfo or .git, and the
// host github.com — optionally with the scheme's default port spelled out
// (https/http :443, ssh :22). An scp-like URL whose alias is literally
// "github.com" (git@github.com:o/r) is indistinguishable from the real host
// and counts. Any other host, port, local path, or file:// URL is not.
func parseGitHubDotComURL(registeredURL string) (owner, repo string, ok bool) {
	host, owner, repo, ok := parseRegisteredURL(registeredURL)
	if !ok || !isGitHubDotComHost(registeredURL, host) {
		return "", "", false
	}
	return owner, repo, true
}

func isGitHubDotComHost(registeredURL, host string) bool {
	if strings.EqualFold(host, "github.com") {
		return true
	}
	s := strings.ToLower(strings.TrimSpace(registeredURL))
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return strings.EqualFold(host, "github.com:443")
	case strings.HasPrefix(s, "ssh://"):
		return strings.EqualFold(host, "github.com:22")
	}
	return false
}

// classifyRegisteredURL compares a registered clone URL against a GitHub
// owner/repo coordinate. tier2Match means the URL is unambiguously
// github.com/<owner>/<repo> (parseGitHubDotComURL, case insensitive).
// nearMatch means the URL names the same owner/repo under some other host —
// an SSH config alias, a GitHub Enterprise host, or github.com with a
// non-default port — where stave cannot tell whether it is the same
// repository. Local paths and file:// URLs match nothing.
func classifyRegisteredURL(registeredURL, owner, repo string) (tier2Match, nearMatch bool) {
	host, gotOwner, gotRepo, ok := parseRegisteredURL(registeredURL)
	if !ok || !strings.EqualFold(gotOwner, owner) || !strings.EqualFold(gotRepo, repo) {
		return false, false
	}
	if isGitHubDotComHost(registeredURL, host) {
		return true, false
	}
	return false, true
}

// fetchPRMetadata reads PR details via the gh CLI when it is available and
// authenticated; the review flow degrades gracefully without it (a missing
// binary surfaces as gh.ErrGHUnavailable through the client's runner).
func fetchPRMetadata(ctx context.Context, client *gh.Client, ref prRef) (prMetadata, error) {
	var meta prMetadata
	if ref.Owner == "" {
		return meta, fmt.Errorf("PR referenced by registered repo name; owner unknown")
	}
	output, err := client.ViewPRJSON(ctx, ref.webURL(),
		"title,body,state,isDraft,baseRefName,headRefName,additions,deletions,changedFiles,url,author,statusCheckRollup")
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(output, &meta); err != nil {
		return meta, fmt.Errorf("parse gh pr view output: %w", err)
	}
	return meta, nil
}

// stageReviewSpec renders the PR's context to a temp file that InitSpace
// copies into spec/, so a summoned agent (or a review skill such as
// pr-teach) starts with the full picture. remedy is the full sentence
// (see reviewMetaRemedy) surfaced when metadata is missing; notes are
// refresh-time disclosures (stale worktree, base drift) appended verbatim.
func stageReviewSpec(repoName string, ref prRef, meta prMetadata, haveMeta bool, baseRef string, remedy string, notes []string) (string, error) {
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
	// baseRef derives from the PR's base branch name, which git allows to
	// contain shell metacharacters (e.g. "main;touch${IFS}/tmp/x" passes
	// `git check-ref-format --branch`), so it is quoted wherever it lands
	// in a pasteable command.
	quotedBase := shellQuote(baseRef)
	fmt.Fprintf(&b, "```sh\ncd %s\ngit diff %s...HEAD           # the full PR diff\ngit log %s..HEAD --oneline   # the PR's commits\n```\n", shellQuote(repoName), quotedBase, quotedBase)
	if haveMeta && strings.TrimSpace(meta.Body) != "" {
		fmt.Fprintf(&b, "\n## Author's description (claims, not facts — verify against the code)\n\n%s\n", strings.TrimSpace(meta.Body))
	}
	if !haveMeta {
		fmt.Fprintf(&b, "\n> PR title/description could not be fetched (gh CLI unavailable or unauthenticated); review from the diff and commits above.")
		if remedy != "" {
			fmt.Fprintf(&b, " %s", remedy)
		}
		b.WriteString("\n")
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, "\n## Refresh notes\n\n")
		for _, note := range notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	if err := os.WriteFile(specFile, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return specFile, nil
}
