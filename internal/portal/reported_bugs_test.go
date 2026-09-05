package portal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRsyncProtectsReferenceWorktreesByDefault(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanSync(ctx, SyncOptions{SpaceID: "ex-1", PortalID: "ssh", Mode: SyncRsync, MaxDelete: -1}); err == nil {
		t.Fatal("negative max-delete was accepted")
	}

	plan, err := svc.PlanSync(ctx, SyncOptions{
		SpaceID:    "ex-1",
		PortalID:   "ssh",
		Mode:       SyncRsync,
		Direction:  SyncBoth,
		Include:    []string{"/***"},
		Delete:     true,
		MaxDelete:  7,
		AllowDirty: true,
		Yes:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("both-direction sync commands = %d, want 2", len(plan.Commands))
	}
	for _, command := range plan.Commands {
		protected := slices.Index(command.Args, "--exclude=/references/")
		broadInclude := slices.Index(command.Args, "--include=/***")
		if protected < 0 {
			t.Fatalf("default rsync does not protect root references: %v", command.Args)
		}
		if broadInclude < 0 || protected > broadInclude {
			t.Fatalf("reference protection must precede caller includes: %v", command.Args)
		}
		if !slices.Contains(command.Args, "--max-delete=7") {
			t.Fatalf("native max-delete guard missing: %v", command.Args)
		}
	}

	references, err := svc.PlanSync(ctx, SyncOptions{
		SpaceID:        "ex-1",
		PortalID:       "ssh",
		Mode:           SyncRsync,
		ReferencesOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := references.Commands[0].Args
	if slices.Contains(args, "--exclude=/references/") {
		t.Fatalf("explicit references-only sync was disabled: %v", args)
	}
	if !slices.Contains(args, "--include=references/***") || !slices.Contains(args, "--exclude=*") {
		t.Fatalf("references-only filters missing: %v", args)
	}
}

func TestAttachRsyncProtectsReferenceWorktrees(t *testing.T) {
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	plan, err := svc.AttachSSH(context.Background(), AttachSSHOptions{
		SpaceID:  "ex-1",
		PortalID: "ssh",
		Host:     "devbox",
		SyncMode: SyncRsync,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("attach commands = %v, want mkdir + rsync", plan.EquivalentCommands())
	}
	if !slices.Contains(plan.Commands[1].Args, "--exclude=/references/") {
		t.Fatalf("attach-time rsync does not protect references: %v", plan.Commands[1].Args)
	}
}

func TestHeadlessCodexAndEnvAuthRegression(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanAuthInherit(ctx, AuthCommandOptions{SpaceID: "ex-1", PortalID: "ssh", Provider: "codex", Method: AuthEnv, Yes: true}); err != nil {
		t.Fatal(err)
	}

	// Planning a login must not reset a previously selected inheritance mode.
	if _, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", PortalID: "ssh", Provider: "codex", Method: "device"}); err != nil {
		t.Fatal(err)
	}
	defaultLogin, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", PortalID: "ssh", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(defaultLogin.Commands[0].String(), "--device-auth") {
		t.Fatalf("remote codex login did not default to device auth: %s", defaultLogin.Commands[0].String())
	}
	explicitNative, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", PortalID: "ssh", Provider: "codex", Method: AuthNative})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(explicitNative.Commands[0].String(), "--device-auth") {
		t.Fatalf("explicit native login was overridden: %s", explicitNative.Commands[0].String())
	}
	portal, _, err := svc.LoadPortal(SelectOptions{SpaceID: "ex-1", PortalID: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if portal.Auth.Mode != AuthEnv || len(portal.Auth.Providers) != 1 || portal.Auth.Providers[0].Mode != AuthEnv || portal.Auth.Providers[0].Status != AuthOK {
		t.Fatalf("auth login planning reset inherited env auth: %#v", portal.Auth)
	}

	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "ssh", With: "codex", Mode: "headless"})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "auth.preflight_missing" {
			t.Fatalf("env auth marked ok still failed summon preflight: %#v", plan.Diagnostics)
		}
	}
	remote := plan.Commands[0].Args[len(plan.Commands[0].Args)-1]
	if !strings.Contains(remote, "--skip-git-repo-check") {
		t.Fatalf("headless codex command lacks repo-check bypass: %s", remote)
	}
	if !strings.Contains(plan.Commands[0].String(), "SendEnv=OPENAI_API_KEY") {
		t.Fatalf("env auth was not forwarded: %s", plan.Commands[0].String())
	}
}

func TestRemoteCommandsUseLoginShell(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}

	plan, err := svc.PlanExec(ctx, ExecOptions{SpaceID: "ex-1", PortalID: "ssh", Command: []string{"agent tool", "it's", "$HOME", "$(false)"}})
	if err != nil {
		t.Fatal(err)
	}
	remote := plan.Commands[0].Args[len(plan.Commands[0].Args)-1]
	if !strings.HasPrefix(remote, `exec "${SHELL:-/bin/sh}" -lc `) {
		t.Fatalf("remote exec does not enter the target login shell: %s", remote)
	}
	for _, literal := range []string{"agent tool", "it", "$HOME", "$(false)"} {
		if !strings.Contains(remote, literal) {
			t.Fatalf("remote command lost %q: %s", literal, remote)
		}
	}

	const payload = "spaces ' quotes $HOME $(printf bad) `printf bad`"
	body := "printf %s " + quoteRemote(payload)
	cmd := exec.Command("sh", "-c", remoteLoginCommand(body))
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != payload {
		t.Fatalf("login-shell serialization output = %q, want %q", out, payload)
	}
}

