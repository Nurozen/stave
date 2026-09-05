package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/agent"
	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/gh"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/memory"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/Nurozen/stave/internal/tether"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Build metadata threaded in from package main's ldflags-populated vars
// (see cmd/stave/main.go). main.run sets these before ExecuteContext.
var (
	BuildVersion string
	BuildCommit  string
	BuildDate    string
)

type app struct {
	version         string
	commit          string
	date            string
	configPath      string
	shellChdirFD    int
	providerFactory agent.ProviderFactory
	secretStore     agent.SecretStore
	summonLauncher  summon.Launcher
	portalRunner    portal.Runner
	ghRunner        gh.Runner // nil = gh.ExecRunner; tests inject fakes
	isTerminal      func(*cobra.Command) bool
}

func NewRootCommand() *cobra.Command {
	return newRootCommand(&app{
		version: BuildVersion,
		commit:  BuildCommit,
		date:    BuildDate,
	})
}

func newRootCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stave",
		Short: "Manage agent workspaces backed by shared bare Git repositories",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.captureShellChdirHandoff()
		},
	}
	// Errors are printed exactly once (by main); usage is only dumped for
	// flag/arg parse mistakes, not for runtime failures.
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, c.CommandPath())
	})
	cmd.PersistentFlags().StringVar(&a.configPath, "config", "", "config file path (default ~/.config/stave/config.yaml)")
	cmd.AddCommand(
		a.setupCommand(),
		a.inscribeCommand(),
		a.shellInitCommand(),
		a.reposCommand(),
		a.spaceCommand(),
		a.memoryCommand(),
		a.configCommand(),
		a.portalCommand(),
		a.agentCommand(),
		a.summonCommand(),
		a.reviewCommand(),
		a.sagaCommand(),
		a.versionCommand(),
	)
	return cmd
}

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Stave version, commit, and build date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, c, d := a.resolveBuildInfo()
			fmt.Fprintf(cmd.OutOrStdout(), "stave %s\ncommit: %s\ndate: %s\n", v, c, d)
			return nil
		},
	}
}

// resolveBuildInfo returns the version, commit, and build date for display,
// preferring values injected via ldflags. When the injected version is empty
// or the "dev" default, it falls back to runtime/debug build info (e.g. for
// `go install`ed binaries). Empty fields are normalized to "unknown".
func (a *app) resolveBuildInfo() (version, commit, date string) {
	version, commit, date = a.version, a.commit, a.date
	if version == "" || version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			v, c, d := fromBuildInfo(bi)
			if v != "" {
				version = v
			}
			if c != "" {
				commit = c
			}
			if d != "" {
				date = d
			}
		}
	}
	if version == "" {
		version = "unknown"
	}
	if commit == "" {
		commit = "unknown"
	}
	if date == "" {
		date = "unknown"
	}
	return version, commit, date
}

// fromBuildInfo extracts version/commit/date from a debug.BuildInfo. It is a
// pure helper (no globals, no I/O) so it can be unit-tested directly. The main
// module version is used unless it is empty or the "(devel)" placeholder; the
// commit and date come from the vcs.revision / vcs.time build settings.
func fromBuildInfo(bi *debug.BuildInfo) (v, c, d string) {
	if bi == nil {
		return "", "", ""
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			c = s.Value
		case "vcs.time":
			d = s.Value
		}
	}
	return v, c, d
}

func (a *app) setupCommand() *cobra.Command {
	var force bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create the Stave root directories and config file",
		Long: `Create the Stave root directories and write the config file.

An existing config file is never rewritten unless --force is given, so a
host that re-runs setup cannot discard edited settings by accident.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				cfg, path, err := config.Load(a.configPath)
				if err != nil {
					return nil, err
				}
				configExisted, err := pathExists(path)
				if err != nil {
					return nil, err
				}
				if configExisted && !force {
					return nil, &space.ConfigExistsError{Path: path}
				}
				created, existed := []string{}, []string{}
				for _, candidate := range []string{cfg.Root, cfg.BareReposDir, cfg.AgentWorkDir} {
					exists, err := pathExists(candidate)
					if err != nil {
						return nil, err
					}
					if exists {
						existed = append(existed, candidate)
					} else {
						created = append(created, candidate)
					}
				}
				if configExisted {
					existed = append(existed, path)
				} else {
					created = append(created, path)
				}
				if err := cfg.EnsureRootDirs(); err != nil {
					return nil, err
				}
				if err := cfg.Save(path); err != nil {
					return nil, err
				}
				if jsonOut {
					return setupJSON{
						ConfigPath:   path,
						Root:         cfg.Root,
						BareReposDir: cfg.BareReposDir,
						AgentWorkDir: cfg.AgentWorkDir,
						Created:      created,
						Existed:      existed,
					}, nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "initialized stave root at %s\nconfig: %s\n", cfg.Root, path)
				return nil, nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "rewrite an existing config file (it is refused otherwise)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

// pathExists reports whether path exists; errors other than not-exist
// propagate.
func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// groupCommand builds a subcommand container that rejects unknown
// subcommands with a nonzero exit instead of cobra's default print-help-and-
// exit-0, so a typo'd command cannot look like success to a script.
func groupCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown command %q for %q; run %q", args[0], cmd.CommandPath(), cmd.CommandPath()+" --help")
		},
	}
}

func (a *app) reposCommand() *cobra.Command {
	cmd := groupCommand("repos", "Manage registered bare repositories")
	cmd.AddCommand(
		a.reposAddCommand(),
		a.reposListCommand(),
		a.reposSyncCommand(),
		a.reposRemoveCommand(),
		a.reposDescribeCommand(),
		a.reposTethersCommand(),
		a.reposTetherCommand(),
		a.reposForgetCommand(),
	)
	return cmd
}

func (a *app) reposAddCommand() *cobra.Command {
	var dryRun bool
	var adopt bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Clone and register a bare repository (or adopt an existing cache)",
		Long: `Clone <url> as a bare mirror under the bare-repos directory and register it
under <name>. After cloning, stave configures branch tracking and fetches,
then attempts to set origin/HEAD and record the remote's default branch in
the registry so space operations can base new branches on it; those last two
steps are best-effort and failures are reported as notes.

If a bare repo already exists at the derived path (for example after
'stave repos remove', which keeps the cache), the add is refused unless
--adopt is given. With --adopt the existing cache is reused when it is a bare
clone of the same repository: its origin URL is rewritten to <url> and it
reaches the same state stave relies on (origin URL, tracking refspec,
remote-tracking refs, origin/HEAD, default branch); pre-existing local
branches in the cache are left as-is.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				// The clone/adopt helpers print their notes to the command's
				// stdout/stderr; --json routes both into the sink for the
				// duration of the run so they become notes (or the dry-run
				// plan) instead of prose beside the payload.
				sink := newOutputSink(cmd, jsonOut)
				if jsonOut {
					stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
					cmd.SetOut(sink.Writer())
					cmd.SetErr(sink.Writer())
					defer func() {
						cmd.SetOut(stdout)
						cmd.SetErr(stderr)
					}()
				}
				out := cmd.OutOrStdout()
				cfg, path, err := a.loadConfig()
				if err != nil {
					return nil, err
				}
				name, url := args[0], args[1]
				if _, exists := cfg.Repos[name]; exists {
					return nil, &space.RepoExistsError{Repo: name}
				}
				if dryRun {
					fmt.Fprintf(out, "dry-run: create %s\n", cfg.Root)
					fmt.Fprintf(out, "dry-run: create %s\n", cfg.BareReposDir)
					fmt.Fprintf(out, "dry-run: create %s\n", cfg.AgentWorkDir)
				} else {
					if err := cfg.EnsureRootDirs(); err != nil {
						return nil, err
					}
				}
				repo, err := cfg.RegisterRepository(name, url, "")
				if err != nil {
					return nil, err
				}
				// A plain dry-run skips the collision stat so the plan prints even
				// over a stale cache; --adopt needs the answer to say whether it
				// would adopt or clone.
				existing := false
				if adopt || !dryRun {
					if _, err := os.Stat(repo.BareRepoPath); err == nil {
						existing = true
					} else if !os.IsNotExist(err) {
						return nil, err
					}
				}
				if existing && !adopt {
					return nil, &space.CacheExistsError{Repo: name, Path: repo.BareRepoPath, RetryHint: adoptRetryHint(name, url)}
				}
				client := git.New(git.WithDryRun(dryRun, func(format string, args ...any) {
					fmt.Fprintf(out, format+"\n", args...)
				}))
				ctx := cmd.Context()
				var adopted adoption
				if existing {
					adopted, err = adoptBareRepo(ctx, cmd, client, repo.BareRepoPath, name, url, dryRun)
					if err != nil {
						return nil, err
					}
				} else if err := cloneBareFresh(ctx, client, url, repo.BareRepoPath, dryRun); err != nil {
					return nil, &space.CloneFailedError{Repo: name, Err: err}
				}
				// Past this point the cache on disk is real. A failure must not
				// leave an adopted cache pointing at a URL (or carrying a fetch
				// refspec) for a registration that never happened, and must tell
				// the user a fresh clone survived. The rollback runs under a
				// context that ignores cancellation: an interrupt is a likely
				// cause of the failure and must not also skip the cleanup.
				failAfterCache := func(err error) error {
					switch {
					case existing && !dryRun:
						return restoreAdoption(context.WithoutCancel(ctx), client, repo.BareRepoPath, adopted, err)
					case !existing && !dryRun:
						return fmt.Errorf("%w; the clone was kept at %s — retry with '%s'", err, shellQuote(repo.BareRepoPath), adoptRetryHint(name, url))
					}
					return err
				}
				branch, err := finalizeBareMirror(ctx, cmd, client, name, repo.BareRepoPath, "", cfg.DefaultBase, true, dryRun)
				if err != nil {
					return nil, failAfterCache(err)
				}
				if branch != "" {
					repo.DefaultBranch = branch
					cfg.Repos[name] = repo
				}
				if !dryRun {
					if err := cfg.Save(path); err != nil {
						return nil, failAfterCache(err)
					}
				}
				registered := fmt.Sprintf("registered %s at %s", name, repo.BareRepoPath)
				fmt.Fprintln(out, registered)
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return reposAddJSON{
					Name:          name,
					URL:           url,
					BareRepoPath:  repo.BareRepoPath,
					DefaultBranch: repo.DefaultBranch,
					Adopted:       existing,
					Notes:         sink.Notes(registered),
				}, nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "reuse an existing bare repo cache at the derived path if it is a clone of the same repository; it reaches the same state stave relies on (origin URL, tracking refspec, remote-tracking refs, origin/HEAD, default branch) and pre-existing local branches are left as-is")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

// reposListRow is the typed `repos list --json` row: the registry entry plus
// the learned tether count the --verbose text path reports.
type reposListRow struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	BareRepoPath  string `json:"bareRepoPath"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Description   string `json:"description,omitempty"`
	TetherCount   int    `json:"tetherCount"`
}

func (a *app) reposListCommand() *cobra.Command {
	var verbose bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			names := sortedRepoNames(cfg)
			tetherCounts := map[string]int{}
			if verbose || jsonOut {
				if f, err := tether.Load(tether.Path(*cfg)); err == nil {
					for _, t := range f.Tethers {
						tetherCounts[t.From]++
					}
				}
			}
			if jsonOut {
				rows := make([]reposListRow, 0, len(names))
				for _, name := range names {
					repo := cfg.Repos[name]
					rows = append(rows, reposListRow{
						Name:          name,
						URL:           repo.URL,
						BareRepoPath:  repo.BareRepoPath,
						DefaultBranch: repo.DefaultBranch,
						Description:   repo.Description,
						TetherCount:   tetherCounts[name],
					})
				}
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			for _, name := range names {
				repo := cfg.Repos[name]
				if verbose {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\ttethers=%d\n", name, repo.URL, repo.BareRepoPath, repo.Description, tetherCounts[name])
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", name, repo.URL, repo.BareRepoPath)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "include descriptions and tether counts")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) reposSyncCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [name]",
		Short: "Fetch and prune registered bare repositories (best-effort origin/HEAD refresh)",
		Long: `Fetch and prune one or all registered bare mirrors. Each sync also attempts
