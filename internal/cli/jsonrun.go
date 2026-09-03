package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// jsonErrorEnvelope is the --json failure shape: {"error": {code, message,
// details?}} on STDOUT with exit 1, so GUI hosts never parse prose.
type jsonErrorEnvelope struct {
	Error jsonErrorBody `json:"error"`
}

type jsonErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// jsonExitError wraps a command error whose envelope has already been written
// to stdout; main honors ExitCode/Silent and prints nothing more.
type jsonExitError struct {
	err error
}

func (e jsonExitError) Error() string { return e.err.Error() }
func (e jsonExitError) Unwrap() error { return e.err }
func (e jsonExitError) ExitCode() int { return 1 }
func (e jsonExitError) Silent() bool  { return true }

// invalidArgumentsError is a CLI-layer usage refusal (flag combinations,
// reserved values) carrying the invalid_arguments code.
type invalidArgumentsError struct {
	msg string
}

func (e *invalidArgumentsError) Error() string { return e.msg }
func (e *invalidArgumentsError) Code() string  { return space.CodeInvalidArguments }

func argErrorf(format string, args ...any) error {
	return &invalidArgumentsError{msg: fmt.Sprintf(format, args...)}
}

// runJSON runs fn. With enabled unset it is transparent: fn's error (if any)
// returns as-is and fn's value is ignored — the human output path already
// printed. With enabled set the success value is encoded to stdout, and an
// error becomes the {"error": ...} envelope on stdout plus a silent exit-1
// error so main.go does not print it a second time.
func runJSON(cmd *cobra.Command, enabled bool, fn func() (any, error)) error {
	value, err := fn()
	if !enabled {
		return err
	}
	if err != nil {
		envelope := jsonErrorEnvelope{Error: jsonErrorBody{
			Code:    space.ErrorCode(err),
			Message: err.Error(),
			Details: space.ErrorDetails(err),
		}}
		if werr := writeJSON(cmd.OutOrStdout(), envelope); werr != nil {
			return werr
		}
		return jsonExitError{err: err}
	}
	return writeJSON(cmd.OutOrStdout(), value)
}

// outputSink is where a verb's service output (notices, dry-run plan lines,
// the success line) goes: straight to the command's stdout in human mode, or
// into a buffer that --json turns into notes / plan lines.
type outputSink struct {
	json bool
	buf  bytes.Buffer
	out  io.Writer
}

func newOutputSink(cmd *cobra.Command, json bool) *outputSink {
	return &outputSink{json: json, out: cmd.OutOrStdout()}
}

// Writer returns the io.Writer the service (and any CLI-side notices) should
// print to.
func (s *outputSink) Writer() io.Writer {
	if s.json {
		return &s.buf
	}
	return s.out
}

// Lines returns everything captured so far, one entry per non-empty line.
func (s *outputSink) Lines() []string {
	return splitOutputLines(s.buf.String())
}

// Notes returns the captured lines minus the verb's own success line(s), i.e.
// exactly the notices a human run would have shown besides the result.
func (s *outputSink) Notes(primary ...string) []string {
	drop := make(map[string]bool, len(primary))
	for _, line := range primary {
		drop[line] = true
	}
	var notes []string
	for _, line := range s.Lines() {
		if !drop[line] {
			notes = append(notes, line)
		}
	}
	return notes
}

func splitOutputLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// dryRunJSON is the --dry-run --json shape for every mutating verb.
type dryRunJSON struct {
	DryRun bool     `json:"dryRun"`
	Plan   []string `json:"plan"`
}

func dryRunPayload(sink *outputSink) dryRunJSON {
	plan := sink.Lines()
	if plan == nil {
		plan = []string{}
	}
	return dryRunJSON{DryRun: true, Plan: plan}
}

// spaceMutationJSON is the success shape of space create/add/remove/restore:
// the manifest is reloaded from disk after the operation.
type spaceMutationJSON struct {
	SpaceID   string         `json:"spaceId"`
	SpacePath string         `json:"spacePath"`
	Manifest  space.Manifest `json:"manifest"`
	Notes     []string       `json:"notes,omitempty"`
}

