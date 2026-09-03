package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/fsio"
	"github.com/Nurozen/stave/internal/memory"
)

// sagaDenLifetime is the provider lifetime of the saga-owned den: it outlives
// N member tasks, so it is durable rather than the default task lifetime.
const sagaDenLifetime = "durable"

// mcpWirer is the optional provider extension for MCP-config-only den sharing
// (saga members are never given attachments or routes; they get space-local
// MCP config pointing at the saga den). It structurally matches the memory
// package's MCPWirer interface, so providers implementing that satisfy this
// seam; providers without it simply skip member wiring.
type mcpWirer interface {
	WriteMCPConfig(ctx context.Context, spacePath, storeID string) error
	RemoveMCPConfig(ctx context.Context, spacePath string) error
}

// Locks live under AgentWorkDir/.locks/, structurally namespaced so a saga
// literally named "saga-membership" cannot collide with the global lock:
//
//	membership.lock        every operation that scans-and-mutates cross-saga membership
//	sagas/<saga-id>.lock   every writer of that saga's manifest
//
// Acquisition order is ALWAYS membership → per-saga. fsio.WithLock's flock is
// non-reentrant across fds even within one process, so lock-held state is
// threaded (the unexported sagaLockHeld options fields), never re-acquired.
func (s Service) locksDir() string {
	return filepath.Join(s.Config.AgentWorkDir, ".locks")
}

// withMembershipLock runs fn under the global membership lock. The directory
// is created first: fsio.WithLock only O_CREATEs the lock file itself.
func (s Service) withMembershipLock(fn func() error) error {
	if err := os.MkdirAll(s.locksDir(), config.DefaultDirMode); err != nil {
		return err
	}
	return fsio.WithLock(filepath.Join(s.locksDir(), "membership.lock"), fn)
}

// withSagaLock runs fn under sagaID's per-saga lock. Callers needing the
// membership lock too must take it BEFORE this one (fixed acquisition order).
func (s Service) withSagaLock(sagaID string, fn func() error) error {
	dir := filepath.Join(s.locksDir(), "sagas")
	if err := os.MkdirAll(dir, config.DefaultDirMode); err != nil {
		return err
	}
	return fsio.WithLock(filepath.Join(dir, sagaID+".lock"), fn)
}

// mutateSagaManifest runs one locked load → verify-saga → fn → save →
// writeAgents cycle under the per-saga lock. It is the entry point for pure
// per-saga mutations; verbs that scan other sagas' membership use
// mutateSagaMembership instead.
func (s Service) mutateSagaManifest(sagaID string, fn func(*Manifest) error) error {
	spacePath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return err
	}
	return s.withSagaLock(sagaID, func() error {
		return s.mutateSagaManifestLocked(sagaID, spacePath, fn)
	})
}

// mutateSagaMembership is mutateSagaManifest under membership + per-saga
// locks, for verbs whose fn scans-and-depends-on other sagas' rosters.
func (s Service) mutateSagaMembership(sagaID string, fn func(*Manifest) error) error {
	spacePath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return err
	}
	return s.withMembershipLock(func() error {
		return s.withSagaLock(sagaID, func() error {
			return s.mutateSagaManifestLocked(sagaID, spacePath, fn)
		})
	})
}

// mutateSagaManifestLocked is the already-locked inner variant: the caller
// holds the per-saga lock (and the membership lock when fn scans other sagas).
// It reloads under the lock so fn always revalidates fresh state.
func (s Service) mutateSagaManifestLocked(sagaID, spacePath string, fn func(*Manifest) error) error {
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		return err
	}
	if manifest.Saga == nil {
		return fmt.Errorf("space %q is not a saga", sagaID)
	}
	if err := fn(&manifest); err != nil {
		return err
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		return err
	}
	if err := s.writeAgents(spacePath, manifest); err != nil {
		return err
	}
	// Membership changed: refresh the saga-root agent settings (Deviation 16)
	// while the lock is still held. Post-commit best-effort: the roster is
	// already saved, so a settings failure prints a notice instead of failing
	// the verb (which would also skip the member-side effects).
	if err := s.writeSagaAgentSettings(spacePath, manifest); err != nil {
		s.printf("notice: could not update saga agent settings: %v\n", err)
	}
	return nil
}

// SagaCreateOptions drives CreateSaga. Sagas hold no edit worktrees, so there
// is deliberately no Edits field.
type SagaCreateOptions struct {
	ID       string
	SpecPath string
	// References are read-only context worktrees on the saga space.
	References []RepoSpec
	// Memories are raw `[provider:]<spec>` values. At most one may be fresh
	// (to-be-owned): that store is "the saga den" and attaches durable.
	Memories []string
	// SkipAmbientMemory disables ambient memory.default attach.
	SkipAmbientMemory bool
	DryRun            bool
	// NoLearn suppresses passive co-occurrence capture. A saga space holds no
	// edits, so capture records nothing regardless; threaded for uniformity.
	NoLearn bool
}

