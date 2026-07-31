package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/gh"
)

// BaseHealth values for SagaRepoStatus.BaseHealth (frozen contract).
const (
	BaseHealthOK            = "ok"
	BaseHealthMerged        = "merged"
	BaseHealthMissing       = "missing"
	BaseHealthOwnerArchived = "owner_archived"
	BaseHealthUnknown       = "unknown"
)

// MergedVia values for SagaRepoStatus.MergedVia (set only when merged).
const (
	MergedViaAncestry = "ancestry"
	MergedViaPR       = "pr"
)

// SagaNote kinds: degraded probes and advisory suggestions.
const (
	NoteKindDegraded   = "degraded"
	NoteKindSuggestion = "suggestion"
)

// SagaStatus is the FROZEN status contract for one saga (the `--json` shape):
// members in topo order with lifecycle state, dirtiness, drift, base health
// and live PR state, plus saga-level advisory notes.
type SagaStatus struct {
	SagaID  string             `json:"saga_id"`
	Members []SagaMemberStatus `json:"members"`         // topo order
	Notes   []SagaNote         `json:"notes,omitempty"` // degradation + suggestions
}

// SagaMemberStatus is one member row of SagaStatus.
type SagaMemberStatus struct {
	ID    string   `json:"id"`
	After []string `json:"after,omitempty"`
	// State is live|archived|missing|corrupt.
	State MemberState `json:"state"`
	// Error carries the read error (corrupt), the archive path (archived), or
	// the reused-id explanation (missing with a live impostor).
	Error string `json:"error,omitempty"`
	// Dirty reports whether ANY edit worktree is dirty; false for non-live.
	Dirty bool `json:"dirty"`
	// Repos covers the member's edit repos; empty for non-live members.
	Repos []SagaRepoStatus `json:"repos,omitempty"`
	// PRs is live PR state for the member's branches, populated only when a
	// PRLookup ran this invocation.
	PRs []SagaPRStatus `json:"prs,omitempty"`
}

// SagaRepoStatus is drift and base health for one member edit repo.
type SagaRepoStatus struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	// BaseHealth is ok|merged|missing|owner_archived|unknown.
	BaseHealth string `json:"base_health"`
	// MergedVia is ancestry|pr, set only when BaseHealth is merged.
	MergedVia string `json:"merged_via,omitempty"`
	// Note carries topology warnings, unknown reasons and degraded probes.
	Note string `json:"note,omitempty"`
}

// SagaPRStatus is one live pull request whose head is a member branch.
type SagaPRStatus struct {
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	State       string `json:"state,omitempty"`
	MergedAt    string `json:"merged_at,omitempty"`
	BaseRefName string `json:"base_ref_name,omitempty"`
}

// SagaNote is a saga-level advisory finding.
type SagaNote struct {
	Kind   string `json:"kind"`
	Member string `json:"member,omitempty"`
	Text   string `json:"text"`
}

// canonicalBaseRef canonicalizes every base spelling to ONE full ref for
// existence and ancestry checks against the bare repo: "origin/x" is the
// remote-tracking ref refs/remotes/origin/x, a bare name is refs/heads/<name>,
// and refs/* spellings pass verbatim. gh baseRefName values canonicalize via
// the origin/ spelling ("origin/"+name) since PR targets live on the remote.
func canonicalBaseRef(ref string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return ""
	case strings.HasPrefix(ref, "refs/"):
		return ref
	case strings.HasPrefix(ref, "origin/"):
		return "refs/remotes/" + ref
	default:
		return "refs/heads/" + ref
	}
}

// baseBranchName strips the ref namespace from a base spelling, leaving the
// bare branch name PR heads are listed by.
func baseBranchName(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimPrefix(base, "refs/remotes/")
	base = strings.TrimPrefix(base, "refs/heads/")
	return strings.TrimPrefix(base, "origin/")
}

// mergeDetector computes base health for member edit repos: layer 1 is commit
// ancestry in the bare repo, layer 2 (optional, Service.PRLookup) is live PR
// state. Lookups are cached per clone-URL+branch; the first lookup failure
// degrades the whole run to ancestry only (one aggregated note, never an
// error).
type mergeDetector struct {
	svc      Service
	lookup   func(ctx context.Context, cloneURL, headBranch string) ([]gh.PR, error)
	siblings []SpaceEntry
	// siblingsOK is false when the sibling scan itself failed: stacked-base
	// owners then cannot be resolved, so stacked detection short-circuits to
	// unknown instead of mislabeling LIVE owners as archived.
	siblingsOK bool
	// scanNoted dedupes the sibling-scan degraded note to one per run.
	scanNoted bool
	prCache   map[string][]gh.PR
	prErr     error
}

