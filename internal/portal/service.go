package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"golang.org/x/term"
)

type Runner interface {
	LookPath(string) (string, error)
	Run(context.Context, Command) (RunResult, error)
}

type Launcher interface {
	Launch(context.Context, Command) error
}

type Git interface {
	IsDirty(context.Context, string) (bool, string, error)
}

type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Service struct {
	Config   config.Config
	Runner   Runner
	Launcher Launcher
	Git      Git
	Now      func() time.Time
	// IsTerminal reports whether stdio is a real terminal; overridable for
	// tests. TTYAuto only requests a TTY (docker -t / ssh -t) when it is,
	// so piped invocations no longer fail with "input device is not a TTY".
	IsTerminal func() bool
}

type InitContainerOptions struct {
	SpaceID       string
	PortalID      string
	Engine        Driver
	Image         string
	ContainerRoot string
	Preset        string
	DryRun        bool
}

type InitDevcontainerOptions struct {
	SpaceID          string
	PortalID         string
	DevcontainerPath string
	ComposeFiles     []string
	Service          string
	ContainerRoot    string
	Preset           string
	DryRun           bool
}

type AttachSSHOptions struct {
	SpaceID        string
	PortalID       string
	Host           string
	Port           int
	IdentityPath   string
	KnownHostsPath string
	StrictHostKey  string
	RemoteRoot     string
	SyncMode       SyncMode
	Preset         string
	DryRun         bool
}

type AttachEC2Options struct {
	SpaceID        string
	PortalID       string
	InstanceID     string
	Host           string
	Port           int
	Region         string
	Profile        string
	SSHUser        string
	IdentityPath   string
	KnownHostsPath string
	StrictHostKey  string
	RemoteRoot     string
	SyncMode       SyncMode
	Preset         string
	DryRun         bool
}

type SelectOptions struct {
	SpaceID  string
	PortalID string
}

func NewService(cfg config.Config, runner Runner, launcher Launcher) Service {
	if runner == nil {
		runner = localRunner{}
	}
	return Service{Config: cfg, Runner: runner, Launcher: launcher, Git: git.New()}
}

// withManifestLock serializes load-modify-save cycles on a space's portal
// manifest across concurrent stave processes. When the space directory does
// not exist yet, fn runs unlocked so it can surface its own load error.
func (s Service) withManifestLock(spaceID string, fn func() (Plan, error)) (Plan, error) {
	spacePath := s.SpacePath(spaceID)
	if _, err := os.Stat(spacePath); err != nil {
		return fn()
	}
	var plan Plan
	err := fsio.WithLock(filepath.Join(spacePath, ManifestName+".lock"), func() error {
		var innerErr error
		plan, innerErr = fn()
		return innerErr
	})
	return plan, err
}

func (s Service) InitContainer(ctx context.Context, opts InitContainerOptions) (Plan, error) {
	return s.withManifestLock(opts.SpaceID, func() (Plan, error) {
		return s.initContainerLocked(ctx, opts)
	})
}

