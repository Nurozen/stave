package memory

// S4 tests: capability-probed pass-through of --edit/--link/--ref/--opt,
// den status skew intelligence, and real warren sync parsing.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const newDenUsage = "usage: marmot den <command> [flags]\n" +
	"  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--ref name=<n>,url=<u>,path=<p>,ref=<r>]... [--no-pointer] [--json]\n" +
	"  link    <den-id> (--edit <warren-id>/<project-id> | --link <target>) [--json]\n"

const oldDenUsage = "usage: marmot den <command> [flags]\n" +
	"  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--no-pointer] [--no-vault] [--dry-run] [--json]\n" +
	"  status  [<den-id>] [--json]\n"

// scriptedMarmot fakes the binary via the Command hook: each spawned argv is
// recorded, and the first matching response key (mutually exclusive
// substrings) selects canned stdout. Unmatched argv exits 1.
func scriptedMarmot(t *testing.T, responses map[string]string, calls *[]string) *Marmot {
	t.Helper()
	return &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			joined := strings.Join(args, " ")
			*calls = append(*calls, joined)
			for key, out := range responses {
				if strings.Contains(joined, key) {
					return exec.CommandContext(ctx, "sh", "-c", "printf '%s' "+shellQuote(out))
				}
			}
			return exec.CommandContext(ctx, "sh", "-c", "echo unhandled >&2; exit 1")
		},
	}
}

func TestDenCreateArgsPassthrough(t *testing.T) {
	args := DenCreateArgs("id", "/proj", "", []string{"w/p"}, []string{"w/q"}, []ReferenceSpec{
		{Name: "billing", URL: "https://example.com/billing.git", Ref: "main"},
		{Name: "forced", URL: "https://example.com/forced.git", MarmotVault: "vault-1"},
	}, map[string]string{"no-vault": "true", "embedding-provider": "mock"}, true)
	joined := strings.Join(args, " ")
	if !containsAll(args, "den", "create", "id", "--lifetime", "task", "--project", "/proj", "--no-pointer", "--json") {
		t.Fatalf("args = %v", args)
	}
	if !strings.Contains(joined, "--ref name=billing,url=https://example.com/billing.git,ref=main") {
		t.Fatalf("missing --ref spec: %v", args)
	}
	// marmotVault-forced specs bypass resolution (direct den link instead).
	if strings.Contains(joined, "forced") {
		t.Fatalf("marmotVault-forced spec must not ride --ref: %v", args)
	}
	// Provider opts become den create flags (true → bare flag), sorted.
	if !strings.Contains(joined, "--embedding-provider mock") || !containsAll(args, "--no-vault") {
		t.Fatalf("opts not passed through: %v", args)
	}
	// --edit/--link are den link verbs, never den create flags.
	for _, ban := range []string{"--edit", "--link"} {
		if strings.Contains(joined, ban) {
			t.Fatalf("%q must not appear on den create: %v", ban, args)
		}
	}
	if args[len(args)-1] != "--json" {
		t.Fatalf("--json must be last: %v", args)
	}
}

func TestDenLinkArgs(t *testing.T) {
	edit := strings.Join(DenLinkArgs("t1", "w/p", true), " ")
	if edit != "den link t1 --edit w/p --json" {
		t.Fatalf("edit args = %q", edit)
	}
	link := strings.Join(DenLinkArgs("t1", "vault-1", false), " ")
	if link != "den link t1 --link vault-1 --json" {
		t.Fatalf("link args = %q", link)
	}
}

func TestSupportsRefPassthroughProbeAndCache(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{"den --help": newDenUsage}, &calls)
	if !m.SupportsRefPassthrough(context.Background()) {
		t.Fatal("new usage must probe true")
	}
	if !m.SupportsRefPassthrough(context.Background()) {
		t.Fatal("cached probe changed")
	}
	if len(calls) != 1 {
		t.Fatalf("probe must be cached per instance: %v", calls)
	}

	var oldCalls []string
	old := scriptedMarmot(t, map[string]string{"den --help": oldDenUsage}, &oldCalls)
	if old.SupportsRefPassthrough(context.Background()) {
		t.Fatal("old usage must probe false")
	}
}

func TestSupportsWarrenSyncProbe(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{"warren --help": "usage: marmot warren <init|add|sync|propose>"}, &calls)
	if !m.SupportsWarrenSync(context.Background()) {
		t.Fatal("new warren usage must probe true")
	}
	var oldCalls []string
	old := scriptedMarmot(t, map[string]string{"warren --help": "usage: marmot warren <init|project|register|propose>"}, &oldCalls)
	if old.SupportsWarrenSync(context.Background()) {
		t.Fatal("old warren usage must probe false")
	}
}

