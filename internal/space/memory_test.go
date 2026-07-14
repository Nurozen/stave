package space

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/memory"
)

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
	}
	if !found {
		t.Fatalf("expected detach on archive: %#v", fake.Calls)
	}
	_ = fg
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
	cfg.Repos["repo-b"] = cfg.Repos["repo-b"] // already present
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