func (s Service) newMergeDetector(siblings []SpaceEntry) *mergeDetector {
	return &mergeDetector{svc: s, lookup: s.PRLookup, siblings: siblings, siblingsOK: true, prCache: map[string][]gh.PR{}}
}

// prsFor lists PRs by head branch through the seam, cached. After the first
// error the probe stays off for the rest of the run (soft degrade).
func (d *mergeDetector) prsFor(ctx context.Context, cloneURL, branch string) []gh.PR {
	if d.lookup == nil || d.prErr != nil || cloneURL == "" || branch == "" {
		return nil
	}
	key := cloneURL + "\x00" + branch
	if prs, ok := d.prCache[key]; ok {
		return prs
	}
	prs, err := d.lookup(ctx, cloneURL, branch)
	if err != nil {
		d.prErr = err
		return nil
	}
	d.prCache[key] = prs
	return prs
}

// degradeNote reports the single aggregated layer-2 degradation, if any.
func (d *mergeDetector) degradeNote() (SagaNote, bool) {
	if d.prErr == nil {
		return SagaNote{}, false
	}
	return SagaNote{Kind: NoteKindDegraded, Text: fmt.Sprintf("PR lookup unavailable (%v); merge detection degraded to commit ancestry only", d.prErr)}, true
}

func anyMergedPR(prs []gh.PR) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") {
			return true
		}
	}
	return false
}

// mergedByAncestry reports tip fully contained in target AND strictly behind
// it. The second check screens out branches whose tip simply EQUALS the
// target (a fresh branch with no commits of its own is trivially an ancestor
// of its base — reporting it merged would be noise on every new saga; the
// cost is that a just-fast-forwarded merge reads ok until the target moves).
func (d *mergeDetector) mergedByAncestry(ctx context.Context, bare, tip, target string) (bool, error) {
	contained, err := d.svc.Git.IsAncestor(ctx, bare, tip, target)
	if err != nil || !contained {
		return false, err
	}
	equalOrAhead, err := d.svc.Git.IsAncestor(ctx, bare, target, tip)
	if err != nil {
		return false, err
	}
	return !equalOrAhead, nil
}