to re-point origin/HEAD at the remote's current default branch and, when the
registry has no default branch recorded for a repo, to discover and record
it; both steps are best-effort and failures are reported as notes. A registry
entry that disagrees with the remote is reported but never rewritten.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := a.loadConfig()
			if err != nil {
				return err
			}
			targets := make([]string, 0, len(cfg.Repos))
			if len(args) == 1 {
				if _, ok := cfg.Repos[args[0]]; !ok {
					return fmt.Errorf("repo %q is not registered", args[0])
				}
				targets = append(targets, args[0])
			} else {
				targets = sortedRepoNames(cfg)
			}
			client := git.New()
			ctx := cmd.Context()
			backfill := map[string]string{}
			for _, name := range targets {
				repo := cfg.Repos[name]
				branch, err := finalizeBareMirror(ctx, cmd, client, name, repo.BareRepoPath, repo.DefaultBranch, cfg.DefaultBase, false, false)
				if err != nil {
					return err
				}
				if repo.DefaultBranch == "" && branch != "" {
					backfill[name] = branch
				}
				fmt.Fprintf(cmd.OutOrStdout(), "synced %s\n", name)
			}
			if len(backfill) == 0 {
				return nil
			}
			// Re-load before writing so a long fetch loop never clobbers edits
			// made to the registry in the meantime; only fill still-empty slots
			// whose registration is still the one that was synced.
			fresh, freshPath, err := a.loadConfig()
			if err != nil {
				return err
			}
			if freshPath == "" {
				freshPath = cfgPath
			}
			changed, skipped := applyBackfill(fresh, cfg.Repos, backfill)
			for _, name := range skipped {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: skipped default-branch backfill for %q: registration changed during sync\n", name)
			}
			if len(changed) == 0 {
				return nil
			}
			// Say "recorded" only once it is true on disk: a failed save must
			// not leave the user believing the registry was updated.
			if err := fresh.Save(freshPath); err != nil {
				return err
			}
			for _, name := range changed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: recorded default branch %q for %q\n", backfill[name], name)
			}
			return nil
		},
	}
}

func (a *app) reposRemoveCommand() *cobra.Command {
	var purge bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a repository (keeps its bare repo cache unless --purge)",
		Long: `Remove <name> from the registry. By default the bare repo cache on disk is
kept so it can be re-registered later with
'stave repos add <name> <url> --adopt'; a note on stderr says where it is
and whether any spaces still use it.

With --purge the cache directory is deleted as well. The purge is refused when
any space under the agent work directory still references the cache (by name
or by path), when a space manifest cannot be read, when the path is not a
bare git repository, or when its origin remote does not name the registered
repository; archive or destroy those spaces first, or fix the registration.
The cache is deleted before the registry entry is dropped, so a failed delete
leaves the repo registered and the command can be re-run.

--dry-run prints what would happen without changing anything; the same
reference scan and purge guards apply.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			repoCfg, ok := cfg.Repos[name]
			if !ok {
				return fmt.Errorf("repo %q is not registered", name)
			}
			barePath := repoCfg.BareRepoPath
			if barePath == "" {
				barePath = cfg.BareRepoPath(name)
			}
			refs, walkErr := spacesReferencingRepo(cfg.AgentWorkDir, name, barePath)
			stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

			if purge {
				if walkErr != nil {
					return fmt.Errorf("refusing to purge: %w; fix or delete that space first", walkErr)
				}
				if len(refs) > 0 {
					return fmt.Errorf("refusing to purge: %d space(s) reference this cache (%s); archive or destroy them first", len(refs), strings.Join(refs, ", "))
				}
				existed := true
				if _, err := os.Lstat(barePath); err != nil {
					if !os.IsNotExist(err) {
						return fmt.Errorf("inspect bare repo cache at %s: %w (repo %q is still registered; re-run to retry)", barePath, err, name)
					}
					existed = false
				}
				if existed {
					// Never RemoveAll a path the registry merely claims is a
					// cache: a mis-edited bareRepoPath must not take a
					// user's directory with it.
					client := git.New()
					isBare, err := client.IsBareRepo(cmd.Context(), barePath)
					if err != nil {
						return fmt.Errorf("refusing to purge: %s is not a bare git repository (%v); delete it manually if intended", barePath, err)
					}
					if !isBare {
						return fmt.Errorf("refusing to purge: %s is not a bare git repository; delete it manually if intended", barePath)
					}
					// Being a bare repo is not enough: the registry may point
					// at somebody else's mirror. Only delete a cache whose
					// origin (as configured, not insteadOf-rewritten) names
					// the registered repository.
					origin, err := client.RemoteConfigURL(cmd.Context(), barePath, "origin")
					if err != nil {
						return fmt.Errorf("refusing to purge: could not read origin remote of %s (%v); delete it manually if intended", barePath, err)
					}
					if ok, _ := sameRepoURL(origin, repoCfg.URL); !ok {
						return fmt.Errorf("refusing to purge: %s is a bare repo for %s, not %s; fix the registration or delete it manually", barePath, redactURL(origin), redactURL(repoCfg.URL))
					}
				}
				if dryRun {
					if existed {
						fmt.Fprintf(stdout, "dry-run: remove directory %s\n", barePath)
					}
					fmt.Fprintf(stdout, "dry-run: unregister %s\n", name)
					if existed {
						fmt.Fprintf(stderr, "note: would delete bare repo cache at %s\n", barePath)
					} else {
						fmt.Fprintf(stderr, "note: no bare repo cache at %s\n", barePath)
					}
					return nil
				}
				if existed {
					if err := os.RemoveAll(barePath); err != nil {
						return fmt.Errorf("delete bare repo cache at %s: %w (repo %q is still registered; re-run to retry)", barePath, err, name)
					}
				}
				cfg.UnregisterRepository(name)
				if err := cfg.Save(path); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "unregistered %s\n", name)
				if existed {
					fmt.Fprintf(stderr, "note: deleted bare repo cache at %s\n", barePath)
				} else {
					fmt.Fprintf(stderr, "note: no bare repo cache at %s\n", barePath)
				}
				return nil
			}

			// keepNotes explains the kept cache identically for the real run
			// ("kept") and the dry-run ("would keep"), so a dry-run surfaces
			// the same reference warning or re-register recipe.
			keepNotes := func(verb string) {
				switch {
				case walkErr != nil:
					fmt.Fprintf(stderr, "note: %s bare repo cache at %s (could not scan spaces: %v)\n", verb, barePath, walkErr)
				case len(refs) > 0:
					fmt.Fprintf(stderr, "note: %s bare repo cache at %s; %d space(s) still use it (%s) — do not move or delete it\n", verb, barePath, len(refs), strings.Join(refs, ", "))
				default:
					fmt.Fprintf(stderr, "note: %s bare repo cache at %s\n", verb, barePath)
					target := shellQuote(cfg.BareReposDir) + string(filepath.Separator) + "<new-name>.git"
					fmt.Fprintf(stderr, "note: to re-register under a new name: mv %s %s && stave repos add <new-name> %s --adopt\n", shellQuote(barePath), target, pasteableURL(repoCfg.URL))
				}
			}
			if dryRun {
				fmt.Fprintf(stdout, "dry-run: unregister %s\n", name)
				keepNotes("would keep")
				return nil
			}
			cfg.UnregisterRepository(name)
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "unregistered %s\n", name)
			keepNotes("kept")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the bare repo cache")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without changing anything")
	return cmd
}

func (a *app) reposDescribeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <repo> [text]",
		Short: "Show or set a registered repository's description",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			repo, ok := cfg.Repos[name]
			if !ok {
				return fmt.Errorf("repo %q is not registered", name)
			}
			if len(args) > 1 {
				repo.Description = strings.Join(args[1:], " ")
				cfg.Repos[name] = repo
				if err := cfg.Save(path); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "described %s\n", name)
				return nil
			}
			if repo.Description == "" {
				return fmt.Errorf("repo %q has no description; set one with: stave repos describe %s <text>", name, name)
			}
			fmt.Fprintln(cmd.OutOrStdout(), repo.Description)
			return nil
		},
	}
}

// tetherRow is the typed --json row for `stave repos tethers`.
type tetherRow struct {
	To       string    `json:"to"`
	ToMode   string    `json:"toMode"`
	Strength string    `json:"strength"`
	Count    int       `json:"count"`
	Pinned   string    `json:"pinned,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

func (a *app) reposTethersCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "tethers <repo>",
		Short: "Show learned co-occurrence tethers originating from a repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Repos[name]; !ok {
				return fmt.Errorf("repo %q is not registered", name)
			}
			f, err := tether.Load(tether.Path(*cfg))
			if err != nil {
				return err
			}
			threshold := cfg.Tethers.StrongThreshold
			rows := make([]tetherRow, 0)
			for _, t := range f.Tethers {
				if t.From != name {
					continue
				}
				rows = append(rows, tetherRow{
					To:       t.To,
					ToMode:   string(t.ToMode),
					Strength: string(tether.EffectiveStrength(t, threshold)),
					Count:    t.Count,
					Pinned:   string(t.Pinned),
					LastSeen: t.LastSeen,
				})
			}
			sort.SliceStable(rows, func(i, j int) bool {
				si, sj := rows[i].Strength == string(tether.Strong), rows[j].Strength == string(tether.Strong)
				if si != sj {
					return si // strong before weak
				}
				if rows[i].Count != rows[j].Count {
					return rows[i].Count > rows[j].Count
				}
				return rows[i].To < rows[j].To
			})
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			for _, row := range rows {
				pinned := ""
				if row.Pinned != "" {
					pinned = fmt.Sprintf(" (pinned %s)", row.Pinned)
				}
				lastSeen := "never"
				if !row.LastSeen.IsZero() {
					lastSeen = row.LastSeen.Format(time.RFC3339)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tcount=%d\tlast-seen=%s%s\n", row.To, row.ToMode, row.Strength, row.Count, lastSeen, pinned)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) reposTetherCommand() *cobra.Command {
	var strong, weak, edit, reference bool
	cmd := &cobra.Command{
		Use:   "tether <from> <to>",
		Short: "Pin a manual co-occurrence tether between two registered repos",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strong && weak {
				return fmt.Errorf("choose at most one of --strong or --weak")
			}
			if edit && reference {
				return fmt.Errorf("choose at most one of --edit or --reference")
			}
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			from, to := args[0], args[1]
			if from == to {
				return fmt.Errorf("cannot tether repo %q to itself", from)
			}
			if _, ok := cfg.Repos[from]; !ok {
				return fmt.Errorf("repo %q is not registered", from)
			}
			if _, ok := cfg.Repos[to]; !ok {
				return fmt.Errorf("repo %q is not registered", to)
			}
			pin := tether.Strong
			if weak {
				pin = tether.Weak
			}
			// Default association mode is reference (OQ-D); --edit overrides.
			toMode := tether.ModeReference
			if edit {
				toMode = tether.ModeEdit
			}
			if err := tether.Update(tether.Path(*cfg), func(f *tether.File) error {
				tether.Pin(f, from, to, pin)
				for i := range f.Tethers {
					if f.Tethers[i].From == from && f.Tethers[i].To == to {
						if edit || reference {
							f.Tethers[i].ToMode = toMode
						}
						// Recompute-on-write (D6): stamp the effective strength so
						// the persisted classification reflects the manual pin.
						f.Tethers[i].Strength = tether.EffectiveStrength(f.Tethers[i], cfg.Tethers.StrongThreshold)
						break
					}
				}
				return nil
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pinned %s -> %s as %s\n", from, to, pin)
			return nil
		},
	}
	cmd.Flags().BoolVar(&strong, "strong", false, "pin the tether as strong (default)")
	cmd.Flags().BoolVar(&weak, "weak", false, "pin the tether as weak")
	cmd.Flags().BoolVar(&edit, "edit", false, "record the association mode as edit")
	cmd.Flags().BoolVar(&reference, "reference", false, "record the association mode as reference (default)")
	return cmd
}

func (a *app) reposForgetCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "forget <from> [to]",
		Short: "Remove co-occurrence tethers originating from a repo",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			from := args[0]
			if _, ok := cfg.Repos[from]; !ok {
				return fmt.Errorf("repo %q is not registered", from)
			}
			if all && len(args) == 2 {
				return fmt.Errorf("pass either a <to> repo or --all, not both")
			}
			if !all && len(args) == 1 {
				return fmt.Errorf("specify a <to> repo or --all to forget every tether from %q", from)
			}
			if err := tether.Update(tether.Path(*cfg), func(f *tether.File) error {
				if all {
					tether.RemoveAll(f, from)
				} else {
					tether.Remove(f, from, args[1])
				}
				return nil
			}); err != nil {
				return err
			}
			if all {
				fmt.Fprintf(cmd.OutOrStdout(), "forgot all tethers from %s\n", from)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "forgot tether %s -> %s\n", from, args[1])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "remove every tether originating from <from>")
	return cmd
}

