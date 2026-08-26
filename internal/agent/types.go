package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
)

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"

	OpSpaceCreate  = "space_create"
	OpSpaceAdd     = "space_add"
	OpSpaceSync    = "space_sync"
	OpSpaceStatus  = "space_status"
	OpReposList    = "repos_list"
	OpReposSync    = "repos_sync"
	OpReposTethers = "repos_tethers"
	OpSummon       = "summon"

	OpSagaCreate = "saga_create"
	OpSagaStatus = "saga_status"
	OpSagaAdd    = "saga_add"

	OpPortalInit           = "portal_init"
	OpPortalAttach         = "portal_attach"
	OpPortalConfigure      = "portal_configure"
	OpPortalList           = "portal_list"
	OpPortalStatus         = "portal_status"
	OpPortalDoctor         = "portal_doctor"
	OpPortalInspect        = "portal_inspect"
	OpPortalAuthStatus     = "portal_auth_status"
	OpPortalLogs           = "portal_logs"
	OpPortalAuthLogin      = "portal_auth_login"
	OpPortalAuthInherit    = "portal_auth_inherit"
	OpPortalAuthRevoke     = "portal_auth_revoke"
	OpPortalUp             = "portal_up"
	OpPortalSync           = "portal_sync"
	OpPortalSummon         = "portal_summon"
	OpPortalDown           = "portal_down"
	OpPortalDetach         = "portal_detach"
	OpPortalDestroyPreview = "portal_destroy_preview"
)

const (
	RunStatusPlanReady   = "plan_ready"
	RunStatusNeedsInput  = "needs_input"
	RunStatusUnsupported = "unsupported"
	RunStatusError       = "error"
)

type ProviderRequest struct {
	Query      string
	Context    Context
	Dispatcher *ToolDispatcher
	MaxTurns   int
	Trace      io.Writer
}

type Provider interface {
	Run(ctx context.Context, request ProviderRequest) (RunResult, error)
}

type ProviderFactory func(providerName, model, apiKey string) (Provider, error)

type Plan struct {
	Summary    string      `json:"summary"`
	Operations []Operation `json:"operations"`
	Notes      []string    `json:"notes,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
}

type Operation struct {
	Type     string `json:"type"`
	SpaceID  string `json:"space_id,omitempty"`
	PortalID string `json:"portal_id,omitempty"`
	// SagaID names the saga a saga_* operation targets; for saga_add the
	// member being registered is SpaceID.
	SagaID string `json:"saga_id,omitempty"`
	// After lists the member ids a saga_add member lands behind.
	After      []string  `json:"after,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	SpecPath   string    `json:"spec_path,omitempty"`
	Edits      []RepoRef `json:"edits,omitempty"`
	References []RepoRef `json:"references,omitempty"`
	// Memories are raw `[provider:]<spec>` values (space create --memory sugar).
	Memories []string `json:"memories,omitempty"`
	// Common requests expanding an editable repo's tethers into reference
	// worktrees at execute time (space create -c). IncludeWeak widens that
	// expansion to weak tethers (--include-weak, which implies Common).
	Common      bool `json:"common,omitempty"`
	IncludeWeak bool `json:"include_weak,omitempty"`
	// MemoryFate is keep|destroy|contribute for space destroy (default keep).
	MemoryFate       string   `json:"memory_fate,omitempty"`
	Repo             string   `json:"repo,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Base             string   `json:"base,omitempty"`
	Ref              string   `json:"ref,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	NoFetch          bool     `json:"no_fetch,omitempty"`
	ReferencesOnly   bool     `json:"references_only,omitempty"`
	Summoner         string   `json:"summoner,omitempty"`
	Driver           string   `json:"driver,omitempty"`
	Preset           string   `json:"preset,omitempty"`
	Engine           string   `json:"engine,omitempty"`
	Image            string   `json:"image,omitempty"`
	Host             string   `json:"host,omitempty"`
	Port             int      `json:"port,omitempty"`
	InstanceID       string   `json:"instance_id,omitempty"`
	Region           string   `json:"region,omitempty"`
	Profile          string   `json:"profile,omitempty"`
	SSHUser          string   `json:"ssh_user,omitempty"`
	IdentityPath     string   `json:"identity_path,omitempty"`
	KnownHostsPath   string   `json:"known_hosts_path,omitempty"`
	StrictHostKey    string   `json:"strict_host_key,omitempty"`
	RemoteRoot       string   `json:"remote_root,omitempty"`
	ContainerRoot    string   `json:"container_root,omitempty"`
	DevcontainerPath string   `json:"devcontainer_path,omitempty"`
	ComposeFiles     []string `json:"compose_files,omitempty"`
	Service          string   `json:"service,omitempty"`
	SyncMode         string   `json:"sync_mode,omitempty"`
	Direction        string   `json:"direction,omitempty"`
	Include          []string `json:"include,omitempty"`
	Exclude          []string `json:"exclude,omitempty"`
	Delete           bool     `json:"delete,omitempty"`
	MaxDelete        int      `json:"max_delete,omitempty"`
	AllowDirty       bool     `json:"allow_dirty,omitempty"`
	AttachMode       string   `json:"attach_mode,omitempty"`
	Workdir          string   `json:"workdir,omitempty"`
	TTY              string   `json:"tty,omitempty"`
	Permission       string   `json:"permission,omitempty"`
	HandoffPrompt    string   `json:"handoff_prompt,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	Method           string   `json:"method,omitempty"`
	Target           string   `json:"target,omitempty"`
	Agent            string   `json:"agent,omitempty"`
	Tail             int      `json:"tail,omitempty"`
	Follow           bool     `json:"follow,omitempty"`
	Timeout          int      `json:"timeout,omitempty"`
	Force            bool     `json:"force,omitempty"`
	Unsupported      string   `json:"unsupported,omitempty"`
}

type RepoRef struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
}

type Context struct {
	Repos       []RepoContext  `json:"repos"`
	Spaces      []SpaceContext `json:"spaces"`
	DefaultBase string         `json:"default_base"`
}

type RepoContext struct {
	Name          string `json:"name"`
	URL           string `json:"url,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type SpaceContext struct {
	ID       string               `json:"id"`
	Kind     string               `json:"kind,omitempty"`
	SpecPath string               `json:"spec_path,omitempty"`
	Repos    []SpaceRepoContext   `json:"repos"`
	Memories []SpaceMemoryContext `json:"memories,omitempty"`
	Portals  []PortalContext      `json:"portals,omitempty"`
	Saga     *SagaContext         `json:"saga,omitempty"`
}