func TestAttachPassthroughRefsAndLinks(t *testing.T) {
	createEnv := `{"schema":1,"den_id":"t1","den_path":"/tmp/dens/t1","pointer_written":false,` +
		`"links":[{"ref":"w/proj","mode":"link","resolved_via":"warren-url"},{"ref":"lost","mode":null,"resolved_via":"none"}],` +
		`"warnings":["ref lost: no warren source_url or checkout vault_id match; skipped"]}`
	linkEnv := `{"schema":1,"den_id":"t1","link":{"target":"w/docs","mode":"edit"},"warnings":[]}`
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"den --help": newDenUsage,
		"den create": createEnv,
		"den link":   linkEnv,
	}, &calls)
	dir := t.TempDir()
	var out strings.Builder
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:   "t1",
		SpacePath: dir,
		EditRefs:  []string{"w/docs"},
		ReferenceSpecs: []ReferenceSpec{
			{Name: "billing", URL: "https://example.com/billing.git"},
			{Name: "lost", URL: "https://example.com/lost.git"},
			{Name: "forced", URL: "https://example.com/forced.git", MarmotVault: "vault-1"},
		},
		Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	// den create argv carries the two resolvable refs, not the forced one.
	var createArgs string
	for _, c := range calls {
		if strings.Contains(c, "den create") {
			createArgs = c
		}
	}
	if !strings.Contains(createArgs, "--ref name=billing,url=https://example.com/billing.git") ||
		!strings.Contains(createArgs, "--ref name=lost,url=https://example.com/lost.git") {
		t.Fatalf("den create argv missing --ref specs: %q", createArgs)
	}
	if strings.Contains(createArgs, "forced") {
		t.Fatalf("forced spec must not be a --ref: %q", createArgs)
	}
	// den link runs for --edit AND for the marmotVault-forced reference.
	var linkCalls []string
	for _, c := range calls {
		if strings.Contains(c, "den link") {
			linkCalls = append(linkCalls, c)
		}
	}
	if len(linkCalls) != 2 ||
		!strings.Contains(linkCalls[0], "den link t1 --edit w/docs --json") ||
		!strings.Contains(linkCalls[1], "den link t1 --link vault-1 --json") {
		t.Fatalf("den link calls = %v", linkCalls)
	}
	// Per-reference resolution reporting.
	rendered := out.String()
	for _, need := range []string{
		"reference billing → w/proj (warren-url)",
		"reference lost → no memory found",
		"reference forced → vault-1 (config marmotVault)",
	} {
		if !strings.Contains(rendered, need) {
			t.Fatalf("output missing %q:\n%s", need, rendered)
		}
	}
	// Parsed links: 2 from create + 2 from den link calls.
	if len(res.Links) != 4 {
		t.Fatalf("links = %#v", res.Links)
	}
	if res.Links[0].ResolvedVia != "warren-url" || res.Links[1].ResolvedVia != "none" ||
		res.Links[2].Mode != "edit" || res.Links[2].ResolvedVia != "explicit" {
		t.Fatalf("links = %#v", res.Links)
	}
}

func TestAttachOldMarmotDropsFlagsWithNotice(t *testing.T) {
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"den --help": oldDenUsage,
		"den create": okEnv("t1"),
	}, &calls)
	dir := t.TempDir()
	var out strings.Builder
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:        "t1",
		SpacePath:      dir,
		EditRefs:       []string{"w/docs"},
		ReferenceSpecs: []ReferenceSpec{{Name: "billing", URL: "https://example.com/billing.git"}},
		Opts:           map[string]string{"no-vault": "true"},
		Out:            &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.Contains(c, "--ref") || strings.Contains(c, "--edit") || strings.Contains(c, "--no-vault") {
			t.Fatalf("old marmot must get the S2 argv: %q", c)
		}
		if strings.Contains(c, "den link") {
			t.Fatalf("old marmot must not receive den link: %q", c)
		}
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "dropping --edit/--link/--ref/--opt") {
			found = true
		}
	}
	if !found || !strings.Contains(out.String(), "notice:") {
		t.Fatalf("drop notice missing: warnings=%#v out=%s", res.Warnings, out.String())
	}
	if res.StoreID != "t1" || !res.MCPConfigWritten {
		t.Fatalf("S2 attach must still succeed: %#v", res)
	}
}