func (a *app) agentCommand() *cobra.Command {
	var providerName string
	var model string
	var incant bool
	var noIncant bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "agent [query]",
		Short: "Ask your agentic paraclete to plan and run Stave operations",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			resolvedProvider, providerCfg, err := resolveAgentProvider(*cfg, providerName, model)
			if err != nil {
				return err
			}
			secret, err := agent.ResolveSecret(cmd.Context(), a.effectiveSecretStore(), providerCfg.APIKeyRef)
			if err != nil {
				return err
			}
			provider, err := a.effectiveProviderFactory()(resolvedProvider, providerCfg.Model, secret)
			if err != nil {
				return err
			}
			agentContext, err := agent.BuildContext(*cfg)
			if err != nil {
				return err
			}
			trace := io.Writer(nil)
			if !jsonOut {
				trace = cmd.ErrOrStderr()
			}
			queryForProvider := query
			var result agent.RunResult
			for turns := 0; turns < 3; turns++ {
				dispatcher := agent.NewToolDispatcher(*cfg, git.New(), cmd.OutOrStdout())
				result, err = provider.Run(cmd.Context(), agent.ProviderRequest{Query: queryForProvider, Context: agentContext, Dispatcher: dispatcher, Trace: trace})
				if err != nil {
					return fmt.Errorf("%s", agent.RedactText(err.Error()))
				}
				if err := agent.ValidatePlan(*cfg, result.Plan); err != nil {
					return fmt.Errorf("%s", agent.RedactText(err.Error()))
				}
				if result.Status == "" {
					result.Status = agent.RunStatusPlanReady
				}
				if result.Status != agent.RunStatusNeedsInput || jsonOut || !a.commandIsTerminal(cmd) {
					break
				}
				answers, err := collectQuestionAnswers(cmd.InOrStdin(), cmd.OutOrStdout(), result)
				if err != nil {
					return err
				}
				queryForProvider = query + "\n\nAdditional answers from inline portal setup:\n" + answers
			}
			executionPlan := result.Plan
			result.Commands = result.Plan.Commands()
			if result.Status == "" {
				result.Status = agent.RunStatusPlanReady
			}
			if result.Status != agent.RunStatusPlanReady {
				result = agent.RedactRunResult(result)
				if jsonOut {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				printAgentPlan(cmd.OutOrStdout(), result)
				return nil
			}
			execute := false
			if incant || cfg.Agent.AutoIncant {
				execute = true
			} else if !noIncant && !jsonOut && a.commandIsTerminal(cmd) {
				printAgentPlan(cmd.OutOrStdout(), result)
				execute, err = confirm(cmd.InOrStdin(), cmd.OutOrStdout())
				if err != nil {
					return err
				}
			}
			if noIncant {
				execute = false
			}
			if execute {
				execOut := cmd.OutOrStdout()
				if jsonOut {
					execOut = io.Discard
				}
				results, err := (agent.Executor{
					Config:           *cfg,
					Git:              git.New(),
					Out:              execOut,
					SummonLauncher:   a.effectiveSummonLauncher(),
					PortalRunner:     a.effectivePortalRunner(),
					AllowInteractive: !jsonOut && a.commandIsTerminal(cmd),
				}).ExecutePlan(cmd.Context(), executionPlan)
				result.Results = results
				result.Executed = true
				if err != nil {
					result = agent.RedactRunResult(result)
					if jsonOut {
						_ = writeJSON(cmd.OutOrStdout(), result)
					}
					return fmt.Errorf("%s", agent.RedactText(err.Error()))
				}
			}
			result = agent.RedactRunResult(result)
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			if !execute || incant || cfg.Agent.AutoIncant {
				printAgentPlan(cmd.OutOrStdout(), result)
			}
			if !execute && !noIncant && !a.commandIsTerminal(cmd) {
				fmt.Fprintln(cmd.OutOrStdout(), "\nNon-interactive terminal detected; no operations were executed. Re-run with --incant or set agent.autoIncant: true to execute.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "agent provider override (openai or anthropic)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().BoolVar(&incant, "incant", false, "execute the validated plan without prompting")
	cmd.Flags().BoolVar(&noIncant, "no-incant", false, "plan only; never execute operations")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	cmd.AddCommand(a.agentConfigureCommand())
	return cmd
}

func (a *app) agentConfigureCommand() *cobra.Command {
	var providerName string
	var model string
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Interactively configure the Stave agent provider and API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			reader := bufio.NewReader(cmd.InOrStdin())
			if providerName == "" {
				providerName, err = promptDefault(reader, cmd.OutOrStdout(), "Provider", firstNonEmpty(cfg.Agent.DefaultProvider, config.DefaultAgentProvider))
				if err != nil {
					return err
				}
			}
			if providerName != agent.ProviderOpenAI && providerName != agent.ProviderAnthropic {
				return fmt.Errorf("provider must be openai or anthropic")
			}
			defaultModel := defaultModelForProvider(providerName)
			if existing := cfg.Agent.Providers[providerName].Model; existing != "" {
				defaultModel = existing
			}
			if model == "" {
				model, err = promptDefault(reader, cmd.OutOrStdout(), "Model", defaultModel)
				if err != nil {
					return err
				}
			}
			store := a.effectiveSecretStore()
			keychain := store.Available()
			apiKeyRef := agent.APIKeyRefForProvider(providerName, keychain)
			if keychain {
				secret, err := readSecret(cmd, reader, fmt.Sprintf("%s API key", providerName))
				if err != nil {
					return err
				}
				if secret == "" {
					return fmt.Errorf("API key is required")
				}
				if err := store.Put(cmd.Context(), apiKeyRef, secret); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Stored %s API key in Keychain as %s\n", providerName, apiKeyRef)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Keychain unavailable; using %s. Set that environment variable before running `stave agent`.\n", apiKeyRef)
			}
			if cfg.Agent.Providers == nil {
				cfg.Agent.Providers = map[string]config.AgentProviderConfig{}
			}
			cfg.Agent.DefaultProvider = providerName
			cfg.Agent.Providers[providerName] = config.AgentProviderConfig{Model: model, APIKeyRef: apiKeyRef}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Configured agent provider %s with model %s\n", providerName, model)
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "provider to configure (openai or anthropic)")
	cmd.Flags().StringVar(&model, "model", "", "model to configure")
	return cmd
}

func (a *app) spaceCommand() *cobra.Command {
	cmd := groupCommand("space", "Manage agent workspaces")
	cmd.AddCommand(
		a.initCommand(),
		a.createCommand(),
		a.addCommand(),
		a.removeCommand(),
		a.syncCommand(),
		a.retargetCommand(),
		a.statusCommand(),
		a.listCommand(),
		a.archiveCommand(),
		a.restoreCommand(),
		a.destroyCommand(),
	)
	return cmd
}

func (a *app) initCommand() *cobra.Command {
	var kind string
	var spec string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "init <space-id>",
		Short: "Create an empty agent workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				if kind == space.KindSaga {
					return nil, argErrorf("kind %q is reserved; use 'stave saga create'", space.KindSaga)
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, false, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.InitSpace(cmd.Context(), space.InitOptions{ID: args[0], Kind: kind, SpecPath: spec}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				return spaceMutationPayload(svc, args[0], sink, fmt.Sprintf("created space %s at %s", args[0], svc.SpacePath(args[0])))
			})
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "space kind, such as ticket, spike, or audit")
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the space")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) createCommand() *cobra.Command {
	var kind string
	var spec string
	var edits []string
	var references []string
	var memories []string
	var sagaID string
	var after []string
	var dryRun bool
	var summonName string
	var common bool
	var includeWeak bool
	var noLearn bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <space-id>",
		Short: "Create a workspace and add edit/reference repos in one command",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Interspersed parsing is off (agent args forward after --), so flags
			// following the positional — --json included — are only known once
			// parsePassthroughArgs has run.
			positionals, agentArgs, parseErr := parsePassthroughArgs(cmd, args, 1, 1, func() bool { return summonName != "" })
			return runJSON(cmd, jsonOut, func() (any, error) {
				if parseErr != nil {
					return nil, parseErr
				}
				if kind == space.KindSaga {
					return nil, argErrorf("kind %q is reserved; use 'stave saga create'", space.KindSaga)
				}
				if jsonOut && summonName != "" {
					return nil, argErrorf("--summon is interactive and cannot be combined with --json")
				}
				spaceID := positionals[0]
				editSpecs, err := parseRepoSpecs(edits)
				if err != nil {
					return nil, err
				}
				refSpecs, err := parseRepoSpecs(references)
				if err != nil {
					return nil, err
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				createOpts := space.CreateOptions{
					ID:         spaceID,
					Kind:       kind,
					SpecPath:   spec,
					Edits:      editSpecs,
					References: refSpecs,
					Memories:   memories,
					NoLearn:    noLearn,
					DryRun:     dryRun,
					SagaID:     sagaID,
					After:      after,
				}
				// -c/--common (and --include-weak) expand the edited repos' learned
				// tethers into extra reference worktrees via the service (D1). The
				// result feeds CommonReferences, not References (OQ-E).
				if common || includeWeak {
					extra, notes, err := svc.ExpandCommonRefs(svc.Config, editSpecs, refSpecs, includeWeak)
					if err != nil {
						return nil, err
					}
					for _, note := range notes {
						fmt.Fprintln(sink.Writer(), note)
					}
					createOpts.CommonReferences = extra
				}
				if err := svc.Create(cmd.Context(), createOpts); err != nil {
					return nil, err
				}
				if jsonOut {
					if dryRun {
						return dryRunPayload(sink), nil
					}
					if err := a.requestShellChdir(svc.SpacePath(spaceID)); err != nil {
						return nil, err
					}
					// Progress lines (the create line, one per repo added) are
					// represented by the payload itself; only notices remain notes.
					primary := []string{fmt.Sprintf("created space %s at %s", spaceID, svc.SpacePath(spaceID))}
					for _, spec := range editSpecs {
						primary = append(primary, fmt.Sprintf("added %s repo %s to %s", space.ModeEdit, spec.Name, spaceID))
					}
					for _, spec := range append(append([]space.RepoSpec{}, refSpecs...), createOpts.CommonReferences...) {
						primary = append(primary, fmt.Sprintf("added %s repo %s to %s", space.ModeReference, spec.Name, spaceID))
					}
					return spaceMutationPayload(svc, spaceID, sink, primary...)
				}
				if summonName == "" {
					if dryRun {
						return nil, nil
					}
					return nil, a.requestShellChdir(svc.SpacePath(spaceID))
				}
				if dryRun {
					return nil, a.printPlannedSummon(cmd, svc.Config, spaceID, summonName, spec, agentArgs)
				}
				if err := a.requestShellChdir(svc.SpacePath(spaceID)); err != nil {
					return nil, err
				}
				return nil, a.runSummon(cmd, svc.Config, spaceID, summonName, "", agentArgs, false)
			})
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "space kind, such as ticket, spike, or audit")
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the space")
	cmd.Flags().StringArrayVarP(&edits, "edit", "e", nil, "editable repo spec, repo[:base]; base may be space:<id> to stack on that space's branch")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "reference repo spec, optionally repo:ref")
	cmd.Flags().StringArrayVar(&memories, "memory", nil, "attach memory: [provider:]<spec>; '.' = fresh task store (repeatable)")
	cmd.Flags().StringVar(&sagaID, "saga", "", "register the new space as a member of this saga")
	cmd.Flags().StringArrayVar(&after, "after", nil, "member id the new space lands behind (requires --saga; repeatable)")
	cmd.Flags().StringVar(&summonName, "summon", "", "launch a summoner after creation (codex, claude, or cursor)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVarP(&common, "common", "c", false, "also add reference worktrees for the edited repos' strong learned tethers")
	cmd.Flags().BoolVar(&includeWeak, "include-weak", false, "with --common, include weak tethers too")
	cmd.Flags().BoolVar(&noLearn, "no-learn", false, "do not record co-occurrence tethers for this create")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func (a *app) summonCommand() *cobra.Command {
	var summoner, prompt string
	var printCommand bool
	cmd := &cobra.Command{
		Use:   "summon <space-id> [agent-flags...]",
		Short: "Launch an interactive agent in a Stave space",
		RunE: func(cmd *cobra.Command, args []string) error {
			positionals, agentArgs, err := parsePassthroughArgs(cmd, args, 1, 1, func() bool { return true })
			if err != nil {
				return err
			}
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			return a.runSummon(cmd, *cfg, positionals[0], summoner, prompt, agentArgs, printCommand)
		},
	}
	cmd.Flags().StringVar(&summoner, "with", "", "summoner to launch (codex, claude, or cursor; defaults to config)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "override the launch prompt (e.g. a skill invocation like \"/pr-teach\")")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "print the launch command instead of running it")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func (a *app) portalCommand() *cobra.Command {
	cmd := groupCommand("portal", "Attach execution environments to Stave spaces")
	cmd.AddCommand(
		a.portalInitCommand(),
		a.portalAttachCommand(),
		a.portalConfigureCommand(),
		a.portalDriversCommand(),
		a.portalDoctorCommand(),
		a.portalListCommand(),
		a.portalStatusCommand(),
		a.portalInspectCommand(),
		a.portalAuthCommand(),
		a.portalUpCommand(),
		a.portalSyncCommand(),
		a.portalShellCommand(),
		a.portalExecCommand(),
		a.portalSummonCommand(),
		a.portalLogsCommand(),
		a.portalDownCommand(),
		a.portalDetachCommand(),
		a.portalDestroyCommand(),
	)
	return cmd
}

// printSagaPortalNote flags, best-effort, that a portal on a saga space covers
// only the saga directory: members are sibling spaces on disk, so they sit
// outside the portal's mounts. Manifest load errors are ignored — the init
// itself surfaces them if they matter.
func printSagaPortalNote(out io.Writer, svc portal.Service, spaceID string) {
	if manifest, err := space.LoadManifest(svc.SpacePath(spaceID)); err == nil && manifest.Saga != nil {
		fmt.Fprintln(out, "note: members are sibling spaces; this portal covers only the saga directory")
	}
}

func (a *app) portalInitCommand() *cobra.Command {
	var preset string
	var yes bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "init <space-id> [portal-id]",
		Short: "Guided portal setup for a space",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			portalID := optionalPortalID(args)
			if strings.HasPrefix(preset, "ssh-") {
				return fmt.Errorf("preset %s attaches an external resource; use `stave portal attach ssh`", preset)
			}
			printSagaPortalNote(cmd.OutOrStdout(), svc, args[0])
			kind := guidedPortalKind(preset)
			reader := bufio.NewReader(cmd.InOrStdin())
			if !yes && !dryRun && a.commandIsTerminal(cmd) && preset == "" {
				kind, err = promptDefault(reader, cmd.OutOrStdout(), "Portal kind (container/devcontainer)", kind)
				if err != nil {
					return err
				}
				if kind != "container" && kind != "devcontainer" {
					return fmt.Errorf("portal kind must be container or devcontainer")
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Equivalent command: %s\n", guidedPortalCommand(args[0], portalID, kind, preset))
			previewPlan, err := guidedPortalPreview(cmd.Context(), svc, args[0], portalID, kind, preset)
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), previewPlan)
			if dryRun {
				return nil
			}
			if !yes && !dryRun && a.commandIsTerminal(cmd) {
				ok, err := confirm(reader, cmd.OutOrStdout())
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
					return nil
				}
			}
			var plan portal.Plan
			if kind == "devcontainer" {
				plan, err = svc.InitDevcontainer(cmd.Context(), portal.InitDevcontainerOptions{SpaceID: args[0], PortalID: portalID, Preset: preset, DryRun: dryRun})
			} else {
				plan, err = svc.InitContainer(cmd.Context(), portal.InitContainerOptions{SpaceID: args[0], PortalID: portalID, Preset: preset, DryRun: dryRun})
			}
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
			return nil
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "", "preset such as local-codex, local-claude, or claude-devcontainer")
	cmd.Flags().BoolVar(&yes, "yes", false, "accept guided defaults")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	cmd.AddCommand(a.portalInitContainerCommand(), a.portalInitDevcontainerCommand())
	return cmd
}