func (s Service) initContainerLocked(ctx context.Context, opts InitContainerOptions) (Plan, error) {
	if opts.Engine == "" {
		opts.Engine = DriverDocker
	}
	if opts.Engine != DriverDocker {
		return Plan{}, fmt.Errorf("container engine must be docker")
	}
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	if _, ok := manifest.Portals[portalID]; ok {
		return Plan{}, fmt.Errorf("portal %q already exists", portalID)
	}
	portal := Portal{
		ID:        portalID,
		Driver:    opts.Engine,
		CreatedAt: s.now(),
		Workspace: Workspace{
			LocalPath:     spacePath,
			ContainerRoot: opts.ContainerRoot,
			SyncMode:      SyncMount,
		},
		Runtime: Runtime{
			Engine:        string(opts.Engine),
			Image:         opts.Image,
			ContainerName: defaultContainerName(opts.SpaceID, portalID),
		},
		Ownership: Ownership{CreatedContainer: true},
	}
	if err := applyPreset(opts.Preset, &portal); err != nil {
		return Plan{}, err
	}
	applyPortalDefaults(opts.SpaceID, &portal)
	if err := ValidatePortal(opts.SpaceID, portal); err != nil {
		return Plan{}, err
	}
	manifest.Portals[portalID] = portal
	plan := Plan{Operation: "init-container", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("record %s portal %s for space %s", opts.Engine, portalID, opts.SpaceID)}
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

func (s Service) InitDevcontainer(ctx context.Context, opts InitDevcontainerOptions) (Plan, error) {
	return s.withManifestLock(opts.SpaceID, func() (Plan, error) {
		return s.initDevcontainerLocked(ctx, opts)
	})
}

func (s Service) initDevcontainerLocked(ctx context.Context, opts InitDevcontainerOptions) (Plan, error) {
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	if _, ok := manifest.Portals[portalID]; ok {
		return Plan{}, fmt.Errorf("portal %q already exists", portalID)
	}
	portal := Portal{
		ID:        portalID,
		Driver:    DriverDevcontainer,
		CreatedAt: s.now(),
		Workspace: Workspace{
			LocalPath:     spacePath,
			ContainerRoot: opts.ContainerRoot,
			SyncMode:      SyncMount,
		},
		Runtime: Runtime{
			Engine:           string(DriverDevcontainer),
			DevcontainerPath: opts.DevcontainerPath,
			ComposeFiles:     append([]string(nil), opts.ComposeFiles...),
			Service:          opts.Service,
		},
		Ownership: Ownership{CreatedContainer: true},
	}
	if err := applyPreset(opts.Preset, &portal); err != nil {
		return Plan{}, err
	}
	applyPortalDefaults(opts.SpaceID, &portal)
	if err := ValidatePortal(opts.SpaceID, portal); err != nil {
		return Plan{}, err
	}
	manifest.Portals[portalID] = portal
	plan := Plan{Operation: "init-devcontainer", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("record devcontainer portal %s for space %s", portalID, opts.SpaceID)}
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

func (s Service) AttachSSH(ctx context.Context, opts AttachSSHOptions) (Plan, error) {
	return s.withManifestLock(opts.SpaceID, func() (Plan, error) {
		return s.attachSSHLocked(ctx, opts)
	})
}

func (s Service) attachSSHLocked(ctx context.Context, opts AttachSSHOptions) (Plan, error) {
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	if _, ok := manifest.Portals[portalID]; ok {
		return Plan{}, fmt.Errorf("portal %q already exists", portalID)
	}
	portal := Portal{
		ID:        portalID,
		Driver:    DriverSSH,
		CreatedAt: s.now(),
		Workspace: Workspace{
			LocalPath:  spacePath,
			RemoteRoot: opts.RemoteRoot,
			SyncMode:   firstSyncMode(opts.SyncMode, SyncRsync),
		},
		Target: Target{Host: opts.Host, Port: opts.Port, IdentityPath: opts.IdentityPath, KnownHostsPath: opts.KnownHostsPath, StrictHostKey: opts.StrictHostKey},
	}
	if err := applyPreset(opts.Preset, &portal); err != nil {
		return Plan{}, err
	}
	portal.Driver = DriverSSH
	if opts.SyncMode != "" {
		portal.Workspace.SyncMode = opts.SyncMode
	}
	applyPortalDefaults(opts.SpaceID, &portal)
	if err := ValidatePortal(opts.SpaceID, portal); err != nil {
		return Plan{}, err
	}
	manifest.Portals[portalID] = portal
	plan := Plan{Operation: "attach-ssh", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("record ssh portal %s for space %s", portalID, opts.SpaceID)}
	if err := setManifestPreview(&plan, spacePath, manifest); err != nil {
		return Plan{}, err
	}
	plan.Commands = append(plan.Commands, remotePrepareCommands(portal, opts.SyncMode != "")...)
	if opts.DryRun {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "manifest", Severity: SeverityInfo, Code: "manifest.preview", Message: "dry-run only; manifest was not written", Evidence: filepath.Join(spacePath, ManifestName)})
		return plan, nil
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s Service) AttachEC2(ctx context.Context, opts AttachEC2Options) (Plan, error) {
	return s.withManifestLock(opts.SpaceID, func() (Plan, error) {
		return s.attachEC2Locked(ctx, opts)
	})
}

func (s Service) attachEC2Locked(ctx context.Context, opts AttachEC2Options) (Plan, error) {
	manifest, spacePath, err := s.loadOrCreateManifest(opts.SpaceID)
	if err != nil {
		return Plan{}, err
	}
	portalID := normalizePortalID(opts.PortalID)
	if _, ok := manifest.Portals[portalID]; ok {
		return Plan{}, fmt.Errorf("portal %q already exists", portalID)
	}
	portal := Portal{
		ID:        portalID,
		Driver:    DriverEC2Attach,
		CreatedAt: s.now(),
		Workspace: Workspace{
			LocalPath:  spacePath,
			RemoteRoot: opts.RemoteRoot,
			SyncMode:   firstSyncMode(opts.SyncMode, SyncRsync),
		},
		Target: Target{Host: opts.Host, Port: opts.Port, InstanceID: opts.InstanceID, Region: opts.Region, Profile: opts.Profile, SSHUser: opts.SSHUser, IdentityPath: opts.IdentityPath, KnownHostsPath: opts.KnownHostsPath, StrictHostKey: opts.StrictHostKey},
	}
	if err := applyPreset(opts.Preset, &portal); err != nil {
		return Plan{}, err
	}
	portal.Driver = DriverEC2Attach
	if opts.SyncMode != "" {
		portal.Workspace.SyncMode = opts.SyncMode
	}
	applyPortalDefaults(opts.SpaceID, &portal)
	if err := ValidatePortal(opts.SpaceID, portal); err != nil {
		return Plan{}, err
	}
	manifest.Portals[portalID] = portal
	plan := Plan{Operation: "attach-ec2", DryRun: opts.DryRun, Mutates: true, Summary: fmt.Sprintf("record ec2 attach portal %s for space %s", portalID, opts.SpaceID)}
	if portal.Target.Host == "" && !opts.DryRun {
		resolved, err := s.resolveEC2Host(ctx, portal)
		if err != nil {
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "ec2", Severity: SeverityWarn, Code: "ec2.host_unresolved", Message: "EC2 SSH host could not be resolved from instance metadata", Evidence: err.Error(), NextAction: "pass --host with a reachable DNS name or IP address"})
		} else {
			// Used for this plan's prepare commands only; the manifest keeps
			// an empty host so future commands re-resolve a fresh address
			// (public DNS/IP changes across instance stop/start).
			portal.Target.Host = resolved
		}
	}
	if err := setManifestPreview(&plan, spacePath, manifest); err != nil {
		return Plan{}, err
	}
	plan.Commands = append(plan.Commands, remotePrepareCommands(portal, opts.SyncMode != "")...)
	if opts.DryRun {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Component: "manifest", Severity: SeverityInfo, Code: "manifest.preview", Message: "dry-run only; manifest was not written", Evidence: filepath.Join(spacePath, ManifestName)})
		return plan, nil
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s Service) LoadPortal(opts SelectOptions) (Portal, string, error) {
	if err := config.ValidateName("space id", opts.SpaceID); err != nil {
		return Portal{}, "", err
	}
	spacePath := s.SpacePath(opts.SpaceID)
	if _, err := os.Stat(spacePath); errors.Is(err, os.ErrNotExist) {
		return Portal{}, "", fmt.Errorf("space %q does not exist; create it with stave space create %s", opts.SpaceID, opts.SpaceID)
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return Portal{}, "", err
	}
	if manifest.SpaceID != opts.SpaceID {
		return Portal{}, "", fmt.Errorf("portal manifest space id %q does not match %q", manifest.SpaceID, opts.SpaceID)
	}
	portalID := normalizePortalID(opts.PortalID)
	portal, ok := manifest.Portals[portalID]
	if !ok {
		return Portal{}, "", fmt.Errorf("portal %q is not registered for space %q", portalID, opts.SpaceID)
	}
	return portal, spacePath, nil
}

