package memory

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRecordingWrapCapturesResultsAndKeepsExtensions(t *testing.T) {
	ctx := context.Background()
	fake := &Fake{}
	fake.StatusFn = func(_ context.Context, opts StatusOptions) (StatusResult, error) {
		if opts.StoreID == "bad" {
			return StatusResult{}, errors.New("nope")
		}
		return StatusResult{StoreID: opts.StoreID, Lifetime: "task"}, nil
	}
	rec := &Recording{}
	wrapped := rec.Wrap(fake)

	if _, err := wrapped.Attach(ctx, AttachOptions{SpaceID: "s", SpacePath: t.TempDir(), Name: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Status(ctx, StatusOptions{StoreID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Status(ctx, StatusOptions{StoreID: "bad"}); err == nil {
		t.Fatal("expected status error")
	}
	if _, err := wrapped.Sync(ctx, SyncOptions{StoreID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Propose(ctx, ProposeOptions{StoreID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Detach(ctx, DetachOptions{StoreID: "s", Fate: FateDestroy, Owned: true}); err != nil {
		t.Fatal(err)
	}

	if len(rec.Attaches) != 1 || rec.Attaches[0].StoreID != "s" || !rec.Attaches[0].Owned {
		t.Fatalf("attaches: %+v", rec.Attaches)
	}
	if len(rec.Statuses) != 2 {
		t.Fatalf("statuses: %+v", rec.Statuses)
	}
	if st, ok := rec.StatusFor("s"); !ok || st.Result.Lifetime != "task" || st.Err != nil {
		t.Fatalf("StatusFor s: %+v %v", st, ok)
	}
	if st, ok := rec.StatusFor("bad"); !ok || st.Err == nil {
		t.Fatalf("StatusFor bad must record the error: %+v %v", st, ok)
	}
	if _, ok := rec.StatusFor("missing"); ok {
		t.Fatal("StatusFor unknown store must miss")
	}
	if len(rec.Syncs) != 1 || len(rec.Proposes) != 1 {
		t.Fatalf("sync/propose: %+v %+v", rec.Syncs, rec.Proposes)
	}
	if len(rec.Detaches) != 1 || rec.Detaches[0].Options.Fate != FateDestroy || !rec.Detaches[0].Result.Destroyed {
		t.Fatalf("detaches: %+v", rec.Detaches)
	}

	// Optional extensions forward to the wrapped provider.
	linker, ok := wrapped.(ReferenceLinker)
	if !ok {
		t.Fatal("wrapped fake must stay a ReferenceLinker")
	}
	if _, err := linker.LinkReference(ctx, LinkReferenceOptions{StoreID: "s", Spec: ReferenceSpec{Name: "docs"}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	wirer, ok := wrapped.(MCPWirer)
	if !ok {
		t.Fatal("wrapped fake must stay an MCPWirer")
	}
	dir := t.TempDir()
	if err := wirer.WriteMCPConfig(ctx, dir, "s"); err != nil {
		t.Fatal(err)
	}
	if err := wirer.RemoveMCPConfig(ctx, dir); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.Calls, "\n")
	for _, want := range []string{"link-reference", "write-mcp-config", "remove-mcp-config"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing forwarded call %q:\n%s", want, joined)
		}
	}
	if wrapped.Name() != "fake" {
		t.Fatalf("Name must forward: %q", wrapped.Name())
	}
}

func TestRecordingWrapBareProviderHasNoExtensions(t *testing.T) {
	var p Provider = &bareProviderOnly{}
	wrapped := (&Recording{}).Wrap(p)
	if _, ok := wrapped.(ReferenceLinker); ok {
		t.Fatal("bare provider must not gain ReferenceLinker")
	}
	if _, ok := wrapped.(MCPWirer); ok {
		t.Fatal("bare provider must not gain MCPWirer")
	}
}

// bareProviderOnly satisfies Provider without any optional extension.
type bareProviderOnly struct{}

func (bareProviderOnly) Name() string                               { return "bare" }
func (bareProviderOnly) Probe(context.Context) (ProbeResult, error) { return ProbeResult{}, nil }
func (bareProviderOnly) Attach(context.Context, AttachOptions) (AttachResult, error) {
	return AttachResult{}, nil
}
func (bareProviderOnly) Status(context.Context, StatusOptions) (StatusResult, error) {
	return StatusResult{}, nil
}
func (bareProviderOnly) Sync(context.Context, SyncOptions) (SyncResult, error) {
	return SyncResult{}, nil
}
func (bareProviderOnly) Propose(context.Context, ProposeOptions) (ProposeResult, error) {
	return ProposeResult{}, nil
}
func (bareProviderOnly) Detach(context.Context, DetachOptions) (DetachResult, error) {
	return DetachResult{}, nil
}

func TestMarmotCapabilitiesAndBinaryName(t *testing.T) {
	ctx := context.Background()
	respond := func(joined string) string {
		switch {
		case strings.Contains(joined, "den --help"):
			return "usage: marmot den <command>\n  create <den-id> [--ref <spec>]...\n  link <den-id> --edit|--link <target>\n"
		case strings.Contains(joined, "warren --help"):
			return "usage: marmot warren <command>\n  add\n  sync\n"
		case strings.Contains(joined, "--version"):
			return "marmot 1.2.3\n"
		}
		return ""
	}
	m := &Marmot{
		Binary:   "marmot-custom",
		LookPath: func(string) (string, error) { return "/bin/marmot-custom", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "printf", "%s", respond(strings.Join(args, " ")))
		},
	}
	if m.BinaryName() != "marmot-custom" {
		t.Fatalf("BinaryName = %q", m.BinaryName())
	}
	got := strings.Join(m.Capabilities(ctx), ",")
	if got != "dens,refs,links,warrens" {
		t.Fatalf("capabilities = %q", got)
	}

	old := &Marmot{
		LookPath: func(string) (string, error) { return "/bin/marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "printf", "%s", "usage: marmot den <command>\n  create <den-id>\n  status\n")
		},
	}
	if got := strings.Join(old.Capabilities(ctx), ","); got != "dens" {
		t.Fatalf("old binary capabilities = %q", got)
	}

	missing := &Marmot{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	if caps := missing.Capabilities(ctx); caps != nil {
		t.Fatalf("missing binary must report no capabilities: %v", caps)
	}
	if (&Marmot{}).BinaryName() != "marmot" {
		t.Fatal("default binary name must be marmot")
	}
}

func TestFakeCapabilities(t *testing.T) {
	f := &Fake{}
	if got := strings.Join(f.Capabilities(context.Background()), ","); got != "dens,refs,links" {
		t.Fatalf("default fake capabilities = %q", got)
	}
	f.CapabilitiesFn = func(context.Context) []string { return []string{"dens"} }
	if got := strings.Join(f.Capabilities(context.Background()), ","); got != "dens" {
		t.Fatalf("custom fake capabilities = %q", got)
	}
}