func (a *app) portalInitContainerCommand() *cobra.Command {
	var image, engine, containerRoot, preset string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "container <space-id> [portal-id]",
		Short: "Create local container portal metadata",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			printSagaPortalNote(cmd.OutOrStdout(), svc, args[0])
			plan, err := svc.InitContainer(cmd.Context(), portal.InitContainerOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Engine: portal.Driver(engine), Image: image, ContainerRoot: containerRoot, Preset: preset, DryRun: dryRun})
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "container image")
	cmd.Flags().StringVar(&engine, "engine", "docker", "container engine: docker")
	cmd.Flags().StringVar(&containerRoot, "container-root", "", "space root inside the container")
	cmd.Flags().StringVar(&preset, "preset", "", "preset such as local-codex or local-claude")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalInitDevcontainerCommand() *cobra.Command {
	var path, service, containerRoot, preset string
	var composeFiles []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "devcontainer <space-id> [portal-id]",
		Short: "Create devcontainer portal metadata",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			printSagaPortalNote(cmd.OutOrStdout(), svc, args[0])
			plan, err := svc.InitDevcontainer(cmd.Context(), portal.InitDevcontainerOptions{SpaceID: args[0], PortalID: optionalPortalID(args), DevcontainerPath: path, Service: service, ContainerRoot: containerRoot, ComposeFiles: composeFiles, Preset: preset, DryRun: dryRun})
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "path to devcontainer.json")
	cmd.Flags().StringVar(&service, "service", "", "devcontainer service")
	cmd.Flags().StringArrayVar(&composeFiles, "compose-file", nil, "compose file, repeatable")
	cmd.Flags().StringVar(&containerRoot, "container-root", "", "space root inside the container")
	cmd.Flags().StringVar(&preset, "preset", "", "preset such as claude-devcontainer")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalAttachCommand() *cobra.Command {
	cmd := groupCommand("attach", "Attach existing external resources")
	cmd.AddCommand(a.portalAttachSSHCommand(), a.portalAttachEC2Command())
	return cmd
}

func (a *app) portalAttachSSHCommand() *cobra.Command {
	var remoteRoot, identity, knownHosts, strictHostKey, syncMode, preset string
	var port int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "ssh <space-id> <host> [portal-id]",
		Short: "Attach an existing SSH host",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			portalID := ""
			if len(args) == 3 {
				portalID = args[2]
			}
			err := a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.AttachSSH(cmd.Context(), portal.AttachSSHOptions{SpaceID: args[0], Host: args[1], PortalID: portalID, Port: port, IdentityPath: identity, KnownHostsPath: knownHosts, StrictHostKey: strictHostKey, RemoteRoot: remoteRoot, SyncMode: portal.SyncMode(syncMode), Preset: preset, DryRun: dryRun})
			}, dryRun, false)
			if err != nil {
				// Echo how the positionals were parsed so a portal id
				// mistakenly given as the host is visible in the error.
				return fmt.Errorf("%w (parsed host %q, portal id %q)", err, args[1], firstNonEmpty(portalID, "default"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteRoot, "remote-root", "", "remote space root")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&identity, "identity", "", "SSH identity path")
	cmd.Flags().StringVar(&knownHosts, "known-hosts", "", "SSH known_hosts file")
	cmd.Flags().StringVar(&strictHostKey, "strict-host-key", "", "SSH StrictHostKeyChecking value")
	cmd.Flags().StringVar(&syncMode, "sync", "", "sync mode: rsync or reconstruct")
	cmd.Flags().StringVar(&preset, "preset", "", "preset such as ssh-codex or ssh-claude")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalAttachEC2Command() *cobra.Command {
	var region, profile, sshUser, identity, knownHosts, strictHostKey, remoteRoot, syncMode, preset, host string
	var port int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "ec2 <space-id> <instance-id> [portal-id]",
		Short: "Attach an existing EC2 instance",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			portalID := ""
			if len(args) == 3 {
				portalID = args[2]
			}
			err := a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.AttachEC2(cmd.Context(), portal.AttachEC2Options{SpaceID: args[0], InstanceID: args[1], Host: host, Port: port, PortalID: portalID, Region: region, Profile: profile, SSHUser: sshUser, IdentityPath: identity, KnownHostsPath: knownHosts, StrictHostKey: strictHostKey, RemoteRoot: remoteRoot, SyncMode: portal.SyncMode(syncMode), Preset: preset, DryRun: dryRun})
			}, dryRun, false)
			if err != nil {
				return fmt.Errorf("%w (parsed instance id %q, portal id %q)", err, args[1], firstNonEmpty(portalID, "default"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "AWS region")
	cmd.Flags().StringVar(&profile, "profile", "", "AWS profile")
	cmd.Flags().StringVar(&host, "host", "", "SSH host, DNS name, or IP address; resolved from EC2 metadata when omitted")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH username")
	cmd.Flags().StringVar(&identity, "identity", "", "SSH identity path")
	cmd.Flags().StringVar(&knownHosts, "known-hosts", "", "SSH known_hosts file")
	cmd.Flags().StringVar(&strictHostKey, "strict-host-key", "", "SSH StrictHostKeyChecking value")
	cmd.Flags().StringVar(&remoteRoot, "remote-root", "", "remote space root")
	cmd.Flags().StringVar(&syncMode, "sync", "", "sync mode: rsync or reconstruct")
	cmd.Flags().StringVar(&preset, "preset", "", "preset such as ssh-codex or ssh-claude")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalConfigureCommand() *cobra.Command {
	var syncMode, containerRoot, remoteRoot, host, agentName, authMode string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "configure <space-id> [portal-id]",
		Short: "Tune an existing portal",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			plan, err := svc.Configure(portal.ConfigureOptions{SpaceID: args[0], PortalID: optionalPortalID(args), SyncMode: portal.SyncMode(syncMode), ContainerRoot: containerRoot, RemoteRoot: remoteRoot, Host: host, Agent: agentName, AuthMode: portal.AuthMode(authMode), DryRun: dryRun})
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
			return nil
		},
	}
	cmd.Flags().StringVar(&syncMode, "sync", "", "sync mode")
	cmd.Flags().StringVar(&containerRoot, "container-root", "", "container root")
	cmd.Flags().StringVar(&remoteRoot, "remote-root", "", "remote root")
	cmd.Flags().StringVar(&host, "host", "", "SSH host, DNS name, or IP address for remote portals")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent provider: codex, claude, or cursor")
	cmd.Flags().StringVar(&authMode, "auth", "", "auth mode")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalDriversCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "drivers",
		Short: "List supported portal drivers",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			for _, driver := range svc.Drivers() {
				ownership := "create"
				if driver.AttachOnly {
					ownership = "attach-only"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", driver.Driver, ownership, driver.Binary, driver.Description)
			}
			return nil
		},
	}
}

