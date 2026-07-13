package space

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
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
	Out    io.Writer
	Now    func() time.Time
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
	DryRun     bool
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
}

type SyncOptions struct {
	SpaceID        string
	ReferencesOnly bool
}

type ArchiveOptions struct {
	SpaceID string
	Force   bool
}

type DestroyOptions struct {
	SpaceID string
	Force   bool
	DryRun  bool
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
		return s.writeAgents(spacePath, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
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
		return s.createDryRun(opts)
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
		if err := s.AddRepo(ctx, AddOptions{SpaceID: opts.ID, RepoName: spec.Name, Mode: ModeReference, Ref: spec.Ref, DryRun: opts.DryRun}); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) createDryRun(opts CreateOptions) error {
	if err := config.ValidateName("space id", opts.ID); err != nil {
		return err
	}
	spacePath := s.SpacePath(opts.ID)
	s.printf("dry-run: create space directory %s\n", spacePath)
	s.printf("dry-run: write %s\n", filepath.Join(spacePath, ManifestName))
	s.printf("dry-run: write %s\n", filepath.Join(spacePath, AgentsName))
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
	return nil
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
	return nil
}

func (s Service) Sync(ctx context.Context, opts SyncOptions) error {
	spacePath := s.SpacePath(opts.SpaceID)
	manifest, err := LoadManifest(spacePath)
	if err != nil {
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
	return nil
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
	for _, repo := range manifest.Repos {
		if err := s.Git.WorktreeRemove(ctx, repo.BareRepoPath, filepath.Join(spacePath, repo.Path), true); err != nil {
			return err
		}
		if err := s.Git.WorktreePrune(ctx, repo.BareRepoPath); err != nil {
			return err
		}
	}
	archiveRoot := filepath.Join(s.Config.AgentWorkDir, ".archive")
	if err := os.MkdirAll(archiveRoot, config.DefaultDirMode); err != nil {
		return err
	}
	dest := filepath.Join(archiveRoot, opts.SpaceID)
	if _, err := os.Stat(dest); err == nil {
		dest = fmt.Sprintf("%s-%s", dest, time.Now().Format("20060102150405"))
	}
	if err := os.Rename(spacePath, dest); err != nil {
		return err
	}
	s.printf("archived %s to %s\n", opts.SpaceID, dest)
	return nil
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
	b.WriteString("\n")
	if len(manifest.Repos) > 0 {
		b.WriteString("## Repositories\n")
		for _, repo := range manifest.Repos {
			fmt.Fprintf(&b, "- `%s`: %s at `%s`\n", repo.Name, repo.Mode, repo.Path)
		}
	}
	return os.WriteFile(filepath.Join(spacePath, AgentsName), []byte(b.String()), 0o644)
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
