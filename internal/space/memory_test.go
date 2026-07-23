package space

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/memory"
)

// oldMarmotFake builds a *memory.Marmot whose capability probe reports a
// pre-P4 binary (no den link/--ref) and answers den create/status and route
// add with minimal envelopes. Every spawned argv is appended to calls.
func oldMarmotFake(t *testing.T, calls *[]string) *memory.Marmot {
	t.Helper()
	oldUsage := "usage: marmot den <command> [flags]\n" +
		"  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--no-pointer] [--json]\n" +
		"  status  [<den-id>] [--json]\n"
	respond := func(joined string) string {
		switch {
		case strings.Contains(joined, "den --help"):
			return oldUsage
		case strings.Contains(joined, "den create"):
			return `{"schema":1,"den_id":"","den_path":"/tmp/dens/x","pointer_written":false,"warnings":[]}`
		default: // den status / route add
			return `{"schema":1,"warnings":[]}`
		}
	}
	return &memory.Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			joined := strings.Join(args, " ")
			*calls = append(*calls, joined)
			return exec.CommandContext(ctx, "printf", "%s", respond(joined))
		},
	}
}

// Space-create path regression (validator finding): `space create --memory …`
// with reference repos on the space and an OLD marmot (probe fails) drops the
// S4 --edit/--link/--ref content — the downgrade notice must reach the
// operator through the service Out on THIS path, for both the fresh-den and
// the attach-existing (--memory marmot:<id>) shapes.
func TestCreateWithMemoryOldMarmotPrintsDropNotice(t *testing.T) {
	for name, spec := range map[string]string{
		"fresh":           ".",
		"attach-existing": "marmot:existing-den",
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, cfg := testService(t)
			if _, ok := cfg.Repos["repo-b"]; !ok {
				t.Fatal("test fixture missing repo-b")
			}
			var calls []string
			m := oldMarmotFake(t, &calls)
			svc.MemoryFactory = func(string) (memory.Provider, error) { return m, nil }
			var out strings.Builder
			svc.Out = &out

			if err := svc.Create(context.Background(), CreateOptions{
				ID:         "drop-" + name,
				References: []RepoSpec{{Name: "repo-b", Ref: "main"}},
				Memories:   []string{spec},
			}); err != nil {
				t.Fatal(err)
			}

			// The old binary must never see S4 argv…
			for _, c := range calls {
				if strings.Contains(c, "--ref ") || strings.Contains(c, "den link") {
					t.Fatalf("old marmot must not receive S4 argv: %q", c)
				}
			}
			// …and the drop must be announced on the space-create output.
			rendered := out.String()
			if !strings.Contains(rendered, "notice:") ||
				!(strings.Contains(rendered, "dropping --edit/--link/--ref") || strings.Contains(rendered, "attached memory without --edit/--link/--ref")) {
				t.Fatalf("drop notice missing from space create output:\n%s", rendered)
			}
			if !strings.Contains(rendered, "upgrade marmot") && !strings.Contains(rendered, "predates den link/--ref support") {
				t.Fatalf("notice must explain the marmot skew:\n%s", rendered)
			}
		})
	}
}

func TestCreateWithMemoryFakeProvider(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake

	var out strings.Builder
	svc.Out = &out

	if err := svc.Create(context.Background(), CreateOptions{
		ID:       "demo-space",
		Memories: []string{"."},
	}); err != nil {
		t.Fatal(err)
	}

	// Attach must run after InitSpace — space exists with memories record.
	spacePath := svc.SpacePath("demo-space")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	mem := manifest.Memories[0]
	// Provider name comes from AttachResult.Provider (fake reports "fake");
	// production marmot reports "marmot".
	if mem.Name != "default" || mem.ID != "demo-space" || !mem.Owned {
		t.Fatalf("attachment = %#v", mem)
	}
	if mem.Provider == "" {
		t.Fatal("provider empty")
	}
	// MCP config written; no .marmot-vault.
	if _, err := os.Stat(filepath.Join(spacePath, ".mcp.json")); err != nil {
		t.Fatalf("mcp config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault must not exist")
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "context-marmot MCP tools") {
		t.Fatalf("AGENTS.md missing memory line:\n%s", agents)
	}
	if !strings.Contains(string(agents), "demo-space") {
		t.Fatalf("AGENTS.md missing den id:\n%s", agents)
	}
	// Fake recorded attach.
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "attach") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fake calls = %#v", fake.Calls)
	}
}

