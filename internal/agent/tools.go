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
	"github.com/Nurozen/stave/internal/space"
)

const (
	ToolReposList          = "stave_repos_list"
	ToolReposSync          = "stave_repos_sync"
	ToolSpaceStatus        = "stave_space_status"
	ToolSpaceSync          = "stave_space_sync"
	ToolSpaceCreate        = "stave_space_create"
	ToolSpaceAdd           = "stave_space_add"
	ToolExplainUnsupported = "stave_explain_unsupported"
	ToolFinish             = "stave_finish"
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
	Finished    bool
}

func NewToolSession() *ToolSession {
	return &ToolSession{Plan: Plan{Operations: []Operation{}}}
}

func (s *ToolSession) RunResult() RunResult {
	plan := s.Plan
	commands := plan.Commands()
	return RunResult{
		Plan:        plan,
		Commands:    commands,
		ToolCalls:   append([]ToolCallRecord(nil), s.ToolCalls...),
		ReadResults: append([]ToolResultRecord(nil), s.ReadResults...),
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
	return []ToolDefinition{
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
			Name:        ToolExplainUnsupported,
			Category:    ToolCategoryControl,
			Description: "Record that the user requested an operation Stave Agent will not execute in v1, such as destroy, archive, remove, reset, delete, push, PR creation, issue tracker updates, or arbitrary shell commands. Use this instead of inventing unsupported tools.",
			Parameters: objectSchema(map[string]any{
				"request":          stringSchema("The unsupported user request."),
				"reason":           stringSchema("Why Stave Agent will not execute it."),
				"manual_follow_up": stringSchema("Optional manual follow-up command or guidance."),
			}, []string{"request", "reason"}),
		},
		{
			Name:        ToolFinish,
			Category:    ToolCategoryControl,
			Description: "Finish planning after all needed read tools have been used and all proposed executable operations have been queued. Provide the final user-facing summary, assumptions, and any warnings. Use this exactly once when the plan is complete.",
			Parameters: objectSchema(map[string]any{
				"summary":  stringSchema("Short user-facing summary of the plan."),
				"notes":    stringArraySchema("Optional assumptions or notes."),
				"warnings": stringArraySchema("Optional warnings."),
			}, []string{"summary"}),
		},
	}
}

func (d *ToolDispatcher) Dispatch(ctx context.Context, call ToolCall) ToolResult {
	if d.Session == nil {
		d.Session = NewToolSession()
	}
	d.Session.ToolCalls = append(d.Session.ToolCalls, ToolCallRecord{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)})
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
		return toolOK(call, map[string]string{"note": note}, "unsupported request recorded")
	case ToolFinish:
		var args finishArgs
		if err := decodeToolArgs(call.Arguments, &args); err != nil {
			return toolError(call, err)
		}
		d.Session.Plan.Summary = args.Summary
		d.Session.Plan.Notes = append(d.Session.Plan.Notes, args.Notes...)
		d.Session.Plan.Warnings = append(d.Session.Plan.Warnings, args.Warnings...)
		d.Session.Finished = true
		return toolOK(call, map[string]any{"summary": args.Summary, "notes": args.Notes, "warnings": args.Warnings}, "planning finished")
	default:
		return toolError(call, fmt.Errorf("unknown tool %q", call.Name))
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
	return ToolResult{CallID: call.ID, Name: call.Name, Payload: payload, Summary: summary}
}

func toolError(call ToolCall, err error) ToolResult {
	return ToolResult{CallID: call.ID, Name: call.Name, Error: true, Summary: err.Error(), Payload: map[string]string{"error": err.Error()}}
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
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
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