func (s Service) LoadPortalForRuntime(ctx context.Context, opts SelectOptions) (Portal, string, error) {
	portal, spacePath, err := s.LoadPortal(opts)
	if err != nil {
		return Portal{}, "", err
	}
	if portal.Driver != DriverEC2Attach || portal.Target.Host != "" {
		return portal, spacePath, nil
	}
	host, err := s.resolveEC2Host(ctx, portal)
	if err != nil {
		return Portal{}, "", fmt.Errorf("ec2 ssh host is not recorded and could not be resolved: %w; pass --host on attach or configure", err)
	}
	// The resolved address is ephemeral runtime data: EC2 public DNS/IP
	// changes across stop/start cycles, so it is used for this invocation
	// only and never persisted back into the manifest.
	portal.Target.Host = host
	return portal, spacePath, nil
}

// UnresolvedEC2Host is substituted into dry-run command previews when the
// EC2 SSH host cannot be resolved (e.g. the aws CLI is unavailable); dry-run
// must never fail on missing runtime tooling.
const UnresolvedEC2Host = "UNRESOLVED-EC2-HOST"

func (s Service) loadPortalForPlan(ctx context.Context, opts SelectOptions, dryRun bool) (Portal, string, error) {
	if !dryRun {
		return s.LoadPortalForRuntime(ctx, opts)
	}
	portal, spacePath, err := s.LoadPortal(opts)
	if err != nil {
		return Portal{}, "", err
	}
	if portal.Driver == DriverEC2Attach && portal.Target.Host == "" {
		if host, err := s.resolveEC2Host(ctx, portal); err == nil {
			portal.Target.Host = host
		} else {
			portal.Target.Host = UnresolvedEC2Host
		}
	}
	return portal, spacePath, nil
}