func TestCreateMemoryDryRunNoExecAndPrintsNoPointer(t *testing.T) {
	svc, _, _ := testService(t)
	// Use real marmot provider for dry-run argv shape (no binary invoke).
	svc.Memory = memory.NewMarmot("marmot")
	var out strings.Builder
	svc.Out = &out

	if err := svc.Create(context.Background(), CreateOptions{
		ID:       "demo-space",
		Memories: []string{"."},
		DryRun:   true,
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "marmot den create") {
		t.Fatalf("expected den create line:\n%s", text)
	}
	if !strings.Contains(text, "--no-pointer") {
		t.Fatalf("expected --no-pointer:\n%s", text)
	}
	if !strings.Contains(text, "--json") {
		t.Fatalf("expected --json:\n%s", text)
	}
	// Space must not exist after dry-run.
	if _, err := os.Stat(svc.SpacePath("demo-space")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create space")
	}
}

func TestExplicitMemoryAttachFailsHard(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing binary")}
		},
	}
	svc.Memory = fake
	err := svc.Create(context.Background(), CreateOptions{
		ID:       "fail-space",
		Memories: []string{"."},
	})
	if err == nil {
		t.Fatal("expected hard failure")
	}
	// Space was created (attach after init) but no memories entry.
	manifest, loadErr := LoadManifest(svc.SpacePath("fail-space"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("partial memories entry: %#v", manifest.Memories)
	}
}

func TestAmbientMemoryDegrades(t *testing.T) {
	svc, _, cfg := testService(t)
	cfg.Memory.Default = true
	svc.Config = cfg
	fake := &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing")}
		},
	}
	svc.Memory = fake
	var out strings.Builder
	svc.Out = &out
	if err := svc.Create(context.Background(), CreateOptions{ID: "ambient"}); err != nil {
		t.Fatalf("ambient should not fail create: %v", err)
	}
	if !strings.Contains(out.String(), "notice:") {
		t.Fatalf("expected notice:\n%s", out.String())
	}
	manifest, err := LoadManifest(svc.SpacePath("ambient"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("ambient degrade must leave no memories: %#v", manifest.Memories)
	}
}

// reviewShapedSpace builds a space the way stave review does: InitSpace +
// AddRepo loops, never Service.Create.
func reviewShapedSpace(t *testing.T, svc Service, id string) {
	t.Helper()
	if err := svc.InitSpace(context.Background(), InitOptions{ID: id, Kind: "review"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: id, RepoName: "repo-a", Mode: ModeEdit, Base: "main"}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachMemoriesReviewShapedExplicitFailsHard(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Memory = &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing binary")}
		},
	}
	reviewShapedSpace(t, svc, "rev-strict")
	err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{
		SpaceID: "rev-strict",
		Specs:   []string{"."},
	})
	if err == nil {
		t.Fatal("explicit spec must fail hard")
	}
	manifest, loadErr := LoadManifest(svc.SpacePath("rev-strict"))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(manifest.Memories) != 0 {
		t.Fatalf("failed attach must leave no memories entry: %#v", manifest.Memories)
	}
}