// CreateSaga creates a saga space: a version-2 manifest with an empty member
// roster, born under the membership + per-saga locks. An existing space path
// is rejected outright — InitSpace's adoption branch must never dress a
// pre-existing space up as a saga.
func (s Service) CreateSaga(ctx context.Context, opts SagaCreateOptions) error {
	spacePath, err := s.resolveSpacePath(opts.ID)
	if err != nil {
		return err
	}
	// At most one owned store: parse every spec up front so a bad batch
	// creates nothing. Attach-existing specs (not fresh) are unlimited.
	fresh := 0
	for _, raw := range opts.Memories {
		parsed, err := memory.ParseMemorySpec(raw, s.defaultMemoryProvider())
		if err != nil {
			return err
		}
		if parsed.Fresh {
			fresh++
		}
	}
	if fresh > 1 {
		return fmt.Errorf("saga %q: at most one owned memory store may be created with the saga (%d fresh --memory specs)", opts.ID, fresh)
	}
	create := CreateOptions{
		ID:                  opts.ID,
		Kind:                KindSaga,
		SpecPath:            opts.SpecPath,
		References:          opts.References,
		Memories:            opts.Memories,
		SkipAmbientMemory:   opts.SkipAmbientMemory,
		DryRun:              opts.DryRun,
		NoLearn:             opts.NoLearn,
		Saga:                &SagaManifest{},
		viaSaga:             true,
		sagaLockHeld:        true,
		ownedMemoryLifetime: sagaDenLifetime,
	}
	if opts.DryRun {
		return s.Create(ctx, create)
	}
	return s.withMembershipLock(func() error {
		return s.withSagaLock(opts.ID, func() error {
			if _, err := os.Stat(spacePath); err == nil {
				return coded(CodeSpaceExists, map[string]any{"path": spacePath}, "space %q already exists; saga create requires a fresh id", opts.ID)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return s.Create(ctx, create)
		})
	})
}

// SagaAdd registers spaceID as a member of sagaID with upsert semantics:
// re-adding an existing member with no after edges and clearAfter false
// preserves its edges; clearAfter resets them; a non-empty after replaces
// them. After targets must be members (or the added id itself) — the manifest
// Validate enforces after ⊆ members and acyclicity on save.
func (s Service) SagaAdd(ctx context.Context, sagaID, spaceID string, after []string, clearAfter bool) error {
	memberPath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	if sagaID == spaceID {
		return fmt.Errorf("saga %q cannot be its own member", sagaID)
	}
	var memberManifest, updated Manifest
	var siblings []SpaceEntry
	if err := s.mutateSagaMembership(sagaID, s.sagaAddMutation(sagaID, spaceID, after, clearAfter, &memberManifest, &updated, &siblings)); err != nil {
		return err
	}
	s.sagaAddMemberEffects(ctx, sagaID, spaceID, memberPath, memberManifest, updated, siblings)
	return nil
}

// sagaAddMutation returns the roster-upsert closure shared by SagaAdd and
// create --saga registration. It must run with the membership + per-saga
// locks held (mutateSagaMembership acquires both; createInSaga holds both and
// enters via mutateSagaManifestLocked). Results thread out via the pointers.
func (s Service) sagaAddMutation(sagaID, spaceID string, after []string, clearAfter bool, memberManifest, updated *Manifest, siblings *[]SpaceEntry) func(*Manifest) error {
	return func(manifest *Manifest) error {
		var err error
		*siblings, err = s.ListSpaces()
		if err != nil {
			return err
		}
		var memberEntry *SpaceEntry
		for i := range *siblings {
			if (*siblings)[i].ID == spaceID {
				memberEntry = &(*siblings)[i]
				break
			}
		}
		if memberEntry == nil {
			return coded(CodeSpaceNotFound, nil, "space %q does not exist", spaceID)
		}
		if memberEntry.Err != nil {
			return fmt.Errorf("space %q has an unreadable manifest: %v", spaceID, memberEntry.Err)
		}
		*memberManifest = *memberEntry.Manifest
		if memberManifest.ID != spaceID {
			return fmt.Errorf("space %q: existing manifest id %q does not match %q", spaceID, memberManifest.ID, spaceID)
		}
		if memberManifest.Saga != nil {
			return coded(CodeSagaSpace, nil, "space %q is itself a saga; sagas cannot be members", spaceID)
		}
		// Single-saga membership, scanned under the membership lock. An
		// unreadable sibling could hide a membership record: fail closed.
		for _, sibling := range *siblings {
			if sibling.ID == sagaID || sibling.ID == spaceID {
				continue
			}
			if sibling.Err != nil {
				return fmt.Errorf("space %q has an unreadable manifest (%v); cannot verify %q is not already a member of it", sibling.ID, sibling.Err, spaceID)
			}
			if sibling.Manifest.Saga == nil {
				continue
			}
			for _, member := range sibling.Manifest.Saga.Members {
				if member.ID == spaceID {
					return coded(CodeSagaMember, map[string]any{"saga": sibling.ID}, "space %q is already a member of saga %q", spaceID, sibling.ID)
				}
			}
		}
		idx := -1
		for i, member := range manifest.Saga.Members {
			if member.ID == spaceID {
				idx = i
				break
			}
		}
		if idx < 0 {
			manifest.Saga.Members = append(manifest.Saga.Members, SagaMember{
				ID:        spaceID,
				After:     append([]string(nil), after...),
				CreatedAt: memberManifest.CreatedAt,
			})
		} else {
			member := &manifest.Saga.Members[idx]
			if clearAfter {
				member.After = nil
			}
			if len(after) > 0 {
				member.After = append([]string(nil), after...)
			}
			member.CreatedAt = memberManifest.CreatedAt
		}
		*updated = *manifest
		return nil
	}
}

// sagaAddMemberEffects prints the registration line and runs the member-side
// best-effort effects shared by SagaAdd and create --saga. The locks protect
// the saga manifest; member files are a documented accepted race. MCP wiring
// happens ONLY when the member has no memory attachment of its own — a
// member's own den always owns its MCP configs (Deviation 18).
func (s Service) sagaAddMemberEffects(ctx context.Context, sagaID, spaceID, memberPath string, memberManifest, updated Manifest, siblings []SpaceEntry) {
	s.printf("added %s to saga %s\n", spaceID, sagaID)
	s.warnStackedBases(sagaID, spaceID, memberManifest, updated, siblings)
	if len(memberManifest.Memories) == 0 {
		if den, ok := findOwnedMemory(updated); ok {
			s.wireSagaDenMCP(ctx, den, memberPath, spaceID)
		}
	}
	if err := s.writeAgents(memberPath, memberManifest); err != nil {
		s.printf("notice: could not refresh %s for member %s: %v\n", AgentsName, spaceID, err)
	}
}

// createInSaga is Create with opts.SagaID set: PREFLIGHT, space creation and
// roster registration run under ONE membership + per-saga lock acquisition so
// a concurrent membership change cannot invalidate the preflight (the locks
// are non-reentrant, so lock-held state threads through rather than being
// re-acquired mid-flow).
func (s Service) createInSaga(ctx context.Context, opts CreateOptions) error {
	if opts.Saga != nil || opts.viaSaga {
		return fmt.Errorf("a saga cannot be created as a member of another saga")
	}
	sagaID := opts.SagaID
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return err
	}
	if opts.ID == sagaID {
		return fmt.Errorf("saga %q cannot be its own member", sagaID)
	}
	run := func() error {
		sagaManifest, err := LoadManifest(sagaPath)
		if err != nil {
			return err
		}
		if sagaManifest.Saga == nil {
			return fmt.Errorf("space %q is not a saga", sagaID)
		}
		// PREFLIGHT — manifest-level checks only, before anything is created:
		// a roster copy gains the candidate member and revalidates, rejecting
		// bad member-id charset, duplicates, after edges outside
		// members ∪ {new id} and cycles while the filesystem is untouched.
		candidate := append(append([]SagaMember(nil), sagaManifest.Saga.Members...), SagaMember{
			ID:    opts.ID,
			After: append([]string(nil), opts.After...),
		})
		if err := validateSagaMembers(candidate); err != nil {
			return err
		}
		inner := opts
		inner.SagaID = ""
		inner.After = nil
		// The saga member's realized set is captured by the OUTER Create,
		// outside the per-saga lock (D2); suppress the inner recording.
		inner.suppressCapture = true
		inner.Edits, err = s.resolveAfterBases(opts)
		if err != nil {
			return err
		}
		if err := s.Create(ctx, inner); err != nil {
			return err
		}
		if opts.DryRun {
			s.printf("dry-run: add %s to saga %s\n", opts.ID, sagaID)
			return nil
		}
		memberPath := s.SpacePath(opts.ID)
		var memberManifest, updated Manifest
		var siblings []SpaceEntry
		if err := s.mutateSagaManifestLocked(sagaID, sagaPath, s.sagaAddMutation(sagaID, opts.ID, opts.After, false, &memberManifest, &updated, &siblings)); err != nil {
			s.printf("notice: space %s was created but NOT registered in saga %s: %v\n", opts.ID, sagaID, err)
			s.printf("recover with: stave saga add %s %s\n", sagaID, opts.ID)
			return err
		}
		s.sagaAddMemberEffects(ctx, sagaID, opts.ID, memberPath, memberManifest, updated, siblings)
		return nil
	}
	if opts.DryRun {
		return run()
	}
	return s.withMembershipLock(func() error {
		return s.withSagaLock(sagaID, run)
	})
}