func (s Service) List(spaceID string) ([]ListEntry, error) {
	if spaceID != "" {
		portalManifest, err := LoadManifest(s.SpacePath(spaceID))
		if err != nil {
			return nil, err
		}
		return listEntries(portalManifest), nil
	}
	entries, err := os.ReadDir(s.Config.AgentWorkDir)
	if err != nil {
		return nil, err
	}
	var all []ListEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(s.Config.AgentWorkDir, entry.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		all = append(all, listEntries(manifest)...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].SpaceID == all[j].SpaceID {
			return all[i].PortalID < all[j].PortalID
		}
		return all[i].SpaceID < all[j].SpaceID
	})
	return all, nil
}

func (s Service) Status(ctx context.Context, opts SelectOptions) (Status, error) {
	portal, _, err := s.LoadPortal(opts)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		SpaceID:  opts.SpaceID,
		PortalID: portal.ID,
		Driver:   portal.Driver,
		Overall:  OverallUnknown,
		State:    "not-queried",
		Health:   "unknown",
		Auth:     append([]AuthProvider(nil), portal.Auth.Providers...),
	}
	if err := s.normalizeRuntimeStatus(ctx, portal, &status); err != nil {
		status.Overall = OverallError
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityError, Code: "runtime.status_error", Message: "runtime status could not be queried", Evidence: err.Error()})
	}
	if authMissing(status.Auth) {
		if status.Overall != OverallError {
			status.Overall = OverallWarn
		}
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "auth", Severity: SeverityWarn, Code: "auth.missing", Message: "one or more portal auth providers are missing", NextAction: "run portal auth login for the missing provider"})
	}
	return status, nil
}

