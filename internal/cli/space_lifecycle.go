package cli

import (
	"fmt"
	"path/filepath"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// removeCommand is the inverse of `space add`: drop one repo's worktree and
// manifest entry from a live space. The edit branch stays in the bare repo.
func (a *app) removeCommand() *cobra.Command {
	var force bool
	var dryRun bool
	var jsonOut bool
	var editMode bool
	var referenceMode bool
	cmd := &cobra.Command{
		Use:   "remove <space-id> <repo>",
		Short: "Remove a repository worktree from a space (the branch is kept)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				if editMode && referenceMode {
					return nil, &invalidArgumentsError{msg: "choose at most one of --edit or --reference"}
				}
				var mode space.RepoMode
				switch {
				case editMode:
					mode = space.ModeEdit
				case referenceMode:
					mode = space.ModeReference
				}
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.RemoveRepo(cmd.Context(), space.RemoveOptions{
					SpaceID:  args[0],
					RepoName: args[1],
					Mode:     mode,
					Force:    force,
					DryRun:   dryRun,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				return spaceMutationPayload(svc, args[0], sink, fmt.Sprintf("removed %s from %s", args[1], args[0]))
			})
		},
	}
	cmd.Flags().BoolVarP(&editMode, "edit", "e", false, "remove the editable worktree entry (required when the repo is also a reference)")
	cmd.Flags().BoolVarP(&referenceMode, "reference", "r", false, "remove the reference worktree entry (required when the repo is also editable)")
	cmd.Flags().BoolVar(&force, "force", false, "remove even when the edit worktree is dirty or other spaces stack on its branch")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}

// restoreCommand is the inverse of `space archive`: move an archived space
// back and re-create its worktrees at the recorded branches/refs.
func (a *app) restoreCommand() *cobra.Command {
	var from string
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "restore <space-id>",
		Short: "Restore an archived space from .archive/ and re-create its worktrees",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				sink := newOutputSink(cmd, jsonOut)
				svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
				if err != nil {
					return nil, err
				}
				if err := svc.Restore(cmd.Context(), space.RestoreOptions{
					SpaceID: args[0],
					From:    from,
					DryRun:  dryRun,
				}); err != nil {
					return nil, err
				}
				if !jsonOut {
					return nil, nil
				}
				if dryRun {
					return dryRunPayload(sink), nil
				}
				// Per-repo "restored ..." progress lines are represented by the
				// reloaded manifest; only notices remain notes.
				spacePath := svc.SpacePath(args[0])
				manifest, err := space.LoadManifest(spacePath)
				if err != nil {
					return nil, err
				}
				primary := []string{fmt.Sprintf("restored %s -> %s", args[0], spacePath)}
				for _, repo := range manifest.Repos {
					worktree := filepath.Join(spacePath, repo.Path)
					if repo.Mode == space.ModeEdit {
						primary = append(primary, fmt.Sprintf("restored edit %s at %s (branch %s)", repo.Name, worktree, repo.Branch))
					} else {
						primary = append(primary, fmt.Sprintf("restored reference %s at %s (%s)", repo.Name, worktree, repo.Ref))
					}
				}
				return spaceMutationPayload(svc, args[0], sink, primary...)
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "name of the .archive/ entry to restore (required when several <space-id>-<timestamp> archives exist)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print operations without changing state")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON (result on success, {\"error\": {code, message}} on failure; exit 1)")
	return cmd
}
