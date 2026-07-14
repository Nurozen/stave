package memory

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a test double for Provider. Recorded calls enable assertions
// without a marmot binary.
type Fake struct {
	Mu sync.Mutex

	ProbeFn   func(context.Context) (ProbeResult, error)
	AttachFn  func(context.Context, AttachOptions) (AttachResult, error)
	StatusFn  func(context.Context, StatusOptions) (StatusResult, error)
	SyncFn    func(context.Context, SyncOptions) (SyncResult, error)
	ProposeFn func(context.Context, ProposeOptions) (ProposeResult, error)
	DetachFn  func(context.Context, DetachOptions) (DetachResult, error)

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

func (f *Fake) Attach(ctx context.Context, opts AttachOptions) (AttachResult, error) {
	f.record("attach", opts.SpaceID, opts.UseID, fmt.Sprintf("dry=%v", opts.DryRun))
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
		line := FormatCommand("marmot", DenCreateArgs(id, opts.SpacePath, "task", opts.EditRefs, opts.LinkRefs, opts.ReferenceSpecs))
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
