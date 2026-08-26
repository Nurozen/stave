package agent

import (
	"encoding/json"
	"path/filepath"
	"reflect"
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
		"stave space add ex api -e -b develop":                           {Type: OpSpaceAdd, SpaceID: "ex", Repo: "api", Mode: "edit", Base: "develop"},
		"stave space add ex docs -r -b main":                             {Type: OpSpaceAdd, SpaceID: "ex", Repo: "docs", Mode: "reference", Base: "main"},
		"stave space sync ex --references-only":                          {Type: OpSpaceSync, SpaceID: "ex", ReferencesOnly: true},
		"stave space status ex":                                          {Type: OpSpaceStatus, SpaceID: "ex"},
		"stave repos list":                                               {Type: OpReposList},
		"stave repos tethers api":                                        {Type: OpReposTethers, Repo: "api"},
		"stave repos sync api":                                           {Type: OpReposSync, Repo: "api"},
		"stave space create ex -e api -c":                                {Type: OpSpaceCreate, SpaceID: "ex", Edits: []RepoRef{{Name: "api"}}, Common: true},
		"stave space create ex -e api -c --include-weak":                 {Type: OpSpaceCreate, SpaceID: "ex", Edits: []RepoRef{{Name: "api"}}, Common: true, IncludeWeak: true},
		"stave summon ex --with cursor":                                  {Type: OpSummon, SpaceID: "ex", Summoner: "cursor"},
		"stave saga create story -s /tmp/spec.md -r web:main --memory .": {Type: OpSagaCreate, SagaID: "story", SpecPath: "/tmp/spec.md", References: []RepoRef{{Name: "web", Ref: "main"}}, Memories: []string{"."}},
		"stave saga status story":                                        {Type: OpSagaStatus, SagaID: "story"},
		"stave saga add story m-2 --after m-1":                           {Type: OpSagaAdd, SagaID: "story", SpaceID: "m-2", After: []string{"m-1"}},
		"stave portal init container ex dev --image 'image with spaces' --container-root '/workspace/ex $1'":                                                                                                           {Type: OpPortalInit, SpaceID: "ex", PortalID: "dev", Driver: "docker", Image: "image with spaces", ContainerRoot: "/workspace/ex $1"},
		"stave portal attach ssh ex 'host name' dev --remote-root ~/stave/ex --identity '~/.ssh/id key' --known-hosts /tmp/known_hosts --strict-host-key yes --sync rsync":                                             {Type: OpPortalAttach, SpaceID: "ex", PortalID: "dev", Driver: "ssh", Host: "host name", RemoteRoot: "~/stave/ex", IdentityPath: "~/.ssh/id key", KnownHostsPath: "/tmp/known_hosts", StrictHostKey: "yes", SyncMode: "rsync"},
		"stave portal attach ec2 ex i-123 dev --remote-root ~/stave/ex --port 2222 --identity ~/.ssh/aws --known-hosts /tmp/aws_known_hosts --strict-host-key yes --sync rsync --host 203.0.113.10 --region us-west-2": {Type: OpPortalAttach, SpaceID: "ex", PortalID: "dev", Driver: "ec2-attach", InstanceID: "i-123", Host: "203.0.113.10", Port: 2222, Region: "us-west-2", RemoteRoot: "~/stave/ex", IdentityPath: "~/.ssh/aws", KnownHostsPath: "/tmp/aws_known_hosts", StrictHostKey: "yes", SyncMode: "rsync"},
		"stave portal configure ex dev --remote-root ~/stave/ex --host 203.0.113.10":                                                                                                                                   {Type: OpPortalConfigure, SpaceID: "ex", PortalID: "dev", RemoteRoot: "~/stave/ex", Host: "203.0.113.10"},
		"stave portal auth status ex dev --provider codex":                                                                           {Type: OpPortalAuthStatus, SpaceID: "ex", PortalID: "dev", Provider: "codex"},
		"stave portal sync ex dev --direction to --mode rsync --include '*.go' --exclude .git --delete --max-delete 3 --allow-dirty": {Type: OpPortalSync, SpaceID: "ex", PortalID: "dev", Direction: "to", SyncMode: "rsync", Include: []string{"*.go"}, Exclude: []string{".git"}, Delete: true, MaxDelete: 3, AllowDirty: true},
		"stave portal summon ex dev --with codex --mode tmux --permission workspace-write":                                           {Type: OpPortalSummon, SpaceID: "ex", PortalID: "dev", Summoner: "codex", Mode: "tmux", Permission: "workspace-write"},
		"stave portal destroy ex dev --dry-run":                                                                                      {Type: OpPortalDestroyPreview, SpaceID: "ex", PortalID: "dev"},
	}
	for want, op := range tests {
		if got := EquivalentCommand(op); got != want {
			t.Fatalf("EquivalentCommand(%#v) = %q, want %q", op, got, want)
		}
	}
}

func TestPortalOperationJSONRoundTrip(t *testing.T) {
	op := Operation{
		Type:           OpPortalAttach,
		SpaceID:        "ex",
		PortalID:       "dev",
		Driver:         "ec2-attach",
		InstanceID:     "i-123",
		Host:           "203.0.113.10",
		Region:         "us-west-2",
		Profile:        "prod",
		SSHUser:        "ubuntu",
		IdentityPath:   "~/.ssh/id_rsa",
		KnownHostsPath: "~/.ssh/known_hosts",
		StrictHostKey:  "accept-new",
		RemoteRoot:     "~/stave/agent-work/ex",
		SyncMode:       "rsync",
		HandoffPrompt:  "Inspect the portal.",
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Operation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, op) {
		t.Fatalf("decoded = %#v, want %#v", decoded, op)
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

func TestBuildContextIncludesPortalSummaries(t *testing.T) {
	cfg := testConfig(t)
	saveTestPortal(t, cfg, "ex-1", "dev", "docker")
	ctx, err := BuildContext(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found *SpaceContext
	for i := range ctx.Spaces {
		if ctx.Spaces[i].ID == "ex-1" {
			found = &ctx.Spaces[i]
			break
		}
	}
	if found == nil || len(found.Portals) != 1 {
		t.Fatalf("context spaces = %#v", ctx.Spaces)
	}
	portal := found.Portals[0]
	if portal.ID != "dev" || portal.Driver != "docker" || !portal.ManifestExists || portal.ContainerRoot == "" {
		t.Fatalf("portal summary = %#v", portal)
	}
}

func TestBuildContextMissingAgentWorkDir(t *testing.T) {
	cfg := testConfig(t)
	cfg.AgentWorkDir = filepath.Join(t.TempDir(), "missing-agent-work")
	ctx, err := BuildContext(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Spaces) != 0 || len(ctx.Repos) != len(cfg.Repos) {
		t.Fatalf("context = %#v", ctx)
	}
}