func (a *app) portalDoctorCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor <space-id> [portal-id]",
		Short: "Run portal preflight diagnostics",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			report, err := svc.Doctor(cmd.Context(), portal.SelectOptions{SpaceID: args[0], PortalID: optionalPortalID(args)})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "portal %s doctor (%s)\n", report.PortalID, report.Overall)
			for _, diagnostic := range report.Diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
				if diagnostic.NextAction != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    next: %s\n", diagnostic.NextAction)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) portalListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [space-id]",
		Short: "List portals",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			spaceID := ""
			if len(args) == 1 {
				spaceID = args[0]
			}
			entries, err := svc.List(spaceID)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), entries)
			}
			for _, entry := range entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", entry.SpaceID, entry.PortalID, entry.Driver, entry.SyncMode, entry.Auth)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) portalStatusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <space-id> [portal-id]",
		Short: "Show portal status",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			status, err := svc.Status(cmd.Context(), portal.SelectOptions{SpaceID: args[0], PortalID: optionalPortalID(args)})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), status)
			}
			if len(args) == 1 {
				if entries, listErr := svc.List(args[0]); listErr == nil && len(entries) > 1 {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: space has %d portals; showing %q (pass a portal id, or run stave portal list %s)\n", len(entries), status.PortalID, args[0])
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "portal %s (%s)\nspace: %s\ndriver: %s\nstate: %s\nhealth: %s\n", status.PortalID, status.Overall, status.SpaceID, status.Driver, status.State, status.Health)
			for _, diagnostic := range status.Diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
				if diagnostic.NextAction != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    next: %s\n", diagnostic.NextAction)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) portalInspectCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "inspect <space-id> [portal-id]",
		Short: "Show portal configuration and ownership",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			report, err := svc.Inspect(cmd.Context(), portal.SelectOptions{SpaceID: args[0], PortalID: optionalPortalID(args)})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "manifest: %s\nportal: %s\ndriver: %s\n", report.ManifestPath, report.Portal.ID, report.Portal.Driver)
			for _, resource := range report.OwnedResources {
				fmt.Fprintf(cmd.OutOrStdout(), "owned: %s\n", resource)
			}
			for _, note := range report.DestroyDryRunNotes {
				fmt.Fprintf(cmd.OutOrStdout(), "destroy-preview: %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) portalAuthCommand() *cobra.Command {
	cmd := groupCommand("auth", "Manage portal target auth")
	cmd.AddCommand(a.portalAuthStatusCommand(), a.portalAuthLoginCommand(), a.portalAuthInheritCommand(), a.portalAuthRevokeCommand())
	return cmd
}

func (a *app) portalAuthStatusCommand() *cobra.Command {
	var provider string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <space-id> [portal-id]",
		Short: "Show portal auth status",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			portalValue, _, err := svc.LoadPortal(portal.SelectOptions{SpaceID: args[0], PortalID: optionalPortalID(args)})
			if err != nil {
				return err
			}
			auth := portalValue.Auth
			if provider != "" && provider != "all" {
				if err := portal.ValidateProviderName(provider); err != nil {
					return err
				}
				filtered := make([]portal.AuthProvider, 0, len(auth.Providers))
				for _, current := range auth.Providers {
					if current.Provider == provider {
						filtered = append(filtered, current)
					}
				}
				if len(filtered) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: provider %q is not configured for this portal\n", provider)
				}
				auth.Providers = filtered
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), auth)
			}
			for _, current := range auth.Providers {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", current.Provider, current.Mode, current.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "all", "provider: codex, claude, cursor, or all")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

func (a *app) portalAuthLoginCommand() *cobra.Command {
	var provider, method string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "login <space-id> [portal-id]",
		Short: "Run provider-native login inside the portal",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanAuthLogin(cmd.Context(), portal.AuthCommandOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Provider: provider, Method: portal.AuthMode(method), DryRun: dryRun})
			}, dryRun, false)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "codex", "provider: codex, claude, or cursor")
	cmd.Flags().StringVar(&method, "method", "", "login method: native or device (remote Codex defaults to device)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalAuthInheritCommand() *cobra.Command {
	var provider, method string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "inherit <space-id> [portal-id]",
		Short: "Explicitly inherit auth into the portal",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanAuthInherit(cmd.Context(), portal.AuthCommandOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Provider: provider, Method: portal.AuthMode(method), DryRun: dryRun, Yes: yes})
			}, dryRun, false)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "codex", "provider: codex, claude, or cursor")
	cmd.Flags().StringVar(&method, "method", "", "inherit method: env, volume, or ssh-forward")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm explicit inherit behavior")
	return cmd
}

func (a *app) portalAuthRevokeCommand() *cobra.Command {
	var provider, target string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "revoke <space-id> [portal-id]",
		Short: "Revoke target-local portal auth",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanAuthRevoke(cmd.Context(), portal.AuthCommandOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Provider: provider, Target: target, DryRun: dryRun, Yes: yes})
			}, dryRun, false)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "codex", "provider: codex, claude, or cursor")
	cmd.Flags().StringVar(&target, "target", "portal", "target: local, portal, or all")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm revocation")
	return cmd
}

func (a *app) portalUpCommand() *cobra.Command {
	var attach, workdir string
	var printCommand, dryRun bool
	cmd := &cobra.Command{
		Use:   "up <space-id> [portal-id]",
		Short: "Start or validate portal runtime",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanUp(cmd.Context(), portal.UpOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Attach: attach, Workdir: workdir, DryRun: dryRun || printCommand})
			}, dryRun, printCommand)
		},
	}
	cmd.Flags().StringVar(&attach, "attach", "none", "attach mode: shell or none")
	cmd.Flags().StringVar(&workdir, "workdir", "", "portal working directory")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalSyncCommand() *cobra.Command {
	var direction, mode string
	var includes, excludes []string
	var referencesOnly, deleteFiles, allowDirty, dryRun, printCommand, yes bool
	var maxDelete int
	cmd := &cobra.Command{
		Use:   "sync <space-id> [portal-id]",
		Short: "Sync workspace files with the portal target",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanSync(cmd.Context(), portal.SyncOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Direction: portal.SyncDirection(direction), Mode: portal.SyncMode(mode), ReferencesOnly: referencesOnly, Include: includes, Exclude: excludes, Delete: deleteFiles, MaxDelete: maxDelete, AllowDirty: allowDirty, DryRun: dryRun || printCommand, Yes: yes})
			}, dryRun, printCommand)
		},
	}
	cmd.Flags().StringVar(&direction, "direction", "to", "sync direction: to, from, or both")
	cmd.Flags().StringVar(&mode, "mode", "", "sync mode: auto, mount, rsync, or reconstruct")
	cmd.Flags().BoolVar(&referencesOnly, "references-only", false, "sync only references")
	cmd.Flags().StringArrayVar(&includes, "include", nil, "include pattern")
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "exclude pattern")
	cmd.Flags().BoolVar(&deleteFiles, "delete", false, "delete files missing from source")
	cmd.Flags().IntVar(&maxDelete, "max-delete", 0, "maximum deletions before rsync stops")
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "allow destructive pull with dirty edits")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm sync")
	return cmd
}

func (a *app) portalShellCommand() *cobra.Command {
	var cwd, user, ttyMode string
	var printCommand, dryRun bool
	cmd := &cobra.Command{
		Use:   "shell <space-id> [portal-id]",
		Short: "Open a shell in the portal",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !printCommand && !a.commandIsTerminal(cmd) {
				printCommand = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Non-interactive terminal detected; printing shell command instead of launching.")
			}
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanShell(cmd.Context(), portal.ShellOptions{SpaceID: args[0], PortalID: optionalPortalID(args), CWD: cwd, User: user, TTY: portal.TTYMode(ttyMode), DryRun: dryRun || printCommand})
			}, dryRun, printCommand)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory inside portal")
	cmd.Flags().StringVar(&user, "user", "", "user inside portal")
	cmd.Flags().StringVar(&ttyMode, "tty", "auto", "tty mode: auto, always, or never")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalExecCommand() *cobra.Command {
	var cwd, user, ttyMode string
	var dryRun, printCommand bool
	cmd := &cobra.Command{
		Use:   "exec <space-id> [portal-id] [flags] -- <command...>",
		Short: "Run a command in the portal",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spaceID, portalID, argv, err := splitPortalExecArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return err
			}
			preview := dryRun || printCommand
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanExec(cmd.Context(), portal.ExecOptions{SpaceID: spaceID, PortalID: portalID, Command: argv, CWD: cwd, User: user, TTY: portal.TTYMode(ttyMode), DryRun: preview})
			}, preview, false)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory inside portal")
	cmd.Flags().StringVar(&user, "user", "", "user inside portal")
	cmd.Flags().StringVar(&ttyMode, "tty", "never", "tty mode: auto, always, or never")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalSummonCommand() *cobra.Command {
	var with, mode, permission, prompt string
	var printCommand, dryRun bool
	cmd := &cobra.Command{
		Use:   "summon <space-id> [portal-id]",
		Short: "Launch a coding agent inside the portal",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if mode == "print" {
				printCommand = true
			}
			// A detached `tmux new-session -d` needs no TTY and the blocking
			// attach stays gated off-TTY (planning.go), so `--mode tmux` should
			// run the detached create off-TTY rather than degrade to a preview.
			if !printCommand && !a.commandIsTerminal(cmd) && mode != "tmux" {
				printCommand = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Non-interactive terminal detected; printing summon command instead of launching.")
			}
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanSummon(cmd.Context(), portal.SummonOptions{SpaceID: args[0], PortalID: optionalPortalID(args), With: with, Mode: mode, Permission: permission, Prompt: prompt, DryRun: dryRun || printCommand})
			}, dryRun, printCommand)
		},
	}
	cmd.Flags().StringVar(&with, "with", "", "summoner: codex, claude, or cursor (defaults to codex)")
	cmd.Flags().StringVar(&mode, "mode", "foreground", "mode: foreground, tmux, headless, or print")
	cmd.Flags().StringVar(&permission, "permission", "workspace-write", "permission for headless codex runs: read-only or workspace-write")
	cmd.Flags().StringVar(&prompt, "prompt", "", "override the launch prompt (e.g. a skill invocation like \"/pr-teach\")")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalLogsCommand() *cobra.Command {
	var follow, dryRun, printCommand bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <space-id> [portal-id]",
		Short: "Show portal runtime or agent logs",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			preview := dryRun || printCommand
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanLogs(cmd.Context(), portal.LogsOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Follow: follow, Tail: tail, DryRun: preview})
			}, preview, false)
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "follow logs")
	cmd.Flags().IntVar(&tail, "tail", 100, "number of lines")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalDownCommand() *cobra.Command {
	var timeout int
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:   "down <space-id> [portal-id]",
		Short: "Stop a Stave-owned portal runtime",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanDown(cmd.Context(), portal.DownOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Timeout: timeout, Force: force, DryRun: dryRun})
			}, dryRun, false)
		},
	}
	cmd.Flags().IntVar(&timeout, "timeout", 0, "graceful stop timeout in seconds")
	cmd.Flags().BoolVar(&force, "force", false, "force stop")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) portalDetachCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "detach <space-id> [portal-id]",
		Short: "Remove attach-only portal metadata",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.portalService(cmd)
			if err != nil {
				return err
			}
			plan, err := svc.Detach(portal.DetachOptions{SpaceID: args[0], PortalID: optionalPortalID(args), DryRun: dryRun})
			if err != nil {
				return err
			}
			printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing")
	return cmd
}

func (a *app) portalDestroyCommand() *cobra.Command {
	var timeout int
	var deleteVolumes, force, dryRun bool
	cmd := &cobra.Command{
		Use:   "destroy <space-id> [portal-id]",
		Short: "Destroy Stave-owned portal runtime resources",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanDestroy(cmd.Context(), portal.DestroyOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Timeout: timeout, DeleteVolumes: deleteVolumes, Force: force, DryRun: dryRun})
			}, dryRun, false)
		},
	}
	cmd.Flags().IntVar(&timeout, "timeout", 0, "graceful stop timeout in seconds")
	cmd.Flags().BoolVar(&deleteVolumes, "delete-volumes", false, "delete recorded Stave-owned volumes")
	cmd.Flags().BoolVar(&force, "force", false, "force destroy")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the commands without running them")
	return cmd
}

