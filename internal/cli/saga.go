package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/memory"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/spf13/cobra"
)

func (a *app) sagaCommand() *cobra.Command {
	cmd := groupCommand("saga", "Coordinate dependent member spaces as one saga")
	cmd.AddCommand(
		a.sagaCreateCommand(),
		a.sagaListCommand(),
		a.sagaStatusCommand(),
		a.sagaSyncCommand(),
		a.sagaAddCommand(),
		a.sagaRemoveCommand(),
		a.sagaArchiveCommand(),
		a.sagaDestroyCommand(),
	)
	return cmd
}

// rejectSagaEditFlags refuses -e/--edit before a literal "--": sagas hold no
// edit worktrees, and with --summon set an unrecognized pre-"--" flag would
// otherwise forward silently to the agent. Everything after "--" forwards as
// usual (parsePassthroughArgs owns that split). Single-dash tokens starting
// with "-e" are the -e shorthand in pflag semantics (-e, -e=x, -ex), so all
// three spellings are caught.
func rejectSagaEditFlags(args []string) error {
	for _, token := range args {
		if token == "--" {
			return nil
		}
		if token == "--edit" || strings.HasPrefix(token, "--edit=") ||
			(strings.HasPrefix(token, "-e") && !strings.HasPrefix(token, "--")) {
			return argErrorf("sagas hold no edit worktrees; create a member instead: stave space create <member-id> --saga <saga-id> -e <repo> (arguments after a literal \"--\" forward to the agent)")
		}
	}
	return nil
}

func (a *app) sagaCreateCommand() *cobra.Command {
	var spec string
	var references []string
	var memories []string
	var dryRun bool
	var summonName string
	var noLearn bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <saga-id>",
		Short: "Create a saga space that coordinates member spaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Interspersed parsing is off (agent args forward after --), so flags
			// following the positional — --json included — are only known once
			// parsePassthroughArgs has run; the -e refusal must still come first.
			rejectErr := rejectSagaEditFlags(args)
			var positionals, agentArgs []string
			var parseErr error
			if rejectErr == nil {
				positionals, agentArgs, parseErr = parsePassthroughArgs(cmd, args, 1, 1, func() bool { return summonName != "" })
			}
			return runJSON(cmd, jsonOut, func() (any, error) {
				if rejectErr != nil {
					return nil, rejectErr
				}
				if parseErr != nil {
					return nil, parseErr
				}
				if jsonOut && summonName != "" {
					return nil, argErrorf("--summon is interactive and cannot be combined with --json")
				}
				sagaID := positionals[0]
				refSpecs, err := parseRepoSpecs(references)
				if err != nil {
					return nil, err
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.CreateSaga(cmd.Context(), space.SagaCreateOptions{
					ID:         sagaID,
					SpecPath:   spec,
					References: refSpecs,
					Memories:   memories,
					NoLearn:    noLearn,
					DryRun:     dryRun,
				}); err != nil {
					return nil, err
				}
				if dryRun {
					if jsonOut {
						return dryRunPayload(sink), nil
					}
					if summonName == "" {
						return nil, nil
					}
					plannedMemories, err := plannedSagaMemories(svc.Config, sagaID, memories)
					if err != nil {
						return nil, err
					}
					plannedSpec := ""
					if spec != "" {
						plannedSpec = "spec"
					}
					spacePath := svc.SpacePath(sagaID)
					prompt := summon.SagaPromptForPlan(spacePath, plannedSpec, summon.ResolveName(svc.Config, summonName), plannedMemories)
					return nil, a.printPlannedSummonWithPrompt(cmd, svc.Config, sagaID, summonName, spec, agentArgs, prompt)
				}
				if err := summon.InstallSagaSkill(svc.SpacePath(sagaID)); err != nil {
					return nil, err
				}
				if err := a.requestShellChdir(svc.SpacePath(sagaID)); err != nil {
					return nil, err
				}
				if jsonOut {
					return sagaMutationPayload(svc, sagaID, sink, fmt.Sprintf("created space %s at %s", sagaID, svc.SpacePath(sagaID)))
				}
				if summonName == "" {
					return nil, nil
				}
				return nil, a.runSummon(cmd, svc.Config, sagaID, summonName, "", agentArgs, false)
			})
		},
	}
	cmd.Flags().StringVarP(&spec, "spec", "s", "", "path to a spec file or directory to copy into the saga space")
	cmd.Flags().StringArrayVarP(&references, "reference", "r", nil, "reference repo spec, optionally repo:ref")
	cmd.Flags().StringArrayVar(&memories, "memory", nil, "attach memory: [provider:]<spec>; '.' = fresh durable store shared with members (repeatable)")
	cmd.Flags().StringVar(&summonName, "summon", "", "launch a summoner after creation (codex, claude, or cursor)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&noLearn, "no-learn", false, "do not record repo tethers for this command (saga roots have no editable anchor, so this is a no-op for the saga root itself; members learn unless they pass --no-learn)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func plannedSagaMemories(cfg config.Config, sagaID string, rawSpecs []string) ([]space.MemoryManifest, error) {
	if len(rawSpecs) == 0 && cfg.Memory.Default {
		rawSpecs = []string{"."}
	}
	planned := make([]space.MemoryManifest, 0, len(rawSpecs))
	for _, raw := range rawSpecs {
		parsed, err := memory.ParseMemorySpec(raw, cfg.Memory.Provider)
		if err != nil {
			return nil, err
		}
		id := parsed.Spec
		if parsed.Fresh {
			id = sagaID
		}
		planned = append(planned, space.MemoryManifest{Provider: parsed.Provider, ID: id, Owned: parsed.Fresh})
	}
	return planned, nil
}

// sagaListRow is the typed --json row: every space with its kind and saga join.
// Path is the space directory; LogicalID the manifest id (null on error rows),
// the same identity `space list --json` carries.
type sagaListRow struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind,omitempty"`
	IsSaga    bool     `json:"isSaga"`
	Members   []string `json:"members,omitempty"`
	MemberOf  string   `json:"memberOf,omitempty"`
	Error     string   `json:"error,omitempty"`
	Path      string   `json:"path"`
	LogicalID *string  `json:"logicalId"`
}