// detect resolves one edit repo's base health, live PR rows and advisory
// notes. Base existence is checked for EVERY spelling via RefExists on the
// canonicalized full ref; merge verdicts follow the rules documented on each
// branch below. Read-only: nothing is persisted.
func (d *mergeDetector) detect(ctx context.Context, memberID string, repo RepoManifest) (SagaRepoStatus, []SagaPRStatus, []SagaNote) {
	rs := SagaRepoStatus{Name: repo.Name, Branch: repo.Branch, Base: repo.Base, BaseHealth: BaseHealthUnknown}
	var prRows []SagaPRStatus
	var notes []SagaNote
	cloneURL := d.svc.Config.Repos[repo.Name].URL
	ownPRs := d.prsFor(ctx, cloneURL, repo.Branch)
	for _, pr := range ownPRs {
		prRows = append(prRows, SagaPRStatus{Repo: repo.Name, Number: pr.Number, State: pr.State, MergedAt: pr.MergedAt, BaseRefName: pr.BaseRefName})
	}
	ownMerged := anyMergedPR(ownPRs)
	base := strings.TrimSpace(repo.Base)
	if base == "" {
		rs.Note = "no recorded base"
		return rs, prRows, notes
	}
	canonical := canonicalBaseRef(base)
	exists, err := d.svc.Git.RefExists(ctx, repo.BareRepoPath, canonical)
	switch {
	case err != nil:
		rs.Note = fmt.Sprintf("base %s: existence check failed: %v", canonical, err)
		return rs, prRows, notes
	case !exists:
		rs.BaseHealth = BaseHealthMissing
		return rs, prRows, notes
	}
	tip := "refs/heads/" + repo.Branch
	owner, owned := resolveBaseOwner(base, repo.BareRepoPath, d.siblings)
	switch {
	case owned && owner == memberID:
		rs.Note = fmt.Sprintf("base %s is this member's own branch; merge target unknown", base)
	case owned && !d.siblingsOK:
		// The owner resolved only via the name-parse fallback; without the
		// sibling scan a nil roster entry would mislabel a LIVE owner as
		// archived, so stacked detection stays unknown.
		rs.Note = fmt.Sprintf("base %s: sibling scan failed; merge target unknown", base)
		if !d.scanNoted {
			d.scanNoted = true
			notes = append(notes, SagaNote{Kind: NoteKindDegraded, Text: "sibling scan failed; stacked-base detection degraded to unknown"})
		}
	case owned:
		d.detectStacked(ctx, &rs, repo, owner, canonical, cloneURL)
	case strings.HasPrefix(canonical, "refs/remotes/"):
		// Ordinary remote base: merged when this member's own branch tip is
		// an ancestor of the base (the work landed), or its PR merged (squash
		// merges never become ancestors).
		merged, err := d.mergedByAncestry(ctx, repo.BareRepoPath, tip, canonical)
		switch {
		case err != nil:
			rs.Note = fmt.Sprintf("ancestry check against %s failed: %v", canonical, err)
		case merged:
			rs.BaseHealth, rs.MergedVia = BaseHealthMerged, MergedViaAncestry
		case ownMerged:
			rs.BaseHealth, rs.MergedVia = BaseHealthMerged, MergedViaPR
		default:
			rs.BaseHealth = BaseHealthOK
		}
	default:
		rs.Note = fmt.Sprintf("base %s matches no space's edit branch; merge target unknown", base)
	}
	// Squash divergence: the branch's own PR reports merged but the branch
	// never became an ancestor of its base — permanently divergent.
	if ownMerged && repo.Branch != "" {
		if contained, err := d.svc.Git.IsAncestor(ctx, repo.BareRepoPath, tip, canonical); err == nil && !contained {
			notes = append(notes, SagaNote{
				Kind:   NoteKindSuggestion,
				Member: memberID,
				Text:   fmt.Sprintf("repo %s: the PR for branch %s merged but the branch is not an ancestor of %s (permanently divergent after a squash merge); consider 'stave saga archive' or retargeting dependents", repo.Name, repo.Branch, canonical),
			})
		}
	}
	return rs, prRows, notes
}

// detectStacked resolves base health for a base owned by sibling space owner:
// the base is merged when its tip is an ancestor of the OWNER's own recorded
// base for that repo (one hop from the owner's manifest), with a live PR on
// the base branch overriding the recorded target (retargeted PRs) or — when
// itself MERGED — deciding outright. An archived/destroyed owner whose ref
// persists is owner_archived; a corrupt owner is unknown.
func (d *mergeDetector) detectStacked(ctx context.Context, rs *SagaRepoStatus, repo RepoManifest, owner, canonical, cloneURL string) {
	var entry *SpaceEntry
	for i := range d.siblings {
		if d.siblings[i].ID == owner {
			entry = &d.siblings[i]
			break
		}
	}
	switch {
	case entry == nil:
		rs.BaseHealth = BaseHealthOwnerArchived
		rs.Note = fmt.Sprintf("base owner %q is archived or destroyed; the ref persists in the bare repo", owner)
		return
	case entry.Err != nil || entry.Manifest == nil:
		rs.Note = fmt.Sprintf("base owner %q has an unreadable manifest; merge target unknown", owner)
		return
	}
	baseBranch := baseBranchName(repo.Base)
	target := ""
	canonicalBare := canonicalRepoPath(repo.BareRepoPath)
	for _, ownerRepo := range entry.Manifest.Repos {
		if ownerRepo.Mode != ModeEdit || ownerRepo.Branch != baseBranch {
			continue
		}
		if canonicalRepoPath(ownerRepo.BareRepoPath) != canonicalBare {
			continue
		}
		target = ownerRepo.Base
		break
	}
	basePRs := d.prsFor(ctx, cloneURL, baseBranch)
	if anyMergedPR(basePRs) {
		rs.BaseHealth, rs.MergedVia = BaseHealthMerged, MergedViaPR
		return
	}
	// Only a live (OPEN) PR overrides the recorded target: --state all also
	// returns CLOSED/abandoned PRs whose stale baseRefName must not retarget
	// the ancestry check.
	for _, pr := range basePRs {
		if strings.EqualFold(pr.State, "OPEN") && pr.BaseRefName != "" {
			target = "origin/" + pr.BaseRefName
			break
		}
	}
	if strings.TrimSpace(target) == "" {
		rs.Note = fmt.Sprintf("base owner %q records no base for repo %s; merge target unknown", owner, repo.Name)
		return
	}
	merged, err := d.mergedByAncestry(ctx, repo.BareRepoPath, canonical, canonicalBaseRef(target))
	switch {
	case err != nil:
		rs.Note = fmt.Sprintf("ancestry check against %s failed: %v", canonicalBaseRef(target), err)
	case merged:
		rs.BaseHealth, rs.MergedVia = BaseHealthMerged, MergedViaAncestry
	default:
		rs.BaseHealth = BaseHealthOK
	}
}

