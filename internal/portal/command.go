package portal

import (
	"strings"
)

type Command struct {
	Program             string
	Args                []string
	Dir                 string
	Env                 []string
	ContinueOnError     bool
	RunIfPreviousFailed bool
}

func (c Command) String() string {
	parts := make([]string, 0, len(c.Env)+2+len(c.Args))
	for _, env := range c.Env {
		parts = append(parts, quoteShell(env))
	}
	if c.Dir != "" {
		parts = append(parts, "cd", quoteShell(c.Dir), "&&")
	}
	if c.Program != "" {
		parts = append(parts, quoteShell(c.Program))
	}
	for _, arg := range c.Args {
		parts = append(parts, quoteShell(arg))
	}
	return strings.Join(parts, " ")
}

func quoteShell(value string) string {
	if value == "" {
		return "''"
	}
	if isShellSafe(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isShellSafe(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

type Plan struct {
	Operation       string
	DryRun          bool
	Mutates         bool
	Summary         string
	Commands        []Command
	ManifestPreview string
	Diagnostics     []Diagnostic
}

func (p Plan) EquivalentCommands() []string {
	commands := make([]string, 0, len(p.Commands))
	for _, command := range p.Commands {
		commands = append(commands, command.String())
	}
	return commands
}

func command(program string, args ...string) Command {
	return Command{Program: program, Args: args}
}
