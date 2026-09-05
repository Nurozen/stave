package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/agent"
	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
	"github.com/spf13/cobra"
)

func TestCLIHelpCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"inscribe", "--help"},
		{"inscribe", "shell", "--help"},
		{"shell-init", "--help"},
		{"repos", "--help"},
		{"space", "--help"},
		{"space", "create", "--help"},
		{"space", "retarget", "--help"},
		{"space", "status", "--help"},
		{"space", "list", "--help"},
		{"repos", "list", "--help"},
		{"memory", "--help"},
		{"memory", "attach", "--help"},
		{"memory", "providers", "--help"},
		{"portal", "--help"},
		{"portal", "init", "--help"},
		{"portal", "init", "container", "--help"},
		{"portal", "init", "devcontainer", "--help"},
		{"portal", "attach", "--help"},
		{"portal", "attach", "ssh", "--help"},
		{"portal", "attach", "ec2", "--help"},
		{"portal", "configure", "--help"},
		{"portal", "drivers", "--help"},
		{"portal", "doctor", "--help"},
		{"portal", "list", "--help"},
		{"portal", "status", "--help"},
		{"portal", "inspect", "--help"},
		{"portal", "auth", "--help"},
		{"portal", "auth", "status", "--help"},
		{"portal", "auth", "login", "--help"},
		{"portal", "auth", "inherit", "--help"},
		{"portal", "auth", "revoke", "--help"},
		{"portal", "up", "--help"},
		{"portal", "sync", "--help"},
		{"portal", "shell", "--help"},
		{"portal", "exec", "--help"},
		{"portal", "summon", "--help"},
		{"portal", "logs", "--help"},
		{"portal", "down", "--help"},
		{"portal", "detach", "--help"},
		{"portal", "destroy", "--help"},
		{"agent", "--help"},
		{"summon", "--help"},
		{"saga", "--help"},
		{"saga", "create", "--help"},
		{"saga", "list", "--help"},
		{"saga", "status", "--help"},
		{"saga", "sync", "--help"},
		{"saga", "add", "--help"},
		{"saga", "remove", "--help"},
		{"saga", "archive", "--help"},
		{"saga", "destroy", "--help"},
		{"version", "--help"},
		{"space", "create", "example", "--help"},
	} {
		cmd := NewRootCommand()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("stave %v error = %v\n%s", args, err, out.String())
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("help output missing Usage for %v:\n%s", args, out.String())
		}
	}
}

func TestPortalAuthLoginLeavesMethodForDriverDefault(t *testing.T) {
	cmd, _, err := NewRootCommand().Find([]string{"portal", "auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	method := cmd.Flags().Lookup("method")
	if method == nil || method.DefValue != "" {
		t.Fatalf("auth login method default = %#v, want empty for driver-specific selection", method)
	}
}

func TestCLISetupReposAddAndCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	spec := filepath.Join(t.TempDir(), "ticket.md")
	if err := os.WriteFile(spec, []byte("ticket"), 0o644); err != nil {
		t.Fatal(err)
	}

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "space", "create", "ex-1234", "-k", "ticket", "-s", spec, "-e", "repo-a", "-r", "repo-b")

	root := filepath.Join(home, "stave")
	spacePath := filepath.Join(root, "agent-work", "ex-1234")
	if _, err := os.Stat(filepath.Join(root, "bare-repos", "repo-a.git")); err != nil {
		t.Fatalf("bare repo missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatalf("edit worktree missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "references", "repo-b")); err != nil {
		t.Fatalf("reference worktree missing: %v", err)
	}
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "ex-1234" || len(manifest.Repos) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.SpecPath != "spec" {
		t.Fatalf("SpecPath = %q", manifest.SpecPath)
	}
	status := runCLI(t, "space", "status", "ex-1234")
	if !strings.Contains(status, "spec:") || !strings.Contains(status, "repo-a [edit]") || !strings.Contains(status, "repo-b [reference]") {
		t.Fatalf("status output missing repos:\n%s", status)
	}
}

func TestCLISpaceStatusRejectsTraversalID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runCLIError(t, nil, "space", "status", "../x")
	if err == nil || !strings.Contains(err.Error(), "space id") {
		t.Fatalf("space status ../x error = %v\n%s", err, out)
	}
}

func TestCLIRetargetFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runGit(t, src, "branch", "dev")
	if err := os.WriteFile(filepath.Join(src, "next.md"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "next.md")
	runGit(t, src, "commit", "-m", "second")

	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "space", "create", "rt-1", "-e", "repo-a:dev")

	out := runCLI(t, "space", "retarget", "rt-1", "--repo", "repo-a", "--base", "origin/main")
	if !strings.Contains(out, "retargeted rt-1 repo repo-a to base origin/main") {
		t.Fatalf("retarget output = %s", out)
	}
	status := runCLI(t, "space", "status", "rt-1")
	if !strings.Contains(status, "base: origin/main") || !strings.Contains(status, "drift: ahead 0, behind 1") {
		t.Fatalf("status after retarget:\n%s", status)
	}
}

func TestCLICreateDryRunDoesNotCreateSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)

	out := runCLI(t, "space", "create", "ex-1234", "-e", "repo-a", "--dry-run")
	if !strings.Contains(out, "dry-run: create space directory") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
}

func TestCLICreateDryRunPrintsSummon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", srcA)

	out := runCLI(t, "space", "create", "ex-1234", "-e", "repo-a", "--summon", "codex", "--dry-run")
	if !strings.Contains(out, "dry-run: summon ex-1234 with codex") || !strings.Contains(out, "codex --cd") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
}

func TestCLISummonPrintCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	out := runCLI(t, "summon", "ex-1234", "--with", "codex", "--print-command")
	if !strings.Contains(out, filepath.Join(home, "stave", "agent-work", "ex-1234")) || !strings.Contains(out, "codex --cd") {
		t.Fatalf("summon output = %s", out)
	}
}

func TestCLISummonForwardsAgentFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-flags")

	out := runCLI(t, "summon", "ex-flags", "--with", "codex", "--yolo", "--print-command")
	if !strings.Contains(out, "codex --cd") || !strings.Contains(out, "--yolo") {
		t.Fatalf("summon output = %s", out)
	}
}

func TestCLIReposListSyncAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srcA := createGitRepo(t, "repo-a")
	srcB := createGitRepo(t, "repo-b")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-b", srcB)
	runCLI(t, "repos", "add", "repo-a", srcA)

	listOut := runCLI(t, "repos", "list")
	if !strings.Contains(listOut, "repo-a\t") || !strings.Contains(listOut, "repo-b\t") || strings.Index(listOut, "repo-a") > strings.Index(listOut, "repo-b") {
		t.Fatalf("repos list output = %s", listOut)
	}
	syncOne := runCLI(t, "repos", "sync", "repo-a")
	if !strings.Contains(syncOne, "synced repo-a") || strings.Contains(syncOne, "repo-b") {
		t.Fatalf("single repo sync output = %s", syncOne)
	}
	syncAll := runCLI(t, "repos", "sync")
	if !strings.Contains(syncAll, "synced repo-a") || !strings.Contains(syncAll, "synced repo-b") {
		t.Fatalf("all repo sync output = %s", syncAll)
	}
	removeOut := runCLI(t, "repos", "remove", "repo-a")
	if !strings.Contains(removeOut, "unregistered repo-a") {
		t.Fatalf("remove output = %s", removeOut)
	}
	listOut = runCLI(t, "repos", "list")
	if strings.Contains(listOut, "repo-a\t") || !strings.Contains(listOut, "repo-b\t") {
		t.Fatalf("repos list after remove = %s", listOut)
	}
	if _, err := runCLIError(t, nil, "repos", "sync", "missing"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected missing repo sync error, err=%v", err)
	}
	if _, err := runCLIError(t, nil, "repos", "remove", "missing"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected missing repo remove error, err=%v", err)
	}
}

func TestCLIReposAddDryRunAndDuplicate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")

	dryRun := runCLI(t, "repos", "add", "dry", src, "--dry-run")
	if !strings.Contains(dryRun, "dry-run: create") || !strings.Contains(dryRun, "registered dry") {
		t.Fatalf("repos add dry-run output = %s", dryRun)
	}
	if strings.Contains(dryRun, "could not discover") {
		t.Fatalf("dry-run emitted a false discovery failure:\n%s", dryRun)
	}
	if list := runCLI(t, "repos", "list"); strings.Contains(list, "dry\t") {
		t.Fatalf("dry-run repo was persisted:\n%s", list)
	}

	runCLI(t, "repos", "add", "repo-a", src)
	if _, err := runCLIError(t, nil, "repos", "add", "repo-a", src); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate repo error, err=%v", err)
	}
}

func TestCLIPortalContainerStatusAndConfigure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	out := runCLI(t, "portal", "init", "container", "ex-1234", "--image", "ubuntu:latest", "--container-root", "/workspace/ex-1234")
	if !strings.Contains(out, "record docker portal default") {
		t.Fatalf("init output = %s", out)
	}
	spacePath := filepath.Join(home, "stave", "agent-work", "ex-1234")
	manifest, err := portal.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["default"].Runtime.Image != "ubuntu:latest" {
		t.Fatalf("portal manifest = %#v", manifest.Portals["default"])
	}

	statusOut := runCLI(t, "portal", "status", "ex-1234", "--json")
	var status portal.Status
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatalf("invalid status JSON:\n%s\n%v", statusOut, err)
	}
	if status.SpaceID != "ex-1234" || status.PortalID != "default" || status.Driver != portal.DriverDocker {
		t.Fatalf("status = %#v", status)
	}

	runCLI(t, "portal", "configure", "ex-1234", "--agent", "claude", "--auth", "volume")
	manifest, err = portal.LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["default"].Auth.Mode != portal.AuthVolume {
		t.Fatalf("auth = %#v", manifest.Portals["default"].Auth)
	}
}

func TestCLIPortalReadCommandsTextAndJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234", "--preset", "local-codex")

	runner := &fakePortalRunner{result: portal.RunResult{Stdout: `[{"State":{"Status":"running","Health":{"Status":"healthy"}}}]`}}
	application := &app{portalRunner: runner}

	drivers := runCLIWithApp(t, application, "portal", "drivers")
	if !strings.Contains(drivers, "docker\tcreate") || !strings.Contains(drivers, "ssh\tattach-only") {
		t.Fatalf("drivers output = %s", drivers)
	}

	listText := runCLIWithApp(t, application, "portal", "list")
	if !strings.Contains(listText, "ex-1234\tdefault\tdocker") {
		t.Fatalf("portal list text = %s", listText)
	}
	listJSON := runCLIWithApp(t, application, "portal", "list", "ex-1234", "--json")
	if !strings.Contains(listJSON, `"space_id"`) || strings.Contains(listJSON, `"SpaceID"`) {
		t.Fatalf("portal list json keys = %s", listJSON)
	}
	var entries []portal.ListEntry
	if err := json.Unmarshal([]byte(listJSON), &entries); err != nil || len(entries) != 1 || entries[0].SpaceID != "ex-1234" {
		t.Fatalf("portal list json = %s err=%v entries=%#v", listJSON, err, entries)
	}

	statusText := runCLIWithApp(t, application, "portal", "status", "ex-1234")
	if !strings.Contains(statusText, "portal default (ok)") || !strings.Contains(statusText, "state: running") {
		t.Fatalf("portal status text = %s", statusText)
	}
	statusJSON := runCLIWithApp(t, application, "portal", "status", "ex-1234", "--json")
	if !strings.Contains(statusJSON, `"portal_id"`) || strings.Contains(statusJSON, `"PortalID"`) {
		t.Fatalf("portal status json keys = %s", statusJSON)
	}
	var status portal.Status
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil || status.Overall != portal.OverallOK || status.Health != "healthy" {
		t.Fatalf("portal status json = %s err=%v status=%#v", statusJSON, err, status)
	}

	doctorText := runCLIWithApp(t, application, "portal", "doctor", "ex-1234")
	if !strings.Contains(doctorText, "portal default doctor (ok)") {
		t.Fatalf("portal doctor text = %s", doctorText)
	}
	doctorJSON := runCLIWithApp(t, application, "portal", "doctor", "ex-1234", "--json")
	if !strings.Contains(doctorJSON, `"overall"`) || strings.Contains(doctorJSON, `"Overall"`) {
		t.Fatalf("portal doctor json keys = %s", doctorJSON)
	}
	var doctor portal.DoctorReport
	if err := json.Unmarshal([]byte(doctorJSON), &doctor); err != nil || doctor.Overall != portal.OverallOK {
		t.Fatalf("portal doctor json = %s err=%v doctor=%#v", doctorJSON, err, doctor)
	}

	inspectText := runCLIWithApp(t, application, "portal", "inspect", "ex-1234")
	if !strings.Contains(inspectText, "portal: default") || !strings.Contains(inspectText, "destroy-preview: stop and remove Stave-owned container") {
		t.Fatalf("portal inspect text = %s", inspectText)
	}
	inspectJSON := runCLIWithApp(t, application, "portal", "inspect", "ex-1234", "--json")
	if !strings.Contains(inspectJSON, `"destroy_dry_run_notes"`) || strings.Contains(inspectJSON, `"DestroyDryRunNotes"`) {
		t.Fatalf("portal inspect json keys = %s", inspectJSON)
	}
	var inspect portal.InspectReport
	if err := json.Unmarshal([]byte(inspectJSON), &inspect); err != nil || inspect.Portal.ID != "default" || len(inspect.OwnedResources) == 0 {
		t.Fatalf("portal inspect json = %s err=%v inspect=%#v", inspectJSON, err, inspect)
	}

	authText := runCLIWithApp(t, application, "portal", "auth", "status", "ex-1234", "--provider", "codex")
	if !strings.Contains(authText, "codex\tnative\tunknown") {
		t.Fatalf("portal auth text = %s", authText)
	}
	authJSON := runCLIWithApp(t, application, "portal", "auth", "status", "ex-1234", "--json")
	var auth portal.Auth
	if err := json.Unmarshal([]byte(authJSON), &auth); err != nil || len(auth.Providers) != 1 || auth.Providers[0].Provider != "codex" {
		t.Fatalf("portal auth json = %s err=%v auth=%#v", authJSON, err, auth)
	}
	if filtered := runCLIWithApp(t, application, "portal", "auth", "status", "ex-1234", "--provider", "cursor"); !strings.Contains(filtered, "not configured for this portal") {
		t.Fatalf("expected filter-miss note, got %q", filtered)
	}
	if _, err := runCLIError(t, application, "portal", "auth", "status", "ex-1234", "--provider", "bad"); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected bad auth status provider error, got %v", err)
	}

	missingRunner := &fakePortalRunner{missing: map[string]bool{"docker": true}}
	missingDoctor := runCLIWithApp(t, &app{portalRunner: missingRunner}, "portal", "doctor", "ex-1234")
	if !strings.Contains(missingDoctor, "driver.binary_missing") {
		t.Fatalf("missing binary doctor output = %s", missingDoctor)
	}
}

// TestCLISagaStatusJSONRedactionContract pins the CLI-side --json pipeline
// for the frozen space.SagaStatus. writeJSON re-encodes through generic maps
// (agent.RedactForJSON), so a whole-payload byte-compare against a typed
// MarshalIndent proves nothing; instead this asserts snake_case keys,
// absence of Go-name keys, and that every realistic ref/URL/PR scalar
// survives the redaction pass value-identically — the emitted payload
// unmarshals back equal to the input struct.
func TestCLISagaStatusJSONRedactionContract(t *testing.T) {
	status := space.SagaStatus{
		SagaID: "pay-1",
		Members: []space.SagaMemberStatus{
			{
				ID:    "pay-1-api",
				After: []string{"pay-1-schema"},
				State: space.MemberLive,
				Dirty: true,
				Repos: []space.SagaRepoStatus{{
					Name:       "api",
					Branch:     "stave/pay-1/api",
					Base:       "refs/heads/stave/pay-1-schema/api",
					Ahead:      3,
					Behind:     1,
					BaseHealth: space.BaseHealthMerged,
					MergedVia:  space.MergedViaPR,
					Note:       `stacks on base refs/heads/stave/pay-1-schema/api owned by "pay-1-schema"`,
				}, {
					Name:       "web",
					Branch:     "stave/pay-1/web",
					Base:       "origin/main",
					BaseHealth: space.BaseHealthOK,
				}},
				PRs: []space.SagaPRStatus{{
					Repo:        "api",
					Number:      41,
					State:       "MERGED",
					MergedAt:    "2026-07-01T12:00:00Z",
					BaseRefName: "main",
				}},
			},
			{ID: "pay-1-schema", State: space.MemberArchived, Error: "/archive/2026/pay-1-schema"},
		},
		Notes: []space.SagaNote{{
			Kind:   space.NoteKindSuggestion,
			Member: "pay-1-api",
			Text:   "repo api: the PR for branch stave/pay-1/api merged but the branch is not an ancestor of refs/remotes/origin/main; see https://github.com/acme/api/pull/41",
		}},
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, status); err != nil {
		t.Fatalf("writeJSON error = %v", err)
	}
	payload := buf.String()
	for _, key := range []string{`"saga_id"`, `"members"`, `"after"`, `"state"`, `"dirty"`, `"repos"`, `"prs"`, `"base_health"`, `"merged_via"`, `"merged_at"`, `"base_ref_name"`, `"notes"`, `"kind"`} {
		if !strings.Contains(payload, key) {
			t.Fatalf("saga status json missing %s:\n%s", key, payload)
		}
	}
	for _, key := range []string{`"SagaID"`, `"Members"`, `"BaseHealth"`, `"MergedVia"`, `"MergedAt"`, `"BaseRefName"`, `"Notes"`} {
		if strings.Contains(payload, key) {
			t.Fatalf("saga status json leaks Go-name key %s:\n%s", key, payload)
		}
	}
	var restored space.SagaStatus
	if err := json.Unmarshal([]byte(payload), &restored); err != nil {
		t.Fatalf("saga status json does not unmarshal into space.SagaStatus: %v\n%s", err, payload)
	}
	if !reflect.DeepEqual(status, restored) {
		t.Fatalf("saga status json round-trip mismatch (redaction mangled a value):\nhave %#v\nwant %#v", restored, status)
	}
	// Spot-check the scalars a redaction pass is most tempted to touch.
	repo := restored.Members[0].Repos[0]
	pr := restored.Members[0].PRs[0]
	if repo.Base != "refs/heads/stave/pay-1-schema/api" || repo.Branch != "stave/pay-1/api" || pr.Number != 41 || pr.BaseRefName != "main" || pr.MergedAt != "2026-07-01T12:00:00Z" {
		t.Fatalf("ref/PR scalars altered: repo=%#v pr=%#v", repo, pr)
	}
	if !strings.Contains(restored.Notes[0].Text, "https://github.com/acme/api/pull/41") {
		t.Fatalf("note URL altered: %q", restored.Notes[0].Text)
	}
}

func TestCLIPortalGuidedInitPromptsPreviewsAndWritesAfterConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	cmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"portal", "init", "ex-1234", "guided"})
	cmd.SetIn(strings.NewReader("devcontainer\ny\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("guided init error = %v\n%s", err, out.String())
	}
	output := out.String()
	commandIndex := strings.Index(output, "Equivalent command: stave portal init devcontainer ex-1234 guided")
	previewIndex := strings.Index(output, "Manifest preview:")
	proceedIndex := strings.Index(output, "Proceed?")
	if !strings.Contains(output, "Portal kind") ||
		commandIndex == -1 ||
		previewIndex == -1 ||
		!strings.Contains(output, "driver: devcontainer") ||
		proceedIndex == -1 {
		t.Fatalf("guided output = %s", output)
	}
	if commandIndex > proceedIndex || previewIndex > proceedIndex {
		t.Fatalf("guided preview must appear before confirmation:\n%s", output)
	}
	manifest, err := portal.LoadManifest(filepath.Join(home, "stave", "agent-work", "ex-1234"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["guided"].Driver != portal.DriverDevcontainer {
		t.Fatalf("guided portal = %#v", manifest.Portals["guided"])
	}
}

func TestCLIPortalGuidedInitCancelDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	cmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"portal", "init", "ex-1234"})
	cmd.SetIn(strings.NewReader("\nn\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("guided init cancel error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Fatalf("guided output = %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", portal.ManifestName)); !os.IsNotExist(err) {
		t.Fatalf("cancel wrote portal manifest: %v", err)
	}
}

func TestCLIPortalInitDryRunPrintsManifestPreview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")

	out := runCLI(t, "portal", "init", "container", "ex-1234", "--image", "alpine:3.20", "--dry-run")
	if !strings.Contains(out, "Manifest preview:") || !strings.Contains(out, "image: alpine:3.20") || !strings.Contains(out, "manifest.preview") {
		t.Fatalf("dry-run output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", portal.ManifestName)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote portal manifest: %v", err)
	}
}