// appendRepoNote joins advisory texts into the repo row's single Note field.
func appendRepoNote(rs *SagaRepoStatus, text string) {
	if rs.Note == "" {
		rs.Note = text
		return
	}
	rs.Note += "; " + text
}

// SagaStatus resolves the full status for sagaID: per member in topo order,
// lifecycle state, per-edit-repo drift and base health (merge awareness),
// live PR state when PRLookup is wired, and advisory notes. Read-only by
// design: it performs ZERO manifest writes. Unlocked snapshot reader, like
// SagaList.
func (s Service) SagaStatus(ctx context.Context, sagaID string) (SagaStatus, error) {
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return SagaStatus{}, err
	}
	manifest, err := LoadManifest(sagaPath)
	if err != nil {
		return SagaStatus{}, err
	}
	if manifest.Saga == nil {
		return SagaStatus{}, fmt.Errorf("space %q is not a saga", sagaID)
	}
	// Siblings feed base-owner resolution (verdicts + topology notes); a scan
	// failure degrades stacked detection to unknown rather than failing status.
	siblings, err := s.ListSpaces()
	detector := s.newMergeDetector(siblings)
	if err != nil {
		detector.siblings, detector.siblingsOK = nil, false
	}
	roster := map[string]bool{}
	for _, member := range manifest.Saga.Members {
		roster[member.ID] = true
	}
	status := SagaStatus{SagaID: sagaID}
	for _, member := range sagaTopoOrder(manifest.Saga.Members) {
		row := SagaMemberStatus{ID: member.ID, After: append([]string(nil), member.After...)}
		row.State, row.Error = s.resolveMemberState(member)
		if row.State != MemberLive {
			status.Members = append(status.Members, row)
			continue
		}
		memberPath := s.SpacePath(member.ID)
		memberManifest, err := LoadManifest(memberPath)
		if err != nil {
			// Raced away between the state probe and this load.
			if errors.Is(err, os.ErrNotExist) {
				row.State, row.Error = MemberMissing, ""
			} else {
				row.State, row.Error = MemberCorrupt, err.Error()
			}
			status.Members = append(status.Members, row)
			continue
		}
		preds := sagaPredecessors(manifest.Saga.Members, member.ID)
		for _, repo := range memberManifest.Repos {
			if repo.Mode != ModeEdit {
				continue
			}
			rs, prRows, notes := detector.detect(ctx, member.ID, repo)
			row.PRs = append(row.PRs, prRows...)
			status.Notes = append(status.Notes, notes...)
			worktreePath := filepath.Join(memberPath, repo.Path)
			if _, err := os.Stat(worktreePath); err != nil {
				appendRepoNote(&rs, "worktree missing")
			} else {
				if dirty, _, err := s.Git.IsDirty(ctx, worktreePath); err != nil {
					status.Notes = append(status.Notes, SagaNote{Kind: NoteKindDegraded, Member: member.ID, Text: fmt.Sprintf("repo %s: dirty check failed: %v", repo.Name, err)})
				} else if dirty {
					row.Dirty = true
				}
				if ahead, behind, err := s.Git.AheadBehind(ctx, worktreePath, repo.Base); err != nil {
					appendRepoNote(&rs, fmt.Sprintf("drift unknown: %v", err))
				} else {
					rs.Ahead, rs.Behind = ahead, behind
				}
			}
			if repo.Base != "" && len(siblings) > 0 {
				if owner, ok := resolveBaseOwner(repo.Base, repo.BareRepoPath, siblings); ok && owner != member.ID {
					switch {
					case !roster[owner]:
						appendRepoNote(&rs, fmt.Sprintf("stacks on base %s owned by space %q outside the saga", repo.Base, owner))
					case !preds[owner]:
						appendRepoNote(&rs, fmt.Sprintf("stacks on base %s owned by %q, which is not among its after-predecessors", repo.Base, owner))
					}
				}
			}
			row.Repos = append(row.Repos, rs)
		}
		status.Members = append(status.Members, row)
	}
	if note, ok := detector.degradeNote(); ok {
		status.Notes = append(status.Notes, note)
	}
	return status, nil
}

