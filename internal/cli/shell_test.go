package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIInscribeZshPreservesContentModeAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	path := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(path, []byte("export EXISTING=value"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, "inscribe", "shell", "--zsh")
	if !strings.Contains(out, "inscribed zsh shell integration") || !strings.Contains(out, path) {
		t.Fatalf("inscribe output = %s", out)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export EXISTING=value\n\n",
		inscribeShellBegin,
		`eval "$(command stave shell-init zsh)"`,
		inscribeShellEnd,
	} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("inscribed .zshrc missing %q:\n%s", want, first)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".zshrc mode = %o, want 600", info.Mode().Perm())
	}

	secondOut := runCLI(t, "inscribe", "shell", "--zsh")
	if !strings.Contains(secondOut, "already inscribed") {
		t.Fatalf("second inscribe output = %s", secondOut)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatalf("second inscribe changed file:\nfirst=%q\nsecond=%q", first, second)
	}
}

func TestCLIInscribeShellSelectionPathsAndDryRun(t *testing.T) {
	home := t.TempDir()
	zdotdir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", zdotdir)

	if _, err := runCLIError(t, nil, "inscribe", "shell"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing shell selection error = %v", err)
	}
	if _, err := runCLIError(t, nil, "inscribe", "shell", "--zsh", "--bash"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("conflicting shell selection error = %v", err)
	}
	if _, err := runCLIError(t, nil, "inscribe", "shell", "--zsh=false"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("false shell selection error = %v", err)
	}
	if _, err := runCLIError(t, nil, "inscribe", "shell", "--zsh", "extra"); err == nil {
		t.Fatal("inscribe accepted a positional argument")
	}

	zshPath := filepath.Join(zdotdir, ".zshrc")
	dryOut := runCLI(t, "inscribe", "shell", "--zsh", "--dry-run")
	if !strings.Contains(dryOut, "dry-run: inscribe zsh") || !strings.Contains(dryOut, zshPath) {
		t.Fatalf("dry-run output = %s", dryOut)
	}
	if _, err := os.Stat(zshPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote .zshrc: %v", err)
	}

	runCLI(t, "inscribe", "shell", "--bash")
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err != nil {
		t.Fatalf("bash inscription missing: %v", err)
	}
	if _, err := os.Stat(zshPath); !os.IsNotExist(err) {
		t.Fatalf("bash inscription incorrectly used ZDOTDIR: %v", err)
	}
}

func TestCLIInscribeShellRCOverrideLegacyMigrationAndSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "zshrc")
	legacy := "export BEFORE=1\n" + `eval "$(stave shell-init zsh)"  # use bash when appropriate` + "\nexport AFTER=1\n"
	if err := os.WriteFile(target, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	runCLI(t, "inscribe", "shell", "--zsh")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("inscribe replaced the .zshrc symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "use bash when appropriate") || strings.Count(string(got), inscribeShellBegin) != 1 {
		t.Fatalf("legacy line was not migrated cleanly:\n%s", got)
	}
	if !strings.Contains(string(got), "export BEFORE=1") || !strings.Contains(string(got), "export AFTER=1") {
		t.Fatalf("migration lost existing content:\n%s", got)
	}

	override := filepath.Join(dotfiles, "custom-zshrc")
	runCLI(t, "inscribe", "shell", "--zsh", "--rc", override)
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("--rc target missing: %v", err)
	}
}

func TestCLIInscribeRejectsMalformedOrUnmanagedIntegration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: "missing end", content: inscribeShellBegin + "\nvalue\n"},
		{name: "unmanaged", content: "eval $(stave shell-init zsh --other)\n"},
		{name: "other shell", content: `eval "$(stave shell-init bash)"` + "\n"},
		{name: "conditional legacy", content: "if true; then\n  " + `eval "$(stave shell-init zsh)"` + "\nfi\n"},
		{name: "legacy outside managed block", content: inscribeShellBlock("zsh") + "\n" + `eval "$(stave shell-init zsh)"` + "\n"},
		{name: "quoted markers", content: `echo "` + inscribeShellBegin + `"` + "\n" + `echo "` + inscribeShellEnd + `"` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", "")
			path := filepath.Join(home, ".zshrc")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := runCLIError(t, nil, "inscribe", "shell", "--zsh"); err == nil {
				t.Fatal("malformed integration was accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.content {
				t.Fatalf("rejected inscription changed file: got %q want %q", got, tc.content)
			}
		})
	}
}

func TestCLIInscribeMigratesDocumentedBashLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".bashrc")
	legacy := `eval "$(stave shell-init bash)"  # use bash when appropriate` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "inscribe", "shell", "--bash")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "use bash when appropriate") || strings.Count(string(got), inscribeShellBegin) != 1 {
		t.Fatalf("documented bash line was not migrated cleanly:\n%s", got)
	}
}