func TestCLIPortalDevcontainerSSHAndEC2(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	application := &app{portalRunner: &fakePortalRunner{}}

	runCLI(t, "portal", "init", "devcontainer", "ex-1234", "dev", "--path", ".devcontainer/devcontainer.json", "--service", "api")
	runCLIWithApp(t, application, "portal", "attach", "ssh", "ex-1234", "devbox.example", "ssh-dev", "--preset", "ssh-codex")
	runCLIWithApp(t, application, "portal", "attach", "ec2", "ex-1234", "i-123", "aws", "--region", "us-west-2", "--ssh-user", "ec2-user", "--host", "203.0.113.10")

	manifest, err := portal.LoadManifest(filepath.Join(home, "stave", "agent-work", "ex-1234"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Portals["dev"].Driver != portal.DriverDevcontainer || manifest.Portals["dev"].Runtime.Service != "api" {
		t.Fatalf("devcontainer = %#v", manifest.Portals["dev"])
	}
	if manifest.Portals["ssh-dev"].Driver != portal.DriverSSH || manifest.Portals["ssh-dev"].Target.Host != "devbox.example" {
		t.Fatalf("ssh = %#v", manifest.Portals["ssh-dev"])
	}
	if manifest.Portals["aws"].Driver != portal.DriverEC2Attach || manifest.Portals["aws"].Target.Region != "us-west-2" || manifest.Portals["aws"].Target.Host != "203.0.113.10" {
		t.Fatalf("ec2 = %#v", manifest.Portals["aws"])
	}
}

func TestCLIPortalDryRunShellSummonAndDetach(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLIWithApp(t, &app{portalRunner: &fakePortalRunner{}}, "portal", "attach", "ssh", "ex-1234", "devbox.example")

	shellCmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return false }})
	shellCmd.SetArgs([]string{"portal", "shell", "ex-1234"})
	var shellOut bytes.Buffer
	shellCmd.SetOut(&shellOut)
	shellCmd.SetErr(&shellOut)
	if err := shellCmd.Execute(); err != nil {
		t.Fatalf("portal shell error = %v\n%s", err, shellOut.String())
	}
	if !strings.Contains(shellOut.String(), "Non-interactive terminal detected") || !strings.Contains(shellOut.String(), "ssh") {
		t.Fatalf("shell output = %s", shellOut.String())
	}

	summonOut := runCLI(t, "portal", "summon", "ex-1234", "--mode", "print", "--with", "codex")
	if !strings.Contains(summonOut, "codex --cd") || !strings.Contains(summonOut, "portal auth login") {
		t.Fatalf("summon output = %s", summonOut)
	}
	runCLI(t, "portal", "detach", "ex-1234")
	manifest, err := portal.LoadManifest(filepath.Join(home, "stave", "agent-work", "ex-1234"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Portals) != 0 {
		t.Fatalf("portal was not detached: %#v", manifest.Portals)
	}
}

func TestCLISummonTmuxOffTTYExecutesDetachedCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	// A default ssh portal so the tmux summon plan has a target to build against.
	runCLIWithApp(t, &app{portalRunner: &fakePortalRunner{}}, "portal", "attach", "ssh", "ex-1234", "devbox.example")

	commandsContain := func(runs []portal.Command, needle string) bool {
		for _, c := range runs {
			if strings.Contains(c.String(), needle) {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name        string
		args        []string
		isTerminal  bool
		wantExec    bool // create command actually ran against the runner
		wantAttach  bool // attach-session command was appended
		wantPrinted bool // plan text printed to stdout (preview)
	}{
		{
			name:       "off-tty mode tmux executes detached create, no attach",
			args:       []string{"portal", "summon", "ex-1234", "--with", "codex", "--mode", "tmux"},
			isTerminal: false,
			wantExec:   true,
			wantAttach: false,
		},
		{
			name:        "off-tty mode tmux with print-command prints only",
			args:        []string{"portal", "summon", "ex-1234", "--with", "codex", "--mode", "tmux", "--print-command"},
			isTerminal:  false,
			wantExec:    false,
			wantAttach:  false,
			wantPrinted: true,
		},
		{
			name:       "real tty mode tmux executes create and attach",
			args:       []string{"portal", "summon", "ex-1234", "--with", "codex", "--mode", "tmux"},
			isTerminal: true,
			wantExec:   true,
			wantAttach: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakePortalRunner{}
			isTerm := tc.isTerminal
			out := runCLIWithApp(t, &app{portalRunner: runner, isTerminal: func(*cobra.Command) bool { return isTerm }}, tc.args...)

			ranCreate := commandsContain(runner.runs, "new-session -d -s stave-ex-1234-default")
			if ranCreate != tc.wantExec {
				t.Fatalf("executed detached create = %v, want %v (runs=%#v)", ranCreate, tc.wantExec, runner.runs)
			}
			if got := commandsContain(runner.runs, "attach-session"); got != tc.wantAttach {
				t.Fatalf("appended attach-session = %v, want %v (runs=%#v)", got, tc.wantAttach, runner.runs)
			}
			if tc.wantExec && strings.Contains(out, "Non-interactive terminal detected") {
				t.Fatalf("mode tmux degraded to preview: %s", out)
			}
			if tc.wantPrinted {
				if len(runner.runs) != 0 {
					t.Fatalf("print-command executed commands: %#v", runner.runs)
				}
				if !strings.Contains(out, "new-session -d -s stave-ex-1234-default") {
					t.Fatalf("print-command did not print create command: %s", out)
				}
			}
		})
	}
}

func TestCLIPortalPlanningCommandsExecuteAndPreview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "portal stdout\n", Stderr: "portal stderr\n"}}
	application := &app{portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return true }}

	for _, tc := range []struct {
		name     string
		args     []string
		want     string
		executes bool
	}{
		{name: "auth login", args: []string{"portal", "auth", "login", "ex-1234", "--provider", "codex", "--method", "native"}, want: "portal stdout", executes: true},
		{name: "auth inherit", args: []string{"portal", "auth", "inherit", "ex-1234", "--provider", "codex", "--method", "env", "--yes"}, want: "auth-inherit for codex"},
		{name: "auth revoke dry-run", args: []string{"portal", "auth", "revoke", "ex-1234", "--provider", "codex", "--target", "all", "--dry-run"}, want: "auth-revoke for codex"},
		{name: "up executes", args: []string{"portal", "up", "ex-1234"}, want: "portal stdout", executes: true},
		{name: "up print command", args: []string{"portal", "up", "ex-1234", "--print-command"}, want: "docker start"},
		{name: "up attach shell print command", args: []string{"portal", "up", "ex-1234", "--print-command", "--attach", "shell", "--workdir", "/workspace/ex-1234/references"}, want: "docker exec -it -w /workspace/ex-1234/references"},
		{name: "exec executes", args: []string{"portal", "exec", "ex-1234", "default", "--", "true"}, want: "portal stdout", executes: true},
		{name: "sync dry-run", args: []string{"portal", "sync", "ex-1234", "--dry-run", "--include", "*.go", "--exclude", ".git", "--delete", "--max-delete", "3", "--yes"}, want: "sync portal default"},
		{name: "sync auto dry-run", args: []string{"portal", "sync", "ex-1234", "--dry-run", "--mode", "auto"}, want: "sync.mount_noop"},
		{name: "logs executes", args: []string{"portal", "logs", "ex-1234", "--tail", "5"}, want: "portal stdout", executes: true},
		{name: "logs preview", args: []string{"portal", "logs", "ex-1234", "--tail", "5", "--follow", "--dry-run"}, want: "show logs for portal default"},
		{name: "down dry-run", args: []string{"portal", "down", "ex-1234", "--timeout", "5", "--force", "--dry-run"}, want: "stop portal default"},
		{name: "destroy dry-run", args: []string{"portal", "destroy", "ex-1234", "--timeout", "5", "--delete-volumes", "--force", "--dry-run"}, want: "destroy Stave-owned resources for portal default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(runner.runs)
			out := runCLIWithApp(t, application, tc.args...)
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
			if tc.executes && len(runner.runs) == before {
				t.Fatalf("%s did not execute runner; output:\n%s", tc.name, out)
			}
			if !tc.executes && len(runner.runs) != before {
				t.Fatalf("%s unexpectedly executed runner: %#v", tc.name, runner.runs[before:])
			}
		})
	}

	runner.err = errors.New("runner stopped")
	out, err := runCLIError(t, application, "portal", "up", "ex-1234")
	if err == nil || !strings.Contains(err.Error(), "runner stopped") || out != "" {
		t.Fatalf("expected portal up runner error, err=%v out=%s", err, out)
	}

	printSummon := runCLIWithApp(t, &app{portalRunner: &fakePortalRunner{}, isTerminal: func(cmd *cobra.Command) bool { return true }}, "portal", "summon", "ex-1234", "--print-command", "--with", "claude")
	if !strings.Contains(printSummon, "claude") || !strings.Contains(printSummon, "portal auth login") {
		t.Fatalf("summon print output = %s", printSummon)
	}
	runner.err = nil
	runCLIWithApp(t, application, "portal", "attach", "ssh", "ex-1234", "devbox.example", "remote")
	detachDryRun := runCLI(t, "portal", "detach", "ex-1234", "remote", "--dry-run")
	if !strings.Contains(detachDryRun, "remove portal metadata remote") {
		t.Fatalf("detach dry-run output = %s", detachDryRun)
	}
}

func TestCLIRunPortalPlanningCommandRunnerPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "portal stdout\n", Stderr: "portal stderr\n"}}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	application := &app{portalRunner: runner}
	build := func(portal.Service) (portal.Plan, error) {
		return portal.Plan{
			Summary:  "exercise fake portal commands",
			Commands: []portal.Command{{Program: "portal-one", Args: []string{"ok"}}, {Program: "portal-two", Args: []string{"ok"}}},
		}, nil
	}

	if err := application.runPortalPlanningCommand(cmd, build, false, false); err != nil {
		t.Fatalf("runPortalPlanningCommand error = %v\n%s", err, out.String())
	}
	if len(runner.runs) != 2 || strings.Contains(out.String(), "portal-one ok") || !strings.Contains(out.String(), "portal stdout") || !strings.Contains(out.String(), "portal stderr") {
		t.Fatalf("runner=%#v out=%s", runner.runs, out.String())
	}

	out.Reset()
	if err := application.runPortalPlanningCommand(cmd, build, true, false); err != nil {
		t.Fatalf("dry-run portal planning error = %v", err)
	}
	if !strings.Contains(out.String(), "portal-one ok") {
		t.Fatalf("dry-run did not print plan: %s", out.String())
	}
	if len(runner.runs) != 2 {
		t.Fatalf("dry-run executed commands: %#v", runner.runs)
	}

	out.Reset()
	if err := application.runPortalPlanningCommand(cmd, build, false, true); err != nil {
		t.Fatalf("print-only portal planning error = %v", err)
	}
	if !strings.Contains(out.String(), "portal-one ok") {
		t.Fatalf("print-only did not print plan: %s", out.String())
	}
	if len(runner.runs) != 2 {
		t.Fatalf("print-only executed commands: %#v", runner.runs)
	}

	runner.err = errors.New("portal runner failed")
	runner.result = portal.RunResult{Stderr: "runtime details"}
	out.Reset()
	if err := application.runPortalPlanningCommand(cmd, build, false, false); err == nil ||
		!strings.Contains(err.Error(), "portal runner failed") ||
		!strings.Contains(err.Error(), "runtime details") {
		t.Fatalf("expected runner error, got %v", err)
	}
}

