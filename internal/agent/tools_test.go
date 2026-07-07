package agent

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
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
		ToolSummon,
		ToolAsk,
		ToolPortalList,
		ToolPortalStatus,
		ToolPortalDoctor,
		ToolPortalInspect,
		ToolPortalAuthStatus,
		ToolPortalLogs,
		ToolPortalInit,
		ToolPortalAttach,
		ToolPortalConfigure,
		ToolPortalAuthLogin,
		ToolPortalAuthInherit,
		ToolPortalAuthRevoke,
		ToolPortalUp,
		ToolPortalSync,
		ToolPortalSummon,
		ToolPortalDown,
		ToolPortalDetach,
		ToolPortalDestroyPreview,
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
	if seen[ToolSummon].Category != ToolCategoryMutate {
		t.Fatalf("summon category = %s", seen[ToolSummon].Category)
	}
	if seen[ToolAsk].Category != ToolCategoryControl {
		t.Fatalf("ask category = %s", seen[ToolAsk].Category)
	}
	if seen[ToolPortalDestroyPreview].Category != ToolCategoryRead {
		t.Fatalf("destroy preview category = %s", seen[ToolPortalDestroyPreview].Category)
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
	if run.Status != RunStatusPlanReady || run.Message != "create workspace" {
		t.Fatalf("run status/message = %#v", run)
	}
	if len(run.Plan.Operations) != 1 || run.Commands[0] != "stave space create ex-2 -e api" {
		t.Fatalf("run = %#v", run)
	}
}

func TestToolDispatcherQueuesAdditionalWorkspaceOperations(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	calls := []struct {
		name string
		args map[string]any
		op   string
	}{
		{ToolReposSync, map[string]any{"repo": "api"}, OpReposSync},
		{ToolSpaceSync, map[string]any{"space_id": "ex-1", "references_only": true}, OpSpaceSync},
		{ToolSpaceAdd, map[string]any{"space_id": "ex-1", "repo": "api", "mode": "reference", "ref": "main", "no_fetch": true}, OpSpaceAdd},
	}
	for _, tc := range calls {
		result := dispatcher.Dispatch(context.Background(), toolCall(tc.name, tc.args))
		if result.Error {
			t.Fatalf("%s result = %#v", tc.name, result)
		}
		last := dispatcher.Session.Plan.Operations[len(dispatcher.Session.Plan.Operations)-1]
		if last.Type != tc.op {
			t.Fatalf("%s operations = %#v", tc.name, dispatcher.Session.Plan.Operations)
		}
	}
	result := dispatcher.Dispatch(context.Background(), toolCall(ToolAsk, map[string]any{
		"message":   "later",
		"questions": []map[string]any{{"id": "q", "prompt": "Question?", "type": "text"}},
	}))
	if !result.Error || !strings.Contains(result.Summary, "cannot be mixed") {
		t.Fatalf("ask after operations result = %#v", result)
	}
}

func TestToolDispatcherAskNeedsInput(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolAsk, map[string]any{
		"message": "Need a portal target.",
		"questions": []map[string]any{{
			"id":      "driver",
			"prompt":  "Which portal driver should Stave use?",
			"type":    "select",
			"options": []string{"docker", "ssh"},
		}},
	}))
	if result.Error {
		t.Fatalf("ask result = %#v", result)
	}
	run := dispatcher.Session.RunResult()
	if !dispatcher.Session.Finished || run.Status != RunStatusNeedsInput || run.Message != "Need a portal target." {
		t.Fatalf("run = %#v", run)
	}
	if len(run.Questions) != 1 || len(run.Plan.Operations) != 0 || len(run.Commands) != 0 {
		t.Fatalf("run = %#v", run)
	}
}

func TestToolDispatcherQueuesSummonAfterCreate(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolSpaceCreate, map[string]any{
		"space_id": "ex-2",
		"edits":    []map[string]any{{"name": "api"}},
	}))
	if result.Error {
		t.Fatalf("space create result = %#v", result)
	}
	result = dispatcher.Dispatch(context.Background(), toolCall(ToolSummon, map[string]any{
		"space_id": "ex-2",
		"summoner": "codex",
	}))
	if result.Error {
		t.Fatalf("summon result = %#v", result)
	}
	run := dispatcher.Session.RunResult()
	if len(run.Commands) != 2 || run.Commands[1] != "stave summon ex-2 --with codex" {
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

	result = dispatcher.Dispatch(context.Background(), toolCall(ToolPortalInit, map[string]any{
		"space_id": "ex-1",
		"driver":   "docker",
		"surprise": "nope",
	}))
	if !result.Error {
		t.Fatalf("unknown portal arg was accepted: %#v", result)
	}
}

