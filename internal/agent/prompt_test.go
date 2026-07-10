package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPromptEmbedsContextAndQuery(t *testing.T) {
	req := ProviderRequest{
		Query: "set up space acme-42",
		Context: Context{
			DefaultBase: "main",
			Repos:       []RepoContext{{Name: "api", URL: "https://example.test/api.git", DefaultBranch: "main"}},
			Spaces:      []SpaceContext{{ID: "beta", Kind: "ticket"}},
		},
	}

	system, user, err := BuildPrompt(req)
	if err != nil {
		t.Fatalf("BuildPrompt error = %v", err)
	}
	if !strings.Contains(system, "Stave planning agent") {
		t.Fatalf("system prompt missing role: %q", system)
	}
	if !strings.Contains(system, "stave_finish") {
		t.Fatalf("system prompt missing finish rule")
	}
	if !strings.HasPrefix(user, "Stave context:\n") {
		t.Fatalf("user prompt missing context header: %q", user)
	}
	if !strings.Contains(user, "User request:\nset up space acme-42") {
		t.Fatalf("user prompt missing query: %q", user)
	}

	// The context section must be valid indented JSON of the request context.
	start := strings.Index(user, "{")
	end := strings.LastIndex(user, "}")
	if start < 0 || end <= start {
		t.Fatalf("no JSON object found in user prompt: %q", user)
	}
	var round Context
	if err := json.Unmarshal([]byte(user[start:end+1]), &round); err != nil {
		t.Fatalf("context JSON did not round-trip: %v", err)
	}
	if round.DefaultBase != "main" || len(round.Repos) != 1 || round.Repos[0].Name != "api" {
		t.Fatalf("context not embedded faithfully: %#v", round)
	}
	if len(round.Spaces) != 1 || round.Spaces[0].ID != "beta" {
		t.Fatalf("spaces not embedded: %#v", round.Spaces)
	}
	if !strings.Contains(user, "  \"repos\"") {
		t.Fatalf("context JSON is not indented: %q", user)
	}
}
