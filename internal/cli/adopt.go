package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Nurozen/stave/internal/gh"
	"github.com/Nurozen/stave/internal/git"
	"github.com/spf13/cobra"
)

// adoptBareRepo validates that the directory already sitting at barePath is a
// bare clone of url and, outside dry-run, rewrites its origin URL to the
// spelling being registered. The caller then runs finalizeBareMirror exactly
// as it would after a fresh clone, so an adopted cache reaches the same
// steady state stave relies on (tracking refspec, fetched refs, origin/HEAD,
// default branch); pre-existing local branches in the cache are left as-is.
//
// The origin URL is read as configured (RemoteConfigURL), not as git would
// rewrite it through url.<base>.insteadOf, so the comparison sees the spelling
// the user wrote and a later rollback never persists a rewritten alias.
//
// The returned adoption records what the cache looked like before this call so
// a caller whose later steps fail can put it back with restoreAdoption:
// prevOrigin is non-empty only when the URL was actually rewritten, and
// prevFetch holds the remote.origin.fetch values that finalizeBareMirror is
// about to replace.
func adoptBareRepo(ctx context.Context, cmd *cobra.Command, client *git.Client, barePath, name, url string, dryRun bool) (adoption, error) {
	reset := fmt.Sprintf("delete it and re-run 'stave repos add %s %s' to clone", shellQuote(name), pasteableURL(url))
	isBare, err := client.IsBareRepo(ctx, barePath)
	if err != nil {
		// IsBareRepo probes barePath as a git dir, so a non-bare clone (whose
		// git dir is barePath/.git) errors the same way a plain directory
		// does. Probe the nested .git to tell the two apart.
		if _, nested := client.IsBareRepo(ctx, filepath.Join(barePath, ".git")); nested == nil {
			isBare = false
		} else {
			return adoption{}, fmt.Errorf("cannot adopt %s: not a git repository (%w) — %s", barePath, err, reset)
		}
	}
	if !isBare {
		return adoption{}, fmt.Errorf("cannot adopt %s: not a bare repository — %s", barePath, reset)
	}
	origin, err := client.RemoteConfigURL(ctx, barePath, "origin")
	if err != nil {
		return adoption{}, fmt.Errorf("cannot adopt %s: could not read origin remote: %w — %s", barePath, err, reset)
	}
	if ok, reason := sameRepoURL(origin, url); !ok {
		return adoption{}, fmt.Errorf("cannot adopt %s: %s — %s", barePath, reason, reset)
	}
	if dryRun {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: would adopt existing bare repo at %s\n", barePath)
		if origin != url {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: would set origin of %s to %s\n", barePath, redactURL(url))
		}
		return adoption{}, nil
	}
	prevFetch, err := client.RemoteFetchRefspecs(ctx, barePath, "origin")
	if err != nil {
		return adoption{}, fmt.Errorf("cannot adopt %s: could not read remote.origin.fetch: %w — %s", barePath, err, reset)
	}
	adopted := adoption{prevFetch: prevFetch}
	if origin != url {
		if err := client.SetRemoteURL(ctx, barePath, url); err != nil {
			return adoption{}, fmt.Errorf("set origin url for %q: %w", name, err)
		}
		adopted.prevOrigin = origin
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "note: adopted existing bare repo at %s\n", barePath)
	return adopted, nil
}

// adoption is what adoptBareRepo changed or is about to change in an existing
// cache, kept so a failed registration can be rolled back.
type adoption struct {
	// prevOrigin is the raw origin URL before adoption; "" when it was not
	// rewritten.
	prevOrigin string
	// prevFetch is every remote.origin.fetch value before adoption; nil when
	// the cache had none.
	prevFetch []string
}

// restoreAdoption rolls an adopted cache back after a failed registration:
// origin is re-pointed at its previous URL when it was rewritten, and a
// custom fetch refspec that stave's standard one replaced is put back. Each
// outcome is folded into cause. Rollback runs even when ctx is already
// canceled (Ctrl-C is a likely reason the registration failed), because a
// cache left pointing at a registration that did not happen is worse than a
// slightly slower exit.
func restoreAdoption(ctx context.Context, client *git.Client, barePath string, prev adoption, cause error) error {
	if prev.prevOrigin != "" {
		cause = restoreOrigin(ctx, client, barePath, prev.prevOrigin, cause)
	}
	return restoreFetchRefspec(ctx, client, barePath, prev.prevFetch, cause)
}

