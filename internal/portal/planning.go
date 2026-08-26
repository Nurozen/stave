package portal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nurozen/stave/internal/space"
)

type SyncDirection string

const (
	SyncTo   SyncDirection = "to"
	SyncFrom SyncDirection = "from"
	SyncBoth SyncDirection = "both"
)

type TTYMode string

const (
	TTYAuto   TTYMode = "auto"
	TTYAlways TTYMode = "always"
	TTYNever  TTYMode = "never"
)

type UpOptions struct {
	SpaceID  string
	PortalID string
	Attach   string
	Workdir  string
	DryRun   bool
}

type ShellOptions struct {
	SpaceID  string
	PortalID string
	CWD      string
	User     string
	TTY      TTYMode
	Shell    string
	DryRun   bool
}

type ExecOptions struct {
	SpaceID  string
	PortalID string
	Command  []string
	CWD      string
	User     string
	TTY      TTYMode
	DryRun   bool
}

type SummonOptions struct {
	SpaceID    string
	PortalID   string
	With       string
	Mode       string
	Permission string
	Prompt     string
	DryRun     bool
}

type SyncOptions struct {
	SpaceID        string
	PortalID       string
	Direction      SyncDirection
	Mode           SyncMode
	ReferencesOnly bool
	Include        []string
	Exclude        []string
	Delete         bool
	MaxDelete      int
	AllowDirty     bool
	DryRun         bool
	Yes            bool
}

func (s Service) PlanUp(ctx context.Context, opts UpOptions) (Plan, error) {
	portal, _, err := s.loadPortalForPlan(ctx, SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID}, opts.DryRun)
	if err != nil {
		return Plan{}, err
	}
	attach := opts.Attach
	if attach == "" {
		attach = "none"
	}
	if attach != "none" && attach != "shell" {
		return Plan{}, fmt.Errorf("attach mode must be shell or none")
	}
	plan := Plan{Operation: "up", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("start or validate portal %s for space %s", portal.ID, opts.SpaceID)}
	switch portal.Driver {
	case DriverDocker:
		engine := portal.Runtime.Engine
		start := command(engine, "start", portal.Runtime.ContainerName)
		start.ContinueOnError = true
		plan.Commands = append(plan.Commands, start)
		args := []string{"run", "-d", "--name", portal.Runtime.ContainerName, "-w", portal.Workspace.ContainerRoot}
		for _, label := range sortedLabels(portal.Runtime.Labels) {
			args = append(args, "--label", label)
		}
		args = append(args, "-v", portal.Workspace.LocalPath+":"+portal.Workspace.ContainerRoot, portal.Runtime.Image, "sleep", "infinity")
		create := command(engine, args...)
		create.RunIfPreviousFailed = true
		plan.Commands = append(plan.Commands, create)
	case DriverDevcontainer:
		plan.Commands = append(plan.Commands, devcontainerCommand(portal, "up"))
	case DriverSSH:
		plan.Commands = append(plan.Commands, remotePrepareCommands(portal, false)...)
	case DriverEC2Attach:
		plan.Commands = append(plan.Commands, ec2DescribeCommand(portal))
		plan.Commands = append(plan.Commands, remotePrepareCommands(portal, false)...)
	}
	if attach == "shell" {
		plan.Commands = append(plan.Commands, s.portalExecCommand(portal, []string{DefaultShell}, portalCWD(portal, opts.Workdir), "", TTYAuto, true))
	}
	return plan, nil
}

func (s Service) PlanShell(ctx context.Context, opts ShellOptions) (Plan, error) {
	if err := validateTTYMode(opts.TTY); err != nil {
		return Plan{}, err
	}
	portal, _, err := s.loadPortalForPlan(ctx, SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID}, opts.DryRun)
	if err != nil {
		return Plan{}, err
	}
	shell := opts.Shell
	if shell == "" {
		shell = DefaultShell
	}
	cwd := portalCWD(portal, opts.CWD)
	tty := firstTTY(opts.TTY, TTYAuto)
	plan := Plan{Operation: "shell", Summary: fmt.Sprintf("open shell in portal %s for space %s", portal.ID, opts.SpaceID)}
	plan.Commands = append(plan.Commands, s.portalExecCommand(portal, []string{shell}, cwd, opts.User, tty, true))
	return plan, nil
}

