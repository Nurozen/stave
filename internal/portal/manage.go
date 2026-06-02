package portal

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type ConfigureOptions struct {
	SpaceID       string
	PortalID      string
	SyncMode      SyncMode
	ContainerRoot string
	RemoteRoot    string
	Agent         string
	AuthMode      AuthMode
	DryRun        bool
}

type AuthCommandOptions struct {
	SpaceID  string
	PortalID string
	Provider string
	Method   AuthMode
	Target   string
	DryRun   bool
	Yes      bool
}

type LogsOptions struct {
	SpaceID  string
	PortalID string
	Agent    string
	Follow   bool
	Tail     int
}

type DownOptions struct {
	SpaceID  string
	PortalID string
	Timeout  int
	Force    bool
	DryRun   bool
}

type DetachOptions struct {
	SpaceID  string
	PortalID string
	DryRun   bool
}

type DestroyOptions struct {
	SpaceID          string
	PortalID         string
	Timeout          int
	DeleteVolumes    bool
	DeleteRemoteData bool
	Force            bool
	DryRun           bool
}

func (s Service) Configure(opts ConfigureOptions) (Plan, error) {
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	portal, ok := manifest.Portals[portalID]
	if !ok {
		return Plan{}, fmt.Errorf("portal %q is not registered for space %q", portalID, opts.SpaceID)
	}
	if opts.SyncMode != "" {
		portal.Workspace.SyncMode = opts.SyncMode
	}
	if opts.ContainerRoot != "" {
		portal.Workspace.ContainerRoot = opts.ContainerRoot
	}
	if opts.RemoteRoot != "" {
		portal.Workspace.RemoteRoot = opts.RemoteRoot
	}
	if opts.AuthMode != "" {
		portal.Auth.Mode = opts.AuthMode
		for i := range portal.Auth.Providers {
			portal.Auth.Providers[i].Mode = opts.AuthMode
		}
	}
	if opts.Agent != "" {
		if err := validateProviderName(opts.Agent); err != nil {
			return Plan{}, err
		}
		if !hasProvider(portal.Auth.Providers, opts.Agent) {
			portal.Auth.Providers = append(portal.Auth.Providers, defaultAuthProvider(opts.Agent, portal.Auth.Mode))
		}
	}
	applyPortalDefaults(opts.SpaceID, &portal)
	if err := ValidatePortal(opts.SpaceID, portal); err != nil {
		return Plan{}, err
	}
	manifest.Portals[portalID] = portal
	plan := Plan{Operation: "configure", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("update portal %s for space %s", portalID, opts.SpaceID)}
	if err := setManifestPreview(&plan, spacePath, manifest); err != nil {
		return Plan{}, err
	}
	if opts.DryRun {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "manifest", Severity: SeverityInfo, Code: "manifest.preview", Message: "dry-run only; manifest was not written", Evidence: filepath.Join(spacePath, ManifestName)})
		return plan, nil
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s Service) PlanAuthLogin(ctx context.Context, opts AuthCommandOptions) (Plan, error) {
	if err := validateAuthLoginMethod(opts.Provider, opts.Method); err != nil {
		return Plan{}, err
	}
	return s.planAuthCommand(ctx, "auth-login", opts, authLoginArgv(opts.Provider, string(opts.Method)))
}

func (s Service) PlanAuthInherit(ctx context.Context, opts AuthCommandOptions) (Plan, error) {
	if err := validateAuthInheritMethod(opts.Method); err != nil {
		return Plan{}, err
	}
	if !opts.DryRun && !opts.Yes {
		return Plan{}, fmt.Errorf("auth inherit requires --yes unless --dry-run is used")
	}
	return s.planAuthCommand(ctx, "auth-inherit", opts, []string{"true"})
}

