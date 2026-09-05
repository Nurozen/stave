package space

import (
	"errors"
	"fmt"
	"testing"
)

// The registry/bootstrap errors keep their historical messages and carry the
// codes and details GUI hosts key on, through fmt.Errorf %w wrapping too.
func TestRepoAndSetupErrorCodes(t *testing.T) {
	cases := []struct {
		err     error
		code    string
		message string
		details map[string]any
	}{
		{&RepoExistsError{Repo: "api"}, CodeRepoExists, `repo "api" is already registered`, map[string]any{"repo": "api"}},
		{&CacheExistsError{Repo: "api", Path: "/b/api.git", RetryHint: "stave repos add api u --adopt"}, CodeCacheExists, "bare repo path already exists: /b/api.git; run 'stave repos add api u --adopt' to reuse it, or delete it to re-clone", map[string]any{"repo": "api", "path": "/b/api.git"}},
		{&CloneFailedError{Repo: "api", Err: errors.New("boom")}, CodeCloneFailed, `clone "api": boom`, map[string]any{"repo": "api"}},
		{&ConfigExistsError{Path: "/c/config.yaml"}, CodeConfigExists, "config already exists at /c/config.yaml; re-run with --force to rewrite it", map[string]any{"path": "/c/config.yaml"}},
	}
	for _, tc := range cases {
		wrapped := fmt.Errorf("outer: %w", tc.err)
		if ErrorCode(tc.err) != tc.code || ErrorCode(wrapped) != tc.code {
			t.Fatalf("%T code = %s / %s, want %s", tc.err, ErrorCode(tc.err), ErrorCode(wrapped), tc.code)
		}
		if tc.err.Error() != tc.message {
			t.Fatalf("%T message = %q, want %q", tc.err, tc.err.Error(), tc.message)
		}
		details := ErrorDetails(wrapped)
		if len(details) != len(tc.details) {
			t.Fatalf("%T details = %v, want %v", tc.err, details, tc.details)
		}
		for key, want := range tc.details {
			if details[key] != want {
				t.Fatalf("%T details[%s] = %v, want %v", tc.err, key, details[key], want)
			}
		}
	}
	var cloneErr *CloneFailedError
	inner := errors.New("boom")
	if !errors.As(&CloneFailedError{Repo: "api", Err: inner}, &cloneErr) || !errors.Is(cloneErr, inner) {
		t.Fatal("CloneFailedError must unwrap to the git failure")
	}
}
