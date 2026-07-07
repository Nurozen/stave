package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type SecretStore interface {
	Available() bool
	Put(ctx context.Context, ref string, value string) error
	Get(ctx context.Context, ref string) (string, error)
	Delete(ctx context.Context, ref string) error
}

type EnvSecretStore struct{}

func (EnvSecretStore) Available() bool { return true }

func (EnvSecretStore) Put(ctx context.Context, ref string, value string) error {
	return errors.New("env secret store cannot persist values")
}

func (EnvSecretStore) Get(ctx context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok || name == "" {
		return "", fmt.Errorf("invalid env secret ref %q", ref)
	}
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return value, nil
}

func (EnvSecretStore) Delete(ctx context.Context, ref string) error { return nil }

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin string) (stdout string, stderr string, err error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type KeychainSecretStore struct {
	SecurityPath string
	Runner       CommandRunner
}

func DefaultSecretStore() SecretStore {
	store := KeychainSecretStore{}
	if store.Available() {
		return store
	}
	return EnvSecretStore{}
}

func (s KeychainSecretStore) Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	path := s.SecurityPath
	if path == "" {
		path = "/usr/bin/security"
	}
	_, err := os.Stat(path)
	return err == nil
}

func (s KeychainSecretStore) Put(ctx context.Context, ref string, value string) error {
	service, account, err := parseKeychainRef(ref)
	if err != nil {
		return err
	}
	_, stderr, err := s.run(ctx, []string{"add-generic-password", "-a", account, "-s", service, "-w", value, "-U"}, "")
	if err != nil {
		return fmt.Errorf("write keychain secret: %s: %w", strings.TrimSpace(stderr), err)
	}
	return nil
}

func (s KeychainSecretStore) Get(ctx context.Context, ref string) (string, error) {
	service, account, err := parseKeychainRef(ref)
	if err != nil {
		return "", err
	}
	stdout, stderr, err := s.run(ctx, []string{"find-generic-password", "-a", account, "-s", service, "-w"}, "")
	if err != nil {
		return "", fmt.Errorf("read keychain secret: %s: %w", strings.TrimSpace(stderr), err)
	}
	return strings.TrimRight(stdout, "\r\n"), nil
}

func (s KeychainSecretStore) Delete(ctx context.Context, ref string) error {
	service, account, err := parseKeychainRef(ref)
	if err != nil {
		return err
	}
	_, _, err = s.run(ctx, []string{"delete-generic-password", "-a", account, "-s", service}, "")
	return err
}

func (s KeychainSecretStore) run(ctx context.Context, args []string, stdin string) (string, string, error) {
	runner := s.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	path := s.SecurityPath
	if path == "" {
		path = "/usr/bin/security"
	}
	return runner.Run(ctx, path, args, stdin)
}

func APIKeyRefForProvider(provider string, keychain bool) string {
	switch provider {
	case ProviderAnthropic:
		if keychain {
			return "keychain:stave/agent/anthropic"
		}
		return "env:ANTHROPIC_API_KEY"
	default:
		if keychain {
			return "keychain:stave/agent/openai"
		}
		return "env:OPENAI_API_KEY"
	}
}

func ResolveSecret(ctx context.Context, store SecretStore, ref string) (string, error) {
	if strings.HasPrefix(ref, "env:") {
		return EnvSecretStore{}.Get(ctx, ref)
	}
	if strings.HasPrefix(ref, "keychain:") {
		if store == nil {
			store = DefaultSecretStore()
		}
		return store.Get(ctx, ref)
	}
	return "", fmt.Errorf("unsupported secret ref %q", ref)
}

func parseKeychainRef(ref string) (service string, account string, err error) {
	value, ok := strings.CutPrefix(ref, "keychain:")
	if !ok || value == "" {
		return "", "", fmt.Errorf("invalid keychain ref %q", ref)
	}
	service, account, ok = strings.Cut(value, "/")
	if !ok || service == "" || account == "" {
		return "", "", fmt.Errorf("invalid keychain ref %q", ref)
	}
	return service, account, nil
}
