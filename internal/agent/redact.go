package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

const redactedValue = "[redacted]"

var secretTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(sk-[a-z0-9._=-]{6,}|sk-ant-[a-z0-9._=-]{6,}|xox[baprs]-[a-z0-9._=-]{6,}|gh[pousr]_[a-z0-9_]{6,})\b`),
	regexp.MustCompile(`(?i)\b(bearer\s+)[a-z0-9._=-]{8,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|oauth[_-]?token|auth[_-]?token|secret|password|credential)\s*[:=]\s*['"]?[^'"\s,}]+`),
}

func RedactText(value string) string {
	redacted := value
	for _, pattern := range secretTextPatterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			if strings.HasPrefix(strings.ToLower(match), "bearer ") {
				return match[:7] + redactedValue
			}
			if idx := strings.IndexAny(match, ":="); idx >= 0 {
				return match[:idx+1] + redactedValue
			}
			return redactedValue
		})
	}
	return redacted
}

func redactRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(RedactText(string(raw)))
	}
	value = redactJSONValue("", value)
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(RedactText(string(raw)))
	}
	return json.RawMessage(data)
}

func redactJSONValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for innerKey, innerValue := range typed {
			out[innerKey] = redactJSONValue(innerKey, innerValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, innerValue := range typed {
			out[i] = redactJSONValue(key, innerValue)
		}
		return out
	case string:
		if typed == "" {
			return typed
		}
		if sensitiveKey(key) || RedactText(typed) != typed {
			return redactedValue
		}
		return typed
	default:
		if sensitiveKey(key) {
			return redactedValue
		}
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, needle := range []string{"api_key", "apikey", "access_token", "oauth_token", "auth_token", "secret", "password", "credential", "private_key"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

func redactAny(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return value
	}
	return redactJSONValue("", decoded)
}

func RedactForJSON(value any) any {
	return redactAny(value)
}

func RedactRunResult(result RunResult) RunResult {
	result.Status = RedactText(result.Status)
	result.Message = RedactText(result.Message)
	for i := range result.Questions {
		result.Questions[i].Prompt = RedactText(result.Questions[i].Prompt)
		result.Questions[i].Description = RedactText(result.Questions[i].Description)
		result.Questions[i].Options = redactStrings(result.Questions[i].Options)
	}
	result.Plan = redactPlan(result.Plan)
	result.Commands = redactStrings(result.Plan.Commands())
	result.ToolCalls = redactToolCalls(result.ToolCalls)
	result.ReadResults = redactToolResults(result.ReadResults)
	for i := range result.Results {
		result.Results[i].Operation = redactOperation(result.Results[i].Operation)
		result.Results[i].Command = RedactText(result.Results[i].Command)
		result.Results[i].Message = RedactText(result.Results[i].Message)
	}
	return result
}

func redactToolCalls(records []ToolCallRecord) []ToolCallRecord {
	out := make([]ToolCallRecord, len(records))
	for i, record := range records {
		out[i] = record
		out[i].Arguments = redactRawJSON(record.Arguments)
		out[i].Message = RedactText(record.Message)
	}
	return out
}

func redactToolResults(records []ToolResultRecord) []ToolResultRecord {
	out := make([]ToolResultRecord, len(records))
	for i, record := range records {
		out[i] = record
		out[i].Payload = redactAny(record.Payload)
		out[i].Summary = RedactText(record.Summary)
	}
	return out
}

func redactPlan(plan Plan) Plan {
	plan.Summary = RedactText(plan.Summary)
	plan.Notes = redactStrings(plan.Notes)
	plan.Warnings = redactStrings(plan.Warnings)
	for i := range plan.Operations {
		plan.Operations[i] = redactOperation(plan.Operations[i])
	}
	return plan
}

func redactOperation(op Operation) Operation {
	op.SpecPath = RedactText(op.SpecPath)
	op.Image = RedactText(op.Image)
	op.Host = RedactText(op.Host)
	op.Profile = RedactText(op.Profile)
	op.IdentityPath = RedactText(op.IdentityPath)
	op.RemoteRoot = RedactText(op.RemoteRoot)
	op.ContainerRoot = RedactText(op.ContainerRoot)
	op.DevcontainerPath = RedactText(op.DevcontainerPath)
	op.Workdir = RedactText(op.Workdir)
	op.HandoffPrompt = RedactText(op.HandoffPrompt)
	op.Unsupported = RedactText(op.Unsupported)
	op.Include = redactStrings(op.Include)
	op.Exclude = redactStrings(op.Exclude)
	op.ComposeFiles = redactStrings(op.ComposeFiles)
	return op
}

func redactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = RedactText(value)
	}
	return out
}
