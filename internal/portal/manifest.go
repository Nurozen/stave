package portal

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"gopkg.in/yaml.v3"
)

const (
	ManifestName    = ".stave-portal.yaml"
	DefaultPortalID = "default"
	DefaultImage    = "ghcr.io/nurozen/stave-dev:latest"
	DefaultService  = "workspace"
	DefaultShell    = "sh"
)

type Driver string

const (
	DriverDocker       Driver = "docker"
	DriverDevcontainer Driver = "devcontainer"
	DriverSSH          Driver = "ssh"
	DriverEC2Attach    Driver = "ec2-attach"
)

type SyncMode string

const (
	SyncMount       SyncMode = "mount"
	SyncRsync       SyncMode = "rsync"
	SyncReconstruct SyncMode = "reconstruct"
)

type AuthMode string

const (
	AuthNative      AuthMode = "native"
	AuthEnv         AuthMode = "env"
	AuthVolume      AuthMode = "volume"
	AuthSSHForward  AuthMode = "ssh-forward"
	AuthCopyCache   AuthMode = "copy-cache"
	AuthRemoteLogin AuthMode = "remote-login"
)

type AuthStatus string

const (
	AuthUnknown AuthStatus = "unknown"
	AuthOK      AuthStatus = "ok"
	AuthMissing AuthStatus = "missing"
	AuthError   AuthStatus = "error"
)

type Manifest struct {
	Version int               `yaml:"version" json:"version"`
	SpaceID string            `yaml:"spaceID" json:"space_id"`
	Portals map[string]Portal `yaml:"portals" json:"portals"`
}

type Portal struct {
	ID        string    `yaml:"id" json:"id"`
	Driver    Driver    `yaml:"driver" json:"driver"`
	CreatedAt time.Time `yaml:"createdAt" json:"created_at"`
	Workspace Workspace `yaml:"workspace" json:"workspace"`
	Target    Target    `yaml:"target" json:"target"`
	Runtime   Runtime   `yaml:"runtime" json:"runtime"`
	Auth      Auth      `yaml:"auth" json:"auth"`
	Ownership Ownership `yaml:"ownership" json:"ownership"`
}

type Workspace struct {
	LocalPath     string   `yaml:"localPath" json:"local_path"`
	RemoteRoot    string   `yaml:"remoteRoot,omitempty" json:"remote_root,omitempty"`
	ContainerRoot string   `yaml:"containerRoot,omitempty" json:"container_root,omitempty"`
	SyncMode      SyncMode `yaml:"syncMode" json:"sync_mode"`
}

type Target struct {
	Host           string `yaml:"host,omitempty" json:"host,omitempty"`
	Port           int    `yaml:"port,omitempty" json:"port,omitempty"`
	InstanceID     string `yaml:"instanceID,omitempty" json:"instance_id,omitempty"`
	Region         string `yaml:"region,omitempty" json:"region,omitempty"`
	Profile        string `yaml:"profile,omitempty" json:"profile,omitempty"`
	SSHUser        string `yaml:"sshUser,omitempty" json:"ssh_user,omitempty"`
	IdentityPath   string `yaml:"identityPath,omitempty" json:"identity_path,omitempty"`
	KnownHostsPath string `yaml:"knownHostsPath,omitempty" json:"known_hosts_path,omitempty"`
	StrictHostKey  string `yaml:"strictHostKey,omitempty" json:"strict_host_key,omitempty"`
	DockerContext  string `yaml:"dockerContext,omitempty" json:"docker_context,omitempty"`
}

