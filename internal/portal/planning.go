package portal

import (
	"context"
	"fmt"
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
}

type ExecOptions struct {
	SpaceID  string
	PortalID string
	Command  []string
	CWD      string
	User     string
	TTY      TTYMode
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
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
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
		plan.Commands = append(plan.Commands, sshCommand(portal, "mkdir -p "+quoteRemote(portal.Workspace.RemoteRoot)))
	case DriverEC2Attach:
		plan.Commands = append(plan.Commands, ec2DescribeCommand(portal))
	}
	if attach == "shell" {
		plan.Commands = append(plan.Commands, portalExecCommand(portal, []string{DefaultShell}, portalCWD(portal, opts.Workdir), "", TTYAuto, true))
	}
	return plan, nil
}

func (s Service) PlanShell(ctx context.Context, opts ShellOptions) (Plan, error) {
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
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
	plan.Commands = append(plan.Commands, portalExecCommand(portal, []string{shell}, cwd, opts.User, tty, true))
	return plan, nil
}

func (s Service) PlanExec(ctx context.Context, opts ExecOptions) (Plan, error) {
	if len(opts.Command) == 0 {
		return Plan{}, fmt.Errorf("exec command is required")
	}
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	cwd := portalCWD(portal, opts.CWD)
	tty := firstTTY(opts.TTY, TTYNever)
	plan := Plan{Operation: "exec", Summary: fmt.Sprintf("run command in portal %s for space %s", portal.ID, opts.SpaceID)}
	plan.Commands = append(plan.Commands, portalExecCommand(portal, opts.Command, cwd, opts.User, tty, false))
	return plan, nil
}

func (s Service) PlanSummon(ctx context.Context, opts SummonOptions) (Plan, error) {
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
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
	interactive := opts.Mode != "headless"
	plan.Commands = append(plan.Commands, portalExecCommand(portal, agentCommand, portalCWD(portal, ""), "", TTYAuto, interactive))
	return plan, nil
}

func (s Service) PlanSync(ctx context.Context, opts SyncOptions) (Plan, error) {
	portal, spacePath, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
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

func portalExecCommand(portal Portal, argv []string, cwd, user string, tty TTYMode, interactive bool) Command {
	switch portal.Driver {
	case DriverDocker:
		args := []string{"exec"}
		if tty == TTYAlways || (tty == TTYAuto && interactive) {
			args = append(args, "-it")
		}
		if user != "" {
			args = append(args, "-u", user)
		}
		if cwd != "" {
			args = append(args, "-w", cwd)
		}
		args = append(args, portal.Runtime.ContainerName)
		args = append(args, argv...)
		return command(portal.Runtime.Engine, args...)
	case DriverDevcontainer:
		args := []string{"exec", "--workspace-folder", portal.Workspace.LocalPath}
		if portal.Runtime.DevcontainerPath != "" {
			args = append(args, "--config", portal.Runtime.DevcontainerPath)
		}
		args = append(args, argv...)
		return command("devcontainer", args...)
	case DriverSSH, DriverEC2Attach:
		remote := "cd " + quoteRemote(cwd) + " && exec " + joinRemote(argv)
		return sshCommand(portal, remote)
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

func sshCommand(portal Portal, remoteCommand string) Command {
	args := sshArgs(portal)
	args = append(args, sshDestination(portal), remoteCommand)
	return command("ssh", args...)
}

func sshArgs(portal Portal) []string {
	args := []string{}
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

func reconstructCommands(portal Portal, opts SyncOptions) ([]Command, error) {
	if !isRemoteDriver(portal.Driver) {
		return nil, fmt.Errorf("reconstruct mode requires ssh or ec2 attach portal")
	}
	metadata := []string{".stave.yaml", ManifestName, "AGENTS.md", "spec/"}
	args := baseRsyncArgs(opts)
	args = append(args, "--relative")
	args = append(args, metadata...)
	args = append(args, remoteEndpoint(portal)+"/")
	return []Command{command("rsync", args...)}, nil
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

func applyPreset(preset string, portal *Portal) {
	switch preset {
	case "", "custom":
		return
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
	}
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
	switch summoner {
	case "codex":
		if opts.Mode == "headless" {
			return []string{"codex", "exec", "--cd", portalCWD(portal, ""), "--sandbox", permission, "--json", prompt}, nil
		}
		return []string{"codex", "--cd", portalCWD(portal, ""), prompt}, nil
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
	if host == "" && portal.Driver == DriverEC2Attach {
		host = portal.Target.InstanceID
	}
	if portal.Target.SSHUser != "" {
		return portal.Target.SSHUser + "@" + host
	}
	return host
}

func remoteEndpoint(portal Portal) string {
	return sshDestination(portal) + ":" + portal.Workspace.RemoteRoot
}

func quoteRemote(value string) string {
	return quoteShell(value)
}

func joinRemote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, quoteRemote(arg))
	}
	return strings.Join(parts, " ")
}