func (s Service) PlanExec(ctx context.Context, opts ExecOptions) (Plan, error) {
	if len(opts.Command) == 0 {
		return Plan{}, fmt.Errorf("exec command is required")
	}
	if err := validateTTYMode(opts.TTY); err != nil {
		return Plan{}, err
	}
	portal, _, err := s.loadPortalForPlan(ctx, SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID}, opts.DryRun)
	if err != nil {
		return Plan{}, err
	}
	cwd := portalCWD(portal, opts.CWD)
	tty := firstTTY(opts.TTY, TTYNever)
	plan := Plan{Operation: "exec", Summary: fmt.Sprintf("run command in portal %s for space %s", portal.ID, opts.SpaceID)}
	plan.Commands = append(plan.Commands, s.portalExecCommand(portal, opts.Command, cwd, opts.User, tty, false))
	return plan, nil
}

func (s Service) PlanSummon(ctx context.Context, opts SummonOptions) (Plan, error) {
	portal, _, err := s.loadPortalForPlan(ctx, SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID}, opts.DryRun)
	if err != nil {
		return Plan{}, err
	}
	if err := validateSummonMode(opts.Mode); err != nil {
		return Plan{}, err
	}
	if err := validateSummonPermission(opts.Permission); err != nil {
		return Plan{}, err
	}
	summoner := normalizeSummoner(opts.With)
	agentCommand, err := summonCommand(summoner, portal, opts)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Operation: "summon", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("launch %s inside portal %s for space %s", summoner, portal.ID, opts.SpaceID)}
	if !authProviderOK(portal.Auth.Providers, summoner) {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Component:  "auth",
			Severity:   SeverityWarn,
			Code:       "auth.preflight_missing",
			Message:    fmt.Sprintf("%s auth is not confirmed for this portal", summoner),
			NextAction: fmt.Sprintf("run portal auth login --provider %s before summoning", summoner),
		})
	}
	if summoner == "cursor" {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Component:  "summon",
			Severity:   SeverityWarn,
			Code:       "summon.cursor_partial",
			Message:    "cursor summon launches cursor-agent without prompt or permission wiring",
			NextAction: "drive cursor-agent interactively after it starts",
		})
	}
	cwd := portalCWD(portal, "")
	switch opts.Mode {
	case "tmux":
		session := tmuxSessionName(opts.SpaceID, portal.ID)
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Component: "summon",
			Severity:  SeverityInfo,
			Code:      "summon.tmux_requires_tmux",
			Message:   "tmux mode requires tmux installed on the portal target",
		})
		// `tmux new-session -A -d` does NOT stay detached on an existing
		// session: with -A, new-session behaves like attach-session and only -D
		// (not lowercase -d) is honored on that path, so a re-summon would try to
		// attach — blocking on a TTY or failing "open terminal failed" on a
		// non-TTY. Guard the detached create with has-session so a fresh session
		// is created detached and an existing one is a true no-op. The guard is a
		// single shell compound, so it works in both the CLI and agent executors
		// without relying on ContinueOnError/RunIfPreviousFailed (D15).
		plan.Commands = append(plan.Commands, s.tmuxCreateCommand(portal, session, agentCommand, cwd))
		// attach-session blocks without a PTY, so only append it when a real
		// terminal is attached and we are not merely previewing (dry-run /
		// print-command). Otherwise start detached and tell the user how to
		// attach (D18 — prevents CI/agent-executor hangs; P2 — no attach in
		// preview).
		if !opts.DryRun && s.stdioIsTerminal() {
			attachArgv := []string{"tmux", "attach-session", "-t", session}
			plan.Commands = append(plan.Commands, s.portalExecCommandFor(portal, attachArgv, cwd, "", TTYAuto, true, agentCommand[0]))
		} else {
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
				Component:  "summon",
				Severity:   SeverityInfo,
				Code:       "summon.tmux_detached",
				Message:    fmt.Sprintf("session %s started; attach with: tmux attach -t %s", session, session),
				NextAction: fmt.Sprintf("tmux attach -t %s", session),
			})
		}
	default:
		interactive := opts.Mode != "headless"
		plan.Commands = append(plan.Commands, s.portalExecCommand(portal, agentCommand, cwd, "", TTYAuto, interactive))
	}
	return plan, nil
}