func (s Service) PlanAuthRevoke(ctx context.Context, opts AuthCommandOptions) (Plan, error) {
	_ = ctx
	target := firstString(opts.Target, "portal")
	if err := validateAuthTarget(target); err != nil {
		return Plan{}, err
	}
	if !opts.DryRun && !opts.Yes {
		return Plan{}, fmt.Errorf("auth revoke requires --yes unless --dry-run is used")
	}
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	if err := validateProviderName(opts.Provider); err != nil {
		return Plan{}, err
	}
	plan := Plan{Operation: "auth-revoke", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("auth-revoke for %s in portal %s", opts.Provider, portal.ID)}
	if target == "local" || target == "all" {
		plan.Commands = append(plan.Commands, commandFromArgv(authLogoutArgv(opts.Provider)))
	}
	if target == "portal" || target == "all" {
		plan.Commands = append(plan.Commands, portalExecCommand(portal, authLogoutArgv(opts.Provider), portalCWD(portal, ""), "", TTYAuto, true))
	}
	return plan, nil
}

func (s Service) planAuthCommand(ctx context.Context, operation string, opts AuthCommandOptions, argv []string) (Plan, error) {
	_ = ctx
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	if err := validateProviderName(opts.Provider); err != nil {
		return Plan{}, err
	}
	plan := Plan{Operation: operation, DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("%s for %s in portal %s", operation, opts.Provider, portal.ID)}
	plan.Commands = append(plan.Commands, portalExecCommand(portal, argv, portalCWD(portal, ""), "", TTYAuto, true))
	return plan, nil
}

func (s Service) PlanLogs(ctx context.Context, opts LogsOptions) (Plan, error) {
	_ = ctx
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	if opts.Tail == 0 {
		opts.Tail = 100
	}
	plan := Plan{Operation: "logs", Summary: fmt.Sprintf("show logs for portal %s in space %s", portal.ID, opts.SpaceID)}
	switch portal.Driver {
	case DriverDocker:
		args := []string{"logs", "--tail", fmt.Sprintf("%d", opts.Tail)}
		if opts.Follow {
			args = append(args, "--follow")
		}
		args = append(args, portal.Runtime.ContainerName)
		plan.Commands = append(plan.Commands, command(portal.Runtime.Engine, args...))
	default:
		session := fmt.Sprintf("stave-%s-%s-%s", opts.SpaceID, portal.ID, firstString(opts.Agent, "agent"))
		plan.Commands = append(plan.Commands, sshCommand(portal, "tmux capture-pane -pt "+quoteRemote(session)+" -S -"+fmt.Sprintf("%d", opts.Tail)))
	}
	return plan, nil
}

func (s Service) PlanDown(ctx context.Context, opts DownOptions) (Plan, error) {
	_ = ctx
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	if isRemoteDriver(portal.Driver) {
		return Plan{}, fmt.Errorf("%s portals are attach-only; use portal detach", portal.Driver)
	}
	plan := Plan{Operation: "down", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("stop portal %s for space %s", portal.ID, opts.SpaceID)}
	switch portal.Driver {
	case DriverDocker:
		args := []string{"stop"}
		if opts.Timeout > 0 {
			args = append(args, "--time", fmt.Sprintf("%d", opts.Timeout))
		}
		args = append(args, portal.Runtime.ContainerName)
		plan.Commands = append(plan.Commands, command(portal.Runtime.Engine, args...))
	case DriverDevcontainer:
		plan.Commands = append(plan.Commands, devcontainerDockerContainerCommand(portal, "stop"))
	}
	return plan, nil
}