// SagaContext surfaces a saga space's member roster to the planner: after
// edges plus the cached PR identities recorded per member (identity only;
// merge state is never cached).
type SagaContext struct {
	Members []SagaMemberContext `json:"members"`
}

// SagaMemberContext is one roster row of SagaContext.
type SagaMemberContext struct {
	ID    string          `json:"id"`
	After []string        `json:"after,omitempty"`
	PRs   []SagaPRContext `json:"prs,omitempty"`
}

// SagaPRContext is one cached pull-request identity for a member repo.
type SagaPRContext struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// SpaceMemoryContext surfaces attachment records to the planner.
type SpaceMemoryContext struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Owned    bool   `json:"owned"`
}

type SpaceRepoContext struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Path   string `json:"path"`
	Base   string `json:"base,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type PortalContext struct {
	ID             string                 `json:"id"`
	Driver         string                 `json:"driver,omitempty"`
	SyncMode       string                 `json:"sync_mode,omitempty"`
	LocalPath      string                 `json:"local_path,omitempty"`
	RemoteRoot     string                 `json:"remote_root,omitempty"`
	ContainerRoot  string                 `json:"container_root,omitempty"`
	AuthMode       string                 `json:"auth_mode,omitempty"`
	Providers      []string               `json:"providers,omitempty"`
	Ownership      PortalOwnershipContext `json:"ownership,omitempty"`
	ManifestExists bool                   `json:"manifest_exists"`
	Runtime        PortalRuntimeContext   `json:"runtime,omitempty"`
	Target         PortalTargetContext    `json:"target,omitempty"`
}

type PortalRuntimeContext struct {
	Engine           string   `json:"engine,omitempty"`
	Image            string   `json:"image,omitempty"`
	ContainerName    string   `json:"container_name,omitempty"`
	ProjectName      string   `json:"project_name,omitempty"`
	Service          string   `json:"service,omitempty"`
	DevcontainerPath string   `json:"devcontainer_path,omitempty"`
	ComposeFiles     []string `json:"compose_files,omitempty"`
}

type PortalTargetContext struct {
	Host          string `json:"host,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	Region        string `json:"region,omitempty"`
	DockerContext string `json:"docker_context,omitempty"`
}