func (a *app) sagaListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all spaces with their kind and saga membership",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			entries, err := svc.SagaList()
			if err != nil {
				return err
			}
			if jsonOut {
				rows := make([]sagaListRow, 0, len(entries))
				for _, entry := range entries {
					row := sagaListRow{ID: entry.ID, Kind: entry.Kind, IsSaga: entry.IsSaga, Members: entry.Members, MemberOf: entry.MemberOf, Path: entry.Path}
					if entry.Err != nil {
						row.Error = entry.Err.Error()
					} else {
						logicalID := entry.LogicalID
						row.LogicalID = &logicalID
					}
					rows = append(rows, row)
				}
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			for _, entry := range entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", entry.ID, firstNonEmpty(entry.Kind, "-"), sagaMembershipColumn(entry))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

// sagaMembershipColumn renders the third saga list column: the roster for
// sagas, the owning saga for members, and the manifest error for unreadable
// spaces.
func sagaMembershipColumn(entry space.SagaListEntry) string {
	switch {
	case entry.Err != nil:
		return fmt.Sprintf("error: %v", entry.Err)
	case entry.IsSaga && len(entry.Members) == 0:
		return "members: (none)"
	case entry.IsSaga:
		return "members: " + strings.Join(entry.Members, ",")
	case entry.MemberOf != "":
		return "saga: " + entry.MemberOf
	default:
		return "-"
	}
}

func (a *app) sagaStatusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <saga-id>",
		Short: "Show saga members in topological order with drift and topology notes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			status, err := svc.SagaStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), status)
			}
			printSagaStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (the frozen SagaStatus contract)")
	return cmd
}

