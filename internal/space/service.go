package space

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"github.com/Nurozen/stave/internal/gh"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/memory"
	"github.com/Nurozen/stave/internal/tether"
)

type Git interface {
	FetchAllPrune(context.Context, string) error
	WorktreeAddBranch(context.Context, string, string, string, string) error
	WorktreeAddExisting(context.Context, string, string, string) error
	WorktreeAddDetached(context.Context, string, string, string) error
	WorktreeRemove(context.Context, string, string, bool) error
	WorktreePrune(context.Context, string) error
	CheckoutDetached(context.Context, string, string) error
	BranchExists(context.Context, string, string) (bool, error)
	IsAncestor(context.Context, string, string, string) (bool, error)
	RefExists(context.Context, string, string) (bool, error)
	RemoteNames(context.Context, string) ([]string, error)
	IsDirty(context.Context, string) (bool, string, error)
	AheadBehind(context.Context, string, string) (int, int, error)
}

type Service struct {
	Config config.Config
	Git    Git
	// Memory is the provider seam. Nil falls back to memory.FromConfig.
	Memory memory.Provider
	// MemoryFactory builds a provider by name; used by multi-provider --memory specs.
	// Nil uses memory.Lookup.
	MemoryFactory func(provider string) (memory.Provider, error)
	Out           io.Writer
	Now           func() time.Time
	// HasPortal, when set, reports whether a space has any portal attached
	// (memory is local-summon-only in v1 — attach prints a notice).
	HasPortal func(spaceID string) bool
	// PRLookup, when set, is the layer-2 merge probe: it lists the pull
	// requests whose head is headBranch in the repo cloned from cloneURL. nil
	// skips PR-based detection entirely (merge awareness degrades to commit
	// ancestry). The CLI wires it to internal/gh.
	PRLookup func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error)
	// onSyncReport, when set, observes every completed SyncWithReport on this
	// Service value; SagaSyncWithReport uses it to collect per-member rows
	// without changing SagaSync.
	onSyncReport func(SyncReport)
}

type InitOptions struct {
	ID       string
	Kind     string
	SpecPath string
	// Saga, when set, is written into the initial manifest so a saga space is
	// born v2 rather than upgraded after the fact. Only CreateSaga sets it.
	Saga *SagaManifest
	// viaSaga marks calls composed by CreateSaga; Kind == KindSaga is rejected
	// on any other path (impostor prevention).
	viaSaga bool
}

type RepoSpec struct {
	Name string
	Ref  string
}

type CreateOptions struct {
	ID         string
	Kind       string
	SpecPath   string
	Edits      []RepoSpec
	References []RepoSpec
	// CommonReferences are extra reference specs expanded from repo tethers
	// (-c/--common via ExpandCommonRefs). They get materialized as reference
	// worktrees exactly like References, but are deliberately NOT fed to
	// co-occurrence capture: expanded refs earn worktrees, they do not
	// reinforce the counts that produced them (OQ-E).
	CommonReferences []RepoSpec
	// NoLearn suppresses passive co-occurrence capture for this create
	// (--no-learn). The space is built normally; only the tether recording is
	// skipped.
	NoLearn bool
	// NoFetch skips the pre-add fetch of every repo's bare mirror, exactly as
	// AddOptions.NoFetch does for a single 'space add'. Scripted callers that
	// already synced the mirrors (or are deliberately building against a
	// pinned one) pay one fetch per repo otherwise.
	NoFetch bool
	// suppressCapture (unexported) blocks capture on the inner non-saga Create
	// composed by createInSaga, so the saga member's realized set is captured
	// exactly once, OUTSIDE the per-saga lock (D2).
	suppressCapture bool
	// Memories are raw `[provider:]<spec>` values from --memory (repeatable).
	// Empty with config memory.default:true triggers ambient attach.
	Memories []string
	// SkipAmbientMemory disables ambient memory.default attach (tests / explicit off).
	SkipAmbientMemory bool
	DryRun            bool
	// SagaID enrolls the new space as a member of that saga. Preflight,
	// creation and registration then run under ONE membership + per-saga lock
	// hold (see createInSaga). Named SagaID because Saga below already carries
	// the saga's own roster on the CreateSaga path.
	SagaID string
	// After lists the member ids the new member lands behind (requires
	// SagaID). It also drives the default-base rule: an edit spec with no
	// explicit base stacks on the branch of the single --after predecessor
	// editing the same repo.
	After []string
	// Saga mirrors InitOptions.Saga (only CreateSaga sets it).
	Saga *SagaManifest
	// viaSaga marks calls composed by CreateSaga (see InitOptions.viaSaga).
	viaSaga bool
	// sagaLockHeld marks that the caller (CreateSaga) already holds the
	// per-saga lock, so composed writers must not re-acquire it (fsio.WithLock
	// flock is non-reentrant across fds even within one process).
	sagaLockHeld bool
	// ownedMemoryLifetime is passed to AttachMemories.Lifetime (the fresh,
	// owned store's provider lifetime; "durable" for the saga-owned den).
	ownedMemoryLifetime string
}

type AddOptions struct {
	SpaceID  string
	RepoName string
	Mode     RepoMode
	Base     string
	Ref      string
	Branch   string
	// StartPoint optionally creates the edit branch at a different ref than
	// Base. Review spaces use it to check out a PR head while Base stays the
	// PR's target branch, so status drift reports the PR's ahead/behind.
	StartPoint string
	NoFetch    bool
	DryRun     bool
	// LinkMemory, when true and the space has memory attached, passes the
	// added reference repo through the provider seam (marmot resolve →
	// den link) so the den gains a read-only link (S4 §3.6 space add parity).
	// Soft: link failures print a notice, the repo is added regardless.
	LinkMemory bool
	// CaptureOnAdd records the delta co-occurrence tethers implied by this add
	// (D5). A plain `stave space add` sets it; Create's internal loops leave it
	// false so the whole realized set is captured once by captureCoOccurrence
	// (no double count).
	CaptureOnAdd bool
	// NoLearn suppresses the CaptureOnAdd delta recording (--no-learn).
	NoLearn bool
	// sagaLockHeld: the caller already holds this space's per-saga lock, so
	// the saga-manifest lock wiring must not re-acquire it (non-reentrant).
	sagaLockHeld bool
}

type SyncOptions struct {
	SpaceID        string
	ReferencesOnly bool
	// SkipFetch skips the per-repo FetchAllPrune. SagaSync sets it after its
	// own deduplicated fetch pass so shared bare repos fetch exactly once.
	SkipFetch bool
	// DryRun skips the generated-file writes (the CLAUDE.md link and
	// AGENTS.md); git-level dry-run printing is the injected client's job,
	// matching every other verb.
	DryRun bool
}

type ArchiveOptions struct {
	SpaceID string
	Force   bool
	DryRun  bool
	// MemoryFate: keep (default) or contribute (contribute-then-keep).
	// destroy is invalid for archive — use Destroy with FateDestroy.
	MemoryFate memory.MemoryFate // empty → keep
	// exemptDependentSpaces lists sibling space ids whose stacked bases must
	// not refuse this teardown: saga lifecycle retires them in the same
	// operation. Deliberately unexported — never set by the CLI; an EXTERNAL
	// dependent still refuses without Force.
	exemptDependentSpaces []string
}

// MemoryFate values: keep | destroy | contribute (default keep).
type DestroyOptions struct {
	SpaceID    string
	Force      bool
	DryRun     bool
	MemoryFate memory.MemoryFate // empty → keep
	// exemptDependentSpaces: see ArchiveOptions.exemptDependentSpaces.
	exemptDependentSpaces []string
	// sagaLockHeld: the caller (sagaTeardownLocked) already holds this space's
	// per-saga lock, so destroying-fate splice-saves in applyMemoryFate must
	// save directly rather than re-acquire it (fsio.WithLock's flock is
	// non-reentrant; see CreateOptions.sagaLockHeld).
	sagaLockHeld bool
	// skipMemoryStoreID names ONE attachment (by store id) the memory-fate
	// loop must skip: the saga den a den-first sagaTeardownLocked has already
	// destroyed (or, in dry-run, already previewed). Only that attachment is
	// skipped — the fate loop still runs over every other attachment, so
	// keep-fate cleanup of additional unowned attachments (route + MCP strip)
	// is never suppressed. Deliberately unexported: never set by the CLI.
	skipMemoryStoreID string
}

// AttachMemoryOptions is the service-level entry for stave memory attach
// and create --memory sugar.
type AttachMemoryOptions struct {
	SpaceID    string
	Provider   string // empty → config default
	UseID      string // attach existing
	Name       string
	EditRefs   []string
	LinkRefs   []string
	Opts       map[string]string
	DryRun     bool
	Strict     bool // true for explicit attach/--memory; false for ambient
	RawSpec    string
	References []RepoSpec // S4: pass raw url/path/ref through seam
	// Lifetime is passed to memory.AttachOptions.Lifetime ("task" default,
	// "durable" for the saga-owned den).
	Lifetime string
	// sagaLockHeld: the caller already holds this space's per-saga lock
	// (see AddOptions.sagaLockHeld).
	sagaLockHeld bool
}

type Status struct {
	Manifest Manifest
	Repos    []RepoStatus
}

type RepoStatus struct {
	Repo          RepoManifest
	Exists        bool
	Dirty         bool
	DirtyOutput   string
	Ahead         int
	Behind        int
	DriftError    string
	ReferenceWarn string
}

type DirtyWorktreeError struct {
	SpaceID string
	Repos   []string
}

func (e *DirtyWorktreeError) Error() string {
	return fmt.Sprintf("space %q has dirty editable worktrees: %s", e.SpaceID, strings.Join(e.Repos, ", "))
}

// MemoryInUseError classifies a provider source_in_use destroy refusal: the
// space's memory den is held by a live process, so destroy/detach cannot
// proceed and --force never helps (marmot's --force covers only
// unpushed_edits / unpushed_unknown). Unlike DirtyWorktreeError, Error()
// APPENDS the wrapped refusal so the literal source_in_use code and marmot's
// hint survive in rendered CLI output (cobra prints err.Error() only).
type MemoryInUseError struct {
	SpaceID string
	Err     error
}

func (e *MemoryInUseError) Error() string {
	return fmt.Sprintf("space %q: memory den is held by a live agent session or another marmot process (marmot serve --den): close agent sessions using this space's memory — or any space sharing its den (saga members do) — and retry (--force does not bypass): %v", e.SpaceID, e.Err)
}

func (e *MemoryInUseError) Unwrap() error { return e.Err }

// wrapSourceInUse classifies ONLY source_in_use provider refusals into
// MemoryInUseError; every other error (including other refusal codes such as
// edit_link_required) flows through verbatim.
func wrapSourceInUse(spaceID string, err error) error {
	if err == nil || !memory.IsSourceInUse(err) {
		return err
	}
	return &MemoryInUseError{SpaceID: spaceID, Err: err}
}

func ParseRepoSpec(raw string) (RepoSpec, error) {
	name, ref, found := strings.Cut(raw, ":")
	if err := config.ValidateName("repo name", name); err != nil {
		return RepoSpec{}, err
	}
	if found && strings.TrimSpace(ref) == "" {
		return RepoSpec{}, coded(CodeInvalidArguments, map[string]any{"spec": raw}, "repo spec %q has an empty ref", raw)
	}
	return RepoSpec{Name: name, Ref: ref}, nil
}

