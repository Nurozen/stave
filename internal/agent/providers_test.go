package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3/responses"
)

func TestOpenAIProviderToolLoop(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	var calls int
	provider := OpenAIProvider{
		Model: "test-model",
		createResponse: func(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			if len(params.Tools) != len(ToolDefinitions()) {
				t.Fatalf("tools = %d, want %d", len(params.Tools), len(ToolDefinitions()))
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

	result, err := provider.Run(context.Background(), ProviderRequest{Query: "create ex-2", Context: Context{}, Dispatcher: dispatcher})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Plan.Summary != "create ex-2" || len(result.Plan.Operations) != 1 || len(result.ReadResults) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAnthropicProviderToolLoop(t *testing.T) {
	cfg := testConfig(t)
	dispatcher := NewToolDispatcher(cfg, nil, nil)
	var calls int
	provider := AnthropicProvider{
		Model: "test-model",
		createMessage: func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
			calls++
			if len(params.Tools) != len(ToolDefinitions()) {
				t.Fatalf("tools = %d, want %d", len(params.Tools), len(ToolDefinitions()))
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

	result, err := provider.Run(context.Background(), ProviderRequest{Query: "create ex-2", Context: Context{}, Dispatcher: dispatcher})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Plan.Summary != "create ex-2" || len(result.Plan.Operations) != 1 || len(result.ReadResults) != 1 {
		t.Fatalf("result = %#v", result)
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
