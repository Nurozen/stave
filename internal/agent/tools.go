package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
)

const (
	ToolReposList            = "stave_repos_list"
	ToolReposSync            = "stave_repos_sync"
	ToolSpaceStatus          = "stave_space_status"
	ToolSpaceSync            = "stave_space_sync"
	ToolSpaceCreate          = "stave_space_create"
	ToolSpaceAdd             = "stave_space_add"
	ToolSummon               = "stave_summon"
	ToolAsk                  = "stave_ask"
	ToolPortalList           = "stave_portal_list"
	ToolPortalStatus         = "stave_portal_status"
	ToolPortalDoctor         = "stave_portal_doctor"
	ToolPortalInspect        = "stave_portal_inspect"
	ToolPortalAuthStatus     = "stave_portal_auth_status"
	ToolPortalLogs           = "stave_portal_logs"
	ToolPortalInit           = "stave_portal_init"
	ToolPortalAttach         = "stave_portal_attach"
	ToolPortalConfigure      = "stave_portal_configure"
	ToolPortalAuthLogin      = "stave_portal_auth_login"
	ToolPortalAuthInherit    = "stave_portal_auth_inherit"
	ToolPortalAuthRevoke     = "stave_portal_auth_revoke"
	ToolPortalUp             = "stave_portal_up"
	ToolPortalSync           = "stave_portal_sync"
	ToolPortalSummon         = "stave_portal_summon"
	ToolPortalDown           = "stave_portal_down"
	ToolPortalDetach         = "stave_portal_detach"
	ToolPortalDestroyPreview = "stave_portal_destroy_preview"
	ToolExplainUnsupported   = "stave_explain_unsupported"
	ToolFinish               = "stave_finish"
)

type ToolCategory string

const (
	ToolCategoryRead    ToolCategory = "read"
	ToolCategoryMutate  ToolCategory = "mutate"
	ToolCategoryControl ToolCategory = "control"
)

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
	Category    ToolCategory
}

type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ToolCallRecord struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Error     bool            `json:"error,omitempty"`
	Message   string          `json:"message,omitempty"`
}

