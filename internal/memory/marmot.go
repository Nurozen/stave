package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SupportedJSONSchema is the marmot --json envelope schema this package negotiates.
const SupportedJSONSchema = 1

// Marmot is the first memory Provider; it shells out to the marmot binary.
type Marmot struct {
	// Binary is the executable path/name (default "marmot").
	Binary string
	// LookPath resolves the binary; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Command builds an *exec.Cmd; defaults to exec.CommandContext.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func NewMarmot(binary string) *Marmot {
	if binary == "" {
		binary = "marmot"
	}
	return &Marmot{Binary: binary}
}

func (m *Marmot) Name() string { return "marmot" }

func (m *Marmot) binary() string {
	if m.Binary == "" {
		return "marmot"
	}
	return m.Binary
}

func (m *Marmot) lookPath(name string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath(name)
	}
	return exec.LookPath(name)
}

func (m *Marmot) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if m.Command != nil {
		return m.Command(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func (m *Marmot) Probe(ctx context.Context) (ProbeResult, error) {
	bin := m.binary()
	path, err := m.lookPath(bin)
	if err != nil {
		return ProbeResult{
			Available:  false,
			Capable:    false,
			Message:    fmt.Sprintf("%s not found on PATH", bin),
			ManualHint: fmt.Sprintf("install marmot or set memory.binary; then: stave memory attach <space-id>"),
		}, &UnavailableError{Provider: "marmot", Err: err, Hint: "binary not found"}
	}
	// Capability probe: den subcommand present.
	cmd := m.command(ctx, path, "den", "--help")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ProbeResult{
			Available:  true,
			Capable:    false,
			Version:    path,
			Message:    fmt.Sprintf("%s lacks den verbs: %v", path, err),
			ManualHint: "upgrade marmot to a build with den create/status/destroy",
		}, &UnsupportedError{Provider: "marmot", Feature: "den", Hint: strings.TrimSpace(stderr.String())}
	}
	version := ""
	vcmd := m.command(ctx, path, "--version")
	var vout bytes.Buffer
	vcmd.Stdout = &vout
	_ = vcmd.Run()
	version = strings.TrimSpace(vout.String())
	return ProbeResult{
		Available: true,
		Capable:   true,
		Version:   version,
		Message:   fmt.Sprintf("marmot ok (%s)", firstLine(version, path)),
	}, nil
}

// DenCreateArgs builds the exact argv for `marmot den create` used by attach.
// ALWAYS includes --no-pointer and --json. Never writes .marmot-vault into spaces.
//
// S2 only: --edit/--link/--ref are P4/S4 and MUST NOT be appended until the
// installed marmot den create accepts them (otherwise attach fails hard with
// invalid_args). Callers still receive those fields on AttachOptions for dry-run
// notices; S4 will re-enable flag pass-through behind a capability probe.
func DenCreateArgs(storeID, spacePath, lifetime string, editRefs, linkRefs []string, refSpecs []ReferenceSpec) []string {
	if lifetime == "" {
		lifetime = "task"
	}
	_ = editRefs
	_ = linkRefs
	_ = refSpecs
	return []string{
		"den", "create", storeID,
		"--lifetime", lifetime,
		"--project", spacePath,
		"--no-pointer",
		"--json",
	}
}

// FormatCommand renders binary + args as a single shell-ish line for dry-run.
func FormatCommand(binary string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, binary)
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func (m *Marmot) Attach(ctx context.Context, opts AttachOptions) (AttachResult, error) {
	bin := m.binary()
	storeID := opts.StoreID
	if storeID == "" {
		storeID = opts.SpaceID
	}
	name := opts.Name
	if name == "" {
		name = "default"
	}
	result := AttachResult{
		Provider: "marmot",
		StoreID:  storeID,
		Name:     name,
		Owned:    opts.UseID == "",
	}

	if opts.UseID != "" {
		// Attach existing: no den create; still write MCP + reverse route if possible.
		result.StoreID = opts.UseID
		result.Owned = false
		storeID = opts.UseID
		if opts.DryRun {
			line := FormatCommand(bin, []string{"den", "status", storeID, "--json"})
			result.DryRunCommands = append(result.DryRunCommands, line)
			printf(opts.Out, "dry-run: %s\n", line)
			printf(opts.Out, "dry-run: write space-local MCP config (den: %s)\n", storeID)
			printf(opts.Out, "dry-run: write memory attachment (marmot: %s, existing) to .stave.yaml\n", storeID)
			return result, nil
		}
		// Verify den exists.
		if _, err := m.runJSON(ctx, bin, []string{"den", "status", storeID, "--json"}); err != nil {
			return result, err
		}
		if err := WriteSpaceMCPConfig(opts.SpacePath, bin, storeID); err != nil {
			return result, err
		}
		result.MCPConfigWritten = true
		return result, nil
	}

	lifetime := opts.Lifetime
	if lifetime == "" {
		lifetime = "task"
	}
	args := DenCreateArgs(storeID, opts.SpacePath, lifetime, opts.EditRefs, opts.LinkRefs, opts.ReferenceSpecs)
	line := FormatCommand(bin, args)

	if opts.DryRun {
		result.DryRunCommands = append(result.DryRunCommands, line)
		printf(opts.Out, "dry-run: %s\n", line)
		printf(opts.Out, "dry-run: write space-local MCP config (den: %s)\n", storeID)
		printf(opts.Out, "dry-run: write memory attachment (marmot: %s, owned) to .stave.yaml\n", storeID)
		return result, nil
	}

	path, err := m.lookPath(bin)
	if err != nil {
		return result, &UnavailableError{Provider: "marmot", Err: err, Hint: "binary not found"}
	}
	envelope, err := m.runJSON(ctx, path, args)
	if err != nil {
		return result, err
	}
	result.StoreID = firstNonEmpty(envelope.DenID, storeID)
	result.StorePath = envelope.DenPath
	result.Warnings = append(result.Warnings, envelope.Warnings...)
	if envelope.PointerWritten {
		result.Warnings = append(result.Warnings, "marmot reported pointer_written=true; stave requested --no-pointer")
	}
	if err := WriteSpaceMCPConfig(opts.SpacePath, path, result.StoreID); err != nil {
		return result, fmt.Errorf("write space-local MCP config: %w", err)
	}
	result.MCPConfigWritten = true
	return result, nil
}

func (m *Marmot) Status(ctx context.Context, opts StatusOptions) (StatusResult, error) {
	bin := m.binary()
	path, err := m.lookPath(bin)
	if err != nil {
		return StatusResult{}, &UnavailableError{Provider: "marmot", Err: err}
	}
	raw, err := m.runRaw(ctx, path, []string{"den", "status", opts.StoreID, "--json"})
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{StoreID: opts.StoreID, RawJSON: string(raw), Summary: string(raw)}, nil
}

func (m *Marmot) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	bin := m.binary()
	args := []string{"warren", "sync", "--json"}
	line := FormatCommand(bin, args)
	if opts.DryRun {
		printf(opts.Out, "dry-run: %s\n", line)
		return SyncResult{Summary: "dry-run", DryRunCommands: []string{line}}, nil
	}
	path, err := m.lookPath(bin)
	if err != nil {
		return SyncResult{}, &UnavailableError{Provider: "marmot", Err: err}
	}
	if _, err := m.runRaw(ctx, path, args); err != nil {
		// Soft: warren sync may not exist yet; surface status re-probe instead.
		statusArgs := []string{"den", "status", opts.StoreID, "--json"}
		raw, statusErr := m.runRaw(ctx, path, statusArgs)
		if statusErr != nil {
			return SyncResult{}, err
		}
		return SyncResult{Summary: string(raw), Warnings: []string{fmt.Sprintf("warren sync unavailable: %v; reported den status", err)}}, nil
	}
	return SyncResult{Summary: "synced"}, nil
}

