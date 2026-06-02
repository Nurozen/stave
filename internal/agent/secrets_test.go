package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	calls  []fakeCommandCall
	out    string
	errOut string
	err    error
}

type fakeCommandCall struct {
	name  string
	args  []string
	stdin string
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, string, error) {
	f.calls = append(f.calls, fakeCommandCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	return f.out, f.errOut, f.err
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

func TestExecCommandRunnerAndDefaultSecretStore(t *testing.T) {
	stdout, stderr, err := (ExecCommandRunner{}).Run(context.Background(), "/bin/cat", nil, "hello")
	if err != nil {
		t.Fatalf("Run() error = %v stderr=%s", err, stderr)
	}
	if stdout != "hello" {
		t.Fatalf("stdout = %q", stdout)
	}

	store := DefaultSecretStore()
	if store == nil || !store.Available() {
		t.Fatalf("default store = %#v", store)
	}
}

func TestEnvSecretStore(t *testing.T) {
	store := EnvSecretStore{}
	if !store.Available() {
		t.Fatal("env store should be available")
	}
	if err := store.Put(context.Background(), "env:OPENAI_API_KEY", "sk"); err == nil {
		t.Fatal("env Put succeeded unexpectedly")
	}
	t.Setenv("OPENAI_API_KEY", "sk-env")
	value, err := store.Get(context.Background(), "env:OPENAI_API_KEY")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if value != "sk-env" {
		t.Fatalf("value = %q", value)
	}
	if _, err := store.Get(context.Background(), "OPENAI_API_KEY"); err == nil {
		t.Fatal("invalid env ref succeeded")
	}
	if _, err := store.Get(context.Background(), "env:MISSING_API_KEY"); err == nil {
		t.Fatal("missing env var succeeded")
	}
	if err := store.Delete(context.Background(), "env:OPENAI_API_KEY"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestSecretRefHelpers(t *testing.T) {
	tests := map[string]string{
		APIKeyRefForProvider(ProviderOpenAI, false):     "env:OPENAI_API_KEY",
		APIKeyRefForProvider(ProviderOpenAI, true):      "keychain:stave/agent/openai",
		APIKeyRefForProvider(ProviderAnthropic, false):  "env:ANTHROPIC_API_KEY",
		APIKeyRefForProvider(ProviderAnthropic, true):   "keychain:stave/agent/anthropic",
		APIKeyRefForProvider("unknown-provider", false): "env:OPENAI_API_KEY",
	}
	for got, want := range tests {
		if got != want {
			t.Fatalf("ref = %q, want %q", got, want)
		}
	}

	t.Setenv("STAVE_TEST_SECRET", "env-secret")
	got, err := ResolveSecret(context.Background(), nil, "env:STAVE_TEST_SECRET")
	if err != nil || got != "env-secret" {
		t.Fatalf("ResolveSecret env got=%q err=%v", got, err)
	}

	fake := fakeSecretStore{values: map[string]string{"keychain:stave/agent/openai": "chain-secret"}}
	got, err = ResolveSecret(context.Background(), fake, "keychain:stave/agent/openai")
	if err != nil || got != "chain-secret" {
		t.Fatalf("ResolveSecret keychain got=%q err=%v", got, err)
	}
	if _, err := ResolveSecret(context.Background(), fake, "file:/tmp/secret"); err == nil || !strings.Contains(err.Error(), "unsupported secret ref") {
		t.Fatalf("unsupported ref err = %v", err)
	}
}

func TestKeychainSecretStoreErrorsAndParsing(t *testing.T) {
	runner := &fakeCommandRunner{errOut: "denied\n", err: errors.New("exit 1")}
	store := KeychainSecretStore{SecurityPath: "/usr/bin/security", Runner: runner}
	ctx := context.Background()

	if err := store.Put(ctx, "keychain:stave/agent/openai", "sk-test"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Put error = %v", err)
	}
	if _, err := store.Get(ctx, "keychain:stave/agent/openai"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Get error = %v", err)
	}
	if err := store.Delete(ctx, "keychain:stave/agent/openai"); err == nil {
		t.Fatal("Delete error was nil")
	}
	for _, ref := range []string{"env:OPENAI_API_KEY", "keychain:", "keychain:missing-account"} {
		if _, _, err := parseKeychainRef(ref); err == nil {
			t.Fatalf("parseKeychainRef(%q) succeeded unexpectedly", ref)
		}
	}
	missingStore := KeychainSecretStore{SecurityPath: "/definitely/missing/security"}
	if missingStore.Available() {
		t.Fatal("missing keychain binary reported available")
	}
}

type fakeSecretStore struct {
	values map[string]string
}

func (f fakeSecretStore) Available() bool { return true }

func (f fakeSecretStore) Put(ctx context.Context, ref string, value string) error { return nil }

func (f fakeSecretStore) Get(ctx context.Context, ref string) (string, error) {
	value, ok := f.values[ref]
	if !ok {
		return "", errors.New("missing fake secret")
	}
	return value, nil
}

func (f fakeSecretStore) Delete(ctx context.Context, ref string) error { return nil }