// resolveAfterBases applies the --after default-base rule: an edit spec with
// NO explicit base stacks on the predecessor's persisted branch
// (refs/heads/<branch>) when EXACTLY ONE --after target edits the same repo
// (canonical BareRepoPath identity; reference-only entries never match). Zero
// matches leave the spec for the default-base chain; multiple matches are
// ambiguous and demand an explicit base.
func (s Service) resolveAfterBases(opts CreateOptions) ([]RepoSpec, error) {
	edits := append([]RepoSpec(nil), opts.Edits...)
	if len(opts.After) == 0 {
		return edits, nil
	}
	type predecessor struct {
		id     string
		branch string
	}
	editsByRepo := map[string][]predecessor{} // canonical bare path → predecessors
	seenAfter := map[string]bool{}
	for _, id := range opts.After {
		if seenAfter[id] {
			continue
		}
		seenAfter[id] = true
		manifest, err := LoadManifest(s.SpacePath(id))
		if err != nil {
			return nil, fmt.Errorf("--after %s: %w", id, err)
		}
		for _, repo := range manifest.Repos {
			if repo.Mode != ModeEdit || repo.Branch == "" {
				continue
			}
			key := canonicalRepoPath(repo.BareRepoPath)
			editsByRepo[key] = append(editsByRepo[key], predecessor{id: id, branch: repo.Branch})
		}
	}
	for i, spec := range edits {
		if spec.Ref != "" {
			continue
		}
		repoCfg, ok := s.Config.Repos[spec.Name]
		if !ok {
			continue // Create itself reports the unregistered repo
		}
		preds := editsByRepo[canonicalRepoPath(repoCfg.BareRepoPath)]
		switch len(preds) {
		case 0:
			// default-base chain
		case 1:
			edits[i].Ref = "refs/heads/" + preds[0].branch
		default:
			ids := make([]string, len(preds))
			for j, pred := range preds {
				ids[j] = pred.id
			}
			return nil, fmt.Errorf("repo %q: multiple --after predecessors edit it (%s); pick an explicit base with -e %s:space:<id>", spec.Name, strings.Join(ids, ", "), spec.Name)
		}
	}
	return edits, nil
}

