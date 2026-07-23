package cli

// Cross-repo e2e: stave memory propose/contribute against the REAL marmot
// binary (no stubs). Exercises the full flow:
//
//	space create --memory .  ->  den create under MARMOT_HOME
//	seed den vault nodes     ->  marmot warren register / den link --edit
//	stave memory propose     ->  den contribute + warren propose (never pushes)
//	stave space archive --memory contribute
//	negative: propose without an edit link surfaces edit_link_required
//	S4 corporate: warren add (shared cache) -> --ref resolution via
//	source_url -> --edit pass-through -> contribute into the CACHE edit
//	worktree -> real warren sync -> skew suffixes in memory/space status
//
// Guarded: skips cleanly when no marmot binary can be resolved (see
// resolveE2EMarmot). Run via test_rig/memory-e2e/run.sh for a fresh
// HEAD-vs-HEAD build.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// marmotRepoPath is where the context-marmot checkout lives on dev machines;
// override with STAVE_E2E_MARMOT_REPO. Used only to build a HEAD binary when
// STAVE_E2E_MARMOT is not set.
const defaultMarmotRepo = "/Users/nurozen/Documents/GitHub/context-marmot"

// resolveE2EMarmot returns an absolute path to a marmot binary:
//  1. $STAVE_E2E_MARMOT (explicit binary, e.g. from test_rig/memory-e2e/run.sh)
//  2. build HEAD from the context-marmot repo when present (preferred over
//     PATH: tests stave HEAD against marmot HEAD, not a stale install)
//  3. `marmot` on PATH
//
// Gate honesty (F25): the ONLY skip is when no source is available at all
// (repo absent, no env override, no PATH binary). An explicit env binary that
// does not exist, or a sibling repo that exists but FAILS TO BUILD, fails the
// test — otherwise a broken marmot silently turns the cross-repo gate green.
func resolveE2EMarmot(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("STAVE_E2E_MARMOT"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatalf("STAVE_E2E_MARMOT %q: %v", bin, err)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("marmot e2e: STAVE_E2E_MARMOT is set but %s does not exist", abs)
		}
		return abs
	}
	repo := os.Getenv("STAVE_E2E_MARMOT_REPO")
	if repo == "" {
		repo = defaultMarmotRepo
	}
	if fi, err := os.Stat(repo); err == nil && fi.IsDir() {
		bin := filepath.Join(t.TempDir(), "marmot")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/marmot")
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("marmot e2e: context-marmot repo exists at %s but building marmot HEAD failed (fix the sibling repo or unset it): %v\n%s", repo, err, out)
		}
		return bin
	}
	if bin, err := exec.LookPath("marmot"); err == nil {
		abs, absErr := filepath.Abs(bin)
		if absErr == nil {
			return abs
		}
		return bin
	}
	t.Skipf("skipping marmot e2e: no STAVE_E2E_MARMOT, no context-marmot repo at %s, and no marmot on PATH", repo)
	return ""
}

// runMarmot invokes the real marmot binary, returning stdout and exit code.
func runMarmot(t *testing.T, bin, dir string, args ...string) (stdout string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("marmot %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	if errBuf.Len() > 0 {
		t.Logf("marmot %v stderr: %s", args, errBuf.String())
	}
	return out.String(), code
}

// writeWarrenCheckout builds a minimal warren git checkout: _warren.md
// manifest at the root plus projects/<pid>/.marmot/_warren.md project
// metadata (the layout marmot's own contribute e2e fixtures use).
func writeWarrenCheckout(t *testing.T, warrenID, projectID string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "warren-"+warrenID)
	marmotDir := filepath.Join(root, "projects", projectID, ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\n" +
		"warren_id: " + warrenID + "\n" +
		"version: 1\n" +
		"projects:\n" +
		"  - project_id: " + projectID + "\n" +
		"    path: projects/" + projectID + "/.marmot\n" +
		"---\n"
	if err := os.WriteFile(filepath.Join(root, "_warren.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := "---\n" +
		"project_id: " + projectID + "\n" +
		"warren_id: " + warrenID + "\n" +
		"vault_id: " + projectID + "\n" +
		"---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_warren.md"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "-b", "main", root)
	runGit(t, root, "config", "user.name", "Test User")
	runGit(t, root, "config", "user.email", "test@example.test")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "init warren")
	return root
}