func printSagaStatus(out io.Writer, status space.SagaStatus) {
	fmt.Fprintf(out, "saga %s (%d members)\n", status.SagaID, len(status.Members))
	for _, member := range status.Members {
		fmt.Fprintf(out, "\n%s [%s]", member.ID, member.State)
		if member.Dirty {
			fmt.Fprintf(out, " dirty")
		}
		if len(member.After) > 0 {
			fmt.Fprintf(out, " after: %s", strings.Join(member.After, ", "))
		}
		fmt.Fprintln(out)
		if member.Error != "" {
			fmt.Fprintf(out, "  detail: %s\n", member.Error)
		}
		for _, repo := range member.Repos {
			fmt.Fprintf(out, "  %s [edit] branch %s\n", repo.Name, repo.Branch)
			health := repo.BaseHealth
			if repo.BaseHealth == space.BaseHealthMerged {
				health = "merged (" + mergedViaLabel(repo, member.PRs) + ")"
			}
			fmt.Fprintf(out, "    base: %s (%s)\n", firstNonEmpty(repo.Base, "(none)"), health)
			fmt.Fprintf(out, "    drift: ahead %d, behind %d\n", repo.Ahead, repo.Behind)
			if repo.Note != "" {
				fmt.Fprintf(out, "    note: %s\n", repo.Note)
			}
		}
		for _, pr := range member.PRs {
			fmt.Fprintf(out, "  pr: %s #%d %s\n", pr.Repo, pr.Number, firstNonEmpty(pr.State, "?"))
		}
	}
	for _, note := range status.Notes {
		member := note.Member
		if member != "" {
			member = " " + member
		}
		fmt.Fprintf(out, "\nnote [%s]%s: %s\n", note.Kind, member, note.Text)
	}
}

// mergedViaLabel renders how a merged base landed: "ancestry", or the
// concrete pull request when the member carries a merged PR row for the repo
// ("PR #41"). A pr verdict without a matching row (stacked bases merge via
// the OWNER's branch PR, which is never one of this member's rows) falls
// back to the bare "PR".
func mergedViaLabel(repo space.SagaRepoStatus, prs []space.SagaPRStatus) string {
	if repo.MergedVia != space.MergedViaPR {
		return repo.MergedVia
	}
	for _, pr := range prs {
		if pr.Repo == repo.Name && strings.EqualFold(pr.State, "MERGED") {
			return fmt.Sprintf("PR #%d", pr.Number)
		}
	}
	return "PR"
}

