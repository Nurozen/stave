package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/git"
)

// cloneBareFresh clones url into a bare repository at dest without ever
// leaving a half-written clone behind. git clone --bare writes directly into
// dest, so an interrupted or failed clone would otherwise leave a directory
// that later runs mistake for a valid mirror. The clone therefore lands in a
// uniquely named sibling reserved with os.MkdirTemp (git clones happily into
// an existing empty directory) and is renamed into place only once it
// completes; on any failure only this call's sibling is removed and dest is
// untouched.
//
// In dry-run mode the clone is delegated to the client unchanged so the
// logged plan names the real destination, and no filesystem work happens.
func cloneBareFresh(ctx context.Context, client *git.Client, url, dest string, dryRun bool) error {
	if dryRun {
		return client.CloneBare(ctx, url, dest)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), filepath.Base(dest)+".partial-")
	if err != nil {
		return fmt.Errorf("reserve clone directory next to %s: %w", dest, err)
	}
	// MkdirTemp creates 0700; the cache is a long-lived directory that other
	// users' tooling may legitimately read, so give it the conventional mode
	// a plain 'git clone --bare' would have produced under the usual umask.
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("set mode on clone directory %s: %w", tmp, err)
	}
	if err := client.CloneBare(ctx, url, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("bare repo appeared concurrently at %s (or rename failed): %w", dest, err)
	}
	return nil
}

// shellQuote renders s safely for pasting into a POSIX shell: returned as-is
// when it contains only conservative characters, otherwise single-quoted.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '.', r == '_', r == '-', r == ':', r == '@', r == '~', r == '#', r == '+', r == '=', r == ',':
		default:
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// adoptRetryHint is the pasteable recipe for re-registering a repo whose
// bare clone survived a failed registration or already sits at the cache
// path. A credentialed URL is replaced by a placeholder rather than echoed.
func adoptRetryHint(name, url string) string {
	return fmt.Sprintf("stave repos add %s %s --adopt", shellQuote(name), pasteableURL(url))
}

// urlCredential splits u around an embedded credential: prefix is everything
// before the userinfo, rest is everything after its "@". ok is false when u
// carries no credential. Two spellings are recognised:
//
//   - scheme://user[:pass]@host/... — any userinfo in an authority is
//     treated as a credential, since https URLs carry tokens as the user.
//   - user:pass@host:path (scp-style) — only a user:pass pair counts; the
//     conventional bare git@ user is an SSH login name, not a secret.
func urlCredential(u string) (prefix, rest string, ok bool) {
	if schemeEnd := strings.Index(u, "://"); schemeEnd >= 0 {
		start := schemeEnd + len("://")
		authority := u[start:]
		if slash := strings.IndexAny(authority, "/?#"); slash >= 0 {
			authority = authority[:slash]
		}
		at := strings.LastIndex(authority, "@")
		if at < 0 {
			return "", "", false
		}
		return u[:start], u[start+at+1:], true
	}
	at := strings.Index(u, "@")
	if at < 0 {
		return "", "", false
	}
	userinfo, hostPath := u[:at], u[at+1:]
	if strings.ContainsAny(userinfo, `/\`) || !strings.Contains(userinfo, ":") || !strings.Contains(hostPath, ":") {
		return "", "", false
	}
	return "", hostPath, true
}

// redactURL returns u with any embedded credential replaced by "***@", for
// messages that display a registered URL. Strings without a credential are
// returned unchanged.
func redactURL(u string) string {
	prefix, rest, ok := urlCredential(u)
	if !ok {
		return u
	}
	return prefix + "***@" + rest
}

// pasteableURL renders u for embedding in a command the user is meant to
// paste: shell-quoted when it carries no credential, otherwise the literal
// placeholder <url> so the secret is never echoed into a terminal or log.
func pasteableURL(u string) string {
	if _, _, ok := urlCredential(u); ok {
		return "<url>"
	}
	return shellQuote(u)
}
