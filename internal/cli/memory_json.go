package cli

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/memory"
	"github.com/Nurozen/stave/internal/space"
	"github.com/spf13/cobra"
)

// memoryAttachmentJSON is one .stave.yaml memories: record as the memory
// verbs report it.
type memoryAttachmentJSON struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Owned    bool   `json:"owned"`
}

func memoryAttachmentRow(mem space.MemoryManifest) memoryAttachmentJSON {
	return memoryAttachmentJSON{Name: mem.Name, Provider: mem.Provider, ID: mem.ID, Owned: mem.Owned}
}

// memoryAttachJSON is the success shape of memory attach.
type memoryAttachJSON struct {
	SpaceID     string               `json:"spaceId"`
	SpacePath   string               `json:"spacePath"`
	Manifest    space.Manifest       `json:"manifest"`
	Attachments []memoryAttachedJSON `json:"attachments"`
	Notes       []string             `json:"notes,omitempty"`
}

type memoryAttachedJSON struct {
	memoryAttachmentJSON
	Linked []memoryLinkedJSON `json:"linked,omitempty"`
}

// memoryLinkedJSON is one reference link the provider resolved on attach.
type memoryLinkedJSON struct {
	Reference   string `json:"reference"`
	Target      string `json:"target,omitempty"`
	Kind        string `json:"kind"`
	ResolvedVia string `json:"resolvedVia,omitempty"`
}

func memoryLinkedRows(links []memory.AttachLink) []memoryLinkedJSON {
	rows := make([]memoryLinkedJSON, 0, len(links))
	for _, link := range links {
		row := memoryLinkedJSON{
			Reference:   firstNonEmpty(link.Reference, link.Ref),
			Kind:        firstNonEmpty(link.Mode, "unresolved"),
			ResolvedVia: link.ResolvedVia,
		}
		if link.Mode != "" {
			row.Target = link.Ref
		}
		rows = append(rows, row)
	}
	return rows
}

// memoryAttachPayload diffs the manifest against the pre-attach records: the
// attachments that appeared are the verb's result, paired (by store id) with
// the provider's recorded Attach result for the resolved links.
func memoryAttachPayload(svc space.Service, spaceID string, before []space.MemoryManifest, rec *memory.Recording, sink *outputSink) (memoryAttachJSON, error) {
	spacePath := svc.SpacePath(spaceID)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return memoryAttachJSON{}, err
	}
	known := make(map[string]bool, len(before))
	for _, mem := range before {
		known[mem.Name] = true
	}
	payload := memoryAttachJSON{SpaceID: spaceID, SpacePath: spacePath, Manifest: manifest, Attachments: []memoryAttachedJSON{}}
	var primary []string
	for _, mem := range manifest.Memories {
		if known[mem.Name] {
			continue
		}
		row := memoryAttachedJSON{memoryAttachmentJSON: memoryAttachmentRow(mem)}
		for _, res := range rec.Attaches {
			if res.StoreID == mem.ID {
				row.Linked = memoryLinkedRows(res.Links)
				break
			}
		}
		payload.Attachments = append(payload.Attachments, row)
		primary = append(primary, fmt.Sprintf("attached memory %s (%s:%s, owned=%v) to %s", mem.Name, mem.Provider, mem.ID, mem.Owned, spaceID))
	}
	payload.Notes = sink.Notes(primary...)
	return payload, nil
}

// memoryDetachJSON is the success shape of memory detach.
type memoryDetachJSON struct {
	SpaceID   string               `json:"spaceId"`
	SpacePath string               `json:"spacePath"`
	Manifest  space.Manifest       `json:"manifest"`
	Detached  []memoryDetachedJSON `json:"detached"`
	Notes     []string             `json:"notes,omitempty"`
}

type memoryDetachedJSON struct {
	memoryAttachmentJSON
	Fate string `json:"fate"`
}