type ToolResult struct {
	CallID  string `json:"callId,omitempty"`
	Name    string `json:"name"`
	Payload any    `json:"payload,omitempty"`
	Error   bool   `json:"error,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ToolResultRecord = ToolResult

type ToolSession struct {
	Plan        Plan
	ToolCalls   []ToolCallRecord
	ReadResults []ToolResultRecord
	Status      string
	Message     string
	Questions   []Question
	Finished    bool
}

func NewToolSession() *ToolSession {
	return &ToolSession{Plan: Plan{Operations: []Operation{}}}
}

func (s *ToolSession) RunResult() RunResult {
	plan := redactPlan(s.Plan)
	commands := plan.Commands()
	status := s.Status
	if status == "" {
		status = RunStatusPlanReady
	}
	message := s.Message
	if message == "" {
		message = plan.Summary
	}
	return RunResult{
		Status:      status,
		Message:     message,
		Questions:   append([]Question(nil), s.Questions...),
		Plan:        plan,
		Commands:    commands,
		ToolCalls:   redactToolCalls(s.ToolCalls),
		ReadResults: redactToolResults(s.ReadResults),
	}
}

type ToolDispatcher struct {
	Config  config.Config
	Git     *git.Client
	Out     io.Writer
	Session *ToolSession
}

func NewToolDispatcher(cfg config.Config, gitClient *git.Client, out io.Writer) *ToolDispatcher {
	if gitClient == nil {
		gitClient = git.New()
	}
	return &ToolDispatcher{Config: cfg, Git: gitClient, Out: out, Session: NewToolSession()}
}

func ToolDefinitions() []ToolDefinition {
	defs := []ToolDefinition{
		{
			Name:        ToolReposList,
			Category:    ToolCategoryRead,
			Description: "List all repositories registered in Stave config. Use this when the user asks what repositories are available, uses an ambiguous repo name, or asks for setup involving repos that may need to be resolved from the registry. This is read-only and never changes filesystem or Git state.",
			Parameters:  objectSchema(nil, nil),
		},
		{
			Name:        ToolReposSync,
			Category:    ToolCategoryMutate,
			Description: "Propose synchronizing registered bare repository caches with their remotes using fetch/prune. Use this when the user asks to update repo caches, refresh all repos, or sync one registered repo. This is a mutating Git/network operation and will be queued for user confirmation before execution.",
			Parameters: objectSchema(map[string]any{
				"repo": stringSchema("Optional registered repo name. If omitted, sync all registered repos."),
			}, nil),
		},
		{
			Name:        ToolSpaceStatus,
			Category:    ToolCategoryRead,
			Description: "Inspect an existing Stave space. Use this to understand what repos are already attached, whether worktrees exist, dirty state, and editable branch drift. This is read-only and safe to call during planning.",
			Parameters: objectSchema(map[string]any{
				"space_id": stringSchema("Existing Stave space id."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolSpaceSync,
			Category:    ToolCategoryMutate,
			Description: "Propose synchronizing an existing Stave space. This fetches relevant bare repos, updates detached reference worktrees when clean, and reports editable branch drift without rebasing, resetting, or force-updating editable worktrees. Use this when the user asks to refresh a space or update references.",
			Parameters: objectSchema(map[string]any{
				"space_id":        stringSchema("Existing Stave space id."),
				"references_only": boolSchema("When true, sync only reference worktrees."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolSpaceCreate,
			Category:    ToolCategoryMutate,
			Description: "Propose creating a new Stave agent workspace. Use this when the user wants a new ticket, audit, spike, or agent work area. Editable repos become top-level worktrees. Reference repos become detached read-only-context worktrees under references/. Prefer this single tool over separate create/add steps when the user describes a new workspace and its repos together.",
			Parameters: objectSchema(map[string]any{
				"space_id":   stringSchema("New Stave space id."),
				"kind":       stringSchema("Optional kind such as ticket, audit, or spike."),
				"spec_path":  stringSchema("Optional path to a spec file or directory supplied by the user."),
				"edits":      repoRefArraySchema("Registered repos to create as editable top-level worktrees."),
				"references": repoRefArraySchema("Registered repos to create as detached reference worktrees under references/."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolSpaceAdd,
			Category:    ToolCategoryMutate,
			Description: "Propose adding one registered repo to an existing Stave space as either editable or reference context. Use editable mode for repos the agent/user intends to modify. Use reference mode for repos that should be available as read-only context under references/. Use this only for existing spaces; for new spaces with multiple repos, prefer stave_space_create.",
			Parameters: objectSchema(map[string]any{
				"space_id": stringSchema("Existing Stave space id."),
				"repo":     stringSchema("Registered repo name."),
				"mode": map[string]any{
					"type":        "string",
					"description": "Whether to add the repo as an editable worktree or a reference worktree.",
					"enum":        []string{"edit", "reference"},
				},
				"base":     stringSchema("Optional branch/ref for editable repos."),
				"ref":      stringSchema("Optional ref for reference repos."),
				"branch":   stringSchema("Optional branch name for editable repos."),
				"no_fetch": boolSchema("When true, do not fetch before adding the worktree."),
			}, []string{"space_id", "repo", "mode"}),
		},
		{
			Name:        ToolSummon,
			Category:    ToolCategoryMutate,
			Description: "Propose launching Codex, Claude Code, or Cursor Agent in a Stave space. Use this when the user asks to summon, open, start, or hand off to an interactive coding agent. The process starts in the Stave space root so it can see .stave.yaml, AGENTS.md, specs, editable repos, and references/. This interactive operation is queued for user confirmation before execution and never runs during planning.",
			Parameters: objectSchema(map[string]any{
				"space_id": stringSchema("Existing Stave space id, or a space id created earlier in this same plan."),
				"summoner": map[string]any{
					"type":        "string",
					"description": "Interactive agent to launch.",
					"enum":        []string{"codex", "claude", "cursor"},
				},
			}, []string{"space_id", "summoner"}),
		},
		{
			Name:        ToolAsk,
			Category:    ToolCategoryControl,
			Description: "Ask the user for missing portal planning information and stop. Use this when required slots are missing after available read-only tools have been used. Do not queue mutations in the same response.",
			Parameters: objectSchema(map[string]any{
				"message":   stringSchema("Short explanation of what information is needed."),
				"questions": questionArraySchema("One to three focused structured questions."),
			}, []string{"message", "questions"}),
		},
	}
	defs = append(defs, portalToolDefinitions()...)
	defs = append(defs,
		ToolDefinition{
			Name:        ToolExplainUnsupported,
			Category:    ToolCategoryControl,
			Description: "Record that the user requested an operation Stave Agent will not execute in v1, such as destroy, archive, remove, reset, delete, push, PR creation, issue tracker updates, or arbitrary shell commands. Use this instead of inventing unsupported tools.",
			Parameters: objectSchema(map[string]any{
				"request":          stringSchema("The unsupported user request."),
				"reason":           stringSchema("Why Stave Agent will not execute it."),
				"manual_follow_up": stringSchema("Optional manual follow-up command or guidance."),
			}, []string{"request", "reason"}),
		},
		ToolDefinition{
			Name:        ToolFinish,
			Category:    ToolCategoryControl,
			Description: "Finish planning after all needed read tools have been used and all proposed executable operations have been queued. Provide the final user-facing summary, assumptions, and any warnings. Use this exactly once when the plan is complete.",
			Parameters: objectSchema(map[string]any{
				"summary":  stringSchema("Short user-facing summary of the plan."),
				"notes":    stringArraySchema("Optional assumptions or notes."),
				"warnings": stringArraySchema("Optional warnings."),
			}, []string{"summary"}),
		},
	)
	return defs
}

func portalToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolPortalList,
			Category:    ToolCategoryRead,
			Description: "List portal manifests for all spaces or for one space. This is read-only and reports manifest-level portal attachments only.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Optional existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default when omitted by portal commands that target one portal."),
			}, nil),
		},
		{
			Name:        ToolPortalStatus,
			Category:    ToolCategoryRead,
			Description: "Read portal runtime and auth status for an existing portal. This is read-only and should be used before proposing portal summon or runtime actions.",
			Parameters:  portalTargetSchema(),
		},
		{
			Name:        ToolPortalDoctor,
			Category:    ToolCategoryRead,
			Description: "Run read-only portal preflight checks for dependencies, manifest shape, auth posture, and safe workspace bindings.",
			Parameters:  portalTargetSchema(),
		},
		{
			Name:        ToolPortalInspect,
			Category:    ToolCategoryRead,
			Description: "Inspect resolved portal configuration and ownership metadata for an existing portal without changing runtime state.",
			Parameters:  portalTargetSchema(),
		},
		{
			Name:        ToolPortalAuthStatus,
			Category:    ToolCategoryRead,
			Description: "Check provider auth status inside a portal target. This is read-only and never transfers or persists secrets.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"provider":  enumSchema("Provider to check.", []string{"codex", "claude", "cursor", "all"}),
			}, []string{"space_id", "provider"}),
		},
		{
			Name:        ToolPortalLogs,
			Category:    ToolCategoryRead,
			Description: "Read bounded portal logs. Follow mode is not allowed for agent planning.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"agent":     enumSchema("Optional agent log stream.", []string{"codex", "claude", "cursor"}),
				"tail":      intSchema("Maximum number of log lines to read."),
				"follow":    boolSchema("Must be false. Follow mode is unsupported for agent planning."),
			}, []string{"space_id", "tail"}),
		},
		{
			Name:        ToolPortalInit,
			Category:    ToolCategoryMutate,
			Description: "Queue creation of portal metadata for a Stave-owned local runtime attached to an existing or same-plan-created space. This does not start compute.",
			Parameters: objectSchema(map[string]any{
				"space_id":          stringSchema("Existing Stave space id, or a space id created earlier in this same plan."),
				"portal_id":         stringSchema("Optional portal id. Defaults to default."),
				"driver":            enumSchema("Local portal driver.", []string{"docker", "devcontainer"}),
				"preset":            stringSchema("Optional portal preset."),
				"engine":            enumSchema("Optional local container engine.", []string{"docker"}),
				"image":             stringSchema("Optional container image for docker portals."),
				"container_root":    stringSchema("Optional workspace path inside the container."),
				"devcontainer_path": stringSchema("Optional path to devcontainer.json."),
				"repo":              stringSchema("Optional repo name for devcontainer selection."),
				"service":           stringSchema("Optional devcontainer/compose service."),
				"compose_files":     stringArraySchema("Optional repeatable compose file paths."),
				"sync_mode":         enumSchema("Optional sync mode.", []string{"mount", "rsync", "reconstruct"}),
			}, []string{"space_id", "driver"}),
		},
		{
			Name:        ToolPortalAttach,
			Category:    ToolCategoryMutate,
			Description: "Queue attaching an existing external SSH or EC2 resource to a Stave space. This never provisions, starts, stops, terminates, or deletes the resource.",
			Parameters: objectSchema(map[string]any{
				"space_id":      stringSchema("Existing Stave space id, or a space id created earlier in this same plan."),
				"portal_id":     stringSchema("Optional portal id. Defaults to default."),
				"driver":        enumSchema("Attach target type.", []string{"ssh", "ec2-attach"}),
				"host":          stringSchema("SSH host for ssh attach."),
				"port":          intSchema("Optional SSH port."),
				"instance_id":   stringSchema("EC2 instance id for ec2-attach."),
				"region":        stringSchema("Optional AWS region."),
				"profile":       stringSchema("Optional AWS profile."),
				"ssh_user":      stringSchema("Optional SSH user."),
				"identity_path": stringSchema("Optional identity file path."),
				"remote_root":   stringSchema("Remote Stave workspace root."),
				"sync_mode":     enumSchema("Remote sync mode.", []string{"rsync", "reconstruct"}),
				"preset":        stringSchema("Optional portal preset."),
			}, []string{"space_id", "driver"}),
		},
		{
			Name:        ToolPortalConfigure,
			Category:    ToolCategoryMutate,
			Description: "Queue safe configuration changes for an existing portal.",
			Parameters: objectSchema(map[string]any{
				"space_id":       stringSchema("Existing Stave space id."),
				"portal_id":      stringSchema("Optional portal id. Defaults to default."),
				"sync_mode":      enumSchema("Optional sync mode.", []string{"mount", "rsync", "reconstruct"}),
				"container_root": stringSchema("Optional container workspace root."),
				"remote_root":    stringSchema("Optional remote workspace root."),
				"agent":          enumSchema("Optional default portal agent.", []string{"codex", "claude", "cursor"}),
				"auth":           enumSchema("Optional auth mode.", []string{"native", "env", "volume", "ssh-forward"}),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolPortalAuthLogin,
			Category:    ToolCategoryMutate,
			Description: "Queue provider-native login inside the portal target. This is interactive and does not copy local credential caches.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"provider":  enumSchema("Provider to authenticate.", []string{"codex", "claude", "cursor"}),
				"method":    enumSchema("Provider-native login method.", []string{"native", "device"}),
			}, []string{"space_id", "provider", "method"}),
		},
		{
			Name:        ToolPortalAuthInherit,
			Category:    ToolCategoryMutate,
			Description: "Queue an explicit non-copy-cache auth inheritance method for a portal target. Agent planning cannot queue copy-cache.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"provider":  enumSchema("Provider to authenticate.", []string{"codex", "claude", "cursor"}),
				"method":    enumSchema("Allowed inherit method.", []string{"env", "volume", "ssh-forward"}),
			}, []string{"space_id", "provider", "method"}),
		},
		{
			Name:        ToolPortalAuthRevoke,
			Category:    ToolCategoryMutate,
			Description: "Queue revoking local cached provider auth on the selected target. Server-side revocation remains provider guidance.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"provider":  enumSchema("Provider to revoke.", []string{"codex", "claude", "cursor"}),
				"target":    enumSchema("Auth cache target.", []string{"local", "portal", "all"}),
			}, []string{"space_id", "provider", "target"}),
		},
		{
			Name:        ToolPortalUp,
			Category:    ToolCategoryMutate,
			Description: "Queue starting or reusing a declared local portal runtime, or validating an attach-only portal. Non-interactive execution must not attach a shell.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id, or a space id with portal init/attach earlier in this same plan."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"attach":    enumSchema("Attach behavior.", []string{"shell", "none"}),
				"workdir":   stringSchema("Optional portal workdir."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolPortalSync,
			Category:    ToolCategoryMutate,
			Description: "Queue a guarded portal workspace sync. Deletes are off by default and must be explicitly bounded.",
			Parameters: objectSchema(map[string]any{
				"space_id":        stringSchema("Existing Stave space id, or a space id with portal init/attach earlier in this same plan."),
				"portal_id":       stringSchema("Optional portal id. Defaults to default."),
				"direction":       enumSchema("Sync direction.", []string{"to", "from", "both"}),
				"mode":            enumSchema("Sync mode.", []string{"auto", "mount", "rsync", "reconstruct"}),
				"references_only": boolSchema("When true, sync references only."),
				"include":         stringArraySchema("Optional include patterns."),
				"exclude":         stringArraySchema("Optional exclude patterns."),
				"delete":          boolSchema("Whether to allow deletes."),
				"max_delete":      intSchema("Maximum deletes allowed when delete is true."),
				"allow_dirty":     boolSchema("Whether to allow dirty local editable worktrees for destructive pulls."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolPortalSummon,
			Category:    ToolCategoryMutate,
			Description: "Queue launching Codex, Claude, or Cursor Agent inside an existing portal target after status/auth preflight.",
			Parameters: objectSchema(map[string]any{
				"space_id":   stringSchema("Existing Stave space id, or a space id with portal init/attach earlier in this same plan."),
				"portal_id":  stringSchema("Optional portal id. Defaults to default."),
				"summoner":   enumSchema("Agent to launch.", []string{"codex", "claude", "cursor"}),
				"mode":       enumSchema("Portal summon mode.", []string{"foreground", "tmux", "headless", "print"}),
				"permission": enumSchema("Agent permission level.", []string{"read-only", "workspace-write"}),
			}, []string{"space_id", "summoner"}),
		},
		{
			Name:        ToolPortalDown,
			Category:    ToolCategoryMutate,
			Description: "Queue stopping Stave-owned local portal runtime resources while preserving data. Attach-only portals should use detach instead.",
			Parameters: objectSchema(map[string]any{
				"space_id":  stringSchema("Existing Stave space id."),
				"portal_id": stringSchema("Optional portal id. Defaults to default."),
				"timeout":   intSchema("Optional graceful stop timeout in seconds."),
				"force":     boolSchema("Force stop only Stave-owned runtime resources."),
			}, []string{"space_id"}),
		},
		{
			Name:        ToolPortalDetach,
			Category:    ToolCategoryMutate,
			Description: "Queue detaching portal metadata for attach-only SSH or EC2 targets without stopping or deleting the external resource.",
			Parameters:  portalTargetSchema(),
		},
		{
			Name:        ToolPortalDestroyPreview,
			Category:    ToolCategoryRead,
			Description: "Preview what portal destroy would affect. This is dry-run only and queues no destructive action.",
			Parameters:  portalTargetSchema(),
		},
	}
}

func (d *ToolDispatcher) Dispatch(ctx context.Context, call ToolCall) ToolResult {
	if d.Session == nil {
		d.Session = NewToolSession()
	}
	d.Session.ToolCalls = append(d.Session.ToolCalls, ToolCallRecord{ID: call.ID, Name: call.Name, Arguments: redactRawJSON(call.Arguments)})
	result := d.dispatch(ctx, call)
	if result.Error {
		d.Session.ToolCalls[len(d.Session.ToolCalls)-1].Error = true
		d.Session.ToolCalls[len(d.Session.ToolCalls)-1].Message = result.Summary
	}
	return result
}

func (d *ToolDispatcher) dispatch(ctx context.Context, call ToolCall) ToolResult {
	switch call.Name {
	case ToolReposList:
		return d.reposList(call)
	case ToolReposSync:
		var args reposSyncArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpReposSync, Repo: args.Repo}
		return d.queueOperation(call, op)
	case ToolSpaceStatus:
		var args spaceStatusArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.spaceStatus(ctx, call, args.SpaceID)
	case ToolSpaceSync:
		var args spaceSyncArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpSpaceSync, SpaceID: args.SpaceID, ReferencesOnly: args.ReferencesOnly}
		return d.queueOperation(call, op)
	case ToolSpaceCreate:
		var args spaceCreateArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpSpaceCreate, SpaceID: args.SpaceID, Kind: args.Kind, SpecPath: args.SpecPath, Edits: args.Edits, References: args.References}
		return d.queueOperation(call, op)
	case ToolSpaceAdd:
		var args spaceAddArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpSpaceAdd, SpaceID: args.SpaceID, Repo: args.Repo, Mode: args.Mode, Base: args.Base, Ref: args.Ref, Branch: args.Branch, NoFetch: args.NoFetch}
		return d.queueOperation(call, op)
	case ToolSummon:
		var args summonArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpSummon, SpaceID: args.SpaceID, Summoner: args.Summoner}
		return d.queueOperation(call, op)
	case ToolAsk:
		var args askArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.ask(call, args)
	case ToolPortalList:
		var args portalListArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalList, SpaceID: args.SpaceID, PortalID: args.PortalID}
		return d.portalRead(call, op)
	case ToolPortalStatus:
		var args portalTargetArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalStatus, SpaceID: args.SpaceID, PortalID: args.PortalID}
		return d.portalRead(call, op)
	case ToolPortalDoctor:
		var args portalTargetArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalDoctor, SpaceID: args.SpaceID, PortalID: args.PortalID}
		return d.portalRead(call, op)
	case ToolPortalInspect:
		var args portalTargetArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalInspect, SpaceID: args.SpaceID, PortalID: args.PortalID}
		return d.portalRead(call, op)
	case ToolPortalAuthStatus:
		var args portalAuthStatusArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalAuthStatus, SpaceID: args.SpaceID, PortalID: args.PortalID, Provider: args.Provider}
		return d.portalRead(call, op)
	case ToolPortalLogs:
		var args portalLogsArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		op := Operation{Type: OpPortalLogs, SpaceID: args.SpaceID, PortalID: args.PortalID, Agent: args.Agent, Tail: args.Tail, Follow: args.Follow}
		if args.Follow {
			return toolError(call, fmt.Errorf("portal logs must be bounded; follow mode is unsupported for agent planning"))
		}
		return d.portalRead(call, op)
	case ToolPortalInit:
		var args portalInitArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalInit, SpaceID: args.SpaceID, PortalID: args.PortalID, Driver: args.Driver, Preset: args.Preset, Engine: args.Engine, Image: args.Image, ContainerRoot: args.ContainerRoot, DevcontainerPath: args.DevcontainerPath, Repo: args.Repo, Service: args.Service, ComposeFiles: args.ComposeFiles, SyncMode: args.SyncMode})
	case ToolPortalAttach:
		var args portalAttachArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalAttach, SpaceID: args.SpaceID, PortalID: args.PortalID, Driver: args.Driver, Host: args.Host, Port: args.Port, InstanceID: args.InstanceID, Region: args.Region, Profile: args.Profile, SSHUser: args.SSHUser, IdentityPath: args.IdentityPath, RemoteRoot: args.RemoteRoot, SyncMode: args.SyncMode, Preset: args.Preset})
	case ToolPortalConfigure:
		var args portalConfigureArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalConfigure, SpaceID: args.SpaceID, PortalID: args.PortalID, SyncMode: args.SyncMode, ContainerRoot: args.ContainerRoot, RemoteRoot: args.RemoteRoot, Agent: args.Agent, Method: args.Auth})
	case ToolPortalAuthLogin:
		var args portalAuthLoginArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalAuthLogin, SpaceID: args.SpaceID, PortalID: args.PortalID, Provider: args.Provider, Method: args.Method})
	case ToolPortalAuthInherit:
		var args portalAuthInheritArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalAuthInherit, SpaceID: args.SpaceID, PortalID: args.PortalID, Provider: args.Provider, Method: args.Method})
	case ToolPortalAuthRevoke:
		var args portalAuthRevokeArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalAuthRevoke, SpaceID: args.SpaceID, PortalID: args.PortalID, Provider: args.Provider, Target: args.Target})
	case ToolPortalUp:
		var args portalUpArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalUp, SpaceID: args.SpaceID, PortalID: args.PortalID, AttachMode: args.Attach, Workdir: args.Workdir})
	case ToolPortalSync:
		var args portalSyncArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalSync, SpaceID: args.SpaceID, PortalID: args.PortalID, Direction: args.Direction, SyncMode: args.Mode, ReferencesOnly: args.ReferencesOnly, Include: args.Include, Exclude: args.Exclude, Delete: args.Delete, MaxDelete: args.MaxDelete, AllowDirty: args.AllowDirty})
	case ToolPortalSummon:
		var args portalSummonArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalSummon, SpaceID: args.SpaceID, PortalID: args.PortalID, Summoner: args.Summoner, Mode: args.Mode, Permission: args.Permission})
	case ToolPortalDown:
		var args portalDownArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalDown, SpaceID: args.SpaceID, PortalID: args.PortalID, Timeout: args.Timeout, Force: args.Force})
	case ToolPortalDetach:
		var args portalTargetArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.queueOperation(call, Operation{Type: OpPortalDetach, SpaceID: args.SpaceID, PortalID: args.PortalID})
	case ToolPortalDestroyPreview:
		var args portalTargetArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		return d.portalRead(call, Operation{Type: OpPortalDestroyPreview, SpaceID: args.SpaceID, PortalID: args.PortalID})
	case ToolExplainUnsupported:
		var args explainUnsupportedArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		note := args.Request + ": " + args.Reason
		if args.ManualFollowUp != "" {
			note += " Manual follow-up: " + args.ManualFollowUp
		}
		d.Session.Plan.Notes = append(d.Session.Plan.Notes, note)
		d.Session.Status = RunStatusUnsupported
		d.Session.Message = note
		return toolOK(call, map[string]string{"note": note}, "unsupported request recorded")
	case ToolFinish:
		var args finishArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		d.Session.Plan.Summary = args.Summary
		d.Session.Plan.Notes = append(d.Session.Plan.Notes, args.Notes...)
		d.Session.Plan.Warnings = append(d.Session.Plan.Warnings, args.Warnings...)
		if d.Session.Status == "" {
			d.Session.Status = RunStatusPlanReady
		}
		if d.Session.Message == "" {
			d.Session.Message = args.Summary
		}
		d.Session.Finished = true
		return toolOK(call, map[string]any{"summary": args.Summary, "notes": args.Notes, "warnings": args.Warnings}, "planning finished")
	default:
		return toolError(call, fmt.Errorf("unknown tool %q", call.Name))
	}
}

func (d *ToolDispatcher) ask(call ToolCall, args askArgs) ToolResult {
	if len(d.Session.Plan.Operations) > 0 {
		return toolError(call, fmt.Errorf("stave_ask cannot be mixed with queued operations"))
	}
	if len(args.Questions) == 0 || len(args.Questions) > 3 {
		return toolError(call, fmt.Errorf("stave_ask requires one to three questions"))
	}
	for _, question := range args.Questions {
		if question.ID == "" || question.Prompt == "" || question.Type == "" {
			return toolError(call, fmt.Errorf("each question requires id, prompt, and type"))
		}
	}
	d.Session.Status = RunStatusNeedsInput
	d.Session.Message = args.Message
	d.Session.Questions = append([]Question(nil), args.Questions...)
	d.Session.Finished = true
	return toolOK(call, map[string]any{"status": RunStatusNeedsInput, "message": args.Message, "questions": args.Questions}, "needs user input")
}

func (d *ToolDispatcher) portalRead(call ToolCall, op Operation) ToolResult {
	if err := ValidatePlan(d.Config, Plan{Operations: []Operation{op}}); err != nil {
		return toolError(call, err)
	}
	payload, summary, err := d.portalReadPayload(op)
	if err != nil {
		return toolError(call, err)
	}
	result := toolOK(call, payload, summary)
	d.Session.ReadResults = append(d.Session.ReadResults, result)
	return result
}

func (d *ToolDispatcher) portalReadPayload(op Operation) (any, string, error) {
	svc := portal.NewService(d.Config, nil, nil)
	switch op.Type {
	case OpPortalList:
		entries, err := svc.List(op.SpaceID)
		return map[string]any{"portals": entries, "command": EquivalentCommand(op)}, "listed portals", err
	case OpPortalStatus:
		status, err := svc.Status(context.Background(), portal.SelectOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		return map[string]any{"status": status, "command": EquivalentCommand(op)}, "read portal status", err
	case OpPortalDoctor:
		report, err := svc.Doctor(context.Background(), portal.SelectOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		return map[string]any{"doctor": report, "command": EquivalentCommand(op)}, "ran portal doctor", err
	case OpPortalInspect:
		report, err := svc.Inspect(context.Background(), portal.SelectOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		return map[string]any{"inspect": report, "command": EquivalentCommand(op)}, "inspected portal", err
	case OpPortalAuthStatus:
		item, _, err := svc.LoadPortal(portal.SelectOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		if err != nil {
			return nil, "", err
		}
		auth := item.Auth
		if op.Provider != "" && op.Provider != "all" {
			filtered := auth.Providers[:0]
			for _, provider := range auth.Providers {
				if provider.Provider == op.Provider {
					filtered = append(filtered, provider)
				}
			}
			auth.Providers = filtered
		}
		return map[string]any{"auth": auth, "command": EquivalentCommand(op)}, "read portal auth status", nil
	case OpPortalLogs:
		plan, err := svc.PlanLogs(context.Background(), portal.LogsOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Agent: op.Agent, Tail: op.Tail, Follow: op.Follow})
		return map[string]any{"plan": plan, "commands": plan.EquivalentCommands()}, "planned bounded portal logs", err
	case OpPortalDestroyPreview:
		report, err := svc.Inspect(context.Background(), portal.SelectOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		return map[string]any{"destroyPreview": report.DestroyDryRunNotes, "ownedResources": report.OwnedResources, "command": EquivalentCommand(op)}, "previewed portal destroy", err
	default:
		return nil, "", fmt.Errorf("unsupported portal read operation %q", op.Type)
	}
}

func (d *ToolDispatcher) reposList(call ToolCall) ToolResult {
	repos := make([]RepoContext, 0, len(d.Config.Repos))
	names := make([]string, 0, len(d.Config.Repos))
	for name := range d.Config.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repo := d.Config.Repos[name]
		repos = append(repos, RepoContext{Name: name, URL: repo.URL, DefaultBranch: repo.DefaultBranch})
	}
	result := toolOK(call, map[string]any{"repos": repos}, fmt.Sprintf("listed %d registered repos", len(repos)))
	d.Session.ReadResults = append(d.Session.ReadResults, result)
	return result
}

func (d *ToolDispatcher) spaceStatus(ctx context.Context, call ToolCall, spaceID string) ToolResult {
	svc := space.NewService(d.Config, d.Git, io.Discard)
	status, err := svc.Status(ctx, spaceID)
	if err != nil {
		return toolError(call, err)
	}
	payload := statusPayload(svc.SpacePath(spaceID), status)
	result := toolOK(call, payload, fmt.Sprintf("inspected space %s", spaceID))
	d.Session.ReadResults = append(d.Session.ReadResults, result)
	return result
}

func (d *ToolDispatcher) queueOperation(call ToolCall, op Operation) ToolResult {
	next := append(append([]Operation(nil), d.Session.Plan.Operations...), op)
	if err := ValidatePlan(d.Config, Plan{Operations: next}); err != nil {
		return toolError(call, err)
	}
	d.Session.Plan.Operations = next
	return toolOK(call, map[string]any{"operation": op, "command": EquivalentCommand(op)}, "queued "+op.Type)
}

func statusPayload(spacePath string, status space.Status) map[string]any {
	repos := make([]map[string]any, 0, len(status.Repos))
	for _, repo := range status.Repos {
		repos = append(repos, map[string]any{
			"name":          repo.Repo.Name,
			"mode":          repo.Repo.Mode,
			"path":          repo.Repo.Path,
			"base":          repo.Repo.Base,
			"ref":           repo.Repo.Ref,
			"branch":        repo.Repo.Branch,
			"exists":        repo.Exists,
			"dirty":         repo.Dirty,
			"ahead":         repo.Ahead,
			"behind":        repo.Behind,
			"driftError":    repo.DriftError,
			"referenceWarn": repo.ReferenceWarn,
		})
	}
	specPath := ""
	if status.Manifest.SpecPath != "" {
		specPath = filepath.Join(spacePath, status.Manifest.SpecPath)
	}
	return map[string]any{
		"id":       status.Manifest.ID,
		"kind":     status.Manifest.Kind,
		"path":     spacePath,
		"specPath": specPath,
		"repos":    repos,
	}
}

func toolOK(call ToolCall, payload any, summary string) ToolResult {
	return ToolResult{CallID: call.ID, Name: call.Name, Payload: redactAny(payload), Summary: RedactText(summary)}
}

func toolError(call ToolCall, err error) ToolResult {
	message := RedactText(err.Error())
	return ToolResult{CallID: call.ID, Name: call.Name, Error: true, Summary: message, Payload: map[string]string{"error": message}}
}

func (r ToolResult) OutputString() string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(r); err != nil {
		return fmt.Sprintf(`{"error":true,"summary":%q}`, err.Error())
	}
	return b.String()
}

func decodeToolArgs(raw json.RawMessage, dest any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid tool arguments: multiple JSON values")
	}
	return nil
}

type reposSyncArgs struct {
	Repo string `json:"repo"`
}

type spaceStatusArgs struct {
	SpaceID string `json:"space_id"`
}

type spaceSyncArgs struct {
	SpaceID        string `json:"space_id"`
	ReferencesOnly bool   `json:"references_only"`
}

type spaceCreateArgs struct {
	SpaceID    string    `json:"space_id"`
	Kind       string    `json:"kind"`
	SpecPath   string    `json:"spec_path"`
	Edits      []RepoRef `json:"edits"`
	References []RepoRef `json:"references"`
}

type spaceAddArgs struct {
	SpaceID string `json:"space_id"`
	Repo    string `json:"repo"`
	Mode    string `json:"mode"`
	Base    string `json:"base"`
	Ref     string `json:"ref"`
	Branch  string `json:"branch"`
	NoFetch bool   `json:"no_fetch"`
}

type summonArgs struct {
	SpaceID  string `json:"space_id"`
	Summoner string `json:"summoner"`
}

type askArgs struct {
	Message   string     `json:"message"`
	Questions []Question `json:"questions"`
}

type portalListArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
}

type portalTargetArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
}

type portalAuthStatusArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Provider string `json:"provider"`
}

type portalLogsArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Agent    string `json:"agent"`
	Tail     int    `json:"tail"`
	Follow   bool   `json:"follow"`
}

type portalInitArgs struct {
	SpaceID          string   `json:"space_id"`
	PortalID         string   `json:"portal_id"`
	Driver           string   `json:"driver"`
	Preset           string   `json:"preset"`
	Engine           string   `json:"engine"`
	Image            string   `json:"image"`
	ContainerRoot    string   `json:"container_root"`
	DevcontainerPath string   `json:"devcontainer_path"`
	Repo             string   `json:"repo"`
	Service          string   `json:"service"`
	ComposeFiles     []string `json:"compose_files"`
	SyncMode         string   `json:"sync_mode"`
}

type portalAttachArgs struct {
	SpaceID      string `json:"space_id"`
	PortalID     string `json:"portal_id"`
	Driver       string `json:"driver"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	InstanceID   string `json:"instance_id"`
	Region       string `json:"region"`
	Profile      string `json:"profile"`
	SSHUser      string `json:"ssh_user"`
	IdentityPath string `json:"identity_path"`
	RemoteRoot   string `json:"remote_root"`
	SyncMode     string `json:"sync_mode"`
	Preset       string `json:"preset"`
}

