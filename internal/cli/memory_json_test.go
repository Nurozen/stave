package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/memory"
)

// fakeMemoryHome sets up a fresh HOME with stave configured to use the
// registered "fake" memory provider and returns the shared Fake instance.
func fakeMemoryHome(t *testing.T) (*memory.Fake, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runCLI(t, "setup")
	cfg, path, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Memory.Provider = "fake"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	fake := &memory.Fake{}
	memory.Register("fake", func(config.MemoryConfig) memory.Provider { return fake })
	return fake, home
}

func decodeJSONArray(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	return rows
}

func attachmentsOf(t *testing.T, payload map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("payload has no %s array: %v", key, payload)
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s entry is not an object: %v", key, item)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestCLIMemoryAttachJSONEnvelope(t *testing.T) {
	fake, home := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	fake.AttachFn = func(_ context.Context, opts memory.AttachOptions) (memory.AttachResult, error) {
		return memory.AttachResult{
			Provider: "fake",
			StoreID:  opts.StoreID,
			Name:     opts.Name,
			Owned:    true,
			Links: []memory.AttachLink{
				{Ref: "w/docs", Reference: "docs", Mode: "link", ResolvedVia: "warren-url"},
				{Ref: "billing", Reference: "billing", ResolvedVia: "none"},
			},
		}, nil
	}

	out := runCLI(t, "memory", "attach", "demo", "--json")
	if strings.Contains(out, "attached memory") {
		t.Fatalf("json mode must not print prose:\n%s", out)
	}
	payload := decodeJSONObject(t, out)
	if payload["spaceId"] != "demo" || payload["spacePath"] != filepath.Join(home, "stave", "agent-work", "demo") {
		t.Fatalf("space identity: %v", payload)
	}
	if mems, _ := manifestOf(t, payload)["memories"].([]any); len(mems) != 1 {
		t.Fatalf("manifest must carry the attachment: %v", payload["manifest"])
	}
	attachments := attachmentsOf(t, payload, "attachments")
	if len(attachments) != 1 {
		t.Fatalf("attachments: %v", attachments)
	}
	row := attachments[0]
	if row["name"] != "default" || row["provider"] != "fake" || row["id"] != "demo" || row["owned"] != true {
		t.Fatalf("attachment row: %v", row)
	}
	linked := attachmentsOf(t, row, "linked")
	if len(linked) != 2 {
		t.Fatalf("linked: %v", linked)
	}
	if linked[0]["reference"] != "docs" || linked[0]["target"] != "w/docs" || linked[0]["kind"] != "link" {
		t.Fatalf("resolved link: %v", linked[0])
	}
	if linked[1]["reference"] != "billing" || linked[1]["kind"] != "unresolved" {
		t.Fatalf("unresolved link: %v", linked[1])
	}
	if _, has := linked[1]["target"]; has {
		t.Fatalf("unresolved link must omit target: %v", linked[1])
	}
	if _, has := payload["notes"]; has {
		t.Fatalf("no notices expected: %v", payload["notes"])
	}
}

func TestCLIMemoryAttachHumanOutputUnchanged(t *testing.T) {
	_, _ = fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	out := runCLI(t, "memory", "attach", "demo")
	if out != "attached memory default (fake:demo, owned=true) to demo\n" {
		t.Fatalf("human output changed:\n%q", out)
	}
}

func TestCLIMemoryAttachDryRunJSONPlan(t *testing.T) {
	_, _ = fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	payload := decodeJSONObject(t, runCLI(t, "memory", "attach", "demo", "--dry-run", "--json"))
	if payload["dryRun"] != true {
		t.Fatalf("dryRun flag: %v", payload)
	}
	plan, _ := payload["plan"].([]any)
	if len(plan) != 1 || !strings.Contains(plan[0].(string), "den create demo") {
		t.Fatalf("plan: %v", payload["plan"])
	}
}