// memoryDetachPayload reports the attachment the verb removed (resolved from
// the pre-detach manifest) with the fate that actually applied: an unowned
// store is never destroyed, so a requested destroy downgrades to keep exactly
// as the human notice says.
func memoryDetachPayload(svc space.Service, spaceID string, target space.MemoryManifest, requested memory.MemoryFate, sink *outputSink) (memoryDetachJSON, error) {
	spacePath := svc.SpacePath(spaceID)
	manifest, err := space.LoadManifest(spacePath)
	if err != nil {
		return memoryDetachJSON{}, err
	}
	fate := requested
	if fate == "" || !target.Owned {
		fate = memory.FateKeep
	}
	return memoryDetachJSON{
		SpaceID:   spaceID,
		SpacePath: spacePath,
		Manifest:  manifest,
		Detached:  []memoryDetachedJSON{{memoryAttachmentJSON: memoryAttachmentRow(target), Fate: string(fate)}},
		Notes:     sink.Notes(fmt.Sprintf("detached memory %s from %s", target.Name, spaceID)),
	}, nil
}

// memoryListJSON is one row of memory list: a space and its attachments.
type memoryListJSON struct {
	SpaceID     string                 `json:"spaceId"`
	SpacePath   string                 `json:"spacePath"`
	Attachments []memoryAttachmentJSON `json:"attachments"`
}

func memoryListRow(spaceID, spacePath string, mems []space.MemoryManifest) memoryListJSON {
	row := memoryListJSON{SpaceID: spaceID, SpacePath: spacePath, Attachments: make([]memoryAttachmentJSON, 0, len(mems))}
	for _, mem := range mems {
		row.Attachments = append(row.Attachments, memoryAttachmentRow(mem))
	}
	return row
}

// memoryListPayload lists one space (a row even when it has no attachments)
// or, with an empty id, every live space that has at least one attachment —
// the same rows the human listing prints.
func memoryListPayload(svc space.Service, spaceID string) ([]memoryListJSON, error) {
	if spaceID != "" {
		if err := config.ValidateSpaceID(spaceID); err != nil {
			return nil, err
		}
		spacePath := svc.SpacePath(spaceID)
		manifest, err := space.LoadManifest(spacePath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, &space.SpaceNotFoundError{SpaceID: spaceID, Path: spacePath}
			}
			return nil, err
		}
		return []memoryListJSON{memoryListRow(spaceID, spacePath, manifest.Memories)}, nil
	}
	rows := []memoryListJSON{}
	spaces, err := svc.ListSpaces()
	if err != nil {
		if os.IsNotExist(err) {
			return rows, nil
		}
		return nil, err
	}
	for _, entry := range spaces {
		if entry.Err != nil || entry.Manifest == nil || len(entry.Manifest.Memories) == 0 {
			continue
		}
		rows = append(rows, memoryListRow(entry.Manifest.ID, entry.Path, entry.Manifest.Memories))
	}
	return rows, nil
}

// memoryStatusJSON is the success shape of memory status.
type memoryStatusJSON struct {
	SpaceID     string                       `json:"spaceId"`
	Attachments []memoryAttachmentStatusJSON `json:"attachments"`
}

type memoryAttachmentStatusJSON struct {
	memoryAttachmentJSON
	// State is the compact freshness the human header row shows in
	// parentheses ("2 unpushed", "stale", "unreachable"); absent when every
	// link is ok or the provider reported no links.
	State    string                 `json:"state,omitempty"`
	Lifetime string                 `json:"lifetime,omitempty"`
	Links    []memoryLinkStatusJSON `json:"links"`
	Error    string                 `json:"error,omitempty"`
}