// SagaSyncOptions drives SagaSync. P2B synced live members only; the struct
// exists so later flags land additively without a signature change.
type SagaSyncOptions struct {
	// DryRun suppresses every write sync performs — the PR-identity cache in
	// the saga roster plus the per-space generated files (CLAUDE.md link,
	// AGENTS.md) via SyncOptions.DryRun; git-level dry-run printing is the
	// injected client's job, matching every other verb.
	DryRun bool
}

// SagaSync fetches and syncs a saga's live members plus the saga space
// itself. Member states resolve FIRST: archived and missing members skip with
// a printed note, a corrupt member fails the whole sync closed. Each unique
// bare repo across the saga and its live members fetches exactly once here;
// the per-space Syncs then run with SkipFetch so nothing fetches twice. The
// saga's own references-only self-refresh also regenerates AGENTS.md and the
// CLAUDE.md link. After the sync, the freshly fetched refs feed the same
// merge detection status uses, printed as human summaries. The ONLY thing
// sync persists from the detection is PR identity (repo + number) into the
// saga roster — status stays a pure reader — and DryRun suppresses even that.
func (s Service) SagaSync(ctx context.Context, sagaID string, opts SagaSyncOptions) error {
	sagaPath, err := s.resolveSpacePath(sagaID)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(sagaPath)
	if err != nil {
		return err
	}
	if manifest.Saga == nil {
		return fmt.Errorf("space %q is not a saga", sagaID)
	}
	type liveMember struct {
		id       string
		manifest Manifest
	}
	var live []liveMember
	for _, member := range sagaTopoOrder(manifest.Saga.Members) {
		state, detail := s.resolveMemberState(member)
		switch state {
		case MemberCorrupt:
			return fmt.Errorf("member %s has a corrupt manifest: %s", member.ID, detail)
		case MemberArchived:
			s.printf("skipping member %s: archived at %s\n", member.ID, detail)
			continue
		case MemberMissing:
			s.printf("skipping member %s: missing\n", member.ID)
			continue
		}
		memberManifest, err := LoadManifest(s.SpacePath(member.ID))
		if err != nil {
			return fmt.Errorf("member %s: %w", member.ID, err)
		}
		live = append(live, liveMember{id: member.ID, manifest: memberManifest})
	}
	// One fetch per unique bare repo across saga + live members.
	fetched := map[string]bool{}
	fetch := func(repos []RepoManifest) error {
		for _, repo := range repos {
			key := canonicalRepoPath(repo.BareRepoPath)
			if fetched[key] {
				continue
			}
			fetched[key] = true
			if err := s.Git.FetchAllPrune(ctx, repo.BareRepoPath); err != nil {
				return err
			}
		}
		return nil
	}
	if err := fetch(manifest.Repos); err != nil {
		return err
	}
	for _, member := range live {
		if err := fetch(member.manifest.Repos); err != nil {
			return err
		}
	}
	for _, member := range live {
		s.printf("syncing member %s\n", member.id)
		if err := s.Sync(ctx, SyncOptions{SpaceID: member.id, SkipFetch: true, DryRun: opts.DryRun}); err != nil {
			return err
		}
	}
	if err := s.Sync(ctx, SyncOptions{SpaceID: sagaID, ReferencesOnly: true, SkipFetch: true, DryRun: opts.DryRun}); err != nil {
		return err
	}
	// Merge detection over the freshly fetched refs: same rules as status,
	// human summaries only, nothing persisted. A sibling-scan failure degrades
	// loudly instead of failing the sync that already succeeded.
	siblings, err := s.ListSpaces()
	if err != nil {
		s.printf("notice: merge detection skipped: %v\n", err)
		return nil
	}
	detector := s.newMergeDetector(siblings)
	discovered := map[string][]SagaPR{}
	for _, member := range live {
		for _, repo := range member.manifest.Repos {
			if repo.Mode != ModeEdit {
				continue
			}
			rs, prRows, notes := detector.detect(ctx, member.id, repo)
			for _, pr := range prRows {
				discovered[member.id] = append(discovered[member.id], SagaPR{Repo: pr.Repo, Number: pr.Number})
			}
			switch rs.BaseHealth {
			case BaseHealthMerged:
				s.printf("member %s repo %s: base %s merged (%s); consider retargeting dependents or archiving\n", member.id, repo.Name, repo.Base, rs.MergedVia)
			case BaseHealthMissing:
				s.printf("member %s repo %s: base %s is missing from the bare repo\n", member.id, repo.Name, repo.Base)
			case BaseHealthOwnerArchived:
				s.printf("member %s repo %s: base %s owner is archived or destroyed\n", member.id, repo.Name, repo.Base)
			case BaseHealthUnknown:
				s.printf("member %s repo %s: base health unknown (%s)\n", member.id, repo.Name, rs.Note)
			}
			for _, note := range notes {
				s.printf("note [%s] %s: %s\n", note.Kind, note.Member, note.Text)
			}
		}
	}
	if note, ok := detector.degradeNote(); ok {
		s.printf("note: %s\n", note.Text)
	}
	if !opts.DryRun {
		s.cacheSagaPRIdentities(sagaID, discovered)
	}
	return nil
}