func TestCLIMemoryAttachJSONErrorEnvelope(t *testing.T) {
	_, _ = fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	code, _ := jsonErrorCode(t, "memory", "attach", "demo", "--json")
	if code != "unknown" {
		t.Fatalf("duplicate attach code = %q", code)
	}
	code, _ = jsonErrorCode(t, "memory", "attach", "demo", "--opt", "novalue", "--json")
	if code != "invalid_arguments" {
		t.Fatalf("bad --opt code = %q", code)
	}
}

func TestCLIMemoryDetachJSON(t *testing.T) {
	_, home := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	runCLI(t, "memory", "attach", "demo", "--use", "shared-den")

	payload := decodeJSONObject(t, runCLI(t, "memory", "detach", "demo", "shared-den", "--destroy", "--json"))
	if payload["spaceId"] != "demo" || payload["spacePath"] != filepath.Join(home, "stave", "agent-work", "demo") {
		t.Fatalf("identity: %v", payload)
	}
	detached := attachmentsOf(t, payload, "detached")
	if len(detached) != 1 || detached[0]["name"] != "shared-den" || detached[0]["id"] != "shared-den" || detached[0]["owned"] != false {
		t.Fatalf("detached: %v", detached)
	}
	// Unowned stores are never destroyed: the fate that applied is keep.
	if detached[0]["fate"] != "keep" {
		t.Fatalf("fate: %v", detached[0])
	}
	notes, _ := payload["notes"].([]any)
	if len(notes) != 1 || !strings.Contains(notes[0].(string), "not owned") {
		t.Fatalf("notes should carry the not-owned notice: %v", payload["notes"])
	}
	if mems, _ := manifestOf(t, payload)["memories"].([]any); len(mems) != 1 {
		t.Fatalf("manifest must drop the attachment: %v", payload["manifest"])
	}

	payload = decodeJSONObject(t, runCLI(t, "memory", "detach", "demo", "--destroy", "--json"))
	detached = attachmentsOf(t, payload, "detached")
	if len(detached) != 1 || detached[0]["name"] != "default" || detached[0]["fate"] != "destroy" {
		t.Fatalf("owned destroy: %v", detached)
	}
	if _, has := payload["notes"]; has {
		t.Fatalf("no notes expected: %v", payload["notes"])
	}
	if _, has := manifestOf(t, payload)["memories"]; has {
		t.Fatalf("manifest must have no memories left: %v", payload["manifest"])
	}
}

func TestCLIMemoryDetachDryRunJSONAndErrors(t *testing.T) {
	fake, _ := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")

	payload := decodeJSONObject(t, runCLI(t, "memory", "detach", "demo", "--dry-run", "--json"))
	if payload["dryRun"] != true {
		t.Fatalf("dry-run payload: %v", payload)
	}
	plan, _ := payload["plan"].([]any)
	if len(plan) == 0 || !strings.Contains(plan[len(plan)-1].(string), "remove memory attachment") {
		t.Fatalf("plan: %v", payload["plan"])
	}

	code, _ := jsonErrorCode(t, "memory", "detach", "demo", "--keep", "--destroy", "--json")
	if code != "invalid_arguments" {
		t.Fatalf("keep+destroy code = %q", code)
	}

	fake.DetachFn = func(context.Context, memory.DetachOptions) (memory.DetachResult, error) {
		return memory.DetachResult{}, &memory.RefusalError{Provider: "marmot", Code: memory.CodeSourceInUse, Message: "den held by serve"}
	}
	code, _ = jsonErrorCode(t, "memory", "detach", "demo", "--json")
	if code != "memory_in_use" {
		t.Fatalf("source_in_use code = %q", code)
	}
}

