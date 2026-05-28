package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolDefinitions(t *testing.T) {
	defs := ToolDefinitions()
	want := []string{
		ToolReposList,
		ToolReposSync,
		ToolSpaceStatus,
		ToolSpaceSync,
		ToolSpaceCreate,
		ToolSpaceAdd,
		ToolExplainUnsupported,
		ToolFinish,
	}
	if len(defs) != len(want) {
		t.Fatalf("len(ToolDefinitions()) = %d, want %d", len(defs), len(want))
	}
	seen := map[string]ToolDefinition{}
	for _, def := range defs {
		seen[def.Name] = def
		if def.Description == "" {
			t.Fatalf("%s missing description", def.Name)
		}
		if def.Parameters["type"] != "object" {
			t.Fatalf("%s schema = %#v", def.Name, def.Parameters)
		}
	}
	for _, name := range want {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing tool %s", name)
		}
	}
	if seen[ToolReposList].Category != ToolCategoryRead {
		t.Fatalf("repos list category = %s", seen[ToolReposList].Category)
	}
	if seen[ToolSpaceCreate].Category != ToolCategoryMutate {
		t.Fatalf("space create category = %s", seen[ToolSpaceCreate].Category)
	}
}

func TestToolDispatcherQueuesOperationsAndFinishes(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolSpaceCreate, map[string]any{
		"space_id": "ex-2",
		"edits":    []map[string]any{{"name": "api"}},
	}))
	if result.Error {
		t.Fatalf("space create result = %#v", result)
	}
	result = dispatcher.Dispatch(context.Background(), toolCall(ToolFinish, map[string]any{
		"summary": "create workspace",
		"notes":   []string{"ready"},
	}))
	if result.Error {
		t.Fatalf("finish result = %#v", result)
	}

	run := dispatcher.Session.RunResult()
	if !dispatcher.Session.Finished || run.Plan.Summary != "create workspace" {
		t.Fatalf("session = %#v", dispatcher.Session)
	}
	if len(run.Plan.Operations) != 1 || run.Commands[0] != "stave space create ex-2 -e api" {
		t.Fatalf("run = %#v", run)
	}
}

func TestToolDispatcherReadAndUnsupportedTools(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolReposList, nil))
	if result.Error {
		t.Fatalf("repos list result = %#v", result)
	}
	if len(dispatcher.Session.ReadResults) != 1 {
		t.Fatalf("read results = %#v", dispatcher.Session.ReadResults)
	}

	result = dispatcher.Dispatch(context.Background(), toolCall(ToolExplainUnsupported, map[string]any{
		"request": "destroy ex-1",
		"reason":  "destructive operations are unsupported",
	}))
	if result.Error {
		t.Fatalf("unsupported result = %#v", result)
	}
	if len(dispatcher.Session.Plan.Notes) != 1 {
		t.Fatalf("notes = %#v", dispatcher.Session.Plan.Notes)
	}
}

func TestToolDispatcherRejectsInvalidArgs(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolSpaceCreate, map[string]any{
		"space_id": "ex-2",
		"edits":    []map[string]any{{"name": "missing"}},
	}))
	if !result.Error {
		t.Fatalf("unknown repo was accepted: %#v", result)
	}
}

func toolCall(name string, args map[string]any) ToolCall {
	raw := json.RawMessage(`{}`)
	if args != nil {
		data, _ := json.Marshal(args)
		raw = data
	}
	return ToolCall{ID: "call-1", Name: name, Arguments: raw}
}
