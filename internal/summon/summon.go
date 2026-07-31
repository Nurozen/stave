package summon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
)

const (
	Codex  = "codex"
	Claude = "claude"
	Cursor = "cursor"
)

type Launcher interface {
	Launch(context.Context, Invocation) error
}

type ExecLauncher struct{}

type Service struct {
	Config      config.Config
	Launcher    Launcher
	Out         io.Writer
	Interactive bool
}

type Options struct {
	SpaceID      string
	Summoner     string
	PrintCommand bool
	// AgentArgs are passed verbatim to the summoned executable before Prompt.
	AgentArgs []string
	// Prompt overrides the generated launch prompt; a skill invocation such
	// as "/pr-teach" launches the agent straight into that skill.
	Prompt string
}

type Invocation struct {
	Summoner string
	Command  string
	Args     []string
	Dir      string
	Prompt   string
}

func (ExecLauncher) Launch(ctx context.Context, invocation Invocation) error {
	cmd := exec.CommandContext(ctx, invocation.Command, invocation.Args...)
	cmd.Dir = invocation.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func NewService(cfg config.Config, launcher Launcher, out io.Writer) Service {
	if launcher == nil {
		launcher = ExecLauncher{}
	}
	return Service{Config: cfg, Launcher: launcher, Out: out, Interactive: true}
}

func (s Service) Summon(ctx context.Context, opts Options) error {
	invocation, err := s.Invocation(opts)
	if err != nil {
		return err
	}
	if opts.PrintCommand || !s.Interactive {
		s.printf("%s\n", CommandString(invocation))
		return nil
	}
	if err := s.Launcher.Launch(ctx, invocation); err != nil {
		return fmt.Errorf("launch %s: %w", invocation.Summoner, err)
	}
	return nil
}

func (s Service) Invocation(opts Options) (Invocation, error) {
	if err := config.ValidateName("space id", opts.SpaceID); err != nil {
		return Invocation{}, err
	}
	spacePath := filepath.Join(s.Config.AgentWorkDir, opts.SpaceID)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return Invocation{}, err
	}
	summoner := ResolveName(s.Config, opts.Summoner)
	prompt := opts.Prompt
	if prompt == "" {
		prompt = defaultPrompt(spacePath, manifest.SpecPath, manifest.Kind, summoner, manifest.Saga != nil, manifest.Memories)
	}
	return BuildInvocation(s.Config, spacePath, summoner, prompt, opts.AgentArgs...)
}

func BuildInvocation(cfg config.Config, spacePath string, summoner string, prompt string, agentArgs ...string) (Invocation, error) {
	if err := ValidateSummoner(summoner); err != nil {
		return Invocation{}, err
	}
	command := cfg.Summon.Commands[summoner]
	if strings.TrimSpace(command) == "" {
		return Invocation{}, fmt.Errorf("summoner %q has no configured command", summoner)
	}
	invocation := Invocation{Summoner: summoner, Command: command, Dir: spacePath, Prompt: prompt}
	switch summoner {
	case Codex:
		invocation.Args = append([]string{"--cd", spacePath}, agentArgs...)
	case Claude, Cursor:
		invocation.Args = append([]string{}, agentArgs...)
	}
	invocation.Args = append(invocation.Args, prompt)
	return invocation, nil
}

func ResolveName(cfg config.Config, requested string) string {
	if requested != "" {
		return requested
	}
	if cfg.Summon.Default != "" {
		return cfg.Summon.Default
	}
	return Codex
}

func ValidateSummoner(name string) error {
	switch name {
	case Codex, Claude, Cursor:
		return nil
	default:
		return fmt.Errorf("summoner must be codex, claude, or cursor")
	}
}

func Prompt(spacePath string, specPath string) string {
	return PromptForKind(spacePath, specPath, "")
}

// SagaPromptForPlan renders the prompt a newly-created saga will receive.
// Unlike defaultPrompt it does not inspect the filesystem: saga create
// previews run before the manifest and embedded skill exist, but the live
// path guarantees that the skill is installed before summoning.
func SagaPromptForPlan(spacePath, specPath, summoner string, memories []space.MemoryManifest) string {
	if summoner == Claude {
		return skillPrompt(SagaSkillName, memories)
	}
	return sagaStancePrompt(spacePath, specPath, memories)
}

// ReviewSkillName is the project-level Claude Code skill that stave review
// installs into each review space (embedded in the stave binary).
const ReviewSkillName = "pr-teach"

// SagaSkillName is the project-level Claude Code skill that saga creation
// installs into each saga space (embedded in the stave binary).
const SagaSkillName = "stave-saga"

