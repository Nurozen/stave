package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3/responses"
)

func TestOpenAIProviderToolLoop(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	var trace strings.Builder
	var calls int
	provider := OpenAIProvider{
		Model: "test-model",
		createResponse: func(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			if len(params.Tools) != len(ToolDefinitions()) {
				t.Fatalf("tools = %d, want %d", len(params.Tools), len(ToolDefinitions()))
			}
			if params.Temperature.Valid() {
				t.Fatalf("temperature should be omitted for OpenAI Responses requests")
			}
			if params.PreviousResponseID.Valid() {
				t.Fatalf("previous_response_id should be omitted for stateless OpenAI tool loops")
			}
			if calls > 1 {
				data, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "function_call_output") {
					t.Fatalf("follow-up request missing function_call_output in %s", data)
				}
			}
			switch calls {
			case 1:
				return openAIResponse(t, `{"id":"r1","output":[{"type":"function_call","call_id":"c1","name":"stave_repos_list","arguments":"{}"}]}`), nil
			case 2:
				return openAIResponse(t, `{"id":"r2","output":[{"type":"function_call","call_id":"c2","name":"stave_space_create","arguments":"{\"space_id\":\"ex-2\",\"edits\":[{\"name\":\"api\"}]}"}]}`), nil
			case 3:
				return openAIResponse(t, `{"id":"r3","output":[{"type":"function_call","call_id":"c3","name":"stave_finish","arguments":"{\"summary\":\"create ex-2\"}"}]}`), nil
			default:
				t.Fatalf("unexpected OpenAI call %d", calls)
				return nil, nil
			}
		},
	}

	result, err := provider.Run(context.Background(), ProviderRequest{Query: "create ex-2", Context: Context{}, Dispatcher: dispatcher, Trace: &trace})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Plan.Summary != "create ex-2" || len(result.Plan.Operations) != 1 || len(result.ReadResults) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	if got := trace.String(); !strings.Contains(got, "agent: thinking with openai") || !strings.Contains(got, "agent: tool stave_repos_list") {
		t.Fatalf("trace missing provider/tool details:\n%s", got)
	}
}

func TestOpenAIToolsEncodeRequiredAsArray(t *testing.T) {
	tools := openAITools(ToolDefinitions())
	if len(tools) == 0 {
		t.Fatal("openAITools returned no tools")
	}
	data, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	parameters, ok := encoded["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters encoded as %T in %s", encoded["parameters"], data)
	}
	required, ok := parameters["required"].([]any)
	if !ok {
		t.Fatalf("required encoded as %T in %s", parameters["required"], data)
	}
	if len(required) != 0 {
		t.Fatalf("required = %#v, want empty array", required)
	}
	if parameters["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v", parameters["additionalProperties"])
	}
}

func TestOpenAISchemaStrictCompatibility(t *testing.T) {
	var repoSync ToolDefinition
	var spaceCreate ToolDefinition
	for _, def := range ToolDefinitions() {
		switch def.Name {
		case ToolReposSync:
			repoSync = def
		case ToolSpaceCreate:
			spaceCreate = def
		}
	}

	repoSchema := openAISchema(repoSync.Parameters)
	if got := repoSchema["required"]; !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("repos sync required = %#v", got)
	}
	repoProperty := repoSchema["properties"].(map[string]any)["repo"].(map[string]any)
	if got := repoProperty["type"]; !reflect.DeepEqual(got, []any{"string", "null"}) {
		t.Fatalf("repo type = %#v", got)
	}

	createSchema := openAISchema(spaceCreate.Parameters)
	if got := createSchema["required"]; !reflect.DeepEqual(got, []string{"edits", "kind", "references", "space_id", "spec_path"}) {
		t.Fatalf("space create required = %#v", got)
	}
	edits := createSchema["properties"].(map[string]any)["edits"].(map[string]any)
	if got := edits["type"]; !reflect.DeepEqual(got, []any{"array", "null"}) {
		t.Fatalf("edits type = %#v", got)
	}
	item := edits["items"].(map[string]any)
	if got := item["required"]; !reflect.DeepEqual(got, []string{"name", "ref"}) {
		t.Fatalf("repo ref required = %#v", got)
	}
	ref := item["properties"].(map[string]any)["ref"].(map[string]any)
	if got := ref["type"]; !reflect.DeepEqual(got, []any{"string", "null"}) {
		t.Fatalf("repo ref type = %#v", got)
	}
}

func TestToolDefinitionsRequiredFieldsAreArrays(t *testing.T) {
	for _, def := range ToolDefinitions() {
		required, ok := def.Parameters["required"].([]string)
		if !ok {
			t.Fatalf("%s required has type %T", def.Name, def.Parameters["required"])
		}
		if required == nil {
			t.Fatalf("%s required is nil", def.Name)
		}
	}
	if got := ToolDefinitions()[0].Parameters["required"]; !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("repos list required = %#v", got)
	}
}