type memoryLinkStatusJSON struct {
	Alias     string `json:"alias"`
	Kind      string `json:"kind"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
	Pending   int    `json:"pending,omitempty"`
	Stale     *bool  `json:"stale,omitempty"`
	Reachable *bool  `json:"reachable,omitempty"`
	State     string `json:"state,omitempty"`
}

func memoryLinkStatusRows(links []memory.LinkStatus) []memoryLinkStatusJSON {
	rows := make([]memoryLinkStatusJSON, 0, len(links))
	for _, link := range links {
		row := memoryLinkStatusJSON{
			Alias:   link.Ref,
			Kind:    firstNonEmpty(link.Mode, "unresolved"),
			Ahead:   link.Ahead,
			Behind:  link.Behind,
			Pending: link.PendingEdits,
			State:   link.State,
		}
		if link.State != "" {
			stale := link.State == "stale"
			reachable := link.State != "unreachable"
			row.Stale = &stale
			row.Reachable = &reachable
		}
		rows = append(rows, row)
	}
	return rows
}

// memoryStatusPayload pairs each attachment the human walk reported with the
// provider Status the recording captured for it. An attachment whose
// provider could not even be built has no recorded call; its lookup error is
// re-derived so the row still explains itself.
func memoryStatusPayload(spaceID string, targets []space.MemoryManifest, rec *memory.Recording, lookup func(provider string) error) memoryStatusJSON {
	payload := memoryStatusJSON{SpaceID: spaceID, Attachments: make([]memoryAttachmentStatusJSON, 0, len(targets))}
	for _, mem := range targets {
		row := memoryAttachmentStatusJSON{memoryAttachmentJSON: memoryAttachmentRow(mem), Links: []memoryLinkStatusJSON{}}
		recorded, ok := rec.StatusFor(mem.ID)
		switch {
		case !ok:
			if err := lookup(mem.Provider); err != nil {
				row.Error = err.Error()
			}
		case recorded.Err != nil:
			row.Error = recorded.Err.Error()
		default:
			row.State = trimMemoryStateSuffix(recorded.Result.StateSuffix())
			row.Lifetime = recorded.Result.Lifetime
			row.Links = memoryLinkStatusRows(recorded.Result.Links)
		}
		payload.Attachments = append(payload.Attachments, row)
	}
	return payload
}

// memoryStatusTargets mirrors MemoryStatus' selection: every attachment, or
// the one the alias names.
func memoryStatusTargets(manifest space.Manifest, alias string) []space.MemoryManifest {
	if alias == "" {
		return manifest.Memories
	}
	if mem, _, ok := manifest.FindMemory(alias); ok {
		return []space.MemoryManifest{mem}
	}
	return nil
}

// memoryFlowJSON is the success shape of memory sync and memory propose.
type memoryFlowJSON struct {
	SpaceID string                 `json:"spaceId"`
	Results []memoryFlowResultJSON `json:"results"`
	Notes   []string               `json:"notes,omitempty"`
}

type memoryFlowResultJSON struct {
	Alias   string `json:"alias"`
	Warren  string `json:"warren,omitempty"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	// Propose handoff (stave never auto-pushes): the branch/commit the
	// contribution landed on and the push command the operator must run.
	Branch      string                    `json:"branch,omitempty"`
	Commit      string                    `json:"commit,omitempty"`
	PushCommand string                    `json:"pushCommand,omitempty"`
	Contributed *memory.ContributedCounts `json:"contributed,omitempty"`
}

const (
	memoryOutcomeSynced   = "synced"
	memoryOutcomeUpToDate = "up-to-date"
	memoryOutcomeFailed   = "failed"
	memoryOutcomeProposed = "proposed"
)

// memorySyncPayload turns the recorded warren sync envelope into one result
// per warren (synced / up-to-date / failed); a provider that reports no
// warrens yields a single synced row carrying its summary.
func memorySyncPayload(spaceID, alias string, rec *memory.Recording, sink *outputSink) memoryFlowJSON {
	payload := memoryFlowJSON{SpaceID: spaceID, Results: []memoryFlowResultJSON{}}
	var primary []string
	for _, res := range rec.Syncs {
		if len(res.Warrens) == 0 {
			payload.Results = append(payload.Results, memoryFlowResultJSON{Alias: alias, Outcome: memoryOutcomeSynced, Detail: res.Summary})
		}
		for _, w := range res.Warrens {
			row := memoryFlowResultJSON{Alias: alias, Warren: w.ID}
			switch {
			case w.Error != "":
				row.Outcome = memoryOutcomeFailed
				row.Detail = w.Error
			case w.Updated:
				row.Outcome = memoryOutcomeSynced
				row.Detail = "pinned " + w.PinnedCommit
			default:
				row.Outcome = memoryOutcomeUpToDate
				row.Detail = "pinned " + w.PinnedCommit
			}
			payload.Results = append(payload.Results, row)
		}
		if res.Summary != "" {
			primary = append(primary, res.Summary)
		}
	}
	payload.Notes = sink.Notes(primary...)
	return payload
}

