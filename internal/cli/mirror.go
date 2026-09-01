package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/spf13/cobra"
)

// finalizeBareMirror brings a bare mirror to stave's steady state after a clone
// or adoption: tracking refspec (optional), fetch, origin/HEAD, default-branch
// discovery. Configure/fetch failures are fatal; set-head and discovery are
// best-effort and reported as notes on stderr. After a failed set-head,
// discovery bypasses the symbolic-ref fast path (which would read a stale
// origin/HEAD and record a branch the remote no longer defaults to) and asks
// the remote directly.
//
// storedDefault is the default branch currently recorded in the registry ("" if
// none) and cfgDefaultBase is the config-wide fallback; both are used only for
// note wording. The returned defaultBranch is "" whenever discovery was skipped
// (dry-run) or failed, so callers can treat non-empty as "safe to record".
func finalizeBareMirror(ctx context.Context, cmd *cobra.Command, client *git.Client, name, barePath, storedDefault, cfgDefaultBase string, includeTracking, dryRun bool) (defaultBranch string, err error) {
	if includeTracking {
		if err := client.ConfigureBareRemoteTracking(ctx, barePath); err != nil {
			return "", fmt.Errorf("configure tracking for %q: %w", name, err)
		}
	}
	if err := client.FetchAllPrune(ctx, barePath); err != nil {
		return "", fmt.Errorf("fetch %q: %w", name, err)
	}
	setHeadErr := client.SetRemoteHead(ctx, barePath)
	if setHeadErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: could not set origin/HEAD for %q: %v\n", name, setHeadErr)
	}
	if dryRun {
		// RemoteDefaultBranch is a read-only probe that executes even under
		// dry-run; the mirror was never created, so it would only emit a false
		// discovery failure. Say what would happen instead.
		fmt.Fprintf(cmd.ErrOrStderr(), "note: would discover default branch for %q\n", name)
		return "", nil
	}
	var discovered string
	if setHeadErr != nil {
		discovered, err = client.RemoteDefaultBranchFromRemote(ctx, barePath)
		if err != nil {
			err = fmt.Errorf("origin/HEAD was not refreshed and the remote could not be queried: %w", err)
		}
	} else {
		discovered, err = client.RemoteDefaultBranch(ctx, barePath)
	}
	if err != nil || discovered == "" {
		if err == nil {
			err = fmt.Errorf("remote reported no default branch")
		}
		fallback := firstNonEmpty(storedDefault, cfgDefaultBase)
		fmt.Fprintf(cmd.ErrOrStderr(), "note: could not discover default branch for %q (%v); space operations will fall back to %q — re-run 'stave repos sync' later to fix this\n", name, err, fallback)
		return "", nil
	}
	if storedDefault != "" && storedDefault != discovered {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: remote default branch for %q is now %q; registry has %q\n", name, discovered, storedDefault)
	}
	return discovered, nil
}

// applyBackfill records discovered default branches into fresh, the registry
// as re-loaded after a sync loop, but only where the entry is still the one
// the sync operated on: same URL and bare path as in orig, and no default
// branch recorded meanwhile. Entries that vanished or were re-pointed during
// the sync are reported in skipped; entries someone else already filled are
// left alone silently. Both slices are sorted by name.
func applyBackfill(fresh *config.Config, orig map[string]config.Repository, backfill map[string]string) (changed, skipped []string) {
	names := make([]string, 0, len(backfill))
	for name := range backfill {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		before, wasThere := orig[name]
		repo, ok := fresh.Repos[name]
		if !ok || !wasThere || repo.URL != before.URL || repo.BareRepoPath != before.BareRepoPath {
			skipped = append(skipped, name)
			continue
		}
		if repo.DefaultBranch != "" {
			continue
		}
		repo.DefaultBranch = backfill[name]
		fresh.Repos[name] = repo
		changed = append(changed, name)
	}
	return changed, skipped
}