func TestCLIInscribeDryRunValidatesTargetParent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZDOTDIR", "")
	path := filepath.Join(t.TempDir(), "missing", ".zshrc")
	if _, err := runCLIError(t, nil, "inscribe", "shell", "--zsh", "--rc", path, "--dry-run"); err == nil {
		t.Fatal("dry-run accepted a target with a missing parent")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target: %v", err)
	}
}

func TestCLIShellInit(t *testing.T) {
	out := runCLI(t, "shell-init", "zsh")
	for _, want := range []string{"stave()", "STAVE_CD_FD=3", "builtin cd --"} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell-init output missing %q:\n%s", want, out)
		}
	}
	if _, err := runCLIError(t, nil, "shell-init", "fish"); err == nil || !strings.Contains(err.Error(), "unsupported shell") {
		t.Fatalf("unsupported shell error = %v", err)
	}
}

func TestShellInitChangesCallingShellDirectory(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			initScript, err := shellInitScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			fakeStave := filepath.Join(binDir, "stave")
			if err := os.WriteFile(fakeStave, []byte("#!/bin/sh\nprintf '%s\\n' \"$STAVE_TEST_TARGET\" >&3\nif [ -n \"$STAVE_TEST_FAIL\" ]; then exit 9; fi\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			start := t.TempDir()
			target := t.TempDir()
			cmd := exec.Command(binary, "-c", `eval "$STAVE_TEST_INIT"; stave; pwd`)
			cmd.Dir = start
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"STAVE_TEST_INIT="+initScript,
				"STAVE_TEST_TARGET="+target,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell hook error = %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != target {
				t.Fatalf("pwd after wrapped stave = %q, want %q", got, target)
			}

			failed := exec.Command(binary, "-c", `eval "$STAVE_TEST_INIT"; stave; stave_rc=$?; printf '%s\n' "$stave_rc"; pwd`)
			failed.Dir = start
			failed.Env = append(cmd.Env, "STAVE_TEST_FAIL=1")
			failedOut, err := failed.CombinedOutput()
			if err != nil {
				t.Fatalf("failed wrapped command harness error = %v\n%s", err, failedOut)
			}
			wantFailed := "9\n" + target
			if got := strings.TrimSpace(string(failedOut)); got != wantFailed {
				t.Fatalf("failed wrapped command output = %q, want %q", got, wantFailed)
			}

			trapMarker := filepath.Join(t.TempDir(), "exit-trap")
			preserved := exec.Command(binary, "-c", `trap 'printf preserved > "$STAVE_TRAP_MARKER"' EXIT; eval "$STAVE_TEST_INIT"; stave`)
			preserved.Dir = start
			preserved.Env = append(cmd.Env, "STAVE_TRAP_MARKER="+trapMarker)
			if out, err := preserved.CombinedOutput(); err != nil {
				t.Fatalf("wrapped command with existing trap error = %v\n%s", err, out)
			}
			marker, err := os.ReadFile(trapMarker)
			if err != nil {
				t.Fatal(err)
			}
			if string(marker) != "preserved" {
				t.Fatalf("restored EXIT trap wrote %q", marker)
			}
		})
	}
}

func TestShellInitCleansHandoffFileWhenShellExits(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			initScript, err := shellInitScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			fakeStave := filepath.Join(binDir, "stave")
			if err := os.WriteFile(fakeStave, []byte("#!/bin/sh\nkill -TERM \"$PPID\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			tmpDir := t.TempDir()
			cmd := exec.Command(binary, "-c", `eval "$STAVE_TEST_INIT"; stave`)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"STAVE_TEST_INIT="+initScript,
				"TMPDIR="+tmpDir,
			)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("terminated shell exited successfully:\n%s", out)
			}
			entries, err := os.ReadDir(tmpDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("shell termination left handoff files: %v", entries)
			}
		})
	}
}

func TestShellInitCleansHandoffFileOnInterrupt(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			initScript, err := shellInitScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			fakeStave := filepath.Join(binDir, "stave")
			if err := os.WriteFile(fakeStave, []byte("#!/bin/sh\nkill -INT \"$PPID\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			tmpDir := t.TempDir()
			cmd := exec.Command(binary, "-c", `eval "$STAVE_TEST_INIT"; stave`)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"STAVE_TEST_INIT="+initScript,
				"TMPDIR="+tmpDir,
			)
			_, _ = cmd.CombinedOutput()
			entries, err := os.ReadDir(tmpDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("shell interrupt left handoff files: %v", entries)
			}
		})
	}
}
