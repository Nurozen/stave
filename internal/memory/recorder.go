package memory

import (
	"context"
	"sync"
)

// Recording captures the structured result of every provider call made
// through a wrapped Provider. The space service prints those results as prose
// and returns only an error; the CLI's --json renderers wrap the provider in
// a Recording so the same service path yields typed data.
type Recording struct {
	mu sync.Mutex

	Attaches []AttachResult
	Statuses []RecordedStatus
	Syncs    []SyncResult
	Proposes []ProposeResult
	Detaches []RecordedDetach
}

// RecordedStatus is one Status call: the store asked about, the provider's
// answer, and its error (a failed status still yields a row).
type RecordedStatus struct {
	StoreID string
	Result  StatusResult
	Err     error
}

// RecordedDetach is one successful Detach call with the options it ran under
// (fate, owned) so callers can report the fate that actually applied.
type RecordedDetach struct {
	Options DetachOptions
	Result  DetachResult
}

// Wrap returns p with every call recorded into r. The wrapper preserves the
// optional ReferenceLinker / MCPWirer extensions exactly when p implements
// them, so type-asserting callers behave as they would on the bare provider.
func (r *Recording) Wrap(p Provider) Provider {
	base := &recorded{Provider: p, rec: r}
	linker, hasLinker := p.(ReferenceLinker)
	wirer, hasWirer := p.(MCPWirer)
	switch {
	case hasLinker && hasWirer:
		return &recordedLinkerWirer{recorded: base, linker: linker, wirer: wirer}
	case hasLinker:
		return &recordedLinker{recorded: base, linker: linker}
	case hasWirer:
		return &recordedWirer{recorded: base, wirer: wirer}
	}
	return base
}

// StatusFor returns the last recorded Status call for storeID.
func (r *Recording) StatusFor(storeID string) (RecordedStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.Statuses) - 1; i >= 0; i-- {
		if r.Statuses[i].StoreID == storeID {
			return r.Statuses[i], true
		}
	}
	return RecordedStatus{}, false
}

type recorded struct {
	Provider
	rec *Recording
}

func (p *recorded) Attach(ctx context.Context, opts AttachOptions) (AttachResult, error) {
	res, err := p.Provider.Attach(ctx, opts)
	if err == nil {
		p.rec.mu.Lock()
		p.rec.Attaches = append(p.rec.Attaches, res)
		p.rec.mu.Unlock()
	}
	return res, err
}

func (p *recorded) Status(ctx context.Context, opts StatusOptions) (StatusResult, error) {
	res, err := p.Provider.Status(ctx, opts)
	p.rec.mu.Lock()
	p.rec.Statuses = append(p.rec.Statuses, RecordedStatus{StoreID: opts.StoreID, Result: res, Err: err})
	p.rec.mu.Unlock()
	return res, err
}

func (p *recorded) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	res, err := p.Provider.Sync(ctx, opts)
	if err == nil {
		p.rec.mu.Lock()
		p.rec.Syncs = append(p.rec.Syncs, res)
		p.rec.mu.Unlock()
	}
	return res, err
}

func (p *recorded) Propose(ctx context.Context, opts ProposeOptions) (ProposeResult, error) {
	res, err := p.Provider.Propose(ctx, opts)
	if err == nil {
		p.rec.mu.Lock()
		p.rec.Proposes = append(p.rec.Proposes, res)
		p.rec.mu.Unlock()
	}
	return res, err
}

func (p *recorded) Detach(ctx context.Context, opts DetachOptions) (DetachResult, error) {
	res, err := p.Provider.Detach(ctx, opts)
	if err == nil {
		p.rec.mu.Lock()
		p.rec.Detaches = append(p.rec.Detaches, RecordedDetach{Options: opts, Result: res})
		p.rec.mu.Unlock()
	}
	return res, err
}

type recordedLinker struct {
	*recorded
	linker ReferenceLinker
}

func (p *recordedLinker) LinkReference(ctx context.Context, opts LinkReferenceOptions) (LinkReferenceResult, error) {
	return p.linker.LinkReference(ctx, opts)
}

type recordedWirer struct {
	*recorded
	wirer MCPWirer
}

func (p *recordedWirer) WriteMCPConfig(ctx context.Context, spacePath, storeID string) error {
	return p.wirer.WriteMCPConfig(ctx, spacePath, storeID)
}

func (p *recordedWirer) RemoveMCPConfig(ctx context.Context, spacePath string) error {
	return p.wirer.RemoveMCPConfig(ctx, spacePath)
}

type recordedLinkerWirer struct {
	*recorded
	linker ReferenceLinker
	wirer  MCPWirer
}

func (p *recordedLinkerWirer) LinkReference(ctx context.Context, opts LinkReferenceOptions) (LinkReferenceResult, error) {
	return p.linker.LinkReference(ctx, opts)
}

func (p *recordedLinkerWirer) WriteMCPConfig(ctx context.Context, spacePath, storeID string) error {
	return p.wirer.WriteMCPConfig(ctx, spacePath, storeID)
}

func (p *recordedLinkerWirer) RemoveMCPConfig(ctx context.Context, spacePath string) error {
	return p.wirer.RemoveMCPConfig(ctx, spacePath)
}
