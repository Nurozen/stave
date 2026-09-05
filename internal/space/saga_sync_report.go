package space

import (
	"context"
	"fmt"
)

// SagaSyncMemberReport is one roster row of a saga sync: live members carry
// the per-repo rows their Sync produced; archived and missing members are
// skipped with the same reason the human walk prints.
type SagaSyncMemberReport struct {
	ID    string           `json:"id"`
	State MemberState      `json:"state"`
	Repos []SyncRepoResult `json:"repos"`
	Note  string           `json:"note,omitempty"`
}

// SagaSyncReport is the structured result of SagaSyncWithReport. Repos are
// the saga space's own (reference) rows from its self-refresh. Lines holds
// the human lines the report accounts for, so --json callers can subtract
// them from captured output and keep the remaining notices (merge findings,
// degrade notes).
type SagaSyncReport struct {
	SagaID    string                 `json:"sagaId"`
	SpacePath string                 `json:"spacePath"`
	Members   []SagaSyncMemberReport `json:"members"`
	Repos     []SyncRepoResult       `json:"repos,omitempty"`
	Lines     []string               `json:"-"`
}

// SagaSyncWithReport runs SagaSync unchanged and collects each member's
// SyncReport through the Service's sync observer. Member rows come out in the
// same topological order the walk used; members the walk skipped are
// re-resolved afterwards (sync never changes lifecycle state) to name why.
func (s Service) SagaSyncWithReport(ctx context.Context, sagaID string, opts SagaSyncOptions) (SagaSyncReport, error) {
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return SagaSyncReport{}, err
	}
	// Type a missing saga as space_not_found up front; every other refusal
	// (not a saga, corrupt member) comes from SagaSync unchanged.
	if _, err := loadLiveManifest(sagaID, sagaPath); err != nil {
		return SagaSyncReport{}, err
	}
	reports := map[string]SyncReport{}
	s.onSyncReport = func(report SyncReport) {
		reports[report.SpaceID] = report
	}
	if err := s.SagaSync(ctx, sagaID, opts); err != nil {
		return SagaSyncReport{}, err
	}
	manifest, err := LoadManifest(sagaPath)
	if err != nil {
		return SagaSyncReport{}, err
	}
	report := SagaSyncReport{SagaID: sagaID, SpacePath: sagaPath, Members: []SagaSyncMemberReport{}}
	if manifest.Saga == nil {
		return report, nil
	}
	for _, member := range sagaTopoOrder(manifest.Saga.Members) {
		row := SagaSyncMemberReport{ID: member.ID, State: MemberLive, Repos: []SyncRepoResult{}}
		if synced, ok := reports[member.ID]; ok {
			row.Repos = synced.Repos
			report.Lines = append(report.Lines, fmt.Sprintf("syncing member %s", member.ID))
			report.Lines = append(report.Lines, synced.Lines...)
		} else {
			state, detail := s.resolveMemberState(member)
			row.State = state
			switch state {
			case MemberArchived:
				row.Note = fmt.Sprintf("archived at %s", detail)
				report.Lines = append(report.Lines, fmt.Sprintf("skipping member %s: archived at %s", member.ID, detail))
			case MemberMissing:
				row.Note = "missing"
				report.Lines = append(report.Lines, fmt.Sprintf("skipping member %s: missing", member.ID))
			default:
				row.Note = detail
			}
		}
		report.Members = append(report.Members, row)
	}
	if self, ok := reports[sagaID]; ok {
		report.Repos = self.Repos
		report.Lines = append(report.Lines, self.Lines...)
	}
	return report, nil
}