// SagaRemove deletes spaceID from sagaID's roster. Other members' after-edges
// referencing the removed member are dropped (they would otherwise dangle and
// fail Validate on save). Saga-den MCP entries are stripped from the member
// only when the member has no attachment of its own — otherwise its MCP
// configs belong to its own den and are left untouched.
func (s Service) SagaRemove(ctx context.Context, sagaID, spaceID string) error {
	memberPath, err := s.resolveSpacePath(spaceID)
	if err != nil {
		return err
	}
	var updated Manifest
	if err := s.mutateSagaManifest(sagaID, func(manifest *Manifest) error {
		members := manifest.Saga.Members
		idx := -1
		for i, member := range members {
			if member.ID == spaceID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("space %q is not a member of saga %q", spaceID, sagaID)
		}
		manifest.Saga.Members = append(members[:idx], members[idx+1:]...)
		for i := range manifest.Saga.Members {
			member := &manifest.Saga.Members[i]
			kept := member.After[:0]
			for _, a := range member.After {
				if a != spaceID {
					kept = append(kept, a)
				}
			}
			if len(kept) == 0 {
				member.After = nil
			} else {
				member.After = kept
			}
		}
		updated = *manifest
		return nil
	}); err != nil {
		return err
	}
	s.printf("removed %s from saga %s\n", spaceID, sagaID)
	// Member-side best-effort cleanup (see SagaAdd for the race posture). A
	// member that no longer loads has nothing member-side to clean.
	memberManifest, err := LoadManifest(memberPath)
	if err != nil {
		return nil
	}
	if len(memberManifest.Memories) == 0 {
		if den, ok := findOwnedMemory(updated); ok {
			s.unwireSagaDenMCP(ctx, den, memberPath, spaceID)
		}
	}
	if err := s.writeAgents(memberPath, memberManifest); err != nil {
		s.printf("notice: could not refresh %s for member %s: %v\n", AgentsName, spaceID, err)
	}
	return nil
}

// SagaArchiveOptions drives SagaArchive, mirroring ArchiveOptions minus the
// space id. MemoryFate (keep or contribute; destroy is refused exactly as
// single-space Archive refuses it) applies to the SAGA space's own
// attachments — member teardowns always run with the default keep fate, so
// member-owned dens survive as durable residue.
type SagaArchiveOptions struct {
	Force  bool
	DryRun bool
	// MemoryFate: keep (default) or contribute (contribute-then-keep).
	MemoryFate memory.MemoryFate // empty → keep
}

// SagaDestroyOptions drives SagaDestroy, mirroring DestroyOptions minus the
// space id. MemoryFate applies to the saga den (the saga space's own
// attachments); members always destroy with the default keep fate.
type SagaDestroyOptions struct {
	Force      bool
	DryRun     bool
	MemoryFate memory.MemoryFate // empty → keep
}

// SagaArchive archives every member of sagaID (reverse topological order of
// the after-DAG) and the saga space itself last, all under one membership +
// per-saga lock hold. Archived and missing members skip with a printed note,
// so retries converge; a corrupt member aborts up front; dirty edit worktrees
// across ALL live members refuse before any teardown (--force overrides).
func (s Service) SagaArchive(ctx context.Context, sagaID string, opts SagaArchiveOptions) error {
	if opts.MemoryFate == memory.FateDestroy {
		return fmt.Errorf("archive does not destroy memory; use 'stave saga destroy --memory destroy' instead")
	}
	return s.sagaTeardown(ctx, sagaID, sagaTeardownSpec{
		verb:   "archive",
		past:   "archived",
		force:  opts.Force,
		dryRun: opts.DryRun,
		fate:   opts.MemoryFate,
	})
}

// SagaDestroy destroys every member of sagaID (reverse topological order) and
// the saga space itself last, under one membership + per-saga lock hold. Only
// MISSING members skip; archived members are reported with their .archive/
// paths and never auto-removed. A den-destroying fate refuses while any
// non-member space still shares the saga den (--force overrides).
func (s Service) SagaDestroy(ctx context.Context, sagaID string, opts SagaDestroyOptions) error {
	return s.sagaTeardown(ctx, sagaID, sagaTeardownSpec{
		verb:    "destroy",
		past:    "destroyed",
		destroy: true,
		force:   opts.Force,
		dryRun:  opts.DryRun,
		fate:    opts.MemoryFate,
	})
}

// sagaTeardownSpec parameterizes the shared archive/destroy walk.
type sagaTeardownSpec struct {
	verb    string
	past    string
	destroy bool
	force   bool
	dryRun  bool
	fate    memory.MemoryFate
}

// sagaTeardown acquires the membership + per-saga locks for the WHOLE
// operation — lifecycle mutates cross-saga-visible state (members leave
// ListSpaces, stacked-base ownership changes) — and runs the walk inside.
func (s Service) sagaTeardown(ctx context.Context, sagaID string, spec sagaTeardownSpec) error {
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return err
	}
	return s.withMembershipLock(func() error {
		return s.withSagaLock(sagaID, func() error {
			return s.sagaTeardownLocked(ctx, sagaID, sagaPath, spec)
		})
	})
}

