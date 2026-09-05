package space

import "fmt"

// Codes for the registry (`stave repos add`) and bootstrap (`stave setup`)
// refusals. They live beside the space codes so GUI hosts key on one table.
const (
	CodeRepoExists   = "repo_exists"
	CodeCloneFailed  = "clone_failed"
	CodeCacheExists  = "cache_exists"
	CodeConfigExists = "config_exists"
)

// RepoExistsError: `repos add` was given a name the registry already holds.
type RepoExistsError struct {
	Repo string
}

func (e *RepoExistsError) Error() string {
	return fmt.Sprintf("repo %q is already registered", e.Repo)
}

func (e *RepoExistsError) Code() string { return CodeRepoExists }

func (e *RepoExistsError) Details() map[string]any {
	return map[string]any{"repo": e.Repo}
}

// CacheExistsError: a bare repo already sits at the derived cache path and
// `repos add` was not told to --adopt it. RetryHint is the pasteable adopt
// command the message quotes.
type CacheExistsError struct {
	Repo      string
	Path      string
	RetryHint string
}

func (e *CacheExistsError) Error() string {
	return fmt.Sprintf("bare repo path already exists: %s; run '%s' to reuse it, or delete it to re-clone", e.Path, e.RetryHint)
}

func (e *CacheExistsError) Code() string { return CodeCacheExists }

func (e *CacheExistsError) Details() map[string]any {
	return map[string]any{"repo": e.Repo, "path": e.Path}
}

// CloneFailedError: the fresh bare clone for `repos add` failed; Err is the
// git failure.
type CloneFailedError struct {
	Repo string
	Err  error
}

func (e *CloneFailedError) Error() string {
	return fmt.Sprintf("clone %q: %v", e.Repo, e.Err)
}

func (e *CloneFailedError) Unwrap() error { return e.Err }

func (e *CloneFailedError) Code() string { return CodeCloneFailed }

func (e *CloneFailedError) Details() map[string]any {
	return map[string]any{"repo": e.Repo}
}

// ConfigExistsError: `stave setup` found a config file it would rewrite and
// was not given --force.
type ConfigExistsError struct {
	Path string
}

func (e *ConfigExistsError) Error() string {
	return fmt.Sprintf("config already exists at %s; re-run with --force to rewrite it", e.Path)
}

func (e *ConfigExistsError) Code() string { return CodeConfigExists }

func (e *ConfigExistsError) Details() map[string]any {
	return map[string]any{"path": e.Path}
}