func TestCLIRunPortalPlanningCommandConditionalCommands(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	createOnMissing := &sequencePortalRunner{errors: []error{errors.New("missing"), nil}}
	application := &app{portalRunner: createOnMissing}
	build := func(portal.Service) (portal.Plan, error) {
		inspect := portal.Command{Program: "docker", Args: []string{"container", "inspect", "name"}, ContinueOnError: true}
		create := portal.Command{Program: "docker", Args: []string{"run", "name"}, RunIfPreviousFailed: true}
		return portal.Plan{Summary: "conditional", Commands: []portal.Command{inspect, create}}, nil
	}
	if err := application.runPortalPlanningCommand(cmd, build, false, false); err != nil {
		t.Fatalf("conditional missing path error = %v", err)
	}
	if len(createOnMissing.runs) != 2 || !strings.Contains(createOnMissing.runs[1].String(), "docker run name") {
		t.Fatalf("missing path runs = %#v", createOnMissing.runs)
	}

	skipCreate := &sequencePortalRunner{}
	application = &app{portalRunner: skipCreate}
	if err := application.runPortalPlanningCommand(cmd, build, false, false); err != nil {
		t.Fatalf("conditional existing path error = %v", err)
	}
	if len(skipCreate.runs) != 1 || !strings.Contains(skipCreate.runs[0].String(), "inspect") {
		t.Fatalf("existing path runs = %#v", skipCreate.runs)
	}
}

func TestCLISplitPortalExecArgs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		spaceID  string
		portalID string
		argv     []string
		dash     int
		wantErr  string
	}{
		{name: "dash separates command", args: []string{"ex-1", "dev", "echo", "hi"}, spaceID: "ex-1", portalID: "dev", argv: []string{"echo", "hi"}, dash: 2},
		{name: "dash with default portal", args: []string{"ex-1", "echo", "hi"}, spaceID: "ex-1", argv: []string{"echo", "hi"}, dash: 1},
		{name: "missing dash is rejected", args: []string{"ex-1", "dev", "echo"}, dash: -1, wantErr: "requires \"--\""},
		{name: "missing command after dash", args: []string{"ex-1", "dev"}, dash: 2, wantErr: "requires a command"},
		{name: "too many positionals before dash", args: []string{"ex-1", "dev", "extra", "echo"}, dash: 3, wantErr: "takes <space-id> [portal-id]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spaceID, portalID, argv, err := splitPortalExecArgs(tc.args, tc.dash)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if spaceID != tc.spaceID || portalID != tc.portalID || strings.Join(argv, "\x00") != strings.Join(tc.argv, "\x00") {
				t.Fatalf("splitPortalExecArgs(%v) = %q %q %v", tc.args, spaceID, portalID, argv)
			}
		})
	}
}

func TestCLISpaceAddSyncArchiveAndDestroy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	runCLI(t, "space", "init", "ex-1234")

	if _, err := runCLIError(t, nil, "space", "add", "ex-1234", "repo-a"); err == nil || !strings.Contains(err.Error(), "choose exactly one") {
		t.Fatalf("expected missing mode error, err=%v", err)
	}
	dryRunRef := runCLI(t, "space", "add", "ex-1234", "repo-a", "--reference", "--base", "main", "--dry-run")
	if !strings.Contains(dryRunRef, "dry-run:") {
		t.Fatalf("reference dry-run output = %s", dryRunRef)
	}
	runCLI(t, "space", "add", "ex-1234", "repo-a", "--edit", "--branch", "ex-1234-repo-a", "--base", "main", "--no-fetch")
	status := runCLI(t, "space", "status", "ex-1234")
	if !strings.Contains(status, "repo-a [edit]") || !strings.Contains(status, "branch: ex-1234-repo-a") {
		t.Fatalf("space status after add = %s", status)
	}
	runCLI(t, "space", "sync", "ex-1234", "--references-only")
	runCLI(t, "space", "archive", "ex-1234", "--force")
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", ".archive", "ex-1234", space.ManifestName)); err != nil {
		t.Fatalf("archive manifest missing: %v", err)
	}

	runCLI(t, "space", "init", "ex-destroy")
	dryDestroy := runCLI(t, "space", "destroy", "ex-destroy", "--dry-run")
	if !strings.Contains(dryDestroy, "dry-run: remove directory") {
		t.Fatalf("destroy dry-run output = %s", dryDestroy)
	}
	runCLI(t, "space", "destroy", "ex-destroy", "--force")
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-destroy")); !os.IsNotExist(err) {
		t.Fatalf("destroy left space directory: %v", err)
	}
}

func TestCLICreateSummonLaunchesAfterCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	cmd := newRootCommand(&app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--summon", "claude"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("space create --summon error = %v\n%s", err, out.String())
	}
	if !launcher.called || launcher.invocation.Summoner != summon.Claude {
		t.Fatalf("launcher = %#v", launcher)
	}
	if launcher.invocation.Dir != filepath.Join(home, "stave", "agent-work", "ex-1234") {
		t.Fatalf("launcher dir = %q", launcher.invocation.Dir)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", space.ManifestName)); err != nil {
		t.Fatalf("space was not created: %v", err)
	}
}

func TestCLICreateSummonForwardsAgentFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	application := &app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }}
	runCLIWithApp(t, application,
		"space", "create", "ex-flags",
		"--summon", "codex",
		"--yolo", "--model", "gpt-5.6",
		"--", "--config", "agent.toml",
	)

	spacePath := filepath.Join(home, "stave", "agent-work", "ex-flags")
	wantPrefix := []string{"--cd", spacePath, "--yolo", "--model", "gpt-5.6", "--config", "agent.toml"}
	if !launcher.called || len(launcher.invocation.Args) != len(wantPrefix)+1 {
		t.Fatalf("launcher invocation = %#v", launcher.invocation)
	}
	for i, want := range wantPrefix {
		if launcher.invocation.Args[i] != want {
			t.Fatalf("launcher args[%d] = %q, want %q; all args=%#v", i, launcher.invocation.Args[i], want, launcher.invocation.Args)
		}
	}
}

func TestCLICreateSummonFlagsDryRunAndTypoGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	out := runCLI(t, "space", "create", "dry-flags", "--summon", "codex", "--yolo", "--dry-run")
	if !strings.Contains(out, "codex --cd") || !strings.Contains(out, "--yolo") {
		t.Fatalf("dry-run summon output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "dry-flags")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created space: %v", err)
	}
	if _, err := runCLIError(t, nil, "space", "create", "typo", "--yolo"); err == nil || !strings.Contains(err.Error(), "unknown flag: --yolo") {
		t.Fatalf("unknown flag without --summon error = %v", err)
	}
}

func TestCLICreateRequestsShellDirectoryEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")

	want := filepath.Join(home, "stave", "agent-work", "enter-me")
	got := captureShellChdir(t, func() {
		runCLI(t, "space", "create", "enter-me")
	})
	if got != want {
		t.Fatalf("shell chdir request = %q, want %q", got, want)
	}

	dry := captureShellChdir(t, func() {
		runCLI(t, "space", "create", "dry-enter", "--dry-run")
	})
	if dry != "" {
		t.Fatalf("dry-run shell chdir request = %q, want empty", dry)
	}
}

func TestCLICreateSummonFailureKeepsSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Summon.Commands["codex"] = filepath.Join(t.TempDir(), "missing-codex")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand(&app{isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--summon", "codex"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("space create --summon unexpectedly succeeded:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "stave", "agent-work", "ex-1234", space.ManifestName)); err != nil {
		t.Fatalf("space was not kept: %v", err)
	}
}

func TestCLICreateSummonFailureStillRequestsShellDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{err: errors.New("launch failed")}
	application := &app{summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }}
	var runErr error
	requestedDir := captureShellChdir(t, func() {
		_, runErr = runCLIError(t, application, "space", "create", "failed-summon", "--summon", "codex")
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "launch codex") {
		t.Fatalf("summon failure error = %v", runErr)
	}
	want := filepath.Join(home, "stave", "agent-work", "failed-summon")
	if requestedDir != want {
		t.Fatalf("failed summon shell chdir request = %q, want %q", requestedDir, want)
	}
}

func TestCLIAgentConfigureWithFakeSecretStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := &fakeSecretStore{available: true}
	cmd := newRootCommand(&app{secretStore: store})
	cmd.SetArgs([]string{"agent", "configure"})
	cmd.SetIn(strings.NewReader("openai\ngpt-test\nsk-test\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent configure error = %v\n%s", err, out.String())
	}
	if store.values["keychain:stave/agent/openai"] != "sk-test" {
		t.Fatalf("secret store = %#v", store.values)
	}
	configBytes, err := os.ReadFile(filepath.Join(home, ".config", "stave", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(configBytes)
	if !strings.Contains(text, "model: gpt-test") || !strings.Contains(text, "apiKeyRef: keychain:stave/agent/openai") {
		t.Fatalf("config missing agent settings:\n%s", text)
	}
}

func TestCLIAgentConfigureFlagsWithoutKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := &fakeSecretStore{available: false}
	cmd := newRootCommand(&app{secretStore: store})
	cmd.SetArgs([]string{"agent", "configure", "--provider", "anthropic", "--model", "claude-test"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent configure flags error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Keychain unavailable") || !strings.Contains(out.String(), "Configured agent provider anthropic") {
		t.Fatalf("configure output = %s", out.String())
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultProvider != agent.ProviderAnthropic || cfg.Agent.Providers[agent.ProviderAnthropic].APIKeyRef != "env:ANTHROPIC_API_KEY" {
		t.Fatalf("agent config = %#v", cfg.Agent)
	}
}

func TestCLIAgentConfigureRejectsInvalidProviderAndBlankSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	invalid := newRootCommand(&app{secretStore: &fakeSecretStore{available: false}})
	invalid.SetArgs([]string{"agent", "configure", "--provider", "other", "--model", "model"})
	var invalidOut bytes.Buffer
	invalid.SetOut(&invalidOut)
	invalid.SetErr(&invalidOut)
	if err := invalid.Execute(); err == nil || !strings.Contains(err.Error(), "provider must be openai or anthropic") {
		t.Fatalf("expected invalid provider error, err=%v out=%s", err, invalidOut.String())
	}

	blank := newRootCommand(&app{secretStore: &fakeSecretStore{available: true}})
	blank.SetArgs([]string{"agent", "configure"})
	blank.SetIn(strings.NewReader("openai\ngpt-test\n\n"))
	var blankOut bytes.Buffer
	blank.SetOut(&blankOut)
	blank.SetErr(&blankOut)
	if err := blank.Execute(); err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("expected blank API key error, err=%v out=%s", err, blankOut.String())
	}
}

func TestCLIAgentPlanOnlyNonTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent query error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "stave repos list") || !strings.Contains(out.String(), "no operations were executed") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestCLIAgentJSONAndIncantExecutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant --json error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if !result.Executed || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentAutoIncantExecutesWithoutFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.AutoIncant = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent auto-incant error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if !result.Executed || len(result.Results) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentNoIncantOverridesAutoIncant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.AutoIncant = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--no-incant", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --no-incant auto-incant error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Executed {
		t.Fatalf("--no-incant did not override autoIncant: %#v", result)
	}
}

func TestCLIAgentTerminalConfirmExecutesAndDeclines(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   string
		executed bool
	}{
		{name: "confirmed", answer: "yes\n", executed: true},
		{name: "declined", answer: "no\n", executed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("OPENAI_API_KEY", "sk-env")
			runCLI(t, "setup")
			factory := func(providerName, model, apiKey string) (agent.Provider, error) {
				return fakeProvider{plan: agent.Plan{Summary: "create space", Operations: []agent.Operation{{Type: agent.OpSpaceCreate, SpaceID: "confirmed-space"}}}}, nil
			}
			cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return true }})
			cmd.SetArgs([]string{"agent", "create a space"})
			cmd.SetIn(strings.NewReader(tc.answer))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("agent terminal confirm error = %v\n%s", err, out.String())
			}
			if strings.Contains(out.String(), "Proceed?") != true {
				t.Fatalf("expected confirmation prompt:\n%s", out.String())
			}
			_, statErr := os.Stat(filepath.Join(home, "stave", "agent-work", "confirmed-space", space.ManifestName))
			if tc.executed && statErr != nil {
				t.Fatalf("confirmed plan did not create space: %v\n%s", statErr, out.String())
			}
			if !tc.executed && !os.IsNotExist(statErr) {
				t.Fatalf("declined plan created space, statErr=%v\n%s", statErr, out.String())
			}
		})
	}
}

func TestCLIAgentNeedsInputJSONDoesNotExecute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{result: agent.RunResult{
			Status:    agent.RunStatusNeedsInput,
			Message:   "Need portal target.",
			Questions: []agent.Question{{ID: "driver", Prompt: "Which portal driver?", Type: "select", Options: []string{"docker", "ssh"}}},
			Plan:      agent.Plan{Operations: []agent.Operation{}},
		}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "set up portal"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent needs_input json error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Status != agent.RunStatusNeedsInput || result.Executed || len(result.Questions) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentAutoIncantNeedsInputDoesNotExecute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.AutoIncant = true
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{result: agent.RunResult{
			Status:    agent.RunStatusNeedsInput,
			Message:   "Need portal target.",
			Questions: []agent.Question{{ID: "driver", Prompt: "Which portal driver?", Type: "select", Options: []string{"docker", "ssh"}}},
			Plan:      agent.Plan{Operations: []agent.Operation{{Type: agent.OpReposList}}},
		}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--json", "set up portal"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent autoIncant needs_input json error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Status != agent.RunStatusNeedsInput || result.Executed || len(result.Results) != 0 {
		t.Fatalf("autoIncant executed needs_input result: %#v", result)
	}
}

func TestCLIAgentNeedsInputNonTTYPrintsQuestions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{result: agent.RunResult{
			Status:    agent.RunStatusNeedsInput,
			Message:   "Need portal target.",
			Questions: []agent.Question{{ID: "driver", Prompt: "Which portal driver?", Type: "select", Options: []string{"docker", "ssh"}}},
			Plan:      agent.Plan{Operations: []agent.Operation{}},
		}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "set up portal"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent needs_input non-tty error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Status: needs_input") || !strings.Contains(out.String(), "Which portal driver?") {
		t.Fatalf("output = %s", out.String())
	}
	if strings.Contains(out.String(), "no operations were executed") {
		t.Fatalf("needs_input should not print generic non-execution warning:\n%s", out.String())
	}
}

func TestCLIAgentTTYNeedsInputCollectsAnswersAndReplans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	provider := &sequenceProvider{results: []agent.RunResult{
		{
			Status:    agent.RunStatusNeedsInput,
			Message:   "Need portal target.",
			Questions: []agent.Question{{ID: "driver", Prompt: "Which portal driver?", Type: "select", Options: []string{"docker", "ssh"}}},
			Plan:      agent.Plan{Operations: []agent.Operation{}},
		},
		{
			Status: agent.RunStatusPlanReady,
			Plan:   agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}},
		},
	}}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return provider, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--no-incant", "set up portal"})
	cmd.SetIn(strings.NewReader("ssh\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent needs_input tty error = %v\n%s", err, out.String())
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if !strings.Contains(provider.queries[1], "- driver: ssh") {
		t.Fatalf("second query did not include answer: %q", provider.queries[1])
	}
	if !strings.Contains(out.String(), "Which portal driver?") || !strings.Contains(out.String(), "Plan: list repos") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestCLIAgentJSONPortalIncantSuppressesExecutionChatter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	runCLI(t, "space", "init", "ex-1234")
	runCLI(t, "portal", "init", "container", "ex-1234")
	runner := &fakePortalRunner{result: portal.RunResult{Stdout: "docker chatter that must stay off stdout"}}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "start portal", Operations: []agent.Operation{{Type: agent.OpPortalUp, SpaceID: "ex-1234"}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, portalRunner: runner, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "start the portal"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent portal --incant --json error = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "docker chatter") || strings.Contains(out.String(), "Plan:") || strings.Contains(out.String(), "Commands:") {
		t.Fatalf("--json emitted execution chatter:\n%s", out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if !result.Executed || len(result.Results) != 1 || len(runner.runs) == 0 {
		t.Fatalf("result=%#v runner=%#v", result, runner.runs)
	}
}

func TestCLIAgentRunAndValidationErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-secret-value")
	runCLI(t, "setup")
	runErrFactory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return errorProvider{err: errors.New("provider saw sk-secret-value")}, nil
	}
	cmd := newRootCommand(&app{providerFactory: runErrFactory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "fail"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("expected redacted provider error, err=%v out=%s", err, out.String())
	}

	validationFactory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "invalid", Operations: []agent.Operation{{Type: agent.OpSpaceStatus}}}}, nil
	}
	cmd = newRootCommand(&app{providerFactory: validationFactory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "invalid"})
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "space id \"\"") {
		t.Fatalf("expected validation error, err=%v out=%s", err, out.String())
	}
}

