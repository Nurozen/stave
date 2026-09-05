package space

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSagaArchiveWithReportSuccess: the report lists each archived member with
// its live root and .archive/ destination in walk order, and the saga space's
// own destination.
func TestSagaArchiveWithReportSuccess(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-rp"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-rp", "r-1")
	createMemberInSaga(t, svc, "epic-rp", "r-2", "r-1")
	// A pre-archived member is skipped: not a step, so not in Completed.
	createMemberInSaga(t, svc, "epic-rp", "r-3")
	if err := svc.Archive(ctx, ArchiveOptions{SpaceID: "r-3", Force: true}); err != nil {
		t.Fatal(err)
	}
	archiveRoot := filepath.Join(cfg.AgentWorkDir, ".archive")

	report, err := svc.SagaArchiveWithReport(ctx, "epic-rp", SagaArchiveOptions{})
	if err != nil {
		t.Fatalf("SagaArchiveWithReport error = %v", err)
	}
	if report.SagaID != "epic-rp" || report.Verb != "archive" || report.SagaPath != svc.SpacePath("epic-rp") || report.FailedAt != "" || report.FailedMember != "" {
		t.Fatalf("report header = %#v", report)
	}
	if !report.SagaTornDown || report.SagaArchivedPath != filepath.Join(archiveRoot, "epic-rp") {
		t.Fatalf("saga step not recorded: %#v", report)
	}
	want := []SagaTeardownStep{
		{ID: "r-2", Action: "archived", Path: svc.SpacePath("r-2"), ArchivedPath: filepath.Join(archiveRoot, "r-2")},
		{ID: "r-1", Action: "archived", Path: svc.SpacePath("r-1"), ArchivedPath: filepath.Join(archiveRoot, "r-1")},
	}
	if len(report.Completed) != len(want) {
		t.Fatalf("completed = %#v, want %#v", report.Completed, want)
	}
	for i := range want {
		if report.Completed[i] != want[i] {
			t.Fatalf("completed[%d] = %#v, want %#v", i, report.Completed[i], want[i])
		}
	}
}

// TestSagaArchiveWithReportCollisionPath: a member whose exact archive name is
// already taken lands under the <id>-<timestamp> shape, and the report carries
// the real destination rather than the exact-name guess.
func TestSagaArchiveWithReportCollisionPath(t *testing.T) {
	svc, _, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-cl"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-cl", "c-1")
	// Occupy .archive/c-1 with an unrelated (different incarnation) space so
	// the member still resolves live and Archive must disambiguate.
	occupied := filepath.Join(cfg.AgentWorkDir, ".archive", "c-1")
	if err := SaveManifest(mustMkdir(t, occupied), Manifest{ID: "c-1", CreatedAt: svc.now().AddDate(0, 0, -1)}); err != nil {
		t.Fatal(err)
	}
	report, err := svc.SagaArchiveWithReport(ctx, "epic-cl", SagaArchiveOptions{})
	if err != nil {
		t.Fatalf("SagaArchiveWithReport error = %v", err)
	}
	if len(report.Completed) != 1 {
		t.Fatalf("completed = %#v", report.Completed)
	}
	got := report.Completed[0].ArchivedPath
	if got == occupied || !strings.HasPrefix(filepath.Base(got), "c-1-") || !dirExists(t, got) {
		t.Fatalf("archived path %q should be the collision-suffixed destination", got)
	}
	if !archiveNameMatches(filepath.Base(got), "c-1") {
		t.Fatalf("archived path %q does not match the collision shape", got)
	}
}

// TestSagaDestroyWithReportSuccess: destroy steps carry live paths and no
// archived path; the saga space records SagaTornDown without a destination.
func TestSagaDestroyWithReportSuccess(t *testing.T) {
	svc, _, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-dr"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-dr", "d-1")
	report, err := svc.SagaDestroyWithReport(ctx, "epic-dr", SagaDestroyOptions{})
	if err != nil {
		t.Fatalf("SagaDestroyWithReport error = %v", err)
	}
	if report.Verb != "destroy" || !report.SagaTornDown || report.SagaArchivedPath != "" {
		t.Fatalf("report = %#v", report)
	}
	if len(report.Completed) != 1 || report.Completed[0] != (SagaTeardownStep{ID: "d-1", Action: "destroyed", Path: svc.SpacePath("d-1")}) {
		t.Fatalf("completed = %#v", report.Completed)
	}
}