func spaceMutationPayload(svc space.Service, spaceID string, sink *outputSink, primary ...string) (spaceMutationJSON, error) {
	spacePath := svc.SpacePath(spaceID)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return spaceMutationJSON{}, err
	}
	return spaceMutationJSON{SpaceID: spaceID, SpacePath: spacePath, Manifest: manifest, Notes: sink.Notes(primary...)}, nil
}

// spaceArchiveJSON is the success shape of space archive.
type spaceArchiveJSON struct {
	SpaceID      string   `json:"spaceId"`
	ArchivedPath string   `json:"archivedPath"`
	Memory       string   `json:"memory"`
	Notes        []string `json:"notes,omitempty"`
}

// spaceDestroyJSON is the success shape of space destroy.
type spaceDestroyJSON struct {
	SpaceID   string   `json:"spaceId"`
	SpacePath string   `json:"spacePath"`
	Destroyed bool     `json:"destroyed"`
	Memory    string   `json:"memory"`
	Notes     []string `json:"notes,omitempty"`
}

// sagaMutationJSON is the success shape of saga create/add/remove.
type sagaMutationJSON struct {
	SagaID    string         `json:"sagaId"`
	SpacePath string         `json:"spacePath"`
	Manifest  space.Manifest `json:"manifest"`
	Notes     []string       `json:"notes,omitempty"`
}

func sagaMutationPayload(svc space.Service, sagaID string, sink *outputSink, primary ...string) (sagaMutationJSON, error) {
	spacePath := svc.SpacePath(sagaID)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return sagaMutationJSON{}, err
	}
	return sagaMutationJSON{SagaID: sagaID, SpacePath: spacePath, Manifest: manifest, Notes: sink.Notes(primary...)}, nil
}

// sagaTeardownJSON is the success shape of saga archive/destroy: one row per
// member in teardown order mirroring what the human walk reported.
type sagaTeardownJSON struct {
	SagaID  string                 `json:"sagaId"`
	Action  string                 `json:"action"`
	Memory  string                 `json:"memory"`
	Members []sagaMemberResultJSON `json:"members"`
	Notes   []string               `json:"notes,omitempty"`
}

type sagaMemberResultJSON struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// sagaTeardownMembers translates the pre-operation member states into the
// per-member outcome of a completed walk: live members took the verb,
// archived and missing members were skipped with the human walk's reason.
func sagaTeardownMembers(states []space.SagaMemberState, destroy bool) []sagaMemberResultJSON {
	done := "archived"
	if destroy {
		done = "destroyed"
	}
	members := make([]sagaMemberResultJSON, 0, len(states))
	for _, state := range states {
		row := sagaMemberResultJSON{ID: state.ID, Action: done}
		switch state.State {
		case space.MemberArchived:
			row.Action = "skipped"
			if destroy {
				row.Note = fmt.Sprintf("archived at %s; destroy leaves archives in place", state.Detail)
			} else {
				row.Note = fmt.Sprintf("already archived at %s", state.Detail)
			}
		case space.MemberMissing:
			row.Action = "skipped"
			row.Note = "missing"
		}
		members = append(members, row)
	}
	return members
}

// archiveEntries lists the .archive/ entries that belong to spaceID (the exact
// name or the Archive collision shape <id>-<timestamp>). Diffing before and
// after an archive yields the destination without parsing the success line.
func archiveEntries(svc space.Service, spaceID string) map[string]bool {
	found := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(svc.Config.AgentWorkDir, ".archive"))
	if err != nil {
		return found
	}
	for _, entry := range entries {
		if entry.Name() == spaceID || strings.HasPrefix(entry.Name(), spaceID+"-") {
			found[entry.Name()] = true
		}
	}
	return found
}

// archivedPathAfter picks the archive entry that appeared during the
// operation; when none is new (should not happen) it falls back to the exact
// name.
func archivedPathAfter(svc space.Service, spaceID string, before map[string]bool) string {
	root := filepath.Join(svc.Config.AgentWorkDir, ".archive")
	for name := range archiveEntries(svc, spaceID) {
		if !before[name] {
			return filepath.Join(root, name)
		}
	}
	return filepath.Join(root, spaceID)
}