func TestAttachMemoriesReviewShapedAmbientDegrades(t *testing.T) {
	svc, _, cfg := testService(t)
	cfg.Memory.Default = true
	svc.Config = cfg
	svc.Memory = &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			return memory.AttachResult{}, &memory.UnavailableError{Provider: "fake", Err: errors.New("missing")}
		},
	}
	var out strings.Builder
	svc.Out = &out
	reviewShapedSpace(t, svc, "rev-ambient")
	if err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{SpaceID: "rev-ambient"}); err != nil {
		t.Fatalf("ambient must degrade softly: %v", err)
	}
	if !strings.Contains(out.String(), "notice: ambient memory attach failed") {
		t.Fatalf("expected degrade notice:\n%s", out.String())
	}
	// Space survives with its repos and no memories entry.
	manifest, err := LoadManifest(svc.SpacePath("rev-ambient"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || len(manifest.Memories) != 0 {
		t.Fatalf("manifest after degrade = %#v", manifest)
	}
}

func TestAttachMemoriesReviewShapedNoSpecsNoAmbientIsNoop(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	reviewShapedSpace(t, svc, "rev-noop")
	if err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{SpaceID: "rev-noop"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("no specs and no ambient must not touch the provider: %#v", fake.Calls)
	}
}

func TestAttachMemoriesReviewShapedReferencesPassedThrough(t *testing.T) {
	svc, _, cfg := testService(t)
	if _, ok := cfg.Repos["repo-b"]; !ok {
		t.Fatal("test fixture missing repo-b")
	}
	var saw []memory.ReferenceSpec
	svc.Memory = &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			saw = opts.ReferenceSpecs
			return memory.AttachResult{Provider: "fake", StoreID: opts.SpaceID, Name: "default", Owned: true, MCPConfigWritten: true}, nil
		},
	}
	reviewShapedSpace(t, svc, "rev-refs")
	if err := svc.AddRepo(context.Background(), AddOptions{SpaceID: "rev-refs", RepoName: "repo-b", Mode: ModeReference, Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{
		SpaceID:    "rev-refs",
		Specs:      []string{"."},
		References: []RepoSpec{{Name: "repo-b", Ref: "main"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0].Name != "repo-b" || saw[0].URL == "" {
		t.Fatalf("reference specs = %#v", saw)
	}
	// Attach rewrote AGENTS.md with the memory line.
	spacePath := svc.SpacePath("rev-refs")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 1 || manifest.Memories[0].ID != "rev-refs" {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	agents, err := os.ReadFile(filepath.Join(spacePath, AgentsName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "context-marmot MCP tools (den: rev-refs)") {
		t.Fatalf("AGENTS.md missing memory line after attach:\n%s", agents)
	}
}

func TestAttachMemoriesTwoSpecsUniqueAliases(t *testing.T) {
	// G3: repeatable --memory attaches BOTH specs with distinct aliases —
	// fresh store keeps "default", attach-existing derives its alias from the id.
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.Create(context.Background(), CreateOptions{
		ID:       "multi",
		Memories: []string{".", "fake:extra-den"},
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(svc.SpacePath("multi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 2 {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	first, second := manifest.Memories[0], manifest.Memories[1]
	if first.Name != "default" || first.ID != "multi" || !first.Owned {
		t.Fatalf("first = %#v", first)
	}
	if second.Name != "extra-den" || second.ID != "extra-den" || second.Owned {
		t.Fatalf("second = %#v", second)
	}
}

func TestAttachMemoriesTwoFreshSpecsSuffixAliases(t *testing.T) {
	// Two fresh specs on DIFFERENT providers must not collide on alias "default".
	svc, _, _ := testService(t)
	svc.MemoryFactory = func(string) (memory.Provider, error) { return &memory.Fake{}, nil }
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "fresh2"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{
		SpaceID: "fresh2",
		Specs:   []string{"fake:den-a", "fake:den-b"},
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(svc.SpacePath("fresh2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 2 {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	if manifest.Memories[0].Name == manifest.Memories[1].Name {
		t.Fatalf("aliases collide: %#v", manifest.Memories)
	}
}

func TestAttachMemoriesBadBatchAttachesNothing(t *testing.T) {
	// G3: prevalidation rejects a bad batch BEFORE any attach — no partial state.
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	for _, specs := range [][]string{
		{".", "."},                       // two fresh on the same provider → same store id
		{"fake:dup-den", "fake:dup-den"}, // duplicate den ids
	} {
		if err := svc.InitSpace(context.Background(), InitOptions{ID: "badbatch"}); err != nil {
			t.Fatal(err)
		}
		err := svc.AttachMemories(context.Background(), AttachMemoriesOptions{
			SpaceID: "badbatch",
			Specs:   specs,
		})
		if err == nil {
			t.Fatalf("bad batch %v must fail", specs)
		}
		manifest, loadErr := LoadManifest(svc.SpacePath("badbatch"))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(manifest.Memories) != 0 {
			t.Fatalf("bad batch %v attached partially: %#v", specs, manifest.Memories)
		}
		if len(fake.Calls) != 0 {
			t.Fatalf("bad batch %v must not touch provider: %#v", specs, fake.Calls)
		}
	}
}

func TestArchiveTwoAttachmentsSingleRouteRelocation(t *testing.T) {
	// G1: route relocation is space-level — exactly ONE per provider, not one
	// per attachment (the second set-project would fail on the consumed route).
	svc, _, cfg := testService(t)
	var detachCalls []memory.DetachOptions
	fake := &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			detachCalls = append(detachCalls, opts)
			return memory.DetachResult{Kept: true}, nil
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "arch2"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("arch2")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "default", Provider: "fake", ID: "arch2", Owned: true},
		{Name: "shared", Provider: "fake", ID: "pre-den", Owned: false},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "arch2"}); err != nil {
		t.Fatal(err)
	}
	if len(detachCalls) != 1 {
		t.Fatalf("expected exactly one route relocation, got %d: %#v", len(detachCalls), detachCalls)
	}
	dest := filepath.Join(cfg.AgentWorkDir, ".archive", "arch2")
	if detachCalls[0].NewSpacePath != dest {
		t.Fatalf("NewSpacePath = %q, want %q", detachCalls[0].NewSpacePath, dest)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("archived dest missing: %v", err)
	}
}

func TestArchiveRouteRelocationFailureWarnsNotAborts(t *testing.T) {
	// G1: rename-first — a route-update failure leaves the space archived with
	// a loud warning instead of aborting after routing was mutated.
	svc, _, cfg := testService(t)
	fake := &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			return memory.DetachResult{}, errors.New("route boom")
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "archwarn"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("archwarn")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "archwarn", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "archwarn"}); err != nil {
		t.Fatalf("archive must not abort on route failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentWorkDir, ".archive", "archwarn")); err != nil {
		t.Fatalf("space must be archived: %v", err)
	}
	if !strings.Contains(out.String(), "warning") || !strings.Contains(out.String(), "route") {
		t.Fatalf("expected loud route warning:\n%s", out.String())
	}
}

func TestDestroyTwoAttachmentsSingleRouteRemoval(t *testing.T) {
	// G1 (destroy flavor): route rm --project is space-level — RemoveRoute set
	// on exactly one keep-detach per provider.
	svc, _, _ := testService(t)
	var removeRoutes []bool
	fake := &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			removeRoutes = append(removeRoutes, opts.RemoveRoute)
			return memory.DetachResult{Kept: true}, nil
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "destroy2"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("destroy2")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "default", Provider: "fake", ID: "d1", Owned: true},
		{Name: "shared", Provider: "fake", ID: "d2", Owned: false},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "destroy2"}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, rr := range removeRoutes {
		if rr {
			count++
		}
	}
	if len(removeRoutes) != 2 || count != 1 {
		t.Fatalf("expected 2 detaches with exactly 1 RemoveRoute: %#v", removeRoutes)
	}
}

func TestDestroyMemoryFateOwnedOnly(t *testing.T) {
	svc, fg, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "fate-space"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("fate-space")
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Memories = []MemoryManifest{
		{Name: "owned", Provider: "fake", ID: "owned-den", Owned: true},
		{Name: "shared", Provider: "fake", ID: "shared-den", Owned: false},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	if err := svc.Destroy(context.Background(), DestroyOptions{
		SpaceID:    "fate-space",
		MemoryFate: memory.FateDestroy,
		Force:      true,
	}); err != nil {
		t.Fatal(err)
	}
	// Fake should have detach for both; owned with destroy, shared forced keep.
	var ownedFate, sharedFate string
	for _, c := range fake.Calls {
		if strings.Contains(c, "owned-den") {
			ownedFate = c
		}
		if strings.Contains(c, "shared-den") {
			sharedFate = c
		}
	}
	if !strings.Contains(ownedFate, "destroy") {
		t.Fatalf("owned fate call = %q", ownedFate)
	}
	// shared still detached but owned=false
	if !strings.Contains(sharedFate, "owned=false") {
		t.Fatalf("shared call = %q", sharedFate)
	}
	_ = fg
}

func TestDestroyDefaultKeep(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "keep-space"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("keep-space")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "keep-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "keep-space"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "keep") || strings.Contains(c, "detach") {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls = %#v", fake.Calls)
	}
}

func TestArchiveRouteRewrite(t *testing.T) {
	svc, fg, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "arch"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("arch")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "arch-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "arch"}); err != nil {
		t.Fatal(err)
	}
	// Detach called with keep fate for archive.
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "detach") && strings.Contains(c, "arch-den") {
			found = true
		}
		// Default fate (keep) must never contribute.
		if strings.Contains(c, "propose") {
			t.Fatalf("default archive must not propose: %#v", fake.Calls)
		}
	}
	if !found {
		t.Fatalf("expected detach on archive: %#v", fake.Calls)
	}
	_ = fg
}

func TestArchiveContributeProposesAllBeforeDetach(t *testing.T) {
	svc, _, cfg := testService(t)
	var detachPaths []string
	fake := &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			detachPaths = append(detachPaths, opts.NewSpacePath)
			return memory.DetachResult{Kept: true}, nil
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "contrib"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("contrib")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "owned", Provider: "fake", ID: "owned-den", Owned: true},
		{Name: "shared", Provider: "fake", ID: "shared-den", Owned: false},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{
		SpaceID:    "contrib",
		MemoryFate: memory.FateContribute,
	}); err != nil {
		t.Fatal(err)
	}
	// Contribute is non-destructive: BOTH attachments proposed, including owned:false.
	lastPropose, firstDetach := -1, -1
	proposed := map[string]bool{}
	for i, c := range fake.Calls {
		if strings.Contains(c, "propose") {
			lastPropose = i
			for _, id := range []string{"owned-den", "shared-den"} {
				if strings.Contains(c, id) {
					proposed[id] = true
				}
			}
		}
		if strings.Contains(c, "detach") && firstDetach == -1 {
			firstDetach = i
		}
	}
	if !proposed["owned-den"] || !proposed["shared-den"] {
		t.Fatalf("expected propose for both attachments: %#v", fake.Calls)
	}
	if firstDetach == -1 || lastPropose == -1 || lastPropose > firstDetach {
		t.Fatalf("propose must complete before detach: %#v", fake.Calls)
	}
	// Normal archive still happened: keep-detach with route rewrite to .archive dest.
	dest := filepath.Join(cfg.AgentWorkDir, ".archive", "contrib")
	for _, p := range detachPaths {
		if p != dest {
			t.Fatalf("detach NewSpacePath = %q, want %q", p, dest)
		}
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("archived dest missing: %v", err)
	}
	if _, err := os.Stat(spacePath); !os.IsNotExist(err) {
		t.Fatal("original space path should be gone after archive")
	}
}

