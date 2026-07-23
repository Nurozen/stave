package space

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/memory"
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
}

type InitOptions struct {
	ID       string
	Kind     string
	SpecPath string
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
	// Memories are raw `[provider:]<spec>` values from --memory (repeatable).
	// Empty with config memory.default:true triggers ambient attach.
	Memories []string
	// SkipAmbientMemory disables ambient memory.default attach (tests / explicit off).
	SkipAmbientMemory bool
	DryRun            bool
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
}

type SyncOptions struct {
	SpaceID        string
	ReferencesOnly bool
}

type ArchiveOptions struct {
	SpaceID string
	Force   bool
	DryRun  bool
	// MemoryFate: keep (default) or contribute (contribute-then-keep).
	// destroy is invalid for archive — use Destroy with FateDestroy.
	MemoryFate memory.MemoryFate // empty → keep
}

// MemoryFate values: keep | destroy | contribute (default keep).
type DestroyOptions struct {
	SpaceID    string
	Force      bool
	DryRun     bool
	MemoryFate memory.MemoryFate // empty → keep
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

func ParseRepoSpec(raw string) (RepoSpec, error) {
	name, ref, found := strings.Cut(raw, ":")
	if err := config.ValidateName("repo name", name); err != nil {
		return RepoSpec{}, err
	}
	if found && strings.TrimSpace(ref) == "" {
		return RepoSpec{}, fmt.Errorf("repo spec %q has an empty ref", raw)
	}
	return RepoSpec{Name: name, Ref: ref}, nil
}

func DefaultBranch(spaceID, repoName string) string {
	return fmt.Sprintf("stave/%s/%s", spaceID, repoName)
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
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	s.printf("created space %s at %s\n", opts.ID, spacePath)
	return nil
}

func (s Service) Create(ctx context.Context, opts CreateOptions) error {
	if opts.DryRun {
		return s.createDryRun(ctx, opts)
	}
	if err := s.InitSpace(ctx, InitOptions{ID: opts.ID, Kind: opts.Kind, SpecPath: opts.SpecPath}); err != nil {
		return err
	}
	for _, spec := range opts.Edits {
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeEdit, Base: spec.Ref, DryRun: opts.DryRun}); err != nil {
			return err
		}
	}
	for _, spec := range opts.References {
		// LinkMemory is a no-op here (memory attaches AFTER the repo loop and
		// passes the references itself); set for uniform semantics.
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeReference, Ref: spec.Ref, DryRun: opts.DryRun, LinkMemory: true}); err != nil {
			return err
		}
	}
	return s.attachMemoriesAfterCreate(ctx, opts)
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
			return fmt.Errorf("repo %q is not registered", spec.Name)
		}
		baseRef := normalizeRemoteRef(firstNonEmpty(spec.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
		s.printf("dry-run: add edit worktree %s from %s at %s\n", DefaultBranch(opts.ID, spec.Name), baseRef, filepath.Join(spacePath, spec.Name))
	}
	for _, spec := range opts.References {
		repoCfg, ok := s.Config.Repos[spec.Name]
		if !ok {
			return fmt.Errorf("repo %q is not registered", spec.Name)
		}
		ref := normalizeRemoteRef(firstNonEmpty(spec.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		s.printf("dry-run: fetch %s\n", repoCfg.BareRepoPath)
		s.printf("dry-run: add reference worktree %s at %s\n", ref, filepath.Join(spacePath, "references", spec.Name))
	}
	// Memory dry-run lines (no binary invoke).
	return s.attachMemoriesAfterCreate(ctx, opts)
}

// attachMemoriesAfterCreate runs explicit --memory specs and/or ambient default.
func (s Service) attachMemoriesAfterCreate(ctx context.Context, opts CreateOptions) error {
	return s.AttachMemories(ctx, AttachMemoriesOptions{
		SpaceID:     opts.ID,
		Specs:       opts.Memories,
		References:  opts.References,
		SkipAmbient: opts.SkipAmbientMemory,
		DryRun:      opts.DryRun,
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
				return fmt.Errorf("--memory specs %q and %q target the same store on provider %q; nothing attached", prev, raw, parsed.Provider)
			}
			seen[key] = raw
		}
	}
	for _, raw := range specs {
		if err := s.AttachMemory(ctx, AttachMemoryOptions{
			SpaceID:    opts.SpaceID,
			RawSpec:    raw,
			DryRun:     opts.DryRun,
			Strict:     strict,
			References: opts.References,
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
				return fmt.Errorf("memory %q already attached to space %q", name, opts.SpaceID)
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
			return fmt.Errorf("memory %q already attached to space %q", result.Name, opts.SpaceID)
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
		return fmt.Errorf("repo mode must be edit or reference")
	}
	repoCfg, ok := s.Config.Repos[opts.RepoName]
	if !ok {
		return fmt.Errorf("repo %q is not registered", opts.RepoName)
	}
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
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
		baseRef := normalizeRemoteRef(firstNonEmpty(opts.Base, repoCfg.DefaultBranch, s.Config.DefaultBase))
		branch := firstNonEmpty(opts.Branch, DefaultBranch(opts.SpaceID, opts.RepoName))
		repoPath := opts.RepoName
		if manifest.HasPath(repoPath) {
			return fmt.Errorf("repo path %q already exists in manifest", repoPath)
		}
		startPoint := baseRef
		if opts.StartPoint != "" {
			startPoint = normalizeRemoteRef(opts.StartPoint)
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
		} else {
			s.printf("dry-run: add edit worktree %s from %s at %s\n", branch, startPoint, worktreePath)
		}
		entry = RepoManifest{Name: opts.RepoName, Mode: ModeEdit, Path: repoPath, Base: baseRef, Branch: branch, BareRepoPath: repoCfg.BareRepoPath}
	case ModeReference:
		ref := normalizeRemoteRef(firstNonEmpty(opts.Ref, repoCfg.DefaultBranch, s.Config.DefaultBase))
		repoPath := filepath.Join("references", opts.RepoName)
		if manifest.HasPath(repoPath) {
			return fmt.Errorf("repo path %q already exists in manifest", repoPath)
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

func (s Service) Sync(ctx context.Context, opts SyncOptions) error {
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	for _, repo := range manifest.Repos {
		if opts.ReferencesOnly && repo.Mode != ModeReference {
			continue
		}
		if err := s.Git.FetchAllPrune(ctx, repo.BareRepoPath); err != nil {
			return err
		}
		worktreePath := filepath.Join(spacePath, repo.Path)
		switch repo.Mode {
		case ModeReference:
			dirty, _, err := s.Git.IsDirty(ctx, worktreePath)
			if err != nil {
				return err
			}
			if dirty {
				s.printf("reference %s is dirty; skipped checkout\n", repo.Name)
				continue
			}
			if err := s.Git.CheckoutDetached(ctx, worktreePath, repo.Ref); err != nil {
				return err
			}
			s.printf("updated reference %s to %s\n", repo.Name, repo.Ref)
		case ModeEdit:
			ahead, behind, err := s.Git.AheadBehind(ctx, worktreePath, repo.Base)
			if err != nil {
				s.printf("edit %s drift unknown: %v\n", repo.Name, err)
				continue
			}
			s.printf("edit %s: ahead %d, behind %d versus %s\n", repo.Name, ahead, behind, repo.Base)
		}
	}
	return s.writeAgents(spacePath, manifest)
}

func (s Service) Status(ctx context.Context, spaceID string) (Status, error) {
	spacePath := s.SpacePath(spaceID)
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

func (s Service) Archive(ctx context.Context, opts ArchiveOptions) error {
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	if !opts.Force {
		if err := s.ensureNoDirtyEdits(ctx, spacePath, manifest); err != nil {
			return err
		}
	}
	fate := opts.MemoryFate
	if fate == "" {
		fate = memory.FateKeep
	}
	if fate == memory.FateDestroy {
		return fmt.Errorf("archive does not destroy memory; use 'stave space destroy --memory destroy' instead")
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
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	if !opts.Force {
		if err := s.ensureNoDirtyEdits(ctx, spacePath, manifest); err != nil {
			return err
		}
	}
	fate := opts.MemoryFate
	if fate == "" {
		fate = memory.FateKeep
	}
	// Memory fate BEFORE RemoveAll — manifest is gone after.
	if err := s.applyMemoryFate(ctx, spacePath, manifest, fate, opts.Force, opts.DryRun); err != nil {
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
// would fail on the already-removed route (G1).
func (s Service) applyMemoryFate(ctx context.Context, spacePath string, manifest Manifest, fate memory.MemoryFate, force, dryRun bool) error {
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
	for _, mem := range manifest.Memories {
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
		if effective == memory.FateKeep && !routeRemoved[mem.Provider] {
			detachOpts.RemoveRoute = true
			routeRemoved[mem.Provider] = true
		}
		if _, err := prov.Detach(ctx, detachOpts); err != nil {
			return fmt.Errorf("memory %s fate %s: %w", mem.Name, effective, err)
		}
	}
	return nil
}

// DetachMemory removes one attachment from the manifest after provider Detach.
func (s Service) DetachMemory(ctx context.Context, spaceID, alias string, fate memory.MemoryFate, force, dryRun bool) error {
	spacePath := s.SpacePath(spaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, idx, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return fmt.Errorf("space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return fmt.Errorf("memory %q not found on space %q", alias, spaceID)
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
	if _, err := prov.Detach(ctx, memory.DetachOptions{
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
	}); err != nil {
		return err
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
	spacePath := s.SpacePath(spaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, _, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return fmt.Errorf("space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return fmt.Errorf("memory %q not found on space %q", alias, spaceID)
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
	spacePath := s.SpacePath(spaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	mem, _, ok := manifest.FindMemory(alias)
	if !ok {
		if alias == "" {
			return fmt.Errorf("space %q has %d memory attachments; specify an alias", spaceID, len(manifest.Memories))
		}
		return fmt.Errorf("memory %q not found on space %q", alias, spaceID)
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
	spacePath := s.SpacePath(spaceID)
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
			return fmt.Errorf("memory %q not found on space %q", alias, spaceID)
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
		manifest, err := LoadManifest(s.SpacePath(spaceID))
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
	entries, err := os.ReadDir(s.Config.AgentWorkDir)
	if err != nil {
		if os.IsNotExist(err) {
			s.printf("no spaces\n")
			return nil
		}
		return err
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".archive" {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(s.Config.AgentWorkDir, entry.Name()))
		if err != nil || len(manifest.Memories) == 0 {
			continue
		}
		found = true
		for _, mem := range manifest.Memories {
			s.printf("%s\t%s\t%s\towned=%v\n", manifest.ID, mem.Name, mem.Provider+":"+mem.ID, mem.Owned)
		}
	}
	if !found {
		s.printf("no memory attachments\n")
	}
	return nil
}

func (s Service) SpacePath(id string) string {
	return filepath.Join(s.Config.AgentWorkDir, id)
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

func (s Service) writeAgents(spacePath string, manifest Manifest) error {
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
	if len(manifest.Repos) > 0 {
		b.WriteString("## Repositories\n")
		for _, repo := range manifest.Repos {
			fmt.Fprintf(&b, "- `%s`: %s at `%s`\n", repo.Name, repo.Mode, repo.Path)
			instructionsPath := filepath.Join(repo.Path, AgentsName)
			info, err := os.Stat(filepath.Join(spacePath, instructionsPath))
			switch {
			case err == nil && !info.IsDir():
				markdownPath := filepath.ToSlash(instructionsPath)
				fmt.Fprintf(&b, "  - Read [`%s`](%s) for repository-specific instructions.\n", markdownPath, markdownPath)
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return err
			}
		}
	}
	if err := ensureClaudeLink(spacePath); err != nil {
		return err
	}
	return fsio.WriteFileAtomic(filepath.Join(spacePath, AgentsName), []byte(b.String()), 0o644)
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

func normalizeRemoteRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "origin/") || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "origin/" + ref
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
