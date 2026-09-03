package space

import (
	"errors"
	"fmt"
	"os"
)

// Stable error codes for machine-readable (--json) consumers. GUI hosts key
// on these instead of parsing prose; the rendered message stays human-first.
const (
	CodeDirtyWorktrees     = "dirty_worktrees"
	CodeDependentSpaces    = "dependent_spaces"
	CodeMemoryInUse        = "memory_in_use"
	CodeSpaceExists        = "space_exists"
	CodeSpaceNotFound      = "space_not_found"
	CodeRepoNotFound       = "repo_not_found"
	CodeRepoNotInSpace     = "repo_not_in_space"
	CodeRepoAlreadyInSpace = "repo_already_in_space"
	CodeRepoModeAmbiguous  = "repo_mode_ambiguous"
	CodeSagaSpace          = "saga_space"
	CodeSagaMember         = "saga_member"
	CodeInvalidName        = "invalid_name"
	CodeBranchMissing      = "branch_missing"
	CodeAmbiguousArchive   = "ambiguous_archive"
	CodeArchiveNotFound    = "archive_not_found"
	CodeInvalidArguments   = "invalid_arguments"
	CodeUnknown            = "unknown"
)

// CodedError is an error carrying a stable machine-readable code. Any error
// type (in any package) satisfies it structurally; ErrorCode finds the first
// one in a wrap chain.
type CodedError interface {
	error
	Code() string
}

// Detailer optionally attaches structured details (repo lists, candidates,
// ...) to a CodedError so hosts need not re-derive them from the message.
type Detailer interface {
	Details() map[string]any
}

// ErrorCode returns the code of the first CodedError in err's chain, or
// CodeUnknown when none carries one (nil yields "").
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded CodedError
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return CodeUnknown
}

// ErrorDetails returns the details of the first Detailer in err's chain, or
// nil.
func ErrorDetails(err error) map[string]any {
	var detailer Detailer
	if errors.As(err, &detailer) {
		return detailer.Details()
	}
	return nil
}

// codedError attaches a code (and optional details) to an existing error
// without changing its rendered message; used where a dedicated type would
// only duplicate a one-off fmt.Errorf.
type codedError struct {
	code    string
	details map[string]any
	err     error
}

func (e *codedError) Error() string           { return e.err.Error() }
func (e *codedError) Unwrap() error           { return e.err }
func (e *codedError) Code() string            { return e.code }
func (e *codedError) Details() map[string]any { return e.details }

// coded wraps a freshly formatted error with a code (details may be nil).
func coded(code string, details map[string]any, format string, args ...any) error {
	return &codedError{code: code, details: details, err: fmt.Errorf(format, args...)}
}

// SpaceNotFoundError: no live space (no manifest) under the space id.
type SpaceNotFoundError struct {
	SpaceID string
	Path    string
}

func (e *SpaceNotFoundError) Error() string {
	return fmt.Sprintf("space %q is not live (no %s at %s)", e.SpaceID, ManifestName, e.Path)
}

func (e *SpaceNotFoundError) Code() string { return CodeSpaceNotFound }

// RepoNotFoundError: the repo name is not in the registry (config.repos).
type RepoNotFoundError struct {
	Repo string
}

func (e *RepoNotFoundError) Error() string {
	return fmt.Sprintf("repo %q is not registered", e.Repo)
}

func (e *RepoNotFoundError) Code() string { return CodeRepoNotFound }

func (e *RepoNotFoundError) Details() map[string]any {
	return map[string]any{"repo": e.Repo}
}

// DependentSpaceError: a teardown/remove refuses because another live space
// stacks one of its edit repos on a branch this operation would retire.
type DependentSpaceError struct {
	SpaceID   string
	Dependent string
	Repo      string
	Branch    string
}

func (e *DependentSpaceError) Error() string {
	return fmt.Sprintf("space %q repo %q stacks on branch %q of space %q (use --force to override)", e.Dependent, e.Repo, e.Branch, e.SpaceID)
}

func (e *DependentSpaceError) Code() string { return CodeDependentSpaces }

func (e *DependentSpaceError) Details() map[string]any {
	return map[string]any{"spaces": []string{e.Dependent}, "repo": e.Repo, "branch": e.Branch}
}

// SagaSpaceError: a single-space verb was pointed at a saga space; the
// saga-aware verbs (or --force) are the way forward.
type SagaSpaceError struct {
	SpaceID string
}

func (e *SagaSpaceError) Error() string {
	return fmt.Sprintf("space %q is a saga; use 'stave saga archive %s' or 'stave saga destroy %s' to tear it down with its members, or --force to override", e.SpaceID, e.SpaceID, e.SpaceID)
}

func (e *SagaSpaceError) Code() string { return CodeSagaSpace }

// SagaMemberError: a single-space verb was pointed at a registered saga
// member; drop it from the roster first (or --force).
type SagaMemberError struct {
	SpaceID string
	SagaID  string
}

func (e *SagaMemberError) Error() string {
	return fmt.Sprintf("space %q is a member of saga %q; use 'stave saga remove %s %s' to drop it from the roster first, or --force to override", e.SpaceID, e.SagaID, e.SagaID, e.SpaceID)
}

func (e *SagaMemberError) Code() string { return CodeSagaMember }

func (e *SagaMemberError) Details() map[string]any {
	return map[string]any{"saga": e.SagaID}
}

// Codes and details for the pre-existing typed errors.

func (e *DirtyWorktreeError) Code() string { return CodeDirtyWorktrees }

func (e *DirtyWorktreeError) Details() map[string]any {
	return map[string]any{"repos": e.Repos}
}

func (e *MemoryInUseError) Code() string { return CodeMemoryInUse }

func (e *RepoNotInSpaceError) Code() string { return CodeRepoNotInSpace }

func (e *RepoNotInSpaceError) Details() map[string]any {
	details := map[string]any{"repo": e.Repo}
	if e.Mode != "" {
		details["mode"] = string(e.Mode)
	}
	return details
}

func (e *RepoModeAmbiguousError) Code() string { return CodeRepoModeAmbiguous }

func (e *RepoModeAmbiguousError) Details() map[string]any {
	return map[string]any{"repo": e.Repo, "modes": e.Modes}
}

func (e *RepoAlreadyInSpaceError) Code() string { return CodeRepoAlreadyInSpace }

func (e *RepoAlreadyInSpaceError) Details() map[string]any {
	return map[string]any{"repo": e.Repo, "mode": string(e.Mode)}
}

func (e *SpaceExistsError) Code() string { return CodeSpaceExists }

func (e *SpaceExistsError) Details() map[string]any {
	return map[string]any{"path": e.Path}
}

func (e *ArchiveNotFoundError) Code() string { return CodeArchiveNotFound }

func (e *AmbiguousArchiveError) Code() string { return CodeAmbiguousArchive }

func (e *AmbiguousArchiveError) Details() map[string]any {
	return map[string]any{"candidates": e.Candidates}
}

func (e *BranchMissingError) Code() string { return CodeBranchMissing }

func (e *BranchMissingError) Details() map[string]any {
	return map[string]any{"repo": e.Repo, "branch": e.Branch}
}

// loadLiveManifest loads spaceID's manifest, typing a missing one as
// SpaceNotFoundError so verbs aimed at an absent space refuse uniformly.
func loadLiveManifest(spaceID, spacePath string) (Manifest, error) {
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, &SpaceNotFoundError{SpaceID: spaceID, Path: spacePath}
		}
		return Manifest{}, err
	}
	return manifest, nil
}