func TestArchiveContributeRefusalAborts(t *testing.T) {
	svc, fg, _ := testService(t)
	fake := &memory.Fake{
		ProposeFn: func(ctx context.Context, opts memory.ProposeOptions) (memory.ProposeResult, error) {
			return memory.ProposeResult{}, &memory.RefusalError{Provider: "fake", Code: "unpushed_edits", Message: "refusing to contribute"}
		},
	}
	svc.Memory = fake
	reviewShapedSpace(t, svc, "refuse")
	spacePath := svc.SpacePath("refuse")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "ref-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	gitCallsBefore := len(fg.calls)

	err := svc.Archive(context.Background(), ArchiveOptions{
		SpaceID:    "refuse",
		Force:      true,
		MemoryFate: memory.FateContribute,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to contribute") {
		t.Fatalf("expected refusal error, got %v", err)
	}
	var refusal *memory.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal must stay unwrappable: %v", err)
	}
	// Space completely untouched: dir at original path, worktree intact,
	// manifest still lists the memory, no detach and no worktree removal.
	if _, err := os.Stat(spacePath); err != nil {
		t.Fatalf("space must remain at original path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spacePath, "repo-a")); err != nil {
		t.Fatalf("worktree must remain: %v", err)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 1 {
		t.Fatalf("manifest memories changed: %#v", manifest.Memories)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c, "detach") {
			t.Fatalf("no detach after refusal: %#v", fake.Calls)
		}
	}
	for _, c := range fg.calls[gitCallsBefore:] {
		if strings.Contains(c, "remove") || strings.Contains(c, "prune") {
			t.Fatalf("no worktree mutation after refusal: %#v", fg.calls)
		}
	}
}

func TestArchiveContributeDryRun(t *testing.T) {
	svc, _, _ := testService(t)
	var proposeDry []bool
	fake := &memory.Fake{
		ProposeFn: func(ctx context.Context, opts memory.ProposeOptions) (memory.ProposeResult, error) {
			proposeDry = append(proposeDry, opts.DryRun)
			return memory.ProposeResult{Summary: "dry-run"}, nil
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "cdry"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("cdry")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "cdry-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(context.Background(), ArchiveOptions{
		SpaceID:    "cdry",
		DryRun:     true,
		MemoryFate: memory.FateContribute,
	}); err != nil {
		t.Fatal(err)
	}
	if len(proposeDry) != 1 || !proposeDry[0] {
		t.Fatalf("expected one dry-run propose: %#v", proposeDry)
	}
	// No mutation: space still at original path with its manifest.
	if _, err := os.Stat(spacePath); err != nil {
		t.Fatalf("dry-run must not move space: %v", err)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 1 {
		t.Fatalf("dry-run must not change manifest: %#v", manifest.Memories)
	}
}

func TestArchiveMemoryDestroyRejected(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "nodestroy"}); err != nil {
		t.Fatal(err)
	}
	err := svc.Archive(context.Background(), ArchiveOptions{
		SpaceID:    "nodestroy",
		MemoryFate: memory.FateDestroy,
	})
	if err == nil || !strings.Contains(err.Error(), "stave space destroy --memory destroy") {
		t.Fatalf("expected destroy redirect error, got %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("rejected archive must not touch provider: %#v", fake.Calls)
	}
	if _, err := os.Stat(svc.SpacePath("nodestroy")); err != nil {
		t.Fatalf("space must remain: %v", err)
	}
}

func TestProposeMemoryDryRun(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "prop"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("prop")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "prop-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProposeMemory(context.Background(), "prop", "", true); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "propose") {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls = %#v", fake.Calls)
	}
}