func TestRemoteTmuxUsesLoginShellForClientAndPane(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }
	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "ssh", With: "codex", Mode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	remote := plan.Commands[0].Args[len(plan.Commands[0].Args)-1]
	if !strings.HasPrefix(remote, `exec "${SHELL:-/bin/sh}" -lc `) {
		t.Fatalf("tmux client does not use target login shell: %s", remote)
	}
	if strings.Count(remote, `${SHELL:-/bin/sh}`) < 2 {
		t.Fatalf("new tmux pane does not start its own login shell: %s", remote)
	}
}

func TestRemoteTmuxLoginShellRoundTripsPaneArgv(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	binDir := t.TempDir()
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "ssh", Host: "devbox", RemoteRoot: binDir}); err != nil {
		t.Fatal(err)
	}
	svc.IsTerminal = func() bool { return false }
	prompt := `look at "spec"; it's $(not-code) and ` + "`still-not-code`"
	plan, err := svc.PlanSummon(ctx, SummonOptions{SpaceID: "ex-1", PortalID: "ssh", With: "codex", Mode: "tmux", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	remote := plan.Commands[0].Args[len(plan.Commands[0].Args)-1]

	argsPath := filepath.Join(binDir, "tmux-args")
	shellPath := filepath.Join(binDir, "target-shell")
	tmuxPath := filepath.Join(binDir, "tmux")
	writeExecutable := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(shellPath, "#!/bin/sh\nif [ \"$1\" = -lc ]; then shift; exec /bin/sh -c \"$1\"; fi\nexec /bin/sh \"$@\"\n")
	writeExecutable(tmuxPath, "#!/bin/sh\nif [ \"$1\" = has-session ]; then exit 1; fi\nprintf '%s\\n' \"$@\" > \"$TMUX_ARGS_OUT\"\n")

	cmd := exec.Command("sh", "-c", remote)
	cmd.Env = append(os.Environ(), "SHELL="+shellPath, "PATH="+binDir+":/usr/bin:/bin", "TMUX_ARGS_OUT="+argsPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("serialized tmux command failed: %v\n%s\ncommand: %s", err, output, remote)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(args) < 7 {
		t.Fatalf("tmux argv too short: %#v", args)
	}
	if args[0] != "new-session" || args[1] != "-d" || args[2] != "-s" || args[3] != "stave-ex-1-ssh" {
		t.Fatalf("tmux control argv changed: %#v", args)
	}
	if args[4] != shellPath || args[5] != "-lc" {
		t.Fatalf("pane login shell argv = %#v", args[4:])
	}
	if got, want := args[6], "cd "+quoteRemotePath(binDir)+" && exec codex --cd . "+quoteRemote(prompt); got != want {
		t.Fatalf("pane command = %q, want %q", got, want)
	}
}

func TestResolvePortalIDFallsBackToSolePortal(t *testing.T) {
	manifest := Manifest{Portals: map[string]Portal{"work": {ID: "work"}}}
	if got, err := resolvePortalID(manifest, ""); err != nil || got != "work" {
		t.Fatalf("sole portal resolution = %q, %v", got, err)
	}
	manifest.Portals[DefaultPortalID] = Portal{ID: DefaultPortalID}
	if got, err := resolvePortalID(manifest, ""); err != nil || got != DefaultPortalID {
		t.Fatalf("default portal resolution = %q, %v", got, err)
	}
	delete(manifest.Portals, DefaultPortalID)
	manifest.Portals["other"] = Portal{ID: "other"}
	if _, err := resolvePortalID(manifest, ""); err == nil || !strings.Contains(err.Error(), "specify one of: other, work") {
		t.Fatalf("ambiguous portal resolution error = %v", err)
	}
	if got, err := resolvePortalID(manifest, " work "); err != nil || got != "work" {
		t.Fatalf("explicit portal resolution = %q, %v", got, err)
	}
}

func TestServiceOperationsFallBackToSolePortal(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	writeSpace(t, cfg, "ex-1")
	svc := NewService(cfg, fakeRunner{}, nil)
	if _, err := svc.AttachSSH(ctx, AttachSSHOptions{SpaceID: "ex-1", PortalID: "work", Host: "devbox"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Configure(ConfigureOptions{SpaceID: "ex-1", Agent: "claude"}); err != nil {
		t.Fatalf("configure with omitted sole portal: %v", err)
	}
	if _, err := svc.PlanAuthLogin(ctx, AuthCommandOptions{SpaceID: "ex-1", Provider: "codex", Method: "device"}); err != nil {
		t.Fatalf("auth login with omitted sole portal: %v", err)
	}
	logs, err := svc.PlanLogs(ctx, LogsOptions{SpaceID: "ex-1"})
	if err != nil {
		t.Fatalf("logs with omitted sole portal: %v", err)
	}
	remote := logs.Commands[0].Args[len(logs.Commands[0].Args)-1]
	if !strings.HasPrefix(remote, `exec "${SHELL:-/bin/sh}" -lc `) {
		t.Fatalf("remote logs do not use the target login shell: %s", remote)
	}
	if _, err := svc.Detach(DetachOptions{SpaceID: "ex-1"}); err != nil {
		t.Fatalf("detach with omitted sole portal: %v", err)
	}
}