func TestCLIMemoryListJSON(t *testing.T) {
	_, home := fakeMemoryHome(t)
	runCLI(t, "space", "init", "a")
	runCLI(t, "space", "init", "b")
	runCLI(t, "space", "init", "c")
	runCLI(t, "memory", "attach", "a")
	runCLI(t, "memory", "attach", "b", "--use", "shared")

	rows := decodeJSONArray(t, runCLI(t, "memory", "list", "--json"))
	if len(rows) != 2 || rows[0]["spaceId"] != "a" || rows[1]["spaceId"] != "b" {
		t.Fatalf("all-spaces rows: %v", rows)
	}
	if rows[0]["spacePath"] != filepath.Join(home, "stave", "agent-work", "a") {
		t.Fatalf("spacePath: %v", rows[0])
	}
	att := attachmentsOf(t, rows[1], "attachments")
	if len(att) != 1 || att[0]["name"] != "shared" || att[0]["provider"] != "fake" || att[0]["id"] != "shared" || att[0]["owned"] != false {
		t.Fatalf("b attachments: %v", att)
	}

	rows = decodeJSONArray(t, runCLI(t, "memory", "list", "c", "--json"))
	if len(rows) != 1 || rows[0]["spaceId"] != "c" || len(attachmentsOf(t, rows[0], "attachments")) != 0 {
		t.Fatalf("empty space row: %v", rows)
	}

	code, _ := jsonErrorCode(t, "memory", "list", "missing", "--json")
	if code != "space_not_found" {
		t.Fatalf("missing space code = %q", code)
	}
}

func TestCLIMemoryStatusJSON(t *testing.T) {
	fake, _ := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	runCLI(t, "memory", "attach", "demo", "--use", "broken")
	fake.StatusFn = func(_ context.Context, opts memory.StatusOptions) (memory.StatusResult, error) {
		if opts.StoreID == "broken" {
			return memory.StatusResult{}, errors.New("den missing")
		}
		return memory.StatusResult{
			StoreID:  opts.StoreID,
			Lifetime: "task",
			Summary:  "den demo (task)",
			Links: []memory.LinkStatus{
				{Ref: "w/docs", Mode: "edit", Ahead: 4, PendingEdits: 5, State: "unpushed"},
				{Ref: "w/billing", Mode: "link", Behind: 3, State: "stale"},
				{Ref: "auth", Mode: "live"},
			},
		}, nil
	}

	payload := decodeJSONObject(t, runCLI(t, "memory", "status", "demo", "--json"))
	if payload["spaceId"] != "demo" {
		t.Fatalf("spaceId: %v", payload)
	}
	rows := attachmentsOf(t, payload, "attachments")
	if len(rows) != 2 {
		t.Fatalf("rows: %v", rows)
	}
	first := rows[0]
	if first["name"] != "default" || first["state"] != "5 unpushed" || first["lifetime"] != "task" {
		t.Fatalf("header row: %v", first)
	}
	links := attachmentsOf(t, first, "links")
	if len(links) != 3 {
		t.Fatalf("links: %v", links)
	}
	if links[0]["alias"] != "w/docs" || links[0]["kind"] != "edit" || links[0]["ahead"] != float64(4) || links[0]["pending"] != float64(5) || links[0]["stale"] != false || links[0]["reachable"] != true {
		t.Fatalf("edit link: %v", links[0])
	}
	if links[1]["kind"] != "link" || links[1]["behind"] != float64(3) || links[1]["stale"] != true {
		t.Fatalf("stale link: %v", links[1])
	}
	if _, has := links[2]["stale"]; has {
		t.Fatalf("link without state must omit stale/reachable: %v", links[2])
	}
	second := rows[1]
	if second["name"] != "broken" || second["error"] != "den missing" || len(attachmentsOf(t, second, "links")) != 0 {
		t.Fatalf("failed row: %v", second)
	}

	// Alias filter narrows to one row.
	payload = decodeJSONObject(t, runCLI(t, "memory", "status", "demo", "broken", "--json"))
	if rows := attachmentsOf(t, payload, "attachments"); len(rows) != 1 || rows[0]["name"] != "broken" {
		t.Fatalf("alias filter: %v", rows)
	}
	code, _ := jsonErrorCode(t, "memory", "status", "demo", "nope", "--json")
	if code != "unknown" {
		t.Fatalf("unknown alias code = %q", code)
	}
}