func TestReferenceSpecsPassedThrough(t *testing.T) {
	svc, _, cfg := testService(t)
	if _, ok := cfg.Repos["repo-b"]; !ok {
		t.Fatal("test fixture missing repo-b")
	}
	var saw []memory.ReferenceSpec
	fake := &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			saw = opts.ReferenceSpecs
			return memory.AttachResult{Provider: "fake", StoreID: opts.SpaceID, Name: "default", Owned: true, MCPConfigWritten: true}, nil
		},
	}
	svc.Memory = fake
	svc.Config = cfg
	// Need git fake for AddRepo of reference — Create with reference + memory.
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "ref-space",
		References: []RepoSpec{{Name: "repo-b", Ref: "main"}},
		Memories:   []string{"."},
	}); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 {
		t.Fatalf("reference specs = %#v", saw)
	}
	if saw[0].Name != "repo-b" || saw[0].URL == "" {
		t.Fatalf("spec = %#v", saw[0])
	}
}

func TestDetachMemoryOwnedAndUnowned(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "det"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("det")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "default", Provider: "fake", ID: "owned-den", Owned: true},
		{Name: "shared", Provider: "fake", ID: "shared-den", Owned: false},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	// Destroy unowned → forced keep, removed from manifest.
	var out strings.Builder
	svc.Out = &out
	if err := svc.DetachMemory(context.Background(), "det", "shared", memory.FateDestroy, false, false); err != nil {
		t.Fatal(err)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 1 || manifest.Memories[0].Name != "default" {
		t.Fatalf("after detach shared: %#v", manifest.Memories)
	}
	if !strings.Contains(out.String(), "not owned") {
		t.Fatalf("expected not owned notice: %s", out.String())
	}

	// Dry-run keep on remaining: no manifest change.
	if err := svc.DetachMemory(context.Background(), "det", "default", memory.FateKeep, false, true); err != nil {
		t.Fatal(err)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 1 {
		t.Fatalf("dry-run must not remove: %#v", manifest.Memories)
	}

	// Real detach keep
	if err := svc.DetachMemory(context.Background(), "det", "default", memory.FateKeep, false, false); err != nil {
		t.Fatal(err)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 0 {
		t.Fatalf("expected empty memories: %#v", manifest.Memories)
	}

	// Missing alias
	if err := svc.DetachMemory(context.Background(), "det", "nope", memory.FateKeep, false, false); err == nil {
		t.Fatal("expected missing alias error")
	}
	// Empty alias with zero attachments
	if err := svc.DetachMemory(context.Background(), "det", "", memory.FateKeep, false, false); err == nil {
		t.Fatal("expected empty-alias multi error")
	}
}

func TestDetachMemoryOneOfManyKeepsProviderWiring(t *testing.T) {
	svc, _, _ := testService(t)
	var seen []memory.DetachOptions
	fake := &memory.Fake{DetachFn: func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		seen = append(seen, opts)
		return memory.DetachResult{Kept: true}, nil
	}}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "multi"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("multi")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "default", Provider: "fake", ID: "den-a", Owned: true},
		{Name: "extra", Provider: "fake", ID: "den-b", Owned: true},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	// Detach one of two (same provider): space wiring must survive for the
	// sibling — no route removal, MCP configs kept, route re-pointed at the
	// surviving den.
	if err := svc.DetachMemory(context.Background(), "multi", "default", memory.FateKeep, false, false); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("detach calls = %#v", seen)
	}
	got := seen[0]
	if got.RemoveRoute {
		t.Fatalf("RemoveRoute must be false while a sibling remains: %#v", got)
	}
	if !got.KeepSpaceWiring {
		t.Fatalf("KeepSpaceWiring must be true while a sibling remains: %#v", got)
	}
	if got.RepointRouteStoreID != "den-b" {
		t.Fatalf("route must re-point at surviving den-b: %#v", got)
	}

	// Detach the last attachment: full space-wiring teardown.
	if err := svc.DetachMemory(context.Background(), "multi", "extra", memory.FateKeep, false, false); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("detach calls = %#v", seen)
	}
	got = seen[1]
	if !got.RemoveRoute || got.KeepSpaceWiring || got.RepointRouteStoreID != "" {
		t.Fatalf("last attachment must tear down wiring: %#v", got)
	}
	manifest, _ = LoadManifest(spacePath)
	if len(manifest.Memories) != 0 {
		t.Fatalf("expected empty memories: %#v", manifest.Memories)
	}
}