func (a *app) addCommand() *cobra.Command {
	var edit bool
	var reference bool
	var base string
	var branch string
	var noFetch bool
	var dryRun bool
	var linkMemory bool
	var noLearn bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <space-id> <repo>",
		Short: "Add an editable or reference repository worktree to a space",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				if edit == reference {
					return nil, argErrorf("choose exactly one of --edit or --reference")
				}
				mode := space.ModeEdit
				ref := ""
				if reference {
					mode = space.ModeReference
					ref = base
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.AddRepo(cmd.Context(), space.AddOptions{
					SpaceID:      args[0],
					RepoName:     args[1],
					Mode:         mode,
					Base:         base,
					Ref:          ref,
					Branch:       branch,
					NoFetch:      noFetch,
					DryRun:       dryRun,
					LinkMemory:   linkMemory,
					CaptureOnAdd: true,
					NoLearn:      noLearn,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return spaceMutationPayload(svc, args[0], sink, fmt.Sprintf("added %s repo %s to %s", mode, args[1], args[0]))
			})
		},
	}
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "add as an editable top-level worktree")
	cmd.Flags().BoolVarP(&reference, "reference", "r", false, "add as a detached reference worktree under references/")
	cmd.Flags().StringVarP(&base, "base", "b", "", "base branch/ref for edit repos (may be space:<id> to stack on that space's branch), or ref for reference repos")
	cmd.Flags().StringVar(&branch, "branch", "", "branch name for editable repos")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip fetching the bare repo before adding the worktree")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&linkMemory, "link-memory", true, "resolve an added reference repo into a read-only memory link when the space has memory attached")
	cmd.Flags().BoolVar(&noLearn, "no-learn", false, "do not record co-occurrence tethers for this add")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) syncCommand() *cobra.Command {
	var referencesOnly bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync <space-id>",
		Short: "Fetch a space's repos, update references, and report editable drift",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, false, sink.Writer())
				if err != nil {
					return nil, err
				}
				report, err := svc.SyncWithReport(cmd.Context(), space.SyncOptions{SpaceID: args[0], ReferencesOnly: referencesOnly})
				if err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				return spaceSyncPayload(report, sink), nil
			})
		},
	}
	cmd.Flags().BoolVar(&referencesOnly, "references-only", false, "only sync reference worktrees")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) retargetCommand() *cobra.Command {
	var repoName string
	var base string
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "retarget <space-id>",
		Short: "Update the base ref an edit repo reports drift against",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				if repoName == "" {
					return nil, argErrorf("--repo is required")
				}
				if base == "" {
					return nil, argErrorf("--base is required")
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.Retarget(cmd.Context(), args[0], repoName, base, dryRun); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return spaceRetargetPayload(svc, args[0], repoName, sink)
			})
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "edit repo whose recorded base to update")
	cmd.Flags().StringVarP(&base, "base", "b", "", "new base ref; base may be space:<id> to stack on that space's branch")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

// spaceStatusJSON is the typed `space status --json` document: the manifest
// verbatim plus one probed row per repo and per memory attachment.
type spaceStatusJSON struct {
	SpaceID   string                  `json:"spaceId"`
	SpacePath string                  `json:"spacePath"`
	Manifest  space.Manifest          `json:"manifest"`
	Repos     []spaceRepoStatusJSON   `json:"repos"`
	Memories  []spaceMemoryStatusJSON `json:"memories"`
}

// spaceRepoStatusJSON is one repo row of `space status --json`. Path is
// absolute (space path joined with the manifest's relative path), matching
// the human output. Ahead/Behind are only probed for edit repos.
type spaceRepoStatusJSON struct {
	Name          string         `json:"name"`
	Mode          space.RepoMode `json:"mode"`
	Path          string         `json:"path"`
	Branch        string         `json:"branch,omitempty"`
	Base          string         `json:"base,omitempty"`
	Ref           string         `json:"ref,omitempty"`
	Exists        bool           `json:"exists"`
	Dirty         bool           `json:"dirty"`
	DirtyOutput   string         `json:"dirtyOutput,omitempty"`
	Ahead         int            `json:"ahead"`
	Behind        int            `json:"behind"`
	DriftError    string         `json:"driftError,omitempty"`
	ReferenceWarn string         `json:"referenceWarn,omitempty"`
}

// spaceMemoryStatusJSON is one memory row of `space status --json`. State is
// the provider's compact link-freshness text (e.g. "2 unpushed", "stale")
// with the human suffix decoration stripped; empty when the probe failed.
type spaceMemoryStatusJSON struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Owned    bool   `json:"owned"`
	State    string `json:"state,omitempty"`
}

func (a *app) statusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <space-id>",
		Short: "Show workspace manifest, dirty state, and editable drift",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			status, err := svc.Status(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// S4 skew intelligence: each memory row gains a compact state
			// suffix (e.g. " (2 unpushed)", " (stale)"); provider failures
			// degrade to no suffix.
			memStates := map[string]string{}
			for _, mem := range status.Manifest.Memories {
				memStates[mem.Name] = svc.MemoryStateSuffix(cmd.Context(), mem)
			}
			spacePath := svc.SpacePath(args[0])
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), buildSpaceStatusJSON(args[0], spacePath, status, memStates))
			}
			printStatus(cmd.OutOrStdout(), spacePath, status, memStates)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

// buildSpaceStatusJSON projects the service Status (plus the per-memory state
// suffixes the human renderer uses) into the typed --json document.
func buildSpaceStatusJSON(spaceID, spacePath string, status space.Status, memStates map[string]string) spaceStatusJSON {
	doc := spaceStatusJSON{
		SpaceID:   spaceID,
		SpacePath: spacePath,
		Manifest:  status.Manifest,
		Repos:     make([]spaceRepoStatusJSON, 0, len(status.Repos)),
		Memories:  make([]spaceMemoryStatusJSON, 0, len(status.Manifest.Memories)),
	}
	for _, repo := range status.Repos {
		doc.Repos = append(doc.Repos, spaceRepoStatusJSON{
			Name:          repo.Repo.Name,
			Mode:          repo.Repo.Mode,
			Path:          filepath.Join(spacePath, repo.Repo.Path),
			Branch:        repo.Repo.Branch,
			Base:          repo.Repo.Base,
			Ref:           repo.Repo.Ref,
			Exists:        repo.Exists,
			Dirty:         repo.Dirty,
			DirtyOutput:   repo.DirtyOutput,
			Ahead:         repo.Ahead,
			Behind:        repo.Behind,
			DriftError:    repo.DriftError,
			ReferenceWarn: repo.ReferenceWarn,
		})
	}
	for _, mem := range status.Manifest.Memories {
		doc.Memories = append(doc.Memories, spaceMemoryStatusJSON{
			Name:     mem.Name,
			Provider: mem.Provider,
			ID:       mem.ID,
			Owned:    mem.Owned,
			State:    trimMemoryStateSuffix(memStates[mem.Name]),
		})
	}
	return doc
}

// trimMemoryStateSuffix turns the human row suffix " (2 unpushed)" into the
// bare state text "2 unpushed".
func trimMemoryStateSuffix(suffix string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(suffix), "("), ")"))
}

