package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/space"
)

func writeSpaceManifest(t *testing.T, workDir, id string, repos ...space.RepoManifest) {
	t.Helper()
	dir := filepath.Join(workDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(dir, space.Manifest{ID: id, Repos: repos}); err != nil {
		t.Fatal(err)
	}
}

func TestSpacesReferencingRepo(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare-repos", "repo-a.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(root, "agent-work")

	// Match by canonical path even when the name differs.
	writeSpaceManifest(t, workDir, "by-path", space.RepoManifest{Name: "other-name", Mode: space.ModeReference, BareRepoPath: filepath.Join(root, "bare-repos", ".", "repo-a.git")})
	// Match by name even when the path differs.
	writeSpaceManifest(t, workDir, "by-name", space.RepoManifest{Name: "repo-a", Mode: space.ModeEdit, BareRepoPath: filepath.Join(root, "elsewhere", "repo-a.git")})
	// A different repo entirely.
	writeSpaceManifest(t, workDir, "unrelated", space.RepoManifest{Name: "repo-b", Mode: space.ModeEdit, BareRepoPath: filepath.Join(root, "bare-repos", "repo-b.git")})
	// A directory without a manifest is not a space.
	if err := os.MkdirAll(filepath.Join(workDir, "not-a-space"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Archived spaces are skipped even when they match.
	writeSpaceManifest(t, filepath.Join(workDir, ".archive"), "archived", space.RepoManifest{Name: "repo-a", Mode: space.ModeEdit, BareRepoPath: bare})
	// A stray file at the top level is ignored.
	if err := os.WriteFile(filepath.Join(workDir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := spacesReferencingRepo(workDir, "repo-a", bare)
	if err != nil {
		t.Fatalf("spacesReferencingRepo error = %v", err)
	}
	if want := []string{"by-name", "by-path"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}

	// A corrupt manifest fails closed and names the space directory.
	corrupt := filepath.Join(workDir, "corrupt")
	if err := os.MkdirAll(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, space.ManifestName), []byte("repos: [unterminated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spacesReferencingRepo(workDir, "repo-a", bare); err == nil || !strings.Contains(err.Error(), corrupt) {
		t.Fatalf("expected error naming %s, got %v", corrupt, err)
	}
}

func TestSpacesReferencingRepoMissingWorkDir(t *testing.T) {
	ids, err := spacesReferencingRepo(filepath.Join(t.TempDir(), "missing"), "repo-a", "/nonexistent/repo-a.git")
	if err != nil || ids != nil {
		t.Fatalf("missing work dir: ids=%v err=%v, want nil, nil", ids, err)
	}
}

// runCLISplit runs the CLI with stdout and stderr captured separately.
func runCLISplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCommand()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestSameCanonicalPathAndSameLocalPathUseFilesystemIdentity(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "Repo.git")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other.git")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.git")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	for _, fn := range []struct {
		name string
		same func(a, b string) bool
	}{{"sameCanonicalPath", sameCanonicalPath}, {"sameLocalPath", sameLocalPath}} {
		t.Run(fn.name, func(t *testing.T) {
			if !fn.same(real, link) || !fn.same(link, real) {
				t.Fatal("symlink and target should match")
			}
			if fn.same(real, other) {
				t.Fatal("distinct directories matched")
			}
			if fn.same(real, filepath.Join(root, "missing.git")) {
				t.Fatal("existing vs missing path matched")
			}
			variant := filepath.Join(root, "repo.git")
			if _, err := os.Stat(variant); err != nil {
				t.Skip("case-sensitive filesystem; no case variant to compare")
			}
			if !fn.same(real, variant) || !fn.same(variant, real) {
				t.Fatalf("case variant %s should match %s on a case-insensitive filesystem", variant, real)
			}
		})
	}
	if sameCanonicalPath("", real) || sameCanonicalPath(real, "") {
		t.Fatal("empty path matched")
	}
}
