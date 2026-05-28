package agent

import (
	"strings"
	"testing"
)

func TestParsePlanTextAndCommands(t *testing.T) {
	raw := "```json\n{\"summary\":\"make it\",\"operations\":[{\"type\":\"space_create\",\"space_id\":\"ex-1\",\"kind\":\"ticket\",\"spec_path\":\"/tmp/spec.md\",\"edits\":[{\"name\":\"api\"}],\"references\":[{\"name\":\"web\",\"ref\":\"main\"}]}]}\n```"
	plan, err := ParsePlanText(raw)
	if err != nil {
		t.Fatalf("ParsePlanText() error = %v", err)
	}
	if plan.Summary != "make it" || len(plan.Operations) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	want := "stave space create ex-1 -k ticket -s /tmp/spec.md -e api -r web:main"
	if got := plan.Commands()[0]; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestEquivalentCommands(t *testing.T) {
	tests := map[string]Operation{
		"stave space add ex api -e -b develop":  {Type: OpSpaceAdd, SpaceID: "ex", Repo: "api", Mode: "edit", Base: "develop"},
		"stave space add ex docs -r -b main":    {Type: OpSpaceAdd, SpaceID: "ex", Repo: "docs", Mode: "reference", Base: "main"},
		"stave space sync ex --references-only": {Type: OpSpaceSync, SpaceID: "ex", ReferencesOnly: true},
		"stave space status ex":                 {Type: OpSpaceStatus, SpaceID: "ex"},
		"stave repos list":                      {Type: OpReposList},
		"stave repos sync api":                  {Type: OpReposSync, Repo: "api"},
		"stave summon ex --with cursor":         {Type: OpSummon, SpaceID: "ex", Summoner: "cursor"},
	}
	for want, op := range tests {
		if got := EquivalentCommand(op); got != want {
			t.Fatalf("EquivalentCommand(%#v) = %q, want %q", op, got, want)
		}
	}
}

func TestBuildPromptIncludesContext(t *testing.T) {
	_, user, err := BuildPrompt(ProviderRequest{
		Query:   "make workspace",
		Context: Context{Repos: []RepoContext{{Name: "api"}}, DefaultBase: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"api", "make workspace", "default_base"} {
		if !strings.Contains(user, needle) {
			t.Fatalf("prompt missing %q: %s", needle, user)
		}
	}
}
