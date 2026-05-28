package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	anthropicparam "github.com/anthropics/anthropic-sdk-go/packages/param"
	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	openaiparam "github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

const defaultMaxToolTurns = 12

func DefaultProviderFactory(providerName, model, apiKey string) (Provider, error) {
	switch providerName {
	case ProviderOpenAI:
		return NewOpenAIProvider(model, apiKey), nil
	case ProviderAnthropic:
		return NewAnthropicProvider(model, apiKey), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", providerName)
	}
}

type openAIResponseCreator func(context.Context, responses.ResponseNewParams) (*responses.Response, error)

type OpenAIProvider struct {
	Model          string
	createResponse openAIResponseCreator
}

func NewOpenAIProvider(model, apiKey string) OpenAIProvider {
	client := openai.NewClient(openaioption.WithAPIKey(apiKey))
	return OpenAIProvider{
		Model: model,
		createResponse: func(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
			return client.Responses.New(ctx, params)
		},
	}
}

func (p OpenAIProvider) Run(ctx context.Context, request ProviderRequest) (RunResult, error) {
	if request.Dispatcher == nil {
		return RunResult{}, fmt.Errorf("agent dispatcher is required")
	}
	system, user, err := BuildPrompt(request)
	if err != nil {
		return RunResult{}, err
	}
	createResponse := p.createResponse
	if createResponse == nil {
		return RunResult{}, fmt.Errorf("OpenAI provider is not configured")
	}
	maxTurns := maxToolTurns(request.MaxTurns)
	input := responses.ResponseNewParamsInputUnion{OfString: openaiparam.NewOpt(user)}
	previousResponseID := ""
	for turn := 0; turn < maxTurns; turn++ {
		params := responses.ResponseNewParams{
			Model:             shared.ResponsesModel(p.Model),
			Instructions:      openaiparam.NewOpt(system),
			Input:             input,
			Tools:             openAITools(ToolDefinitions()),
			ToolChoice:        responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openaiparam.NewOpt(responses.ToolChoiceOptionsAuto)},
			ParallelToolCalls: openaiparam.NewOpt(false),
			MaxOutputTokens:   openaiparam.NewOpt[int64](4096),
			Temperature:       openaiparam.NewOpt(0.1),
			Store:             openaiparam.NewOpt(false),
		}
		if previousResponseID != "" {
			params.PreviousResponseID = openaiparam.NewOpt(previousResponseID)
		}
		resp, err := createResponse(ctx, params)
		if err != nil {
			return RunResult{}, err
		}
		previousResponseID = resp.ID
		calls := openAIToolCalls(resp)
		if len(calls) == 0 {
			if request.Dispatcher.Session.Finished {
				return request.Dispatcher.Session.RunResult(), nil
			}
			return RunResult{}, fmt.Errorf("OpenAI response did not call a Stave tool")
		}
		outputs := make(responses.ResponseInputParam, 0, len(calls))
		for _, call := range calls {
			result := request.Dispatcher.Dispatch(ctx, call)
			outputs = append(outputs, responses.ResponseInputItemParamOfFunctionCallOutput(call.ID, result.OutputString()))
		}
		if request.Dispatcher.Session.Finished {
			return request.Dispatcher.Session.RunResult(), nil
		}
		input = responses.ResponseNewParamsInputUnion{OfInputItemList: outputs}
	}
	return RunResult{}, fmt.Errorf("agent exceeded %d tool turns", maxTurns)
}

type anthropicMessageCreator func(context.Context, anthropic.MessageNewParams) (*anthropic.Message, error)

type AnthropicProvider struct {
	Model         string
	createMessage anthropicMessageCreator
}

func NewAnthropicProvider(model, apiKey string) AnthropicProvider {
	client := anthropic.NewClient(anthropicoption.WithAPIKey(apiKey))
	return AnthropicProvider{
		Model: model,
		createMessage: func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
			return client.Messages.New(ctx, params)
		},
	}
}

func (p AnthropicProvider) Run(ctx context.Context, request ProviderRequest) (RunResult, error) {
	if request.Dispatcher == nil {
		return RunResult{}, fmt.Errorf("agent dispatcher is required")
	}
	system, user, err := BuildPrompt(request)
	if err != nil {
		return RunResult{}, err
	}
	createMessage := p.createMessage
	if createMessage == nil {
		return RunResult{}, fmt.Errorf("anthropic provider is not configured")
	}
	maxTurns := maxToolTurns(request.MaxTurns)
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
	}
	for turn := 0; turn < maxTurns; turn++ {
		resp, err := createMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(p.Model),
			MaxTokens: 4096,
			System:    []anthropic.TextBlockParam{{Text: system}},
			Messages:  messages,
			Tools:     anthropicTools(ToolDefinitions()),
			ToolChoice: anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{
				DisableParallelToolUse: anthropicparam.NewOpt(true),
			}},
			Temperature: anthropicparam.NewOpt(0.1),
		})
		if err != nil {
			return RunResult{}, err
		}
		calls := anthropicToolCalls(resp)
		if len(calls) == 0 {
			if request.Dispatcher.Session.Finished {
				return request.Dispatcher.Session.RunResult(), nil
			}
			return RunResult{}, fmt.Errorf("anthropic response did not call a Stave tool")
		}
		resultBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(calls))
		for _, call := range calls {
			result := request.Dispatcher.Dispatch(ctx, call)
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(call.ID, result.OutputString(), result.Error))
		}
		if request.Dispatcher.Session.Finished {
			return request.Dispatcher.Session.RunResult(), nil
		}
		messages = append(messages, resp.ToParam(), anthropic.NewUserMessage(resultBlocks...))
	}
	return RunResult{}, fmt.Errorf("agent exceeded %d tool turns", maxTurns)
}