// defaultPrompt picks the launch prompt for a space: Claude sessions in
// review and saga spaces launch straight into the embedded skill when the
// space carries it, so the guided loop starts without any typing. A saga is
// detected by its manifest saga block, not Kind alone: a kind:saga space
// without a roster gets the plain default prompt.
func defaultPrompt(spacePath, specPath, kind, summoner string, hasSaga bool, memories []space.MemoryManifest) string {
	if hasSaga {
		if summoner == Claude {
			skillPath := filepath.Join(spacePath, ".claude", "skills", SagaSkillName, "SKILL.md")
			if _, err := os.Stat(skillPath); err == nil {
				return skillPrompt(SagaSkillName, memories)
			}
		}
		return sagaStancePrompt(spacePath, specPath, memories)
	}
	if kind == "review" && summoner == Claude {
		skillPath := filepath.Join(spacePath, ".claude", "skills", ReviewSkillName, "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			return skillPrompt(ReviewSkillName, memories)
		}
	}
	return PromptForKindWithMemories(spacePath, specPath, kind, memories)
}

// skillPrompt launches an embedded skill. When the space has memory attached,
// the memory bullets ride along after a blank line as skill arguments
// (slash-command-with-arguments is safe; the skill ignores extra context) —
// summoned agents must still learn memory exists.
func skillPrompt(name string, memories []space.MemoryManifest) string {
	if len(memories) == 0 {
		return "/" + name
	}
	var b strings.Builder
	b.WriteString("/" + name + "\n")
	for _, mem := range memories {
		b.WriteString("\n")
		b.WriteString(memoryBullet(mem))
	}
	return b.String()
}

// sagaStancePrompt is the coordinator prompt for saga spaces when the
// embedded saga skill cannot run (non-Claude summoners, or a saga space that
// never had the skill installed). The saga holds no editable worktrees of its
// own, so the usual editable/reference Focus bullets are replaced with the
// coordination loop.
func sagaStancePrompt(spacePath string, specPath string, memories []space.MemoryManifest) string {
	var b strings.Builder
	b.WriteString("Using a team of agents, familiarize yourself with this saga workspace.\n\n")
	b.WriteString("Focus on:\n")
	b.WriteString("- AGENTS.md for the member spaces and their merge order.\n")
	b.WriteString("- Running stave saga status " + filepath.Base(spacePath) + " --json for live member state.\n")
	if specPath != "" {
		b.WriteString("- The spec material at ")
		b.WriteString(resolveSpecPath(spacePath, specPath))
		b.WriteString(".\n")
	}
	for _, mem := range memories {
		b.WriteString(memoryBullet(mem))
		b.WriteString("\n")
	}
	b.WriteString("\nThis is a saga space: it coordinates member spaces rather than holding work of its own. Member edits happen through separate per-member summons, and never rebase or retarget a member unless the user asks.")
	return b.String()
}

// memoryBullet is the shared memory-attachment line (AGENTS.md sentence).
func memoryBullet(mem space.MemoryManifest) string {
	return "- Persistent memory is available via the context-marmot MCP tools (den: " + mem.ID + ")."
}

// PromptForKind tailors the launch prompt to the space's purpose: a review
// space primes the agent to assess a change rather than implement one.
func PromptForKind(spacePath string, specPath string, kind string) string {
	return PromptForKindWithMemories(spacePath, specPath, kind, nil)
}

// PromptForKindWithMemories is PromptForKind plus optional memory attachment lines.
func PromptForKindWithMemories(spacePath string, specPath string, kind string, memories []space.MemoryManifest) string {
	var b strings.Builder
	b.WriteString("Using a team of agents, familiarize yourself with this workspace.\n\n")
	b.WriteString("Focus on:\n")
	b.WriteString("- The editable repositories at the top level.\n")
	b.WriteString("- The reference repositories under references/ as read-only context.\n")
	if specPath != "" {
		b.WriteString("- The spec material at ")
		b.WriteString(resolveSpecPath(spacePath, specPath))
		b.WriteString(".\n")
	}
	for _, mem := range memories {
		b.WriteString(memoryBullet(mem))
		b.WriteString("\n")
	}
	if kind == "review" {
		b.WriteString("\nThis is a review space: the worktree is checked out at the change under review and the spec records its context (a pull request's metadata and diff commands). Help the user assess the change — treat the author's description as claims to verify, not facts — and do not modify the code unless the user explicitly asks.")
	} else {
		b.WriteString("\nKeep in mind how the codebase relates to the spec, then propose a concise implementation plan before making changes.")
	}
	return b.String()
}

func CommandString(invocation Invocation) string {
	parts := []string{"cd", shellQuote(invocation.Dir), "&&", shellQuote(invocation.Command)}
	for _, arg := range invocation.Args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func (s Service) printf(format string, args ...any) {
	if s.Out != nil {
		fmt.Fprintf(s.Out, format, args...)
	}
}

func resolveSpecPath(spacePath string, specPath string) string {
	if filepath.IsAbs(specPath) {
		return specPath
	}
	return filepath.Join(spacePath, specPath)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool { return !isSafeShellRune(r) }) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isSafeShellRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' || r == ',' || r == '@' || r == '%' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}
