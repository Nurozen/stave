// Package memory is the provider-agnostic seam for space-attached persistent
// memory stores (marmot dens first). Provider selection is config
// (memory.provider), never the command path.
package memory

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Provider is the exec-backed seam for a memory backend.
// Modeled after space.Service.Git so unit tests can fake it.
type Provider interface {
	// Name returns the registered provider id (e.g. "marmot").
	Name() string
	// Probe checks binary presence and capability. Returns a human summary.
	Probe(ctx context.Context) (ProbeResult, error)
	// Attach creates or binds a store to a space. When opts.DryRun is set the
	// provider MUST NOT invoke the binary; it prints the exact argv instead.
	Attach(ctx context.Context, opts AttachOptions) (AttachResult, error)
	// Status reports provider-side state for an attachment.
	Status(ctx context.Context, opts StatusOptions) (StatusResult, error)
	// Sync refreshes provider state (e.g. warren sync + skew re-report).
	Sync(ctx context.Context, opts SyncOptions) (SyncResult, error)
	// Propose flows task learnings back (den contribute + warren propose).
	Propose(ctx context.Context, opts ProposeOptions) (ProposeResult, error)
	// Detach unbinds a store; may destroy when Fate is destroy/contribute.
	Detach(ctx context.Context, opts DetachOptions) (DetachResult, error)
}

// MemoryFate controls what happens to an owned store on detach/destroy.
type MemoryFate string

const (
	FateKeep       MemoryFate = "keep"
	FateDestroy    MemoryFate = "destroy"
	FateContribute MemoryFate = "contribute"
)

// ParseMemoryFate accepts keep|destroy|contribute (default keep when empty).
func ParseMemoryFate(raw string) (MemoryFate, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", string(FateKeep):
		return FateKeep, nil
	case string(FateDestroy):
		return FateDestroy, nil
	case string(FateContribute):
		return FateContribute, nil
	default:
		return "", fmt.Errorf("memory fate %q must be keep, destroy, or contribute", raw)
	}
}

type ProbeResult struct {
	Available  bool
	Version    string
	Capable    bool
	Message    string
	ManualHint string // printed when ambient degrades
}

type AttachOptions struct {
	// SpaceID is the stave space id; default task store id when UseID is empty.
	SpaceID string
	// SpacePath is the absolute space root (project path for reverse route).
	SpacePath string
	// UseID attaches an EXISTING durable store instead of creating a task store.
	UseID string
	// StoreID overrides the created store id (default SpaceID).
	StoreID string
	// Name is the attachment alias written to the manifest.
	Name string
	// Lifetime is "task" (default) or "durable".
	Lifetime string
	// EditRefs / LinkRefs are raw provider refs passed through (S4 content).
	EditRefs []string
	LinkRefs []string
	// ReferenceSpecs are raw url/path/ref triples for read-only links (S4).
	ReferenceSpecs []ReferenceSpec
	// Opts are provider-specific k=v knobs.
	Opts map[string]string
	// DryRun prints exact ops without invoking the binary.
	DryRun bool
	// Out receives dry-run / progress lines.
	Out io.Writer
	// Strict: when true, probe/create failure is fatal (explicit attach/--memory).
	// When false, ambient degrade is allowed by the caller (provider still returns err).
	Strict bool
}

// ReferenceSpec is a raw reference-repo descriptor passed through the seam
// for provider-side resolution (S4). Stave does not map these itself.
type ReferenceSpec struct {
	Name        string // registered repo name
	URL         string // raw url/path from config
	Ref         string // optional ref
	MarmotVault string // optional override/suppression ("off" or vault id)
}

type AttachResult struct {
	Provider       string
	StoreID        string
	StorePath      string
	Owned          bool // true when we created the store
	Name           string
	Warnings       []string
	DryRunCommands []string
	// MCPConfigWritten is true when space-local MCP config was written.
	MCPConfigWritten bool
}

type StatusOptions struct {
	StoreID string
	Out     io.Writer
	JSON    bool
}

type StatusResult struct {
	StoreID  string
	RawJSON  string
	Summary  string
	Warnings []string
}

type SyncOptions struct {
	StoreID string
	Out     io.Writer
	DryRun  bool
}

type SyncResult struct {
	Summary        string
	DryRunCommands []string
	Warnings       []string
}

type ProposeOptions struct {
	StoreID string
	Out     io.Writer
	DryRun  bool
	// Force is reserved for contribute refusal overrides (not auto-push).
	Force bool
}

type ProposeResult struct {
	Summary        string
	DryRunCommands []string
	Warnings       []string
	RawJSON        string
}

type DetachOptions struct {
	StoreID   string
	SpacePath string
	// NewSpacePath, when set, asks the provider to rewrite the reverse route
	// (archive path move). Empty + Fate keep → route remove or leave as-is.
	NewSpacePath string
	// RemoveRoute drops the reverse route without destroying the store.
	RemoveRoute bool
	Fate        MemoryFate
	Force       bool
	Owned       bool // caller must pass attachment.Owned; false never destroys
	DryRun      bool
	Out         io.Writer
}

type DetachResult struct {
	Summary        string
	DryRunCommands []string
	Kept           bool
	Destroyed      bool
	Warnings       []string
}

// UnsupportedError means the binary is present but lacks a verb/flag.
type UnsupportedError struct {
	Provider string
	Feature  string
	Hint     string
}

func (e *UnsupportedError) Error() string {
	msg := fmt.Sprintf("%s does not support %s", e.Provider, e.Feature)
	if e.Hint != "" {
		return msg + ": " + e.Hint
	}
	return msg
}

// UnavailableError means the provider binary is missing or not runnable.
type UnavailableError struct {
	Provider string
	Err      error
	Hint     string
}

func (e *UnavailableError) Error() string {
	msg := fmt.Sprintf("%s provider unavailable", e.Provider)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	return msg
}

func (e *UnavailableError) Unwrap() error { return e.Err }

// RefusalError wraps a provider refusal (e.g. unpushed edits on destroy).
type RefusalError struct {
	Provider string
	Code     string
	Message  string
	Hint     string
}

func (e *RefusalError) Error() string {
	msg := e.Message
	if e.Code != "" {
		msg = fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	return msg
}

func printf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}
