package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nurozen/stave/internal/agent"
	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type app struct {
	configPath      string
	providerFactory agent.ProviderFactory
	secretStore     agent.SecretStore
	isTerminal      func(*cobra.Command) bool
}

func NewRootCommand() *cobra.Command {
	return newRootCommand(&app{})
}

func newRootCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stave",
		Short: "Manage agent workspaces backed by shared bare Git repositories",
	}
	cmd.PersistentFlags().StringVar(&a.configPath, "config", "", "config file path (default ~/.config/stave/config.yaml)")
	cmd.AddCommand(
		a.setupCommand(),
		a.reposCommand(),
		a.spaceCommand(),
		a.agentCommand(),
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
			dispatcher := agent.NewToolDispatcher(*cfg, git.New(), cmd.OutOrStdout())
			trace := io.Writer(nil)
			if !jsonOut {
				trace = cmd.ErrOrStderr()
			}
			result, err := provider.Run(cmd.Context(), agent.ProviderRequest{Query: query, Context: agentContext, Dispatcher: dispatcher, Trace: trace})
			if err != nil {
				return err
			}
			if err := agent.ValidatePlan(*cfg, result.Plan); err != nil {
				return err
			}
			result.Commands = result.Plan.Commands()
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
				results, err := (agent.Executor{Config: *cfg, Git: git.New(), Out: cmd.OutOrStdout()}).ExecutePlan(cmd.Context(), result.Plan)
				result.Results = results
				result.Executed = true
				if err != nil {
					if jsonOut {
						_ = writeJSON(cmd.OutOrStdout(), result)
					}
					return err
				}
			}
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
	reader := bufio.NewReader(in)
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

func printAgentPlan(out io.Writer, result agent.RunResult) {
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
	return encoder.Encode(value)
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

func (a *app) commandIsTerminal(cmd *cobra.Command) bool {
	if a.isTerminal != nil {
		return a.isTerminal(cmd)
	}
	file, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
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