// sagaTeardownLocked is the walk body, entered with both locks held: resolve
// all member states, fail fast on guards, then tear members down in reverse
// topological order with the saga space last. Same-operation members are
// exempted from the dependent-base guard; external dependents still refuse.
func (s Service) sagaTeardownLocked(ctx context.Context, sagaID, sagaPath string, spec sagaTeardownSpec) error {
	manifest, err := LoadManifest(sagaPath)
	if err != nil {
		return err
	}
	if manifest.Saga == nil {
		return fmt.Errorf("space %q is not a saga", sagaID)
	}
	plan := s.sagaTeardownOrder(manifest)
	for _, p := range plan {
		if p.State == MemberCorrupt {
			return fmt.Errorf("member %s has a corrupt manifest (%s); saga %s aborted with nothing torn down", p.ID, p.Detail, spec.verb)
		}
	}
	exempt := make([]string, 0, len(manifest.Saga.Members))
	for _, member := range manifest.Saga.Members {
		exempt = append(exempt, member.ID)
	}
	// Fail-fast guards across ALL live members BEFORE any teardown, so a
	// refusal leaves the whole saga intact. The dependent-base scan runs here
	// too: an external space stacking on a topologically-early member (torn
	// down LAST) would otherwise only refuse mid-walk, after later members are
	// already gone. The in-walk check inside Archive/Destroy stays as defense
	// in depth.
	//
	// The SAGA SPACE's own two guards are deliberately not hoisted; they run
	// only when Archive/Destroy reaches it at the final step, and that is safe
	// for both. Dirty edits: a saga space is a coordination shell holding no
	// edit worktrees of its own (members own the branches), so the scan has
	// nothing to find and cannot produce a late refusal. Dependents: every
	// space that stacks on the saga is a member, and members are on the exempt
	// list, so the only refusal the late scan can raise comes from an EXTERNAL
	// dependent — which the member pass above has no visibility into anyway
	// and which legitimately blocks the saga space alone.
	if !spec.force {
		for _, p := range plan {
			if p.State != MemberLive {
				continue
			}
			memberManifest, err := LoadManifest(s.SpacePath(p.ID))
			if err != nil {
				return fmt.Errorf("member %s: %w", p.ID, err)
			}
			if err := s.guardRefusal(s.ensureNoDirtyEdits(ctx, s.SpacePath(p.ID), memberManifest), spec.dryRun); err != nil {
				return err
			}
			if err := s.guardRefusal(s.ensureNoDependentSpaces(p.ID, memberManifest, exempt), spec.dryRun); err != nil {
				return err
			}
		}
		if spec.destroy && spec.fate != "" && spec.fate != memory.FateKeep {
			if err := s.guardRefusal(s.ensureSagaDenUnshared(sagaID, manifest), spec.dryRun); err != nil {
				return err
			}
		}
	}
	// Den-first predicate: a destroying fate on a saga that owns a den. Same
	// fate predicate as the den-refcount guard above, which stays a preflight —
	// it must still fire before the den is touched.
	den, hasDen := findOwnedMemory(manifest)
	denFirst := spec.destroy && hasDen && spec.fate != "" && spec.fate != memory.FateKeep
	if spec.dryRun {
		s.printf("dry-run: saga %s plan for %s (members in reverse topological order, saga space last):\n", spec.verb, sagaID)
		step := 1
		if denFirst {
			s.printf("dry-run: %d. destroy the saga den %s first (fate %s; a live agent serve refuses here with nothing destroyed)\n", step, den.ID, spec.fate)
			step++
		}
		for _, p := range plan {
			switch p.State {
			case MemberArchived:
				if spec.destroy {
					s.printf("dry-run: %d. report member %s: archived at %s (left in place)\n", step, p.ID, p.Detail)
				} else {
					s.printf("dry-run: %d. skip member %s: already archived at %s\n", step, p.ID, p.Detail)
				}
			case MemberMissing:
				s.printf("dry-run: %d. skip member %s: missing\n", step, p.ID)
			default:
				s.printf("dry-run: %d. %s member %s\n", step, spec.verb, p.ID)
			}
			step++
		}
		s.printf("dry-run: %d. %s saga space %s\n", step, spec.verb, sagaID)
	}
	// Den-first: destroy the saga den BEFORE any member teardown, so marmot's
	// source_in_use refusal (a live agent serve on the den — any member's,
	// since they all point at it) fails the whole operation while every member
	// and the saga space are still intact. Deliberately OUTSIDE the !force
	// guard: stave's own guards above are force-skippable, marmot's refusal is
	// not. detachMemoryLocked (both locks held here) strips the members' saga-
	// den MCP wiring first and restores it on refusal, and its splice-save
	// removes the owned attachment so the final saga-space teardown finds it
	// already handled. In dry-run it previews the provider's real destroy argv
	// right after the numbered plan — though a dry-run cannot predict a live-
	// serve refusal (marmot's dry-run returns before lock acquisition).
	if denFirst {
		if err := s.detachMemoryLocked(ctx, sagaPath, sagaID, den.Name, spec.fate, spec.force, spec.dryRun); err != nil {
			s.reportSagaTeardownFailure(sagaID, spec, "the saga den", nil)
			return fmt.Errorf("saga %s: %w", spec.verb, err)
		}
	}
	teardown := func(id string, fate memory.MemoryFate, skipStoreID string) error {
		if spec.destroy {
			// sagaLockHeld: this walk holds sagaID's per-saga lock; only the
			// saga space itself (id == sagaID) has a Saga manifest, so only its
			// destroying-fate splice-saves would otherwise re-acquire — and
			// deadlock on — that lock (fsio flock is non-reentrant).
			return s.Destroy(ctx, DestroyOptions{SpaceID: id, Force: spec.force, DryRun: spec.dryRun, MemoryFate: fate, exemptDependentSpaces: exempt, sagaLockHeld: id == sagaID, skipMemoryStoreID: skipStoreID})
		}
		return s.Archive(ctx, ArchiveOptions{SpaceID: id, Force: spec.force, DryRun: spec.dryRun, MemoryFate: fate, exemptDependentSpaces: exempt})
	}
	var completed []string
	for _, p := range plan {
		switch p.State {
		case MemberArchived:
			if spec.destroy {
				// Never silently skipped, never auto-removed: the archive is the
				// user's to keep or delete.
				s.printf("member %s is archived at %s; destroy leaves archives in place (remove it manually if desired)\n", p.ID, p.Detail)
			} else {
				s.printf("skipping member %s: already archived at %s\n", p.ID, p.Detail)
			}
			continue
		case MemberMissing:
			s.printf("skipping member %s: missing\n", p.ID)
			continue
		}
		if err := teardown(p.ID, "", ""); err != nil {
			s.reportSagaTeardownFailure(sagaID, spec, "member "+p.ID, completed)
			return fmt.Errorf("saga %s: member %s: %w", spec.verb, p.ID, err)
		}
		completed = append(completed, p.ID)
	}
	// The saga space itself, last: references only, memory fate applies to
	// the saga den (the window-guard is CLI-layer, so no interference here).
	// A den handled den-first is skipped by store id — ONLY that one, so the
	// fate loop still keep-detaches any additional unowned attachments (their
	// routes and MCP wiring must not dangle), and a dry-run does not preview
	// the den's destroy lines twice.
	denSkip := ""
	if denFirst {
		denSkip = den.ID
	}
	if err := teardown(sagaID, spec.fate, denSkip); err != nil {
		s.reportSagaTeardownFailure(sagaID, spec, "the saga space", completed)
		return fmt.Errorf("saga %s: %w", spec.verb, err)
	}
	return nil
}