func (a *app) archiveCommand() *cobra.Command {
	var force bool
	var dryRun bool
	var memoryFate string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "archive <space-id>",
		Short: "Remove worktrees and preserve space metadata/notes under .archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				fate, err := parseMemoryFateArg(memoryFate)
				if err != nil {
					return nil, err
				}
				if fate == memory.FateDestroy {
					return nil, argErrorf("--memory destroy is not valid for archive; use 'stave space destroy --memory destroy' to destroy owned memory")
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if !force {
					if err := sagaLifecycleGuard(svc, args[0]); err != nil {
						return nil, err
					}
				}
				var before map[string]bool
				if jsonOut {
					before = archiveEntries(svc, args[0])
				}
				if err := svc.Archive(cmd.Context(), space.ArchiveOptions{
					SpaceID:    args[0],
					Force:      force,
					DryRun:     dryRun,
					MemoryFate: fate,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				dest := archivedPathAfter(svc, args[0], before)
				return spaceArchiveJSON{
					SpaceID:      args[0],
					ArchivedPath: dest,
					Memory:       string(fate),
					Notes:        sink.Notes(fmt.Sprintf("archived %s to %s", args[0], dest)),
				}, nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "archive even when editable worktrees are dirty or other spaces stack on this space's branches")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().StringVar(&memoryFate, "memory", string(memory.FateKeep), "memory fate on archive: keep or contribute (contribute-then-keep; destroy is not allowed)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) destroyCommand() *cobra.Command {
	var force bool
	var dryRun bool
	var memoryFate string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "destroy <space-id>",
		Short: "Remove a space's worktrees and delete its directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				fate, err := parseMemoryFateArg(memoryFate)
				if err != nil {
					return nil, err
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if !force {
					if err := sagaLifecycleGuard(svc, args[0]); err != nil {
						return nil, err
					}
				}
				if err := svc.Destroy(cmd.Context(), space.DestroyOptions{
					SpaceID:    args[0],
					Force:      force,
					DryRun:     dryRun,
					MemoryFate: fate,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return spaceDestroyJSON{
					SpaceID:   args[0],
					SpacePath: svc.SpacePath(args[0]),
					Destroyed: true,
					Memory:    string(fate),
					Notes:     sink.Notes("destroyed " + args[0]),
				}, nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "destroy even when editable worktrees are dirty or other spaces stack on this space's branches")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().StringVar(&memoryFate, "memory", string(memory.FateKeep), "owned memory fate: keep, destroy, or contribute (default keep)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) memoryCommand() *cobra.Command {
	cmd := groupCommand("memory", "Manage provider-agnostic space memory attachments")
	cmd.AddCommand(
		a.memoryProvidersCommand(),
		a.memoryAttachCommand(),
		a.memoryStatusCommand(),
		a.memoryListCommand(),
		a.memorySyncCommand(),
		a.memoryProposeCommand(),
		a.memoryDetachCommand(),
	)
	return cmd
}

func (a *app) memoryProvidersCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List registered memory providers and capability-probe results",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				cfg, _, err := a.loadConfig()
				if err != nil {
					return nil, err
				}
				mc := cfg.Memory
				mc.ApplyDefaults()
				if jsonOut {
					return memoryProvidersPayload(cmd.Context(), mc), nil
				}
				for _, name := range memory.Names() {
					prov, err := memory.Lookup(name, mc)
					if err != nil {
						fmt.Fprintf(cmd.OutOrStdout(), "%s\terror: %v\n", name, err)
						continue
					}
					probe, probeErr := prov.Probe(cmd.Context())
					status := "ok"
					if probeErr != nil {
						status = probeErr.Error()
					} else if !probe.Capable {
						status = probe.Message
					} else if probe.Message != "" {
						status = probe.Message
					}
					marker := ""
					if name == mc.Provider {
						marker = " (default)"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s%s\t%s\n", name, marker, status)
				}
				return nil, nil
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ([{name, binary, default, available, version, capabilities[], error}])")
	return cmd
}

func (a *app) memoryAttachCommand() *cobra.Command {
	var provider string
	var useID string
	var name string
	var editRefs []string
	var linkRefs []string
	var opts []string
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "attach <space-id>",
		Short: "Attach a memory store to a space (create task store or --use existing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				optMap := map[string]string{}
				for _, raw := range opts {
					k, v, ok := strings.Cut(raw, "=")
					if !ok || k == "" {
						return nil, argErrorf("invalid --opt %q (want k=v)", raw)
					}
					optMap[k] = v
				}
				sink := newOutputSink(cmd, jsonOut)
				rec := &memory.Recording{}
				svc, err := a.memoryJSONService(cmd, dryRun, sink, rec)
				if err != nil {
					return nil, err
				}
				var before []space.MemoryManifest
				if jsonOut && !dryRun {
					if manifest, err := space.LoadManifest(svc.SpacePath(args[0])); err == nil {
						before = manifest.Memories
					}
				}
				if err := svc.AttachMemory(cmd.Context(), space.AttachMemoryOptions{
					SpaceID:  args[0],
					Provider: provider,
					UseID:    useID,
					Name:     name,
					EditRefs: editRefs,
					LinkRefs: linkRefs,
					Opts:     optMap,
					DryRun:   dryRun,
					Strict:   true,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return memoryAttachPayload(svc, args[0], before, rec, sink)
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "memory provider (default from config memory.provider)")
	cmd.Flags().StringVar(&useID, "use", "", "attach an existing durable store id instead of creating a task store")
	cmd.Flags().StringVar(&name, "name", "", "attachment alias (default \"default\")")
	cmd.Flags().StringArrayVar(&editRefs, "edit", nil, "provider edit ref (repeatable, passed through)")
	cmd.Flags().StringArrayVar(&linkRefs, "link", nil, "provider link ref (repeatable, passed through)")
	cmd.Flags().StringArrayVar(&opts, "opt", nil, "provider-specific k=v option (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print exact provider commands without invoking them")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) memoryStatusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <space-id> [alias]",
		Short: "Show memory attachment status (provider, id, freshness)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				alias := ""
				if len(args) > 1 {
					alias = args[1]
				}
				sink := newOutputSink(cmd, jsonOut)
				rec := &memory.Recording{}
				svc, err := a.memoryJSONService(cmd, false, sink, rec)
				if err != nil {
					return nil, err
				}
				if err := svc.MemoryStatus(cmd.Context(), args[0], alias); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				manifest, err := space.LoadManifest(svc.SpacePath(args[0]))
				if err != nil {
					return nil, err
				}
				mc := svc.Config.Memory
				mc.ApplyDefaults()
				lookup := func(provider string) error {
					_, err := memory.Lookup(provider, mc)
					return err
				}
				return memoryStatusPayload(args[0], memoryStatusTargets(manifest, alias), rec, lookup), nil
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ({spaceId, attachments[{name, provider, id, owned, state?, links[]}]})")
	return cmd
}

func (a *app) memoryListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [space-id]",
		Short: "List memory attachments for a space or all spaces",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				svc, err := a.service(cmd)
				if err != nil {
					return nil, err
				}
				spaceID := ""
				if len(args) == 1 {
					spaceID = args[0]
				}
				if jsonOut {
					return memoryListPayload(svc, spaceID)
				}
				return nil, svc.ListMemories(spaceID)
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ([{spaceId, spacePath, attachments[]}])")
	return cmd
}

func (a *app) memorySyncCommand() *cobra.Command {
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync <space-id> [alias]",
		Short: "Sync memory provider state (e.g. warren sync + skew re-report)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				alias := ""
				if len(args) > 1 {
					alias = args[1]
				}
				sink := newOutputSink(cmd, jsonOut)
				rec := &memory.Recording{}
				svc, err := a.memoryJSONService(cmd, dryRun, sink, rec)
				if err != nil {
					return nil, err
				}
				if err := svc.SyncMemory(cmd.Context(), args[0], alias, dryRun); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return memorySyncPayload(args[0], memoryAliasFor(svc, args[0], alias), rec, sink), nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without invoking the provider")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ({spaceId, results[{alias, warren?, outcome, detail?}]})")
	return cmd
}

func (a *app) memoryProposeCommand() *cobra.Command {
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "propose <space-id> [alias]",
		Short: "Flow task learnings back (den contribute + warren propose; never auto-pushes)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				alias := ""
				if len(args) > 1 {
					alias = args[1]
				}
				sink := newOutputSink(cmd, jsonOut)
				rec := &memory.Recording{}
				svc, err := a.memoryJSONService(cmd, dryRun, sink, rec)
				if err != nil {
					return nil, err
				}
				if err := svc.ProposeMemory(cmd.Context(), args[0], alias, dryRun); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return memoryProposePayload(args[0], memoryAliasFor(svc, args[0], alias), rec, sink), nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print contribute/propose commands without invoking them")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ({spaceId, results[{alias, outcome, detail?, pushCommand?}]})")
	return cmd
}

// memoryAliasFor resolves the attachment alias a sync/propose ran against:
// the alias given, or the single attachment's name when none was.
func memoryAliasFor(svc space.Service, spaceID, alias string) string {
	manifest, err := space.LoadManifest(svc.SpacePath(spaceID))
	if err != nil {
		return alias
	}
	if mem, _, ok := manifest.FindMemory(alias); ok {
		return mem.Name
	}
	return alias
}

func (a *app) memoryDetachCommand() *cobra.Command {
	var keep bool
	var destroy bool
	var force bool
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "detach <space-id> [alias]",
		Short: "Detach a memory attachment (--keep default, or --destroy)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				if keep && destroy {
					return nil, argErrorf("specify only one of --keep or --destroy")
				}
				fate := memory.FateKeep
				if destroy {
					fate = memory.FateDestroy
				}
				alias := ""
				if len(args) > 1 {
					alias = args[1]
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.memoryJSONService(cmd, dryRun, sink, nil)
				if err != nil {
					return nil, err
				}
				var target space.MemoryManifest
				if jsonOut && !dryRun {
					if manifest, err := space.LoadManifest(svc.SpacePath(args[0])); err == nil {
						target, _, _ = manifest.FindMemory(alias)
					}
				}
				if err := svc.DetachMemory(cmd.Context(), args[0], alias, fate, force, dryRun); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return memoryDetachPayload(svc, args[0], target, fate, sink)
			})
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "leave the store intact (default)")
	cmd.Flags().BoolVar(&destroy, "destroy", false, "destroy an owned store on detach")
	cmd.Flags().BoolVar(&force, "force", false, "forward force to provider destroy (unpushed edits)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) service(cmd *cobra.Command) (space.Service, error) {
	return a.serviceWithDryRun(cmd, false)
}

func (a *app) serviceWithDryRun(cmd *cobra.Command, dryRun bool) (space.Service, error) {
	return a.serviceWithOutput(cmd, dryRun, cmd.OutOrStdout())
}

// serviceWithOutput builds the service with its notices and dry-run plan
// routed to out: the command's stdout, or a --json capture buffer.
func (a *app) serviceWithOutput(cmd *cobra.Command, dryRun bool, out io.Writer) (space.Service, error) {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return space.Service{}, err
	}
	if !dryRun {
		if err := cfg.EnsureRootDirs(); err != nil {
			return space.Service{}, err
		}
	}
	client := git.New(git.WithDryRun(dryRun, func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	}))
	svc := space.NewService(*cfg, client, out)
	// Layer-2 merge probe: live PR state through the real gh binary (nil
	// ghRunner = gh.ExecRunner), wired ONLY at the CLI layer. Clone URLs
	// that are not GitHub-shaped (local paths, other forges) skip the probe
	// silently without touching gh; a missing or failing gh degrades inside
	// the merge detector to ancestry only.
	ghClient := &gh.Client{Runner: a.ghRunner}
	svc.PRLookup = func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error) {
		host, owner, repo, ok := gh.ParseOwnerRepo(cloneURL)
		if !ok {
			return nil, nil
		}
		return ghClient.ListPRsByHead(ctx, host+"/"+owner+"/"+repo, headBranch)
	}
	// Portal notice for memory attach (local-summon-only v1).
	svc.HasPortal = func(spaceID string) bool {
		manifest, err := portal.LoadManifest(svc.SpacePath(spaceID))
		if err != nil {
			return false
		}
		return len(manifest.Portals) > 0
	}
	return svc, nil
}

func (a *app) portalService(cmd *cobra.Command) (portal.Service, error) {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return portal.Service{}, err
	}
	svc := portal.NewService(*cfg, a.effectivePortalRunner(), nil)
	svc.IsTerminal = func() bool { return a.commandIsTerminal(cmd) }
	return svc, nil
}

func (a *app) runPortalPlanningCommand(cmd *cobra.Command, build func(portal.Service) (portal.Plan, error), dryRun bool, printOnly bool) error {
	svc, err := a.portalService(cmd)
	if err != nil {
		return err
	}
	plan, err := build(svc)
	if err != nil {
		return err
	}
	if dryRun || printOnly || len(plan.Commands) == 0 {
		printPortalPlan(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan)
	}
	if dryRun || printOnly || len(plan.Commands) == 0 {
		return nil
	}
	// On the execution path the preview plan (with its diagnostics) is not
	// printed, so surface warn/error diagnostics (e.g. summon.cursor_partial)
	// to stderr before running the commands. Info-severity notes stay quiet.
	printPortalPlanWarnings(cmd.ErrOrStderr(), plan)
	previousFailed := false
	var suppressed []string
	for _, command := range plan.Commands {
		if command.RunIfPreviousFailed && !previousFailed {
			continue
		}
		command.Stream = !command.ContinueOnError
		result, err := svc.Runner.Run(cmd.Context(), command)
		if err != nil {
			previousFailed = true
			if command.ContinueOnError {
				// Hold the output back: it is noise when the fallback
				// succeeds, but essential context when it fails too.
				if output := strings.TrimSpace(strings.Join(nonEmptyStrings(result.Stderr, result.Stdout), "\n")); output != "" {
					suppressed = append(suppressed, fmt.Sprintf("%s: %s", command.String(), output))
				}
				continue
			}
			if len(suppressed) > 0 {
				result.Stderr = strings.Join(append(suppressed, result.Stderr), "\n")
			}
			printed := command.Stream || printPortalRunResult(cmd, result)
			return portalCommandError(command, result, err, printed)
		}
		printPortalRunResult(cmd, result)
		previousFailed = false
	}
	// Lifecycle commands otherwise emit only raw subprocess output (a bare
	// container id or name), which reads the same for start, stop, and
	// destroy; say what actually happened.
	switch plan.Operation {
	case "up", "down", "destroy":
		fmt.Fprintf(cmd.OutOrStdout(), "ok: %s\n", plan.Summary)
	}
	return nil
}

type portalRunError struct {
	command  portal.Command
	err      error
	exitCode int
	silent   bool
}

func (e portalRunError) Error() string {
	return fmt.Sprintf("portal command failed: %s\n%v", e.command.String(), e.err)
}

func (e portalRunError) Unwrap() error {
	return e.err
}

func (e portalRunError) ExitCode() int {
	if e.exitCode > 0 {
		return e.exitCode
	}
	return 1
}

func (e portalRunError) Silent() bool {
	return e.silent
}

func portalCommandError(command portal.Command, result portal.RunResult, err error, printed bool) error {
	output := strings.TrimSpace(strings.Join(nonEmptyStrings(result.Stderr, result.Stdout), "\n"))
	if output == "" {
		return portalRunError{command: command, err: err, exitCode: result.ExitCode, silent: printed}
	}
	return portalRunError{command: command, err: fmt.Errorf("%w\n%s", err, output), exitCode: result.ExitCode, silent: printed}
}

func printPortalRunResult(cmd *cobra.Command, result portal.RunResult) bool {
	printed := false
	if result.Stdout != "" {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		printed = true
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), result.Stderr)
		printed = true
	}
	return printed
}

func nonEmptyStrings(values ...string) []string {
	var out []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func printPortalPlan(out io.Writer, errOut io.Writer, plan portal.Plan) {
	if plan.Summary != "" {
		fmt.Fprintf(out, "Plan: %s\n", plan.Summary)
	}
	if len(plan.Commands) > 0 {
		fmt.Fprintln(out, "Commands:")
		for _, command := range plan.EquivalentCommands() {
			fmt.Fprintf(out, "  %s\n", command)
		}
	}
	if plan.ManifestPreview != "" {
		fmt.Fprintln(out, "Manifest preview:")
		_, _ = fmt.Fprint(out, plan.ManifestPreview)
		if !strings.HasSuffix(plan.ManifestPreview, "\n") {
			fmt.Fprintln(out)
		}
	}
	// Diagnostics are advisory, not part of the copy-pasteable preview, so
	// they go to stderr where they cannot pollute script-captured stdout.
	for _, diagnostic := range plan.Diagnostics {
		printPortalDiagnostic(errOut, diagnostic)
	}
}

// printPortalPlanWarnings surfaces only warn- and error-severity diagnostics.
// It is used on the execution path, where the full plan preview (and its
// info-severity notes) is not printed but actionable warnings must still reach
// the user.
func printPortalPlanWarnings(errOut io.Writer, plan portal.Plan) {
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Severity == portal.SeverityWarn || diagnostic.Severity == portal.SeverityError {
			printPortalDiagnostic(errOut, diagnostic)
		}
	}
}