// tmuxCreateCommand builds the guarded, always-detached tmux create for summon
// (tmux mode):
//
//	tmux has-session -t <session> 2>/dev/null || tmux new-session -d -s <session> <agent...>
//
// For ssh/ec2 the command is already serialized into a remote shell string, so
// the `||` compound is embedded directly, preserving the per-arg shell quoting
// of the agent argv (including the free-text prompt). For docker/devcontainer
// the executor runs argv directly with no shell, so the compound is wrapped in
// `sh -c "..."` to give it a shell. Provider auth-env propagation is keyed off
// the nested summoner (agentCommand[0]) either way (D16).
func (s Service) tmuxCreateCommand(portal Portal, session string, agentCommand []string, cwd string) Command {
	envProgram := agentCommand[0]
	newSession := append([]string{"tmux", "new-session", "-d", "-s", session}, agentCommand...)
	switch portal.Driver {
	case DriverSSH, DriverEC2Attach:
		guard := "tmux has-session -t " + quoteRemote(session) + " 2>/dev/null || exec " + joinRemote(newSession)
		// Group the guard so a failed `cd` gates BOTH branches (otherwise
		// `cd && A || B` runs B in the wrong directory on cd failure).
		remote := "cd " + quoteRemotePath(cwd) + " && { " + guard + " ; }"
		cmd := sshCommandWithEnv(portal, inheritedEnvNames(portal, envProgram), remote)
		cmd.Interactive = false
		return cmd
	default:
		guard := "tmux has-session -t " + quoteShell(session) + " 2>/dev/null || " + joinRemote(newSession)
		return s.portalExecCommandFor(portal, []string{"sh", "-c", guard}, cwd, "", TTYAuto, false, envProgram)
	}
}

// tmuxSessionName is the summoner-independent session name shared by summon
// (tmux mode) and logs pane-capture. Portals do not persist a summoner (D17),
// so the name keys only off space and portal.
func tmuxSessionName(spaceID, portalID string) string {
	return fmt.Sprintf("stave-%s-%s", spaceID, portalID)
}

func (s Service) PlanSync(ctx context.Context, opts SyncOptions) (Plan, error) {
	portal, spacePath, err := s.loadPortalForPlan(ctx, SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID}, opts.DryRun)
	if err != nil {
		return Plan{}, err
	}
	mode := opts.Mode
	if mode == "" || mode == "auto" {
		mode = portal.Workspace.SyncMode
	}
	direction := opts.Direction
	if direction == "" {
		direction = SyncTo
	}
	if direction != SyncTo && direction != SyncFrom && direction != SyncBoth {
		return Plan{}, fmt.Errorf("sync direction must be to, from, or both")
	}
	if opts.Delete && !opts.DryRun && !opts.Yes {
		return Plan{}, fmt.Errorf("sync delete requires dry-run preview or explicit yes")
	}
	if (direction == SyncFrom || direction == SyncBoth) && !opts.AllowDirty {
		if err := s.ensureNoDirtyEdits(ctx, spacePath, opts.SpaceID); err != nil {
			return Plan{}, err
		}
	}
	plan := Plan{Operation: "sync", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("sync portal %s for space %s", portal.ID, opts.SpaceID)}
	switch mode {
	case SyncMount:
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "sync", Severity: SeverityInfo, Code: "sync.mount_noop", Message: "mount sync uses the host-visible workspace; no file copy command is needed"})
	case SyncRsync:
		if !isRemoteDriver(portal.Driver) {
			return Plan{}, fmt.Errorf("rsync mode requires ssh or ec2 attach portal")
		}
		commands, err := rsyncCommands(portal, opts, direction)
		if err != nil {
			return Plan{}, err
		}
		plan.Commands = append(plan.Commands, commands...)
	case SyncReconstruct:
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "sync", Severity: SeverityInfo, Code: "sync.reconstruct_preview", Message: "reconstruct mode copies manifests/specs and recreates worktrees before syncing deltas"})
		commands, err := reconstructCommands(portal, opts)
		if err != nil {
			return Plan{}, err
		}
		plan.Commands = append(plan.Commands, commands...)
	default:
		return Plan{}, fmt.Errorf("sync mode %q is not supported", mode)
	}
	if opts.MaxDelete > 0 {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "sync", Severity: SeverityInfo, Code: "sync.max_delete", Message: "max-delete is recorded for CLI enforcement after dry-run parsing", Evidence: fmt.Sprintf("%d", opts.MaxDelete)})
	}
	return plan, nil
}

