package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nurozen/stave/internal/fsio"
	"github.com/spf13/cobra"
)

const (
	shellChdirFDEnv      = "STAVE_CD_FD"
	inscribeShellBegin   = "# >>> stave shell integration >>>"
	inscribeShellEnd     = "# <<< stave shell integration <<<"
	defaultShellFileMode = 0o644
)

func (a *app) inscribeCommand() *cobra.Command {
	cmd := groupCommand("inscribe", "Persist Stave integrations in local tool configuration")
	cmd.AddCommand(a.inscribeShellCommand())
	return cmd
}

func (a *app) inscribeShellCommand() *cobra.Command {
	var zsh, bash, dryRun bool
	var rcPath string
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Configure shell integration for automatic space directory entry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			shell, err := selectedShell(zsh, bash)
			if err != nil {
				return err
			}
			path := rcPath
			if path == "" {
				path, err = shellConfigPath(shell)
				if err != nil {
					return err
				}
			} else {
				path, err = filepath.Abs(path)
				if err != nil {
					return err
				}
			}
			result, err := inscribeShellConfig(path, shell, dryRun)
			if err != nil {
				return err
			}
			switch {
			case !result.Changed:
				fmt.Fprintf(cmd.OutOrStdout(), "%s shell integration is already inscribed in %s\n", shell, path)
			case dryRun:
				action := "update"
				if result.Created {
					action = "create"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: inscribe %s shell integration in %s (%s file)\n", shell, path, action)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "inscribed %s shell integration in %s\n", shell, path)
				fmt.Fprintln(cmd.OutOrStdout(), "Restart the shell or source the file to activate it in the current session.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&zsh, "zsh", false, "configure zsh via $ZDOTDIR/.zshrc or ~/.zshrc")
	cmd.Flags().BoolVar(&bash, "bash", false, "configure bash via ~/.bashrc")
	cmd.Flags().StringVar(&rcPath, "rc", "", "override the shell startup file path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the target without changing it")
	return cmd
}

type inscribeResult struct {
	Changed bool
	Created bool
}

func selectedShell(zsh, bash bool) (string, error) {
	if zsh == bash {
		return "", fmt.Errorf("select exactly one shell with --zsh or --bash")
	}
	if zsh {
		return "zsh", nil
	}
	return "bash", nil
}

func shellConfigPath(shell string) (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	var path string
	switch shell {
	case "zsh":
		root := os.Getenv("ZDOTDIR")
		if root == "" {
			root = home
		}
		path = filepath.Join(root, ".zshrc")
	case "bash":
		path = filepath.Join(home, ".bashrc")
	default:
		return "", fmt.Errorf("unsupported shell %q; use bash or zsh", shell)
	}
	return filepath.Abs(path)
}

func inscribeShellConfig(path, shell string, dryRun bool) (inscribeResult, error) {
	path, existing, perm, created, err := loadShellConfig(path)
	if err != nil {
		return inscribeResult{}, err
	}
	updated, err := updateManagedShellBlock(existing, shell)
	if err != nil {
		return inscribeResult{}, fmt.Errorf("update %s: %w", path, err)
	}
	result := inscribeResult{Changed: string(updated) != string(existing), Created: created}
	if !result.Changed {
		return result, nil
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return inscribeResult{}, err
	}
	if !dirInfo.IsDir() {
		return inscribeResult{}, fmt.Errorf("shell config parent %s is not a directory", filepath.Dir(path))
	}
	if dryRun {
		return result, nil
	}
	if err := fsio.WriteFileAtomic(path, updated, perm); err != nil {
		return inscribeResult{}, err
	}
	return result, nil
}

func loadShellConfig(path string) (string, []byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil, defaultShellFileMode, true, nil
	}
	if err != nil {
		return "", nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, 0, false, fmt.Errorf("resolve shell config symlink %s: %w", path, err)
		}
		path = resolved
		info, err = os.Stat(path)
		if err != nil {
			return "", nil, 0, false, err
		}
	}
	if !info.Mode().IsRegular() {
		return "", nil, 0, false, fmt.Errorf("shell config %s is not a regular file", path)
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return "", nil, 0, false, err
	}
	return path, existing, info.Mode().Perm(), false, nil
}

func updateManagedShellBlock(existing []byte, shell string) ([]byte, error) {
	block := inscribeShellBlock(shell)
	text := string(existing)
	begins := exactMarkerLines(text, inscribeShellBegin)
	ends := exactMarkerLines(text, inscribeShellEnd)
	if len(begins) != strings.Count(text, inscribeShellBegin) || len(ends) != strings.Count(text, inscribeShellEnd) {
		return nil, fmt.Errorf("managed shell integration marker text must appear on a standalone line")
	}
	if len(begins) == 0 && len(ends) == 0 {
		if migrated, found, err := replaceLegacyShellInit(text, shell, block); err != nil {
			return nil, err
		} else if found {
			return []byte(migrated), nil
		}
		if containsUnmanagedShellInit(text) {
			return nil, fmt.Errorf("an unmanaged shell-init invocation already exists; remove it or replace it with the Stave managed block")
		}
		if text == "" {
			return []byte(block + "\n"), nil
		}
		separator := "\n\n"
		if strings.HasSuffix(text, "\n\n") {
			separator = ""
		} else if strings.HasSuffix(text, "\n") {
			separator = "\n"
		}
		return []byte(text + separator + block + "\n"), nil
	}
	if len(begins) != 1 || len(ends) != 1 {
		return nil, fmt.Errorf("managed shell integration markers are malformed; expected one begin and one end marker")
	}
	if ends[0].start < begins[0].start {
		return nil, fmt.Errorf("managed shell integration markers are malformed; end marker precedes begin marker")
	}
	prefix := text[:begins[0].start]
	suffix := text[ends[0].end:]
	if containsUnmanagedShellInit(prefix + suffix) {
		return nil, fmt.Errorf("an unmanaged shell-init invocation exists outside the managed block; remove it before inscribing")
	}
	return []byte(prefix + block + suffix), nil
}

type textRange struct {
	start int
	end   int
}

func exactMarkerLines(text, marker string) []textRange {
	var matches []textRange
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		content := strings.TrimSuffix(line, "\n")
		content = strings.TrimSuffix(content, "\r")
		if strings.TrimSpace(content) == marker {
			matches = append(matches, textRange{start: offset, end: offset + len(content)})
		}
		offset += len(line)
	}
	return matches
}