func DefaultBranch(spaceID, repoName string) string {
	return fmt.Sprintf("stave/%s/%s", spaceID, repoName)
}

// ResolveBaseRef expands base sugar and canonicalizes stave branch spellings:
//   - "space:<id>" resolves to the edit branch that space owns for repoName
//     ("refs/heads/stave/<id>/<repoName>"); an invalid id is an error.
//   - bare "stave/..." and "origin/stave/..." bases rewrite to
//     "refs/heads/stave/..." with changed=true so callers can warn:
//     normalizeRemoteRef would otherwise mint "origin/stave/...", which never
//     resolves because stave never pushes its branches.
//   - everything else passes through unchanged.
func ResolveBaseRef(base, repoName string) (resolved string, changed bool, err error) {
	base = strings.TrimSpace(base)
	if id, ok := strings.CutPrefix(base, "space:"); ok {
		if err := config.ValidateSpaceID(id); err != nil {
			return "", false, fmt.Errorf("base %q: %w", base, err)
		}
		return "refs/heads/" + DefaultBranch(id, repoName), false, nil
	}
	if branch, ok := strings.CutPrefix(base, "origin/"); ok && strings.HasPrefix(branch, "stave/") {
		return "refs/heads/" + branch, true, nil
	}
	if strings.HasPrefix(base, "stave/") {
		return "refs/heads/" + base, true, nil
	}
	return base, false, nil
}

func NewService(cfg config.Config, gitClient Git, out io.Writer) Service {
	if gitClient == nil {
		gitClient = git.New()
	}
	return Service{Config: cfg, Git: gitClient, Out: out}
}

func (s Service) InitSpace(ctx context.Context, opts InitOptions) error {
	if err := config.ValidateName("space id", opts.ID); err != nil {
		return err
	}
	if opts.Kind == KindSaga && !opts.viaSaga {
		return coded(CodeInvalidArguments, map[string]any{"kind": KindSaga}, "kind %q is reserved; use 'stave saga create'", KindSaga)
	}
	spacePath := s.SpacePath(opts.ID)
	_, statErr := os.Stat(spacePath)
	spaceAlreadyExisted := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.MkdirAll(spacePath, config.DefaultDirMode); err != nil {
		return err
	}

	manifestPath := filepath.Join(spacePath, ManifestName)
	if _, err := os.Stat(manifestPath); err == nil {
		manifest, err := LoadManifest(spacePath)
		if err != nil {
			return err
		}
		if manifest.ID != opts.ID {
			return fmt.Errorf("existing manifest id %q does not match %q", manifest.ID, opts.ID)
		}
		// Adoption never crosses the saga boundary: a plain create must not
		// "succeed" against a saga manifest (or vice versa) — the concurrent
		// same-ID creation race resolves to one winner and one clean error.
		if (manifest.Saga != nil) != (opts.Saga != nil) {
			if manifest.Saga != nil {
				return coded(CodeSpaceExists, map[string]any{"path": spacePath}, "space %q already exists as a saga", opts.ID)
			}
			return coded(CodeSpaceExists, map[string]any{"path": spacePath}, "space %q already exists and is not a saga", opts.ID)
		}
		if err := ensureClaudeLink(spacePath); err != nil {
			return err
		}
		return s.writeAgents(spacePath, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureClaudeLink(spacePath); err != nil {
		if !spaceAlreadyExisted {
			_ = os.Remove(spacePath)
		}
		return err
	}

	specPath := ""
	if opts.SpecPath != "" {
		source, err := filepath.Abs(opts.SpecPath)
		if err != nil {
			return err
		}
		if err := copySpec(source, filepath.Join(spacePath, "spec")); err != nil {
			return err
		}
		specPath = "spec"
	}
	manifest := Manifest{
		ID:        opts.ID,
		Kind:      opts.Kind,
		CreatedAt: s.now(),
		SpecPath:  specPath,
		Repos:     []RepoManifest{},
		Saga:      opts.Saga,
	}
	if err := saveManifestExclusive(spacePath, manifest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("space %q was created concurrently by another process", opts.ID)
		}
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("created space %s at %s\n", opts.ID, spacePath)
	return nil
}

func (s Service) Create(ctx context.Context, opts CreateOptions) error {
	if len(opts.After) > 0 && opts.SagaID == "" {
		return coded(CodeInvalidArguments, nil, "--after requires --saga")
	}
	if opts.SagaID != "" {
		if err := s.createInSaga(ctx, opts); err != nil {
			// Recovery-learning: the inner member Create is suppressed and the
			// success capture below never runs on error, so a member whose repos
			// are durably built but whose create failed (e.g. --memory attach)
			// would stay unlearned. Capture now — post-lock (createInSaga released
			// the locks), best-effort, only if the member is durably materialized.
			if !opts.DryRun && !opts.NoLearn && s.spaceHasRepos(opts.ID) {
				s.captureCoOccurrence(opts.Edits, opts.References)
			}
			return err
		}
		// Capture the realized set OUTSIDE the per-saga lock (D2): the inner
		// Create ran with suppressCapture set, so this is the only recording.
		if !opts.DryRun && !opts.NoLearn {
			s.captureCoOccurrence(opts.Edits, opts.References)
		}
		return nil
	}
	if opts.DryRun {
		return s.createDryRun(ctx, opts)
	}
	if err := s.InitSpace(ctx, InitOptions{ID: opts.ID, Kind: opts.Kind, SpecPath: opts.SpecPath, Saga: opts.Saga, viaSaga: opts.viaSaga}); err != nil {
		return err
	}
	for _, spec := range opts.Edits {
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeEdit, Base: spec.Ref, DryRun: opts.DryRun, NoFetch: opts.NoFetch, sagaLockHeld: opts.sagaLockHeld}); err != nil {
			return err
		}
	}
	for _, spec := range opts.References {
		// LinkMemory is a no-op here (memory attaches AFTER the repo loop and
		// passes the references itself); set for uniform semantics.
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeReference, Ref: spec.Ref, DryRun: opts.DryRun, NoFetch: opts.NoFetch, LinkMemory: true, sagaLockHeld: opts.sagaLockHeld}); err != nil {
			return err
		}
	}
	// Tether-expanded references (-c/--common) get worktrees exactly like
	// explicit references, but are excluded from capture below (OQ-E).
	for _, spec := range opts.CommonReferences {
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeReference, Ref: spec.Ref, DryRun: opts.DryRun, NoFetch: opts.NoFetch, LinkMemory: true, sagaLockHeld: opts.sagaLockHeld}); err != nil {
			return err
		}
	}
	// The repos are durably materialized before memory attach runs; capture is
	// tied to durable materialization, not command success (a failed explicit
	// --memory attach must not lose the co-occurrence record). Hold the attach
	// error, run capture, then surface it.
	memErr := s.attachMemoriesAfterCreate(ctx, opts)
	// Passive co-occurrence capture: only the explicit realized set (edits and
	// -r references), never the -c expansion (OQ-E). suppressCapture is set by
	// createInSaga so the saga path records once, outside the lock (D2).
	if !opts.DryRun && !opts.suppressCapture && !opts.NoLearn {
		s.captureCoOccurrence(opts.Edits, opts.References)
	}
	return memErr
}