func (s Service) portalExecCommand(portal Portal, argv []string, cwd, user string, tty TTYMode, interactive bool) Command {
	program := ""
	if len(argv) > 0 {
		program = argv[0]
	}
	return s.portalExecCommandFor(portal, argv, cwd, user, tty, interactive, program)
}

// portalExecCommandFor behaves like portalExecCommand but derives inherited
// provider env from envProgram rather than argv[0]. This matters when the real
// program is wrapped by another (e.g. tmux new-session <agent...>): argv[0]
// becomes "tmux", but the provider env that must survive belongs to the nested
// summoner (D16).
func (s Service) portalExecCommandFor(portal Portal, argv []string, cwd, user string, tty TTYMode, interactive bool, envProgram string) Command {
	providerEnv := inheritedEnvNames(portal, envProgram)
	tty = s.resolveTTY(tty)
	wantTTY := tty == TTYAlways || (tty == TTYAuto && interactive)
	switch portal.Driver {
	case DriverDocker:
		args := []string{"exec"}
		if wantTTY {
			args = append(args, "-it")
		} else if interactive {
			// Keep stdin wired through without allocating a TTY so piped
			// interactive invocations do not fail with "not a TTY".
			args = append(args, "-i")
		}
		for _, name := range providerEnv {
			args = append(args, "-e", name)
		}
		if user != "" {
			args = append(args, "-u", user)
		}
		if cwd != "" {
			args = append(args, "-w", cwd)
		}
		args = append(args, portal.Runtime.ContainerName)
		args = append(args, argv...)
		cmd := command(portal.Runtime.Engine, args...)
		cmd.Interactive = interactive
		return cmd
	case DriverDevcontainer:
		args := []string{"exec", "--workspace-folder", portal.Workspace.LocalPath}
		if portal.Runtime.DevcontainerPath != "" {
			args = append(args, "--config", portal.Runtime.DevcontainerPath)
		}
		args = append(args, argv...)
		cmd := command("devcontainer", args...)
		cmd.Interactive = interactive
		return cmd
	case DriverSSH, DriverEC2Attach:
		remote := "cd " + quoteRemotePath(cwd) + " && exec " + joinRemote(argv)
		var extra []string
		if wantTTY {
			extra = []string{"-t"}
		}
		cmd := sshCommandWithEnv(portal, providerEnv, remote, extra...)
		cmd.Interactive = interactive
		return cmd
	default:
		return Command{}
	}
}

func devcontainerCommand(portal Portal, action string) Command {
	args := []string{action, "--workspace-folder", portal.Workspace.LocalPath}
	if portal.Runtime.DevcontainerPath != "" {
		args = append(args, "--config", portal.Runtime.DevcontainerPath)
	}
	return command("devcontainer", args...)
}