func TestCLIMemoryProvidersJSON(t *testing.T) {
	_, _ = fakeMemoryHome(t)
	// No marmot binary reachable: the marmot row reports unavailable.
	t.Setenv("PATH", t.TempDir())
	rows := decodeJSONArray(t, runCLI(t, "memory", "providers", "--json"))
	byName := map[string]map[string]any{}
	for _, row := range rows {
		byName[row["name"].(string)] = row
	}
	fakeRow, ok := byName["fake"]
	if !ok {
		t.Fatalf("fake provider missing: %v", rows)
	}
	if fakeRow["available"] != true || fakeRow["default"] != true {
		t.Fatalf("fake row: %v", fakeRow)
	}
	caps, _ := fakeRow["capabilities"].([]any)
	if len(caps) != 3 || caps[0] != "dens" {
		t.Fatalf("fake capabilities: %v", fakeRow["capabilities"])
	}
	if _, has := fakeRow["error"]; has {
		t.Fatalf("fake row must not carry an error: %v", fakeRow)
	}
	marmotRow, ok := byName["marmot"]
	if !ok {
		t.Fatalf("marmot provider missing: %v", rows)
	}
	if marmotRow["available"] != false || marmotRow["default"] != false || marmotRow["binary"] != "marmot" {
		t.Fatalf("marmot row: %v", marmotRow)
	}
	if msg, _ := marmotRow["error"].(string); !strings.Contains(msg, "unavailable") {
		t.Fatalf("marmot error: %v", marmotRow)
	}
	if caps, _ := marmotRow["capabilities"].([]any); len(caps) != 0 {
		t.Fatalf("unavailable marmot must report no capabilities: %v", marmotRow)
	}
}

func TestCLIMemorySyncJSON(t *testing.T) {
	fake, _ := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	fake.SyncFn = func(context.Context, memory.SyncOptions) (memory.SyncResult, error) {
		return memory.SyncResult{
			Summary: "synced 2/3 warrens (1 failed)",
			Warrens: []memory.WarrenSync{
				{ID: "w1", Updated: true, PinnedCommit: "abc1234"},
				{ID: "w2", Updated: false, PinnedCommit: "def5678"},
				{ID: "w3", Error: "fetch failed"},
			},
		}, nil
	}

	payload := decodeJSONObject(t, runCLI(t, "memory", "sync", "demo", "--json"))
	if payload["spaceId"] != "demo" {
		t.Fatalf("spaceId: %v", payload)
	}
	results := attachmentsOf(t, payload, "results")
	if len(results) != 3 {
		t.Fatalf("results: %v", results)
	}
	want := []struct{ warren, outcome, detail string }{
		{"w1", "synced", "pinned abc1234"},
		{"w2", "up-to-date", "pinned def5678"},
		{"w3", "failed", "fetch failed"},
	}
	for i, w := range want {
		if results[i]["alias"] != "default" || results[i]["warren"] != w.warren || results[i]["outcome"] != w.outcome || results[i]["detail"] != w.detail {
			t.Fatalf("result %d: %v", i, results[i])
		}
	}
	// The post-sync skew re-report line rides along as a note.
	notes, _ := payload["notes"].([]any)
	if len(notes) != 1 || notes[0] != "default (ok)" {
		t.Fatalf("notes: %v", payload["notes"])
	}

	// Provider without warren data: one synced row carrying the summary.
	fake.SyncFn = nil
	payload = decodeJSONObject(t, runCLI(t, "memory", "sync", "demo", "--json"))
	results = attachmentsOf(t, payload, "results")
	if len(results) != 1 || results[0]["outcome"] != "synced" || results[0]["detail"] != "fake sync" {
		t.Fatalf("plain sync: %v", results)
	}
	if _, has := results[0]["warren"]; has {
		t.Fatalf("warren must be omitted: %v", results[0])
	}

	payload = decodeJSONObject(t, runCLI(t, "memory", "sync", "demo", "--dry-run", "--json"))
	if payload["dryRun"] != true {
		t.Fatalf("dry-run: %v", payload)
	}
}