type PortalOwnershipContext struct {
	CreatedContainer bool   `json:"created_container,omitempty"`
	RemoteRoot       string `json:"remote_root,omitempty"`
}

type ExecutionResult struct {
	Operation Operation `json:"operation"`
	Command   string    `json:"command"`
	Executed  bool      `json:"executed"`
	Message   string    `json:"message,omitempty"`
}

type RunResult struct {
	Status      string             `json:"status"`
	Message     string             `json:"message,omitempty"`
	Questions   []Question         `json:"questions,omitempty"`
	Plan        Plan               `json:"plan"`
	Commands    []string           `json:"commands"`
	ToolCalls   []ToolCallRecord   `json:"toolCalls,omitempty"`
	ReadResults []ToolResultRecord `json:"readResults,omitempty"`
	Results     []ExecutionResult  `json:"results,omitempty"`
	Executed    bool               `json:"executed"`
}

type Question struct {
	ID          string   `json:"id"`
	Prompt      string   `json:"prompt"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (p Plan) Commands() []string {
	commands := make([]string, 0, len(p.Operations))
	for _, op := range p.Operations {
		commands = append(commands, EquivalentCommand(op))
	}
	return commands
}

func EquivalentCommand(op Operation) string {
	switch op.Type {
	case OpSpaceCreate:
		parts := []string{"stave", "space", "create", shellQuote(op.SpaceID)}
		if op.Kind != "" {
			parts = append(parts, "-k", shellQuote(op.Kind))
		}
		if op.SpecPath != "" {
			parts = append(parts, "-s", shellQuote(op.SpecPath))
		}
		for _, ref := range op.Edits {
			parts = append(parts, "-e", shellQuote(repoSpec(ref)))
		}
		for _, ref := range op.References {
			parts = append(parts, "-r", shellQuote(repoSpec(ref)))
		}
		for _, mem := range op.Memories {
			parts = append(parts, "--memory", shellQuote(mem))
		}
		if op.Common {
			parts = append(parts, "-c")
		}
		if op.IncludeWeak {
			parts = append(parts, "--include-weak")
		}
		return strings.Join(parts, " ")
	case OpSpaceAdd:
		parts := []string{"stave", "space", "add", shellQuote(op.SpaceID), shellQuote(op.Repo)}
		if op.Mode == string(space.ModeReference) {
			parts = append(parts, "-r")
		} else {
			parts = append(parts, "-e")
		}
		if op.Base != "" {
			parts = append(parts, "-b", shellQuote(op.Base))
		}
		if op.Branch != "" {
			parts = append(parts, "--branch", shellQuote(op.Branch))
		}
		if op.NoFetch {
			parts = append(parts, "--no-fetch")
		}
		return strings.Join(parts, " ")
	case OpSpaceSync:
		parts := []string{"stave", "space", "sync", shellQuote(op.SpaceID)}
		if op.ReferencesOnly {
			parts = append(parts, "--references-only")
		}
		return strings.Join(parts, " ")
	case OpSpaceStatus:
		return strings.Join([]string{"stave", "space", "status", shellQuote(op.SpaceID)}, " ")
	case OpReposList:
		return "stave repos list"
	case OpReposTethers:
		return strings.Join([]string{"stave", "repos", "tethers", shellQuote(op.Repo)}, " ")
	case OpReposSync:
		if op.Repo != "" {
			return strings.Join([]string{"stave", "repos", "sync", shellQuote(op.Repo)}, " ")
		}
		return "stave repos sync"
	case OpSummon:
		summoner := op.Summoner
		if summoner == "" {
			summoner = summon.Codex
		}
		return strings.Join([]string{"stave", "summon", shellQuote(op.SpaceID), "--with", shellQuote(summoner)}, " ")
	case OpSagaCreate:
		parts := []string{"stave", "saga", "create", shellQuote(op.SagaID)}
		if op.SpecPath != "" {
			parts = append(parts, "-s", shellQuote(op.SpecPath))
		}
		for _, ref := range op.References {
			parts = append(parts, "-r", shellQuote(repoSpec(ref)))
		}
		for _, mem := range op.Memories {
			parts = append(parts, "--memory", shellQuote(mem))
		}
		return strings.Join(parts, " ")
	case OpSagaStatus:
		return strings.Join([]string{"stave", "saga", "status", shellQuote(op.SagaID)}, " ")
	case OpSagaAdd:
		parts := []string{"stave", "saga", "add", shellQuote(op.SagaID), shellQuote(op.SpaceID)}
		for _, id := range op.After {
			parts = append(parts, "--after", shellQuote(id))
		}
		return strings.Join(parts, " ")
	case OpPortalList:
		parts := []string{"stave", "portal", "list"}
		appendSpaceAndPortal(&parts, op)
		return strings.Join(parts, " ")
	case OpPortalStatus:
		parts := []string{"stave", "portal", "status", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		return strings.Join(parts, " ")
	case OpPortalDoctor:
		parts := []string{"stave", "portal", "doctor", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		return strings.Join(parts, " ")
	case OpPortalInspect:
		parts := []string{"stave", "portal", "inspect", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		return strings.Join(parts, " ")
	case OpPortalAuthStatus:
		parts := []string{"stave", "portal", "auth", "status", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--provider", op.Provider)
		return strings.Join(parts, " ")
	case OpPortalLogs:
		parts := []string{"stave", "portal", "logs", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--agent", op.Agent)
		appendIntFlag(&parts, "--tail", op.Tail)
		return strings.Join(parts, " ")
	case OpPortalInit:
		parts := []string{"stave", "portal", "init"}
		if op.Driver != "" {
			parts = append(parts, shellQuote(portalInitDriver(op.Driver)))
		}
		parts = append(parts, shellQuote(op.SpaceID))
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--image", op.Image)
		appendFlagValue(&parts, "--engine", op.Engine)
		appendFlagValue(&parts, "--container-root", op.ContainerRoot)
		appendFlagValue(&parts, "--preset", op.Preset)
		appendFlagValue(&parts, "--path", op.DevcontainerPath)
		appendFlagValue(&parts, "--repo", op.Repo)
		appendFlagValue(&parts, "--service", op.Service)
		appendRepeatedFlag(&parts, "--compose-file", op.ComposeFiles)
		appendFlagValue(&parts, "--sync", op.SyncMode)
		return strings.Join(parts, " ")
	case OpPortalAttach:
		parts := []string{"stave", "portal", "attach", shellQuote(portalAttachDriver(op.Driver)), shellQuote(op.SpaceID)}
		if op.Driver == string(portal.DriverEC2Attach) {
			parts = append(parts, shellQuote(op.InstanceID))
		} else {
			parts = append(parts, shellQuote(op.Host))
		}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--remote-root", op.RemoteRoot)
		appendIntFlag(&parts, "--port", op.Port)
		appendFlagValue(&parts, "--identity", op.IdentityPath)
		appendFlagValue(&parts, "--known-hosts", op.KnownHostsPath)
		appendFlagValue(&parts, "--strict-host-key", op.StrictHostKey)
		appendFlagValue(&parts, "--sync", op.SyncMode)
		appendFlagValue(&parts, "--preset", op.Preset)
		if op.Driver == string(portal.DriverEC2Attach) || op.Driver == "ec2" {
			appendFlagValue(&parts, "--host", op.Host)
		}
		appendFlagValue(&parts, "--region", op.Region)
		appendFlagValue(&parts, "--profile", op.Profile)
		appendFlagValue(&parts, "--ssh-user", op.SSHUser)
		return strings.Join(parts, " ")
	case OpPortalConfigure:
		parts := []string{"stave", "portal", "configure", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--sync", op.SyncMode)
		appendFlagValue(&parts, "--container-root", op.ContainerRoot)
		appendFlagValue(&parts, "--remote-root", op.RemoteRoot)
		appendFlagValue(&parts, "--host", op.Host)
		appendFlagValue(&parts, "--agent", op.Agent)
		appendFlagValue(&parts, "--auth", op.Method)
		return strings.Join(parts, " ")
	case OpPortalAuthLogin:
		parts := []string{"stave", "portal", "auth", "login", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--provider", op.Provider)
		appendFlagValue(&parts, "--method", op.Method)
		return strings.Join(parts, " ")
	case OpPortalAuthInherit:
		parts := []string{"stave", "portal", "auth", "inherit", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--provider", op.Provider)
		appendFlagValue(&parts, "--method", op.Method)
		parts = append(parts, "--yes")
		return strings.Join(parts, " ")
	case OpPortalAuthRevoke:
		parts := []string{"stave", "portal", "auth", "revoke", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--provider", op.Provider)
		appendFlagValue(&parts, "--target", op.Target)
		parts = append(parts, "--yes")
		return strings.Join(parts, " ")
	case OpPortalUp:
		parts := []string{"stave", "portal", "up", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--attach", op.AttachMode)
		appendFlagValue(&parts, "--workdir", op.Workdir)
		return strings.Join(parts, " ")
	case OpPortalSync:
		parts := []string{"stave", "portal", "sync", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--direction", op.Direction)
		appendFlagValue(&parts, "--mode", op.SyncMode)
		if op.ReferencesOnly {
			parts = append(parts, "--references-only")
		}
		appendRepeatedFlag(&parts, "--include", op.Include)
		appendRepeatedFlag(&parts, "--exclude", op.Exclude)
		if op.Delete {
			parts = append(parts, "--delete")
		}
		appendIntFlag(&parts, "--max-delete", op.MaxDelete)
		if op.AllowDirty {
			parts = append(parts, "--allow-dirty")
		}
		return strings.Join(parts, " ")
	case OpPortalSummon:
		parts := []string{"stave", "portal", "summon", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendFlagValue(&parts, "--with", op.Summoner)
		appendFlagValue(&parts, "--mode", op.Mode)
		appendFlagValue(&parts, "--permission", op.Permission)
		return strings.Join(parts, " ")
	case OpPortalDown:
		parts := []string{"stave", "portal", "down", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		appendIntFlag(&parts, "--timeout", op.Timeout)
		if op.Force {
			parts = append(parts, "--force")
		}
		return strings.Join(parts, " ")
	case OpPortalDetach:
		parts := []string{"stave", "portal", "detach", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		return strings.Join(parts, " ")
	case OpPortalDestroyPreview:
		parts := []string{"stave", "portal", "destroy", shellQuote(op.SpaceID)}
		appendPortalID(&parts, op)
		parts = append(parts, "--dry-run")
		return strings.Join(parts, " ")
	default:
		if op.Unsupported != "" {
			return "# unsupported: " + op.Unsupported
		}
		return "# unsupported operation: " + op.Type
	}
}

func appendSpaceAndPortal(parts *[]string, op Operation) {
	if op.SpaceID == "" {
		return
	}
	*parts = append(*parts, shellQuote(op.SpaceID))
	appendPortalID(parts, op)
}

func appendPortalID(parts *[]string, op Operation) {
	if op.PortalID != "" && op.PortalID != portal.DefaultPortalID {
		*parts = append(*parts, shellQuote(op.PortalID))
	}
}

func portalID(id string) string {
	if id == "" {
		return portal.DefaultPortalID
	}
	return id
}

func appendFlagValue(parts *[]string, flag string, value string) {
	if value != "" {
		*parts = append(*parts, flag, shellQuote(value))
	}
}

func appendIntFlag(parts *[]string, flag string, value int) {
	if value > 0 {
		*parts = append(*parts, flag, fmt.Sprintf("%d", value))
	}
}

func appendRepeatedFlag(parts *[]string, flag string, values []string) {
	for _, value := range values {
		if value != "" {
			*parts = append(*parts, flag, shellQuote(value))
		}
	}
}

func portalInitDriver(driver string) string {
	if driver == string(portal.DriverDocker) {
		return "container"
	}
	return driver
}

func portalAttachDriver(driver string) string {
	if driver == string(portal.DriverEC2Attach) {
		return "ec2"
	}
	return driver
}

func isPortalOperation(op Operation) bool {
	return strings.HasPrefix(op.Type, "portal_")
}

func ParsePlanText(text string) (Plan, error) {
	cleaned := strings.TrimSpace(text)
	if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
	}
	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		cleaned = cleaned[start : end+1]
	}
	var plan Plan
	if err := json.Unmarshal([]byte(cleaned), &plan); err != nil {
		return Plan{}, fmt.Errorf("parse agent plan JSON: %w", err)
	}
	return plan, nil
}

func repoSpec(ref RepoRef) string {
	if ref.Ref == "" {
		return ref.Name
	}
	return ref.Name + ":" + ref.Ref
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n'\"$`\\*?[]{}()<>|&;!") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}