// sshCommand builds a non-interactive ssh invocation: BatchMode fails fast
// instead of hanging on an auth prompt, and -n keeps ssh from draining stdin.
func sshCommand(portal Portal, remoteCommand string) Command {
	args := append([]string{"-n", "-o", "BatchMode=yes"}, sshArgs(portal)...)
	args = append(args, sshDestination(portal), remoteCommand)
	return command("ssh", args...)
}

func sshCommandWithEnv(portal Portal, envNames []string, remoteCommand string, extraArgs ...string) Command {
	args := append(append([]string{}, extraArgs...), sshArgs(portal)...)
	for _, name := range envNames {
		args = append(args, "-o", "SendEnv="+name)
	}
	args = append(args, sshDestination(portal), remoteCommand)
	return command("ssh", args...)
}

func sshArgs(portal Portal) []string {
	args := []string{"-o", "ConnectTimeout=10"}
	if portal.Target.Port > 0 {
		args = append(args, "-p", fmt.Sprintf("%d", portal.Target.Port))
	}
	if portal.Target.IdentityPath != "" {
		args = append(args, "-i", portal.Target.IdentityPath)
	}
	if portal.Target.KnownHostsPath != "" {
		args = append(args, "-o", "UserKnownHostsFile="+portal.Target.KnownHostsPath)
	}
	if portal.Target.StrictHostKey != "" {
		args = append(args, "-o", "StrictHostKeyChecking="+portal.Target.StrictHostKey)
	}
	return args
}

func ec2DescribeCommand(portal Portal) Command {
	args := []string{"ec2", "describe-instances", "--instance-ids", portal.Target.InstanceID}
	if portal.Target.Region != "" {
		args = append(args, "--region", portal.Target.Region)
	}
	if portal.Target.Profile != "" {
		args = append(args, "--profile", portal.Target.Profile)
	}
	return command("aws", args...)
}

func rsyncCommands(portal Portal, opts SyncOptions, direction SyncDirection) ([]Command, error) {
	if portal.Workspace.RemoteRoot == "" {
		return nil, fmt.Errorf("remote root is required for rsync")
	}
	var commands []Command
	if direction == SyncTo || direction == SyncBoth {
		commands = append(commands, rsyncCommand(portal, opts, portal.Workspace.LocalPath+"/", remoteEndpoint(portal)+"/"))
	}
	if direction == SyncFrom || direction == SyncBoth {
		commands = append(commands, rsyncCommand(portal, opts, remoteEndpoint(portal)+"/", portal.Workspace.LocalPath+"/"))
	}
	return commands, nil
}

func remotePrepareCommands(portal Portal, sync bool) []Command {
	if !isRemoteDriver(portal.Driver) || portal.Workspace.RemoteRoot == "" || strings.TrimSpace(portal.Target.Host) == "" {
		return nil
	}
	commands := []Command{sshCommand(portal, "mkdir -p "+quoteRemotePath(portal.Workspace.RemoteRoot))}
	if sync {
		var syncCommands []Command
		var err error
		switch portal.Workspace.SyncMode {
		case SyncReconstruct:
			syncCommands, err = reconstructCommands(portal, SyncOptions{})
		default:
			syncCommands, err = rsyncCommands(portal, SyncOptions{}, SyncTo)
		}
		if err == nil {
			commands = append(commands, syncCommands...)
		}
	}
	return commands
}

