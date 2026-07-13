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
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type app struct {
	configPath      string
	providerFactory agent.ProviderFactory
	secretStore     agent.SecretStore
	summonLauncher  summon.Launcher
	portalRunner    portal.Runner
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
		a.reposCommand(),
		a.spaceCommand(),
		a.portalCommand(),
		a.agentCommand(),
		a.summonCommand(),
		a.reviewCommand(),
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
	var summonName string
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
			if err := svc.Create(cmd.Context(), space.CreateOptions{ID: args[0], Kind: kind, SpecPath: spec, Edits: editSpecs, References: refSpecs, DryRun: dryRun}); err != nil {
				return err
			}
			if summonName == "" {
				return nil
			}
			if dryRun {
				return a.printPlannedSummon(cmd, svc.Config, args[0], summonName, spec)
			}
			return a.runSummon(cmd, svc.Config, args[0], summonName, "", false)
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "space kind, such as ticket, spike, or audit")
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the space")
	cmd.Flags().StringArrayVarP(&edits, "edit", "e", nil, "editable repo spec, optionally repo:base")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "reference repo spec, optionally repo:ref")
	cmd.Flags().StringVar(&summonName, "summon", "", "launch a summoner after creation (codex, claude, or cursor)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	return cmd
}

func (a *app) summonCommand() *cobra.Command {
	var summoner, prompt string
	var printCommand bool
	cmd := &cobra.Command{
		Use:   "summon <space-id>",
		Short: "Launch an interactive agent in a Stave space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			return a.runSummon(cmd, *cfg, args[0], summoner, prompt, printCommand)
		},
	}
	cmd.Flags().StringVar(&summoner, "with", "", "summoner to launch (codex, claude, or cursor; defaults to config)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "override the launch prompt (e.g. a skill invocation like \"/pr-teach\")")
	cmd.Flags().BoolVar(&printCommand, "print-command", false, "print the launch command instead of running it")
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
	cmd.Flags().StringVar(&method, "method", "native", "login method")
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
	cmd.Flags().IntVar(&maxDelete, "max-delete", 0, "maximum deletes after dry-run parsing")
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
			if !printCommand && !a.commandIsTerminal(cmd) {
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
	var agentName string
	var follow, dryRun, printCommand bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <space-id> [portal-id]",
		Short: "Show portal runtime or agent logs",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			preview := dryRun || printCommand
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanLogs(cmd.Context(), portal.LogsOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Agent: agentName, Follow: follow, Tail: tail, DryRun: preview})
			}, preview, false)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent session name")
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
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "detach <space-id> [portal-id]",
		Short: "Remove attach-only portal metadata",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = yes
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
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm detach")
	return cmd
}

func (a *app) portalDestroyCommand() *cobra.Command {
	var timeout int
	var deleteVolumes, deleteRemoteData, force, dryRun bool
	cmd := &cobra.Command{
		Use:   "destroy <space-id> [portal-id]",
		Short: "Destroy Stave-owned portal runtime resources",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPortalPlanningCommand(cmd, func(svc portal.Service) (portal.Plan, error) {
				return svc.PlanDestroy(cmd.Context(), portal.DestroyOptions{SpaceID: args[0], PortalID: optionalPortalID(args), Timeout: timeout, DeleteVolumes: deleteVolumes, DeleteRemoteData: deleteRemoteData, Force: force, DryRun: dryRun})
			}, dryRun, false)
		},
	}
	cmd.Flags().IntVar(&timeout, "timeout", 0, "graceful stop timeout in seconds")
	cmd.Flags().BoolVar(&deleteVolumes, "delete-volumes", false, "delete recorded Stave-owned volumes")
	cmd.Flags().BoolVar(&deleteRemoteData, "delete-remote-data", false, "delete exact recorded remote data when supported")
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
		fmt.Fprintf(errOut, "[%s] %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		if diagnostic.NextAction != "" {
			fmt.Fprintf(errOut, "  next: %s\n", diagnostic.NextAction)
		}
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

func (a *app) runSummon(cmd *cobra.Command, cfg config.Config, spaceID string, summoner string, prompt string, printCommand bool) error {
	svc := summon.NewService(cfg, a.effectiveSummonLauncher(), cmd.OutOrStdout())
	svc.Interactive = a.commandIsTerminal(cmd)
	if !printCommand && !svc.Interactive {
		fmt.Fprintln(cmd.ErrOrStderr(), "Non-interactive terminal detected; printing summon command instead of launching.")
	}
	return svc.Summon(cmd.Context(), summon.Options{SpaceID: spaceID, Summoner: summoner, Prompt: prompt, PrintCommand: printCommand})
}

func (a *app) printPlannedSummon(cmd *cobra.Command, cfg config.Config, spaceID string, summoner string, specPath string) error {
	spacePath := filepath.Join(cfg.AgentWorkDir, spaceID)
	plannedSpec := ""
	if specPath != "" {
		plannedSpec = "spec"
	}
	invocation, err := summon.BuildInvocation(cfg, spacePath, summon.ResolveName(cfg, summoner), summon.Prompt(spacePath, plannedSpec))
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