// Attach-existing (UseID) never carries S4 content — the drop must be
// noticed on this path too, with probe-gated wording: "predates" for an old
// marmot, the attach-existing explanation otherwise.
func TestAttachUseIDDropsS4WithNotice(t *testing.T) {
	for name, tc := range map[string]struct {
		denUsage string
		want     string
	}{
		"old-marmot": {oldDenUsage, "installed marmot predates den link/--ref support"},
		"new-marmot": {newDenUsage, "attach-existing reuses den shared-den as-is"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			m := scriptedMarmot(t, map[string]string{
				"den --help": tc.denUsage,
				"den status": okEnv("shared-den"),
				"route add":  `{"schema":1,"warnings":[]}`,
			}, &calls)
			var out strings.Builder
			res, err := m.Attach(context.Background(), AttachOptions{
				SpaceID:        "s",
				SpacePath:      t.TempDir(),
				UseID:          "shared-den",
				EditRefs:       []string{"w/docs"},
				ReferenceSpecs: []ReferenceSpec{{Name: "billing", URL: "https://example.com/billing.git"}},
				Out:            &out,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range calls {
				if strings.Contains(c, "--ref") || strings.Contains(c, "--edit") || strings.Contains(c, "den link") || strings.Contains(c, "den create") {
					t.Fatalf("attach-existing must not pass S4 argv: %q", c)
				}
			}
			rendered := out.String()
			if !strings.Contains(rendered, "notice: attached memory without --edit/--link/--ref") || !strings.Contains(rendered, tc.want) {
				t.Fatalf("drop notice missing %q:\n%s", tc.want, rendered)
			}
			found := false
			for _, w := range res.Warnings {
				if strings.Contains(w, "attached memory without --edit/--link/--ref") {
					found = true
				}
			}
			if !found {
				t.Fatalf("warnings = %#v", res.Warnings)
			}
			if res.StoreID != "shared-den" || res.Owned || !res.MCPConfigWritten {
				t.Fatalf("attach-existing must still succeed: %#v", res)
			}
		})
	}
}

func TestAttachDryRunWithS4FlagsPrintsPlan(t *testing.T) {
	m := &Marmot{
		Binary: "marmot",
		LookPath: func(string) (string, error) {
			t.Fatal("dry-run must not look up binary")
			return "", nil
		},
	}
	var buf strings.Builder
	res, err := m.Attach(context.Background(), AttachOptions{
		SpaceID:        "t1",
		SpacePath:      "/tmp/t1",
		EditRefs:       []string{"w/docs"},
		LinkRefs:       []string{"w/billing"},
		ReferenceSpecs: []ReferenceSpec{{Name: "r", URL: "https://example.com/r.git"}},
		DryRun:         true,
		Out:            &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	// den create + two den link lines.
	if len(res.DryRunCommands) != 3 {
		t.Fatalf("DryRunCommands = %#v", res.DryRunCommands)
	}
	if !strings.Contains(res.DryRunCommands[0], "--ref name=r,url=https://example.com/r.git") {
		t.Fatalf("create line: %q", res.DryRunCommands[0])
	}
	if !strings.Contains(res.DryRunCommands[1], "--edit w/docs") || !strings.Contains(res.DryRunCommands[2], "--link w/billing") {
		t.Fatalf("link lines: %#v", res.DryRunCommands)
	}
}

func TestStatusParsesDenStatusFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "contracts", "den_status.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	m := scriptedMarmot(t, map[string]string{"den status": string(data)}, &calls)
	st, err := m.Status(context.Background(), StatusOptions{StoreID: "myproject"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Lifetime != "durable" || len(st.Links) != 3 {
		t.Fatalf("status = %#v", st)
	}
	edit := st.Links[0]
	if edit.Mode != "edit" || edit.Ahead != 4 || edit.PendingEdits != 5 || edit.State != "unpushed" {
		t.Fatalf("edit link = %#v", edit)
	}
	pinned := st.Links[1]
	if pinned.Mode != "link" || pinned.Behind != 3 || pinned.State != "stale" ||
		!strings.HasPrefix(pinned.PinnedCommit, "abc1234") || !strings.HasPrefix(pinned.SourceCommit, "77aa88b") {
		t.Fatalf("pinned link = %#v", pinned)
	}
	for _, need := range []string{
		"den myproject (durable)",
		"platform-warren/docs  edit  ahead 4 / behind 0 / 5 pending edits (unpushed)",
		"platform-warren/billing  link  pinned abc1234  behind 3 (stale)",
		"[vault snapshot from source commit 77aa88b]",
		"auth-service-den  live (ok)",
	} {
		if !strings.Contains(st.Summary, need) {
			t.Fatalf("summary missing %q:\n%s", need, st.Summary)
		}
	}
	if st.StateSuffix() != " (5 unpushed)" {
		t.Fatalf("suffix = %q", st.StateSuffix())
	}
}

func TestStatusOldEnvelopeDegradesGracefully(t *testing.T) {
	// Old marmot den status (no links/lifetime): parse succeeds, no rows.
	var calls []string
	m := scriptedMarmot(t, map[string]string{"den status": okEnv("sid")}, &calls)
	st, err := m.Status(context.Background(), StatusOptions{StoreID: "sid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Links) != 0 || st.StateSuffix() != "" {
		t.Fatalf("status = %#v", st)
	}
	if !strings.Contains(st.Summary, "links: none") {
		t.Fatalf("summary = %q", st.Summary)
	}
}

func TestStateSuffixVocabulary(t *testing.T) {
	if s := (StatusResult{Links: []LinkStatus{{State: "ok"}}}).StateSuffix(); s != "" {
		t.Fatalf("ok suffix = %q", s)
	}
	if s := (StatusResult{Links: []LinkStatus{{State: "stale"}}}).StateSuffix(); s != " (stale)" {
		t.Fatalf("stale suffix = %q", s)
	}
	if s := (StatusResult{Links: []LinkStatus{{State: "unreachable"}}}).StateSuffix(); s != " (unreachable)" {
		t.Fatalf("unreachable suffix = %q", s)
	}
	// Pending edits dominate staleness.
	if s := (StatusResult{Links: []LinkStatus{{State: "stale"}, {State: "unpushed", PendingEdits: 2}}}).StateSuffix(); s != " (2 unpushed)" {
		t.Fatalf("mixed suffix = %q", s)
	}
}

func TestSyncParsesWarrenSyncFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "contracts", "warren_sync.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	m := scriptedMarmot(t, map[string]string{
		"warren --help": "usage: marmot warren <init|add|sync|propose>",
		"warren sync":   string(data),
	}, &calls)
	var out strings.Builder
	res, err := m.Sync(context.Background(), SyncOptions{StoreID: "s", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warrens) != 3 {
		t.Fatalf("warrens = %#v", res.Warrens)
	}
	rendered := out.String()
	for _, need := range []string{
		"synced product-platform (updated 0123456 → 89abcde)",
		"synced docs-warren (up to date at fedcba9)",
		"warren ghost-warren failed:",
	} {
		if !strings.Contains(rendered, need) {
			t.Fatalf("sync rendering missing %q:\n%s", need, rendered)
		}
	}
	if res.Summary != "synced 2/3 warrens (1 failed)" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("per-warren failure must surface as a warning")
	}
}

func TestSyncAllWarrensFailedIsError(t *testing.T) {
	// marmot exits 1 with a valid envelope when every warren failed; stave's
	// memory sync must mirror the nonzero exit.
	env := `{"schema":1,"warrens":[{"id":"a","fetched":false,"previous_commit":"","pinned_commit":"","updated":false,"error":"boom"}],"warnings":[]}`
	var calls []string
	m := &Marmot{
		Binary:   "marmot",
		LookPath: func(string) (string, error) { return "marmot", nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			joined := strings.Join(args, " ")
			calls = append(calls, joined)
			if strings.Contains(joined, "warren --help") {
				return exec.CommandContext(ctx, "sh", "-c", "printf '%s' 'usage: marmot warren <add|sync>' >&2; exit 1")
			}
			if strings.Contains(joined, "warren sync") {
				return exec.CommandContext(ctx, "sh", "-c", "printf '%s' "+shellQuote(env)+"; exit 1")
			}
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		},
	}
	res, err := m.Sync(context.Background(), SyncOptions{StoreID: "s"})
	if err == nil {
		t.Fatalf("all-failed sync must error: %#v", res)
	}
	if !strings.Contains(err.Error(), "all 1 warrens failed") {
		t.Fatalf("err = %v", err)
	}
	if len(res.Warrens) != 1 {
		t.Fatalf("parsed warrens must survive the error: %#v", res)
	}
}
