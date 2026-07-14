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
		prompt = defaultPrompt(spacePath, manifest.SpecPath, manifest.Kind, summoner, manifest.Memories)
	}
	return BuildInvocation(s.Config, spacePath, summoner, prompt)
}

func BuildInvocation(cfg config.Config, spacePath string, summoner string, prompt string) (Invocation, error) {
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
		invocation.Args = []string{"--cd", spacePath, prompt}
	case Claude, Cursor:
		invocation.Args = []string{prompt}
	}
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

// ReviewSkillName is the project-level Claude Code skill that stave review
// installs into each review space (embedded in the stave binary).
const ReviewSkillName = "pr-teach"

// defaultPrompt picks the launch prompt for a space: Claude sessions in
// review spaces launch straight into the embedded review skill when the
// space carries it, so the guided review loop starts without any typing.
func defaultPrompt(spacePath, specPath, kind, summoner string, memories []space.MemoryManifest) string {
	if kind == "review" && summoner == Claude {
		skillPath := filepath.Join(spacePath, ".claude", "skills", ReviewSkillName, "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			return "/" + ReviewSkillName
		}
	}
	return PromptForKindWithMemories(spacePath, specPath, kind, memories)
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
		b.WriteString("- Persistent memory is available via the context-marmot MCP tools (den: ")
		b.WriteString(mem.ID)
		b.WriteString(").\n")
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
