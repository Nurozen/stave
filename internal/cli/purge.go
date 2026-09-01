package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Nurozen/stave/internal/space"
)

// spacesReferencingRepo walks agentWorkDir and returns the IDs of spaces whose
// manifest references the given bare repo: any RepoManifest entry (edit or
// reference) whose canonical BareRepoPath equals bareRepoPath's, or whose Name
// equals repoName. A directory without a .stave.yaml is not a space and is
// skipped; any other read or parse error fails closed and is returned naming
// the space directory. A missing agentWorkDir yields no spaces. The .archive
// directory is skipped: archived spaces have their worktrees pruned before
// archiving and there is no unarchive.
func spacesReferencingRepo(agentWorkDir, repoName, bareRepoPath string) ([]string, error) {
	entries, err := os.ReadDir(agentWorkDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan spaces in %s: %w", agentWorkDir, err)
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".archive" {
			continue
		}
		spacePath := filepath.Join(agentWorkDir, entry.Name())
		manifest, err := space.LoadManifest(spacePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // no .stave.yaml — not a space
			}
			return nil, fmt.Errorf("read space manifest in %s: %w", spacePath, err)
		}
		for _, repo := range manifest.Repos {
			if repo.Name == repoName || sameCanonicalPath(repo.BareRepoPath, bareRepoPath) {
				ids = append(ids, entry.Name())
				break
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// canonicalPath resolves symlinks best-effort and cleans the result so two
// spellings of the same on-disk location compare equal.
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
}

// sameCanonicalPath compares two paths after resolving symlinks and cleaning,
// so a bare repo reachable through a symlinked root still matches. When the
// strings still differ but both paths exist, the filesystem decides: on a
// case-insensitive volume two case variants name the same directory. Empty
// paths never match.
func sameCanonicalPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if canonicalPath(a) == canonicalPath(b) {
		return true
	}
	return sameFile(a, b)
}

// sameFile reports whether a and b both exist and are the same filesystem
// object, as decided by os.SameFile.
func sameFile(a, b string) bool {
	statA, errA := os.Stat(a)
	statB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(statA, statB)
}