// errSagaPRCacheNoop aborts the locked cache mutation before its save when
// every discovered PR identity is already recorded, so a converged sync
// writes nothing.
var errSagaPRCacheNoop = errors.New("saga PR cache: nothing new")

// cacheSagaPRIdentities persists newly discovered PR rows into the roster's
// member entries under the per-saga lock. IDENTITY ONLY — state, mergedAt and
// baseRefName are always re-read live from the forge, never persisted — and
// already-cached identities are not duplicated. The landed detector lists PRs
// by head branch, so nothing consumes the cache yet: identities are recorded
// for future by-number (gh pr view) lookups. Soft by design: a cache failure
// prints a notice, because the sync itself already succeeded.
func (s Service) cacheSagaPRIdentities(sagaID string, discovered map[string][]SagaPR) {
	if len(discovered) == 0 {
		return
	}
	err := s.mutateSagaManifest(sagaID, func(manifest *Manifest) error {
		changed := false
		for i := range manifest.Saga.Members {
			member := &manifest.Saga.Members[i]
			for _, row := range discovered[member.ID] {
				cached := false
				for _, have := range member.PRs {
					if have == row {
						cached = true
						break
					}
				}
				if !cached {
					member.PRs = append(member.PRs, row)
					changed = true
				}
			}
		}
		if !changed {
			return errSagaPRCacheNoop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSagaPRCacheNoop) {
		s.printf("notice: could not cache PR identities for saga %s: %v\n", sagaID, err)
	}
}

// sagaTopoOrder returns the members in a stable topological order of the
// after-graph (roster order breaks ties). Validation keeps saved manifests
// acyclic; were a cycle present anyway, its members append in roster order so
// rendering never drops anyone.
func sagaTopoOrder(members []SagaMember) []SagaMember {
	pending := make(map[string]int, len(members))
	unblocks := make(map[string][]string, len(members))
	for _, member := range members {
		pending[member.ID] = len(member.After)
		for _, after := range member.After {
			unblocks[after] = append(unblocks[after], member.ID)
		}
	}
	ordered := make([]SagaMember, 0, len(members))
	emitted := make(map[string]bool, len(members))
	for len(ordered) < len(members) {
		progressed := false
		for _, member := range members {
			if emitted[member.ID] || pending[member.ID] != 0 {
				continue
			}
			emitted[member.ID] = true
			ordered = append(ordered, member)
			for _, blocked := range unblocks[member.ID] {
				pending[blocked]--
			}
			progressed = true
		}
		if !progressed {
			for _, member := range members {
				if !emitted[member.ID] {
					ordered = append(ordered, member)
				}
			}
			break
		}
	}
	return ordered
}
