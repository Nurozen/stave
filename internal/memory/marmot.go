package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

	// S4 capability probes, cached per instance (one subprocess each, ever).
	refProbeOnce  sync.Once
	refProbeOK    bool
	syncProbeOnce sync.Once
	syncProbeOK   bool
}

func NewMarmot(binary string) *Marmot {
	if binary == "" {
		binary = "marmot"
	}
	return &Marmot{Binary: binary}
}

var (
	_ Provider        = (*Marmot)(nil)
	_ ReferenceLinker = (*Marmot)(nil)
	_ MCPWirer        = (*Marmot)(nil)
)

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
			ManualHint: "install marmot or set memory.binary; then: stave memory attach <space-id>",
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

// capabilityOutput runs the binary with args and returns combined
// stdout+stderr regardless of exit code (marmot prints usage on stderr, and
// e.g. `warren --help` exits nonzero on old builds). Empty when the binary
// cannot be resolved or started.
func (m *Marmot) capabilityOutput(ctx context.Context, args ...string) string {
	path, err := m.lookPath(m.binary())
	if err != nil {
		return ""
	}
	cmd := m.command(ctx, path, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run()
	return out.String()
}

// SupportsRefPassthrough reports whether the installed marmot has the P4
// den surface: `den link` (--edit/--link) and `den create --ref`. Probe:
// `marmot den --help` exits 0 on every den-capable build (S2 convention) and
// its usage text lists the verbs/flags additively — the S4 surface is present
// exactly when it mentions both the `link` verb and the `--ref` flag. Cached
// per Marmot instance.
func (m *Marmot) SupportsRefPassthrough(ctx context.Context) bool {
	m.refProbeOnce.Do(func() {
		out := m.capabilityOutput(ctx, "den", "--help")
		m.refProbeOK = strings.Contains(out, "--ref") && strings.Contains(out, "link")
	})
	return m.refProbeOK
}

// SupportsWarrenSync reports whether the installed marmot has cache-backed
// warrens (`warren add`/`warren sync`, P2). Probe: `marmot warren --help`
// prints the subcommand list on stderr on every build (exit code varies);
// the P2 surface is present exactly when it mentions `sync`. Cached per
// Marmot instance.
func (m *Marmot) SupportsWarrenSync(ctx context.Context) bool {
	m.syncProbeOnce.Do(func() {
		out := m.capabilityOutput(ctx, "warren", "--help")
		m.syncProbeOK = strings.Contains(out, "sync")
	})
	return m.syncProbeOK
}

// DenCreateArgs builds the exact argv for `marmot den create` used by attach.
// ALWAYS includes --no-pointer and --json. Never writes .marmot-vault into spaces.
//
// passthrough=false is the S2 shape: --ref/--opt MUST NOT be appended when the
// installed marmot den create does not accept them (otherwise attach fails
// hard with invalid_args). passthrough=true (S4, gated on
// SupportsRefPassthrough) appends one repeatable --ref name=,url=,ref= spec
// per reference repo (marmot resolves them into links) and provider --opt
// knobs as den create flags (k=true → bare --k, else --k v; sorted for a
// deterministic argv).
//
// editRefs/linkRefs never appear here in either mode: --edit/--link belong to
// `den link`, issued as separate calls after create (see DenLinkArgs).
// Reference specs with an explicit marmotVault id are excluded — they bypass
// resolution via a direct `den link --link <id>` — and "off" specs are
// filtered before the seam.
func DenCreateArgs(storeID, spacePath, lifetime string, editRefs, linkRefs []string, refSpecs []ReferenceSpec, opts map[string]string, passthrough bool) []string {
	if lifetime == "" {
		lifetime = "task"
	}
	_ = editRefs
	_ = linkRefs
	args := []string{
		"den", "create", storeID,
		"--lifetime", lifetime,
		"--project", spacePath,
		"--no-pointer",
	}
	if passthrough {
		for _, spec := range refSpecs {
			if spec.MarmotVault != "" {
				continue // "off" filtered upstream; explicit id → direct den link
			}
			args = append(args, "--ref", refSpecArg(spec))
		}
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if opts[k] == "true" {
				args = append(args, "--"+k)
				continue
			}
			args = append(args, "--"+k, opts[k])
		}
	}
	return append(args, "--json")
}

// refSpecArg renders one ReferenceSpec in marmot's --ref machine grammar
// (name=<n>,url=<u>,path=<p>,ref=<r>; empty components omitted).
func refSpecArg(spec ReferenceSpec) string {
	parts := make([]string, 0, 3)
	if spec.Name != "" {
		parts = append(parts, "name="+spec.Name)
	}
	if spec.URL != "" {
		parts = append(parts, "url="+spec.URL)
	}
	if spec.Ref != "" {
		parts = append(parts, "ref="+spec.Ref)
	}
	return strings.Join(parts, ",")
}