func TestDetachMemoryCountsSiblingsPerProvider(t *testing.T) {
	// A remaining attachment of a DIFFERENT provider must not keep this
	// provider's wiring alive: the detached attachment is the last of its
	// provider, so its route + MCP configs are removed.
	svc, _, _ := testService(t)
	var seen []memory.DetachOptions
	fake := &memory.Fake{DetachFn: func(_ context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
		seen = append(seen, opts)
		return memory.DetachResult{Kept: true}, nil
	}}
	svc.MemoryFactory = func(string) (memory.Provider, error) { return fake, nil }
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "mixed"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("mixed")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{
		{Name: "default", Provider: "fake", ID: "den-a", Owned: true},
		{Name: "other", Provider: "other", ID: "den-x", Owned: true},
	}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.DetachMemory(context.Background(), "mixed", "default", memory.FateKeep, false, false); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("detach calls = %#v", seen)
	}
	got := seen[0]
	if !got.RemoveRoute || got.KeepSpaceWiring || got.RepointRouteStoreID != "" {
		t.Fatalf("last-of-provider detach must tear down its wiring: %#v", got)
	}
}

func TestSyncAndMemoryStatus(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "syncsp"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("syncsp")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "sync-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.SyncMemory(context.Background(), "syncsp", "default", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncMemory(context.Background(), "syncsp", "default", true); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "sync") {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls = %#v", fake.Calls)
	}
	if err := svc.SyncMemory(context.Background(), "syncsp", "missing", false); err == nil {
		t.Fatal("expected missing")
	}
	// empty alias with one attachment still works via FindMemory("")? depends on FindMemory
	// MemoryStatus all
	out.Reset()
	if err := svc.MemoryStatus(context.Background(), "syncsp", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sync-den") {
		t.Fatalf("status out: %s", out.String())
	}
	// MemoryStatus alias
	if err := svc.MemoryStatus(context.Background(), "syncsp", "default"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MemoryStatus(context.Background(), "syncsp", "nope"); err == nil {
		t.Fatal("expected missing alias")
	}
	// no memories
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "empty-mem"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := svc.MemoryStatus(context.Background(), "empty-mem", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no memory") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestAttachMemoryDirectAndDuplicate(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "att"}); err != nil {
		t.Fatal(err)
	}
	// provider name "fake" matches Fake.Name(); empty provider also selects svc.Memory.
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{
		SpaceID:  "att",
		RawSpec:  ".",
		Strict:   true,
		Provider: "fake",
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(svc.SpacePath("att"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Memories) != 1 {
		t.Fatalf("memories = %#v", manifest.Memories)
	}
	// Duplicate attach rejected
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{
		SpaceID: "att",
		RawSpec: ".",
		Strict:  true,
	}); err == nil {
		t.Fatal("expected duplicate error")
	}
	// Dry-run attach another name
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{
		SpaceID: "att",
		Name:    "second",
		UseID:   "existing",
		DryRun:  true,
	}); err != nil {
		t.Fatal(err)
	}
	// Invalid raw spec
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{
		SpaceID: "att",
		RawSpec: "../bad",
		Strict:  true,
	}); err == nil {
		t.Fatal("expected bad spec")
	}
}