func (m *Marmot) Propose(ctx context.Context, opts ProposeOptions) (ProposeResult, error) {
	bin := m.binary()
	contribute := []string{"den", "contribute", opts.StoreID, "--json"}
	propose := []string{"warren", "propose", "--json"}
	lines := []string{FormatCommand(bin, contribute), FormatCommand(bin, propose)}
	if opts.DryRun {
		for _, line := range lines {
			printf(opts.Out, "dry-run: %s\n", line)
		}
		return ProposeResult{Summary: "dry-run", DryRunCommands: lines}, nil
	}
	path, err := m.lookPath(bin)
	if err != nil {
		return ProposeResult{}, &UnavailableError{Provider: "marmot", Err: err}
	}
	raw, err := m.runRaw(ctx, path, contribute)
	if err != nil {
		return ProposeResult{}, err
	}
	if _, err := m.runRaw(ctx, path, propose); err != nil {
		return ProposeResult{
			RawJSON:  string(raw),
			Summary:  "contributed; warren propose failed",
			Warnings: []string{err.Error()},
		}, err
	}
	return ProposeResult{RawJSON: string(raw), Summary: "contributed and proposed (no auto-push)"}, nil
}

func (m *Marmot) Detach(ctx context.Context, opts DetachOptions) (DetachResult, error) {
	bin := m.binary()
	result := DetachResult{}
	fate := opts.Fate
	if fate == "" {
		fate = FateKeep
	}
	// owned:false never destroyed.
	if !opts.Owned && (fate == FateDestroy || fate == FateContribute) {
		fate = FateKeep
		result.Warnings = append(result.Warnings, "attachment is not owned; forcing fate=keep (detach only)")
	}

	var cmds [][]string
	switch fate {
	case FateContribute:
		cmds = append(cmds,
			[]string{"den", "contribute", opts.StoreID, "--json"},
			[]string{"warren", "propose", "--json"},
			[]string{"den", "destroy", opts.StoreID, "--json"},
		)
		if opts.Force {
			// inject --force before --json on destroy
			last := cmds[len(cmds)-1]
			cmds[len(cmds)-1] = []string{"den", "destroy", opts.StoreID, "--force", "--json"}
			_ = last
		}
	case FateDestroy:
		destroy := []string{"den", "destroy", opts.StoreID, "--json"}
		if opts.Force {
			destroy = []string{"den", "destroy", opts.StoreID, "--force", "--json"}
		}
		cmds = append(cmds, destroy)
	default: // keep
		result.Kept = true
		// D6: marmot route set-project --from <old> --to <new> (not positional args).
		if opts.NewSpacePath != "" && opts.SpacePath != "" {
			cmds = append(cmds, []string{"route", "set-project", "--from", opts.SpacePath, "--to", opts.NewSpacePath, "--json"})
		} else if opts.RemoveRoute && opts.SpacePath != "" {
			cmds = append(cmds, []string{"route", "rm", "--project", opts.SpacePath, "--json"})
		}
	}

	for _, args := range cmds {
		line := FormatCommand(bin, args)
		result.DryRunCommands = append(result.DryRunCommands, line)
		if opts.DryRun {
			printf(opts.Out, "dry-run: %s\n", line)
			continue
		}
		path, err := m.lookPath(bin)
		if err != nil {
			return result, &UnavailableError{Provider: "marmot", Err: err}
		}
		if _, err := m.runJSON(ctx, path, args); err != nil {
			return result, err
		}
	}
	if opts.DryRun {
		// Archive path moves (NewSpacePath set) keep MCP configs with the
		// space; only true detach/destroy strips them.
		if opts.SpacePath != "" && opts.NewSpacePath == "" {
			result.DryRunCommands = append(result.DryRunCommands, "remove space-local context-marmot MCP config")
			printf(opts.Out, "dry-run: remove space-local context-marmot MCP config\n")
		}
		return result, nil
	}
	// Strip generated MCP bindings when the attachment is leaving the space
	// (not when only rewriting reverse routes for archive).
	if opts.SpacePath != "" && opts.NewSpacePath == "" {
		if err := RemoveSpaceMCPConfig(opts.SpacePath); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("mcp cleanup: %v", err))
		}
	}
	if fate == FateDestroy || fate == FateContribute {
		result.Destroyed = true
		result.Kept = false
		result.Summary = fmt.Sprintf("destroyed den %s", opts.StoreID)
	} else {
		result.Summary = fmt.Sprintf("den %s kept — durable residue of this task; inspect with 'marmot den status %s'", opts.StoreID, opts.StoreID)
		printf(opts.Out, "%s\n", result.Summary)
	}
	return result, nil
}