// ResolveArgs builds the argv for `marmot resolve` (S4 space add parity):
// diagnostic resolution of one reference repo, sharing the exact resolver
// den create --ref uses.
func ResolveArgs(spec ReferenceSpec) []string {
	args := []string{"resolve"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.URL != "" {
		args = append(args, "--url", spec.URL)
	}
	if spec.Ref != "" {
		args = append(args, "--ref", spec.Ref)
	}
	return append(args, "--json")
}

// LinkReference implements the optional ReferenceLinker seam for `space add`
// parity (plan §3.6): resolve one newly added reference repo and, when it
// resolves via warren-url, link it read-only onto the den. v1 policy:
//   - config marmotVault id → direct `den link --link <id>` (skips resolution)
//   - resolved via warren-url → `den link --link <warren>/<project>`
//   - checkout-vault / none → notice only, no link (repo already added)
//   - old binary (probe fails) → notice, degrade — attach parity with the S2 drop
func (m *Marmot) LinkReference(ctx context.Context, opts LinkReferenceOptions) (LinkReferenceResult, error) {
	bin := m.binary()
	res := LinkReferenceResult{}
	spec := opts.Spec
	label := firstNonEmpty(spec.Name, spec.URL)
	if spec.MarmotVault == "off" {
		return res, nil // suppressed upstream; defensive
	}
	resolveArgs := ResolveArgs(spec)
	directTarget := spec.MarmotVault // explicit vault id bypasses resolution
	if opts.DryRun {
		// Dry-run never invokes the binary (so it cannot probe or resolve):
		// print the plan.
		if directTarget == "" {
			line := FormatCommand(bin, resolveArgs)
			res.DryRunCommands = append(res.DryRunCommands, line)
			printf(opts.Out, "dry-run: %s\n", line)
			line = FormatCommand(bin, DenLinkArgs(opts.StoreID, "<warren>/<project>", false)) + " (when resolved via warren-url)"
			res.DryRunCommands = append(res.DryRunCommands, line)
			printf(opts.Out, "dry-run: %s\n", line)
			return res, nil
		}
		line := FormatCommand(bin, DenLinkArgs(opts.StoreID, directTarget, false))
		res.DryRunCommands = append(res.DryRunCommands, line)
		printf(opts.Out, "dry-run: %s\n", line)
		return res, nil
	}
	path, err := m.lookPath(bin)
	if err != nil {
		return res, &UnavailableError{Provider: "marmot", Err: err, Hint: "binary not found"}
	}
	// resolve + den link shipped together (P4): one probe gates both. Old
	// binaries degrade with a notice — the reference repo is already added.
	if !m.SupportsRefPassthrough(ctx) {
		res.Notice = fmt.Sprintf("installed marmot lacks resolve/den link support; reference %s added without a memory link (upgrade marmot)", label)
		printf(opts.Out, "notice: %s\n", res.Notice)
		return res, nil
	}
	target := directTarget
	resolvedVia := "explicit"
	if target == "" {
		env, rerr := m.runJSON(ctx, path, resolveArgs, opts.SpacePath)
		if rerr != nil {
			return res, fmt.Errorf("resolve %s: %w", label, rerr)
		}
		reportWarnings(opts.Out, &res.Warnings, env.Warnings)
		resolvedVia = firstNonEmpty(env.ResolvedVia, "none")
		switch resolvedVia {
		case "warren-url":
			target = env.Warren + "/" + env.Project
		case "checkout-vault":
			res.Link = AttachLink{ResolvedVia: resolvedVia}
			res.Notice = fmt.Sprintf("reference %s resolves via checkout-vault (%s); v1 links only warren-url matches — link manually: marmot den link %s --link %s", label, env.VaultID, opts.StoreID, env.VaultID)
			printf(opts.Out, "notice: %s\n", res.Notice)
			return res, nil
		default:
			res.Link = AttachLink{ResolvedVia: "none"}
			printf(opts.Out, "reference %s → no memory found\n", label)
			return res, nil
		}
	}
	linkEnv, lerr := m.runJSON(ctx, path, DenLinkArgs(opts.StoreID, target, false), opts.SpacePath)
	if lerr != nil {
		return res, fmt.Errorf("den link %s: %w", target, lerr)
	}
	mode := "link"
	if linkEnv.Link != nil && linkEnv.Link.Mode != nil && *linkEnv.Link.Mode != "" {
		mode = *linkEnv.Link.Mode
	}
	reportWarnings(opts.Out, &res.Warnings, linkEnv.Warnings)
	res.Linked = true
	res.Link = AttachLink{Ref: target, Mode: mode, ResolvedVia: resolvedVia}
	if resolvedVia == "explicit" {
		printf(opts.Out, "reference %s → %s (config marmotVault)\n", label, target)
	} else {
		printf(opts.Out, "reference %s → %s (%s)\n", label, target, resolvedVia)
	}
	return res, nil
}

// WriteMCPConfig implements the optional MCPWirer seam: it writes the four
// space-local harness configs pointing `serve --den <storeID>` at the
// resolved binary (embedding MARMOT_HOME exactly as attach does), reusing the
// same rendering attach goes through.
func (m *Marmot) WriteMCPConfig(_ context.Context, spacePath, storeID string) error {
	bin := m.binary()
	if path, err := m.lookPath(bin); err == nil {
		bin = path
	}
	return WriteSpaceMCPConfig(spacePath, bin, storeID)
}

// RemoveMCPConfig implements the optional MCPWirer seam: it strips ONLY the
// generated context-marmot entries via the entry-level cleanup detach uses,
// preserving any other servers/settings.
func (m *Marmot) RemoveMCPConfig(_ context.Context, spacePath string) error {
	return RemoveSpaceMCPConfig(spacePath)
}

// DenLinkArgs builds the argv for one `marmot den link` pass-through call.
func DenLinkArgs(storeID, target string, edit bool) []string {
	flag := "--link"
	if edit {
		flag = "--edit"
	}
	return []string{"den", "link", storeID, flag, target, "--json"}
}

// reportWarnings appends envelope warnings to a result's warning list AND
// prints each with a "warning:" prefix — parsed warnings must never be
// silently swallowed (F23).
func reportWarnings(out io.Writer, dest *[]string, warnings []string) {
	for _, w := range warnings {
		*dest = append(*dest, w)
		printf(out, "warning: %s\n", w)
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

	hasS4 := len(opts.EditRefs) > 0 || len(opts.LinkRefs) > 0 || len(opts.Opts) > 0 || len(opts.ReferenceSpecs) > 0

	if opts.UseID != "" {
		// Attach existing: no den create; still write MCP + reverse route if possible.
		result.StoreID = opts.UseID
		result.Owned = false
		storeID = opts.UseID
		// S4 content never passes through on attach-existing (there is no den
		// create to carry --ref/--opt, and v1 does not issue den link here).
		// The drop must NEVER be silent — every caller path (space create,
		// review, memory attach) sees the notice via opts.Out (F23).
		if hasS4 {
			notice := "attached memory without --edit/--link/--ref: attach-existing reuses den " + storeID + " as-is (v1 does not link references on attach-existing)"
			if !opts.DryRun && !m.SupportsRefPassthrough(ctx) {
				notice = "attached memory without --edit/--link/--ref: installed marmot predates den link/--ref support"
			}
			result.Warnings = append(result.Warnings, notice)
			printf(opts.Out, "notice: %s\n", notice)
		}
		// NOTE: --json must precede the positional den id — marmot's flag
		// parsing stops at the first non-flag argument.
		routeArgs := []string{"route", "add", "--project", opts.SpacePath, "--json", storeID}
		if opts.DryRun {
			line := FormatCommand(bin, []string{"den", "status", storeID, "--json"})
			result.DryRunCommands = append(result.DryRunCommands, line)
			printf(opts.Out, "dry-run: %s\n", line)
			routeLine := FormatCommand(bin, routeArgs)
			result.DryRunCommands = append(result.DryRunCommands, routeLine)
			printf(opts.Out, "dry-run: %s\n", routeLine)
			printf(opts.Out, "dry-run: write space-local MCP config (den: %s)\n", storeID)
			printf(opts.Out, "dry-run: write memory attachment (marmot: %s, existing) to .stave.yaml\n", storeID)
			return result, nil
		}
		// Verify den exists.
		if _, err := m.runJSON(ctx, bin, []string{"den", "status", storeID, "--json"}, opts.SpacePath); err != nil {
			return result, err
		}
		// Register the reverse route (space path → den id) so space-level route
		// ops (archive relocation, detach route rm) and cwd-based resolution
		// inside the space work for attach-existing too (G2). Marmot's route
		// table maps one path to one id, so on multi-attach the route follows
		// the most recently attached den.
		if opts.SpacePath != "" {
			if _, err := m.runJSON(ctx, bin, routeArgs, ""); err != nil {
				return result, fmt.Errorf("register reverse route for existing den %s: %w", storeID, err)
			}
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

	if opts.DryRun {
		// Dry-run never invokes the binary, so it cannot probe: print the
		// full S4 plan (a non-S4 marmot drops these at real attach time with
		// a notice instead). With no S4 flags this is the S2 argv unchanged.
		args := DenCreateArgs(storeID, opts.SpacePath, lifetime, opts.EditRefs, opts.LinkRefs, opts.ReferenceSpecs, opts.Opts, true)
		line := FormatCommand(bin, args)
		result.DryRunCommands = append(result.DryRunCommands, line)
		printf(opts.Out, "dry-run: %s\n", line)
		for _, lk := range denLinkPlan(storeID, opts) {
			linkLine := FormatCommand(bin, lk.args)
			result.DryRunCommands = append(result.DryRunCommands, linkLine)
			printf(opts.Out, "dry-run: %s\n", linkLine)
		}
		printf(opts.Out, "dry-run: write space-local MCP config (den: %s)\n", storeID)
		printf(opts.Out, "dry-run: write memory attachment (marmot: %s, owned) to .stave.yaml\n", storeID)
		return result, nil
	}

	path, err := m.lookPath(bin)
	if err != nil {
		return result, &UnavailableError{Provider: "marmot", Err: err, Hint: "binary not found"}
	}
	// S4 pass-through is capability-gated: old binaries keep the byte-stable
	// S2 argv, with a notice when flags are dropped. Attaches without any S4
	// content skip the probe entirely (the argv is identical either way).
	passthrough := hasS4 && m.SupportsRefPassthrough(ctx)
	if hasS4 && !passthrough {
		notice := "installed marmot lacks den link/--ref support; dropping --edit/--link/--ref/--opt (S2 attach — upgrade marmot for reference pass-through)"
		result.Warnings = append(result.Warnings, notice)
		printf(opts.Out, "notice: %s\n", notice)
	}
	args := DenCreateArgs(storeID, opts.SpacePath, lifetime, opts.EditRefs, opts.LinkRefs, opts.ReferenceSpecs, opts.Opts, passthrough)
	envelope, err := m.runJSON(ctx, path, args, opts.SpacePath)
	if err != nil {
		return result, err
	}
	result.StoreID = firstNonEmpty(envelope.DenID, storeID)
	result.StorePath = envelope.DenPath
	reportWarnings(opts.Out, &result.Warnings, envelope.Warnings)
	if envelope.PointerWritten {
		reportWarnings(opts.Out, &result.Warnings, []string{"marmot reported pointer_written=true; stave requested --no-pointer"})
	}
	if passthrough {
		// Per-reference resolution: envelope links are the --ref outcomes in
		// spec order (marmot resolves; stave only reports).
		passed := make([]ReferenceSpec, 0, len(opts.ReferenceSpecs))
		for _, spec := range opts.ReferenceSpecs {
			if spec.MarmotVault == "" {
				passed = append(passed, spec)
			}
		}
		for i, l := range envelope.Links {
			mode := ""
			if l.Mode != nil {
				mode = *l.Mode
			}
			result.Links = append(result.Links, AttachLink{Ref: l.Ref, Mode: mode, ResolvedVia: l.ResolvedVia})
			label := l.Ref
			if i < len(passed) {
				label = firstNonEmpty(passed[i].Name, passed[i].URL, l.Ref)
			}
			if mode == "" {
				printf(opts.Out, "reference %s → no memory found\n", label)
			} else {
				printf(opts.Out, "reference %s → %s (%s)\n", label, l.Ref, l.ResolvedVia)
			}
		}
		// --edit/--link and marmotVault-forced links go through den link,
		// AFTER create (they are den link verbs, not den create flags).
		for _, lk := range denLinkPlan(result.StoreID, opts) {
			linkEnv, lerr := m.runJSON(ctx, path, lk.args, opts.SpacePath)
			if lerr != nil {
				return result, fmt.Errorf("den link %s: %w", lk.target, lerr)
			}
			mode := lk.mode
			if linkEnv.Link != nil && linkEnv.Link.Mode != nil && *linkEnv.Link.Mode != "" {
				mode = *linkEnv.Link.Mode
			}
			result.Links = append(result.Links, AttachLink{Ref: lk.target, Mode: mode, ResolvedVia: "explicit"})
			reportWarnings(opts.Out, &result.Warnings, linkEnv.Warnings)
			if lk.label != "" {
				printf(opts.Out, "reference %s → %s (config marmotVault)\n", lk.label, lk.target)
			} else {
				printf(opts.Out, "linked %s (mode=%s)\n", lk.target, mode)
			}
		}
	}
	if err := WriteSpaceMCPConfig(opts.SpacePath, path, result.StoreID); err != nil {
		return result, fmt.Errorf("write space-local MCP config: %w", err)
	}
	result.MCPConfigWritten = true
	return result, nil
}

// denLinkCall is one planned `den link` pass-through invocation.
type denLinkCall struct {
	args   []string
	target string
	mode   string // expected mode for reporting; envelope wins when present
	label  string // reference name for marmotVault-forced links
}

// denLinkPlan expands AttachOptions into the den link calls issued after den
// create: --edit refs, --link refs, then reference repos whose config
// marmotVault forces an explicit target (skipping resolution entirely).
func denLinkPlan(storeID string, opts AttachOptions) []denLinkCall {
	var calls []denLinkCall
	for _, ref := range opts.EditRefs {
		calls = append(calls, denLinkCall{args: DenLinkArgs(storeID, ref, true), target: ref, mode: "edit"})
	}
	for _, ref := range opts.LinkRefs {
		calls = append(calls, denLinkCall{args: DenLinkArgs(storeID, ref, false), target: ref, mode: "link"})
	}
	for _, spec := range opts.ReferenceSpecs {
		if spec.MarmotVault == "" || spec.MarmotVault == "off" {
			continue
		}
		calls = append(calls, denLinkCall{
			args:   DenLinkArgs(storeID, spec.MarmotVault, false),
			target: spec.MarmotVault,
			label:  firstNonEmpty(spec.Name, spec.URL),
		})
	}
	return calls
}

func (m *Marmot) Status(ctx context.Context, opts StatusOptions) (StatusResult, error) {
	bin := m.binary()
	path, err := m.lookPath(bin)
	if err != nil {
		return StatusResult{}, &UnavailableError{Provider: "marmot", Err: err}
	}
	// runJSONRaw like every other verb: structured refusals surface as
	// RefusalError (not raw blobs) and the schema is negotiated. The raw JSON
	// rides along for display. Binaries without link freshness fields still
	// parse fine (additive envelope) and render an empty links list.
	env, raw, err := m.runJSONRaw(ctx, path, []string{"den", "status", opts.StoreID, "--json"}, "")
	if err != nil {
		return StatusResult{StoreID: opts.StoreID, RawJSON: string(raw)}, err
	}
	result := StatusResult{StoreID: opts.StoreID, RawJSON: string(raw), Summary: string(raw), Warnings: env.Warnings}
	result.StoreID = firstNonEmpty(env.DenID, opts.StoreID)
	result.Lifetime = env.Lifetime
	for _, l := range env.Links {
		mode := ""
		if l.Mode != nil {
			mode = *l.Mode
		}
		pinned := ""
		if l.PinnedCommit != nil {
			pinned = *l.PinnedCommit
		}
		result.Links = append(result.Links, LinkStatus{
			Ref:          l.Ref,
			Mode:         mode,
			PinnedCommit: pinned,
			Ahead:        l.Ahead,
			Behind:       l.Behind,
			PendingEdits: l.PendingEdits,
			State:        l.State,
			SourceCommit: l.SourceCommit,
		})
	}
	result.Summary = renderStatusSummary(result)
	return result, nil
}

// renderStatusSummary compacts a parsed den status into per-link rows:
//
//	den t1 (task)
//	  w/docs  edit  ahead 4 / behind 0 / 5 pending edits (unpushed)
//	  w/billing  link  pinned abc1234  behind 3 (stale)  [vault from source 77aa88b]
//	  auth-den  live  (ok)
func renderStatusSummary(st StatusResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "den %s", st.StoreID)
	if st.Lifetime != "" {
		fmt.Fprintf(&b, " (%s)", st.Lifetime)
	}
	if len(st.Links) == 0 {
		b.WriteString("  links: none")
		return b.String()
	}
	for _, l := range st.Links {
		fmt.Fprintf(&b, "\n%s  %s", l.Ref, firstNonEmpty(l.Mode, "unresolved"))
		switch l.Mode {
		case "edit":
			fmt.Fprintf(&b, "  ahead %d / behind %d / %d pending edits", l.Ahead, l.Behind, l.PendingEdits)
		case "link":
			if l.PinnedCommit != "" {
				fmt.Fprintf(&b, "  pinned %s", shortCommit(l.PinnedCommit))
			}
			fmt.Fprintf(&b, "  behind %d", l.Behind)
		}
		if l.State != "" {
			fmt.Fprintf(&b, " (%s)", l.State)
		}
		if l.SourceCommit != "" {
			fmt.Fprintf(&b, "  [vault snapshot from source commit %s]", shortCommit(l.SourceCommit))
		}
	}
	return b.String()
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
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
	if !m.SupportsWarrenSync(ctx) {
		// Old binary (probe-gated): report den status instead — the ONLY
		// remaining soft fallback. New binaries surface real sync failures.
		statusArgs := []string{"den", "status", opts.StoreID, "--json"}
		raw, statusErr := m.runRaw(ctx, path, statusArgs, "")
		if statusErr != nil {
			return SyncResult{}, fmt.Errorf("installed marmot lacks warren sync and den status failed: %w", statusErr)
		}
		return SyncResult{Summary: string(raw), Warnings: []string{"installed marmot lacks warren sync; reported den status instead (upgrade marmot for cache-backed warren sync)"}}, nil
	}
	// Run warren sync and parse the envelope even on a nonzero exit: marmot
	// exits 1 only when EVERY warren failed, and still prints the envelope.
	raw, runErr := m.runRaw(ctx, path, args, "")
	var env envelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil || env.Schema != SupportedJSONSchema {
		if runErr != nil {
			return SyncResult{}, runErr
		}
		return SyncResult{}, fmt.Errorf("decode marmot warren sync json: %v\n%s", jerr, raw)
	}
	if env.Error != nil {
		return SyncResult{}, &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
	}
	result := SyncResult{Warrens: env.Warrens, Warnings: env.Warnings}
	failed := 0
	for _, w := range env.Warrens {
		switch {
		case w.Error != "":
			failed++
			printf(opts.Out, "warren %s failed: %s\n", w.ID, w.Error)
			result.Warnings = append(result.Warnings, fmt.Sprintf("warren %s: %s", w.ID, w.Error))
		case w.Updated && w.PreviousCommit == "":
			printf(opts.Out, "synced %s (pinned %s)\n", w.ID, shortCommit(w.PinnedCommit))
		case w.Updated:
			printf(opts.Out, "synced %s (updated %s → %s)\n", w.ID, shortCommit(w.PreviousCommit), shortCommit(w.PinnedCommit))
		default:
			printf(opts.Out, "synced %s (up to date at %s)\n", w.ID, shortCommit(w.PinnedCommit))
		}
	}
	switch {
	case len(env.Warrens) == 0:
		result.Summary = "no cached warrens to sync"
	case failed == len(env.Warrens):
		// Mirror marmot's exit semantics: nonzero only when every warren failed.
		result.Summary = fmt.Sprintf("all %d warrens failed to sync", failed)
		return result, fmt.Errorf("warren sync: all %d warrens failed", failed)
	case failed > 0:
		result.Summary = fmt.Sprintf("synced %d/%d warrens (%d failed)", len(env.Warrens)-failed, len(env.Warrens), failed)
	default:
		result.Summary = fmt.Sprintf("synced %d warren(s)", len(env.Warrens))
	}
	return result, nil
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
	// runJSONRaw (not runRaw) so refusals surface as structured
	// RefusalErrors with marmot's error code — same seam as Attach/Detach.
	// opts.SpacePath (when known) becomes the subprocess cwd so marmot's
	// reverse-route workspace resolution targets the space, not stave's cwd.
	cEnv, raw, err := m.runJSONRaw(ctx, path, contribute, opts.SpacePath)
	if err != nil {
		return ProposeResult{}, err
	}
	// Parse BOTH envelopes (G4): contribute carries branch/commit/counts (and,
	// additively, push_command/checkout); propose carries push_command and
	// nothing_to_propose. None of it may be silently discarded.
	result := ProposeResult{
		RawJSON:     string(raw),
		Branch:      cEnv.Branch,
		Commit:      cEnv.Commit,
		Committed:   cEnv.Committed,
		Contributed: cEnv.Contributed,
		Checkout:    cEnv.Checkout,
		PushCommand: cEnv.PushCommand,
	}
	result.Warnings = append(result.Warnings, cEnv.Warnings...)
	pEnv, praw, err := m.runJSONRaw(ctx, path, propose, opts.SpacePath)
	result.ProposeRawJSON = string(praw)
	if err != nil {
		result.Summary = "contributed; warren propose failed"
		result.Warnings = append(result.Warnings, err.Error())
		return result, err
	}
	result.Warnings = append(result.Warnings, pEnv.Warnings...)
	result.NothingToPropose = pEnv.NothingToPropose
	if result.PushCommand == "" {
		result.PushCommand = pEnv.PushCommand
	}
	if result.Branch == "" {
		result.Branch = pEnv.Branch
	}
	if result.Commit == "" {
		result.Commit = pEnv.Commit
	}
	result.Summary = "contributed and proposed (no auto-push)"
	return result, nil
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

	// contribute/propose/destroy run with cwd = the space path (marmot resolves
	// the warren-propose workspace from cwd via reverse routes). Route rewrite
	// commands (fate keep) pass explicit --from/--project paths and may run
	// after the space dir is gone, so they inherit stave's cwd. Route updates
	// are tolerant: a missing route warns instead of aborting (the den itself
	// is untouched either way).
	execDir := ""
	var cmds [][]string      // destructive/contribute commands — failure aborts
	var routeCmds [][]string // space-level route updates — failure warns
	switch fate {
	case FateContribute:
		execDir = opts.SpacePath
		// Contribute + propose through the shared Propose flow so counts,
		// warnings and the push command surface BEFORE the den is destroyed (G4).
		pres, perr := m.Propose(ctx, ProposeOptions{
			StoreID:   opts.StoreID,
			SpacePath: opts.SpacePath,
			DryRun:    opts.DryRun,
			Out:       opts.Out,
			Force:     opts.Force,
		})
		result.DryRunCommands = append(result.DryRunCommands, pres.DryRunCommands...)
		result.Warnings = append(result.Warnings, pres.Warnings...)
		if perr != nil {
			return result, perr
		}
		if !opts.DryRun {
			PrintProposeOutcome(opts.Out, pres)
		}
		destroy := []string{"den", "destroy", opts.StoreID, "--json"}
		if opts.Force {
			destroy = []string{"den", "destroy", opts.StoreID, "--force", "--json"}
		}
		cmds = append(cmds, destroy)
	case FateDestroy:
		execDir = opts.SpacePath
		destroy := []string{"den", "destroy", opts.StoreID, "--json"}
		if opts.Force {
			destroy = []string{"den", "destroy", opts.StoreID, "--force", "--json"}
		}
		cmds = append(cmds, destroy)
	default: // keep
		result.Kept = true
		// D6: marmot route set-project --from <old> --to <new> (not positional args).
		if opts.NewSpacePath != "" && opts.SpacePath != "" {
			routeCmds = append(routeCmds, []string{"route", "set-project", "--from", opts.SpacePath, "--to", opts.NewSpacePath, "--json"})
		} else if opts.RemoveRoute && opts.SpacePath != "" {
			routeCmds = append(routeCmds, []string{"route", "rm", "--project", opts.SpacePath, "--json"})
		}
	}
	// Sibling attachments remain on the space (any fate): the reverse route
	// maps the space to exactly ONE den, so re-point it at a surviving den
	// instead of leaving it dangling at the detached/destroyed one. route add
	// upserts, so this is safe even when the route already targets a survivor.
	if opts.RepointRouteStoreID != "" && opts.SpacePath != "" && opts.NewSpacePath == "" {
		routeCmds = append(routeCmds, []string{"route", "add", "--project", opts.SpacePath, "--json", opts.RepointRouteStoreID})
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
		env, err := m.runJSON(ctx, path, args, execDir)
		if err != nil {
			return result, err
		}
		reportWarnings(opts.Out, &result.Warnings, env.Warnings)
	}
	for _, args := range routeCmds {
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
		if _, err := m.runJSON(ctx, path, args, ""); err != nil {
			warn := fmt.Sprintf("route update failed (den untouched; repair with 'marmot %s'): %v", strings.Join(args[:len(args)-1], " "), err)
			result.Warnings = append(result.Warnings, warn)
			printf(opts.Out, "warning: %s\n", warn)
		}
	}
	if opts.DryRun {
		// Archive path moves (NewSpacePath set) keep MCP configs with the
		// space; only detaching the provider's LAST attachment strips them
		// (KeepSpaceWiring means siblings remain and still need them).
		if opts.SpacePath != "" && opts.NewSpacePath == "" {
			switch {
			case !opts.KeepSpaceWiring:
				result.DryRunCommands = append(result.DryRunCommands, "remove space-local context-marmot MCP config")
				printf(opts.Out, "dry-run: remove space-local context-marmot MCP config\n")
			case opts.RepointRouteStoreID != "":
				line := fmt.Sprintf("re-point space-local context-marmot MCP config at den %s", opts.RepointRouteStoreID)
				result.DryRunCommands = append(result.DryRunCommands, line)
				printf(opts.Out, "dry-run: %s\n", line)
			}
		}
		return result, nil
	}
	// Strip generated MCP bindings when the PROVIDER is leaving the space
	// (not when only rewriting reverse routes for archive, and not while
	// sibling attachments still rely on them). When siblings remain, rewrite
	// the config so `serve --den` targets a surviving den instead of the
	// detached one.
	if opts.SpacePath != "" && opts.NewSpacePath == "" {
		switch {
		case !opts.KeepSpaceWiring:
			if err := RemoveSpaceMCPConfig(opts.SpacePath); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("mcp cleanup: %v", err))
			}
		case opts.RepointRouteStoreID != "":
			if err := WriteSpaceMCPConfig(opts.SpacePath, bin, opts.RepointRouteStoreID); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("mcp re-point: %v", err))
			}
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
	// S4 additive fields. Links carries both shapes: den create --ref
	// outcomes ({ref, mode|null, resolved_via}) and den status freshness rows
	// ({ref, mode, pinned_commit|null, ahead, behind, pending_edits, state,
	// source_commit}). Link is den link's single-link object; Warrens is the
	// warren sync per-warren result list; Lifetime rides den status.
	Lifetime string         `json:"lifetime"`
	Links    []envelopeLink `json:"links"`
	Link     *envelopeLink  `json:"link"`
	Warrens  []WarrenSync   `json:"warrens"`
	// `marmot resolve --json` fields (S4 space add parity,
	// testdata/contracts/resolve.v1.json): how a reference repo would resolve
	// into a den link. ResolvedVia is warren-url|checkout-vault|none.
	ResolvedVia string `json:"resolved_via"`
	Warren      string `json:"warren"`
	Project     string `json:"project"`
	Detail      string `json:"detail"`
	// Contribute/propose handoff fields (G4). push_command/checkout are
	// additive on contribute — parsed opportunistically.
	Branch           string             `json:"branch"`
	Commit           string             `json:"commit"`
	Committed        bool               `json:"committed"`
	Contributed      *ContributedCounts `json:"contributed"`
	PushCommand      string             `json:"push_command"`
	Checkout         string             `json:"checkout"`
	NothingToPropose bool               `json:"nothing_to_propose"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// envelopeLink is the union of marmot's link JSON shapes (see envelope.Links).
type envelopeLink struct {
	Ref          string  `json:"ref"`
	Mode         *string `json:"mode"`
	ResolvedVia  string  `json:"resolved_via"`
	PinnedCommit *string `json:"pinned_commit"`
	Ahead        int     `json:"ahead"`
	Behind       int     `json:"behind"`
	PendingEdits int     `json:"pending_edits"`
	State        string  `json:"state"`
	SourceCommit string  `json:"source_commit"`
	Target       string  `json:"target"`
}

func (m *Marmot) runJSON(ctx context.Context, path string, args []string, dir string) (envelope, error) {
	env, _, err := m.runJSONRaw(ctx, path, args, dir)
	return env, err
}

// runJSONRaw runs a --json marmot verb and returns the parsed envelope plus
// the raw stdout bytes (for RawJSON passthrough fields).
func (m *Marmot) runJSONRaw(ctx context.Context, path string, args []string, dir string) (envelope, []byte, error) {
	raw, err := m.runRaw(ctx, path, args, dir)
	if err != nil {
		// Try to parse structured error from stdout.
		var env envelope
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			return env, raw, &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
		}
		return envelope{}, raw, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, raw, fmt.Errorf("decode marmot json: %w\n%s", err, raw)
	}
	if env.Schema != SupportedJSONSchema {
		if env.Schema == 0 {
			return env, raw, fmt.Errorf("decode marmot json: missing schema field (want %d)", SupportedJSONSchema)
		}
		if env.Schema > SupportedJSONSchema {
			return env, raw, &UnsupportedError{Provider: "marmot", Feature: fmt.Sprintf("json schema %d", env.Schema), Hint: fmt.Sprintf("stave supports schema <= %d", SupportedJSONSchema)}
		}
		return env, raw, fmt.Errorf("decode marmot json: unsupported schema %d (want %d)", env.Schema, SupportedJSONSchema)
	}
	if env.Error != nil {
		return env, raw, &RefusalError{Provider: "marmot", Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint}
	}
	return env, raw, nil
}

// runRaw executes the binary. When dir is non-empty the subprocess runs with
// that working directory (the space path), so marmot's cwd-based workspace
// resolution (reverse routes) targets the space regardless of stave's own cwd.
func (m *Marmot) runRaw(ctx context.Context, path string, args []string, dir string) ([]byte, error) {
	cmd := m.command(ctx, path, args...)
	if dir != "" {
		cmd.Dir = dir
	}
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

// WriteSpaceMCPConfig writes harness MCP configs pointing at the den via
// `marmot serve --den <id>` (supported by every binary that passes the den
// probe). Never writes .marmot-vault. When MARMOT_HOME is set, it is embedded
// in each server env so MCP clients started without that env still resolve
// the same dens root.
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

	// VS Code: .vscode/mcp.json (servers key)
	vsServer := map[string]any{
		"type":    "stdio",
		"command": absBin,
		"args":    args,
	}
	if env, ok := server["env"]; ok {
		vsServer["env"] = env
	}
	// Preflight every existing JSON file before writing any of them. A malformed
	// user config must stop the update without being overwritten or leaving the
	// other harness configs partially updated.
	jsonConfigs := []struct {
		path   string
		mapKey string
		server map[string]any
		doc    map[string]any
	}{
		{path: filepath.Join(spacePath, ".mcp.json"), mapKey: "mcpServers", server: server},
		{path: filepath.Join(spacePath, ".cursor", "mcp.json"), mapKey: "mcpServers", server: server},
		{path: filepath.Join(spacePath, ".vscode", "mcp.json"), mapKey: "servers", server: vsServer},
	}
	for i := range jsonConfigs {
		doc, err := mergeJSONMCPServer(jsonConfigs[i].path, jsonConfigs[i].mapKey, jsonConfigs[i].server)
		if err != nil {
			return err
		}
		jsonConfigs[i].doc = doc
	}
	for _, dir := range []string{filepath.Join(spacePath, ".cursor"), filepath.Join(spacePath, ".vscode")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, config := range jsonConfigs {
		if err := writeJSONAtomic(config.path, config.doc); err != nil {
			return err
		}
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

// mergeJSONMCPServer loads one harness JSON config and replaces only its
// context-marmot server entry. Unrelated servers and top-level settings are
// preserved. Existing malformed or schema-incompatible documents are rejected
// so a write never destroys user configuration it cannot safely merge.
func mergeJSONMCPServer(path, mapKey string, server map[string]any) (map[string]any, error) {
	doc := make(map[string]any)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if doc == nil {
			return nil, fmt.Errorf("%s: expected a JSON object", path)
		}
	}
	servers := make(map[string]any)
	if existing, present := doc[mapKey]; present {
		var ok bool
		servers, ok = existing.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: %q must be a JSON object", path, mapKey)
		}
	}
	servers["context-marmot"] = server
	doc[mapKey] = servers
	return doc, nil
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