// SagaMemberState is one saga member's resolved lifecycle state (live,
// archived, missing, corrupt) with its detail (archive path, corrupt reason).
type SagaMemberState struct {
	ID     string
	State  MemberState
	Detail string
}

// sagaTeardownOrder resolves every member of manifest in reverse topological
// order — dependents before the members they land after — the exact walk
// SagaArchive and SagaDestroy take, so a stacked branch never outlives its
// base mid-walk.
func (s Service) sagaTeardownOrder(manifest Manifest) []SagaMemberState {
	order := sagaTopoOrder(manifest.Saga.Members)
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	plan := make([]SagaMemberState, 0, len(order))
	for _, member := range order {
		state, detail := s.resolveMemberState(member)
		plan = append(plan, SagaMemberState{ID: member.ID, State: state, Detail: detail})
	}
	return plan
}

// SagaMemberStates reports sagaID's members in teardown order with their
// current states; machine-readable saga archive/destroy build their per-member
// report from it. Missing saga → SpaceNotFoundError; non-saga → error.
func (s Service) SagaMemberStates(sagaID string) ([]SagaMemberState, error) {
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return nil, err
	}
	manifest, err := loadLiveManifest(sagaID, sagaPath)
	if err != nil {
		return nil, err
	}
	if manifest.Saga == nil {
		return nil, fmt.Errorf("space %q is not a saga", sagaID)
	}
	return s.sagaTeardownOrder(manifest), nil
}

// reportSagaTeardownFailure prints the partial-failure handoff: which members
// completed before the stop and that a retry converges (completed members
// resolve archived/missing and skip).
func (s Service) reportSagaTeardownFailure(sagaID string, spec sagaTeardownSpec, failedAt string, completed []string) {
	s.printf("saga %s stopped at %s; the saga record is intact\n", spec.verb, failedAt)
	if len(completed) > 0 {
		s.printf("members already %s this run: %s\n", spec.past, strings.Join(completed, ", "))
	}
	s.printf("fix the cause and re-run 'stave saga %s %s': completed members are skipped, so retries converge\n", spec.verb, sagaID)
}

// ensureSagaDenUnshared is the den-refcount guard for a den-destroying
// SagaDestroy: any NON-member space holding an unowned attachment of the saga
// den would be stranded by its destruction, so the destroy refuses naming the
// sharer (--force overrides). Members are excluded — they are torn down in
// the same operation, and their unowned attachments detach with keep fate,
// which never touches the den (the den itself is destroyed FIRST, before any
// member teardown). Fails closed on unreadable siblings.
func (s Service) ensureSagaDenUnshared(sagaID string, manifest Manifest) error {
	den, ok := findOwnedMemory(manifest)
	if !ok {
		return nil
	}
	members := make(map[string]bool, len(manifest.Saga.Members))
	for _, member := range manifest.Saga.Members {
		members[member.ID] = true
	}
	siblings, err := s.ListSpaces()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, sibling := range siblings {
		if sibling.ID == sagaID || members[sibling.ID] {
			continue
		}
		if sibling.Err != nil {
			return fmt.Errorf("space %q has an unreadable manifest (%v); cannot verify it does not share the saga den %q (use --force to override)", sibling.ID, sibling.Err, den.ID)
		}
		for _, mem := range sibling.Manifest.Memories {
			if !mem.Owned && mem.ID == den.ID {
				return fmt.Errorf("space %q shares the saga den %q (attachment %q); destroying the den would strand it (use --force to override)", sibling.ID, den.ID, mem.Name)
			}
		}
	}
	return nil
}

// SagaListEntry is one row from SagaList: every space under AgentWorkDir with
// its kind and saga-membership join.
type SagaListEntry struct {
	ID string
	// Path is the space directory. The CLI does not currently render it;
	// reserved for machine consumers and future output modes.
	Path string
	Kind string
	// IsSaga mirrors the single saga predicate (manifest.Saga != nil).
	IsSaga bool
	// Members is the roster in manifest order (saga rows only).
	Members []string
	// MemberOf is the id of the saga listing this space, empty when none.
	MemberOf string
	// Err records a manifest read/parse failure; Kind/IsSaga/Members are
	// zero-valued then.
	Err error
}