type portalConfigureArgs struct {
	SpaceID       string `json:"space_id"`
	PortalID      string `json:"portal_id"`
	SyncMode      string `json:"sync_mode"`
	ContainerRoot string `json:"container_root"`
	RemoteRoot    string `json:"remote_root"`
	Agent         string `json:"agent"`
	Auth          string `json:"auth"`
}

type portalAuthLoginArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Provider string `json:"provider"`
	Method   string `json:"method"`
}

type portalAuthInheritArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Provider string `json:"provider"`
	Method   string `json:"method"`
}

type portalAuthRevokeArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Provider string `json:"provider"`
	Target   string `json:"target"`
}

type portalUpArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Attach   string `json:"attach"`
	Workdir  string `json:"workdir"`
}

type portalSyncArgs struct {
	SpaceID        string   `json:"space_id"`
	PortalID       string   `json:"portal_id"`
	Direction      string   `json:"direction"`
	Mode           string   `json:"mode"`
	ReferencesOnly bool     `json:"references_only"`
	Include        []string `json:"include"`
	Exclude        []string `json:"exclude"`
	Delete         bool     `json:"delete"`
	MaxDelete      int      `json:"max_delete"`
	AllowDirty     bool     `json:"allow_dirty"`
}

type portalSummonArgs struct {
	SpaceID    string `json:"space_id"`
	PortalID   string `json:"portal_id"`
	Summoner   string `json:"summoner"`
	Mode       string `json:"mode"`
	Permission string `json:"permission"`
}