// probeTimeout bounds read-only status probes (ssh true, docker inspect,
// aws describe-instances) so an unreachable target degrades to a warning
// instead of hanging the command indefinitely.
const probeTimeout = 30 * time.Second

func (s Service) normalizeRuntimeStatus(ctx context.Context, portal Portal, status *Status) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if s.Runner == nil {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityInfo, Code: "runtime.no_runner", Message: "runtime status was not queried because no runner is configured"})
		return nil
	}
	for _, binary := range statusBinaries(portal) {
		if _, err := s.Runner.LookPath(binary); err != nil {
			status.Overall = OverallWarn
			status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "driver", Severity: SeverityWarn, Code: "driver.binary_missing", Message: fmt.Sprintf("%s was not found in PATH", binary), NextAction: fmt.Sprintf("install %s or choose another portal driver", binary)})
			return nil
		}
	}
	switch portal.Driver {
	case DriverDocker:
		return s.normalizeContainerStatus(ctx, portal, status)
	case DriverDevcontainer:
		return s.normalizeCommandStatus(ctx, status, devcontainerExecTrue(portal), "devcontainer")
	case DriverSSH:
		return s.normalizeCommandStatus(ctx, status, sshCommand(portal, "true"), "ssh")
	case DriverEC2Attach:
		return s.normalizeEC2Status(ctx, portal, status)
	default:
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityInfo, Code: "runtime.not_supported", Message: "runtime status is not implemented for this driver"})
		return nil
	}
}

func (s Service) normalizeContainerStatus(ctx context.Context, portal Portal, status *Status) error {
	result, err := s.Runner.Run(ctx, command(portal.Runtime.Engine, "container", "inspect", portal.Runtime.ContainerName))
	if err != nil {
		status.Overall = OverallWarn
		status.State = "missing"
		status.Health = "unknown"
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityWarn, Code: "runtime.container_inspect_failed", Message: "container inspect did not find a running portal container", Evidence: firstString(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout), err.Error())})
		return nil
	}
	state, health := parseContainerInspect(result.Stdout)
	status.State = firstString(state, "unknown")
	status.Health = firstString(health, "unknown")
	if status.State == "running" && (status.Health == "healthy" || status.Health == "ok" || status.Health == "not-configured" || status.Health == "unknown") {
		status.Overall = OverallOK
		return nil
	}
	status.Overall = OverallWarn
	status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityWarn, Code: "runtime.container_not_ready", Message: "container exists but is not ready", Evidence: fmt.Sprintf("state=%s health=%s", status.State, status.Health), NextAction: fmt.Sprintf("run stave portal up %s %s to start it", status.SpaceID, status.PortalID)})
	return nil
}

func (s Service) normalizeCommandStatus(ctx context.Context, status *Status, cmd Command, component string) error {
	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		status.Overall = OverallWarn
		status.State = "unreachable"
		status.Health = "unknown"
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: component, Severity: SeverityWarn, Code: component + ".unreachable", Message: "portal target did not respond to a read-only status probe", Evidence: firstString(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout), err.Error())})
		return nil
	}
	status.Overall = OverallOK
	status.State = "reachable"
	status.Health = "ok"
	return nil
}

func (s Service) normalizeEC2Status(ctx context.Context, portal Portal, status *Status) error {
	result, err := s.Runner.Run(ctx, ec2DescribeCommand(portal))
	if err != nil {
		status.Overall = OverallWarn
		status.State = "unreachable"
		status.Health = "unknown"
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "ec2", Severity: SeverityWarn, Code: "ec2.describe_failed", Message: "EC2 instance metadata could not be read", Evidence: firstString(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout), err.Error())})
		return nil
	}
	state := parseEC2State(result.Stdout)
	status.State = firstString(state, "reachable")
	status.Health = "ok"
	if status.State == "running" || status.State == "reachable" {
		status.Overall = OverallOK
		return nil
	}
	status.Overall = OverallWarn
	status.Diagnostics = append(status.Diagnostics, Diagnostic{Component: "ec2", Severity: SeverityWarn, Code: "ec2.not_running", Message: "EC2 instance is not running", Evidence: status.State})
	return nil
}

