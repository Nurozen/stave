package cli

import (
	"fmt"
	"time"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// spaceListRow is the typed `space list --json` row: one space (live or, with
// --archived, one archive entry) with its manifest summary and saga join.
//
// Identity for hosts is (logicalId, manifestCreatedAt): `id` is the DIRECTORY
// name (for archived rows the archive basename, e.g. "t1-20260903120000"),
// `createdAt` is the legacy whole-second stamp, while `logicalId` is the
// manifest's own id and `manifestCreatedAt` keeps the manifest's full
// fractional precision so it compares equal to the stamp in .stave.yaml.
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
	// LogicalID is the manifest id; null on error rows (no manifest to read).
	LogicalID *string `json:"logicalId"`
	// ArchiveBasename is the .archive/ directory name (archived rows only).
	ArchiveBasename string `json:"archiveBasename,omitempty"`
	// ManifestCreatedAt is the manifest stamp in RFC3339Nano (UTC); omitted
	// when the manifest carries no stamp or could not be read.
	ManifestCreatedAt string `json:"manifestCreatedAt,omitempty"`
	// ManifestVersion is the .stave.yaml schema version (0 = pre-version or
	// unreadable).
	ManifestVersion int                   `json:"manifestVersion"`
	Memories        []spaceListMemoryJSON `json:"memories"`
}

// spaceListRepoJSON is the per-repo summary carried by a `space list --json`
// row; the full manifest is available from `space status --json`.
type spaceListRepoJSON struct {
	Name string         `json:"name"`
	Mode space.RepoMode `json:"mode"`
}

// spaceListMemoryJSON is one manifest memory attachment as `space list --json`
// carries it (the same fields the manifest records).
type spaceListMemoryJSON struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Owned    bool   `json:"owned"`
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
		row.ArchiveBasename = entry.ID
		rows = append(rows, row)
	}
	return rows, nil
}

func spaceListRowFrom(id, path string, manifest *space.Manifest, err error) spaceListRow {
	row := spaceListRow{ID: id, Path: path, Repos: []spaceListRepoJSON{}, Memories: []spaceListMemoryJSON{}}
	if err != nil {
		row.Error = err.Error()
		return row
	}
	logicalID := manifest.ID
	row.LogicalID = &logicalID
	row.ManifestVersion = manifest.Version
	row.Kind = manifest.Kind
	if !manifest.CreatedAt.IsZero() {
		row.CreatedAt = manifest.CreatedAt.UTC().Format(time.RFC3339)
		row.ManifestCreatedAt = manifest.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	row.IsSaga = manifest.Saga != nil
	for _, repo := range manifest.Repos {
		row.Repos = append(row.Repos, spaceListRepoJSON{Name: repo.Name, Mode: repo.Mode})
	}
	for _, mem := range manifest.Memories {
		row.Memories = append(row.Memories, spaceListMemoryJSON{Name: mem.Name, Provider: mem.Provider, ID: mem.ID, Owned: mem.Owned})
	}
	return row
}