func (a *app) sagaSyncCommand() *cobra.Command {
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync <saga-id>",
		Short: "Fetch shared bare repos once and sync every live saga member",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				report, err := svc.SagaSyncWithReport(cmd.Context(), args[0], space.SagaSyncOptions{DryRun: dryRun})
				if err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return sagaSyncPayload(report, sink), nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) sagaAddCommand() *cobra.Command {
	var after []string
	var clearAfter bool
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <saga-id> <space-id>",
		Short: "Register an existing space as a saga member",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if dryRun {
					fmt.Fprintf(sink.Writer(), "dry-run: add %s to saga %s\n", args[1], args[0])
					if jsonOut {
						return dryRunPayload(sink), nil
					}
					return nil, nil
				}
				if err := svc.SagaAdd(cmd.Context(), args[0], args[1], after, clearAfter); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				return sagaMutationPayload(svc, args[0], sink, fmt.Sprintf("added %s to saga %s", args[1], args[0]))
			})
		},
	}
	cmd.Flags().StringArrayVar(&after, "after", nil, "member id this space lands behind (repeatable)")
	cmd.Flags().BoolVar(&clearAfter, "clear-after", false, "reset the member's after edges before applying --after")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) sagaRemoveCommand() *cobra.Command {
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "remove <saga-id> <space-id>",
		Short: "Remove a member from a saga's roster",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if dryRun {
					fmt.Fprintf(sink.Writer(), "dry-run: remove %s from saga %s\n", args[1], args[0])
					if jsonOut {
						return dryRunPayload(sink), nil
					}
					return nil, nil
				}
				if err := svc.SagaRemove(cmd.Context(), args[0], args[1]); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				return sagaMutationPayload(svc, args[0], sink, fmt.Sprintf("removed %s from saga %s", args[1], args[0]))
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) sagaArchiveCommand() *cobra.Command {
	var force bool
	var dryRun bool
	var memoryFate string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "archive <saga-id>",
		Short: "Archive every member in reverse topological order, then the saga space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				fate, err := parseMemoryFateArg(memoryFate)
				if err != nil {
					return nil, err
				}
				if fate == memory.FateDestroy {
					return nil, argErrorf("--memory destroy is not valid for archive; use 'stave saga destroy --memory destroy' to destroy owned memory")
				}
				return a.runSagaTeardown(cmd, args[0], jsonOut, dryRun, fate, false, func(svc space.Service) (space.SagaTeardownReport, error) {
					return svc.SagaArchiveWithReport(cmd.Context(), args[0], space.SagaArchiveOptions{
						Force:      force,
						DryRun:     dryRun,
						MemoryFate: fate,
					})
				})
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "archive even when member worktrees are dirty or other spaces stack on member branches")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the ordered teardown plan without changing state")
	cmd.Flags().StringVar(&memoryFate, "memory", string(memory.FateKeep), "saga den fate on archive: keep or contribute (contribute-then-keep; destroy is not allowed)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

func (a *app) sagaDestroyCommand() *cobra.Command {
	var force bool
	var dryRun bool
	var memoryFate string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "destroy <saga-id>",
		Short: "Destroy every member in reverse topological order, then the saga space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				fate, err := parseMemoryFateArg(memoryFate)
				if err != nil {
					return nil, err
				}
				return a.runSagaTeardown(cmd, args[0], jsonOut, dryRun, fate, true, func(svc space.Service) (space.SagaTeardownReport, error) {
					return svc.SagaDestroyWithReport(cmd.Context(), args[0], space.SagaDestroyOptions{
						Force:      force,
						DryRun:     dryRun,
						MemoryFate: fate,
					})
				})
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "destroy even when member worktrees are dirty or other spaces stack on member branches or share the saga den")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the ordered teardown plan without changing state")
	cmd.Flags().StringVar(&memoryFate, "memory", string(memory.FateKeep), "saga den fate: keep, destroy, or contribute (default keep)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

// runSagaTeardown runs a saga archive/destroy walk. In --json mode it snapshots
// the members' states first (skipped members are not steps in the walk's
// report) and returns the sagaTeardownJSON payload, or the dry-run plan. A
// mid-walk failure surfaces as the service's *SagaTeardownError, whose
// details (completed steps, failedMember) the error envelope carries.
func (a *app) runSagaTeardown(cmd *cobra.Command, sagaID string, jsonOut, dryRun bool, fate memory.MemoryFate, destroy bool, run func(space.Service) (space.SagaTeardownReport, error)) (any, error) {
	sink := newOutputSink(cmd, jsonOut)
	svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
	if err != nil {
		return nil, err
	}
	var states []space.SagaMemberState
	if jsonOut && !dryRun {
		if states, err = svc.SagaMemberStates(sagaID); err != nil {
			return nil, err
		}
	}
	report, err := run(svc)
	if err != nil {
		return nil, err
	}
	if !jsonOut {
		return nil, nil
	}
	if dryRun {
		return dryRunPayload(sink), nil
	}
	action := "archived"
	if destroy {
		action = "destroyed"
	}
	return sagaTeardownJSON{
		SagaID:           sagaID,
		Action:           action,
		Memory:           string(fate),
		Members:          sagaTeardownMembers(svc, states, report, destroy),
		Notes:            sink.Lines(),
		SagaPath:         report.SagaPath,
		SagaArchivedPath: report.SagaArchivedPath,
	}, nil
}

// parseMemoryFateArg types a bad --memory value as invalid_arguments without
// changing its message.
func parseMemoryFateArg(raw string) (memory.MemoryFate, error) {
	fate, err := memory.ParseMemoryFate(raw)
	if err != nil {
		return "", &invalidArgumentsError{msg: err.Error()}
	}
	return fate, nil
}

// sagaLifecycleGuard redirects single-space archive/destroy away from saga
// state: a saga space or a registered saga member refuses without --force and
// points at the saga-aware verbs instead. Deliberately CLI-only —
// service-layer walks bypass it. Unreadable manifests and scan failures fall
// through to the service's own errors.
func sagaLifecycleGuard(svc space.Service, spaceID string) error {
	if manifest, err := space.LoadManifest(svc.SpacePath(spaceID)); err == nil && manifest.Saga != nil {
		return &space.SagaSpaceError{SpaceID: spaceID}
	}
	entries, err := svc.ListSpaces()
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Err != nil || entry.Manifest.Saga == nil {
			continue
		}
		for _, member := range entry.Manifest.Saga.Members {
			if member.ID == spaceID {
				return &space.SagaMemberError{SpaceID: spaceID, SagaID: entry.ID}
			}
		}
	}
	return nil
}
