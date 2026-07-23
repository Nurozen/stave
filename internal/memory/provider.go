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

// ReferenceLinker is an OPTIONAL Provider extension (S4 `space add` parity,
// plan §3.6): providers implementing it resolve one newly added reference
// repo into a read-only link on an already-attached store. Callers
// type-assert; providers without it simply skip linking.
type ReferenceLinker interface {
	LinkReference(ctx context.Context, opts LinkReferenceOptions) (LinkReferenceResult, error)
}

// LinkReferenceOptions drives one post-attach reference link (space add).
type LinkReferenceOptions struct {
	StoreID   string
	SpacePath string
	Spec      ReferenceSpec
	// DryRun prints exact ops without invoking the binary.
	DryRun bool
	Out    io.Writer
}

// LinkReferenceResult reports one space-add link outcome.
type LinkReferenceResult struct {
	// Linked is true when a provider link was created. False with a Notice
	// means the reference did not resolve to memory or the binary lacks the
	// verbs (repo still added either way — linking is soft).
	Linked bool
	Link   AttachLink
	// Notice is the human line for skipped/degraded outcomes.
	Notice         string
	Warnings       []string
	DryRunCommands []string
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
	// Links are the provider-resolved reference links from the create
	// envelope (S4): one entry per --ref, in spec order, plus any links made
	// by --edit/--link pass-through. ResolvedVia is the marmot vocabulary
	// (warren-url | checkout-vault | none) or "explicit" for direct links.
	Links []AttachLink
}

// AttachLink is one resolved reference/link outcome on attach.
type AttachLink struct {
	// Ref is the provider-side link label (e.g. "warren/project" for
	// resolved refs, the spec name for unresolved ones).
	Ref string
	// Mode is edit|link|live, empty when the reference did not resolve.
	Mode string
	// ResolvedVia is warren-url|checkout-vault|none for --ref resolution,
	// or "explicit" for --edit/--link/marmotVault-forced links.
	ResolvedVia string
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
	// Lifetime is the provider store lifetime (marmot: task|durable).
	Lifetime string
	// Links carry per-link freshness parsed from the provider status
	// envelope (S4 skew intelligence). Empty for providers/binaries that
	// report no links.
	Links []LinkStatus
}

// LinkStatus is one den link's freshness row from `den status --json`.
type LinkStatus struct {
	Ref          string
	Mode         string
	PinnedCommit string
	Ahead        int
	Behind       int
	PendingEdits int
	// State is marmot's vocabulary: ok | unpushed | stale | unreachable.
	State string
	// SourceCommit, when set on a pinned link, is the source-repo commit the
	// vault snapshot was taken from (skew note vs. PinnedCommit).
	SourceCommit string
}

// StateSuffix compacts link freshness into a short row suffix for
// `space status` (e.g. " (2 unpushed)", " (stale)"). Empty when everything
// is ok or no link data is available.
func (r StatusResult) StateSuffix() string {
	pending := 0
	stale := false
	unreachable := false
	for _, l := range r.Links {
		pending += l.PendingEdits
		switch l.State {
		case "stale":
			stale = true
		case "unreachable":
			unreachable = true
		}
	}
	switch {
	case pending > 0:
		return fmt.Sprintf(" (%d unpushed)", pending)
	case stale:
		return " (stale)"
	case unreachable:
		return " (unreachable)"
	}
	return ""
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
	// Warrens are per-warren outcomes from `warren sync --json` (S4).
	Warrens []WarrenSync
}

// WarrenSync mirrors one entry of marmot's warren sync envelope
// (testdata/contracts/warren_sync.v1.json). Field names are the stable
// stave-consumed contract.
type WarrenSync struct {
	ID             string `json:"id"`
	Fetched        bool   `json:"fetched"`
	PreviousCommit string `json:"previous_commit"`
	PinnedCommit   string `json:"pinned_commit"`
	Updated        bool   `json:"updated"`
	Error          string `json:"error,omitempty"`
}

type ProposeOptions struct {
	StoreID string
	// SpacePath is the absolute space root. When set, providers run their
	// subprocesses with this as the working directory so cwd-based workspace
	// resolution (marmot reverse routes) targets the space, not stave's cwd.
	SpacePath string
	Out       io.Writer
	DryRun    bool
	// Force is reserved for contribute refusal overrides (not auto-push).
	Force bool
}

// ContributedCounts mirrors marmot's den contribute `contributed` object.
type ContributedCounts struct {
	Added      int `json:"added"`
	Updated    int `json:"updated"`
	Superseded int `json:"superseded"`
	Noop       int `json:"noop"`
}

type ProposeResult struct {
	Summary        string
	DryRunCommands []string
	Warnings       []string
	// RawJSON is the contribute envelope; ProposeRawJSON the propose envelope.
	RawJSON        string
	ProposeRawJSON string
	// Handoff data parsed from the contribute/propose envelopes (G4): the user
	// must learn what was contributed and what to push — stave never auto-pushes.
	Branch           string
	Commit           string
	Committed        bool
	Contributed      *ContributedCounts
	PushCommand      string
	Checkout         string
	NothingToPropose bool
}

// PrintProposeOutcome renders contribute/propose handoff data for humans:
// contributed counts, every warning (never silently dropped — especially
// before a destroy), and the push command (or "nothing new to push").
func PrintProposeOutcome(w io.Writer, res ProposeResult) {
	if res.Contributed != nil {
		c := res.Contributed
		printf(w, "contributed: %d added, %d updated, %d superseded, %d noop\n", c.Added, c.Updated, c.Superseded, c.Noop)
	}
	if res.Branch != "" {
		if res.Commit != "" {
			printf(w, "branch: %s @ %s\n", res.Branch, res.Commit)
		} else {
			printf(w, "branch: %s\n", res.Branch)
		}
	}
	for _, warn := range res.Warnings {
		printf(w, "warning: %s\n", warn)
	}
	switch {
	case res.PushCommand != "":
		printf(w, "push with: %s\n", res.PushCommand)
	case res.NothingToPropose:
		printf(w, "nothing new to push\n")
	}
}

type DetachOptions struct {
	StoreID   string
	SpacePath string
	// NewSpacePath, when set, asks the provider to rewrite the reverse route
	// (archive path move). Empty + Fate keep → route remove or leave as-is.
	NewSpacePath string
	// RemoveRoute drops the reverse route without destroying the store.
	RemoveRoute bool
	// KeepSpaceWiring: other attachments of this provider remain on the
	// space, so space-level wiring (space-local MCP configs) must be left in
	// place — only detaching the provider's LAST attachment tears it down.
	KeepSpaceWiring bool
	// RepointRouteStoreID, when set (with KeepSpaceWiring), names a remaining
	// attachment's store id: the provider re-points the space reverse route
	// (and MCP config) at it so neither dangles at the detached store. The
	// route maps one space path to exactly one store, so it must follow a
	// surviving attachment.
	RepointRouteStoreID string
	Fate                MemoryFate
	Force               bool
	Owned               bool // caller must pass attachment.Owned; false never destroys
	DryRun              bool
	Out                 io.Writer
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
