package cli

import (
	"fmt"

	"github.com/Nurozen/stave/internal/space"
)

// spaceSyncJSON is the success shape of space sync: the manifest plus one
// row per processed repo (references-only filtering drops edit rows, as the
// human output does).
type spaceSyncJSON struct {
	SpaceID   string                 `json:"spaceId"`
	SpacePath string                 `json:"spacePath"`
	Manifest  space.Manifest         `json:"manifest"`
	Repos     []space.SyncRepoResult `json:"repos"`
	Notes     []string               `json:"notes,omitempty"`
}

func spaceSyncPayload(report space.SyncReport, sink *outputSink) spaceSyncJSON {
	return spaceSyncJSON{
		SpaceID:   report.SpaceID,
		SpacePath: report.SpacePath,
		Manifest:  report.Manifest,
		Repos:     report.Repos,
		Notes:     sink.Notes(report.Lines...),
	}
}

// sagaSyncJSON is the success shape of saga sync: members in walk order with
// their per-repo rows, the saga space's own reference rows, and every other
// notice the walk printed (merge findings, degrade notes) as notes.
type sagaSyncJSON struct {
	SagaID    string                       `json:"sagaId"`
	SpacePath string                       `json:"spacePath"`
	Members   []space.SagaSyncMemberReport `json:"members"`
	Repos     []space.SyncRepoResult       `json:"repos,omitempty"`
	Notes     []string                     `json:"notes,omitempty"`
}

func sagaSyncPayload(report space.SagaSyncReport, sink *outputSink) sagaSyncJSON {
	return sagaSyncJSON{
		SagaID:    report.SagaID,
		SpacePath: report.SpacePath,
		Members:   report.Members,
		Repos:     report.Repos,
		Notes:     sink.Notes(report.Lines...),
	}
}

// spaceRetargetPayload is the space mutation payload after a retarget; the
// success line is rebuilt from the reloaded manifest's recorded base.
func spaceRetargetPayload(svc space.Service, spaceID, repoName string, sink *outputSink) (spaceMutationJSON, error) {
	manifest, err := space.LoadManifest(svc.SpacePath(spaceID))
	if err != nil {
		return spaceMutationJSON{}, err
	}
	base := ""
	if repo, _, ok := manifest.FindRepo(repoName); ok {
		base = repo.Base
	}
	return spaceMutationPayload(svc, spaceID, sink, fmt.Sprintf("retargeted %s repo %s to base %s", spaceID, repoName, base))
}

// reposAddJSON is the success shape of repos add.
type reposAddJSON struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	BareRepoPath  string   `json:"bareRepoPath"`
	DefaultBranch string   `json:"defaultBranch,omitempty"`
	Adopted       bool     `json:"adopted"`
	Notes         []string `json:"notes,omitempty"`
}

// setupJSON is the success shape of setup: which of the root directories and
// the config file the run created versus found already present.
type setupJSON struct {
	ConfigPath   string   `json:"configPath"`
	Root         string   `json:"root"`
	BareReposDir string   `json:"bareReposDir"`
	AgentWorkDir string   `json:"agentWorkDir"`
	Created      []string `json:"created"`
	Existed      []string `json:"existed"`
}
