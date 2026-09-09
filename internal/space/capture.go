package space

import (
	"fmt"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/tether"
)

// captureCoOccurrence records the passive co-occurrence tethers implied by a
// realized space: every editable repo tethers to every OTHER repo present
// (edits and references alike). It is best-effort — mirroring linkMemoryOnAdd,
// it never returns an error; a failure prints a notice and the space stands.
//
// The associate set A is edits ∪ references (edit precedence when a name is in
// both). For each edit e and each associate a≠e, one Bump of e→a is recorded in
// a SINGLE Update transaction, with a's mode carried as the ToMode (edit when a
// came from the edit set, reference otherwise). After bumping, each touched
// tether's effective Strength is stamped so the persisted file carries a
// meaningful classification (D6: the threshold lives here, not in tether.Save).
func (s Service) captureCoOccurrence(edits, references []RepoSpec) {
	if len(edits) == 0 || !s.Config.Tethers.IsEnabled() {
		return
	}
	// Associate set A = edits ∪ references. Edit precedence: a name present in
	// both sets is an edit associate (so its ToMode wins as edit).
	type assoc struct {
		name string
		mode tether.Mode
	}
	seen := map[string]bool{}
	var associates []assoc
	for _, e := range edits {
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		associates = append(associates, assoc{name: e.Name, mode: tether.ModeEdit})
	}
	for _, r := range references {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		associates = append(associates, assoc{name: r.Name, mode: tether.ModeReference})
	}
	// No pair is possible with a single distinct repo (a solo edit): skip
	// entirely rather than open/rewrite an empty tether file.
	if len(associates) < 2 {
		return
	}
	now := s.now()
	threshold := s.Config.Tethers.StrongThreshold
	if err := tether.Update(tether.Path(s.Config), func(f *tether.File) error {
		for _, e := range edits {
			for _, a := range associates {
				if a.name == e.Name {
					continue
				}
				tether.Bump(f, e.Name, a.name, a.mode, now)
			}
		}
		stampStrength(f, threshold)
		return nil
	}); err != nil {
		s.printf("notice: could not record repo tethers: %v\n", err)
	}
}

// captureDeltaOnAdd records only the tethers implied by adding ONE repo to a
// space that already holds prior. It never re-records old-to-old pairs (those
// were captured at create time), so a stave space add reinforces exactly the
// new edges (D13). Best-effort, like captureCoOccurrence.
//
//   - Added as a reference: each prior editable p gains p→added(reference).
//   - Added as an editable: added→a for every prior associate a (edits and
//     references), plus p→added(edit) for every prior editable p.
func (s Service) captureDeltaOnAdd(prior []RepoManifest, addedName string, addedMode tether.Mode) {
	if !s.Config.Tethers.IsEnabled() {
		return
	}
	// Normalize the prior associates to a UNIQUE set keyed by repo Name with
	// edit>reference precedence: Stave allows the same repo as both an edit and
	// a reference worktree, so a name that appears twice (once each mode) must be
	// counted exactly once — as an edit — or the added repo's delta double-counts
	// that pair (an A present as edit AND reference would otherwise be bumped
	// twice when adding editable C).
	priorMode := map[string]tether.Mode{}
	var priorOrder []string
	for _, p := range prior {
		mode := tether.ModeReference
		if p.Mode == ModeEdit {
			mode = tether.ModeEdit
		}
		if existing, seen := priorMode[p.Name]; seen {
			// Edit precedence: promote to edit if either occurrence is an edit.
			if existing != tether.ModeEdit && mode == tether.ModeEdit {
				priorMode[p.Name] = tether.ModeEdit
			}
			continue
		}
		priorMode[p.Name] = mode
		priorOrder = append(priorOrder, p.Name)
	}

	// Compute the (from,to,toMode) pairs FIRST so a delta that produces no pair
	// (a reference added to a space with no editable, the first repo added, or a
	// saga reference-add re-entering under withSagaLock) never opens the tether
	// lock or rewrites an empty tether file.
	type pair struct {
		from, to string
		toMode   tether.Mode
	}
	var pairs []pair
	if addedMode == tether.ModeReference {
		for _, name := range priorOrder {
			if name == addedName {
				continue // self-pair (same repo already present); nothing to record
			}
			if priorMode[name] == tether.ModeEdit {
				pairs = append(pairs, pair{from: name, to: addedName, toMode: tether.ModeReference})
			}
		}
	} else {
		for _, name := range priorOrder {
			if name == addedName {
				continue // self-pair (same repo already present); nothing to record
			}
			pairs = append(pairs, pair{from: addedName, to: name, toMode: priorMode[name]})
			if priorMode[name] == tether.ModeEdit {
				pairs = append(pairs, pair{from: name, to: addedName, toMode: tether.ModeEdit})
			}
		}
	}
	if len(pairs) == 0 {
		return
	}

	now := s.now()
	threshold := s.Config.Tethers.StrongThreshold
	if err := tether.Update(tether.Path(s.Config), func(f *tether.File) error {
		for _, p := range pairs {
			tether.Bump(f, p.from, p.to, p.toMode, now)
		}
		stampStrength(f, threshold)
		return nil
	}); err != nil {
		s.printf("notice: could not record repo tethers: %v\n", err)
	}
}

// stampStrength derives and writes each tether's effective Strength so the
// persisted file carries a meaningful classification (tether.Save is
// deliberately threshold-blind — see D6).
func stampStrength(f *tether.File, threshold int) {
	for i := range f.Tethers {
		f.Tethers[i].Strength = tether.EffectiveStrength(f.Tethers[i], threshold)
	}
}

// ExpandCommonRefs expands the co-occurrence tethers of the given edits into
// extra reference specs for -c/--common. It lives on the service (not in the
// tether package) so both the CLI and the agent layer expand identically (D1).
//
// Tethers must be enabled: -c/--common is meaningless with learning off, so a
// disabled config is a hard error rather than a silent empty expansion. Each
// edit's strong (or, with includeWeak, all) tethers contribute their To repo,
// de-duplicated against the explicit -r references and the -e edits, and any To
// not registered in config.Repos is skipped with an accumulated note.
func (s Service) ExpandCommonRefs(cfg config.Config, edits, explicitRefs []RepoSpec, includeWeak bool) (extra []RepoSpec, notes []string, err error) {
	if !cfg.Tethers.IsEnabled() {
		return nil, nil, coded(CodeInvalidArguments, nil, "repo tethers are disabled; enable tethers.enabled in config to use -c/--common")
	}
	f, err := tether.Load(tether.Path(cfg))
	if err != nil {
		return nil, nil, err
	}
	skip := map[string]bool{}
	for _, e := range edits {
		skip[e.Name] = true
	}
	for _, r := range explicitRefs {
		skip[r.Name] = true
	}
	seen := map[string]bool{}
	for _, e := range edits {
		for _, t := range tether.Common(f, e.Name, includeWeak, cfg.Tethers.StrongThreshold) {
			to := t.To
			if skip[to] || seen[to] {
				continue
			}
			seen[to] = true
			if _, ok := cfg.Repos[to]; !ok {
				notes = append(notes, fmt.Sprintf("skipping tethered repo %q: not registered in config", to))
				continue
			}
			extra = append(extra, RepoSpec{Name: to})
		}
	}
	return extra, notes, nil
}