func replaceLegacyShellInit(text, shell, block string) (string, bool, error) {
	lines := strings.SplitAfter(text, "\n")
	match := -1
	for i, line := range lines {
		if !isLegacyShellInitLine(line, shell) {
			continue
		}
		if match >= 0 {
			return "", false, fmt.Errorf("multiple legacy shell integration lines found; remove duplicates before inscribing")
		}
		match = i
	}
	if match < 0 {
		return text, false, nil
	}
	lineEnding := ""
	if strings.HasSuffix(lines[match], "\n") {
		lineEnding = "\n"
	}
	lines[match] = block + lineEnding
	return strings.Join(lines, ""), true, nil
}

func isLegacyShellInitLine(line, shell string) bool {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	line = strings.TrimRight(line, " \t")
	for _, command := range []string{
		"eval \"$(stave shell-init " + shell + ")\"",
		"eval \"$(command stave shell-init " + shell + ")\"",
	} {
		if line == command {
			return true
		}
		if strings.HasPrefix(line, command) {
			remainder := strings.TrimPrefix(line, command)
			if len(remainder) > 0 && (remainder[0] == ' ' || remainder[0] == '\t') &&
				strings.TrimSpace(remainder) == "# use bash when appropriate" {
				return true
			}
		}
	}
	return false
}

func containsUnmanagedShellInit(text string) bool {
	return strings.Contains(text, "stave shell-init")
}

func inscribeShellBlock(shell string) string {
	return strings.Join([]string{
		inscribeShellBegin,
		"# Managed by `stave inscribe shell --" + shell + "`.",
		"eval \"$(command stave shell-init " + shell + ")\"",
		inscribeShellEnd,
	}, "\n")
}