// JSON envelope types (schema 1) — negotiate on Schema field.

type envelope struct {
	Schema         int      `json:"schema"`
	DenID          string   `json:"den_id"`
	DenPath        string   `json:"den_path"`
	VaultID        string   `json:"vault_id"`
	PointerWritten bool     `json:"pointer_written"`
	Warnings       []string `json:"warnings"`
	Destroyed      bool     `json:"destroyed"`
	Kept           bool     `json:"kept"`
	Error          *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

func (m *Marmot) runJSON(ctx context.Context, path string, args []string) (envelope, error) {
	raw, err := m.runRaw(ctx, path, args)
	if err != nil {
		// Try to parse structured error from stdout.
		var env envelope
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			return env, &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
		}
		return envelope{}, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, fmt.Errorf("decode marmot json: %w\n%s", err, raw)
	}
	if env.Schema != SupportedJSONSchema {
		if env.Schema == 0 {
			return env, fmt.Errorf("decode marmot json: missing schema field (want %d)", SupportedJSONSchema)
		}
		if env.Schema > SupportedJSONSchema {
			return env, &UnsupportedError{Provider: "marmot", Feature: fmt.Sprintf("json schema %d", env.Schema), Hint: fmt.Sprintf("stave supports schema <= %d", SupportedJSONSchema)}
		}
		return env, fmt.Errorf("decode marmot json: unsupported schema %d (want %d)", env.Schema, SupportedJSONSchema)
	}
	if env.Error != nil {
		return env, &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
	}
	return env, nil
}