// writeE2ENode writes a node markdown file into a den vault (same shape
// marmot's node writer produces: YAML frontmatter + summary paragraph).
func writeE2ENode(t *testing.T, vaultDir, id, summary string) {
	t.Helper()
	path := filepath.Join(vaultDir, filepath.FromSlash(id)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" +
		"id: " + id + "\n" +
		"type: concept\n" +
		"namespace: default\n" +
		"status: active\n" +
		"---\n\n" +
		summary + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMarmotMemoryE2E(t *testing.T) {
	marmotBin := resolveE2EMarmot(t)

	home := t.TempDir()
	marmotHome := filepath.Join(home, "marmot-home")
	if err := os.MkdirAll(marmotHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MARMOT_HOME", marmotHome)

	// In-process CLI runs are cwd-sensitive (marmot resolves the den vault
	// for `warren propose` via cwd -> routes.yml); restore cwd afterwards.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	runCLI(t, "setup")
	rewriteMemoryConfig(t, home, "binary: marmot", "binary: "+marmotBin)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "repos", "add", "repo-a", src)

	const (
		warrenID  = "w"
		projectID = "proj"
	)
	warrenRoot := writeWarrenCheckout(t, warrenID, projectID)
	spacePath := filepath.Join(home, "stave", "agent-work", "t1")
	vaultDir := filepath.Join(marmotHome, "dens", "t1", "vault")
	editBranch := "marmot/edit/t1/" + projectID

	step := func(name string, fn func(t *testing.T)) {
		t.Helper()
		if !t.Run(name, fn) {
			t.Fatalf("aborting: step %q failed", name)
		}
	}

	step("space create attaches den", func(t *testing.T) {
		out := runCLI(t, "space", "create", "t1", "-e", "repo-a", "--memory", ".")
		if !strings.Contains(out, "attached memory default") {
			t.Fatalf("space create output missing attach line:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(marmotHome, "dens", "t1", "_den.md")); err != nil {
			t.Fatalf("den t1 missing under MARMOT_HOME: %v", err)
		}
		manifest, err := os.ReadFile(filepath.Join(spacePath, ".stave.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(manifest), "marmot") || !strings.Contains(string(manifest), "t1") {
			t.Fatalf(".stave.yaml missing marmot attachment:\n%s", manifest)
		}
		mcp, err := os.ReadFile(filepath.Join(spacePath, ".mcp.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, need := range []string{"serve", "--den", "t1"} {
			if !strings.Contains(string(mcp), need) {
				t.Fatalf(".mcp.json missing %q:\n%s", need, mcp)
			}
		}
	})

	step("seed den vault nodes", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(vaultDir, "_config.md")); err != nil {
			t.Fatalf("den identity vault missing _config.md: %v", err)
		}
		writeE2ENode(t, vaultDir, "notes/alpha", "Alpha summary from the stave e2e.")
		writeE2ENode(t, vaultDir, "notes/beta", "Beta summary from the stave e2e.")
	})

	step("warren register and den link", func(t *testing.T) {
		out, code := runMarmot(t, marmotBin, "", "warren", "register", warrenID, warrenRoot, "--dir", vaultDir)
		if code != 0 {
			t.Fatalf("warren register: code=%d out=%s", code, out)
		}
		out, code = runMarmot(t, marmotBin, "", "den", "link", "t1", "--edit", warrenID+"/"+projectID, "--json")
		if code != 0 {
			t.Fatalf("den link: code=%d out=%s", code, out)
		}
		var env struct {
			Schema int    `json:"schema"`
			DenID  string `json:"den_id"`
			Link   struct {
				Target string `json:"target"`
				Mode   string `json:"mode"`
			} `json:"link"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("den link envelope: %v out=%s", err, out)
		}
		if env.Schema != 1 || env.DenID != "t1" || env.Link.Mode != "edit" || env.Link.Target != warrenID+"/"+projectID {
			t.Fatalf("den link envelope body: %+v", env)
		}
	})

	step("memory propose contributes to warren", func(t *testing.T) {
		// Deliberately run from OUTSIDE the space: stave sets the marmot
		// subprocess cwd to the space path, so marmot's cwd-based reverse
		// route resolution finds the den without stave itself being in the
		// space directory.
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		out := runCLI(t, "memory", "propose", "t1")
		if !strings.Contains(out, "contributed and proposed") {
			t.Fatalf("propose output missing success summary:\n%s", out)
		}
		// G4: handoff data must reach the user — contributed counts plus what
		// to push (or an explicit nothing-new statement). Never silent.
		if !strings.Contains(out, "contributed: 2 added") {
			t.Fatalf("propose output missing contributed counts:\n%s", out)
		}
		if !strings.Contains(out, "push with: git") && !strings.Contains(out, "nothing new to push") {
			t.Fatalf("propose output missing push handoff:\n%s", out)
		}
		gitOutput(t, warrenRoot, "rev-parse", "--verify", "refs/heads/"+editBranch)
		files := gitOutput(t, warrenRoot, "diff", "--name-only", "main", editBranch)
		for _, need := range []string{
			"projects/" + projectID + "/.marmot/notes/alpha.md",
			"projects/" + projectID + "/.marmot/notes/beta.md",
		} {
			if !strings.Contains(files, need) {
				t.Fatalf("edit branch missing %s:\n%s", need, files)
			}
		}
		if strings.Contains(files, ".marmot-data") {
			t.Fatalf(".marmot-data leaked into the edit branch:\n%s", files)
		}
		// Never auto-pushes; the fixture has no remote at all.
		if remotes := gitOutput(t, warrenRoot, "remote"); remotes != "" {
			t.Fatalf("warren checkout unexpectedly has remotes: %s", remotes)
		}
	})

	step("memory propose is idempotent", func(t *testing.T) {
		// Intentionally chdir'd INTO the space: the in-space (cwd-based)
		// flow must keep working alongside the explicit subprocess dir.
		if err := os.Chdir(spacePath); err != nil {
			t.Fatal(err)
		}
		out := runCLI(t, "memory", "propose", "t1")
		if !strings.Contains(out, "contributed and proposed") {
			t.Fatalf("second propose output:\n%s", out)
		}
		if n := gitOutput(t, warrenRoot, "rev-list", "--count", "main.."+editBranch); n != "1" {
			t.Fatalf("all-noop propose added a commit: rev-list count = %s", n)
		}
	})

	step("archive with memory contribute", func(t *testing.T) {
		// Also from outside the space: the archive contribute loop passes the
		// space path down as the marmot subprocess cwd.
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		out := runCLI(t, "space", "archive", "t1", "--memory", "contribute")
		if !strings.Contains(out, "archived t1") {
			t.Fatalf("archive output:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", ".archive", "t1")); err != nil {
			t.Fatalf("archived space missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(marmotHome, "dens", "t1", "_den.md")); err != nil {
			t.Fatalf("archive must keep the den: %v", err)
		}
	})

	step("propose without edit link fails", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		runCLI(t, "space", "create", "t2", "-e", "repo-a", "--memory", ".")
		out, err := runCLIError(t, nil, "memory", "propose", "t2")
		if err == nil {
			t.Fatalf("propose without an edit link must fail:\n%s", out)
		}
		combined := out + "\n" + err.Error()
		if !strings.Contains(combined, "edit_link_required") {
			t.Fatalf("expected marmot's edit_link_required to surface, got:\n%s", combined)
		}
	})

	// preCreateDen makes a durable den outside any space (attach-existing input).
	preCreateDen := func(t *testing.T, denID string) {
		t.Helper()
		proj := filepath.Join(home, "pre-proj-"+denID)
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runMarmot(t, marmotBin, "", "den", "create", denID, "--lifetime", "durable", "--project", proj, "--no-pointer", "--json")
		if code != 0 {
			t.Fatalf("den create %s: code=%d out=%s", denID, code, out)
		}
	}

	step("attach existing den unowned then archive keep (G1+G2)", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		preCreateDen(t, "pre1")
		out := runCLI(t, "space", "create", "t3", "--memory", "marmot:pre1")
		if !strings.Contains(out, "owned=false") {
			t.Fatalf("attach-existing must be unowned:\n%s", out)
		}
		// Alias derives from the den id (G3 scheme).
		if !strings.Contains(out, "attached memory pre1") {
			t.Fatalf("alias should derive from den id:\n%s", out)
		}
		// G2: reverse route registered for the space path.
		routes, code := runMarmot(t, marmotBin, "", "route")
		if code != 0 {
			t.Fatalf("route list: %s", routes)
		}
		if !strings.Contains(routes, "t3") || !strings.Contains(routes, "pre1") {
			t.Fatalf("reverse route for t3 -> pre1 missing:\n%s", routes)
		}

		// G1+G2 regression: archive (fate keep) succeeds and relocates the route.
		out = runCLI(t, "space", "archive", "t3", "--memory", "keep")
		if !strings.Contains(out, "archived t3") {
			t.Fatalf("archive output:\n%s", out)
		}
		if strings.Contains(out, "route relocation failed") {
			t.Fatalf("route relocation must succeed:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(marmotHome, "dens", "pre1", "_den.md")); err != nil {
			t.Fatalf("archive must keep the existing den: %v", err)
		}
		routes, _ = runMarmot(t, marmotBin, "", "route")
		if !strings.Contains(routes, filepath.Join(".archive", "t3")) {
			t.Fatalf("route not relocated to archive path:\n%s", routes)
		}
	})

	// S4 CORPORATE workflow: register-free cache-backed warren (`warren add`
	// replaces manual register), reference repos passed as --ref with
	// provider-side source_url resolution, --edit pass-through through stave,
	// contribute into the CACHE edit worktree (user checkout untouched),
	// real warren sync, and skew intelligence in memory/space status.
	step("corporate: warren add + ref resolution + edit pass-through", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		srcB := createGitRepo(t, "repo-b")
		runCLI(t, "repos", "add", "repo-b", srcB)

		// Author-side warren built with real marmot verbs: init + project
		// import --source-url (manifest v3 provenance). Two projects so the
		// --ref-resolved pinned link (proj-b) and the --edit mount (notes)
		// do not collide on one target.
		w2Root := filepath.Join(home, "w2")
		if err := os.MkdirAll(w2Root, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, code := runMarmot(t, marmotBin, "", "warren", "init", "--id", "w2", "--warren-dir", w2Root); code != 0 {
			t.Fatalf("warren init: code=%d out=%s", code, out)
		}
		// warren project import requires a source vault with _config.md.
		writeVaultConfig := func(dir string) {
			t.Helper()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\ntoken_budget: 8192\n---\n"
			if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		seedVault := filepath.Join(home, "seed-vault")
		writeVaultConfig(seedVault)
		writeE2ENode(t, seedVault, "seed/readme", "Seed knowledge for proj-b.")
		if out, code := runMarmot(t, marmotBin, "", "warren", "project", "import", "proj-b", seedVault,
			"--warren-dir", w2Root, "--vault-id", "proj-b", "--source-url", srcB); code != 0 {
			t.Fatalf("project import proj-b: code=%d out=%s", code, out)
		}
		notesVault := filepath.Join(home, "notes-vault")
		writeVaultConfig(notesVault)
		writeE2ENode(t, notesVault, "seed/notes", "Seed knowledge for notes.")
		if out, code := runMarmot(t, marmotBin, "", "warren", "project", "import", "notes", notesVault,
			"--warren-dir", w2Root, "--vault-id", "notes"); code != 0 {
			t.Fatalf("project import notes: code=%d out=%s", code, out)
		}
		runGit(t, "", "init", "-b", "main", w2Root)
		runGit(t, w2Root, "config", "user.name", "Test User")
		runGit(t, w2Root, "config", "user.email", "test@example.test")
		runGit(t, w2Root, "add", "-A")
		runGit(t, w2Root, "commit", "-m", "init warren w2")

		// Register-free: warren add clones into the shared cache.
		if out, code := runMarmot(t, marmotBin, "", "warren", "add", w2Root, "--id", "w2", "--json"); code != 0 {
			t.Fatalf("warren add: code=%d out=%s", code, out)
		}

		// Space with repo-b as a read-only reference; memory attach passes it
		// through as --ref and pushes --edit through to den link.
		runCLI(t, "space", "create", "t5", "-e", "repo-a", "-r", "repo-b")
		attachOut := runCLI(t, "memory", "attach", "t5", "--edit", "w2/notes")
		if !strings.Contains(attachOut, "reference repo-b → w2/proj-b (warren-url)") {
			t.Fatalf("attach missing --ref resolution line:\n%s", attachOut)
		}
		if !strings.Contains(attachOut, "linked w2/notes (mode=edit)") {
			t.Fatalf("attach missing edit pass-through line:\n%s", attachOut)
		}

		// den status --json: pinned reference link + edit mount both present.
		statusJSON, code := runMarmot(t, marmotBin, "", "den", "status", "t5", "--json")
		if code != 0 {
			t.Fatalf("den status: code=%d out=%s", code, statusJSON)
		}
		var st struct {
			Schema int `json:"schema"`
			Links  []struct {
				Ref          string  `json:"ref"`
				Mode         *string `json:"mode"`
				PinnedCommit *string `json:"pinned_commit"`
			} `json:"links"`
		}
		if err := json.Unmarshal([]byte(statusJSON), &st); err != nil {
			t.Fatalf("den status envelope: %v out=%s", err, statusJSON)
		}
		modes := map[string]string{}
		var pinned *string
		for _, l := range st.Links {
			if l.Mode != nil {
				modes[l.Ref] = *l.Mode
			}
			if l.Ref == "w2/proj-b" {
				pinned = l.PinnedCommit
			}
		}
		if modes["w2/proj-b"] != "link" || modes["w2/notes"] != "edit" {
			t.Fatalf("den links = %#v", st.Links)
		}
		if pinned == nil || *pinned == "" {
			t.Fatalf("reference link must be pinned: %s", statusJSON)
		}
	})

	step("corporate: real sync, contribute to cache worktree, skew status", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		// Real warren sync (probe-gated pass-through, per-warren rendering).
		syncOut := runCLI(t, "memory", "sync", "t5")
		if !strings.Contains(syncOut, "synced w2 (up to date at ") {
			t.Fatalf("memory sync output:\n%s", syncOut)
		}

		// Agent writes into the den vault, then propose from OUTSIDE the space.
		t5Vault := filepath.Join(marmotHome, "dens", "t5", "vault")
		writeE2ENode(t, t5Vault, "notes/gamma", "Gamma summary from the corporate e2e.")
		writeE2ENode(t, t5Vault, "notes/delta", "Delta summary from the corporate e2e.")
		proposeOut := runCLI(t, "memory", "propose", "t5")
		if !strings.Contains(proposeOut, "contributed and proposed") || !strings.Contains(proposeOut, "contributed: 2 added") {
			t.Fatalf("propose output:\n%s", proposeOut)
		}

		// Contribute landed on the edit branch in the CACHE bare, via the
		// dedicated edit worktree — never in the author/user checkout.
		cacheBare := filepath.Join(marmotHome, "warren-cache", "w2.git")
		editBranch := "marmot/edit/t5/w2"
		gitOutput(t, "", "--git-dir", cacheBare, "rev-parse", "--verify", "refs/heads/"+editBranch)
		files := gitOutput(t, "", "--git-dir", cacheBare, "show", "--name-only", "--format=", editBranch)
		for _, need := range []string{
			"projects/notes/.marmot/notes/gamma.md",
			"projects/notes/.marmot/notes/delta.md",
		} {
			if !strings.Contains(files, need) {
				t.Fatalf("cache edit branch missing %s:\n%s", need, files)
			}
		}
		w2Root := filepath.Join(home, "w2")
		if dirty := gitOutput(t, w2Root, "status", "--porcelain"); strings.TrimSpace(dirty) != "" {
			t.Fatalf("user-side warren checkout must stay clean:\n%s", dirty)
		}
		if out, err := runCLIError(t, nil, "memory", "status", "t5"); err != nil {
			t.Fatalf("memory status: %v\n%s", err, out)
		} else {
			// Skew intelligence: unpushed edit count on header + per-link row.
			if !strings.Contains(out, "(1 unpushed)") || !strings.Contains(out, "w2/notes  edit  ahead 1") {
				t.Fatalf("memory status missing skew rows:\n%s", out)
			}
			if !strings.Contains(out, "w2/proj-b  link  pinned ") {
				t.Fatalf("memory status missing pinned row:\n%s", out)
			}
		}
		// User-side checkout has no edit branch at all.
		if _, err := exec.Command("git", "-C", w2Root, "rev-parse", "--verify", "refs/heads/"+editBranch).Output(); err == nil {
			t.Fatalf("edit branch must not exist in the user checkout")
		}

		// space status memory row gains the state suffix.
		spaceStatus := runCLI(t, "space", "status", "t5")
		if !strings.Contains(spaceStatus, "[memory] default marmot den=t5 owned (1 unpushed)") {
			t.Fatalf("space status memory row:\n%s", spaceStatus)
		}

		// Archive with contribute (idempotent NOOP re-contribute) keeps the den.
		archiveOut := runCLI(t, "space", "archive", "t5", "--memory", "contribute")
		if !strings.Contains(archiveOut, "archived t5") {
			t.Fatalf("archive output:\n%s", archiveOut)
		}
		if _, err := os.Stat(filepath.Join(marmotHome, "dens", "t5", "_den.md")); err != nil {
			t.Fatalf("archive must keep the den: %v", err)
		}
		if n := gitOutput(t, "", "--git-dir", cacheBare, "rev-list", "--count", "main.."+editBranch); n != "1" {
			t.Fatalf("all-noop re-contribute added a commit: rev-list count = %s", n)
		}
	})

	// F5 / plan §3.6: `space add` parity — adding a reference repo to a space
	// that ALREADY has memory attached resolves the repo's url against the
	// cached warren (source_url match) and links it read-only onto the den.
	step("space add links reference into attached memory (S4 add parity)", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		// Memory attaches first, with no reference repos in the space.
		runCLI(t, "space", "create", "t6", "-e", "repo-a", "--memory", ".")
		// repo-b's registered url matches warren w2's proj-b source_url.
		// --no-fetch: the bare repo already has the refs (fetched when t5 was
		// created) and repo-b's source dir lived in the corporate subtest's
		// TempDir, which is gone by now.
		addOut := runCLI(t, "space", "add", "t6", "repo-b", "--reference", "--no-fetch")
		if !strings.Contains(addOut, "added reference repo repo-b to t6") {
			t.Fatalf("space add output:\n%s", addOut)
		}
		if !strings.Contains(addOut, "reference repo-b → w2/proj-b (warren-url)") {
			t.Fatalf("space add missing memory link line:\n%s", addOut)
		}
		// The den gained a pinned read-only link; memory status shows it.
		out, err := runCLIError(t, nil, "memory", "status", "t6")
		if err != nil {
			t.Fatalf("memory status: %v\n%s", err, out)
		}
		if !strings.Contains(out, "w2/proj-b  link  pinned ") {
			t.Fatalf("memory status missing pinned link row:\n%s", out)
		}
	})

	step("two-memory attach then archive (G3+G1)", func(t *testing.T) {
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		preCreateDen(t, "pre2")
		out := runCLI(t, "space", "create", "t4", "--memory", ".", "--memory", "marmot:pre2")
		if !strings.Contains(out, "attached memory default") || !strings.Contains(out, "attached memory pre2") {
			t.Fatalf("both memories must attach:\n%s", out)
		}
		list := runCLI(t, "memory", "list", "t4")
		for _, need := range []string{"default", "pre2"} {
			if !strings.Contains(list, need) {
				t.Fatalf("memory list missing %q:\n%s", need, list)
			}
		}
		// G1 regression proper: archive with TWO attachments used to rewrite the
		// route once per attachment — the second set-project consumed route
		// aborted the archive mid-mutation. One space-level relocation now.
		out = runCLI(t, "space", "archive", "t4")
		if !strings.Contains(out, "archived t4") {
			t.Fatalf("archive with two attachments failed:\n%s", out)
		}
		if strings.Contains(out, "route relocation failed") {
			t.Fatalf("route relocation must succeed once per space:\n%s", out)
		}
		for _, den := range []string{"t4", "pre2"} {
			if _, err := os.Stat(filepath.Join(marmotHome, "dens", den, "_den.md")); err != nil {
				t.Fatalf("archive must keep den %s: %v", den, err)
			}
		}
	})
}

// TestMarmotOldBinaryAttachDegrades: a stub "old marmot" (den verbs but no
// den link / --ref surface) must still attach — the S4 flags are dropped with
// a notice, the argv stays byte-stable S2, and no den link call is issued.
// Stub-based: runs everywhere, no real marmot build needed.
func TestMarmotOldBinaryAttachDegrades(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MARMOT_HOME", filepath.Join(home, "marmot-home"))

	logPath := filepath.Join(home, "marmot-argv.log")
	stub := filepath.Join(home, "old-marmot")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *\"den --help\"*)\n" +
		"    cat >&2 <<'USAGE'\n" +
		"usage: marmot den <command> [flags]\n" +
		"commands:\n" +
		"  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--no-pointer] [--no-vault] [--dry-run] [--json]\n" +
		"  status  [<den-id>] [--json]\n" +
		"  destroy <den-id> [--force] [--dry-run] [--json]\n" +
		"USAGE\n" +
		"    exit 0;;\n" +
		"  *\"den create\"*)\n" +
		"    printf '%s' '{\"schema\":1,\"den_id\":\"old-sp\",\"den_path\":\"/tmp/dens/old-sp\",\"pointer_written\":false,\"warnings\":[]}'\n" +
		"    exit 0;;\n" +
		"  *--version*) echo old-marmot 0.1; exit 0;;\n" +
		"esac\n" +
		"echo \"old marmot: unknown: $*\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runCLI(t, "setup")
	rewriteMemoryConfig(t, home, "binary: marmot", "binary: "+stub)
	runCLI(t, "space", "init", "old-sp")

	out := runCLI(t, "memory", "attach", "old-sp", "--edit", "w/p", "--link", "a/b")
	if !strings.Contains(out, "notice: installed marmot lacks den link/--ref support; dropping --edit/--link/--ref/--opt") {
		t.Fatalf("expected drop notice:\n%s", out)
	}
	if !strings.Contains(out, "attached memory default (marmot:old-sp, owned=true)") {
		t.Fatalf("attach must still succeed:\n%s", out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, ban := range []string{"--edit", "--link", "--ref", "den link"} {
		if strings.Contains(string(log), ban) {
			t.Fatalf("old marmot must never see %q:\n%s", ban, log)
		}
	}
	if !strings.Contains(string(log), "den create old-sp --lifetime task --project") ||
		!strings.Contains(string(log), "--no-pointer --json") {
		t.Fatalf("S2 argv must be byte-stable:\n%s", log)
	}
	manifest, err := os.ReadFile(filepath.Join(home, "stave", "agent-work", "old-sp", ".stave.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "old-sp") || !strings.Contains(string(manifest), "marmot") {
		t.Fatalf(".stave.yaml missing attachment:\n%s", manifest)
	}
}