func TestAnthropicProviderToolLoop(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	var trace strings.Builder
	var calls int
	provider := AnthropicProvider{
		Model: "test-model",
		createMessage: func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
			calls++
			if len(params.Tools) != len(ToolDefinitions()) {
				t.Fatalf("tools = %d, want %d", len(params.Tools), len(ToolDefinitions()))
			}
			disableParallel := params.ToolChoice.GetDisableParallelToolUse()
			if disableParallel == nil || !*disableParallel {
				t.Fatalf("disable_parallel_tool_use = %#v, want true", disableParallel)
			}
			if params.Temperature.Valid() {
				t.Fatalf("temperature should be omitted for Anthropic Messages requests")
			}
			switch calls {
			case 1:
				return anthropicMessage(t, `{"id":"m1","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use","stop_sequence":null,"content":[{"type":"tool_use","id":"t1","name":"stave_repos_list","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			case 2:
				return anthropicMessage(t, `{"id":"m2","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use","stop_sequence":null,"content":[{"type":"tool_use","id":"t2","name":"stave_space_create","input":{"space_id":"ex-2","edits":[{"name":"api"}]}}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			case 3:
				return anthropicMessage(t, `{"id":"m3","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use","stop_sequence":null,"content":[{"type":"tool_use","id":"t3","name":"stave_finish","input":{"summary":"create ex-2"}}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			default:
				t.Fatalf("unexpected Anthropic call %d", calls)
				return nil, nil
			}
		},
	}

	result, err := provider.Run(context.Background(), ProviderRequest{Query: "create ex-2", Context: Context{}, Dispatcher: dispatcher, Trace: &trace})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Plan.Summary != "create ex-2" || len(result.Plan.Operations) != 1 || len(result.ReadResults) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := trace.String(); !strings.Contains(got, "agent: thinking with anthropic") || !strings.Contains(got, "agent: tool stave_repos_list") {
		t.Fatalf("trace missing provider/tool details:\n%s", got)
	}
}

func TestAnthropicToolsStrictSchemaEncoding(t *testing.T) {
	tools := anthropicTools(ToolDefinitions())
	if len(tools) != len(ToolDefinitions()) {
		t.Fatalf("tools = %d, want %d", len(tools), len(ToolDefinitions()))
	}
	for index, tool := range tools {
		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var encoded map[string]any
		if err := json.Unmarshal(data, &encoded); err != nil {
			t.Fatal(err)
		}
		if encoded["name"] != ToolDefinitions()[index].Name {
			t.Fatalf("tool %d name = %#v in %s", index, encoded["name"], data)
		}
		if encoded["description"] == "" {
			t.Fatalf("tool %d missing description in %s", index, data)
		}
		if encoded["strict"] != true {
			t.Fatalf("tool %d strict = %#v in %s", index, encoded["strict"], data)
		}
		inputSchema, ok := encoded["input_schema"].(map[string]any)
		if !ok {
			t.Fatalf("tool %d input_schema encoded as %T in %s", index, encoded["input_schema"], data)
		}
		if inputSchema["type"] != "object" {
			t.Fatalf("tool %d input_schema.type = %#v in %s", index, inputSchema["type"], data)
		}
		if _, ok := inputSchema["properties"].(map[string]any); !ok {
			t.Fatalf("tool %d properties encoded as %T in %s", index, inputSchema["properties"], data)
		}
		if _, ok := inputSchema["required"].([]any); !ok {
			t.Fatalf("tool %d required encoded as %T in %s", index, inputSchema["required"], data)
		}
		if inputSchema["additionalProperties"] != false {
			t.Fatalf("tool %d additionalProperties = %#v in %s", index, inputSchema["additionalProperties"], data)
		}
	}
}

func TestProviderMaxToolTurns(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	provider := OpenAIProvider{
		Model: "test-model",
		createResponse: func(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
			return openAIResponse(t, `{"id":"r1","output":[{"type":"function_call","call_id":"c1","name":"stave_repos_list","arguments":"{}"}]}`), nil
		},
	}
	_, err := provider.Run(context.Background(), ProviderRequest{Query: "loop", Context: Context{}, Dispatcher: dispatcher, MaxTurns: 1})
	if err == nil || !strings.Contains(err.Error(), "exceeded 1 tool turns") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIProviderAllowsRetryAfterToolError(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	var calls int
	provider := OpenAIProvider{
		Model: "test-model",
		createResponse: func(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			switch calls {
			case 1:
				return openAIResponse(t, `{"id":"r1","output":[{"type":"function_call","call_id":"c1","name":"stave_space_create","arguments":"{\"space_id\":\"ex-2\",\"edits\":[{\"name\":\"missing\"}]}"}]}`), nil
			case 2:
				return openAIResponse(t, `{"id":"r2","output":[{"type":"function_call","call_id":"c2","name":"stave_space_create","arguments":"{\"space_id\":\"ex-2\",\"edits\":[{\"name\":\"api\"}]}"}]}`), nil
			case 3:
				return openAIResponse(t, `{"id":"r3","output":[{"type":"function_call","call_id":"c3","name":"stave_finish","arguments":"{\"summary\":\"create ex-2\"}"}]}`), nil
			default:
				t.Fatalf("unexpected OpenAI call %d", calls)
				return nil, nil
			}
		},
	}

	result, err := provider.Run(context.Background(), ProviderRequest{Query: "create ex-2", Context: Context{}, Dispatcher: dispatcher})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.ToolCalls) != 3 || !result.ToolCalls[0].Error {
		t.Fatalf("tool calls = %#v", result.ToolCalls)
	}
	if len(result.Plan.Operations) != 1 || len(result.Plan.Operations[0].Edits) != 1 || result.Plan.Operations[0].Edits[0].Name != "api" {
		t.Fatalf("operations = %#v", result.Plan.Operations)
	}
}

func openAIResponse(t *testing.T, raw string) *responses.Response {
	t.Helper()
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	return &resp
}

func anthropicMessage(t *testing.T, raw string) *anthropic.Message {
	t.Helper()
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	return &msg
}