func TestCLIAgentJSONDoesNotPromptOnTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--json", "list repos"})
	cmd.SetIn(strings.NewReader("y\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --json error = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "Proceed?") {
		t.Fatalf("--json prompted unexpectedly:\n%s", out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if result.Executed {
		t.Fatalf("--json without --incant executed unexpectedly: %#v", result)
	}
}

func TestCLIAgentJSONDoesNotEchoPlannerSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-secret-value")
	runCLI(t, "setup")
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		if apiKey != "sk-secret-value" {
			t.Fatalf("apiKey = %q", apiKey)
		}
		return fakeProvider{plan: agent.Plan{Summary: "list repos", Operations: []agent.Operation{{Type: agent.OpReposList}}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, isTerminal: func(cmd *cobra.Command) bool { return false }})
	cmd.SetArgs([]string{"agent", "--json", "list repos"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --json error = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "sk-secret-value") {
		t.Fatalf("--json leaked planner secret:\n%s", out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
}

func TestCLIQuestionAndProviderHelperBranches(t *testing.T) {
	var out bytes.Buffer
	answers, err := collectQuestionAnswers(strings.NewReader("\n\ncustom\n"), &out, agent.RunResult{
		Message: "Need details.",
		Questions: []agent.Question{
			{ID: "driver", Prompt: "Which portal driver?", Options: []string{"docker", "ssh"}},
			{ID: "host", Prompt: "Host", Required: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answers, "- driver: docker") || !strings.Contains(answers, "- host: custom") || !strings.Contains(out.String(), "A value is required.") {
		t.Fatalf("answers=%q out=%s", answers, out.String())
	}
	ok, err := confirm(strings.NewReader("yes\n"), &out)
	if err != nil || !ok {
		t.Fatalf("confirm yes = %v %v", ok, err)
	}
	ok, err = confirm(strings.NewReader("no\n"), &out)
	if err != nil || ok {
		t.Fatalf("confirm no = %v %v", ok, err)
	}

	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent = config.DefaultAgentConfig()
	name, providerCfg, err := resolveAgentProvider(*cfg, agent.ProviderAnthropic, "override-model")
	if err != nil || name != agent.ProviderAnthropic || providerCfg.Model != "override-model" || providerCfg.APIKeyRef != "env:ANTHROPIC_API_KEY" {
		t.Fatalf("resolve anthropic = %q %#v %v", name, providerCfg, err)
	}
	if _, _, err := resolveAgentProvider(*cfg, "bogus", ""); err == nil {
		t.Fatal("expected invalid provider error")
	}
	if defaultModelForProvider(agent.ProviderAnthropic) != config.DefaultAgentModelAnthropic || defaultModelForProvider(agent.ProviderOpenAI) != config.DefaultAgentModelOpenAI {
		t.Fatal("default model helper mismatch")
	}
	if guidedPortalKind("claude-devcontainer") != "devcontainer" || guidedPortalKind("") != "container" {
		t.Fatal("guided portal kind mismatch")
	}
	if command := guidedPortalCommand("ex-1", "dev", "container", "local-codex"); !strings.Contains(command, "--preset local-codex") {
		t.Fatalf("guided command = %s", command)
	}
	if firstNonEmpty("", "", "value") != "value" || firstNonEmpty("", "") != "" {
		t.Fatal("firstNonEmpty mismatch")
	}

	var planOut bytes.Buffer
	printAgentPlan(&planOut, agent.RunResult{
		Status:   agent.RunStatusUnsupported,
		Message:  "Cannot do that.",
		Executed: true,
		Plan: agent.Plan{
			Summary:    "helper plan",
			Operations: []agent.Operation{{Type: agent.OpReposList}},
			Notes:      []string{"note"},
			Warnings:   []string{"warning"},
		},
		Commands: []string{"stave repos list"},
	})
	if !strings.Contains(planOut.String(), "Status: unsupported") || !strings.Contains(planOut.String(), "Notes:") || !strings.Contains(planOut.String(), "Executed.") {
		t.Fatalf("printAgentPlan output = %s", planOut.String())
	}

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"stave", "--help"}
	if err := ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext help error = %v", err)
	}
}

func TestCLICommandsSurfaceConfigLoadErrors(t *testing.T) {
	badConfig := t.TempDir()
	for _, args := range [][]string{
		{"setup"},
		{"repos", "list"},
		{"repos", "sync"},
		{"repos", "remove", "repo-a"},
		{"space", "status", "ex-1"},
		{"space", "sync", "ex-1"},
		{"space", "archive", "ex-1"},
		{"space", "destroy", "ex-1"},
		{"saga", "create", "epic-1"},
		{"saga", "list"},
		{"saga", "status", "epic-1"},
		{"saga", "sync", "epic-1"},
		{"saga", "add", "epic-1", "ex-1"},
		{"saga", "remove", "epic-1", "ex-1"},
		{"saga", "archive", "epic-1"},
		{"saga", "destroy", "epic-1"},
		{"summon", "ex-1"},
		{"portal", "drivers"},
		{"portal", "doctor", "ex-1"},
		{"portal", "list"},
		{"portal", "status", "ex-1"},
		{"portal", "inspect", "ex-1"},
		{"portal", "auth", "status", "ex-1"},
		{"portal", "auth", "login", "ex-1"},
		{"portal", "auth", "inherit", "ex-1", "--method", "env"},
		{"portal", "auth", "revoke", "ex-1"},
		{"portal", "up", "ex-1"},
		{"portal", "sync", "ex-1"},
		{"portal", "shell", "ex-1", "--print-command"},
		{"portal", "exec", "ex-1", "--", "true"},
		{"portal", "summon", "ex-1", "--print-command"},
		{"portal", "logs", "ex-1"},
		{"portal", "down", "ex-1"},
		{"portal", "detach", "ex-1"},
		{"portal", "destroy", "ex-1"},
		{"agent", "plan"},
		{"agent", "configure"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			argsWithConfig := append([]string{"--config", badConfig}, args...)
			out, err := runCLIError(t, nil, argsWithConfig...)
			if err == nil || !strings.Contains(err.Error(), "read config") {
				t.Fatalf("expected config load error for %v, err=%v out=%s", args, err, out)
			}
		})
	}
}

func TestCLIAgentJSONIncantSkipsSummonLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "create and summon", Operations: []agent.Operation{
			{Type: agent.OpSpaceCreate, SpaceID: "ex-2"},
			{Type: agent.OpSummon, SpaceID: "ex-2", Summoner: "codex"},
		}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--incant", "--json", "create ex-2 and summon codex"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant --json summon error = %v\n%s", err, out.String())
	}
	var result agent.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON:\n%s\n%v", out.String(), err)
	}
	if launcher.called {
		t.Fatal("summon launcher was called during JSON output")
	}
	if len(result.Results) != 2 || !result.Results[0].Executed || result.Results[1].Executed {
		t.Fatalf("result = %#v", result)
	}
}

func TestCLIAgentIncantLaunchesSummonOnTTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	runCLI(t, "setup")
	launcher := &fakeSummonLauncher{}
	factory := func(providerName, model, apiKey string) (agent.Provider, error) {
		return fakeProvider{plan: agent.Plan{Summary: "create and summon", Operations: []agent.Operation{
			{Type: agent.OpSpaceCreate, SpaceID: "ex-2"},
			{Type: agent.OpSummon, SpaceID: "ex-2", Summoner: "cursor"},
		}}}, nil
	}
	cmd := newRootCommand(&app{providerFactory: factory, summonLauncher: launcher, isTerminal: func(cmd *cobra.Command) bool { return true }})
	cmd.SetArgs([]string{"agent", "--incant", "create ex-2 and summon cursor"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent --incant summon error = %v\n%s", err, out.String())
	}
	if !launcher.called || launcher.invocation.Summoner != summon.Cursor {
		t.Fatalf("launcher = %#v", launcher)
	}
}

func TestCLITicketFlagIsRemoved(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"space", "create", "ex-1234", "--ticket", "ticket.md"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("--ticket unexpectedly succeeded:\n%s", out.String())
	}
}

type fakeProvider struct {
	plan   agent.Plan
	result agent.RunResult
}

func (f fakeProvider) Run(ctx context.Context, request agent.ProviderRequest) (agent.RunResult, error) {
	if f.result.Status != "" || f.result.Message != "" || len(f.result.Questions) > 0 || len(f.result.Plan.Operations) > 0 {
		if f.result.Commands == nil {
			f.result.Commands = f.result.Plan.Commands()
		}
		return f.result, nil
	}
	return agent.RunResult{Plan: f.plan, Commands: f.plan.Commands()}, nil
}

type sequenceProvider struct {
	results []agent.RunResult
	queries []string
	calls   int
}

func (f *sequenceProvider) Run(ctx context.Context, request agent.ProviderRequest) (agent.RunResult, error) {
	f.queries = append(f.queries, request.Query)
	index := f.calls
	f.calls++
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	result := f.results[index]
	if result.Commands == nil {
		result.Commands = result.Plan.Commands()
	}
	return result, nil
}

type errorProvider struct {
	err error
}

func (f errorProvider) Run(ctx context.Context, request agent.ProviderRequest) (agent.RunResult, error) {
	return agent.RunResult{}, f.err
}

type fakeSecretStore struct {
	available bool
	values    map[string]string
}

type fakeSummonLauncher struct {
	called     bool
	invocation summon.Invocation
	chdirEnv   string
	err        error
}

type fakePortalRunner struct {
	runs    []portal.Command
	result  portal.RunResult
	err     error
	missing map[string]bool
}

type sequencePortalRunner struct {
	runs    []portal.Command
	results []portal.RunResult
	errors  []error
}

func (f *fakeSummonLauncher) Launch(ctx context.Context, invocation summon.Invocation) error {
	f.called = true
	f.invocation = invocation
	f.chdirEnv = os.Getenv(shellChdirFDEnv)
	return f.err
}

func (f *fakePortalRunner) LookPath(name string) (string, error) {
	if f.missing != nil && f.missing[name] {
		return "", os.ErrNotExist
	}
	return "/bin/" + name, nil
}

func (f *fakePortalRunner) Run(ctx context.Context, command portal.Command) (portal.RunResult, error) {
	_ = ctx
	f.runs = append(f.runs, command)
	if f.err != nil {
		return f.result, f.err
	}
	return f.result, nil
}

func (f *sequencePortalRunner) LookPath(name string) (string, error) {
	return "/bin/" + name, nil
}

func (f *sequencePortalRunner) Run(ctx context.Context, command portal.Command) (portal.RunResult, error) {
	_ = ctx
	f.runs = append(f.runs, command)
	index := len(f.runs) - 1
	var result portal.RunResult
	if index < len(f.results) {
		result = f.results[index]
	}
	var err error
	if index < len(f.errors) {
		err = f.errors[index]
	}
	return result, err
}

func (f *fakeSecretStore) Available() bool {
	return f.available
}

func (f *fakeSecretStore) Put(ctx context.Context, ref string, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[ref] = value
	return nil
}

func (f *fakeSecretStore) Get(ctx context.Context, ref string) (string, error) {
	return f.values[ref], nil
}

func (f *fakeSecretStore) Delete(ctx context.Context, ref string) error {
	delete(f.values, ref)
	return nil
}

func TestCLISpaceCommandsAreNotTopLevel(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("top-level create unexpectedly succeeded:\n%s", out.String())
	}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	return runCLIWithApp(t, nil, args...)
}

func runCLIWithApp(t *testing.T, application *app, args ...string) string {
	t.Helper()
	out, err := runCLIError(t, application, args...)
	if err != nil {
		t.Fatalf("stave %v error = %v\n%s", args, err, out)
	}
	return out
}

func runCLIError(t *testing.T, application *app, args ...string) (string, error) {
	t.Helper()
	var cmd *cobra.Command
	if application == nil {
		cmd = NewRootCommand()
	} else {
		cmd = newRootCommand(application)
	}
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func captureShellChdir(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(shellChdirFDEnv, strconv.Itoa(int(writer.Fd())))
	run()
	// requestShellChdir owns and closes the inherited descriptor when it
	// writes. Close also covers commands such as --dry-run that emit nothing.
	_ = writer.Close()
	data, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func createGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "-b", "main", dir)
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
}

func TestCLIVersion(t *testing.T) {
	out := runCLIWithApp(t, &app{version: "1.2.3", commit: "abc123", date: "2026-08-26"}, "version")
	for _, want := range []string{"stave 1.2.3", "commit: abc123", "date: 2026-08-26"} {
		if !strings.Contains(out, want) {
			t.Fatalf("version output missing %q:\n%s", want, out)
		}
	}
}

func TestCLIVersionFallback(t *testing.T) {
	out := runCLIWithApp(t, &app{}, "version")
	if strings.TrimSpace(out) == "" {
		t.Fatalf("version output empty")
	}
	if !strings.HasPrefix(out, "stave ") {
		t.Fatalf("version output should start with %q:\n%s", "stave ", out)
	}
}

func TestCLIVersionNoArgs(t *testing.T) {
	_, err := runCLIError(t, &app{version: "1.2.3"}, "version", "extra")
	if err == nil {
		t.Fatalf("version with extra arg unexpectedly succeeded")
	}
}

func TestFromBuildInfo(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		bi := &debug.BuildInfo{
			Main: debug.Module{Version: "v1.4.0"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "cafebabe"},
				{Key: "vcs.time", Value: "2026-02-01T00:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			},
		}
		v, c, d := fromBuildInfo(bi)
		if v != "v1.4.0" || c != "cafebabe" || d != "2026-02-01T00:00:00Z" {
			t.Fatalf("fromBuildInfo = %q, %q, %q", v, c, d)
		}
	})
	t.Run("devel version ignored", func(t *testing.T) {
		bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
		v, c, d := fromBuildInfo(bi)
		if v != "" || c != "" || d != "" {
			t.Fatalf("fromBuildInfo(devel) = %q, %q, %q; want empties", v, c, d)
		}
	})
	t.Run("nil", func(t *testing.T) {
		v, c, d := fromBuildInfo(nil)
		if v != "" || c != "" || d != "" {
			t.Fatalf("fromBuildInfo(nil) = %q, %q, %q; want empties", v, c, d)
		}
	})
}