type Runtime struct {
	Engine           string            `yaml:"engine,omitempty" json:"engine,omitempty"`
	Image            string            `yaml:"image,omitempty" json:"image,omitempty"`
	ContainerName    string            `yaml:"containerName,omitempty" json:"container_name,omitempty"`
	ProjectName      string            `yaml:"projectName,omitempty" json:"project_name,omitempty"`
	Service          string            `yaml:"service,omitempty" json:"service,omitempty"`
	DevcontainerPath string            `yaml:"devcontainerPath,omitempty" json:"devcontainer_path,omitempty"`
	ComposeFiles     []string          `yaml:"composeFiles,omitempty" json:"compose_files,omitempty"`
	Labels           map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

type Auth struct {
	Mode      AuthMode       `yaml:"mode" json:"mode"`
	Providers []AuthProvider `yaml:"providers,omitempty" json:"providers,omitempty"`
}

type AuthProvider struct {
	Provider    string     `yaml:"provider" json:"provider"`
	Mode        AuthMode   `yaml:"mode" json:"mode"`
	Target      string     `yaml:"target" json:"target"`
	Status      AuthStatus `yaml:"status" json:"status"`
	SecretRef   string     `yaml:"secretRef,omitempty" json:"secret_ref,omitempty"`
	LastChecked string     `yaml:"lastChecked,omitempty" json:"last_checked,omitempty"`
	Warnings    []string   `yaml:"warnings,omitempty" json:"warnings,omitempty"`
}

type Ownership struct {
	CreatedContainer bool     `yaml:"createdContainer,omitempty" json:"created_container,omitempty"`
	CreatedVolumes   []string `yaml:"createdVolumes,omitempty" json:"created_volumes,omitempty"`
	CreatedNetworks  []string `yaml:"createdNetworks,omitempty" json:"created_networks,omitempty"`
}

func LoadManifest(spacePath string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := manifest.ApplyDefaults(spacePath); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func SaveManifest(spacePath string, manifest Manifest) error {
	data, err := MarshalManifest(spacePath, manifest)
	if err != nil {
		return err
	}
	return fsio.WriteFileAtomic(filepath.Join(spacePath, ManifestName), data, 0o644)
}

func MarshalManifest(spacePath string, manifest Manifest) ([]byte, error) {
	if err := manifest.ApplyDefaults(spacePath); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(manifest)
}

func (m *Manifest) ApplyDefaults(spacePath string) error {
	if m.Version == 0 {
		m.Version = 1
	}
	if m.Portals == nil {
		m.Portals = map[string]Portal{}
	}
	for id, portal := range m.Portals {
		if portal.ID == "" {
			portal.ID = id
		}
		if portal.Workspace.LocalPath == "" {
			local, err := filepath.Abs(spacePath)
			if err != nil {
				return err
			}
			portal.Workspace.LocalPath = local
		}
		applyPortalDefaults(m.SpaceID, &portal)
		m.Portals[id] = portal
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("portal manifest version must be 1")
	}
	if err := config.ValidateName("space id", m.SpaceID); err != nil {
		return err
	}
	for id, portal := range m.Portals {
		if id != portal.ID {
			return fmt.Errorf("portal map key %q does not match portal id %q", id, portal.ID)
		}
		if err := ValidatePortal(m.SpaceID, portal); err != nil {
			return fmt.Errorf("portal %q: %w", id, err)
		}
	}
	return nil
}

func ValidatePortal(spaceID string, portal Portal) error {
	if err := config.ValidateName("portal id", portal.ID); err != nil {
		return err
	}
	if !slices.Contains(SupportedDrivers(), portal.Driver) {
		return fmt.Errorf("driver %q is not supported", portal.Driver)
	}
	if !slices.Contains(SupportedSyncModes(), portal.Workspace.SyncMode) {
		return fmt.Errorf("sync mode %q is not supported", portal.Workspace.SyncMode)
	}
	if err := validatePath("local path", portal.Workspace.LocalPath, true); err != nil {
		return err
	}
	if err := validatePath("container root", portal.Workspace.ContainerRoot, false); err != nil {
		return err
	}
	if err := validatePath("remote root", portal.Workspace.RemoteRoot, false); err != nil {
		return err
	}
	if portal.Target.Port < 0 || portal.Target.Port > 65535 {
		return fmt.Errorf("target port %d is outside 0-65535", portal.Target.Port)
	}
	if err := validatePath("identity path", portal.Target.IdentityPath, true); err != nil {
		return err
	}
	if err := validatePath("known hosts path", portal.Target.KnownHostsPath, true); err != nil {
		return err
	}
	if err := validateStrictHostKey(portal.Target.StrictHostKey); err != nil {
		return err
	}
	switch portal.Driver {
	case DriverDocker:
		if portal.Runtime.ContainerName == "" {
			return fmt.Errorf("container name is required")
		}
		if portal.Workspace.ContainerRoot == "" {
			return fmt.Errorf("container root is required")
		}
		if portal.Workspace.SyncMode != SyncMount {
			return fmt.Errorf("%s portals require mount sync", portal.Driver)
		}
	case DriverDevcontainer:
		if portal.Workspace.ContainerRoot == "" {
			return fmt.Errorf("container root is required")
		}
	case DriverSSH:
		if strings.TrimSpace(portal.Target.Host) == "" {
			return fmt.Errorf("ssh host is required")
		}
		if portal.Workspace.RemoteRoot == "" {
			return fmt.Errorf("remote root is required")
		}
		if portal.Workspace.SyncMode == SyncMount {
			return fmt.Errorf("ssh portals cannot use mount sync")
		}
	case DriverEC2Attach:
		if strings.TrimSpace(portal.Target.InstanceID) == "" {
			return fmt.Errorf("instance id is required")
		}
		if portal.Workspace.RemoteRoot == "" {
			return fmt.Errorf("remote root is required")
		}
		if portal.Workspace.SyncMode == SyncMount {
			return fmt.Errorf("ec2 attach portals cannot use mount sync")
		}
	}
	if err := validateLabels(spaceID, portal); err != nil {
		return err
	}
	if portal.Auth.Mode == AuthCopyCache {
		return fmt.Errorf("copy-cache auth cannot be stored as the default auth mode")
	}
	if err := validateAuthMode(portal.Auth.Mode); err != nil {
		return err
	}
	for _, provider := range portal.Auth.Providers {
		if provider.SecretRef != "" {
			return fmt.Errorf("auth provider %q stores secretRef; portal manifests must not store credential references", provider.Provider)
		}
		if provider.Mode == AuthCopyCache {
			return fmt.Errorf("auth provider %q uses copy-cache; this requires direct CLI intent and must not be persisted as default", provider.Provider)
		}
		if err := validateAuthMode(provider.Mode); err != nil {
			return fmt.Errorf("auth provider %q: %w", provider.Provider, err)
		}
	}
	return nil
}

func validateAuthMode(mode AuthMode) error {
	switch mode {
	case "", AuthNative, AuthEnv, AuthVolume, AuthSSHForward, AuthRemoteLogin:
		return nil
	default:
		return fmt.Errorf("auth mode %q is not supported; use native, env, volume, ssh-forward, or remote-login", mode)
	}
}

func validateStrictHostKey(value string) error {
	switch strings.TrimSpace(value) {
	case "", "yes", "no", "ask", "accept-new":
		return nil
	default:
		return fmt.Errorf("strict host key checking must be yes, no, ask, or accept-new")
	}
}

func SupportedDrivers() []Driver {
	return []Driver{DriverDocker, DriverDevcontainer, DriverSSH, DriverEC2Attach}
}

func SupportedSyncModes() []SyncMode {
	return []SyncMode{SyncMount, SyncRsync, SyncReconstruct}
}

func applyPortalDefaults(spaceID string, portal *Portal) {
	if portal.ID == "" {
		portal.ID = DefaultPortalID
	}
	if portal.Target.Port == 0 {
		portal.Target.Port = 22
	}
	if portal.Workspace.ContainerRoot == "" && isLocalDriver(portal.Driver) {
		portal.Workspace.ContainerRoot = defaultContainerRoot(spaceID)
	}
	if portal.Workspace.RemoteRoot == "" && isRemoteDriver(portal.Driver) {
		portal.Workspace.RemoteRoot = defaultRemoteRoot(spaceID)
	}
	if portal.Workspace.SyncMode == "" {
		if isRemoteDriver(portal.Driver) {
			portal.Workspace.SyncMode = SyncRsync
		} else {
			portal.Workspace.SyncMode = SyncMount
		}
	}
	if portal.Runtime.Engine == "" {
		portal.Runtime.Engine = string(portal.Driver)
	}
	if portal.Runtime.Image == "" && portal.Driver == DriverDocker {
		portal.Runtime.Image = DefaultImage
	}
	if portal.Runtime.ContainerName == "" && portal.Driver == DriverDocker {
		portal.Runtime.ContainerName = defaultContainerName(spaceID, portal.ID)
	}
	if portal.Runtime.ProjectName == "" {
		portal.Runtime.ProjectName = defaultProjectName(spaceID)
	}
	if portal.Runtime.Service == "" {
		portal.Runtime.Service = DefaultService
	}
	if portal.Runtime.Labels == nil {
		portal.Runtime.Labels = map[string]string{}
	}
	portal.Runtime.Labels["stave.space"] = spaceID
	portal.Runtime.Labels["stave.portal"] = portal.ID
	if portal.Auth.Mode == "" {
		portal.Auth.Mode = AuthNative
	}
	if len(portal.Auth.Providers) == 0 {
		portal.Auth.Providers = []AuthProvider{defaultAuthProvider("codex", portal.Auth.Mode)}
	}
	for i := range portal.Auth.Providers {
		if portal.Auth.Providers[i].Mode == "" {
			portal.Auth.Providers[i].Mode = portal.Auth.Mode
		}
		if portal.Auth.Providers[i].Target == "" {
			portal.Auth.Providers[i].Target = "portal"
		}
		if portal.Auth.Providers[i].Status == "" {
			portal.Auth.Providers[i].Status = AuthUnknown
		}
	}
}

func defaultAuthProvider(provider string, mode AuthMode) AuthProvider {
	return AuthProvider{Provider: provider, Mode: mode, Target: "portal", Status: AuthUnknown}
}

func validateLabels(spaceID string, portal Portal) error {
	if portal.Runtime.Labels["stave.space"] != spaceID {
		return fmt.Errorf("runtime labels must include stave.space=%s", spaceID)
	}
	if portal.Runtime.Labels["stave.portal"] != portal.ID {
		return fmt.Errorf("runtime labels must include stave.portal=%s", portal.ID)
	}
	return nil
}

func validatePath(label, path string, allowRelative bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if strings.ContainsAny(path, "\x00\n\r") {
		return fmt.Errorf("%s contains an unsafe character", label)
	}
	if strings.Contains(path, "*") || strings.Contains(path, "?") {
		return fmt.Errorf("%s must not contain glob characters", label)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == string(filepath.Separator) || cleaned == "~" {
		return fmt.Errorf("%s %q is too broad", label, path)
	}
	if !allowRelative && strings.HasPrefix(cleaned, "..") {
		return fmt.Errorf("%s %q must not escape upward", label, path)
	}
	return nil
}

func defaultContainerRoot(spaceID string) string {
	return "/workspace/" + spaceID
}

func defaultRemoteRoot(spaceID string) string {
	return "~/stave/agent-work/" + spaceID
}

func defaultContainerName(spaceID, portalID string) string {
	return "stave-" + strings.ReplaceAll(spaceID, ".", "-") + "-" + strings.ReplaceAll(portalID, ".", "-")
}

func defaultProjectName(spaceID string) string {
	return "stave_" + strings.Map(func(r rune) rune {
		switch r {
		case '.', '-':
			return '_'
		default:
			return r
		}
	}, spaceID)
}

func isLocalDriver(driver Driver) bool {
	return driver == DriverDocker || driver == DriverDevcontainer
}

func isRemoteDriver(driver Driver) bool {
	return driver == DriverSSH || driver == DriverEC2Attach
}