// memoryProposePayload reports the contribute + propose handoff: proposed
// (with the push command) or up-to-date when marmot had nothing new.
func memoryProposePayload(spaceID, alias string, rec *memory.Recording, sink *outputSink) memoryFlowJSON {
	payload := memoryFlowJSON{SpaceID: spaceID, Results: []memoryFlowResultJSON{}}
	var primary []string
	for _, res := range rec.Proposes {
		row := memoryFlowResultJSON{
			Alias:       alias,
			Outcome:     memoryOutcomeProposed,
			Detail:      res.Summary,
			Branch:      res.Branch,
			Commit:      res.Commit,
			PushCommand: res.PushCommand,
			Contributed: res.Contributed,
		}
		if res.NothingToPropose && res.PushCommand == "" {
			row.Outcome = memoryOutcomeUpToDate
			row.Detail = "nothing new to push"
		}
		payload.Results = append(payload.Results, row)
		if res.Summary != "" {
			primary = append(primary, res.Summary)
		}
	}
	payload.Notes = sink.Notes(primary...)
	return payload
}

// memoryProviderJSON is one row of memory providers --json.
type memoryProviderJSON struct {
	Name         string   `json:"name"`
	Binary       string   `json:"binary,omitempty"`
	Default      bool     `json:"default"`
	Available    bool     `json:"available"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities"`
	Error        string   `json:"error,omitempty"`
}

// memoryProvidersPayload probes every registered provider the way the human
// listing does and adds the optional binary / capability reports.
func memoryProvidersPayload(ctx context.Context, mc config.MemoryConfig) []memoryProviderJSON {
	names := memory.Names()
	sort.Strings(names)
	rows := make([]memoryProviderJSON, 0, len(names))
	for _, name := range names {
		row := memoryProviderJSON{Name: name, Default: name == mc.Provider, Capabilities: []string{}}
		prov, err := memory.Lookup(name, mc)
		if err != nil {
			row.Error = err.Error()
			rows = append(rows, row)
			continue
		}
		if reporter, ok := prov.(memory.BinaryReporter); ok {
			row.Binary = reporter.BinaryName()
		}
		probe, probeErr := prov.Probe(ctx)
		row.Available = probe.Available
		row.Version = probe.Version
		switch {
		case probeErr != nil:
			row.Error = probeErr.Error()
		case !probe.Capable:
			row.Error = probe.Message
		}
		if reporter, ok := prov.(memory.CapabilityReporter); ok && probeErr == nil {
			if caps := reporter.Capabilities(ctx); len(caps) > 0 {
				row.Capabilities = caps
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// memoryJSONService builds the verb's service with its output routed to the
// sink and every provider wrapped in rec, so the human code path runs
// unchanged while its structured results are captured for the payload.
func (a *app) memoryJSONService(cmd *cobra.Command, dryRun bool, sink *outputSink, rec *memory.Recording) (space.Service, error) {
	svc, err := a.serviceWithOutput(cmd, dryRun, sink.Writer())
	if err != nil {
		return space.Service{}, err
	}
	if rec != nil {
		mc := svc.Config.Memory
		mc.ApplyDefaults()
		svc.MemoryFactory = func(name string) (memory.Provider, error) {
			prov, err := memory.Lookup(name, mc)
			if err != nil {
				return nil, err
			}
			return rec.Wrap(prov), nil
		}
	}
	return svc, nil
}