func TestToolDispatcherPortalTools(t *testing.T) {
	cfg := testConfig(t)
	saveTestPortal(t, cfg, "ex-1", "existing", "docker")
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolPortalInit, map[string]any{
		"space_id":       "ex-1",
		"portal_id":      "dev",
		"driver":         "docker",
		"image":          "ubuntu:latest",
		"container_root": "/workspace/ex-1",
	}))
	if result.Error {
		t.Fatalf("portal init result = %#v", result)
	}
	run := dispatcher.Session.RunResult()
	if len(run.Plan.Operations) != 1 || run.Commands[0] != "stave portal init container ex-1 dev --image ubuntu:latest --container-root /workspace/ex-1" {
		t.Fatalf("run = %#v", run)
	}

	result = dispatcher.Dispatch(context.Background(), toolCall(ToolPortalDestroyPreview, map[string]any{
		"space_id":  "ex-1",
		"portal_id": "existing",
	}))
	if result.Error {
		t.Fatalf("destroy preview result = %#v", result)
	}
	if len(dispatcher.Session.Plan.Operations) != 1 || len(dispatcher.Session.ReadResults) != 1 {
		t.Fatalf("session = %#v", dispatcher.Session)
	}
}

func TestToolDispatcherPortalReadPayloads(t *testing.T) {
	cfg := testConfig(t)
	saveTestPortal(t, cfg, "ex-1", "dev", portal.DriverDocker)
	saveTestPortal(t, cfg, "ex-1", "ssh-dev", portal.DriverSSH)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	calls := []struct {
		name string
		args map[string]any
		key  string
	}{
		{ToolPortalList, map[string]any{"space_id": "ex-1"}, "portals"},
		{ToolPortalStatus, map[string]any{"space_id": "ex-1", "portal_id": "dev"}, "status"},
		{ToolPortalDoctor, map[string]any{"space_id": "ex-1", "portal_id": "dev"}, "doctor"},
		{ToolPortalInspect, map[string]any{"space_id": "ex-1", "portal_id": "dev"}, "inspect"},
		{ToolPortalAuthStatus, map[string]any{"space_id": "ex-1", "portal_id": "dev", "provider": "codex"}, "auth"},
		{ToolPortalLogs, map[string]any{"space_id": "ex-1", "portal_id": "ssh-dev", "agent": "codex", "tail": 25}, "commands"},
		{ToolPortalDestroyPreview, map[string]any{"space_id": "ex-1", "portal_id": "dev"}, "destroyPreview"},
	}
	for _, tc := range calls {
		result := dispatcher.Dispatch(context.Background(), toolCall(tc.name, tc.args))
		if result.Error {
			t.Fatalf("%s result = %#v", tc.name, result)
		}
		payload, ok := result.Payload.(map[string]any)
		if !ok {
			t.Fatalf("%s payload type = %T", tc.name, result.Payload)
		}
		if _, ok := payload[tc.key]; !ok {
			t.Fatalf("%s payload missing %q: %#v", tc.name, tc.key, payload)
		}
	}
	if len(dispatcher.Session.ReadResults) != len(calls) {
		t.Fatalf("read results = %#v", dispatcher.Session.ReadResults)
	}

	authData, err := json.Marshal(dispatcher.Session.ReadResults[4].Payload.(map[string]any)["auth"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(authData), "codex") || strings.Contains(string(authData), "claude") {
		t.Fatalf("filtered auth = %s", authData)
	}
	if _, _, err := dispatcher.portalReadPayload(Operation{Type: OpPortalConfigure, SpaceID: "ex-1", PortalID: "dev"}); err == nil {
		t.Fatal("unsupported portal read payload succeeded")
	}
}

func TestToolDispatcherQueuesPortalMutationTools(t *testing.T) {
	cfg := testConfig(t)
	saveTestPortal(t, cfg, "ex-1", "local", portal.DriverDocker)
	saveTestPortal(t, cfg, "ex-1", "remote", portal.DriverSSH)
	tests := []struct {
		name string
		args map[string]any
		op   string
	}{
		{ToolPortalAttach, map[string]any{"space_id": "ex-1", "portal_id": "new-ssh", "driver": "ssh", "host": "host.test", "remote_root": "~/stave/ex-1"}, OpPortalAttach},
		{ToolPortalAttach, map[string]any{"space_id": "ex-1", "portal_id": "new-ec2", "driver": "ec2-attach", "instance_id": "i-123", "host": "203.0.113.10", "region": "us-west-2", "ssh_user": "ec2-user"}, OpPortalAttach},
		{ToolPortalConfigure, map[string]any{"space_id": "ex-1", "portal_id": "local", "host": "203.0.113.10", "agent": "cursor", "auth": "volume"}, OpPortalConfigure},
		{ToolPortalAuthLogin, map[string]any{"space_id": "ex-1", "portal_id": "local", "provider": "codex", "method": "device"}, OpPortalAuthLogin},
		{ToolPortalAuthInherit, map[string]any{"space_id": "ex-1", "portal_id": "local", "provider": "codex", "method": "env"}, OpPortalAuthInherit},
		{ToolPortalAuthRevoke, map[string]any{"space_id": "ex-1", "portal_id": "local", "provider": "codex", "target": "portal"}, OpPortalAuthRevoke},
		{ToolPortalUp, map[string]any{"space_id": "ex-1", "portal_id": "local", "attach": "none", "workdir": "/workspace/ex-1"}, OpPortalUp},
		{ToolPortalSync, map[string]any{"space_id": "ex-1", "portal_id": "local", "direction": "to", "mode": "rsync", "delete": true, "max_delete": 2}, OpPortalSync},
		{ToolPortalSummon, map[string]any{"space_id": "ex-1", "portal_id": "local", "summoner": "claude", "mode": "tmux", "permission": "workspace-write"}, OpPortalSummon},
		{ToolPortalDown, map[string]any{"space_id": "ex-1", "portal_id": "local", "timeout": 10, "force": true}, OpPortalDown},
		{ToolPortalDetach, map[string]any{"space_id": "ex-1", "portal_id": "remote"}, OpPortalDetach},
	}
	for _, tc := range tests {
		dispatcher := NewToolDispatcher(cfg, nil, nil)
		if tc.name == ToolPortalSummon {
			auth := dispatcher.Dispatch(context.Background(), toolCall(ToolPortalAuthLogin, map[string]any{"space_id": "ex-1", "portal_id": "local", "provider": "claude", "method": "native"}))
			if auth.Error {
				t.Fatalf("auth setup result = %#v", auth)
			}
		}
		result := dispatcher.Dispatch(context.Background(), toolCall(tc.name, tc.args))
		if result.Error {
			t.Fatalf("%s result = %#v", tc.name, result)
		}
		last := dispatcher.Session.Plan.Operations[len(dispatcher.Session.Plan.Operations)-1]
		if last.Type != tc.op {
			t.Fatalf("%s operations = %#v", tc.name, dispatcher.Session.Plan.Operations)
		}
		if host, ok := tc.args["host"].(string); ok && last.Host != host {
			t.Fatalf("%s host = %q, want %q", tc.name, last.Host, host)
		}
		if !strings.Contains(result.Summary, tc.op) {
			t.Fatalf("%s summary = %q", tc.name, result.Summary)
		}
	}
}

func TestToolDispatcherSpaceStatusPayload(t *testing.T) {
	cfg := testConfig(t)
	spacePath := filepath.Join(cfg.AgentWorkDir, "ex-1")
	if err := os.MkdirAll(filepath.Join(spacePath, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := space.SaveManifest(spacePath, space.Manifest{
		ID:       "ex-1",
		Kind:     "ticket",
		SpecPath: "spec",
		Repos: []space.RepoManifest{{
			Name:         "api",
			Mode:         space.ModeEdit,
			Path:         "api",
			Base:         "origin/main",
			Branch:       "stave/ex-1/api",
			BareRepoPath: cfg.Repos["api"].BareRepoPath,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewToolDispatcher(cfg, git.New(git.WithRunner(&executorGitRunner{})), nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolSpaceStatus, map[string]any{"space_id": "ex-1"}))
	if result.Error {
		t.Fatalf("space status result = %#v", result)
	}
	payload := result.Payload.(map[string]any)
	if payload["id"] != "ex-1" || !strings.HasSuffix(payload["specPath"].(string), filepath.Join("ex-1", "spec")) {
		t.Fatalf("payload = %#v", payload)
	}
	reposData, err := json.Marshal(payload["repos"])
	if err != nil {
		t.Fatal(err)
	}
	reposText := string(reposData)
	if !strings.Contains(reposText, `"name":"api"`) || !strings.Contains(reposText, `"ahead":2`) || !strings.Contains(reposText, `"behind":1`) {
		t.Fatalf("repos = %s", reposData)
	}
}

func TestToolDispatcherValidationAndEncodingBranches(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)

	result := dispatcher.Dispatch(context.Background(), toolCall(ToolPortalLogs, map[string]any{
		"space_id": "ex-1",
		"tail":     10,
		"follow":   true,
	}))
	if !result.Error || !strings.Contains(result.Summary, "follow mode") {
		t.Fatalf("follow logs result = %#v", result)
	}

	result = dispatcher.Dispatch(context.Background(), toolCall(ToolAsk, map[string]any{
		"message": "too many",
		"questions": []map[string]any{
			{"id": "a", "prompt": "a", "type": "text"},
			{"id": "b", "prompt": "b", "type": "text"},
			{"id": "c", "prompt": "c", "type": "text"},
			{"id": "d", "prompt": "d", "type": "text"},
		},
	}))
	if !result.Error {
		t.Fatalf("too many questions accepted: %#v", result)
	}

	raw := json.RawMessage(`{} {}`)
	var args reposSyncArgs
	if err := decodeToolArgs(raw, &args); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("decode multiple values err = %v", err)
	}

	bad := ToolResult{Name: "bad", Payload: math.Inf(1)}.OutputString()
	if !strings.Contains(bad, "unsupported value") {
		t.Fatalf("bad output string = %s", bad)
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