func TestCLIMemoryProposeJSON(t *testing.T) {
	fake, _ := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	fake.ProposeFn = func(context.Context, memory.ProposeOptions) (memory.ProposeResult, error) {
		return memory.ProposeResult{
			Summary:     "contributed and proposed (no auto-push)",
			Branch:      "stave/demo",
			Commit:      "abc1234",
			PushCommand: "git -C /w push origin stave/demo",
			Contributed: &memory.ContributedCounts{Added: 2, Updated: 1},
			Warnings:    []string{"one warning"},
		}, nil
	}

	payload := decodeJSONObject(t, runCLI(t, "memory", "propose", "demo", "--json"))
	results := attachmentsOf(t, payload, "results")
	if len(results) != 1 {
		t.Fatalf("results: %v", results)
	}
	row := results[0]
	if row["alias"] != "default" || row["outcome"] != "proposed" || row["pushCommand"] != "git -C /w push origin stave/demo" || row["branch"] != "stave/demo" {
		t.Fatalf("propose row: %v", row)
	}
	if counts, _ := row["contributed"].(map[string]any); counts["added"] != float64(2) {
		t.Fatalf("contributed: %v", row["contributed"])
	}
	notes, _ := payload["notes"].([]any)
	joined := strings.Join(func() []string {
		s := make([]string, 0, len(notes))
		for _, n := range notes {
			s = append(s, n.(string))
		}
		return s
	}(), "\n")
	if !strings.Contains(joined, "warning: one warning") || !strings.Contains(joined, "contributed: 2 added") {
		t.Fatalf("notes: %v", payload["notes"])
	}

	fake.ProposeFn = func(_ context.Context, opts memory.ProposeOptions) (memory.ProposeResult, error) {
		if opts.DryRun {
			lines := []string{"marmot den contribute demo --json", "marmot warren propose --json"}
			for _, line := range lines {
				fmt.Fprintf(opts.Out, "dry-run: %s\n", line)
			}
			return memory.ProposeResult{Summary: "dry-run", DryRunCommands: lines}, nil
		}
		return memory.ProposeResult{Summary: "contributed and proposed (no auto-push)", NothingToPropose: true}, nil
	}
	payload = decodeJSONObject(t, runCLI(t, "memory", "propose", "demo", "--json"))
	results = attachmentsOf(t, payload, "results")
	if len(results) != 1 || results[0]["outcome"] != "up-to-date" || results[0]["detail"] != "nothing new to push" {
		t.Fatalf("nothing-to-propose row: %v", results)
	}

	payload = decodeJSONObject(t, runCLI(t, "memory", "propose", "demo", "--dry-run", "--json"))
	if payload["dryRun"] != true {
		t.Fatalf("dry-run: %v", payload)
	}
	plan, _ := payload["plan"].([]any)
	if len(plan) != 3 || !strings.Contains(plan[0].(string), "den contribute") || plan[2] != "dry-run" {
		t.Fatalf("propose dry-run plan should carry contribute + propose + summary: %v", plan)
	}
}

func TestCLIMemoryProposeJSONFailureEnvelope(t *testing.T) {
	fake, _ := fakeMemoryHome(t)
	runCLI(t, "space", "init", "demo")
	runCLI(t, "memory", "attach", "demo")
	fake.ProposeFn = func(context.Context, memory.ProposeOptions) (memory.ProposeResult, error) {
		return memory.ProposeResult{}, &memory.RefusalError{Provider: "marmot", Code: "edit_link_required", Message: "no edit link"}
	}
	code, _ := jsonErrorCode(t, "memory", "propose", "demo", "--json")
	if code != "unknown" {
		t.Fatalf("refusal code = %q", code)
	}
}
