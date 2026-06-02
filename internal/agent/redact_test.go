package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactTextPatterns(t *testing.T) {
	inputs := []string{
		"api_key=sk-secret123456",
		"Authorization: bearer abcdefghijklmnop",
		"password: hunter2",
		"token sk-ant-secret123456",
	}
	for _, input := range inputs {
		if got := RedactText(input); strings.Contains(got, "secret123456") || strings.Contains(got, "hunter2") || strings.Contains(got, "abcdefghijklmnop") {
			t.Fatalf("RedactText(%q) = %q", input, got)
		}
	}
}

func TestRedactRawJSONAndRunResult(t *testing.T) {
	raw := redactRawJSON(json.RawMessage(`{"api_key":"sk-secret123456","nested":{"access_token":"tok-secret"},"safe":"ok"}`))
	if strings.Contains(string(raw), "sk-secret") || strings.Contains(string(raw), "tok-secret") {
		t.Fatalf("redacted json leaked: %s", raw)
	}
	if !strings.Contains(string(raw), `"safe":"ok"`) {
		t.Fatalf("safe json value was lost: %s", raw)
	}

	result := RedactRunResult(RunResult{
		Message:   "bearer abcdefghijklmnop",
		Questions: []Question{{ID: "token", Prompt: "token sk-secret123456", Options: []string{"sk-secret123456"}}},
		Plan: Plan{
			Summary:    "use api_key=sk-secret123456",
			Operations: []Operation{{Type: OpPortalSummon, SpaceID: "ex-1", Host: "devbox", HandoffPrompt: "password=hunter2"}},
			Notes:      []string{"access_token=tok-secret"},
			Warnings:   []string{"ghp_secretvalue"},
		},
		ToolCalls:   []ToolCallRecord{{Name: ToolPortalInit, Arguments: json.RawMessage(`{"secret":"sk-secret123456"}`), Message: "password=hunter2"}},
		ReadResults: []ToolResultRecord{{Name: ToolPortalStatus, Payload: map[string]any{"access_token": "tok-secret"}, Summary: "api_key=sk-secret123456"}},
		Results:     []ExecutionResult{{Command: "echo sk-secret123456", Message: "password=hunter2", Operation: Operation{Type: OpPortalSummon, HandoffPrompt: "sk-secret123456"}}},
	})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sk-secret123456", "hunter2", "tok-secret", "abcdefghijklmnop"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("redacted run result leaked %q: %s", leak, data)
		}
	}
}

func TestRedactFallbacks(t *testing.T) {
	if got := RedactForJSON(map[string]any{"safe": "value"}); got == nil {
		t.Fatal("RedactForJSON returned nil")
	}
	raw := redactRawJSON(json.RawMessage(`{"api_key":"sk-secret123456"`))
	if strings.Contains(string(raw), "sk-secret123456") {
		t.Fatalf("malformed JSON fallback leaked: %s", raw)
	}
	_ = redactAny(func() {})
}