func bareOriginHead(t *testing.T, bare string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("origin/HEAD unresolved in %s: %v\n%s", bare, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCLIReposAddSetsOriginHead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	if head := bareOriginHead(t, bare); head != "origin/main" {
		t.Fatalf("origin/HEAD = %q, want origin/main", head)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["repo-a"].DefaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", cfg.Repos["repo-a"].DefaultBranch)
	}
}

func TestCLIReposAddTrunkDefaultBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(t.TempDir(), "trunky")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "-b", "trunk", src)
	runGit(t, src, "config", "user.name", "Test User")
	runGit(t, src, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# trunky\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "README.md")
	runGit(t, src, "commit", "-m", "initial")

	runCLI(t, "setup")
	out := runCLI(t, "repos", "add", "trunky", src)
	if strings.Contains(out, "could not discover") {
		t.Fatalf("unexpected discovery note:\n%s", out)
	}
	bare := filepath.Join(home, "stave", "bare-repos", "trunky.git")
	if head := bareOriginHead(t, bare); head != "origin/trunk" {
		t.Fatalf("origin/HEAD = %q, want origin/trunk", head)
	}
	cfg, _, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["trunky"].DefaultBranch != "trunk" {
		t.Fatalf("defaultBranch = %q, want trunk", cfg.Repos["trunky"].DefaultBranch)
	}
}

func TestCLIReposSyncBackfillsDefaultBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Repos["repo-a"]
	repo.DefaultBranch = ""
	cfg.Repos["repo-a"] = repo
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, "repos", "sync")
	if !strings.Contains(out, "synced repo-a") || !strings.Contains(out, `recorded default branch "main" for "repo-a"`) {
		t.Fatalf("first sync output = %s", out)
	}
	cfg, path, err = config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["repo-a"].DefaultBranch != "main" {
		t.Fatalf("defaultBranch after backfill = %q", cfg.Repos["repo-a"].DefaultBranch)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Second sync is a no-op: no note, no write.
	out = runCLI(t, "repos", "sync")
	if !strings.Contains(out, "synced repo-a") || strings.Contains(out, "recorded default branch") {
		t.Fatalf("second sync output = %s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("config rewritten by no-op sync:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestCLIReposSyncBackfillSaveFailureEmitsNoRecordedNote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Repos["repo-a"]
	repo.DefaultBranch = ""
	cfg.Repos["repo-a"] = repo
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	// The registry is written atomically via a sibling temp file, so a
	// read-only config directory makes the save (and only the save) fail.
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out, err := runCLIError(t, nil, "repos", "sync")
	if err == nil {
		t.Fatalf("expected save failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "synced repo-a") {
		t.Fatalf("sync itself should have run:\n%s", out)
	}
	if strings.Contains(out, "recorded default branch") {
		t.Fatalf("must not claim the default branch was recorded when the save failed:\n%s", out)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["repo-a"].DefaultBranch != "" {
		t.Fatalf("defaultBranch = %q, want still empty after failed save", cfg.Repos["repo-a"].DefaultBranch)
	}
}

func TestCLIReposSyncDriftNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)

	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Repos["repo-a"]
	repo.DefaultBranch = "nope"
	cfg.Repos["repo-a"] = repo
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, "repos", "sync", "repo-a")
	if !strings.Contains(out, "synced repo-a") || !strings.Contains(out, "is now") || !strings.Contains(out, `registry has "nope"`) {
		t.Fatalf("drift sync output = %s", out)
	}
	if strings.Contains(out, "recorded default branch") {
		t.Fatalf("drift must not auto-update:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("config rewritten on drift:\n--- before\n%s\n--- after\n%s", before, after)
	}
	cfg, _, err = config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["repo-a"].DefaultBranch != "nope" {
		t.Fatalf("defaultBranch = %q, want nope (unchanged)", cfg.Repos["repo-a"].DefaultBranch)
	}
}

func setupRemoveFixture(t *testing.T) (home, bare, cfgPath, src string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	src = createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	bare = filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("bare repo cache missing after add: %v", err)
	}
	cfgPath = filepath.Join(home, ".config", "stave", "config.yaml")
	return home, bare, cfgPath, src
}

func repoRegistered(t *testing.T, cfgPath, name string) bool {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	_, ok := cfg.Repos[name]
	return ok
}

func TestCLIReposRemoveNotesKeptCache(t *testing.T) {
	_, bare, cfgPath, src := setupRemoveFixture(t)
	stdout, stderr, err := runCLISplit(t, "repos", "remove", "repo-a")
	if err != nil {
		t.Fatalf("remove error = %v\n%s%s", err, stdout, stderr)
	}
	if stdout != "unregistered repo-a\n" {
		t.Fatalf("stdout = %q, want exactly %q", stdout, "unregistered repo-a\n")
	}
	for _, want := range []string{"note: kept bare repo cache at " + bare, "--adopt", "<new-name>", src} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "still use it") {
		t.Fatalf("unexpected reference warning:\n%s", stderr)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after remove")
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache should be kept: %v", err)
	}
}

func TestCLIReposRemoveWarnsWhenReferenced(t *testing.T) {
	_, bare, _, _ := setupRemoveFixture(t)
	runCLI(t, "space", "create", "s1", "-e", "repo-a")
	out := runCLI(t, "repos", "remove", "repo-a")
	if !strings.HasPrefix(out, "unregistered repo-a\n") {
		t.Fatalf("output should start with unregistered line:\n%s", out)
	}
	if !strings.Contains(out, "still use it (s1)") || !strings.Contains(out, "do not move or delete it") {
		t.Fatalf("expected reference warning:\n%s", out)
	}
	if strings.Contains(out, "--adopt") {
		t.Fatalf("re-register hint should be suppressed when referenced:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache should be kept: %v", err)
	}
}

func TestCLIReposRemovePurgeDeletesCache(t *testing.T) {
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.HasPrefix(out, "unregistered repo-a\n") || !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("purge output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted, stat err = %v", err)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after purge")
	}
}

func TestCLIReposRemovePurgeRefusedBySpace(t *testing.T) {
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	runCLI(t, "space", "create", "s1", "-e", "repo-a")
	_, err := runCLIError(t, nil, "repos", "remove", "repo-a", "--purge")
	if err == nil || !strings.Contains(err.Error(), "refusing to purge") || !strings.Contains(err.Error(), "s1") {
		t.Fatalf("expected refusal naming s1, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache should be intact after refusal: %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a should still be registered after refusal")
	}
	runCLI(t, "space", "destroy", "s1", "--force")
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("purge after destroy output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted, stat err = %v", err)
	}
}

// TestCLIReposRemovePurgeRefusedByReviewSpace pins the guard against a
// review-created space, whose manifest records the repo as an edit worktree.
func TestCLIReposRemovePurgeRefusedByReviewSpace(t *testing.T) {
	home, _, _ := reviewTestFixture(t)
	reviewTestCreateSpace(t, nil, "review", "repo-a#7")
	bare := filepath.Join(home, "stave", "bare-repos", "repo-a.git")
	cfgPath := filepath.Join(home, ".config", "stave", "config.yaml")

	_, err := runCLIError(t, nil, "repos", "remove", "repo-a", "--purge")
	if err == nil || !strings.Contains(err.Error(), "refusing to purge") || !strings.Contains(err.Error(), "review-repo-a-7") {
		t.Fatalf("expected refusal naming review-repo-a-7, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache should be intact after refusal: %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a should still be registered after refusal")
	}
}

func TestCLIReposRemovePurgeFailsClosedOnCorruptManifest(t *testing.T) {
	home, bare, cfgPath, _ := setupRemoveFixture(t)
	corrupt := filepath.Join(home, "stave", "agent-work", "broken-space")
	if err := os.MkdirAll(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, space.ManifestName), []byte("repos: [unterminated"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLIError(t, nil, "repos", "remove", "repo-a", "--purge")
	if err == nil || !strings.Contains(err.Error(), "refusing to purge") || !strings.Contains(err.Error(), "broken-space") {
		t.Fatalf("expected fail-closed error naming broken-space, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache should be intact: %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a should still be registered")
	}
	// Without --purge the removal proceeds but the note explains the scan failure.
	out := runCLI(t, "repos", "remove", "repo-a")
	if !strings.HasPrefix(out, "unregistered repo-a\n") || !strings.Contains(out, "could not scan spaces") {
		t.Fatalf("remove output = %s", out)
	}
}

func TestCLIReposRemovePurgeMissingWorkDir(t *testing.T) {
	home, bare, cfgPath, _ := setupRemoveFixture(t)
	if err := os.RemoveAll(filepath.Join(home, "stave", "agent-work")); err != nil {
		t.Fatal(err)
	}
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("purge output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted, stat err = %v", err)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after purge")
	}
}

func TestCLIReposRemoveDryRun(t *testing.T) {
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	for _, args := range [][]string{
		{"repos", "remove", "repo-a", "--dry-run"},
		{"repos", "remove", "repo-a", "--purge", "--dry-run"},
	} {
		out := runCLI(t, args...)
		if !strings.Contains(out, "dry-run: unregister repo-a") || !strings.Contains(out, "would") || !strings.Contains(out, bare) {
			t.Fatalf("%v output = %s", args, out)
		}
		if strings.Contains(out, "unregistered repo-a") {
			t.Fatalf("%v dry-run printed the real completion line:\n%s", args, out)
		}
		if !repoRegistered(t, cfgPath, "repo-a") {
			t.Fatalf("%v unregistered the repo", args)
		}
		if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
			t.Fatalf("%v touched the cache: %v", args, err)
		}
	}
	keep := runCLI(t, "repos", "remove", "repo-a", "--dry-run")
	if !strings.Contains(keep, "would keep bare repo cache") || strings.Contains(keep, "remove directory") {
		t.Fatalf("keep dry-run output = %s", keep)
	}
	purge := runCLI(t, "repos", "remove", "repo-a", "--purge", "--dry-run")
	if !strings.Contains(purge, "would delete bare repo cache") || !strings.Contains(purge, "dry-run: remove directory "+bare) {
		t.Fatalf("purge dry-run output = %s", purge)
	}
}