// TestSagaArchiveWithReportPartialFailure injects a git failure at the second
// member: the error is a *SagaTeardownError whose report lists the first
// member's completed step, names the failed member, and whose Details merge
// the walk state; the plain SagaArchive still surfaces the same error text.
func TestSagaArchiveWithReportPartialFailure(t *testing.T) {
	svc, fg, cfg := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-pr"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-pr", "p-1")
	createMemberInSaga(t, svc, "epic-pr", "p-2", "p-1")
	fg.removeFn = func(bare, path string) error {
		if strings.Contains(path, string(filepath.Separator)+"p-1"+string(filepath.Separator)) {
			return errors.New("worktree removal exploded")
		}
		return nil
	}
	report, err := svc.SagaArchiveWithReport(ctx, "epic-pr", SagaArchiveOptions{})
	var teardownErr *SagaTeardownError
	if !errors.As(err, &teardownErr) {
		t.Fatalf("error %T %v is not a *SagaTeardownError", err, err)
	}
	if !strings.Contains(err.Error(), "member p-1") || !strings.Contains(err.Error(), "worktree removal exploded") {
		t.Fatalf("error text = %v", err)
	}
	if teardownErr.Code() != CodeUnknown || ErrorCode(err) != CodeUnknown {
		t.Fatalf("code = %q / %q, want unknown (cause carries none)", teardownErr.Code(), ErrorCode(err))
	}
	// The returned report and the wrapped one agree.
	if report.FailedAt != "member" || report.FailedMember != "p-1" || teardownErr.Report.FailedMember != "p-1" {
		t.Fatalf("failure locator = %#v", report)
	}
	archived := filepath.Join(cfg.AgentWorkDir, ".archive", "p-2")
	if len(report.Completed) != 1 || report.Completed[0] != (SagaTeardownStep{ID: "p-2", Action: "archived", Path: svc.SpacePath("p-2"), ArchivedPath: archived}) {
		t.Fatalf("completed = %#v", report.Completed)
	}
	if report.SagaTornDown || report.SagaArchivedPath != "" {
		t.Fatalf("saga step recorded despite the stop: %#v", report)
	}
	details := ErrorDetails(err)
	steps, ok := details["completed"].([]SagaTeardownStep)
	if !ok || len(steps) != 1 || steps[0].ID != "p-2" {
		t.Fatalf("details.completed = %#v", details["completed"])
	}
	if details["failedMember"] != "p-1" || details["failedAt"] != "member" {
		t.Fatalf("details = %#v", details)
	}

	// The report-less entry point returns the same wrapper, so errors.As on
	// the underlying cause chain keeps working for existing callers.
	fg.calls = nil
	err = svc.SagaArchive(ctx, "epic-pr", SagaArchiveOptions{})
	if !errors.As(err, &teardownErr) || teardownErr.Report.FailedMember != "p-1" {
		t.Fatalf("SagaArchive error = %T %v", err, err)
	}
	// On the retry p-2 is already archived: nothing completed before the stop.
	if len(teardownErr.Report.Completed) != 0 || len(teardownErr.Details()["completed"].([]SagaTeardownStep)) != 0 {
		t.Fatalf("retry report completed = %#v", teardownErr.Report.Completed)
	}
}

// TestSagaTeardownErrorMergesCauseDetails: a coded cause keeps its code and
// its details alongside the walk's completed steps; guard refusals raised in
// preflight are NOT wrapped (nothing was torn down).
func TestSagaTeardownErrorMergesCauseDetails(t *testing.T) {
	cause := &DirtyWorktreeError{SpaceID: "m-1", Repos: []string{"api"}}
	err := &SagaTeardownError{
		Report: SagaTeardownReport{FailedAt: "saga", FailedMember: "epic-x", Completed: []SagaTeardownStep{{ID: "m-1", Action: "destroyed", Path: "/w/m-1"}}},
		Cause:  cause,
	}
	if ErrorCode(err) != CodeDirtyWorktrees {
		t.Fatalf("code = %q", ErrorCode(err))
	}
	details := ErrorDetails(err)
	if repos, _ := details["repos"].([]string); len(repos) != 1 || repos[0] != "api" {
		t.Fatalf("cause details lost: %#v", details)
	}
	if details["failedAt"] != "saga" || details["failedMember"] != "epic-x" {
		t.Fatalf("locator missing: %#v", details)
	}
	var dirty *DirtyWorktreeError
	if !errors.As(err, &dirty) || dirty != cause {
		t.Fatal("Unwrap does not reach the cause")
	}

	svc, fg, _ := testService(t)
	ctx := context.Background()
	if err := svc.CreateSaga(ctx, SagaCreateOptions{ID: "epic-gd"}); err != nil {
		t.Fatal(err)
	}
	createMemberInSaga(t, svc, "epic-gd", "g-1")
	fg.dirty = map[string]bool{filepath.Join(svc.SpacePath("g-1"), "repo-a"): true}
	_, err2 := svc.SagaArchiveWithReport(ctx, "epic-gd", SagaArchiveOptions{})
	var wrapped *SagaTeardownError
	if errors.As(err2, &wrapped) || ErrorCode(err2) != CodeDirtyWorktrees {
		t.Fatalf("preflight refusal should not be wrapped: %T %v", err2, err2)
	}
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