// SagaList joins ListSpaces with saga membership: all spaces, each carrying
// its kind, whether it is a saga, its roster (sagas) and its owning saga
// (members). Unlocked snapshot reader by design.
func (s Service) SagaList() ([]SagaListEntry, error) {
	spaces, err := s.ListSpaces()
	if err != nil {
		return nil, err
	}
	memberOf := map[string]string{}
	for _, entry := range spaces {
		if entry.Err != nil || entry.Manifest.Saga == nil {
			continue
		}
		for _, member := range entry.Manifest.Saga.Members {
			if _, taken := memberOf[member.ID]; !taken {
				memberOf[member.ID] = entry.ID
			}
		}
	}
	entries := make([]SagaListEntry, 0, len(spaces))
	for _, entry := range spaces {
		row := SagaListEntry{ID: entry.ID, Path: entry.Path, MemberOf: memberOf[entry.ID], Err: entry.Err}
		if entry.Err == nil {
			row.Kind = entry.Manifest.Kind
			if entry.Manifest.Saga != nil {
				row.IsSaga = true
				for _, member := range entry.Manifest.Saga.Members {
					row.Members = append(row.Members, member.ID)
				}
			}
		}
		entries = append(entries, row)
	}
	return entries, nil
}

// MemberState classifies a saga member's filesystem lifecycle state.
type MemberState string

const (
	MemberLive     MemberState = "live"
	MemberArchived MemberState = "archived"
	MemberMissing  MemberState = "missing"
	MemberCorrupt  MemberState = "corrupt"
)

// resolveMemberState resolves one member by filesystem evidence. live = the
// member directory holds a loadable manifest whose ID matches AND whose
// CreatedAt matches the recorded incarnation stamp when both are non-zero (a
// destroyed member's id reused by an unrelated space must not be treated as
// the member — teardown would destroy the stranger); corrupt = the manifest
// exists but cannot be read/parsed; archived = a directory under .archive/
// named exactly <id> or <id>-<14 digits> holds a manifest with the member's
// ID whose CreatedAt equals the recorded incarnation stamp (zero on either
// side is always a mismatch — a stale archive of a reused id must not
// masquerade as the member); everything else is missing. The detail string
// carries the read error (corrupt), the archive path (archived), or the
// reused-id explanation (missing with a live impostor).
func (s Service) resolveMemberState(member SagaMember) (MemberState, string) {
	reusedDetail := ""
	manifest, err := LoadManifest(s.SpacePath(member.ID))
	switch {
	case err == nil:
		if manifest.ID == member.ID {
			if member.CreatedAt.IsZero() || manifest.CreatedAt.IsZero() || manifest.CreatedAt.Equal(member.CreatedAt) {
				return MemberLive, ""
			}
			// A different incarnation lives under the member's id; the real
			// member may still be archived, so the scan below still runs.
			reusedDetail = fmt.Sprintf("id reused by a different space (created %s)", manifest.CreatedAt.Format(time.RFC3339))
		}
		// The directory holds some other space's manifest; fall through.
	case !errors.Is(err, os.ErrNotExist):
		return MemberCorrupt, err.Error()
	}
	if path, ok := s.findArchivedMember(member); ok {
		return MemberArchived, path
	}
	return MemberMissing, reusedDetail
}

// findArchivedMember scans .archive/ for a verified archive of the member:
// exact name match (or the Archive collision-suffix shape), manifest ID match,
// and the CreatedAt incarnation check. Unverifiable candidates (unreadable
// manifests) never match.
func (s Service) findArchivedMember(member SagaMember) (string, bool) {
	archiveRoot := filepath.Join(s.Config.AgentWorkDir, ".archive")
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !archiveNameMatches(entry.Name(), member.ID) {
			continue
		}
		path := filepath.Join(archiveRoot, entry.Name())
		manifest, err := LoadManifest(path)
		if err != nil || manifest.ID != member.ID {
			continue
		}
		if member.CreatedAt.IsZero() || manifest.CreatedAt.IsZero() {
			continue
		}
		if !manifest.CreatedAt.Equal(member.CreatedAt) {
			continue
		}
		return path, true
	}
	return "", false
}