// restoreOrigin best-effort re-points origin of the bare repo at prevOrigin
// after a failed adoption and folds the outcome into cause, so a cache whose
// URL was rewritten by adoptBareRepo is never left pointing at a registration
// that did not happen. It ignores cancellation of ctx; see restoreAdoption.
func restoreOrigin(ctx context.Context, client *git.Client, barePath, prevOrigin string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if restoreErr := client.SetRemoteURL(ctx, barePath, prevOrigin); restoreErr != nil {
		return fmt.Errorf("%w; could not restore origin to %s: %w", cause, redactURL(prevOrigin), restoreErr)
	}
	return fmt.Errorf("%w; origin restored to %s", cause, redactURL(prevOrigin))
}

// restoreFetchRefspec puts back the remote.origin.fetch value an adopted cache
// had before finalizeBareMirror replaced it with git.StandardFetchRefspec,
// folding the outcome into cause. It ignores cancellation of ctx; see
// restoreAdoption.
//
// Only a single custom value is restored. A cache that had no refspec keeps
// stave's standard one, which is harmless for any bare mirror. When several
// values were configured a single-value 'git config' write cannot replace
// them, so tracking configuration itself fails and they are left untouched;
// should they nevertheless turn out replaced, the error says so rather than
// guessing at a multi-value restore. Nothing is written when the current
// value still equals the prior one (the failure came before tracking ran).
func restoreFetchRefspec(ctx context.Context, client *git.Client, barePath string, prevFetch []string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if len(prevFetch) == 0 || (len(prevFetch) == 1 && prevFetch[0] == git.StandardFetchRefspec) {
		return cause
	}
	current, err := client.RemoteFetchRefspecs(ctx, barePath, "origin")
	if err != nil {
		return fmt.Errorf("%w; could not read remote.origin.fetch to restore it: %w", cause, err)
	}
	if slices.Equal(current, prevFetch) {
		return cause
	}
	if len(prevFetch) > 1 {
		return fmt.Errorf("%w; note: remote.origin.fetch was replaced by stave's standard refspec", cause)
	}
	if restoreErr := client.SetRemoteFetchRefspec(ctx, barePath, prevFetch[0]); restoreErr != nil {
		return fmt.Errorf("%w; could not restore remote.origin.fetch to %s: %w", cause, prevFetch[0], restoreErr)
	}
	return fmt.Errorf("%w; remote.origin.fetch restored to %s", cause, prevFetch[0])
}

// sameRepoURL reports whether an existing bare cache's origin URL and a newly
// supplied registration URL name the same repository.
//
// The rule is deliberately narrow. When both sides parse as host/owner/repo
// clone URLs, they match on owner and repo (case-insensitively) and the host
// is ignored, so an SSH-config alias such as git@hl_external:o/r.git adopts a
// cache cloned from https://github.com/o/r.git. When both sides are local
// paths (with any file:// prefix stripped) they match when they clean to the
// same absolute path or resolve through symlinks to the same directory; a
// trailing ".git" is significant because /srv/repo and /srv/repo.git are
// distinct directories. Anything else must be spelled identically.
func sameRepoURL(existingOrigin, givenURL string) (ok bool, reason string) {
	existing := normalizeRepoURL(existingOrigin)
	given := normalizeRepoURL(givenURL)

	if !isFileURL(existing) && !isFileURL(given) {
		_, existingOwner, existingRepo, existingOK := gh.ParseAnyOwnerRepo(existing)
		_, givenOwner, givenRepo, givenOK := gh.ParseAnyOwnerRepo(given)
		if existingOK && givenOK {
			if strings.EqualFold(existingOwner, givenOwner) && strings.EqualFold(existingRepo, givenRepo) {
				return true, ""
			}
			return false, fmt.Sprintf("origin remote %q points at %s/%s, not %s/%s", redactURL(existingOrigin), existingOwner, existingRepo, givenOwner, givenRepo)
		}
	}

	existingPath := strings.TrimPrefix(existing, "file://")
	givenPath := strings.TrimPrefix(given, "file://")
	if isPathLike(existingPath) && isPathLike(givenPath) && sameLocalPath(existingPath, givenPath) {
		return true, ""
	}

	if existing == given {
		return true, ""
	}
	return false, fmt.Sprintf("origin remote %q does not match %q", redactURL(existingOrigin), redactURL(givenURL))
}

func normalizeRepoURL(s string) string {
	s = strings.TrimSpace(s)
	for len(s) > 1 && strings.HasSuffix(s, "/") {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

func isFileURL(s string) bool {
	return strings.HasPrefix(s, "file://")
}

func isPathLike(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || filepath.IsAbs(s)
}

// sameLocalPath reports whether a and b name the same directory: both are
// made absolute (so relative spellings resolve against the working
// directory) and then compared by sameCanonicalPath, which resolves
// symlinks and falls back to filesystem identity so case variants on a
// case-insensitive volume still match.
func sameLocalPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return sameCanonicalPath(absA, absB)
}