func openAITools(defs []ToolDefinition) []responses.ToolUnionParam {
	tools := make([]responses.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		tool := responses.ToolParamOfFunction(def.Name, openAISchema(def.Parameters), true)
		if tool.OfFunction != nil {
			tool.OfFunction.Description = openaiparam.NewOpt(def.Description)
		}
		tools = append(tools, tool)
	}
	return tools
}

func openAISchema(schema map[string]any) map[string]any {
	normalized, _ := normalizeOpenAISchema(schema).(map[string]any)
	return normalized
}

func normalizeOpenAISchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed)+1)
		for key, inner := range typed {
			normalized[key] = normalizeOpenAISchema(inner)
		}
		if props, ok := normalized["properties"].(map[string]any); ok {
			originalRequired := stringSet(typed["required"])
			required := make([]string, 0, len(props))
			for name, prop := range props {
				required = append(required, name)
				if !originalRequired[name] {
					props[name] = nullableSchema(prop)
				}
			}
			sort.Strings(required)
			normalized["required"] = required
			normalized["additionalProperties"] = false
		} else if typed["type"] == "object" {
			normalized["required"] = []string{}
			normalized["additionalProperties"] = false
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, inner := range typed {
			normalized = append(normalized, normalizeOpenAISchema(inner))
		}
		return normalized
	case []string:
		normalized := make([]any, 0, len(typed))
		for _, inner := range typed {
			normalized = append(normalized, inner)
		}
		return normalized
	default:
		return typed
	}
}

func nullableSchema(value any) any {
	schema, ok := value.(map[string]any)
	if !ok {
		return value
	}
	nullable := make(map[string]any, len(schema))
	for key, inner := range schema {
		nullable[key] = inner
	}
	switch typ := nullable["type"].(type) {
	case string:
		if typ != "null" {
			nullable["type"] = []any{typ, "null"}
		}
	case []any:
		hasNull := false
		for _, entry := range typ {
			if entry == "null" {
				hasNull = true
				break
			}
		}
		if !hasNull {
			nullable["type"] = append(append([]any(nil), typ...), "null")
		}
	}
	if enum, ok := nullable["enum"].([]string); ok {
		expanded := make([]any, 0, len(enum)+1)
		for _, entry := range enum {
			expanded = append(expanded, entry)
		}
		expanded = append(expanded, nil)
		nullable["enum"] = expanded
	}
	return nullable
}

func stringSet(value any) map[string]bool {
	set := map[string]bool{}
	switch typed := value.(type) {
	case []string:
		for _, entry := range typed {
			set[entry] = true
		}
	case []any:
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				set[text] = true
			}
		}
	}
	return set
}

func openAIToolCalls(resp *responses.Response) []ToolCall {
	if resp == nil {
		return nil
	}
	calls := []ToolCall{}
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		call := item.AsFunctionCall()
		calls = append(calls, ToolCall{ID: call.CallID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	}
	return calls
}

func anthropicTools(defs []ToolDefinition) []anthropic.ToolUnionParam {
	tools := make([]anthropic.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        def.Name,
			Description: anthropicparam.NewOpt(def.Description),
			InputSchema: anthropicSchema(def.Parameters),
			Strict:      anthropicparam.NewOpt(true),
		}})
	}
	return tools
}

func anthropicSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	properties := schema["properties"]
	required, _ := schema["required"].([]string)
	input := anthropic.ToolInputSchemaParam{Properties: properties, Required: required}
	if additionalProperties, ok := schema["additionalProperties"]; ok {
		input.ExtraFields = map[string]any{"additionalProperties": additionalProperties}
	}
	return input
}

func anthropicToolCalls(resp *anthropic.Message) []ToolCall {
	if resp == nil {
		return nil
	}
	calls := []ToolCall{}
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: json.RawMessage(block.Input)})
	}
	return calls
}

func maxToolTurns(value int) int {
	if value > 0 {
		return value
	}
	return defaultMaxToolTurns
}
