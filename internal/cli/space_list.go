package cli

import (
	"fmt"
	"time"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// spaceListRow is the typed `space list --json` row: one space (live or, with
// --archived, one archive entry) with its manifest summary and saga join.
type spaceListRow struct {
	ID        string              `json:"id"`
	Path      string              `json:"path"`
	Kind      string              `json:"kind,omitempty"`
	CreatedAt string              `json:"createdAt,omitempty"`
	IsSaga    bool                `json:"isSaga"`
	MemberOf  string              `json:"memberOf,omitempty"`
	Repos     []spaceListRepoJSON `json:"repos"`
	Archived  bool                `json:"archived,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// spaceListRepoJSON is the per-repo summary carried by a `space list --json`
// row; the full manifest is available from `space status --json`.
type spaceListRepoJSON struct {
	Name string         `json:"name"`
	Mode space.RepoMode `json:"mode"`
}

func (a *app) listCommand() *cobra.Command {
	var jsonOut bool
	var archived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List spaces (id, kind, path); --archived lists .archive/ entries instead",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := a.service(cmd)
			if err != nil {
				return err
			}
			var rows []spaceListRow
			if archived {
				rows, err = archivedSpaceListRows(svc)
			} else {
				rows, err = liveSpaceListRows(svc)
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			for _, row := range rows {
				if row.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\terror: %s\n", row.ID, row.Error)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", row.ID, firstNonEmpty(row.Kind, "-"), row.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	cmd.Flags().BoolVar(&archived, "archived", false, "list archived spaces under .archive/ instead of live spaces")
	return cmd
}

// liveSpaceListRows joins ListSpaces with the saga membership SagaList
// derives, so a member row carries the id of the saga that lists it.
func liveSpaceListRows(svc space.Service) ([]spaceListRow, error) {
	spaces, err := svc.ListSpaces()
	if err != nil {
		return nil, err
	}
	sagaEntries, err := svc.SagaList()
	if err != nil {
		return nil, err
	}
	memberOf := make(map[string]string, len(sagaEntries))
	for _, entry := range sagaEntries {
		if entry.MemberOf != "" {
			memberOf[entry.ID] = entry.MemberOf
		}
	}
	rows := make([]spaceListRow, 0, len(spaces))
	for _, entry := range spaces {
		row := spaceListRowFrom(entry.ID, entry.Path, entry.Manifest, entry.Err)
		row.MemberOf = memberOf[entry.ID]
		rows = append(rows, row)
	}
	return rows, nil
}

// archivedSpaceListRows lists .archive/ entries. Archived spaces carry no saga
// join: sagas track archived members by id and incarnation, not the reverse.
func archivedSpaceListRows(svc space.Service) ([]spaceListRow, error) {
	archives, err := svc.ListArchivedSpaces()
	if err != nil {
		return nil, err
	}
	rows := make([]spaceListRow, 0, len(archives))
	for _, entry := range archives {
		row := spaceListRowFrom(entry.ID, entry.Path, entry.Manifest, entry.Err)
		row.Archived = true
		rows = append(rows, row)
	}
	return rows, nil
}

func spaceListRowFrom(id, path string, manifest *space.Manifest, err error) spaceListRow {
	row := spaceListRow{ID: id, Path: path, Repos: []spaceListRepoJSON{}}
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Kind = manifest.Kind
	if !manifest.CreatedAt.IsZero() {
		row.CreatedAt = manifest.CreatedAt.UTC().Format(time.RFC3339)
	}
	row.IsSaga = manifest.Saga != nil
	for _, repo := range manifest.Repos {
		row.Repos = append(row.Repos, spaceListRepoJSON{Name: repo.Name, Mode: repo.Mode})
	}
	return row
}
