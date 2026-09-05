package memory

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a test double for Provider. Recorded calls enable assertions
// without a marmot binary. Attach deliberately mirrors none of Marmot's den
// vault config write (watch_sources: false) — that is a provider-internal
// side effect under MARMOT_HOME the space layer never observes.
type Fake struct {
	Mu sync.Mutex

	ProbeFn         func(context.Context) (ProbeResult, error)
	AttachFn        func(context.Context, AttachOptions) (AttachResult, error)
	StatusFn        func(context.Context, StatusOptions) (StatusResult, error)
	SyncFn          func(context.Context, SyncOptions) (SyncResult, error)
	ProposeFn       func(context.Context, ProposeOptions) (ProposeResult, error)
	DetachFn        func(context.Context, DetachOptions) (DetachResult, error)
	LinkReferenceFn func(context.Context, LinkReferenceOptions) (LinkReferenceResult, error)

	CapabilitiesFn func(context.Context) []string

	WriteMCPConfigFn  func(ctx context.Context, spacePath, storeID string) error
	RemoveMCPConfigFn func(ctx context.Context, spacePath string) error

	Calls []string

	// DefaultAttachOwned controls the Owned field of the default Attach result.
	DefaultAttachOwned bool
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) record(parts ...string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprint(parts))
}

func (f *Fake) Probe(ctx context.Context) (ProbeResult, error) {
	f.record("probe")
	if f.ProbeFn != nil {
		return f.ProbeFn(ctx)
	}
	return ProbeResult{Available: true, Capable: true, Message: "fake ok"}, nil
}

// Capabilities makes Fake a CapabilityReporter (memory providers --json).
func (f *Fake) Capabilities(ctx context.Context) []string {
	f.record("capabilities")
	if f.CapabilitiesFn != nil {
		return f.CapabilitiesFn(ctx)
	}
	return []string{"dens", "refs", "links"}
}

func (f *Fake) Attach(ctx context.Context, opts AttachOptions) (AttachResult, error) {
	f.record("attach", opts.SpaceID, opts.UseID, "lifetime="+opts.Lifetime, fmt.Sprintf("dry=%v", opts.DryRun))
	if f.AttachFn != nil {
		return f.AttachFn(ctx, opts)
	}
	id := opts.UseID
	owned := false
	if id == "" {
		id = opts.StoreID
		if id == "" {
			id = opts.SpaceID
		}
		owned = true
	}
	name := opts.Name
	if name == "" {
		name = "default"
	}
	if opts.DryRun {
		line := FormatCommand("marmot", DenCreateArgs(id, opts.SpacePath, "task", opts.EditRefs, opts.LinkRefs, opts.ReferenceSpecs, opts.Opts, true))
		printf(opts.Out, "dry-run: %s\n", line)
		return AttachResult{
			Provider:       "fake",
			StoreID:        id,
			Name:           name,
			Owned:          owned,
			DryRunCommands: []string{line},
		}, nil
	}
	if opts.SpacePath != "" {
		_ = WriteSpaceMCPConfig(opts.SpacePath, "marmot", id)
	}
	return AttachResult{Provider: "fake", StoreID: id, Name: name, Owned: owned, MCPConfigWritten: opts.SpacePath != ""}, nil
}

func (f *Fake) Status(ctx context.Context, opts StatusOptions) (StatusResult, error) {
	f.record("status", opts.StoreID)
	if f.StatusFn != nil {
		return f.StatusFn(ctx, opts)
	}
	return StatusResult{StoreID: opts.StoreID, Summary: "fake status"}, nil
}

func (f *Fake) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	f.record("sync", opts.StoreID)
	if f.SyncFn != nil {
		return f.SyncFn(ctx, opts)
	}
	return SyncResult{Summary: "fake sync"}, nil
}

func (f *Fake) Propose(ctx context.Context, opts ProposeOptions) (ProposeResult, error) {
	f.record("propose", opts.StoreID)
	if f.ProposeFn != nil {
		return f.ProposeFn(ctx, opts)
	}
	return ProposeResult{Summary: "fake propose"}, nil
}

// LinkReference makes Fake a ReferenceLinker (S4 space add parity tests).
func (f *Fake) LinkReference(ctx context.Context, opts LinkReferenceOptions) (LinkReferenceResult, error) {
	f.record("link-reference", opts.StoreID, opts.Spec.Name, opts.Spec.URL, fmt.Sprintf("dry=%v", opts.DryRun))
	if f.LinkReferenceFn != nil {
		return f.LinkReferenceFn(ctx, opts)
	}
	target := "w/" + opts.Spec.Name
	if opts.Spec.MarmotVault != "" && opts.Spec.MarmotVault != "off" {
		target = opts.Spec.MarmotVault
	}
	if opts.DryRun {
		line := FormatCommand("marmot", DenLinkArgs(opts.StoreID, target, false))
		printf(opts.Out, "dry-run: %s\n", line)
		return LinkReferenceResult{DryRunCommands: []string{line}}, nil
	}
	printf(opts.Out, "reference %s → %s (warren-url)\n", firstNonEmpty(opts.Spec.Name, opts.Spec.URL), target)
	return LinkReferenceResult{Linked: true, Link: AttachLink{Ref: target, Mode: "link", ResolvedVia: "warren-url"}}, nil
}

// WriteMCPConfig makes Fake an MCPWirer (saga member wiring tests).
func (f *Fake) WriteMCPConfig(ctx context.Context, spacePath, storeID string) error {
	f.record("write-mcp-config", spacePath, storeID)
	if f.WriteMCPConfigFn != nil {
		return f.WriteMCPConfigFn(ctx, spacePath, storeID)
	}
	return WriteSpaceMCPConfig(spacePath, "marmot", storeID)
}

// RemoveMCPConfig makes Fake an MCPWirer (saga member wiring tests).
func (f *Fake) RemoveMCPConfig(ctx context.Context, spacePath string) error {
	f.record("remove-mcp-config", spacePath)
	if f.RemoveMCPConfigFn != nil {
		return f.RemoveMCPConfigFn(ctx, spacePath)
	}
	return RemoveSpaceMCPConfig(spacePath)
}

func (f *Fake) Detach(ctx context.Context, opts DetachOptions) (DetachResult, error) {
	f.record("detach", opts.StoreID, string(opts.Fate), fmt.Sprintf("owned=%v", opts.Owned))
	if f.DetachFn != nil {
		return f.DetachFn(ctx, opts)
	}
	kept := opts.Fate == FateKeep || opts.Fate == "" || !opts.Owned
	return DetachResult{
		Summary:   fmt.Sprintf("fake detach %s fate=%s", opts.StoreID, opts.Fate),
		Kept:      kept,
		Destroyed: !kept && (opts.Fate == FateDestroy || opts.Fate == FateContribute),
	}, nil
}