// archiveNameMatches reports whether an .archive/ entry name belongs to id:
// exactly <id>, or <id>- followed by exactly 14 digits (Archive's collision
// timestamp suffix). No blanket prefix match — member "pay" must not claim a
// sibling's ".archive/pay-web".
func archiveNameMatches(name, id string) bool {
	if name == id {
		return true
	}
	suffix, ok := strings.CutPrefix(name, id+"-")
	if !ok || len(suffix) != 14 {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resolveBaseOwner maps a recorded edit base to the sibling space that owns
// the branch it names. The authoritative match is against siblings' edit-mode
// Repos[].Branch values scoped to the same canonical BareRepoPath (same-named
// branches in different repos must not alias), in exactly the spellings
// refs/heads/<b>, origin/<b> and <b>. The stave/<id>/<repo> naming convention
// is the fallback for owners no longer listed (archived/destroyed spaces).
func resolveBaseOwner(base, bareRepoPath string, siblings []SpaceEntry) (string, bool) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", false
	}
	canonical := canonicalRepoPath(bareRepoPath)
	for _, sibling := range siblings {
		if sibling.Err != nil || sibling.Manifest == nil {
			continue
		}
		for _, repo := range sibling.Manifest.Repos {
			if repo.Mode != ModeEdit || repo.Branch == "" {
				continue
			}
			if canonicalRepoPath(repo.BareRepoPath) != canonical {
				continue
			}
			switch base {
			case "refs/heads/" + repo.Branch, "origin/" + repo.Branch, repo.Branch:
				return sibling.ID, true
			}
		}
	}
	branch := strings.TrimPrefix(base, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")
	if rest, ok := strings.CutPrefix(branch, "stave/"); ok {
		if id, repoName, found := strings.Cut(rest, "/"); found && repoName != "" && config.ValidateSpaceID(id) == nil {
			return id, true
		}
	}
	return "", false
}

// canonicalRepoPath canonicalizes a bare-repo path for identity comparison
// (symlinked roots — macOS $TMPDIR — must not defeat the same-repo scoping).
func canonicalRepoPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// warnStackedBases reports — never rejects — member edit bases whose owning
// space is outside the saga, or inside it but not among the member's
// (transitive) after-predecessors. DAG-vs-stack divergence is advisory.
func (s Service) warnStackedBases(sagaID, memberID string, memberManifest, sagaManifest Manifest, siblings []SpaceEntry) {
	roster := map[string]bool{}
	for _, member := range sagaManifest.Saga.Members {
		roster[member.ID] = true
	}
	preds := sagaPredecessors(sagaManifest.Saga.Members, memberID)
	for _, repo := range memberManifest.Repos {
		if repo.Mode != ModeEdit || repo.Base == "" {
			continue
		}
		owner, ok := resolveBaseOwner(repo.Base, repo.BareRepoPath, siblings)
		if !ok || owner == memberID {
			continue
		}
		switch {
		case !roster[owner]:
			s.printf("warning: member %s repo %s stacks on base %s owned by space %q outside saga %s\n", memberID, repo.Name, repo.Base, owner, sagaID)
		case !preds[owner]:
			s.printf("warning: member %s repo %s stacks on base %s owned by %q, which is not among its after-predecessors\n", memberID, repo.Name, repo.Base, owner)
		}
	}
}

// sagaPredecessors returns the transitive after-closure of memberID over the
// roster's edges.
func sagaPredecessors(members []SagaMember, memberID string) map[string]bool {
	afterOf := make(map[string][]string, len(members))
	for _, member := range members {
		afterOf[member.ID] = member.After
	}
	preds := map[string]bool{}
	queue := append([]string(nil), afterOf[memberID]...)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if preds[id] {
			continue
		}
		preds[id] = true
		queue = append(queue, afterOf[id]...)
	}
	return preds
}

// findOwnedMemory returns the manifest's owned attachment — "the saga den".
// At-most-one owned store is enforced at saga create, so lookup is
// unambiguous; zero owned attachments means all den wiring no-ops.
func findOwnedMemory(manifest Manifest) (MemoryManifest, bool) {
	for _, mem := range manifest.Memories {
		if mem.Owned {
			return mem, true
		}
	}
	return MemoryManifest{}, false
}

// wireSagaDenMCP writes the saga den's space-local MCP configs into a member.
// Soft by design: the membership record is already committed, so any failure
// prints a notice and the member simply lacks wiring.
func (s Service) wireSagaDenMCP(ctx context.Context, den MemoryManifest, memberPath, memberID string) {
	prov, err := s.provider(den.Provider)
	if err != nil {
		s.printf("notice: saga den MCP wiring skipped for %s: %v\n", memberID, err)
		return
	}
	wirer, ok := prov.(mcpWirer)
	if !ok {
		return
	}
	if err := wirer.WriteMCPConfig(ctx, memberPath, den.ID); err != nil {
		s.printf("notice: saga den MCP wiring failed for %s: %v\n", memberID, err)
		return
	}
	s.printf("wired saga den %s MCP config into %s\n", den.ID, memberID)
}

// stripSagaDenMemberWiring strips the saga den's MCP bindings from every
// member that has no memory attachment of its own — exactly the set
// sagaAddMemberEffects wired; a member's own den always owns its configs —
// before a den-destroying detach, so no member client started from a stale
// config can re-acquire the den mid-destroy (the provider closes the same
// window for the saga space's own wiring). It returns a restore closure the
// caller runs when the destroy fails: only failures KNOWN to leave the den
// intact re-wire (memory.DenIntactAfterFailedDestroy — the same scoping the
// provider uses for the saga space's own wiring restore); after an ambiguous
// failure the members stay unwired rather than be rewired at a possibly
// destroyed den. Both directions are member-side best-effort, like all den
// wiring (unwire/wire print notices, never fail the verb).
func (s Service) stripSagaDenMemberWiring(ctx context.Context, manifest Manifest, den MemoryManifest) func(cause error) {
	type strippedMember struct {
		id   string
		path string
	}
	var stripped []strippedMember
	for _, member := range manifest.Saga.Members {
		memberPath := s.SpacePath(member.ID)
		memberManifest, err := LoadManifest(memberPath)
		if err != nil || len(memberManifest.Memories) > 0 {
			continue // missing/unreadable members have nothing to strip
		}
		s.unwireSagaDenMCP(ctx, den, memberPath, member.ID)
		stripped = append(stripped, strippedMember{id: member.ID, path: memberPath})
	}
	return func(cause error) {
		if !memory.DenIntactAfterFailedDestroy(cause) {
			return
		}
		for _, member := range stripped {
			s.wireSagaDenMCP(ctx, den, member.path, member.id)
		}
	}
}

// unwireSagaDenMCP strips the saga den's MCP entries from a member (other
// servers and settings are preserved by the provider). Soft, like wiring.
func (s Service) unwireSagaDenMCP(ctx context.Context, den MemoryManifest, memberPath, memberID string) {
	prov, err := s.provider(den.Provider)
	if err != nil {
		s.printf("notice: saga den MCP cleanup skipped for %s: %v\n", memberID, err)
		return
	}
	wirer, ok := prov.(mcpWirer)
	if !ok {
		return
	}
	if err := wirer.RemoveMCPConfig(ctx, memberPath); err != nil {
		s.printf("notice: saga den MCP cleanup failed for %s: %v\n", memberID, err)
	}
}
