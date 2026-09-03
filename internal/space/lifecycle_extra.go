package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nurozen/stave/internal/config"
)

// RemoveOptions drives RemoveRepo (`stave space remove`), the inverse of
// AddRepo.
type RemoveOptions struct {
	SpaceID  string
	RepoName string
	// Mode selects which entry to remove when the repo is present both as an
	// edit and as a reference. Empty means "the only entry"; with two entries
	// present it is a RepoModeAmbiguousError.
	Mode RepoMode
	// Force skips the dirty-worktree and dependent-stacked-space guards for
	// edit repos.
	Force  bool
	DryRun bool
	// sagaLockHeld: the caller already holds this space's per-saga lock (see
	// AddOptions.sagaLockHeld).
	sagaLockHeld bool
}

// RestoreOptions drives Restore (`stave space restore`), the inverse of
// Archive.
type RestoreOptions struct {
	SpaceID string
	// From names the `.archive/` entry to restore explicitly. Empty resolves
	// the exact `<space-id>` entry, then a single `<space-id>-<timestamp>`
	// candidate (several candidates refuse with AmbiguousArchiveError).
	From   string
	DryRun bool
}

// selectRepoEntry picks the manifest entry `space remove` acts on. A repo
// may legitimately appear twice (edit + reference); then the caller must say
// which mode it means.
func selectRepoEntry(manifest Manifest, spaceID, repoName string, mode RepoMode) (RepoManifest, int, error) {
	var matches []int
	for i, entry := range manifest.Repos {
		if entry.Name != repoName {
			continue
		}
		if mode != "" && entry.Mode != mode {
			continue
		}
		matches = append(matches, i)
	}
	switch len(matches) {
	case 0:
		return RepoManifest{}, -1, &RepoNotInSpaceError{SpaceID: spaceID, Repo: repoName, Mode: mode}
	case 1:
		return manifest.Repos[matches[0]], matches[0], nil
	default:
		modes := make([]string, 0, len(matches))
		for _, i := range matches {
			modes = append(modes, string(manifest.Repos[i].Mode))
		}
		return RepoManifest{}, -1, &RepoModeAmbiguousError{SpaceID: spaceID, Repo: repoName, Modes: modes}
	}
}

// RepoModeAmbiguousError: the repo is present in more than one mode and the
// caller did not say which entry to remove (`--edit` / `--reference`).
type RepoModeAmbiguousError struct {
	SpaceID string
	Repo    string
	Modes   []string
}

func (e *RepoModeAmbiguousError) Error() string {
	return fmt.Sprintf("space %q has repo %q as both %s; pass --edit or --reference to choose", e.SpaceID, e.Repo, strings.Join(e.Modes, " and "))
}

// RepoNotInSpaceError: the manifest lists no repo by that name (or not in
// the requested mode when Mode is set).
type RepoNotInSpaceError struct {
	SpaceID string
	Repo    string
	Mode    RepoMode
}

func (e *RepoNotInSpaceError) Error() string {
	if e.Mode != "" {
		return fmt.Sprintf("space %q has no %s repo %q", e.SpaceID, e.Mode, e.Repo)
	}
	return fmt.Sprintf("space %q has no repo %q", e.SpaceID, e.Repo)
}

// RepoAlreadyInSpaceError: `space add` refuses a repo the manifest already
// lists in the same mode. The other mode stays allowed (edit + reference of
// one repo is a supported layout); a same-mode duplicate would make
// `space remove` and the generated AGENTS.md ambiguous.
type RepoAlreadyInSpaceError struct {
	SpaceID string
	Repo    string
	Mode    RepoMode
}

func (e *RepoAlreadyInSpaceError) Error() string {
	return fmt.Sprintf("space %q already has repo %q as %s", e.SpaceID, e.Repo, e.Mode)
}

// SpaceExistsError: restore refuses because a live space (or any directory)
// already occupies the target path.
type SpaceExistsError struct {
	SpaceID string
	Path    string
}

func (e *SpaceExistsError) Error() string {
	return fmt.Sprintf("space %q already exists at %s; archive or destroy it before restoring", e.SpaceID, e.Path)
}