func (s Service) createDryRun(ctx context.Context, opts CreateOptions) error {
	if err := config.ValidateName("space id", opts.ID); err != nil {
		return err
	}
	spacePath := s.SpacePath(opts.ID)
	s.printf("dry-run: create space directory %s\n", spacePath)
	s.printf("dry-run: write %s\n", filepath.Join(spacePath, ManifestName))
	s.printf("dry-run: write %s\n", filepath.Join(spacePath, AgentsName))
	s.printf("dry-run: link %s -> %s\n", filepath.Join(spacePath, ClaudeName), AgentsName)
	if opts.SpecPath != "" {
		source, err := filepath.Abs(opts.SpecPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(source); err != nil {
			return err
		}
		s.printf("dry-run: copy spec %s into %s\n", source, filepath.Join(spacePath, "spec"))
	}
	for _, spec := range opts.Edits {
		repoCfg, ok := s.Config.Repos[spec.Name]
		if !ok {
			return &RepoNotFoundError{Repo: spec.Name}
		}
		resolvedBase, _, err := ResolveBaseRef(spec.Ref, spec.Name)
		if err != nil {
			return err
		}
		baseRef, err := s.resolveRef(ctx, spec.Name, repoCfg.BareRepoPath, firstNonEmpty(resolvedBase, repoCfg.DefaultBranch, s.Config.DefaultBase))
		if err != nil {
			return err
		}
		if !opts.NoFetch {
			s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
		}
		s.printf("dry-run: add edit worktree %s from %s at %s\n", DefaultBranch(opts.ID, spec.Name), baseRef, filepath.Join(spacePath, spec.Name))
	}
	for _, spec := range opts.References {
		repoCfg, ok := s.Config.Repos[spec.Name]
		if !ok {
			return &RepoNotFoundError{Repo: spec.Name}
		}
		ref, err := s.resolveRef(ctx, spec.Name, repoCfg.BareRepoPath, firstNonEmpty(spec.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		if err != nil {
			return err
		}
		if !opts.NoFetch {
			s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
		}
		s.printf("dry-run: add reference worktree %s at %s\n", ref, filepath.Join(spacePath, "references", spec.Name))
	}
	for _, spec := range opts.CommonReferences {
		repoCfg, ok := s.Config.Repos[spec.Name]
		if !ok {
			return &RepoNotFoundError{Repo: spec.Name}
		}
		ref, err := s.resolveRef(ctx, spec.Name, repoCfg.BareRepoPath, firstNonEmpty(spec.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		if err != nil {
			return err
		}
		if !opts.NoFetch {
			s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
		}
		s.printf("dry-run: add reference worktree %s at %s (from repo tethers)\n", ref, filepath.Join(spacePath, "references", spec.Name))
	}
	// Memory dry-run lines (no binary invoke).
	return s.attachMemoriesAfterCreate(ctx, opts)
}

// attachMemoriesAfterCreate runs explicit --memory specs and/or ambient default.
//
// Memory linking sees BOTH explicit -r references AND tether-expanded
// -c/--common references (deduplicated by Name): every materialized reference
// worktree earns its read-only memory/vault link. This is deliberately wider
// than co-occurrence capture, which stays edits ∪ explicit references only
// (-c refs earn worktrees + memory links but do not reinforce counts — OQ-E).
func (s Service) attachMemoriesAfterCreate(ctx context.Context, opts CreateOptions) error {
	refs := append([]RepoSpec(nil), opts.References...)
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.Name] = true
	}
	for _, r := range opts.CommonReferences {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		refs = append(refs, r)
	}
	return s.AttachMemories(ctx, AttachMemoriesOptions{
		SpaceID:      opts.ID,
		Specs:        opts.Memories,
		References:   refs,
		SkipAmbient:  opts.SkipAmbientMemory,
		DryRun:       opts.DryRun,
		Lifetime:     opts.ownedMemoryLifetime,
		sagaLockHeld: opts.sagaLockHeld,
	})
}

// AttachMemoriesOptions drives AttachMemories, shared by space create and
// review setup (which builds spaces via InitSpace + AddRepo rather than Create).
type AttachMemoriesOptions struct {
	SpaceID string
	// Specs are raw `[provider:]<spec>` values from --memory (repeatable).
	// Non-empty specs attach strictly (failure aborts); empty specs with
	// config memory.default:true attach "." softly (notice on failure).
	Specs []string
	// References are passed through to the provider seam so marmot-side
	// resolution sees the space's read-only context repos.
	References  []RepoSpec
	SkipAmbient bool
	DryRun      bool
	// Lifetime is applied only to FRESH specs — the stores this space will own
	// ("durable" for the saga-owned den). Attach-existing specs never carry it.
	Lifetime string
	// sagaLockHeld: the caller already holds this space's per-saga lock.
	sagaLockHeld bool
}

// AttachMemories runs explicit memory specs and/or the ambient default with
// the explicit-fails-hard / ambient-soft-degrade policy.
func (s Service) AttachMemories(ctx context.Context, opts AttachMemoriesOptions) error {
	specs := opts.Specs
	strict := len(specs) > 0
	if len(specs) == 0 && !opts.SkipAmbient && s.Config.Memory.Default {
		specs = []string{"."}
		strict = false
	}
	// Prevalidate the whole batch BEFORE attaching anything so a bad batch
	// attaches nothing (G3): parse every spec and refuse duplicates that would
	// target the same store (two fresh specs on one provider both create the
	// space-id den; two identical ids collide outright).
	if strict {
		seen := map[string]string{}
		for _, raw := range specs {
			parsed, err := memory.ParseMemorySpec(raw, s.defaultMemoryProvider())
			if err != nil {
				return err
			}
			key := parsed.Provider + ":" + parsed.Spec
			if prev, dup := seen[key]; dup {
				return coded(CodeInvalidArguments, map[string]any{"specs": []string{prev, raw}, "provider": parsed.Provider}, "--memory specs %q and %q target the same store on provider %q; nothing attached", prev, raw, parsed.Provider)
			}
			seen[key] = raw
		}
	}
	for _, raw := range specs {
		lifetime := ""
		if opts.Lifetime != "" {
			// Lifetime applies only to fresh (to-be-owned) stores; a parse
			// failure is surfaced by AttachMemory itself.
			if parsed, err := memory.ParseMemorySpec(raw, s.defaultMemoryProvider()); err == nil && parsed.Fresh {
				lifetime = opts.Lifetime
			}
		}
		if err := s.AttachMemory(ctx, AttachMemoryOptions{
			SpaceID:      opts.SpaceID,
			RawSpec:      raw,
			DryRun:       opts.DryRun,
			Strict:       strict,
			References:   opts.References,
			Lifetime:     lifetime,
			sagaLockHeld: opts.sagaLockHeld,
		}); err != nil {
			if !strict {
				s.printf("notice: ambient memory attach failed: %v\n", err)
				s.printf("notice: equivalent: stave memory attach %s\n", opts.SpaceID)
				continue
			}
			return err
		}
	}
	return nil
}

// AttachMemory binds a memory store to a space via the provider seam, writes
// space-local MCP config, and appends a MemoryManifest record (owned when created).
func (s Service) AttachMemory(ctx context.Context, opts AttachMemoryOptions) error {
	if err := config.ValidateName("space id", opts.SpaceID); err != nil {
		return err
	}
	spacePath := s.SpacePath(opts.SpaceID)
	// Saga spaces serialize manifest writers under the per-saga lock; re-run
	// under it (re-loading inside) unless the caller already holds it.
	if !opts.DryRun && !opts.sagaLockHeld {
		if probe, err := LoadManifest(spacePath); err == nil && probe.Saga != nil {
			locked := opts
			locked.sagaLockHeld = true
			return s.withSagaLock(opts.SpaceID, func() error { return s.AttachMemory(ctx, locked) })
		}
	}
	providerName := opts.Provider
	useID := opts.UseID
	name := opts.Name

	if opts.RawSpec != "" {
		parsed, err := memory.ParseMemorySpec(opts.RawSpec, s.defaultMemoryProvider())
		if err != nil {
			return err
		}
		if providerName == "" {
			providerName = parsed.Provider
		}
		if !parsed.Fresh && useID == "" {
			useID = parsed.Spec
		}
	}
	if providerName == "" {
		providerName = s.defaultMemoryProvider()
	}
	// Default alias scheme (G3, documented in docs/memory.md): explicit --name
	// wins; attach-existing derives the alias from the store id; fresh stores
	// use "default". Derived aliases are uniquified against the manifest
	// (base, base-2, base-3, …) so repeatable --memory never collides.
	if name == "" {
		name = "default"
		if useID != "" {
			name = useID
		}
	}

	prov, err := s.provider(providerName)
	if err != nil {
		if opts.Strict {
			return err
		}
		return err
	}

	// Preflight: reject duplicate aliases / store ids BEFORE provider effects
	// so a failed retry does not leave an orphan den without a memories: entry.
	storeIDHint := useID
	if storeIDHint == "" {
		storeIDHint = opts.SpaceID
	}
	if !opts.DryRun {
		manifest, err := LoadManifest(spacePath)
		if err != nil {
			return err
		}
		if err := ensureClaudeLink(spacePath); err != nil {
			return err
		}
		if opts.Name == "" {
			name = uniqueAlias(name, manifest.Memories)
		}
		for _, existing := range manifest.Memories {
			if existing.Name == name || existing.ID == storeIDHint {
				return coded(CodeMemoryAttached, map[string]any{"space": opts.SpaceID, "memory": name}, "memory %q already attached to space %q", name, opts.SpaceID)
			}
		}
	}

	// Build reference specs for S4 pass-through (provider probes the marmot
	// binary and passes them as repeatable --ref, or drops them with a notice
	// on pre-P4 binaries).
	var refSpecs []memory.ReferenceSpec
	refs := opts.References
	if len(refs) == 0 && !opts.DryRun {
		if manifest, err := LoadManifest(spacePath); err == nil {
			for _, repo := range manifest.Repos {
				if repo.Mode != ModeReference {
					continue
				}
				refs = append(refs, RepoSpec{Name: repo.Name, Ref: repo.Ref})
			}
		}
	}
	for _, r := range refs {
		repoCfg := s.Config.Repos[r.Name]
		if repoCfg.MarmotVault == "off" {
			continue
		}
		refSpecs = append(refSpecs, memory.ReferenceSpec{
			Name:        r.Name,
			URL:         repoCfg.URL,
			Ref:         r.Ref,
			MarmotVault: repoCfg.MarmotVault,
		})
	}

	attachOpts := memory.AttachOptions{
		SpaceID:        opts.SpaceID,
		SpacePath:      spacePath,
		StoreID:        opts.SpaceID,
		UseID:          useID,
		Name:           name,
		Lifetime:       opts.Lifetime,
		EditRefs:       opts.EditRefs,
		LinkRefs:       opts.LinkRefs,
		ReferenceSpecs: refSpecs,
		Opts:           opts.Opts,
		DryRun:         opts.DryRun,
		Out:            s.Out,
		Strict:         opts.Strict,
	}
	result, err := prov.Attach(ctx, attachOpts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return nil
	}

	// Portal local-summon-only notice (v1).
	if s.HasPortal != nil && s.HasPortal(opts.SpaceID) {
		s.printf("notice: memory is local-summon-only in v1; portal-attached spaces do not mount dens into remote environments\n")
	}

	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	// Re-check after attach (another process may have raced).
	for _, existing := range manifest.Memories {
		if existing.Name == result.Name || existing.ID == result.StoreID {
			return coded(CodeMemoryAttached, map[string]any{"space": opts.SpaceID, "memory": result.Name}, "memory %q already attached to space %q", result.Name, opts.SpaceID)
		}
	}
	manifest.Memories = append(manifest.Memories, MemoryManifest{
		Name:     result.Name,
		Provider: firstNonEmpty(result.Provider, providerName),
		ID:       result.StoreID,
		Owned:    result.Owned,
	})
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	// Invariant: never leave .marmot-vault in the space.
	if _, err := os.Stat(filepath.Join(spacePath, ".marmot-vault")); err == nil {
		_ = os.Remove(filepath.Join(spacePath, ".marmot-vault"))
		s.printf("notice: removed unexpected .marmot-vault pointer from space (stave uses space-local MCP config)\n")
	}
	s.printf("attached memory %s (%s:%s, owned=%v) to %s\n", result.Name, providerName, result.StoreID, result.Owned, opts.SpaceID)
	return nil
}

// uniqueAlias returns base when free, else base-2, base-3, … (G3).
func uniqueAlias(base string, existing []MemoryManifest) string {
	taken := map[string]bool{}
	for _, mem := range existing {
		taken[mem.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

func (s Service) defaultMemoryProvider() string {
	mc := s.Config.Memory
	mc.ApplyDefaults()
	return mc.Provider
}

func (s Service) provider(name string) (memory.Provider, error) {
	if s.MemoryFactory != nil {
		return s.MemoryFactory(name)
	}
	if s.Memory != nil && (name == "" || name == s.Memory.Name() || name == s.defaultMemoryProvider()) {
		return s.Memory, nil
	}
	mc := s.Config.Memory
	mc.ApplyDefaults()
	return memory.Lookup(name, mc)
}

func (s Service) AddRepo(ctx context.Context, opts AddOptions) error {
	if err := config.ValidateName("space id", opts.SpaceID); err != nil {
		return err
	}
	if err := config.ValidateName("repo name", opts.RepoName); err != nil {
		return err
	}
	if opts.Mode != ModeEdit && opts.Mode != ModeReference {
		return coded(CodeInvalidArguments, map[string]any{"mode": string(opts.Mode)}, "repo mode must be edit or reference")
	}
	repoCfg, ok := s.Config.Repos[opts.RepoName]
	if !ok {
		return &RepoNotFoundError{Repo: opts.RepoName}
	}
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := loadLiveManifest(opts.SpaceID, spacePath)
	if err != nil {
		return err
	}
	// The same repo may be present once as an edit AND once as a reference
	// (compare a branch against its base); only a second entry in the SAME
	// mode is refused, since it would make remove and AGENTS.md ambiguous.
	for _, existing := range manifest.Repos {
		if existing.Name == opts.RepoName && existing.Mode == opts.Mode {
			return &RepoAlreadyInSpaceError{SpaceID: opts.SpaceID, Repo: opts.RepoName, Mode: existing.Mode}
		}
	}
	// Snapshot the repo slice BEFORE the new entry is appended: captureDeltaOnAdd
	// records only pairs involving the added repo, against this prior set (D13).
	priorRepos := append([]RepoManifest(nil), manifest.Repos...)
	if manifest.Saga != nil {
		if opts.Mode == ModeEdit {
			return coded(CodeSagaSpace, map[string]any{"space": opts.SpaceID}, "saga space %q holds no edit worktrees; add the repo to a member space instead", opts.SpaceID)
		}
		// Saga spaces serialize manifest writers under the per-saga lock;
		// re-run under it (re-loading inside) unless the caller holds it.
		if !opts.DryRun && !opts.sagaLockHeld {
			locked := opts
			locked.sagaLockHeld = true
			return s.withSagaLock(opts.SpaceID, func() error { return s.AddRepo(ctx, locked) })
		}
	}
	if !opts.DryRun {
		if err := ensureClaudeLink(spacePath); err != nil {
			return err
		}
	}
	if !opts.NoFetch && opts.DryRun {
		s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
	} else if !opts.NoFetch {
		if err := s.Git.FetchAllPrune(ctx, repoCfg.BareRepoPath); err != nil {
			return err
		}
	}

	var entry RepoManifest
	switch opts.Mode {
	case ModeEdit:
		resolvedBase, baseChanged, err := ResolveBaseRef(opts.Base, opts.RepoName)
		if err != nil {
			return err
		}
		baseIsSugar := strings.HasPrefix(opts.Base, "space:")
		if baseChanged && !opts.DryRun {
			s.printf("notice: base %q canonicalized to %q (stave branches live only in the bare repo; %q would never resolve)\n", opts.Base, resolvedBase, "origin/"+strings.TrimPrefix(resolvedBase, "refs/heads/"))
		}
		baseRef, err := s.resolveRef(ctx, opts.RepoName, repoCfg.BareRepoPath, firstNonEmpty(resolvedBase, repoCfg.DefaultBranch, s.Config.DefaultBase))
		if err != nil {
			// "space:<id>" sugar keeps its own phrasing: the caller named a
			// sibling space, not a ref, and should hear about it that way.
			var notFound *RefNotFoundError
			if baseIsSugar && errors.As(err, &notFound) {
				return coded(CodeRefNotFound, notFound.Details(), "base %q: space %q has no branch %q for repo %q",
					opts.Base, strings.TrimPrefix(opts.Base, "space:"), strings.TrimPrefix(notFound.Tried, "refs/heads/"), opts.RepoName)
			}
			return err
		}
		branch := firstNonEmpty(opts.Branch, DefaultBranch(opts.SpaceID, opts.RepoName))
		repoPath := opts.RepoName
		if manifest.HasPath(repoPath) {
			return &RepoPathTakenError{SpaceID: opts.SpaceID, Repo: opts.RepoName, Path: repoPath}
		}
		startPoint := baseRef
		if opts.StartPoint != "" {
			if startPoint, err = s.resolveRef(ctx, opts.RepoName, repoCfg.BareRepoPath, opts.StartPoint); err != nil {
				return err
			}
		}
		worktreePath := filepath.Join(spacePath, repoPath)
		if !opts.DryRun {
			exists, err := s.Git.BranchExists(ctx, repoCfg.BareRepoPath, branch)
			if err != nil {
				return err
			}
			if exists {
				err = s.Git.WorktreeAddExisting(ctx, repoCfg.BareRepoPath, worktreePath, branch)
			} else {
				err = s.Git.WorktreeAddBranch(ctx, repoCfg.BareRepoPath, worktreePath, branch, startPoint)
			}
			if err != nil {
				return err
			}
			if exists {
				s.printf("warning: branch %q already exists; the worktree adopted its current head, so the requested base/start-point was ignored and the recorded base %s is aspirational\n", branch, baseRef)
				if ahead, behind, driftErr := s.Git.AheadBehind(ctx, worktreePath, baseRef); driftErr == nil {
					s.printf("warning: adopted branch is ahead %d, behind %d versus %s\n", ahead, behind, baseRef)
				}
			}
		} else {
			s.printf("dry-run: add edit worktree %s from %s at %s\n", branch, startPoint, worktreePath)
		}
		entry = RepoManifest{Name: opts.RepoName, Mode: ModeEdit, Path: repoPath, Base: baseRef, Branch: branch, BareRepoPath: repoCfg.BareRepoPath}
	case ModeReference:
		ref, err := s.resolveRef(ctx, opts.RepoName, repoCfg.BareRepoPath, firstNonEmpty(opts.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		if err != nil {
			return err
		}
		repoPath := filepath.Join("references", opts.RepoName)
		if manifest.HasPath(repoPath) {
			return &RepoPathTakenError{SpaceID: opts.SpaceID, Repo: opts.RepoName, Path: repoPath}
		}
		worktreePath := filepath.Join(spacePath, repoPath)
		if !opts.DryRun {
			if err := os.MkdirAll(filepath.Dir(worktreePath), config.DefaultDirMode); err != nil {
				return err
			}
			if err := s.Git.WorktreeAddDetached(ctx, repoCfg.BareRepoPath, worktreePath, ref); err != nil {
				return err
			}
		} else {
			s.printf("dry-run: add reference worktree %s at %s\n", ref, worktreePath)
		}
		entry = RepoManifest{Name: opts.RepoName, Mode: ModeReference, Path: repoPath, Ref: ref, BareRepoPath: repoCfg.BareRepoPath}
	}

	if opts.DryRun {
		s.linkMemoryOnAdd(ctx, spacePath, manifest, opts, repoCfg, entry)
		return nil
	}
	manifest.Repos = append(manifest.Repos, entry)
	sort.Slice(manifest.Repos, func(i, j int) bool { return manifest.Repos[i].Path < manifest.Repos[j].Path })
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	// Delta co-occurrence capture (D5): after the manifest is durably saved,
	// before best-effort AGENTS.md / memory linking. Best-effort itself.
	if !opts.DryRun && opts.CaptureOnAdd && !opts.NoLearn {
		addedMode := tether.ModeReference
		if opts.Mode == ModeEdit {
			addedMode = tether.ModeEdit
		}
		s.captureDeltaOnAdd(priorRepos, opts.RepoName, addedMode)
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("added %s repo %s to %s\n", opts.Mode, opts.RepoName, opts.SpaceID)
	// Memory linking runs AFTER the manifest save: a link failure must never
	// lose the repo entry (linking is soft, the add already succeeded).
	s.linkMemoryOnAdd(ctx, spacePath, manifest, opts, repoCfg, entry)
	return nil
}

// linkMemoryOnAdd resolves a newly added reference repo into read-only memory
// links on every attachment whose provider supports the ReferenceLinker seam
// (S4 §3.6 space add parity). Soft by design: any failure prints a notice —
// the repo is already added and stays added.
func (s Service) linkMemoryOnAdd(ctx context.Context, spacePath string, manifest Manifest, opts AddOptions, repoCfg config.Repository, entry RepoManifest) {
	if !opts.LinkMemory || opts.Mode != ModeReference || len(manifest.Memories) == 0 {
		return
	}
	if repoCfg.MarmotVault == "off" {
		return // config suppression, same as attach-time filtering
	}
	spec := memory.ReferenceSpec{
		Name:        opts.RepoName,
		URL:         repoCfg.URL,
		Ref:         entry.Ref,
		MarmotVault: repoCfg.MarmotVault,
	}
	for _, mem := range manifest.Memories {
		prov, err := s.provider(mem.Provider)
		if err != nil {
			s.printf("notice: memory %s: %v; reference %s added without a memory link\n", mem.Name, err, opts.RepoName)
			continue
		}
		linker, ok := prov.(memory.ReferenceLinker)
		if !ok {
			continue
		}
		if _, err := linker.LinkReference(ctx, memory.LinkReferenceOptions{
			StoreID:   mem.ID,
			SpacePath: spacePath,
			Spec:      spec,
			DryRun:    opts.DryRun,
			Out:       s.Out,
		}); err != nil {
			s.printf("notice: memory %s link failed: %v; reference %s added without a memory link\n", mem.Name, err, opts.RepoName)
		}
	}
}

// SyncRepoResult is one per-repo row of a space sync: what happened to the
// worktree and, for edit repos, the drift versus the recorded base.
type SyncRepoResult struct {
	Name string   `json:"name"`
	Mode RepoMode `json:"mode"`
	// Action is one of: "fetched" (bare repo fetched, nothing else reported —
	// an edit repo whose drift probe failed), "updated" (reference checked out
	// to its ref), "skipped" (dirty reference left alone), "drift-reported"
	// (edit repo with Ahead/Behind populated).
	Action string `json:"action"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	Note   string `json:"note,omitempty"`
}

// Sync row actions.
const (
	SyncActionFetched       = "fetched"
	SyncActionUpdated       = "updated"
	SyncActionSkipped       = "skipped"
	SyncActionDriftReported = "drift-reported"
)

// SyncReport is the structured result of SyncWithReport. Lines holds the
// exact human lines the rows account for, so a --json caller can subtract
// them from captured output and keep only the remaining notices.
type SyncReport struct {
	SpaceID   string           `json:"spaceId"`
	SpacePath string           `json:"spacePath"`
	Manifest  Manifest         `json:"manifest"`
	Repos     []SyncRepoResult `json:"repos"`
	Lines     []string         `json:"-"`
}

// Sync fetches a space's repos, refreshes reference checkouts, and reports
// edit drift. It is SyncWithReport minus the structured result.
func (s Service) Sync(ctx context.Context, opts SyncOptions) error {
	_, err := s.SyncWithReport(ctx, opts)
	return err
}

// SyncWithReport is Sync returning one row per processed repo. Human output
// is unchanged; repos filtered out by ReferencesOnly are not reported, just
// as they print nothing.
func (s Service) SyncWithReport(ctx context.Context, opts SyncOptions) (SyncReport, error) {
	spacePath, err := s.resolveSpacePath(opts.SpaceID)
	if err != nil {
		return SyncReport{}, err
	}
	manifest, err := loadLiveManifest(opts.SpaceID, spacePath)
	if err != nil {
		return SyncReport{}, err
	}
	report := SyncReport{SpaceID: opts.SpaceID, SpacePath: spacePath, Manifest: manifest, Repos: []SyncRepoResult{}}
	if !opts.DryRun {
		if err := ensureClaudeLink(spacePath); err != nil {
			return SyncReport{}, err
		}
	}
	for _, repo := range manifest.Repos {
		if opts.ReferencesOnly && repo.Mode != ModeReference {
			continue
		}
		if !opts.SkipFetch {
			if err := s.Git.FetchAllPrune(ctx, repo.BareRepoPath); err != nil {
				return SyncReport{}, err
			}
		}
		worktreePath := filepath.Join(spacePath, repo.Path)
		row := SyncRepoResult{Name: repo.Name, Mode: repo.Mode}
		var line string
		switch repo.Mode {
		case ModeReference:
			dirty, _, err := s.Git.IsDirty(ctx, worktreePath)
			if err != nil {
				return SyncReport{}, err
			}
			if dirty {
				row.Action = SyncActionSkipped
				row.Note = "reference worktree is dirty; checkout skipped"
				line = fmt.Sprintf("reference %s is dirty; skipped checkout", repo.Name)
				break
			}
			if err := s.Git.CheckoutDetached(ctx, worktreePath, repo.Ref); err != nil {
				return SyncReport{}, err
			}
			row.Action = SyncActionUpdated
			line = fmt.Sprintf("updated reference %s to %s", repo.Name, repo.Ref)
		case ModeEdit:
			ahead, behind, err := s.Git.AheadBehind(ctx, worktreePath, repo.Base)
			if err != nil {
				row.Action = SyncActionFetched
				row.Note = fmt.Sprintf("drift unknown: %v", err)
				line = fmt.Sprintf("edit %s drift unknown: %v", repo.Name, err)
				break
			}
			row.Action, row.Ahead, row.Behind = SyncActionDriftReported, ahead, behind
			line = fmt.Sprintf("edit %s: ahead %d, behind %d versus %s", repo.Name, ahead, behind, repo.Base)
		}
		if line != "" {
			s.printf("%s\n", line)
			report.Lines = append(report.Lines, line)
		}
		report.Repos = append(report.Repos, row)
	}
	if !opts.DryRun {
		if err := s.writeAgents(spacePath, manifest); err != nil {
			return SyncReport{}, err
		}
	}
	if s.onSyncReport != nil {
		s.onSyncReport(report)
	}
	return report, nil
}

func (s Service) Status(ctx context.Context, spaceID string) (Status, error) {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return Status{}, err
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return Status{}, err
	}
	status := Status{Manifest: manifest}
	for _, repo := range manifest.Repos {
		worktreePath := filepath.Join(spacePath, repo.Path)
		repoStatus := RepoStatus{Repo: repo}
		if _, err := os.Stat(worktreePath); err == nil {
			repoStatus.Exists = true
			repoStatus.Dirty, repoStatus.DirtyOutput, err = s.Git.IsDirty(ctx, worktreePath)
			if err != nil {
				return Status{}, err
			}
			if repo.Mode == ModeEdit {
				repoStatus.Ahead, repoStatus.Behind, err = s.Git.AheadBehind(ctx, worktreePath, repo.Base)
				if err != nil {
					repoStatus.DriftError = err.Error()
				}
			} else if repoStatus.Dirty {
				repoStatus.ReferenceWarn = "reference worktree is dirty"
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Status{}, err
		}
		status.Repos = append(status.Repos, repoStatus)
	}
	return status, nil
}

// Retarget updates the recorded Base of an edit repo — the ref drift reports
// against — without touching the worktree. Base sugar ("space:<id>") and
// stave branch spellings resolve exactly as they do for AddRepo; resolved
// stave branches must exist in the bare repo before the manifest is rewritten.
// dryRun previews the retarget without checking branches or saving.
func (s Service) Retarget(ctx context.Context, spaceID, repoName, base string, dryRun bool) error {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(base) == "" {
		return coded(CodeInvalidArguments, map[string]any{"space": spaceID, "repo": repoName}, "a base ref is required")
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	repo, idx, ok := manifest.FindRepo(repoName)
	if !ok {
		return &RepoNotInSpaceError{SpaceID: spaceID, Repo: repoName}
	}
	if repo.Mode != ModeEdit {
		return &RepoNotInSpaceError{SpaceID: spaceID, Repo: repoName, Mode: ModeEdit}
	}
	resolved, changed, err := ResolveBaseRef(base, repoName)
	if err != nil {
		return err
	}
	if changed && !dryRun {
		s.printf("notice: base %q canonicalized to %q (stave branches live only in the bare repo; %q would never resolve)\n", base, resolved, "origin/"+strings.TrimPrefix(resolved, "refs/heads/"))
	}
	// Resolution (non-origin remotes included) and the existence check are the
	// same ones AddRepo applies, so a base that retarget accepts is one a
	// worktree could actually be built from. Under --dry-run the manifest is
	// not written, so an unresolvable base is previewed, not refused.
	baseRef, err := s.resolveRef(ctx, repoName, repo.BareRepoPath, resolved)
	if err != nil {
		if !dryRun {
			return err
		}
		baseRef = normalizeRemoteRef(resolved)
		s.printf("warning: base %s\n", err)
	}
	if dryRun {
		s.printf("dry-run: retarget %s repo %s to base %s\n", spaceID, repoName, baseRef)
		return nil
	}
	manifest.Repos[idx].Base = baseRef
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("retargeted %s repo %s to base %s\n", spaceID, repoName, baseRef)
	return nil
}

func (s Service) Archive(ctx context.Context, opts ArchiveOptions) error {
	spacePath, err := s.resolveSpacePath(opts.SpaceID)
	if err != nil {
		return err
	}
	manifest, err := loadLiveManifest(opts.SpaceID, spacePath)
	if err != nil {
		return err
	}
	if !opts.Force {
		if err := s.guardRefusal(s.ensureNoDirtyEdits(ctx, spacePath, manifest), opts.DryRun); err != nil {
			return err
		}
		if err := s.guardRefusal(s.ensureNoDependentSpaces(opts.SpaceID, manifest, opts.exemptDependentSpaces), opts.DryRun); err != nil {
			return err
		}
	}
	fate := opts.MemoryFate
	if fate == "" {
		fate = memory.FateKeep
	}
	if fate == memory.FateDestroy {
		return coded(CodeInvalidArguments, nil, "archive does not destroy memory; use 'stave space destroy --memory destroy' instead")
	}
	if fate == memory.FateContribute {
		// Contribute-then-keep: propose for EVERY attachment (contribute is
		// non-destructive, so owned:false participates too) BEFORE any
		// mutation. A propose failure aborts with the space untouched.
		for _, mem := range manifest.Memories {
			prov, err := s.provider(mem.Provider)
			if err != nil {
				return fmt.Errorf("memory %s: %w", mem.Name, err)
			}
			res, err := prov.Propose(ctx, memory.ProposeOptions{StoreID: mem.ID, SpacePath: spacePath, DryRun: opts.DryRun, Out: s.Out})
			if err != nil {
				return fmt.Errorf("memory %s contribute failed; archive aborted (space untouched): %w", mem.Name, err)
			}
			if !opts.DryRun {
				memory.PrintProposeOutcome(s.Out, res)
			}
			if res.Summary != "" {
				s.printf("memory %s: %s\n", mem.Name, res.Summary)
			}
		}
	}
	// Compute archive dest first so reverse-route relocation knows NewSpacePath.
	archiveRoot := filepath.Join(s.Config.AgentWorkDir, ".archive")
	dest := filepath.Join(archiveRoot, opts.SpaceID)
	if _, err := os.Stat(dest); err == nil {
		dest = fmt.Sprintf("%s-%s", dest, time.Now().Format("20060102150405"))
	}

	for _, repo := range manifest.Repos {
		if opts.DryRun {
			s.printf("dry-run: remove worktree %s\n", filepath.Join(spacePath, repo.Path))
			continue
		}
		if err := s.Git.WorktreeRemove(ctx, repo.BareRepoPath, filepath.Join(spacePath, repo.Path), true); err != nil {
			return err
		}
		if err := s.Git.WorktreePrune(ctx, repo.BareRepoPath); err != nil {
			return err
		}
	}
	// Resolve symlinks in the old path WHILE IT STILL EXISTS: marmot normalizes
	// route keys via EvalSymlinks, which cannot resolve the path after the
	// rename (macOS $TMPDIR-style symlinked roots would then miss the route).
	routeFrom := spacePath
	if resolved, err := filepath.EvalSymlinks(spacePath); err == nil {
		routeFrom = resolved
	}
	if opts.DryRun {
		s.printf("dry-run: archive %s to %s\n", spacePath, dest)
		s.relocateMemoryRoutes(ctx, routeFrom, dest, manifest, true)
		return nil
	}
	if err := os.MkdirAll(archiveRoot, config.DefaultDirMode); err != nil {
		return err
	}
	if err := os.Rename(spacePath, dest); err != nil {
		return err
	}
	// Route relocation is a SPACE-level operation (the route is keyed by the
	// space path, not by attachment), so it runs ONCE per provider AFTER the
	// rename. Rename-first means a route-update failure leaves the space
	// archived with a stale route — recoverable, loudly warned — instead of
	// the old failure mode: routing mutated but the archive aborted.
	s.relocateMemoryRoutes(ctx, routeFrom, dest, manifest, false)
	s.printf("archived %s to %s\n", opts.SpaceID, dest)
	return nil
}

// relocateMemoryRoutes rewrites the reverse route (old space path → new path)
// once per provider. Dens are untouched; failures warn instead of aborting
// (the route can be repaired with `marmot route set-project --from … --to …`).
func (s Service) relocateMemoryRoutes(ctx context.Context, oldPath, newPath string, manifest Manifest, dryRun bool) {
	seen := map[string]bool{}
	for _, mem := range manifest.Memories {
		if seen[mem.Provider] {
			continue
		}
		seen[mem.Provider] = true
		prov, err := s.provider(mem.Provider)
		if err != nil {
			s.printf("warning: memory %s: %v; reverse route may still point at %s (repair: marmot route set-project --from %s --to %s)\n", mem.Name, err, oldPath, oldPath, newPath)
			continue
		}
		if _, err := prov.Detach(ctx, memory.DetachOptions{
			StoreID:      mem.ID,
			SpacePath:    oldPath,
			NewSpacePath: newPath,
			Fate:         memory.FateKeep,
			Owned:        mem.Owned,
			DryRun:       dryRun,
			Out:          s.Out,
		}); err != nil {
			s.printf("warning: memory %s route relocation failed: %v; space archived at %s but the reverse route may still point at %s (repair: marmot route set-project --from %s --to %s)\n", mem.Name, err, newPath, oldPath, oldPath, newPath)
		}
	}
}

func (s Service) Destroy(ctx context.Context, opts DestroyOptions) error {
	spacePath, err := s.resolveSpacePath(opts.SpaceID)
	if err != nil {
		return err
	}
	manifest, err := loadLiveManifest(opts.SpaceID, spacePath)
	if err != nil {
		return err
	}
	if !opts.Force {
		if err := s.guardRefusal(s.ensureNoDirtyEdits(ctx, spacePath, manifest), opts.DryRun); err != nil {
			return err
		}
		if err := s.guardRefusal(s.ensureNoDependentSpaces(opts.SpaceID, manifest, opts.exemptDependentSpaces), opts.DryRun); err != nil {
			return err
		}
	}
	fate := opts.MemoryFate
	if fate == "" {
		fate = memory.FateKeep
	}
	// Memory fate BEFORE RemoveAll — manifest is gone after.
	if err := s.applyMemoryFate(ctx, spacePath, opts.SpaceID, &manifest, fate, opts.Force, opts.DryRun, opts.sagaLockHeld, opts.skipMemoryStoreID); err != nil {
		return err
	}
	for _, repo := range manifest.Repos {
		worktreePath := filepath.Join(spacePath, repo.Path)
		if opts.DryRun {
			s.printf("dry-run: remove worktree %s\n", worktreePath)
			continue
		}
		if err := s.Git.WorktreeRemove(ctx, repo.BareRepoPath, worktreePath, opts.Force); err != nil {
			return err
		}
		if err := s.Git.WorktreePrune(ctx, repo.BareRepoPath); err != nil {
			return err
		}
	}
	if opts.DryRun {
		s.printf("dry-run: remove directory %s\n", spacePath)
		return nil
	}
	if err := os.RemoveAll(spacePath); err != nil {
		return err
	}
	s.printf("destroyed %s\n", opts.SpaceID)
	return nil
}

// applyMemoryFate runs provider Detach for each attachment (space destroy).
// owned:false never destroyed. The reverse-route removal is a SPACE-level
// operation (one route per space path), so RemoveRoute is issued at most once
// per provider rather than once per attachment — a second `route rm --project`
// would fail on the already-removed route (G1). The once-per-provider map is
// per-RUN state: a retry after a partial failure re-issues route rm for a
// keep-fate attachment whose route the previous run already removed, which
// marmot tolerates with a warning rather than a failure (route commands are
// warn-tolerant), so cross-retry double-removal still converges.
//
// Destroying fates record durably as they go: each successful (or
// den_not_found-tolerated) destroy splices its attachment from the manifest,
// saves it and rewrites AGENTS.md before the next attachment is touched, so a
// mid-loop failure leaves the manifest listing exactly the unprocessed
// attachments — a retry never re-destroys (or re-contributes) a den that is
// already gone. Keep-fate attachments are never spliced: the whole manifest
// disappears with the space, and a retry's redundant keep-detach is harmless.
// Dry-run never saves.
//
// skipStoreID names one already-handled attachment (the saga den destroyed
// den-first by sagaTeardownLocked) to skip in BOTH real and dry-run paths; in
// real runs its splice-save usually removed the record already, so the skip
// mainly keeps a dry-run from previewing the den's destroy lines twice.
func (s Service) applyMemoryFate(ctx context.Context, spacePath, spaceID string, manifest *Manifest, fate memory.MemoryFate, force, dryRun, sagaLockHeld bool, skipStoreID string) error {
	if len(manifest.Memories) == 0 {
		return nil
	}
	if fate == "" {
		fate = memory.FateKeep
	}
	if !dryRun && fate == memory.FateKeep {
		s.printf("memory fate=keep (default): dens retained as durable residue of this task\n")
	}
	routeRemoved := map[string]bool{}
	// Iterate a snapshot: destroying-fate successes splice manifest.Memories
	// in place (head-consume of the durable attachment list).
	memories := append([]MemoryManifest(nil), manifest.Memories...)
	for _, mem := range memories {
		if skipStoreID != "" && mem.ID == skipStoreID {
			continue // handled den-first by the saga teardown
		}
		effective := fate
		if !mem.Owned && (fate == memory.FateDestroy || fate == memory.FateContribute) {
			s.printf("notice: memory %q (%s) is not owned; detach-only (never destroy)\n", mem.Name, mem.ID)
			effective = memory.FateKeep
		}
		prov, err := s.provider(mem.Provider)
		if err != nil {
			return fmt.Errorf("memory %s: %w", mem.Name, err)
		}
		detachOpts := memory.DetachOptions{
			StoreID:   mem.ID,
			SpacePath: spacePath,
			Fate:      effective,
			Force:     force,
			Owned:     mem.Owned,
			DryRun:    dryRun,
			Out:       s.Out,
		}
		if effective == memory.FateKeep {
			if !routeRemoved[mem.Provider] {
				detachOpts.RemoveRoute = true
				routeRemoved[mem.Provider] = true
			}
			if _, err := prov.Detach(ctx, detachOpts); err != nil {
				return fmt.Errorf("memory %s fate %s: %w", mem.Name, effective, wrapSourceInUse(spaceID, err))
			}
			continue
		}
		// Direct `stave space destroy <saga> --memory destroy|contribute` lands
		// here instead of detachMemoryLocked, and owes the members the same
		// pre-destroy strip of the saga den's MCP wiring (a member client
		// started from a stale config could re-acquire the den mid-destroy) and
		// the same restore when the destroy is refused. No extra lock: the
		// strip reads member manifests and writes member-side configs only —
		// the saga manifest is touched exclusively by detachDestroyingAndRecord,
		// which takes the per-saga lock itself when sagaLockHeld is false.
		var rewireMembers func(cause error)
		if !dryRun && manifest.Saga != nil {
			rewireMembers = s.stripSagaDenMemberWiring(ctx, *manifest, mem)
		}
		if err := s.detachDestroyingAndRecord(ctx, prov, detachOpts, spacePath, spaceID, manifest, mem, sagaLockHeld); err != nil {
			if rewireMembers != nil {
				rewireMembers(err)
			}
			return fmt.Errorf("memory %s fate %s: %w", mem.Name, effective, wrapSourceInUse(spaceID, err))
		}
	}
	return nil
}

// detachDestroyingAndRecord runs ONE destroying-fate (destroy | contribute)
// provider Detach and records the outcome durably in the same per-success
// transaction: the attachment is spliced from manifest.Memories, the manifest
// saved, and AGENTS.md rewritten (it must not keep advertising a destroyed
// den — the space directory still exists until Destroy's final RemoveAll).
// Shared by applyMemoryFate and detachMemoryLocked, whose crash window is
// identical: provider Detach succeeds, then the process dies before the
// manifest save — without the den_not_found tolerance below every retry
// would wedge on the vanished den.
//
// den_not_found tolerance: FateDestroy treats the refusal unconditionally as
// already-done (the desired end state — no den — holds). FateContribute
// cannot know whether the vanished den's content was ever contributed, so its
// notice says exactly that; the record is still spliced so retries converge.
//
// Save transaction: with manifest.Saga != nil and the per-saga lock NOT held
// (direct `space destroy <saga> --force`), the splice-save runs as a locked
// reload → remove-by-StoreID → save, so a concurrent saga writer's rows (e.g.
// saga sync's PR cache) are never clobbered by this caller's pre-loop
// snapshot. With the lock held (sagaTeardownLocked, detachMemoryLocked) — or
// on a plain space — the caller's manifest is spliced and saved directly.
// Dry-run never saves.
func (s Service) detachDestroyingAndRecord(ctx context.Context, prov memory.Provider, detachOpts memory.DetachOptions, spacePath, spaceID string, manifest *Manifest, mem MemoryManifest, sagaLockHeld bool) error {
	if _, err := prov.Detach(ctx, detachOpts); err != nil {
		switch {
		case !memory.IsDenNotFound(err):
			return err
		case detachOpts.Fate == memory.FateContribute:
			s.printf("notice: memory %q (%s): den vanished before its contribution could be verified; removing the attachment record so retries converge\n", mem.Name, mem.ID)
		default: // FateDestroy — the desired end state (no den) already holds.
			s.printf("notice: memory %q (%s): den not found; already destroyed — treating as done\n", mem.Name, mem.ID)
		}
	}
	if detachOpts.DryRun {
		return nil
	}
	splice := func(m *Manifest) bool {
		for i, cand := range m.Memories {
			if cand.ID == mem.ID {
				m.Memories = append(m.Memories[:i], m.Memories[i+1:]...)
				return true
			}
		}
		return false
	}
	if manifest.Saga != nil && !sagaLockHeld {
		splice(manifest) // keep the caller's in-memory snapshot in step
		return s.withSagaLock(spaceID, func() error {
			fresh, err := LoadManifest(spacePath)
			if err != nil {
				return err
			}
			if splice(&fresh) {
				if err := SaveManifest(spacePath, fresh); err != nil {
					return err
				}
			}
			return s.writeAgents(spacePath, fresh)
		})
	}
	if splice(manifest) {
		if err := SaveManifest(spacePath, *manifest); err != nil {
			return err
		}
	}
	return s.writeAgents(spacePath, *manifest)
}

// DetachMemory removes one attachment from the manifest after provider Detach.
// Saga spaces serialize manifest writers under the per-saga lock; callers that
// already hold it (lifecycle walks) use detachMemoryLocked instead.
func (s Service) DetachMemory(ctx context.Context, spaceID, alias string, fate memory.MemoryFate, force, dryRun bool) error {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	if !dryRun {
		if probe, err := LoadManifest(spacePath); err == nil && probe.Saga != nil {
			return s.withSagaLock(spaceID, func() error {
				return s.detachMemoryLocked(ctx, spacePath, spaceID, alias, fate, force, dryRun)
			})
		}
	}
	return s.detachMemoryLocked(ctx, spacePath, spaceID, alias, fate, force, dryRun)
}

// detachMemoryLocked is DetachMemory's body, entered with any required saga
// lock already held (it re-loads the manifest under that lock).
func (s Service) detachMemoryLocked(ctx context.Context, spacePath, spaceID, alias string, fate memory.MemoryFate, force, dryRun bool) error {
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, idx, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return coded(CodeMemoryAliasNeeded, map[string]any{"space": spaceID, "count": len(manifest.Memories)}, "space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return coded(CodeMemoryNotFound, map[string]any{"space": spaceID, "memory": alias}, "memory %q not found on space %q", alias, spaceID)
	}
	if fate == "" {
		fate = memory.FateKeep
	}
	if !dryRun {
		if err := ensureClaudeLink(spacePath); err != nil {
			return err
		}
	}
	if !mem.Owned && (fate == memory.FateDestroy || fate == memory.FateContribute) {
		s.printf("notice: memory %q is not owned; detach-only\n", mem.Name)
		fate = memory.FateKeep
	}
	prov, err := s.provider(mem.Provider)
	if err != nil {
		return err
	}
	// Space wiring (reverse route + space-local MCP configs) belongs to the
	// PROVIDER, not to this one attachment: when sibling attachments of the
	// same provider remain, the wiring must survive the detach — tearing it
	// down would break them. The route maps the space to exactly one den, so
	// re-point it (and the MCP config) at the first surviving sibling rather
	// than leave it dangling at the detached den. Only detaching the
	// provider's last attachment removes route + MCP configs. Attachments of
	// OTHER providers don't count: their wiring is separate and untouched.
	repointID := ""
	for i, other := range manifest.Memories {
		if i != idx && other.Provider == mem.Provider {
			repointID = other.ID
			break
		}
	}
	lastForProvider := repointID == ""
	detachOpts := memory.DetachOptions{
		StoreID:             mem.ID,
		SpacePath:           spacePath,
		RemoveRoute:         fate == memory.FateKeep && lastForProvider,
		KeepSpaceWiring:     !lastForProvider,
		RepointRouteStoreID: repointID,
		Fate:                fate,
		Force:               force,
		Owned:               mem.Owned,
		DryRun:              dryRun,
		Out:                 s.Out,
	}
	if fate == memory.FateDestroy || fate == memory.FateContribute {
		// A destroying fate on a SAGA's own den first strips the saga-den MCP
		// wiring from members (a member client started from a stale config
		// could re-acquire the den mid-destroy, exactly the hazard the provider
		// closes for the saga space's own wiring) and re-wires them when the
		// destroy is refused — the den survived, so members must keep talking
		// to it. Covers both `stave memory detach <saga> --destroy` and the
		// den-first saga teardown, which routes through here. Dry-run mutates
		// no member files.
		var rewireMembers func(cause error)
		if !dryRun && manifest.Saga != nil {
			rewireMembers = s.stripSagaDenMemberWiring(ctx, manifest, mem)
		}
		// Destroying fates share applyMemoryFate's crash-window handling: the
		// splice-save runs in the same transaction as the successful Detach,
		// and a den that already vanished (den_not_found) still converges.
		// Any required saga lock is already held on entry (sagaLockHeld=true).
		if err := s.detachDestroyingAndRecord(ctx, prov, detachOpts, spacePath, spaceID, &manifest, mem, true); err != nil {
			if rewireMembers != nil {
				rewireMembers(err)
			}
			return wrapSourceInUse(spaceID, err)
		}
		if dryRun {
			s.printf("dry-run: remove memory attachment %q from .stave.yaml\n", mem.Name)
			return nil
		}
		s.printf("detached memory %s from %s\n", mem.Name, spaceID)
		return nil
	}
	if _, err := prov.Detach(ctx, detachOpts); err != nil {
		return wrapSourceInUse(spaceID, err)
	}
	if dryRun {
		s.printf("dry-run: remove memory attachment %q from .stave.yaml\n", mem.Name)
		return nil
	}
	manifest.Memories = append(manifest.Memories[:idx], manifest.Memories[idx+1:]...)
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("detached memory %s from %s\n", mem.Name, spaceID)
	return nil
}

// ProposeMemory runs provider Propose (contribute + warren propose) for an attachment.
func (s Service) ProposeMemory(ctx context.Context, spaceID, alias string, dryRun bool) error {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, _, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return coded(CodeMemoryAliasNeeded, map[string]any{"space": spaceID, "count": len(manifest.Memories)}, "space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return coded(CodeMemoryNotFound, map[string]any{"space": spaceID, "memory": alias}, "memory %q not found on space %q", alias, spaceID)
	}
	prov, err := s.provider(mem.Provider)
	if err != nil {
		return err
	}
	res, err := prov.Propose(ctx, memory.ProposeOptions{StoreID: mem.ID, SpacePath: spacePath, DryRun: dryRun, Out: s.Out})
	if err != nil {
		return err
	}
	// G4: surface the handoff — contributed counts, warnings, push command.
	if !dryRun {
		memory.PrintProposeOutcome(s.Out, res)
	}
	if res.Summary != "" {
		s.printf("%s\n", res.Summary)
	}
	return nil
}

// SyncMemory runs provider Sync for an attachment.
func (s Service) SyncMemory(ctx context.Context, spaceID, alias string, dryRun bool) error {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, _, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return coded(CodeMemoryAliasNeeded, map[string]any{"space": spaceID, "count": len(manifest.Memories)}, "space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return coded(CodeMemoryNotFound, map[string]any{"space": spaceID, "memory": alias}, "memory %q not found on space %q", alias, spaceID)
	}
	prov, err := s.provider(mem.Provider)
	if err != nil {
		return err
	}
	res, err := prov.Sync(ctx, memory.SyncOptions{StoreID: mem.ID, DryRun: dryRun, Out: s.Out})
	if err != nil {
		return err
	}
	if res.Summary != "" {
		s.printf("%s\n", res.Summary)
	}
	// Skew re-report (plan §9.1): after a successful sync, probe provider
	// status and re-print the compact per-link state so the user sees the
	// POST-sync freshness. Degrades silently — the sync itself succeeded.
	if !dryRun {
		if st, serr := prov.Status(ctx, memory.StatusOptions{StoreID: mem.ID}); serr == nil {
			suffix := st.StateSuffix()
			if suffix == "" {
				suffix = " (ok)"
			}
			s.printf("%s%s\n", mem.Name, suffix)
		}
	}
	return nil
}

// MemoryStatus prints provider status for one or all attachments.
func (s Service) MemoryStatus(ctx context.Context, spaceID, alias string) error {
	spacePath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	if len(manifest.Memories) == 0 {
		s.printf("space %s: no memory attachments\n", spaceID)
		return nil
	}
	targets := manifest.Memories
	if alias != "" {
		mem, _, ok := manifest.FindMemory(alias)
		if !ok {
			return coded(CodeMemoryNotFound, map[string]any{"space": spaceID, "memory": alias}, "memory %q not found on space %q", alias, spaceID)
		}
		targets = []MemoryManifest{mem}
	}
	for _, mem := range targets {
		prov, err := s.provider(mem.Provider)
		if err != nil {
			s.printf("%s  provider=%s id=%s owned=%v\n", mem.Name, mem.Provider, mem.ID, mem.Owned)
			s.printf("  status: %v\n", err)
			continue
		}
		res, err := prov.Status(ctx, memory.StatusOptions{StoreID: mem.ID, Out: s.Out})
		if err != nil {
			s.printf("%s  provider=%s id=%s owned=%v\n", mem.Name, mem.Provider, mem.ID, mem.Owned)
			s.printf("  status: %v\n", err)
			continue
		}
		// Header row carries the compact state suffix (skew intelligence);
		// per-link rows follow, indented, from the provider summary.
		s.printf("%s  provider=%s id=%s owned=%v%s\n", mem.Name, mem.Provider, mem.ID, mem.Owned, res.StateSuffix())
		for _, line := range strings.Split(strings.TrimSpace(res.Summary), "\n") {
			if line == "" {
				continue
			}
			s.printf("  %s\n", line)
		}
	}
	return nil
}

// MemoryStateSuffix probes the provider for one attachment and compacts its
// link freshness into a row suffix for `space status` (e.g. " (2 unpushed)",
// " (stale)"). Any failure — provider missing, binary missing, store gone —
// degrades to an empty suffix: space status must never break on memory
// intelligence.
func (s Service) MemoryStateSuffix(ctx context.Context, mem MemoryManifest) string {
	prov, err := s.provider(mem.Provider)
	if err != nil {
		return ""
	}
	res, err := prov.Status(ctx, memory.StatusOptions{StoreID: mem.ID})
	if err != nil {
		return ""
	}
	return res.StateSuffix()
}

// ListMemories prints attachment records for one space or all spaces.
func (s Service) ListMemories(spaceID string) error {
	if spaceID != "" {
		spacePath, err := s.resolveSpacePath(spaceID)
		if err != nil {
			return err
		}
		manifest, err := LoadManifest(spacePath)
		if err != nil {
			return err
		}
		if len(manifest.Memories) == 0 {
			s.printf("space %s: no memory attachments\n", spaceID)
			return nil
		}
		for _, mem := range manifest.Memories {
			s.printf("%s\t%s\t%s\towned=%v\n", spaceID, mem.Name, mem.Provider+":"+mem.ID, mem.Owned)
		}
		return nil
	}
	spaces, err := s.ListSpaces()
	if err != nil {
		if os.IsNotExist(err) {
			s.printf("no spaces\n")
			return nil
		}
		return err
	}
	found := false
	for _, entry := range spaces {
		if entry.Err != nil || len(entry.Manifest.Memories) == 0 {
			continue
		}
		found = true
		for _, mem := range entry.Manifest.Memories {
			s.printf("%s\t%s\t%s\towned=%v\n", entry.Manifest.ID, mem.Name, mem.Provider+":"+mem.ID, mem.Owned)
		}
	}
	if !found {
		s.printf("no memory attachments\n")
	}
	return nil
}

// SpaceEntry is one row from ListSpaces: a directory under AgentWorkDir that
// holds a .stave.yaml. Err records a read/parse failure — the entry is still
// returned so callers can fail closed on unreadable spaces.
type SpaceEntry struct {
	ID       string
	Path     string
	Manifest *Manifest
	Err      error
}

// ListSpaces enumerates spaces under AgentWorkDir. Directories without a
// .stave.yaml are not spaces and are skipped entirely; a manifest that exists
// but cannot be read or parsed yields an entry with Err set. The ReadDir
// error (including a missing AgentWorkDir) is returned verbatim.
func (s Service) ListSpaces() ([]SpaceEntry, error) {
	entries, err := os.ReadDir(s.Config.AgentWorkDir)
	if err != nil {
		return nil, err
	}
	var spaces []SpaceEntry
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".archive" {
			continue
		}
		path := filepath.Join(s.Config.AgentWorkDir, entry.Name())
		manifest, err := LoadManifest(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // no .stave.yaml — not a space
			}
			spaces = append(spaces, SpaceEntry{ID: entry.Name(), Path: path, Err: err})
			continue
		}
		spaces = append(spaces, SpaceEntry{ID: entry.Name(), Path: path, Manifest: &manifest})
	}
	return spaces, nil
}

func (s Service) SpacePath(id string) string {
	return filepath.Join(s.Config.AgentWorkDir, id)
}

// spaceHasRepos reports whether the space is durably materialized: its manifest
// loads and lists at least one repo. Used by the saga-create error path to gate
// recovery-learning capture to members whose repos are actually built.
func (s Service) spaceHasRepos(id string) bool {
	m, err := LoadManifest(s.SpacePath(id))
	return err == nil && len(m.Repos) > 0
}

// resolveSpacePath validates id before joining it under AgentWorkDir so verbs
// taking a raw space id can never traverse outside the work dir. Callers that
// validate the id themselves keep using the exported SpacePath.
func (s Service) resolveSpacePath(id string) (string, error) {
	if err := config.ValidateSpaceID(id); err != nil {
		return "", err
	}
	return s.SpacePath(id), nil
}

func (s Service) ensureNoDirtyEdits(ctx context.Context, spacePath string, manifest Manifest) error {
	var dirtyRepos []string
	for _, repo := range manifest.Repos {
		if repo.Mode != ModeEdit {
			continue
		}
		dirty, _, err := s.Git.IsDirty(ctx, filepath.Join(spacePath, repo.Path))
		if err != nil {
			return err
		}
		if dirty {
			dirtyRepos = append(dirtyRepos, repo.Name)
		}
	}
	if len(dirtyRepos) > 0 {
		return &DirtyWorktreeError{SpaceID: manifest.ID, Repos: dirtyRepos}
	}
	return nil
}

// ensureNoDependentSpaces refuses archive/destroy when a sibling space's
// manifest records a Base on one of this space's edit branches (stacked via
// "space:<id>" sugar or an explicit stave/... base). Matches are scoped to the
// same canonical BareRepoPath — same-named branches in different repos must
// not alias (mirroring resolveBaseOwner's scoping). Fails closed: a sibling
// whose manifest cannot be read also refuses, because it may hide such a
// dependency. --force overrides. Siblings in exempt are retired by the same
// saga lifecycle operation and never refuse.
func (s Service) ensureNoDependentSpaces(spaceID string, manifest Manifest, exempt []string) error {
	spellings := map[string]map[string]string{} // canonical bare path → base spelling → branch
	for _, repo := range manifest.Repos {
		if repo.Mode != ModeEdit || repo.Branch == "" {
			continue
		}
		key := canonicalRepoPath(repo.BareRepoPath)
		byBase := spellings[key]
		if byBase == nil {
			byBase = map[string]string{}
			spellings[key] = byBase
		}
		byBase["refs/heads/"+repo.Branch] = repo.Branch
		byBase["origin/"+repo.Branch] = repo.Branch
		byBase[repo.Branch] = repo.Branch
	}
	if len(spellings) == 0 {
		return nil
	}
	siblings, err := s.ListSpaces()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	exemptSet := make(map[string]bool, len(exempt))
	for _, id := range exempt {
		exemptSet[id] = true
	}
	for _, sibling := range siblings {
		if sibling.ID == spaceID || exemptSet[sibling.ID] {
			continue
		}
		if sibling.Err != nil {
			return fmt.Errorf("space %q has an unreadable manifest (%v); cannot verify it does not stack on %q (use --force to override)", sibling.ID, sibling.Err, spaceID)
		}
		for _, repo := range sibling.Manifest.Repos {
			if branch, ok := spellings[canonicalRepoPath(repo.BareRepoPath)][repo.Base]; ok {
				return &DependentSpaceError{SpaceID: spaceID, Dependent: sibling.ID, Repo: repo.Name, Branch: branch}
			}
		}
	}
	return nil
}

// guardRefusal downgrades a would-refuse guard error to a printed dry-run
// diagnostic so previews keep going; real runs keep the hard refusal.
func (s Service) guardRefusal(err error, dryRun bool) error {
	if err == nil || !dryRun {
		return err
	}
	s.printf("dry-run: would refuse: %v\n", err)
	return nil
}

func (s Service) writeAgents(spacePath string, manifest Manifest) error {
	if manifest.Saga != nil {
		return s.writeSagaAgents(spacePath, manifest)
	}
	var b strings.Builder
	b.WriteString("# Stave Workspace Instructions\n\n")
	b.WriteString("- Top-level repository folders are editable worktrees for this space.\n")
	b.WriteString("- Repositories under `references/` are read-only context unless the user explicitly asks for edits there.\n")
	b.WriteString("- Keep `.stave.yaml` aligned with worktree changes made through Stave.\n")
	if manifest.SpecPath != "" {
		fmt.Fprintf(&b, "- Read `%s/` before starting; it holds the task or review context for this space.\n", manifest.SpecPath)
	}
	for _, mem := range manifest.Memories {
		fmt.Fprintf(&b, "- Persistent memory is available via the context-marmot MCP tools (den: %s).\n", mem.ID)
	}
	b.WriteString("\n")
	if err := writeRepoSection(&b, spacePath, manifest); err != nil {
		return err
	}
	b.WriteString(s.memberSagaSection(manifest))
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	return fsio.WriteFileAtomic(filepath.Join(spacePath, AgentsName), []byte(b.String()), 0o644)
}

// writeRepoSection renders the '## Repositories' block with nested
// instruction links; shared by the ordinary and saga templates.
func writeRepoSection(b *strings.Builder, spacePath string, manifest Manifest) error {
	if len(manifest.Repos) == 0 {
		return nil
	}
	b.WriteString("## Repositories\n")
	for _, repo := range manifest.Repos {
		fmt.Fprintf(b, "- `%s`: %s at `%s`\n", repo.Name, repo.Mode, repo.Path)
		instructionsPath := filepath.Join(repo.Path, AgentsName)
		info, err := os.Stat(filepath.Join(spacePath, instructionsPath))
		switch {
		case err == nil && !info.IsDir():
			markdownPath := filepath.ToSlash(instructionsPath)
			fmt.Fprintf(b, "  - Read [`%s`](%s) for repository-specific instructions.\n", markdownPath, markdownPath)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	return nil
}

// writeSagaAgents renders a saga space's AGENTS.md: the member graph in topo
// order plus the coordination doctrine. Deliberately NO branches and NO stack
// bases — those are volatile (a retarget would strand them in prose); live
// state always comes from `stave saga status`.
func (s Service) writeSagaAgents(spacePath string, manifest Manifest) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Stave Saga: %s\n\n", manifest.ID)
	b.WriteString("- This space coordinates the saga's member spaces; it holds no editable worktrees of its own.\n")
	if manifest.SpecPath != "" {
		fmt.Fprintf(&b, "- Read `%s/` before starting; it holds the saga's task or review context.\n", manifest.SpecPath)
	}
	for _, mem := range manifest.Memories {
		fmt.Fprintf(&b, "- Persistent memory is available via the context-marmot MCP tools (den: %s).\n", mem.ID)
	}
	b.WriteString("\n## Members\n")
	if len(manifest.Saga.Members) == 0 {
		b.WriteString("- none yet; enroll spaces with `stave saga add`.\n")
	}
	for i, member := range sagaTopoOrder(manifest.Saga.Members) {
		fmt.Fprintf(&b, "%d. `%s` (../%s)", i+1, member.ID, member.ID)
		if len(member.After) > 0 {
			fmt.Fprintf(&b, " — after: %s", strings.Join(member.After, ", "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nFor live state run: stave saga status %s --json\n", manifest.ID)
	b.WriteString("\n")
	if err := writeRepoSection(&b, spacePath, manifest); err != nil {
		return err
	}
	if len(manifest.Repos) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("## Doctrine\n")
	b.WriteString("- Work happens inside member directories via separate per-member summons; this space only coordinates.\n")
	b.WriteString("- Before editing inside a member, read that member's AGENTS.md.\n")
	b.WriteString("- Never rebase or retarget a member without being asked.\n")
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	return fsio.WriteFileAtomic(filepath.Join(spacePath, AgentsName), []byte(b.String()), 0o644)
}

// memberSagaSection renders the '## Saga' block appended to a member space's
// AGENTS.md: the owning saga (reverse-lookup over ListSpaces), the member's
// own stacked bases (member-local data only) and the coordination pointer.
// Any scan failure degrades to no section — membership rendering must never
// break ordinary space writes.
func (s Service) memberSagaSection(manifest Manifest) string {
	if manifest.ID == "" {
		return ""
	}
	spaces, err := s.ListSpaces()
	if err != nil {
		return ""
	}
	sagaID := ""
	for _, entry := range spaces {
		if entry.Err != nil || entry.Manifest.Saga == nil {
			continue
		}
		for _, member := range entry.Manifest.Saga.Members {
			if member.ID == manifest.ID {
				sagaID = entry.ID
				break
			}
		}
		if sagaID != "" {
			break
		}
	}
	if sagaID == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Saga\n")
	fmt.Fprintf(&b, "- This space is a member of saga `%s` (`../%s`).\n", sagaID, sagaID)
	for _, repo := range manifest.Repos {
		if repo.Mode != ModeEdit || repo.Base == "" {
			continue
		}
		fmt.Fprintf(&b, "- `%s` stacks on base `%s`; drift reports against it.\n", repo.Name, repo.Base)
	}
	fmt.Fprintf(&b, "- Coordinate via `stave saga status %s --json`.\n", sagaID)
	return b.String()
}

func ensureClaudeLink(spacePath string) error {
	linkPath := filepath.Join(spacePath, ClaudeName)
	info, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(AgentsName, linkPath); err != nil {
			if runtime.GOOS == "windows" {
				return fmt.Errorf("create %s symlink to %s: %w (Windows requires Developer Mode or administrator symlink privileges)", linkPath, AgentsName, err)
			}
			return fmt.Errorf("create %s symlink to %s: %w", linkPath, AgentsName, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s already exists and is not a symlink; move it before Stave can link it to %s", linkPath, AgentsName)
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	if target == AgentsName {
		return nil
	}
	return fmt.Errorf("%s points to %q instead of %s; move it before Stave can create the generated link", linkPath, target, AgentsName)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) printf(format string, args ...any) {
	if s.Out != nil {
		fmt.Fprintf(s.Out, format, args...)
	}
}

// normalizeRemoteRef is the offline spelling rule: anything that is not
// already a full ref or an origin/ ref is assumed to name a branch on origin.
// It cannot see the bare repo, so it is only the fallback for
// resolveRefSpelling; call that instead wherever a bare repo is in hand.
func normalizeRemoteRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "origin/") || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "origin/" + ref
}

// resolveRefSpelling maps a user-supplied base/ref onto the ref stave records
// and hands to git, using the remotes the bare repo actually has. full is the
// unambiguous ref RefExists verifies; spelling is what lands in the manifest.
//
// A base whose first path segment names a configured remote resolves against
// THAT remote: with an origin + fork mirror, "fork/main" is
// refs/remotes/fork/main, not the origin/fork/main that the origin-only rule
// used to mint. Non-origin remotes keep the full refs/remotes/ spelling
// because a bare "fork/main" is ambiguous under git's rev-parse ordering (a
// local branch literally named fork/main would win); "origin/..." keeps its
// short spelling, which is what every existing manifest already carries.
func resolveRefSpelling(ref string, remotes []string) (full, spelling string) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "refs/") {
		return ref, ref
	}
	if remote, rest, ok := strings.Cut(ref, "/"); ok && rest != "" && remote != "origin" && slices.Contains(remotes, remote) {
		return "refs/remotes/" + ref, "refs/remotes/" + ref
	}
	spelling = normalizeRemoteRef(ref)
	return "refs/remotes/" + spelling, spelling
}

// resolveRef is resolveRefSpelling plus an existence check, so a base that
// cannot resolve is refused here — with a coded error naming the ref stave
// looked for and the remotes it could have used — instead of reaching
// 'git worktree add' as a raw git failure.
//
// The bare repo is the only source of truth for both halves. When it cannot
// be interrogated at all (not cloned yet, unreadable) the offline spelling
// rule stands in and the check is skipped: an unusable mirror is the worktree
// add's problem to report, not a reason to invent a resolution error.
func (s Service) resolveRef(ctx context.Context, repoName, bareRepoPath, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", coded(CodeInvalidArguments, map[string]any{"repo": repoName}, "repo %q: a base ref is required", repoName)
	}
	remotes, err := s.Git.RemoteNames(ctx, bareRepoPath)
	if err != nil {
		return normalizeRemoteRef(ref), nil
	}
	full, spelling := resolveRefSpelling(ref, remotes)
	exists, err := s.Git.RefExists(ctx, bareRepoPath, full)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", &RefNotFoundError{Repo: repoName, Ref: strings.TrimSpace(ref), Tried: full, Remotes: remotes}
	}
	return spelling, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copySpec(src, destDir string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}
	if info.IsDir() {
		return copyDirContents(src, destDir)
	}
	return copyFile(src, filepath.Join(destDir, filepath.Base(src)))
}

func copyDirContents(srcDir, destDir string) error {
	return filepath.WalkDir(srcDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destDir, 0o755)
		}
		dest := filepath.Join(destDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		return copyFile(path, dest)
	})
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}

// ArchivedSpaceEntry is one archived space under <AgentWorkDir>/.archive/.
// ID is the archive directory name: usually the space id, or
// <id>-<timestamp> when Archive had to disambiguate a collision (see
// archiveNameMatches). Manifest is nil and Err set when the archived manifest
// cannot be read or parsed.
type ArchivedSpaceEntry struct {
	ID       string
	Path     string
	Manifest *Manifest
	Err      error
}

// ListArchivedSpaces enumerates archived spaces under <AgentWorkDir>/.archive/
// with the same rules as ListSpaces: directories without a .stave.yaml are
// skipped, unreadable manifests yield an entry with Err set. A missing
// .archive/ directory is not an error: it simply holds no archives.
func (s Service) ListArchivedSpaces() ([]ArchivedSpaceEntry, error) {
	archiveRoot := filepath.Join(s.Config.AgentWorkDir, ".archive")
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var spaces []ArchivedSpaceEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(archiveRoot, entry.Name())
		manifest, err := LoadManifest(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // no .stave.yaml — not an archived space
			}
			spaces = append(spaces, ArchivedSpaceEntry{ID: entry.Name(), Path: path, Err: err})
			continue
		}
		spaces = append(spaces, ArchivedSpaceEntry{ID: entry.Name(), Path: path, Manifest: &manifest})
	}
	return spaces, nil
}