func TestCLIReposRemovePurgeDeleteFailureKeepsRegistration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	// Strip write permission from every directory in the cache so RemoveAll
	// cannot unlink anything inside it.
	chmodDirs := func(mode os.FileMode) {
		t.Helper()
		if err := filepath.WalkDir(bare, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.Chmod(p, mode)
			}
			return nil
		}); err != nil {
			t.Fatalf("chmod cache dirs: %v", err)
		}
	}
	chmodDirs(0o500)
	t.Cleanup(func() {
		// Best-effort: the cache is gone once the retry purge succeeds.
		_ = filepath.WalkDir(bare, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})

	_, err := runCLIError(t, nil, "repos", "remove", "repo-a", "--purge")
	if err == nil || !strings.Contains(err.Error(), "delete bare repo cache at "+bare) || !strings.Contains(err.Error(), "still registered") {
		t.Fatalf("expected delete failure that keeps registration, err = %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a should still be registered after a failed purge")
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache HEAD should survive a failed purge: %v", err)
	}

	chmodDirs(0o755)
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("retry purge output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted on retry, stat err = %v", err)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after retry")
	}
}

func TestCLIReposRemovePurgeRefusesNonBarePath(t *testing.T) {
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	plain := filepath.Join(t.TempDir(), "precious")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(plain, "keep.txt")
	if err := os.WriteFile(keep, []byte("do not delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Config surgery: point the registration at a directory that is not a
	// bare repo, as a mis-edited config might.
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Repos["repo-a"]
	repo.BareRepoPath = plain
	cfg.Repos["repo-a"] = repo
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"repos", "remove", "repo-a", "--purge"},
		{"repos", "remove", "repo-a", "--purge", "--dry-run"},
	} {
		_, err := runCLIError(t, nil, args...)
		if err == nil || !strings.Contains(err.Error(), "refusing to purge: "+plain+" is not a bare git repository (git --git-dir "+plain+" rev-parse --is-bare-repository failed") || !strings.Contains(err.Error(), "delete it manually") {
			t.Fatalf("%v err = %v, want not-bare refusal naming the git error", args, err)
		}
		if data, err := os.ReadFile(keep); err != nil || string(data) != "do not delete" {
			t.Fatalf("%v touched the directory: data=%q err=%v", args, data, err)
		}
		if !repoRegistered(t, cfgPath, "repo-a") {
			t.Fatalf("%v unregistered the repo", args)
		}
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("real cache should be untouched: %v", err)
	}
}

// TestCLIReposRemovePurgeRefusesForeignBareRepo pins that --purge deletes
// only a cache whose origin names the registered repository: a registration
// re-pointed at somebody else's bare repo must not take it down.
func TestCLIReposRemovePurgeRefusesForeignBareRepo(t *testing.T) {
	_, bare, cfgPath, src := setupRemoveFixture(t)
	srcB := createGitRepo(t, "repo-b")
	foreign := filepath.Join(t.TempDir(), "b.git")
	runGit(t, "", "clone", "--bare", srcB, foreign)

	pointAt := func(path string) {
		t.Helper()
		cfg, _, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		repo := cfg.Repos["repo-a"]
		repo.BareRepoPath = path
		cfg.Repos["repo-a"] = repo
		if err := cfg.Save(cfgPath); err != nil {
			t.Fatal(err)
		}
	}
	pointAt(foreign)

	want := "refusing to purge: " + foreign + " is a bare repo for " + srcB + ", not " + src + "; fix the registration or delete it manually"
	for _, args := range [][]string{
		{"repos", "remove", "repo-a", "--purge"},
		{"repos", "remove", "repo-a", "--purge", "--dry-run"},
	} {
		_, err := runCLIError(t, nil, args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v err = %v, want %q", args, err, want)
		}
		if _, err := os.Stat(filepath.Join(foreign, "HEAD")); err != nil {
			t.Fatalf("%v deleted the foreign bare repo: %v", args, err)
		}
		if !repoRegistered(t, cfgPath, "repo-a") {
			t.Fatalf("%v unregistered the repo", args)
		}
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("real cache should be untouched: %v", err)
	}

	// A cache with no origin at all cannot prove its identity either.
	pointAt(bare)
	runGit(t, "", "--git-dir", bare, "remote", "remove", "origin")
	_, err := runCLIError(t, nil, "repos", "remove", "repo-a", "--purge")
	if err == nil || !strings.Contains(err.Error(), "refusing to purge: could not read origin remote of "+bare) {
		t.Fatalf("missing origin: err = %v, want refusal", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cache without origin was deleted: %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a was unregistered despite refused purge")
	}

	// With the registration pointing at its own cache again, purge proceeds.
	runGit(t, "", "--git-dir", bare, "remote", "add", "origin", src)
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("purge output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted, stat err = %v", err)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after purge")
	}
	if _, err := os.Stat(filepath.Join(foreign, "HEAD")); err != nil {
		t.Fatalf("foreign bare repo should survive the real purge: %v", err)
	}
}

// TestCLIReposRemovePurgeAcceptsEquivalentOriginSpelling pins that the
// identity check tolerates the same repository spelled differently (here a
// file:// URL for a local path), so a legitimately adopted cache still purges.
func TestCLIReposRemovePurgeAcceptsEquivalentOriginSpelling(t *testing.T) {
	_, bare, cfgPath, src := setupRemoveFixture(t)
	runGit(t, "", "--git-dir", bare, "remote", "set-url", "origin", "file://"+src)
	out := runCLI(t, "repos", "remove", "repo-a", "--purge")
	if !strings.Contains(out, "deleted bare repo cache at "+bare) {
		t.Fatalf("purge output = %s", out)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("cache should be deleted, stat err = %v", err)
	}
	if repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("repo-a still registered after purge")
	}
}

func TestCLIReposRemoveDryRunReportsReferences(t *testing.T) {
	_, bare, cfgPath, _ := setupRemoveFixture(t)
	runCLI(t, "space", "create", "s1", "-e", "repo-a")

	stdout, stderr, err := runCLISplit(t, "repos", "remove", "repo-a", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run error = %v\n%s%s", err, stdout, stderr)
	}
	if stdout != "dry-run: unregister repo-a\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "note: would keep bare repo cache at "+bare+"; 1 space(s) still use it (s1)") || !strings.Contains(stderr, "do not move or delete it") {
		t.Fatalf("dry-run should show the same reference warning as the real run:\n%s", stderr)
	}
	if strings.Contains(stderr, "--adopt") {
		t.Fatalf("re-register hint should be suppressed when referenced:\n%s", stderr)
	}

	_, err = runCLIError(t, nil, "repos", "remove", "repo-a", "--purge", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "refusing to purge") || !strings.Contains(err.Error(), "s1") {
		t.Fatalf("purge dry-run should refuse like the real run, err = %v", err)
	}
	if !repoRegistered(t, cfgPath, "repo-a") {
		t.Fatal("dry-run unregistered the repo")
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("dry-run touched the cache: %v", err)
	}
}

func TestCLIReposRemoveRecipeIsShellQuoted(t *testing.T) {
	home := filepath.Join(t.TempDir(), "my home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	src := createGitRepo(t, "repo-a")
	runCLI(t, "setup")
	runCLI(t, "repos", "add", "repo-a", src)
	bareDir := filepath.Join(home, "stave", "bare-repos")
	bare := filepath.Join(bareDir, "repo-a.git")

	for _, args := range [][]string{
		{"repos", "remove", "repo-a", "--dry-run"},
		{"repos", "remove", "repo-a"},
	} {
		_, stderr, err := runCLISplit(t, args...)
		if err != nil {
			t.Fatalf("%v error = %v\n%s", args, err, stderr)
		}
		want := "mv '" + bare + "' '" + bareDir + "'/<new-name>.git && stave repos add <new-name> " + shellQuote(src) + " --adopt"
		if !strings.Contains(stderr, want) {
			t.Fatalf("%v recipe not shell-quoted; want %q in:\n%s", args, want, stderr)
		}
	}
	if _, err := runCLIError(t, nil, "repos", "add", "repo-a", src); err == nil || !strings.Contains(err.Error(), "stave repos add repo-a "+shellQuote(src)+" --adopt") {
		t.Fatalf("collision hint err = %v", err)
	}
}

// TestCLICredentialedURLNeverEchoed registers a URL carrying a token and
// checks that no output or error of the commands that display or embed the
// registered URL leaks it.
func TestCLICredentialedURLNeverEchoed(t *testing.T) {
	const secret = "tok3n"
	credURL := "https://user:" + secret + "@ghe.example.com/acme/x.git"

	home, _, _, _ := setupRemoveFixture(t)
	setRegisteredURL(t, "repo-a", credURL)

	stdout, stderr, err := runCLISplit(t, "repos", "remove", "repo-a")
	if err != nil {
		t.Fatalf("remove error = %v\n%s%s", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Fatalf("repos remove leaked the credential:\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "stave repos add <new-name> <url> --adopt") {
		t.Fatalf("remove recipe should use the <url> placeholder:\n%s", stderr)
	}

	// Near match: the PR names acme/x on github.com; the registration
	// points at the same owner/repo under another host with a token.
	src := createGitRepo(t, "x")
	runCLI(t, "repos", "add", "x-alias", src)
	setRegisteredURL(t, "x-alias", credURL)
	out, err := runCLIError(t, nil, "review", "https://github.com/acme/x/pull/1")
	assertErrContainsAll(t, err, `"x-alias"`, "https://***@ghe.example.com/acme/x.git", "--repo")
	if strings.Contains(out+err.Error(), secret) {
		t.Fatalf("near-match review error leaked the credential:\n%s\n%v", out, err)
	}

	// Multi near match renders every candidate URL.
	runCLI(t, "repos", "add", "x-other", src)
	setRegisteredURL(t, "x-other", "ssh://git:"+secret+"@ghe2.example.com/acme/x.git")
	out, err = runCLIError(t, nil, "review", "https://github.com/acme/x/pull/1")
	assertErrContainsAll(t, err, "x-alias", "x-other", "ssh://***@ghe2.example.com/acme/x.git", `host "ghe2.example.com"`)
	if strings.Contains(out+err.Error(), secret) {
		t.Fatalf("multi near-match review error leaked the credential:\n%s\n%v", out, err)
	}

	// Collision hint on repos add embeds the URL in a pasteable command.
	if err := os.MkdirAll(filepath.Join(home, "stave", "bare-repos", "y.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = runCLIError(t, nil, "repos", "add", "y", "https://user:"+secret+"@github.com/acme/y.git")
	assertErrContainsAll(t, err, "bare repo path already exists", "stave repos add y <url> --adopt")
	if strings.Contains(out+err.Error(), secret) {
		t.Fatalf("collision hint leaked the credential:\n%s\n%v", out, err)
	}
}