func (s Service) Doctor(ctx context.Context, opts SelectOptions) (DoctorReport, error) {
	portal, _, err := s.LoadPortal(opts)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{SpaceID: opts.SpaceID, PortalID: portal.ID, Overall: OverallOK}
	if _, err := space.LoadManifest(s.SpacePath(opts.SpaceID)); err != nil {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Component: "space", Severity: SeverityError, Code: "space.manifest_missing", Message: "space manifest could not be loaded", Evidence: err.Error()})
	}
	for _, binary := range requiredBinaries(portal) {
		if _, err := s.Runner.LookPath(binary); err != nil {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Component: "driver", Severity: SeverityWarn, Code: "driver.binary_missing", Message: fmt.Sprintf("%s was not found in PATH", binary), NextAction: fmt.Sprintf("install %s or choose another portal driver", binary)})
		}
	}
	// Probe the runtime the same way status does so doctor cannot report ok
	// while the daemon/host is unreachable.
	runtimeStatus := Status{Overall: OverallUnknown, State: "not-queried", Health: "unknown"}
	if err := s.normalizeRuntimeStatus(ctx, portal, &runtimeStatus); err != nil {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Component: "runtime", Severity: SeverityError, Code: "runtime.status_error", Message: "runtime status could not be queried", Evidence: err.Error()})
	} else {
		report.Diagnostics = append(report.Diagnostics, runtimeStatus.Diagnostics...)
	}
	if portal.Auth.Mode == AuthCopyCache {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Component: "auth", Severity: SeverityError, Code: "auth.copy_cache_default", Message: "copy-cache cannot be the persisted default auth posture"})
	}
	report.Overall = overallFromDiagnostics(report.Diagnostics)
	return report, nil
}

func (s Service) Inspect(ctx context.Context, opts SelectOptions) (InspectReport, error) {
	portal, spacePath, err := s.LoadPortal(opts)
	if err != nil {
		return InspectReport{}, err
	}
	report := InspectReport{ManifestPath: filepath.Join(spacePath, ManifestName), Portal: portal, OwnedResources: []string{}}
	if portal.Ownership.CreatedContainer && portal.Runtime.ContainerName != "" {
		report.OwnedResources = append(report.OwnedResources, "container:"+portal.Runtime.ContainerName)
		report.DestroyDryRunNotes = append(report.DestroyDryRunNotes, fmt.Sprintf("stop and remove Stave-owned container %s", portal.Runtime.ContainerName))
	}
	for _, volume := range portal.Ownership.CreatedVolumes {
		report.OwnedResources = append(report.OwnedResources, "volume:"+volume)
		report.DestroyDryRunNotes = append(report.DestroyDryRunNotes, "remove Stave-owned volume "+volume)
	}
	for _, network := range portal.Ownership.CreatedNetworks {
		report.OwnedResources = append(report.OwnedResources, "network:"+network)
		report.DestroyDryRunNotes = append(report.DestroyDryRunNotes, "remove Stave-owned network "+network)
	}
	report.SyncScope = append(report.SyncScope, "local:"+portal.Workspace.LocalPath)
	if portal.Workspace.RemoteRoot != "" {
		report.SyncScope = append(report.SyncScope, "remote:"+portal.Workspace.RemoteRoot)
	}
	if portal.Workspace.ContainerRoot != "" {
		report.SyncScope = append(report.SyncScope, "container:"+portal.Workspace.ContainerRoot)
	}
	if isRemoteDriver(portal.Driver) {
		report.DestroyDryRunNotes = append(report.DestroyDryRunNotes, "remote host or EC2 instance is attach-only and will not be stopped, terminated, or broadly deleted")
	}
	if len(report.DestroyDryRunNotes) == 0 {
		report.DestroyDryRunNotes = append(report.DestroyDryRunNotes, "no Stave-owned runtime resources are recorded")
	}
	return report, nil
}

