package space

import (
	"os"
	"path/filepath"
)

// SagaTeardownStep is one completed teardown step of a saga archive/destroy
// walk: the member (or, as the final step, the saga space) that was torn down,
// its live root before the step, and — for archive — where it landed.
type SagaTeardownStep struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "archived" | "destroyed"
	Path   string `json:"path"`
	// ArchivedPath is the .archive/ destination; empty for destroy.
	ArchivedPath string `json:"archivedPath,omitempty"`
}

// SagaTeardownReport is what a saga archive/destroy walk got done. On success
// Completed lists every live member torn down this run, in walk order, and
// the saga space's own step is recorded in SagaTornDown/SagaArchivedPath. On
// a mid-walk failure Completed holds exactly the steps that finished before
// the stop and FailedAt/FailedMember locate the failure, so a host can
// reconcile its view without rescanning the work directory.
type SagaTeardownReport struct {
	SagaID   string
	SagaPath string
	// Verb is "archive" or "destroy".
	Verb string
	// Completed lists the members torn down this run (skipped members —
	// already archived or missing — are not steps and do not appear).
	Completed []SagaTeardownStep
	// SagaTornDown is set once the saga space itself was archived/destroyed.
	SagaTornDown bool
	// SagaArchivedPath is the saga space's .archive/ destination (archive only).
	SagaArchivedPath string
	// FailedAt is "" on success, else "den" (den-first destroy refused before
	// any member teardown), "member" (a member step failed) or "saga" (the
	// saga space's own step failed).
	FailedAt string
	// FailedMember is the id of the step that failed: the member id, or the
	// saga id when FailedAt is "saga". Empty on success and for "den".
	FailedMember string
}

// SagaTeardownError wraps a teardown-step failure with the walk's report.
// Code delegates to the cause, Details merge the cause's details with the
// report's completed steps and failure locator, and Unwrap keeps errors.As /
// errors.Is on the underlying error working.
type SagaTeardownError struct {
	Report SagaTeardownReport
	Cause  error
}

func (e *SagaTeardownError) Error() string { return e.Cause.Error() }
func (e *SagaTeardownError) Unwrap() error { return e.Cause }

// Code is the underlying cause's code (CodeUnknown when it carries none).
func (e *SagaTeardownError) Code() string { return ErrorCode(e.Cause) }

// Details returns the cause's details (if any) plus "completed" (always, an
// empty array when nothing was torn down), "failedAt" and "failedMember"
// (when a member or the saga space failed).
func (e *SagaTeardownError) Details() map[string]any {
	details := map[string]any{}
	for key, value := range ErrorDetails(e.Cause) {
		details[key] = value
	}
	completed := e.Report.Completed
	if completed == nil {
		completed = []SagaTeardownStep{}
	}
	details["completed"] = completed
	if e.Report.FailedAt != "" {
		details["failedAt"] = e.Report.FailedAt
	}
	if e.Report.FailedMember != "" {
		details["failedMember"] = e.Report.FailedMember
	}
	return details
}

// archiveEntriesFor snapshots the .archive/ entry names that belong to
// spaceID (exact name or the collision shape <id>-<14 digits>). Diffing
// before and after an Archive step yields its destination without parsing
// the success line.
func (s Service) archiveEntriesFor(spaceID string) map[string]bool {
	found := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.Config.AgentWorkDir, ".archive"))
	if err != nil {
		return found
	}
	for _, entry := range entries {
		if entry.IsDir() && archiveNameMatches(entry.Name(), spaceID) {
			found[entry.Name()] = true
		}
	}
	return found
}

// newArchiveEntry returns the .archive/ path that appeared for spaceID since
// the before snapshot, falling back to the exact-name destination when none
// is new (which a successful Archive never produces).
func (s Service) newArchiveEntry(spaceID string, before map[string]bool) string {
	root := filepath.Join(s.Config.AgentWorkDir, ".archive")
	for name := range s.archiveEntriesFor(spaceID) {
		if !before[name] {
			return filepath.Join(root, name)
		}
	}
	return filepath.Join(root, spaceID)
}