func TestAttachMemoryRemovesUnexpectedVault(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			// Simulate a bad provider that wrote .marmot-vault
			if opts.SpacePath != "" {
				_ = os.WriteFile(filepath.Join(opts.SpacePath, ".marmot-vault"), []byte("bad"), 0o644)
			}
			return memory.AttachResult{Provider: "fake", StoreID: opts.SpaceID, Name: "default", Owned: true, MCPConfigWritten: true}, nil
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "vaulty"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{SpaceID: "vaulty", Strict: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.SpacePath("vaulty"), ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatal(".marmot-vault should have been removed")
	}
	if !strings.Contains(out.String(), ".marmot-vault") {
		t.Fatalf("expected notice: %s", out.String())
	}
}

func TestAttachMemoryPortalNotice(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	svc.HasPortal = func(id string) bool { return id == "port" }
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "port"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.AttachMemory(context.Background(), AttachMemoryOptions{SpaceID: "port", Strict: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "portal") {
		t.Fatalf("expected portal notice: %s", out.String())
	}
}

func TestApplyMemoryFateProviderError(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			return memory.DetachResult{}, errors.New("detach boom")
		},
	}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "boom"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("boom")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "d", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	err := svc.Destroy(context.Background(), DestroyOptions{SpaceID: "boom", MemoryFate: memory.FateKeep, Force: true})
	if err == nil || !strings.Contains(err.Error(), "detach boom") {
		t.Fatalf("got %v", err)
	}
}