func reconstructCommands(portal Portal, opts SyncOptions) ([]Command, error) {
	if !isRemoteDriver(portal.Driver) {
		return nil, fmt.Errorf("reconstruct mode requires ssh or ec2 attach portal")
	}
	if strings.TrimSpace(portal.Workspace.LocalPath) == "" {
		return nil, fmt.Errorf("reconstruct mode requires a local space path")
	}
	metadata := []string{".stave.yaml", ManifestName, space.AgentsName}
	for _, optional := range []string{space.ClaudeName, "spec/"} {
		_, err := os.Lstat(filepath.Join(portal.Workspace.LocalPath, optional))
		if err == nil {
			metadata = append(metadata, optional)
			continue
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	args := baseRsyncArgs(opts)
	args = append(args, "--relative")
	args = append(args, metadata...)
	args = append(args, remoteEndpoint(portal)+"/")
	cmd := command("rsync", args...)
	cmd.Dir = portal.Workspace.LocalPath
	return []Command{cmd}, nil
}

func rsyncCommand(portal Portal, opts SyncOptions, source, dest string) Command {
	args := baseRsyncArgs(opts)
	if sshOptions := sshArgs(portal); len(sshOptions) > 0 {
		args = append(args, "-e", strings.Join(append([]string{"ssh"}, sshOptions...), " "))
	}
	if opts.ReferencesOnly {
		args = append(args, "--include=references/***", "--exclude=*")
	}
	for _, include := range opts.Include {
		args = append(args, "--include="+include)
	}
	for _, exclude := range opts.Exclude {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, source, dest)
	return command("rsync", args...)
}

func baseRsyncArgs(opts SyncOptions) []string {
	args := []string{"-aiz", "--delay-updates", "--partial-dir=.rsync-partial", "--exclude=.git", "--exclude=.git/"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.Delete {
		args = append(args, "--delete-delay")
	}
	return args
}

func (s Service) ensureNoDirtyEdits(ctx context.Context, spacePath, spaceID string) error {
	spaceManifest, err := spaceManifest(spacePath)
	if err != nil {
		return err
	}
	var dirty []string
	for _, repo := range spaceManifest.Repos {
		if repo.Mode != "edit" {
			continue
		}
		isDirty, _, err := s.Git.IsDirty(ctx, filepath.Join(spacePath, repo.Path))
		if err != nil {
			return err
		}
		if isDirty {
			dirty = append(dirty, repo.Name)
		}
	}
	if len(dirty) > 0 {
		sort.Strings(dirty)
		return fmt.Errorf("space %q has dirty editable worktrees: %s", spaceID, strings.Join(dirty, ", "))
	}
	return nil
}

func spaceManifest(spacePath string) (space.Manifest, error) {
	return space.LoadManifest(spacePath)
}

func applyPreset(preset string, portal *Portal) error {
	switch preset {
	case "", "custom":
		return nil
	case "local-codex":
		portal.Workspace.SyncMode = SyncMount
		portal.Auth.Mode = AuthNative
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("codex", AuthNative)}
	case "local-claude":
		portal.Workspace.SyncMode = SyncMount
		portal.Auth.Mode = AuthNative
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("claude", AuthNative)}
	case "claude-devcontainer":
		portal.Workspace.SyncMode = SyncMount
		portal.Auth.Mode = AuthNative
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("claude", AuthNative)}
	case "ssh-codex":
		portal.Workspace.SyncMode = SyncRsync
		portal.Auth.Mode = AuthRemoteLogin
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("codex", AuthRemoteLogin)}
	case "ssh-claude":
		portal.Workspace.SyncMode = SyncRsync
		portal.Auth.Mode = AuthRemoteLogin
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("claude", AuthRemoteLogin)}
	default:
		return fmt.Errorf("preset %q is not supported; use local-codex, local-claude, claude-devcontainer, ssh-codex, or ssh-claude", preset)
	}
	return nil
}

func summonCommand(summoner string, portal Portal, opts SummonOptions) ([]string, error) {
	prompt := opts.Prompt
	if prompt == "" {
		prompt = "Inspect .stave.yaml, AGENTS.md, spec/, editable repos, and references/ before making changes."
	}
	permission := opts.Permission
	if permission == "" {
		permission = "workspace-write"
	}
	// The exec wrapper already switches into the workspace (docker -w /
	// remote cd with $HOME expansion); a literal --cd '~/...' would never be
	// tilde-expanded on the remote side, so point codex at the current dir.
	cwd := portalCWD(portal, "")
	if isRemoteDriver(portal.Driver) {
		cwd = "."
	}
	switch summoner {
	case "codex":
		if opts.Mode == "headless" {
			return []string{"codex", "exec", "--cd", cwd, "--sandbox", permission, "--json", prompt}, nil
		}
		return []string{"codex", "--cd", cwd, prompt}, nil
	case "claude":
		if opts.Mode == "headless" {
			return []string{"claude", "-p", "--output-format", "stream-json", prompt}, nil
		}
		return []string{"claude", prompt}, nil
	case "cursor":
		return []string{"cursor-agent"}, nil
	default:
		return nil, fmt.Errorf("unsupported summoner %q", summoner)
	}
}