func (a *app) shellInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "shell-init <shell>",
		Short: "Print shell integration for automatic space directory entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := shellInitScript(args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), script)
			return err
		},
	}
}

func shellInitScript(shell string) (string, error) {
	switch shell {
	case "bash", "zsh":
		return `stave() {
  local _stave_cd_file _stave_status _stave_cd_status _stave_cd_target _stave_saved_traps _stave_exit_status
  _stave_cd_file="$(mktemp "${TMPDIR:-/tmp}/stave-cd.XXXXXXXXXX")" || return 1
  trap > "$_stave_cd_file"
  _stave_saved_traps="$(command cat "$_stave_cd_file")"
  : > "$_stave_cd_file"
  trap '_stave_exit_status=$?; command rm -f -- "$_stave_cd_file"; trap - HUP INT TERM EXIT; if [ -n "$_stave_saved_traps" ]; then eval "$_stave_saved_traps"; fi; exit "$_stave_exit_status"' EXIT
  trap 'command rm -f -- "$_stave_cd_file"; trap - HUP INT TERM EXIT; if [ -n "$_stave_saved_traps" ]; then eval "$_stave_saved_traps"; fi; kill -HUP "$$"' HUP
  trap 'command rm -f -- "$_stave_cd_file"; trap - HUP INT TERM EXIT; if [ -n "$_stave_saved_traps" ]; then eval "$_stave_saved_traps"; fi; kill -INT "$$"' INT
  trap 'command rm -f -- "$_stave_cd_file"; trap - HUP INT TERM EXIT; if [ -n "$_stave_saved_traps" ]; then eval "$_stave_saved_traps"; fi; kill -TERM "$$"' TERM
  STAVE_CD_FD=3 command stave "$@" 3>"$_stave_cd_file"
  _stave_status=$?
  if IFS= read -r _stave_cd_target < "$_stave_cd_file" && [ -n "$_stave_cd_target" ]; then
    builtin cd -- "$_stave_cd_target"
    _stave_cd_status=$?
    if [ "$_stave_status" -eq 0 ] && [ "$_stave_cd_status" -ne 0 ]; then
      _stave_status=$_stave_cd_status
    fi
  fi
  command rm -f -- "$_stave_cd_file"
  trap - HUP INT TERM EXIT
  if [ -n "$_stave_saved_traps" ]; then
    eval "$_stave_saved_traps"
  fi
  return "$_stave_status"
}
`, nil
	default:
		return "", fmt.Errorf("unsupported shell %q; use bash or zsh", shell)
	}
}

// captureShellChdirHandoff retains the inherited descriptor in app state and
// removes it from the environment before Stave launches any subprocesses.
func (a *app) captureShellChdirHandoff() error {
	rawFD := os.Getenv(shellChdirFDEnv)
	if rawFD == "" {
		return nil
	}
	if err := os.Unsetenv(shellChdirFDEnv); err != nil {
		return fmt.Errorf("clear %s: %w", shellChdirFDEnv, err)
	}
	fd, err := strconv.Atoi(rawFD)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid %s descriptor %q", shellChdirFDEnv, rawFD)
	}
	a.shellChdirFD = fd
	return nil
}

// requestShellChdir asks an installed shell wrapper to enter path after Stave
// exits. A numeric inherited descriptor avoids trusting an environment-provided
// file path and leaves ordinary, non-integrated CLI use unchanged.
func (a *app) requestShellChdir(path string) error {
	fd := a.shellChdirFD
	if fd == 0 {
		return nil
	}
	a.shellChdirFD = 0
	file := os.NewFile(uintptr(fd), "stave shell directory handoff")
	if file == nil {
		return fmt.Errorf("open %s descriptor %d", shellChdirFDEnv, fd)
	}
	if _, err := fmt.Fprintln(file, path); err != nil {
		_ = file.Close()
		return fmt.Errorf("write shell directory handoff: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close shell directory handoff: %w", err)
	}
	return nil
}