type portalDownArgs struct {
	SpaceID  string `json:"space_id"`
	PortalID string `json:"portal_id"`
	Timeout  int    `json:"timeout"`
	Force    bool   `json:"force"`
}

type explainUnsupportedArgs struct {
	Request        string `json:"request"`
	Reason         string `json:"reason"`
	ManualFollowUp string `json:"manual_follow_up"`
}

type finishArgs struct {
	Summary  string   `json:"summary"`
	Notes    []string `json:"notes"`
	Warnings []string `json:"warnings"`
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func boolSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func stringArraySchema(description string) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": map[string]any{"type": "string"}}
}

func repoRefArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": objectSchema(map[string]any{
			"name": stringSchema("Registered repo name."),
			"ref":  stringSchema("Optional branch/ref."),
		}, []string{"name"}),
	}
}

func portalTargetSchema() map[string]any {
	return objectSchema(map[string]any{
		"space_id":  stringSchema("Existing Stave space id."),
		"portal_id": stringSchema("Optional portal id. Defaults to default."),
	}, []string{"space_id"})
}

func enumSchema(description string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func intSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func questionArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"minItems":    1,
		"maxItems":    3,
		"items": objectSchema(map[string]any{
			"id":          stringSchema("Stable question id."),
			"prompt":      stringSchema("User-facing question."),
			"type":        enumSchema("Question type.", []string{"text", "select", "multiselect", "confirm"}),
			"required":    boolSchema("Whether this answer is required."),
			"options":     stringArraySchema("Allowed options for select-style questions."),
			"description": stringSchema("Optional short explanation."),
		}, []string{"id", "prompt", "type"}),
	}
}