func authProviderOK(providers []AuthProvider, provider string) bool {
	for _, status := range providers {
		if status.Provider == provider && status.Status == AuthOK {
			return true
		}
	}
	return false
}

func normalizeSummoner(value string) string {
	if value == "" {
		return "codex"
	}
	if value == "cursor-agent" {
		return "cursor"
	}
	return value
}

func portalCWD(portal Portal, cwd string) string {
	if cwd != "" {
		return cwd
	}
	if portal.Workspace.ContainerRoot != "" && isLocalDriver(portal.Driver) {
		return portal.Workspace.ContainerRoot
	}
	if portal.Workspace.RemoteRoot != "" {
		return portal.Workspace.RemoteRoot
	}
	return portal.Workspace.LocalPath
}

func firstTTY(value, fallback TTYMode) TTYMode {
	if value != "" {
		return value
	}
	return fallback
}

func validateTTYMode(value TTYMode) error {
	switch value {
	case "", TTYAuto, TTYAlways, TTYNever:
		return nil
	default:
		return fmt.Errorf("tty mode %q is not supported; use auto, always, or never", value)
	}
}

func validateSummonMode(mode string) error {
	switch mode {
	case "", "foreground", "tmux", "headless", "print":
		return nil
	default:
		return fmt.Errorf("summon mode %q is not supported; use foreground, tmux, headless, or print", mode)
	}
}

func validateSummonPermission(permission string) error {
	switch permission {
	case "", "read-only", "workspace-write":
		return nil
	default:
		return fmt.Errorf("summon permission %q is not supported; use read-only or workspace-write", permission)
	}
}

func sortedLabels(labels map[string]string) []string {
	parts := make([]string, 0, len(labels))
	for key, value := range labels {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return parts
}

func sshDestination(portal Portal) string {
	host := portal.Target.Host
	if portal.Target.SSHUser != "" {
		return portal.Target.SSHUser + "@" + host
	}
	return host
}

func inheritedEnvNames(portal Portal, program string) []string {
	if program == "" {
		return nil
	}
	provider := commandProvider(program)
	if provider == "" {
		return nil
	}
	for _, current := range portal.Auth.Providers {
		if current.Provider == provider && current.Mode == AuthEnv && current.Status == AuthOK {
			return providerEnvNames(provider)
		}
	}
	return nil
}

func commandProvider(program string) string {
	switch filepath.Base(program) {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	case "cursor-agent":
		return "cursor"
	default:
		return ""
	}
}

func providerEnvNames(provider string) []string {
	switch provider {
	case "codex":
		return []string{"OPENAI_API_KEY", "CODEX_HOME"}
	case "claude":
		return []string{"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"}
	case "cursor":
		return []string{"CURSOR_API_KEY"}
	default:
		return nil
	}
}

func remoteEndpoint(portal Portal) string {
	return sshDestination(portal) + ":" + portal.Workspace.RemoteRoot
}

func quoteRemote(value string) string {
	return quoteShell(value)
}

// quoteRemotePath quotes a remote filesystem path while keeping a leading
// tilde meaningful: single-quoting '~/x' would make the remote shell create a
// literal directory named '~', diverging from rsync's host:~/x endpoint which
// does expand to $HOME. A leading ~/ is therefore rewritten to "$HOME"/.
func quoteRemotePath(value string) string {
	if value == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		return `"$HOME"/` + quoteShell(rest)
	}
	return quoteShell(value)
}

func joinRemote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, quoteRemote(arg))
	}
	return strings.Join(parts, " ")
}
