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
	return BuildInvocation(s.Config, spacePath, ResolveName(s.Config, opts.Summoner), Prompt(spacePath, manifest.SpecPath))
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
	b.WriteString("\nKeep in mind how the codebase relates to the spec, then propose a concise implementation plan before making changes.")
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