func (s Service) Detach(opts DetachOptions) (Plan, error) {
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	portal, ok := manifest.Portals[portalID]
	if !ok {
		return Plan{}, fmt.Errorf("portal %q is not registered for space %q", portalID, opts.SpaceID)
	}
	if !isRemoteDriver(portal.Driver) {
		return Plan{}, fmt.Errorf("detach is for attach-only portals; use destroy for Stave-owned local runtimes")
	}
	plan := Plan{Operation: "detach", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("remove portal metadata %s for space %s", portalID, opts.SpaceID)}
	delete(manifest.Portals, portalID)
	if err := setManifestPreview(&plan, spacePath, manifest); err != nil {
		return Plan{}, err
	}
	if opts.DryRun {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "manifest", Severity: SeverityInfo, Code: "manifest.preview", Message: "dry-run only; manifest was not written", Evidence: filepath.Join(spacePath, ManifestName)})
		return plan, nil
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s Service) PlanDestroy(ctx context.Context, opts DestroyOptions) (Plan, error) {
	_ = ctx
	portal, _, err := s.LoadPortal(SelectOptions{SpaceID: opts.SpaceID, PortalID: opts.PortalID})
	if err != nil {
		return Plan{}, err
	}
	if isRemoteDriver(portal.Driver) {
		return Plan{}, fmt.Errorf("%s portals are attach-only; use portal detach", portal.Driver)
	}
	if !portal.Ownership.CreatedContainer {
		return Plan{}, fmt.Errorf("portal %s has no recorded Stave-owned container", portal.ID)
	}
	plan := Plan{Operation: "destroy", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("destroy Stave-owned resources for portal %s", portal.ID)}
	switch portal.Driver {
	case DriverDocker:
		plan.Commands = append(plan.Commands, command(portal.Runtime.Engine, "rm", "-f", portal.Runtime.ContainerName))
		if opts.DeleteVolumes {
			for _, volume := range portal.Ownership.CreatedVolumes {
				plan.Commands = append(plan.Commands, command(portal.Runtime.Engine, "volume", "rm", volume))
			}
		}
	case DriverDevcontainer:
		plan.Commands = append(plan.Commands, devcontainerDockerContainerCommand(portal, "rm -f"))
	}
	return plan, nil
}

func devcontainerDockerContainerCommand(portal Portal, action string) Command {
	filters := []string{"label=devcontainer.local_folder=" + portal.Workspace.LocalPath}
	if portal.Runtime.DevcontainerPath != "" {
		filters = append(filters, "label=devcontainer.config_file="+portal.Runtime.DevcontainerPath)
	}
	args := make([]string, 0, len(filters)*2)
	for _, filter := range filters {
		args = append(args, "--filter", quoteShell(filter))
	}
	psMode := "-aq"
	if action == "stop" {
		psMode = "-q"
	}
	script := "ids=$(docker ps " + psMode + " " + strings.Join(args, " ") + "); test -z \"$ids\" || docker " + action + " $ids"
	return command("sh", "-c", script)
}

func authLoginArgv(provider, method string) []string {
	switch provider {
	case "claude":
		return []string{"claude", "auth", "login"}
	case "cursor":
		return []string{"cursor-agent", "status"}
	default:
		args := []string{"codex", "login"}
		if method == "device" {
			args = append(args, "--device-auth")
		}
		return args
	}
}

func authLogoutArgv(provider string) []string {
	switch provider {
	case "claude":
		return []string{"claude", "auth", "logout"}
	case "cursor":
		return []string{"cursor-agent", "logout"}
	default:
		return []string{"codex", "logout"}
	}
}

func commandFromArgv(argv []string) Command {
	if len(argv) == 0 {
		return Command{}
	}
	return command(argv[0], argv[1:]...)
}

func validateProviderName(provider string) error {
	switch provider {
	case "codex", "claude", "cursor":
		return nil
	default:
		return fmt.Errorf("provider must be codex, claude, or cursor")
	}
}

func ValidateProviderName(provider string) error {
	return validateProviderName(provider)
}

func validateAuthLoginMethod(provider string, method AuthMode) error {
	if method == "" || method == AuthNative {
		return nil
	}
	if method == "device" && provider == "codex" {
		return nil
	}
	if method == "device" {
		return fmt.Errorf("device login method is only supported for codex")
	}
	return fmt.Errorf("auth login method must be native or device")
}

func validateAuthInheritMethod(method AuthMode) error {
	switch method {
	case AuthEnv, AuthVolume, AuthSSHForward:
		return nil
	case AuthCopyCache:
		return fmt.Errorf("copy-cache requires direct manual handling and is not automated")
	case "":
		return fmt.Errorf("auth inherit method is required")
	default:
		return fmt.Errorf("auth inherit method must be env, volume, or ssh-forward")
	}
}

func validateAuthTarget(target string) error {
	switch target {
	case "local", "portal", "all":
		return nil
	default:
		return fmt.Errorf("auth target must be local, portal, or all")
	}
}

func hasProvider(providers []AuthProvider, provider string) bool {
	for _, current := range providers {
		if current.Provider == provider {
			return true
		}
	}
	return false
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