func (s Service) Drivers() []DriverInfo {
	return []DriverInfo{
		{Driver: DriverDocker, CreateCapable: true, Binary: "docker", Description: "local Docker container around the Stave space"},
		{Driver: DriverDevcontainer, CreateCapable: true, Binary: "devcontainer", Description: "Dev Containers CLI workspace"},
		{Driver: DriverSSH, AttachOnly: true, Binary: "ssh", Description: "existing SSH host"},
		{Driver: DriverEC2Attach, AttachOnly: true, Binary: "aws", Description: "existing EC2 instance metadata plus SSH"},
	}
}

func (s Service) SpacePath(id string) string {
	return filepath.Join(s.Config.AgentWorkDir, id)
}

func setManifestPreview(plan *Plan, spacePath string, manifest Manifest) error {
	data, err := MarshalManifest(spacePath, manifest)
	if err != nil {
		return err
	}
	plan.ManifestPreview = string(data)
	return nil
}

func (s Service) loadOrCreateManifest(spaceID string) (Manifest, string, error) {
	if err := config.ValidateName("space id", spaceID); err != nil {
		return Manifest{}, "", err
	}
	spacePath := s.SpacePath(spaceID)
	if _, err := os.Stat(spacePath); errors.Is(err, os.ErrNotExist) {
		return Manifest{}, "", fmt.Errorf("space %q does not exist; create it with stave space create %s", spaceID, spaceID)
	}
	spaceManifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return Manifest{}, "", err
	}
	if spaceManifest.ID != spaceID {
		return Manifest{}, "", fmt.Errorf("space manifest id %q does not match %q", spaceManifest.ID, spaceID)
	}
	manifest, err := LoadManifest(spacePath)
	if err == nil {
		if manifest.SpaceID != spaceID {
			return Manifest{}, "", fmt.Errorf("portal manifest space id %q does not match %q", manifest.SpaceID, spaceID)
		}
		return manifest, spacePath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, "", err
	}
	return Manifest{Version: 1, SpaceID: spaceID, Portals: map[string]Portal{}}, spacePath, nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) stdioIsTerminal() bool {
	if s.IsTerminal != nil {
		return s.IsTerminal()
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// resolveTTY pins TTYAuto to a concrete mode based on the actual stdio: a
// TTY is only requested when one is really attached.
func (s Service) resolveTTY(tty TTYMode) TTYMode {
	if tty == TTYAuto && !s.stdioIsTerminal() {
		return TTYNever
	}
	return tty
}

type localRunner struct{}

func (localRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (localRunner) Run(ctx context.Context, cmd Command) (RunResult, error) {
	execCmd := exec.CommandContext(ctx, cmd.Program, cmd.Args...)
	execCmd.Dir = cmd.Dir
	execCmd.Env = append(os.Environ(), cmd.Env...)
	if cmd.Interactive || cmd.Stream {
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		err := execCmd.Run()
		result := RunResult{}
		if err == nil {
			return result, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	err := execCmd.Run()
	result := RunResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}

func normalizePortalID(id string) string {
	if strings.TrimSpace(id) == "" {
		return DefaultPortalID
	}
	return strings.TrimSpace(id)
}

func firstSyncMode(value, fallback SyncMode) SyncMode {
	if value != "" {
		return value
	}
	return fallback
}

func listEntries(manifest Manifest) []ListEntry {
	entries := make([]ListEntry, 0, len(manifest.Portals))
	for _, portal := range manifest.Portals {
		entries = append(entries, ListEntry{SpaceID: manifest.SpaceID, PortalID: portal.ID, Driver: portal.Driver, SyncMode: portal.Workspace.SyncMode, Auth: summarizeAuth(portal.Auth.Providers)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PortalID < entries[j].PortalID })
	return entries
}

func summarizeAuth(providers []AuthProvider) string {
	if len(providers) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, len(providers))
	for _, provider := range providers {
		parts = append(parts, provider.Provider+":"+string(provider.Status))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func authMissing(providers []AuthProvider) bool {
	for _, provider := range providers {
		if provider.Status == AuthMissing || provider.Status == AuthError {
			return true
		}
	}
	return false
}

func overallFromDiagnostics(diagnostics []Diagnostic) Overall {
	overall := OverallOK
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return OverallError
		}
		if diagnostic.Severity == SeverityWarn {
			overall = OverallWarn
		}
	}
	return overall
}

func requiredBinaries(portal Portal) []string {
	switch portal.Driver {
	case DriverDocker:
		return []string{"docker"}
	case DriverDevcontainer:
		return []string{"devcontainer"}
	case DriverSSH:
		return []string{"ssh", "rsync"}
	case DriverEC2Attach:
		return []string{"aws", "ssh", "rsync"}
	default:
		return nil
	}
}

func statusBinaries(portal Portal) []string {
	switch portal.Driver {
	case DriverDocker:
		return []string{"docker"}
	case DriverDevcontainer:
		return []string{"devcontainer"}
	case DriverSSH:
		return []string{"ssh"}
	case DriverEC2Attach:
		return []string{"aws"}
	default:
		return nil
	}
}

func devcontainerExecTrue(portal Portal) Command {
	args := []string{"exec", "--workspace-folder", portal.Workspace.LocalPath}
	if portal.Runtime.DevcontainerPath != "" {
		args = append(args, "--config", portal.Runtime.DevcontainerPath)
	}
	args = append(args, "true")
	return command("devcontainer", args...)
}

func parseContainerInspect(output string) (state string, health string) {
	var inspect []struct {
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(output), &inspect); err != nil || len(inspect) == 0 {
		return "", ""
	}
	state = strings.ToLower(strings.TrimSpace(inspect[0].State.Status))
	if state == "" && inspect[0].State.Running {
		state = "running"
	}
	if inspect[0].State.Health == nil {
		health = "not-configured"
	} else {
		health = strings.ToLower(strings.TrimSpace(inspect[0].State.Health.Status))
	}
	return state, health
}

func parseEC2State(output string) string {
	var payload struct {
		Reservations []struct {
			Instances []struct {
				State struct {
					Name string `json:"Name"`
				} `json:"State"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return ""
	}
	for _, reservation := range payload.Reservations {
		for _, instance := range reservation.Instances {
			if state := strings.ToLower(strings.TrimSpace(instance.State.Name)); state != "" {
				return state
			}
		}
	}
	return ""
}

func (s Service) resolveEC2Host(ctx context.Context, portal Portal) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, err := s.Runner.Run(ctx, ec2DescribeCommand(portal))
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(firstString(result.Stderr, result.Stdout)))
	}
	host := parseEC2SSHHost(result.Stdout)
	if host == "" {
		return "", fmt.Errorf("describe-instances did not include PublicDnsName, PublicIpAddress, or PrivateIpAddress")
	}
	return host, nil
}

func parseEC2SSHHost(output string) string {
	var payload struct {
		Reservations []struct {
			Instances []struct {
				PublicDNSName    string `json:"PublicDnsName"`
				PublicIPAddress  string `json:"PublicIpAddress"`
				PrivateIPAddress string `json:"PrivateIpAddress"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return ""
	}
	for _, reservation := range payload.Reservations {
		for _, instance := range reservation.Instances {
			for _, candidate := range []string{instance.PublicDNSName, instance.PublicIPAddress, instance.PrivateIPAddress} {
				if candidate = strings.TrimSpace(candidate); candidate != "" {
					return candidate
				}
			}
		}
	}
	return ""
}