func TestArchiveDryRunWithMemory(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "ardy"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("ardy")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "arch-den", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.Archive(context.Background(), ArchiveOptions{SpaceID: "ardy", DryRun: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	// Space still exists
	if _, err := os.Stat(spacePath); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fake.Calls {
		if strings.Contains(c, "detach") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected detach dry-run: %#v", fake.Calls)
	}
}

func TestProposeMemoryMissing(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Memory = &memory.Fake{}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "prmiss"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProposeMemory(context.Background(), "prmiss", "nope", false); err == nil {
		t.Fatal("expected missing")
	}
	if err := svc.ProposeMemory(context.Background(), "prmiss", "", false); err == nil {
		t.Fatal("expected empty alias error")
	}
}

func TestDetachMemoryProviderError(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Memory = &memory.Fake{
		DetachFn: func(ctx context.Context, opts memory.DetachOptions) (memory.DetachResult, error) {
			return memory.DetachResult{}, errors.New("nope")
		},
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "derr"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("derr")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "d", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.DetachMemory(context.Background(), "derr", "default", memory.FateDestroy, true, false); err == nil {
		t.Fatal("expected error")
	}
}

func TestMemoryStatusProviderErrors(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Memory = &memory.Fake{
		StatusFn: func(ctx context.Context, opts memory.StatusOptions) (memory.StatusResult, error) {
			return memory.StatusResult{}, errors.New("status fail")
		},
	}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "sterr"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("sterr")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "d", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	svc.Out = &out
	if err := svc.MemoryStatus(context.Background(), "sterr", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "status fail") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestSyncMemoryEmptyAliasMulti(t *testing.T) {
	svc, _, _ := testService(t)
	svc.Memory = &memory.Fake{}
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "synce"}); err != nil {
		t.Fatal(err)
	}
	// zero memories, empty alias
	if err := svc.SyncMemory(context.Background(), "synce", "", false); err == nil {
		t.Fatal("expected error")
	}
}

func TestReferenceVaultOffSkipped(t *testing.T) {
	svc, _, cfg := testService(t)
	// mark repo-b vault off
	rb := cfg.Repos["repo-b"]
	rb.MarmotVault = "off"
	cfg.Repos["repo-b"] = rb
	svc.Config = cfg
	var saw []memory.ReferenceSpec
	fake := &memory.Fake{
		AttachFn: func(ctx context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
			saw = opts.ReferenceSpecs
			return memory.AttachResult{Provider: "fake", StoreID: opts.SpaceID, Name: "default", Owned: true, MCPConfigWritten: true}, nil
		},
	}
	svc.Memory = fake
	if err := svc.Create(context.Background(), CreateOptions{
		ID:         "refoff",
		References: []RepoSpec{{Name: "repo-b", Ref: "main"}},
		Memories:   []string{"."},
	}); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 0 {
		t.Fatalf("vault off should skip refs: %#v", saw)
	}
}

// F5 / plan §3.6: space add passes an added reference repo through the
// ReferenceLinker seam when memory is already attached — and only then.
func TestAddRepoLinksMemoryOnAdd(t *testing.T) {
	svc, _, _ := testService(t)
	fake := &memory.Fake{}
	svc.Memory = fake
	var out strings.Builder
	svc.Out = &out
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "addlink"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("addlink")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "den-1", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	// Reference add with LinkMemory → one LinkReference call with the den id
	// and the repo's raw url spec.
	if err := svc.AddRepo(context.Background(), AddOptions{
		SpaceID: "addlink", RepoName: "repo-b", Mode: ModeReference, NoFetch: true, LinkMemory: true,
	}); err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, c := range fake.Calls {
		if strings.Contains(c, "link-reference den-1 repo-b file:///repo-b") {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("calls = %#v", fake.Calls)
	}
	if !strings.Contains(out.String(), "reference repo-b → w/repo-b (warren-url)") {
		t.Fatalf("output missing link line:\n%s", out.String())
	}

	// Edit adds and LinkMemory=false adds never touch the seam.
	before := len(fake.Calls)
	if err := svc.AddRepo(context.Background(), AddOptions{
		SpaceID: "addlink", RepoName: "repo-a", Mode: ModeEdit, NoFetch: true, LinkMemory: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRepo(context.Background(), AddOptions{
		SpaceID: "addlink", RepoName: "repo-a", Mode: ModeReference, NoFetch: true, LinkMemory: false,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != before {
		t.Fatalf("edit/LinkMemory=false must not link: %#v", fake.Calls[before:])
	}
}

// F5: repos.<name>.marmotVault "off" suppresses space-add linking, and a
// LinkReference failure is soft (notice, repo stays added).
func TestAddRepoLinkMemoryOffAndSoftFailure(t *testing.T) {
	svc, _, cfg := testService(t)
	rb := cfg.Repos["repo-b"]
	rb.MarmotVault = "off"
	cfg.Repos["repo-b"] = rb
	svc.Config = cfg
	fake := &memory.Fake{
		LinkReferenceFn: func(ctx context.Context, opts memory.LinkReferenceOptions) (memory.LinkReferenceResult, error) {
			return memory.LinkReferenceResult{}, errors.New("resolver exploded")
		},
	}
	svc.Memory = fake
	var out strings.Builder
	svc.Out = &out
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "addoff"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("addoff")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "den-1", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}

	// off → no seam call at all.
	if err := svc.AddRepo(context.Background(), AddOptions{
		SpaceID: "addoff", RepoName: "repo-b", Mode: ModeReference, NoFetch: true, LinkMemory: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c, "link-reference") {
			t.Fatalf("marmotVault off must skip linking: %#v", fake.Calls)
		}
	}

	// Failure → notice, AddRepo still succeeds and the repo is in the manifest.
	if err := svc.AddRepo(context.Background(), AddOptions{
		SpaceID: "addoff", RepoName: "repo-a", Mode: ModeReference, NoFetch: true, LinkMemory: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "notice: memory default link failed: resolver exploded; reference repo-a added without a memory link") {
		t.Fatalf("output missing soft-failure notice:\n%s", out.String())
	}
	manifest, err := LoadManifest(spacePath)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.HasPath(filepath.Join("references", "repo-a")) {
		t.Fatalf("repo must stay added on link failure: %#v", manifest.Repos)
	}
}

// F22: memory sync re-reports skew after a successful provider sync — the
// service fetches Status and prints the compact state suffix line.
func TestSyncMemorySkewReReport(t *testing.T) {
	svc, _, _ := testService(t)
	var order []string
	fake := &memory.Fake{
		SyncFn: func(ctx context.Context, opts memory.SyncOptions) (memory.SyncResult, error) {
			order = append(order, "sync")
			return memory.SyncResult{Summary: "synced 1 warren(s)"}, nil
		},
		StatusFn: func(ctx context.Context, opts memory.StatusOptions) (memory.StatusResult, error) {
			order = append(order, "status")
			return memory.StatusResult{Links: []memory.LinkStatus{{State: "unpushed", PendingEdits: 2}}}, nil
		},
	}
	svc.Memory = fake
	var out strings.Builder
	svc.Out = &out
	if err := svc.InitSpace(context.Background(), InitOptions{ID: "skewsync"}); err != nil {
		t.Fatal(err)
	}
	spacePath := svc.SpacePath("skewsync")
	manifest, _ := LoadManifest(spacePath)
	manifest.Memories = []MemoryManifest{{Name: "default", Provider: "fake", ID: "den-1", Owned: true}}
	if err := SaveManifest(spacePath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncMemory(context.Background(), "skewsync", "default", false); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "sync" || order[1] != "status" {
		t.Fatalf("order = %v (status must run AFTER sync)", order)
	}
	if !strings.Contains(out.String(), "synced 1 warren(s)") || !strings.Contains(out.String(), "default (2 unpushed)") {
		t.Fatalf("output missing skew re-report:\n%s", out.String())
	}

	// Clean state renders (ok); dry-run skips the re-report entirely.
	fake.StatusFn = func(ctx context.Context, opts memory.StatusOptions) (memory.StatusResult, error) {
		order = append(order, "status")
		return memory.StatusResult{}, nil
	}
	out.Reset()
	if err := svc.SyncMemory(context.Background(), "skewsync", "default", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "default (ok)") {
		t.Fatalf("output missing (ok) re-report:\n%s", out.String())
	}
	order = nil
	if err := svc.SyncMemory(context.Background(), "skewsync", "default", true); err != nil {
		t.Fatal(err)
	}
	for _, o := range order {
		if o == "status" {
			t.Fatalf("dry-run must not probe status: %v", order)
		}
	}
}