func (m *Marmot) runRaw(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := m.command(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.Bytes()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("marmot %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// WriteSpaceMCPConfig writes harness MCP configs pointing at the den.
// Never writes .marmot-vault. Uses den id via `marmot serve --den <id>` when
// the binary supports it; falls back to path-less `marmot serve` + reverse route.
// When MARMOT_HOME is set, it is embedded in each server env so MCP clients
// started without that env still resolve the same dens root.
func WriteSpaceMCPConfig(spacePath, binary, storeID string) error {
	if spacePath == "" {
		return fmt.Errorf("space path required for MCP config")
	}
	absBin := binary
	if !filepath.IsAbs(binary) {
		if resolved, err := exec.LookPath(binary); err == nil {
			absBin = resolved
		}
	}
	args := []string{"serve", "--den", storeID}
	server := map[string]any{
		"command": absBin,
		"args":    args,
	}
	if home := strings.TrimSpace(os.Getenv("MARMOT_HOME")); home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			home = abs
		}
		server["env"] = map[string]string{"MARMOT_HOME": home}
	}

	// Claude Code / generic: .mcp.json
	claude := map[string]any{"mcpServers": map[string]any{"context-marmot": server}}
	if err := writeJSONAtomic(filepath.Join(spacePath, ".mcp.json"), claude); err != nil {
		return err
	}
	// Cursor: .cursor/mcp.json
	if err := os.MkdirAll(filepath.Join(spacePath, ".cursor"), 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(spacePath, ".cursor", "mcp.json"), claude); err != nil {
		return err
	}
	// VS Code: .vscode/mcp.json (servers key)
	vsServer := map[string]any{
		"type":    "stdio",
		"command": absBin,
		"args":    args,
	}
	if env, ok := server["env"]; ok {
		vsServer["env"] = env
	}
	vscode := map[string]any{
		"servers": map[string]any{
			"context-marmot": vsServer,
		},
	}
	if err := os.MkdirAll(filepath.Join(spacePath, ".vscode"), 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(spacePath, ".vscode", "mcp.json"), vscode); err != nil {
		return err
	}
	// Codex: .codex/config.toml section
	var env map[string]string
	if e, ok := server["env"].(map[string]string); ok {
		env = e
	}
	if err := writeCodexMCP(spacePath, absBin, args, env); err != nil {
		return err
	}
	// Invariant: never create .marmot-vault
	return nil
}

func writeCodexMCP(spacePath, binary string, args []string, env map[string]string) error {
	dir := filepath.Join(spacePath, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.toml")
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	// Replace any prior context-marmot section so reattach updates den id.
	cleaned := stripCodexMCPSection(existing)
	var b strings.Builder
	b.WriteString(cleaned)
	if cleaned != "" && !strings.HasSuffix(cleaned, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n[mcp_servers.context-marmot]\n")
	b.WriteString("enabled = true\n")
	fmt.Fprintf(&b, "command = %q\n", binary)
	b.WriteString("args = [")
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", a)
	}
	b.WriteString("]\n")
	if home, ok := env["MARMOT_HOME"]; ok && home != "" {
		b.WriteString("\n[mcp_servers.context-marmot.env]\n")
		fmt.Fprintf(&b, "MARMOT_HOME = %q\n", home)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// RemoveSpaceMCPConfig strips the generated context-marmot MCP entries from
// the four harness config files while preserving any other servers/settings.
// Missing files are ignored. Empty JSON files after removal are deleted.
func RemoveSpaceMCPConfig(spacePath string) error {
	if spacePath == "" {
		return fmt.Errorf("space path required for MCP cleanup")
	}
	var errs []string
	for _, rel := range []string{".mcp.json", filepath.Join(".cursor", "mcp.json")} {
		if err := removeJSONMCPServer(filepath.Join(spacePath, rel), "mcpServers"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := removeJSONMCPServer(filepath.Join(spacePath, ".vscode", "mcp.json"), "servers"); err != nil {
		errs = append(errs, err.Error())
	}
	if err := removeCodexMCP(filepath.Join(spacePath, ".codex", "config.toml")); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func removeJSONMCPServer(path, mapKey string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		// Leave unparseable files alone rather than destroying user config.
		return fmt.Errorf("%s: %w", path, err)
	}
	servers, ok := doc[mapKey].(map[string]any)
	if !ok {
		return nil
	}
	if _, present := servers["context-marmot"]; !present {
		return nil
	}
	delete(servers, "context-marmot")
	if len(servers) == 0 {
		delete(doc, mapKey)
	} else {
		doc[mapKey] = servers
	}
	if len(doc) == 0 {
		_ = os.Remove(path)
		return nil
	}
	return writeJSONAtomic(path, doc)
}

func removeCodexMCP(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cleaned := strings.TrimSpace(stripCodexMCPSection(string(data)))
	if cleaned == "" {
		_ = os.Remove(path)
		return nil
	}
	if !strings.HasSuffix(cleaned, "\n") {
		cleaned += "\n"
	}
	return os.WriteFile(path, []byte(cleaned), 0o644)
}

// stripCodexMCPSection removes [mcp_servers.context-marmot] and nested
// [mcp_servers.context-marmot.*] tables from a TOML document.
func stripCodexMCPSection(src string) string {
	if src == "" || !strings.Contains(src, "mcp_servers.context-marmot") {
		return src
	}
	lines := strings.Split(src, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			header := strings.TrimSpace(trim[1 : len(trim)-1])
			if header == "mcp_servers.context-marmot" || strings.HasPrefix(header, "mcp_servers.context-marmot.") {
				skipping = true
				continue
			}
			skipping = false
		}
		if skipping {
			continue
		}
		out = append(out, line)
	}
	// Collapse excessive blank lines left by section removal.
	var compact []string
	blank := 0
	for _, line := range out {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		compact = append(compact, line)
	}
	return strings.Join(compact, "\n")
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Prefer atomic when available via fsio — local import to avoid cycle is fine.
	return writeFileAtomic(path, data)
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`'
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