func printPortalDiagnostic(errOut io.Writer, diagnostic portal.Diagnostic) {
	fmt.Fprintf(errOut, "[%s] %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
	if diagnostic.NextAction != "" {
		fmt.Fprintf(errOut, "  next: %s\n", diagnostic.NextAction)
	}
}

func optionalPortalID(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	return ""
}

func guidedPortalKind(preset string) string {
	if preset == "claude-devcontainer" {
		return "devcontainer"
	}
	return "container"
}

func guidedPortalCommand(spaceID, portalID, kind, preset string) string {
	parts := []string{"stave", "portal", "init", kind, spaceID}
	if portalID != "" {
		parts = append(parts, portalID)
	}
	if preset != "" {
		parts = append(parts, "--preset", preset)
	}
	return strings.Join(parts, " ")
}

func guidedPortalPreview(ctx context.Context, svc portal.Service, spaceID, portalID, kind, preset string) (portal.Plan, error) {
	if kind == "devcontainer" {
		return svc.InitDevcontainer(ctx, portal.InitDevcontainerOptions{SpaceID: spaceID, PortalID: portalID, Preset: preset, DryRun: true})
	}
	return svc.InitContainer(ctx, portal.InitContainerOptions{SpaceID: spaceID, PortalID: portalID, Preset: preset, DryRun: true})
}

// splitPortalExecArgs separates "space-id [portal-id]" from the command argv.
// The "--" separator is required: without it, a typo'd portal id would
// silently become the command executed inside the default portal.
func splitPortalExecArgs(args []string, argsLenAtDash int) (string, string, []string, error) {
	if argsLenAtDash < 0 || argsLenAtDash > len(args) {
		return "", "", nil, fmt.Errorf("portal exec requires \"--\" before the command, e.g. stave portal exec <space-id> [portal-id] -- <command...>")
	}
	head := args[:argsLenAtDash]
	if len(head) == 0 || len(head) > 2 {
		return "", "", nil, fmt.Errorf("portal exec takes <space-id> [portal-id] before \"--\", got %d arguments", len(head))
	}
	argv := args[argsLenAtDash:]
	if len(argv) == 0 {
		return "", "", nil, fmt.Errorf("portal exec requires a command after \"--\"")
	}
	portalID := ""
	if len(head) > 1 {
		portalID = head[1]
	}
	return head[0], portalID, argv, nil
}

func (a *app) runSummon(cmd *cobra.Command, cfg config.Config, spaceID string, summoner string, prompt string, agentArgs []string, printCommand bool) error {
	svc := summon.NewService(cfg, a.effectiveSummonLauncher(), cmd.OutOrStdout())
	svc.Interactive = a.commandIsTerminal(cmd)
	if !printCommand && !svc.Interactive {
		fmt.Fprintln(cmd.ErrOrStderr(), "Non-interactive terminal detected; printing summon command instead of launching.")
	}
	return svc.Summon(cmd.Context(), summon.Options{SpaceID: spaceID, Summoner: summoner, Prompt: prompt, AgentArgs: agentArgs, PrintCommand: printCommand})
}

func (a *app) printPlannedSummon(cmd *cobra.Command, cfg config.Config, spaceID string, summoner string, specPath string, agentArgs []string) error {
	return a.printPlannedSummonWithPrompt(cmd, cfg, spaceID, summoner, specPath, agentArgs, "")
}

func (a *app) printPlannedSummonWithPrompt(cmd *cobra.Command, cfg config.Config, spaceID string, summoner string, specPath string, agentArgs []string, prompt string) error {
	spacePath := filepath.Join(cfg.AgentWorkDir, spaceID)
	plannedSpec := ""
	if specPath != "" {
		plannedSpec = "spec"
	}
	resolvedSummoner := summon.ResolveName(cfg, summoner)
	if prompt == "" {
		prompt = summon.Prompt(spacePath, plannedSpec)
	}
	invocation, err := summon.BuildInvocation(cfg, spacePath, resolvedSummoner, prompt, agentArgs...)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dry-run: summon %s with %s\n", spaceID, invocation.Summoner)
	fmt.Fprintln(cmd.OutOrStdout(), summon.CommandString(invocation))
	return nil
}

func (a *app) loadConfig() (*config.Config, string, error) {
	return config.Load(a.configPath)
}

func parseRepoSpecs(values []string) ([]space.RepoSpec, error) {
	specs := make([]space.RepoSpec, 0, len(values))
	for _, value := range values {
		spec, err := space.ParseRepoSpec(value)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func printStatus(out interface{ Write([]byte) (int, error) }, spacePath string, status space.Status, memStates map[string]string) {
	fmt.Fprintf(out, "space %s (%s)\npath: %s\n", status.Manifest.ID, status.Manifest.Kind, spacePath)
	if status.Manifest.SpecPath != "" {
		fmt.Fprintf(out, "spec: %s\n", filepath.Join(spacePath, status.Manifest.SpecPath))
	}
	for _, repo := range status.Repos {
		state := "clean"
		if repo.Dirty {
			state = "dirty"
		}
		exists := "missing"
		if repo.Exists {
			exists = "present"
		}
		fmt.Fprintf(out, "\n%s [%s] %s %s\n", repo.Repo.Name, repo.Repo.Mode, exists, state)
		fmt.Fprintf(out, "  path: %s\n", filepath.Join(spacePath, repo.Repo.Path))
		if repo.Repo.Mode == space.ModeEdit {
			fmt.Fprintf(out, "  branch: %s\n  base: %s\n", repo.Repo.Branch, repo.Repo.Base)
			if repo.DriftError != "" {
				fmt.Fprintf(out, "  drift: unknown (%s)\n", repo.DriftError)
			} else {
				fmt.Fprintf(out, "  drift: ahead %d, behind %d\n", repo.Ahead, repo.Behind)
			}
		} else {
			fmt.Fprintf(out, "  ref: %s\n", repo.Repo.Ref)
			if repo.ReferenceWarn != "" {
				fmt.Fprintf(out, "  warning: %s\n", repo.ReferenceWarn)
			}
		}
	}
	for _, mem := range status.Manifest.Memories {
		owned := ""
		if mem.Owned {
			owned = " owned"
		}
		fmt.Fprintf(out, "\n[memory] %s %s den=%s%s%s\n", mem.Name, mem.Provider, mem.ID, owned, memStates[mem.Name])
	}
}

func resolveAgentProvider(cfg config.Config, providerOverride, modelOverride string) (string, config.AgentProviderConfig, error) {
	providerName := firstNonEmpty(providerOverride, cfg.Agent.DefaultProvider, config.DefaultAgentProvider)
	if providerName != agent.ProviderOpenAI && providerName != agent.ProviderAnthropic {
		return "", config.AgentProviderConfig{}, fmt.Errorf("provider must be openai or anthropic")
	}
	providerCfg := cfg.Agent.Providers[providerName]
	if providerCfg.Model == "" {
		providerCfg.Model = defaultModelForProvider(providerName)
	}
	if modelOverride != "" {
		providerCfg.Model = modelOverride
	}
	if providerCfg.APIKeyRef == "" {
		providerCfg.APIKeyRef = agent.APIKeyRefForProvider(providerName, false)
	}
	return providerName, providerCfg, nil
}

func defaultModelForProvider(provider string) string {
	if provider == agent.ProviderAnthropic {
		return config.DefaultAgentModelAnthropic
	}
	return config.DefaultAgentModelOpenAI
}

func promptDefault(reader *bufio.Reader, out io.Writer, label string, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func readSecret(cmd *cobra.Command, reader *bufio.Reader, label string) (string, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s: ", label)
	if file, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		data, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func confirm(in io.Reader, out io.Writer) (bool, error) {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	if _, err := fmt.Fprint(out, "\nProceed? [y/N]: "); err != nil {
		return false, err
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "y" || value == "yes", nil
}

func collectQuestionAnswers(in io.Reader, out io.Writer, result agent.RunResult) (string, error) {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	if result.Message != "" {
		fmt.Fprintf(out, "%s\n", result.Message)
	}
	var b strings.Builder
	for _, question := range result.Questions {
		answer := ""
		var err error
		if len(question.Options) > 0 {
			fmt.Fprintf(out, "%s\n", question.Prompt)
			for i, option := range question.Options {
				fmt.Fprintf(out, "  %d. %s\n", i+1, option)
			}
			answer, err = promptDefault(reader, out, "Answer", question.Options[0])
		} else {
			for {
				fmt.Fprintf(out, "%s: ", question.Prompt)
				answer, err = reader.ReadString('\n')
				if err != nil && err != io.EOF {
					return "", err
				}
				answer = strings.TrimSpace(answer)
				if answer != "" || !question.Required {
					break
				}
				fmt.Fprintln(out, "A value is required.")
			}
		}
		if err != nil && err != io.EOF {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" && len(question.Options) > 0 {
			answer = question.Options[0]
		}
		fmt.Fprintf(&b, "- %s: %s\n", question.ID, answer)
	}
	return b.String(), nil
}

func printAgentPlan(out io.Writer, result agent.RunResult) {
	if result.Status != "" && result.Status != agent.RunStatusPlanReady {
		fmt.Fprintf(out, "Status: %s\n", result.Status)
	}
	if result.Message != "" && result.Message != result.Plan.Summary {
		fmt.Fprintf(out, "Message: %s\n", result.Message)
	}
	if len(result.Questions) > 0 {
		fmt.Fprintln(out, "Questions:")
		for _, question := range result.Questions {
			fmt.Fprintf(out, "  - %s\n", question.Prompt)
			if len(question.Options) > 0 {
				fmt.Fprintf(out, "    options: %s\n", strings.Join(question.Options, ", "))
			}
		}
	}
	if result.Plan.Summary != "" {
		fmt.Fprintf(out, "Plan: %s\n", result.Plan.Summary)
	} else {
		fmt.Fprintln(out, "Plan:")
	}
	for i, op := range result.Plan.Operations {
		fmt.Fprintf(out, "  %d. %s\n", i+1, op.Type)
	}
	if len(result.Commands) > 0 {
		fmt.Fprintln(out, "\nCommands:")
		for _, command := range result.Commands {
			fmt.Fprintf(out, "  %s\n", command)
		}
	}
	if len(result.Plan.Notes) > 0 {
		fmt.Fprintln(out, "\nNotes:")
		for _, note := range result.Plan.Notes {
			fmt.Fprintf(out, "  - %s\n", note)
		}
	}
	if len(result.Plan.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, warning := range result.Plan.Warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}
	if result.Executed {
		fmt.Fprintln(out, "\nExecuted.")
	}
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(agent.RedactForJSON(value))
}

func (a *app) effectiveProviderFactory() agent.ProviderFactory {
	if a.providerFactory != nil {
		return a.providerFactory
	}
	return agent.DefaultProviderFactory
}

func (a *app) effectiveSecretStore() agent.SecretStore {
	if a.secretStore != nil {
		return a.secretStore
	}
	return agent.DefaultSecretStore()
}

func (a *app) effectiveSummonLauncher() summon.Launcher {
	if a.summonLauncher != nil {
		return a.summonLauncher
	}
	return summon.ExecLauncher{}
}

func (a *app) effectivePortalRunner() portal.Runner {
	return a.portalRunner
}

func (a *app) commandIsTerminal(cmd *cobra.Command) bool {
	if a.isTerminal != nil {
		return a.isTerminal(cmd)
	}
	file, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// sortedRepoNames returns the registered repo names in lexical order so
// listings, sync order, and resolution errors are deterministic across runs.
func sortedRepoNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Repos))
	for name := range cfg.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func ExecuteContext(ctx context.Context) error {
	cmd := NewRootCommand()
	cmd.SetContext(ctx)
	return cmd.Execute()
}
