package agent

import (
	"context"
	"reflect"
	"testing"
)

type fakeCommandRunner struct {
	calls []fakeCommandCall
	out   string
	err   error
}

type fakeCommandCall struct {
	name  string
	args  []string
	stdin string
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, string, error) {
	f.calls = append(f.calls, fakeCommandCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	return f.out, "", f.err
}

func TestKeychainSecretStoreCommands(t *testing.T) {
	runner := &fakeCommandRunner{out: "secret\n"}
	store := KeychainSecretStore{SecurityPath: "/usr/bin/security", Runner: runner}
	ctx := context.Background()

	if err := store.Put(ctx, "keychain:stave/agent/openai", "sk-test"); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := store.Get(ctx, "keychain:stave/agent/openai")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != "secret" {
		t.Fatalf("secret = %q", got)
	}
	if err := store.Delete(ctx, "keychain:stave/agent/openai"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	wants := [][]string{
		{"add-generic-password", "-a", "agent/openai", "-s", "stave", "-w", "sk-test", "-U"},
		{"find-generic-password", "-a", "agent/openai", "-s", "stave", "-w"},
		{"delete-generic-password", "-a", "agent/openai", "-s", "stave"},
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d args = %#v, want %#v", i, runner.calls[i].args, want)
		}
	}
}

func TestEnvSecretStore(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	value, err := EnvSecretStore{}.Get(context.Background(), "env:OPENAI_API_KEY")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if value != "sk-env" {
		t.Fatalf("value = %q", value)
	}
}
