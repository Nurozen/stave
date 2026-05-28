package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nurozen/stave/internal/space"
)

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"

	OpSpaceCreate = "space_create"
	OpSpaceAdd    = "space_add"
	OpSpaceSync   = "space_sync"
	OpSpaceStatus = "space_status"
	OpReposList   = "repos_list"
	OpReposSync   = "repos_sync"
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
	Type           string    `json:"type"`
	SpaceID        string    `json:"space_id,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	SpecPath       string    `json:"spec_path,omitempty"`
	Edits          []RepoRef `json:"edits,omitempty"`
	References     []RepoRef `json:"references,omitempty"`
	Repo           string    `json:"repo,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Base           string    `json:"base,omitempty"`
	Ref            string    `json:"ref,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	NoFetch        bool      `json:"no_fetch,omitempty"`
	ReferencesOnly bool      `json:"references_only,omitempty"`
	Unsupported    string    `json:"unsupported,omitempty"`
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
	ID       string             `json:"id"`
	Kind     string             `json:"kind,omitempty"`
	SpecPath string             `json:"spec_path,omitempty"`
	Repos    []SpaceRepoContext `json:"repos"`
}

type SpaceRepoContext struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Path   string `json:"path"`
	Base   string `json:"base,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type ExecutionResult struct {
	Operation Operation `json:"operation"`
	Command   string    `json:"command"`
	Executed  bool      `json:"executed"`
	Message   string    `json:"message,omitempty"`
}

type RunResult struct {
	Plan        Plan               `json:"plan"`
	Commands    []string           `json:"commands"`
	ToolCalls   []ToolCallRecord   `json:"toolCalls,omitempty"`
	ReadResults []ToolResultRecord `json:"readResults,omitempty"`
	Results     []ExecutionResult  `json:"results,omitempty"`
	Executed    bool               `json:"executed"`
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
	case OpReposSync:
		if op.Repo != "" {
			return strings.Join([]string{"stave", "repos", "sync", shellQuote(op.Repo)}, " ")
		}
		return "stave repos sync"
	default:
		if op.Unsupported != "" {
			return "# unsupported: " + op.Unsupported
		}
		return "# unsupported operation: " + op.Type
	}
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
	if strings.ContainsAny(value, " \t\n'\"$`\\") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}
