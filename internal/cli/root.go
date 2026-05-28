package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

type app struct {
	configPath string
}

func NewRootCommand() *cobra.Command {
	a := &app{}
	cmd := &cobra.Command{
		Use:   "stave",
		Short: "Manage agent workspaces backed by shared bare Git repositories",
	}
	cmd.PersistentFlags().StringVar(&a.configPath, "config", "", "config file path (default ~/.config/stave/config.yaml)")
	cmd.AddCommand(
		a.setupCommand(),
		a.reposCommand(),
		a.spaceCommand(),
	)
	return cmd
}

func (a *app) setupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Create the Stave root directories and config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := config.Load(a.configPath)
			if err != nil {
				return err
			}
			if err := cfg.EnsureRootDirs(); err != nil {
				return err
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized stave root at %s\nconfig: %s\n", cfg.Root, path)
			return nil
		},
	}
}

func (a *app) reposCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage registered bare repositories",
	}
	cmd.AddCommand(a.reposAddCommand(), a.reposListCommand(), a.reposSyncCommand(), a.reposRemoveCommand())
	return cmd
}

func (a *app) reposAddCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Clone and register a bare repository",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			name, url := args[0], args[1]
			if _, exists := cfg.Repos[name]; exists {
				return fmt.Errorf("repo %q is already registered", name)
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: create %s\n", cfg.Root)
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: create %s\n", cfg.BareReposDir)
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: create %s\n", cfg.AgentWorkDir)
			} else {
				if err := cfg.EnsureRootDirs(); err != nil {
					return err
				}
			}
			repo, err := cfg.RegisterRepository(name, url, "")
			if err != nil {
				return err
			}
			if !dryRun {
				if _, err := os.Stat(repo.BareRepoPath); err == nil {
					return fmt.Errorf("bare repo path already exists: %s", repo.BareRepoPath)
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			client := git.New(git.WithDryRun(dryRun, func(format string, args ...any) {
				fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...)
			}))
			ctx := cmd.Context()
			if err := client.CloneBare(ctx, url, repo.BareRepoPath); err != nil {
				return err
			}
			if err := client.ConfigureBareRemoteTracking(ctx, repo.BareRepoPath); err != nil {
				return err
			}
			if err := client.FetchAllPrune(ctx, repo.BareRepoPath); err != nil {
				return err
			}
			if branch, err := client.RemoteDefaultBranch(ctx, repo.BareRepoPath); err == nil && branch != "" {
				repo.DefaultBranch = branch
				cfg.Repos[name] = repo
			}
			if !dryRun {
				if err := cfg.Save(path); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "registered %s at %s\n", name, repo.BareRepoPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	return cmd
}

func (a *app) reposListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.Repos))
			for name := range cfg.Repos {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				repo := cfg.Repos[name]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", name, repo.URL, repo.BareRepoPath)
			}
			return nil
		},
	}
}

func (a *app) reposSyncCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [name]",
		Short: "Fetch and prune registered bare repositories",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
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
				for name := range cfg.Repos {
					targets = append(targets, name)
				}
				sort.Strings(targets)
			}
			client := git.New()
			for _, name := range targets {
				repo := cfg.Repos[name]
				if err := client.FetchAllPrune(cmd.Context(), repo.BareRepoPath); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "synced %s\n", name)
			}
			return nil
		},
	}
}

func (a *app) reposRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a repository without deleting its bare repo cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Repos[name]; !ok {
				return fmt.Errorf("repo %q is not registered", name)
			}
			cfg.UnregisterRepository(name)
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unregistered %s\n", name)
			return nil
		},
	}
}

func (a *app) spaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "space",
		Short: "Manage agent workspaces",
	}
	cmd.AddCommand(
		a.initCommand(),
		a.createCommand(),
		a.addCommand(),
		a.syncCommand(),
		a.statusCommand(),
		a.archiveCommand(),
		a.destroyCommand(),
	)
	return cmd
}

func (a *app) initCommand() *cobra.Command {
	var kind string
	var spec string
	cmd := &cobra.Command{
		Use:   "init <space-id>",
		Short: "Create an empty agent workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			return svc.InitSpace(cmd.Context(), space.InitOptions{ID: args[0], Kind: kind, SpecPath: spec})
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "space kind, such as ticket, spike, or audit")
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the space")
	return cmd
}