// ArchiveNotFoundError: no `.archive/` entry matches the space id.
type ArchiveNotFoundError struct {
	SpaceID     string
	ArchiveRoot string
}

func (e *ArchiveNotFoundError) Error() string {
	return fmt.Sprintf("no archive of space %q under %s", e.SpaceID, e.ArchiveRoot)
}

// AmbiguousArchiveError: several timestamped archives match the space id and
// none is the exact name; the caller must pick one with --from.
type AmbiguousArchiveError struct {
	SpaceID    string
	Candidates []string
}

func (e *AmbiguousArchiveError) Error() string {
	return fmt.Sprintf("space %q has %d archived copies (%s); pass --from <name> to choose one", e.SpaceID, len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// BranchMissingError: an archived edit repo's recorded branch no longer
// exists in the bare repo, so its worktree cannot be re-created.
type BranchMissingError struct {
	Repo   string
	Branch string
}

func (e *BranchMissingError) Error() string {
	return fmt.Sprintf("repo %q: branch %q no longer exists in the bare repo; restore refused (archive left intact)", e.Repo, e.Branch)
}

// RemoveRepo detaches one repo from a live space: the worktree is removed and
// pruned from the shared bare repo, the manifest entry dropped, AGENTS.md
// regenerated. Edit repos refuse when dirty or when another live space stacks
// on their branch unless opts.Force.
//
// The edit branch itself is NEVER deleted: Stave never deletes branches. It
// stays in the bare repo (as after archive), so a later `space add --edit`
// with the same branch adopts it and any stacked space's base keeps
// resolving.
func (s Service) RemoveRepo(ctx context.Context, opts RemoveOptions) error {
	spacePath, err := s.resolveSpacePath(opts.SpaceID)
	if err != nil {
		return err
	}
	if err := config.ValidateName("repo name", opts.RepoName); err != nil {
		return err
	}
	manifest, err := loadLiveManifest(opts.SpaceID, spacePath)
	if err != nil {
		return err
	}
	repo, index, err := selectRepoEntry(manifest, opts.SpaceID, opts.RepoName, opts.Mode)
	if err != nil {
		return err
	}
	// Saga spaces serialize manifest writers under the per-saga lock;
	// re-run under it (re-loading inside) unless the caller holds it.
	if manifest.Saga != nil && !opts.DryRun && !opts.sagaLockHeld {
		locked := opts
		locked.sagaLockHeld = true
		return s.withSagaLock(opts.SpaceID, func() error { return s.RemoveRepo(ctx, locked) })
	}
	worktreePath := filepath.Join(spacePath, repo.Path)
	if repo.Mode == ModeEdit && !opts.Force {
		// Both guards run against a one-repo view of the manifest so only
		// THIS repo's dirtiness / dependents can refuse.
		only := Manifest{ID: manifest.ID, Repos: []RepoManifest{repo}}
		if err := s.guardRefusal(s.ensureNoDirtyEdits(ctx, spacePath, only), opts.DryRun); err != nil {
			return err
		}
		if err := s.guardRefusal(s.ensureNoDependentSpaces(opts.SpaceID, only, nil), opts.DryRun); err != nil {
			return err
		}
	}
	if opts.DryRun {
		s.printf("dry-run: remove worktree %s\n", worktreePath)
		s.printf("dry-run: prune worktrees of %s\n", repo.BareRepoPath)
		s.printf("dry-run: drop %s from %s\n", repo.Name, filepath.Join(spacePath, ManifestName))
		if repo.Mode == ModeEdit {
			s.printf("dry-run: branch %s is kept in the bare repo (stave never deletes branches)\n", repo.Branch)
		}
		s.noticeMemoryLinkKept(manifest, repo)
		return nil
	}
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	if _, statErr := os.Lstat(worktreePath); errors.Is(statErr, os.ErrNotExist) {
		s.printf("notice: worktree %s is already missing; pruning its registration\n", worktreePath)
	} else if err := s.Git.WorktreeRemove(ctx, repo.BareRepoPath, worktreePath, true); err != nil {
		return err
	}
	if err := s.Git.WorktreePrune(ctx, repo.BareRepoPath); err != nil {
		return err
	}
	// Branch deliberately untouched (see the doc comment above).
	manifest.Repos = append(manifest.Repos[:index:index], manifest.Repos[index+1:]...)
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("removed %s from %s\n", repo.Name, opts.SpaceID)
	s.noticeMemoryLinkKept(manifest, repo)
	return nil
}

// noticeMemoryLinkKept explains that a removed reference's memory link (made
// by linkMemoryOnAdd / attach) stays: the provider seam has no unlink verb,
// so RemoveRepo deliberately invents none.
func (s Service) noticeMemoryLinkKept(manifest Manifest, repo RepoManifest) {
	if repo.Mode != ModeReference || len(manifest.Memories) == 0 {
		return
	}
	s.printf("notice: any memory link for reference %s is left in place (the memory provider has no unlink verb)\n", repo.Name)
}

// Restore moves an archived space back under AgentWorkDir and re-creates its
// worktrees from the manifest: edit repos at their recorded branch (which
// Archive left in the bare repo), references detached at their recorded ref.
// All edit branches are verified BEFORE any mutation so a refusal leaves the
// archive intact; a reference whose ref is gone is skipped with a warning
// (the rest of the space still restores). Memory reverse routes are moved
// back best-effort (the inverse of Archive's relocation).
func (s Service) Restore(ctx context.Context, opts RestoreOptions) error {
	spacePath, err := s.resolveSpacePath(opts.SpaceID)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(spacePath); statErr == nil {
		return &SpaceExistsError{SpaceID: opts.SpaceID, Path: spacePath}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	archivedPath, err := s.locateArchive(opts.SpaceID, opts.From)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(archivedPath)
	if err != nil {
		return fmt.Errorf("archive %s: %w", archivedPath, err)
	}
	if manifest.ID != opts.SpaceID {
		return fmt.Errorf("archive %s holds space %q, not %q", archivedPath, manifest.ID, opts.SpaceID)
	}

	// Preflight every repo before touching anything.
	skipRefs := map[string]string{} // repo name → missing ref
	for _, repo := range manifest.Repos {
		switch repo.Mode {
		case ModeEdit:
			exists, err := s.Git.BranchExists(ctx, repo.BareRepoPath, repo.Branch)
			if err != nil {
				return fmt.Errorf("repo %q: %w", repo.Name, err)
			}
			if !exists {
				return &BranchMissingError{Repo: repo.Name, Branch: repo.Branch}
			}
		case ModeReference:
			exists, err := s.Git.RefExists(ctx, repo.BareRepoPath, fullRefName(repo.Ref))
			if err != nil {
				return fmt.Errorf("repo %q: %w", repo.Name, err)
			}
			if !exists {
				skipRefs[repo.Name] = repo.Ref
			}
		}
	}

	// Resolve symlinks in the archive path WHILE IT STILL EXISTS (see
	// Archive: marmot normalizes route keys via EvalSymlinks).
	routeFrom := archivedPath
	if resolved, err := filepath.EvalSymlinks(archivedPath); err == nil {
		routeFrom = resolved
	}
	if opts.DryRun {
		s.printf("dry-run: restore %s -> %s\n", archivedPath, spacePath)
		for _, repo := range manifest.Repos {
			worktreePath := filepath.Join(spacePath, repo.Path)
			switch repo.Mode {
			case ModeEdit:
				s.printf("dry-run: add edit worktree %s at %s\n", repo.Branch, worktreePath)
			case ModeReference:
				if ref, missing := skipRefs[repo.Name]; missing {
					s.printf("dry-run: would skip reference %s: ref %s not found in %s\n", repo.Name, ref, repo.BareRepoPath)
					continue
				}
				s.printf("dry-run: add reference worktree %s at %s\n", repo.Ref, worktreePath)
			}
		}
		s.relocateMemoryRoutes(ctx, routeFrom, spacePath, manifest, true)
		s.noteSagaAfterRestore(opts.SpaceID, manifest)
		return nil
	}

	if err := os.Rename(archivedPath, spacePath); err != nil {
		return err
	}
	for _, repo := range manifest.Repos {
		worktreePath := filepath.Join(spacePath, repo.Path)
		switch repo.Mode {
		case ModeEdit:
			if err := s.Git.WorktreeAddExisting(ctx, repo.BareRepoPath, worktreePath, repo.Branch); err != nil {
				return fmt.Errorf("restore %s: repo %q: %w (space directory is back at %s; re-run restore after moving it to %s, or add the repo again)", opts.SpaceID, repo.Name, err, spacePath, archivedPath)
			}
			s.printf("restored edit %s at %s (branch %s)\n", repo.Name, worktreePath, repo.Branch)
		case ModeReference:
			if ref, missing := skipRefs[repo.Name]; missing {
				s.printf("warning: reference %s skipped: ref %s not found in %s (run 'stave repos sync' and 'stave space add %s %s --reference')\n", repo.Name, ref, repo.BareRepoPath, opts.SpaceID, repo.Name)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(worktreePath), config.DefaultDirMode); err != nil {
				return err
			}
			if err := s.Git.WorktreeAddDetached(ctx, repo.BareRepoPath, worktreePath, repo.Ref); err != nil {
				return fmt.Errorf("restore %s: repo %q: %w", opts.SpaceID, repo.Name, err)
			}
			s.printf("restored reference %s at %s (%s)\n", repo.Name, worktreePath, repo.Ref)
		}
	}
	routeTo := spacePath
	if resolved, err := filepath.EvalSymlinks(spacePath); err == nil {
		routeTo = resolved
	}
	s.relocateMemoryRoutes(ctx, routeFrom, routeTo, manifest, false)
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("restored %s -> %s\n", opts.SpaceID, spacePath)
	s.noteSagaAfterRestore(opts.SpaceID, manifest)
	return nil
}

// locateArchive resolves the `.archive/` entry to restore. from, when set,
// must be a plain entry name (ValidateName rejects separators and
// traversal). Otherwise: the exact id, else the single timestamped
// candidate, else ArchiveNotFoundError / AmbiguousArchiveError.
func (s Service) locateArchive(spaceID, from string) (string, error) {
	archiveRoot := filepath.Join(s.Config.AgentWorkDir, ".archive")
	if from != "" {
		if err := config.ValidateName("archive name", from); err != nil {
			return "", err
		}
		path := filepath.Join(archiveRoot, from)
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("archive %q: %w", from, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("archive %q is not a directory", from)
		}
		return path, nil
	}
	exact := filepath.Join(archiveRoot, spaceID)
	if info, err := os.Stat(exact); err == nil && info.IsDir() {
		return exact, nil
	}
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &ArchiveNotFoundError{SpaceID: spaceID, ArchiveRoot: archiveRoot}
		}
		return "", err
	}
	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() && archiveNameMatches(entry.Name(), spaceID) {
			candidates = append(candidates, entry.Name())
		}
	}
	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", &ArchiveNotFoundError{SpaceID: spaceID, ArchiveRoot: archiveRoot}
	case 1:
		return filepath.Join(archiveRoot, candidates[0]), nil
	default:
		return "", &AmbiguousArchiveError{SpaceID: spaceID, Candidates: candidates}
	}
}

// noteSagaAfterRestore prints the saga-awareness note: restoring never
// touches rosters, so saga state is only refreshed by `stave saga sync`.
func (s Service) noteSagaAfterRestore(spaceID string, manifest Manifest) {
	if manifest.Saga != nil {
		s.printf("note: %s is a saga; member state may be stale until 'stave saga sync %s'\n", spaceID, spaceID)
		return
	}
	entries, err := s.ListSpaces()
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Err != nil || entry.Manifest.Saga == nil {
			continue
		}
		for _, member := range entry.Manifest.Saga.Members {
			if member.ID == spaceID {
				s.printf("note: %s is a member of saga %s; its status may be stale until 'stave saga sync %s'\n", spaceID, entry.ID, entry.ID)
				return
			}
		}
	}
}

// fullRefName maps a manifest ref spelling to the full ref RefExists probes:
// origin/<b> → refs/remotes/origin/<b>; refs/... verbatim; anything else is
// assumed to be a branch under refs/heads/.
func fullRefName(ref string) string {
	switch {
	case strings.HasPrefix(ref, "refs/"):
		return ref
	case strings.HasPrefix(ref, "origin/"):
		return "refs/remotes/" + ref
	default:
		return "refs/heads/" + ref
	}
}