func (a *app) createCommand() *cobra.Command {
	var kind string
	var spec string
	var edits []string
	var references []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "create <space-id>",
		Short: "Create a workspace and add edit/reference repos in one command",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			editSpecs, err := parseRepoSpecs(edits)
			if err != nil {
				return err
			}
			refSpecs, err := parseRepoSpecs(references)
			if err != nil {
				return err
			}
			svc, err := a.serviceWithDryRun(cmd, dryRun)
			if err != nil {
				return err
			}
			return svc.Create(cmd.Context(), space.CreateOptions{ID: args[0], Kind: kind, SpecPath: spec, Edits: editSpecs, References: refSpecs, DryRun: dryRun})
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "space kind, such as ticket, spike, or audit")
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the space")
	cmd.Flags().StringArrayVarP(&edits, "edit", "e", nil, "editable repo spec, optionally repo:base")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "reference repo spec, optionally repo:ref")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	return cmd
}

func (a *app) addCommand() *cobra.Command {
	var edit bool
	var reference bool
	var base string
	var branch string
	var noFetch bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "add <space-id> <repo>",
		Short: "Add an editable or reference repository worktree to a space",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if edit == reference {
				return fmt.Errorf("choose exactly one of --edit or --reference")
			}
			mode := space.ModeEdit
			ref := ""
			if reference {
				mode = space.ModeReference
				ref = base
			}
			svc, err := a.serviceWithDryRun(cmd, dryRun)
			if err != nil {
				return err
			}
			return svc.AddRepo(cmd.Context(), space.AddOptions{
				SpaceID:  args[0],
				RepoName: args[1],
				Mode:     mode,
				Base:     base,
				Ref:      ref,
				Branch:   branch,
				NoFetch:  noFetch,
				DryRun:   dryRun,
			})
		},
	}
	cmd.Flags().BoolVarP(&edit, "edit", "e", false, "add as an editable top-level worktree")
	cmd.Flags().BoolVarP(&reference, "reference", "r", false, "add as a detached reference worktree under references/")
	cmd.Flags().StringVarP(&base, "base", "b", "", "base branch/ref for edit repos, or ref for reference repos")
	cmd.Flags().StringVar(&branch, "branch", "", "branch name for editable repos")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip fetching the bare repo before adding the worktree")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	return cmd
}

func (a *app) syncCommand() *cobra.Command {
	var referencesOnly bool
	cmd := &cobra.Command{
		Use:   "sync <space-id>",
		Short: "Fetch a space's repos, update references, and report editable drift",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			return svc.Sync(cmd.Context(), space.SyncOptions{SpaceID: args[0], ReferencesOnly: referencesOnly})
		},
	}
	cmd.Flags().BoolVar(&referencesOnly, "references-only", false, "only sync reference worktrees")
	return cmd
}

func (a *app) statusCommand() *cobra.Command {
	return &cobra.Command{
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
			printStatus(cmd.OutOrStdout(), svc.SpacePath(args[0]), status)
			return nil
		},
	}
}

func (a *app) archiveCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "archive <space-id>",
		Short: "Remove worktrees and preserve space metadata/notes under .archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			return svc.Archive(cmd.Context(), space.ArchiveOptions{SpaceID: args[0], Force: force})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "archive even when editable worktrees are dirty")
	return cmd
}

func (a *app) destroyCommand() *cobra.Command {
	var force bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "destroy <space-id>",
		Short: "Remove a space's worktrees and delete its directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.serviceWithDryRun(cmd, dryRun)
			if err != nil {
				return err
			}
			return svc.Destroy(cmd.Context(), space.DestroyOptions{SpaceID: args[0], Force: force, DryRun: dryRun})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "destroy even when editable worktrees are dirty")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	return cmd
}

func (a *app) service(cmd *cobra.Command) (space.Service, error) {
	return a.serviceWithDryRun(cmd, false)
}

func (a *app) serviceWithDryRun(cmd *cobra.Command, dryRun bool) (space.Service, error) {
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
		fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...)
	}))
	return space.NewService(*cfg, client, cmd.OutOrStdout()), nil
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

func printStatus(out interface{ Write([]byte) (int, error) }, spacePath string, status space.Status) {
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
}

func ExecuteContext(ctx context.Context) error {
	cmd := NewRootCommand()
	cmd.SetContext(ctx)
	return cmd.Execute()
}
