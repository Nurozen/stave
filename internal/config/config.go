package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

const (
	AppName                    = "stave"
	DefaultBase                = "main"
	DefaultAgentModelOpenAI    = "gpt-5.5"
	DefaultAgentModelAnthropic = "claude-opus-4-7"
	DefaultAgentProvider       = "openai"
	ConfigFileName             = "config.yaml"
	DefaultConfigMode          = 0o600
	DefaultDirMode             = 0o755
)

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Config struct {
	Root         string                `mapstructure:"root" yaml:"root"`
	BareReposDir string                `mapstructure:"bareReposDir" yaml:"bareReposDir"`
	AgentWorkDir string                `mapstructure:"agentWorkDir" yaml:"agentWorkDir"`
	DefaultBase  string                `mapstructure:"defaultBase" yaml:"defaultBase"`
	Repos        map[string]Repository `mapstructure:"repos" yaml:"repos"`
	Agent        AgentConfig           `mapstructure:"agent" yaml:"agent,omitempty"`
	Summon       SummonConfig          `mapstructure:"summon" yaml:"summon,omitempty"`
}

type Repository struct {
	Name          string `mapstructure:"name" yaml:"name"`
	URL           string `mapstructure:"url" yaml:"url"`
	BareRepoPath  string `mapstructure:"bareRepoPath" yaml:"bareRepoPath"`
	DefaultBranch string `mapstructure:"defaultBranch,omitempty" yaml:"defaultBranch,omitempty"`
}

type AgentConfig struct {
	DefaultProvider string                         `mapstructure:"defaultProvider" yaml:"defaultProvider,omitempty"`
	AutoIncant      bool                           `mapstructure:"autoIncant" yaml:"autoIncant,omitempty"`
	Providers       map[string]AgentProviderConfig `mapstructure:"providers" yaml:"providers,omitempty"`
}

type AgentProviderConfig struct {
	Model     string `mapstructure:"model" yaml:"model,omitempty"`
	APIKeyRef string `mapstructure:"apiKeyRef" yaml:"apiKeyRef,omitempty"`
}

type SummonConfig struct {
	Default  string            `mapstructure:"default" yaml:"default,omitempty"`
	Commands map[string]string `mapstructure:"commands" yaml:"commands,omitempty"`
}

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, AppName), nil
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", AppName, ConfigFileName), nil
}

func Default() (*Config, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	return &Config{
		Root:         root,
		BareReposDir: filepath.Join(root, "bare-repos"),
		AgentWorkDir: filepath.Join(root, "agent-work"),
		DefaultBase:  DefaultBase,
		Repos:        map[string]Repository{},
		Agent:        DefaultAgentConfig(),
		Summon:       DefaultSummonConfig(),
	}, nil
}

func Load(path string) (*Config, string, error) {
	if path == "" {
		defaultPath, err := DefaultConfigPath()
		if err != nil {
			return nil, "", err
		}
		path = defaultPath
	}

	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	defaults, err := Default()
	if err != nil {
		return nil, path, err
	}
	v.SetDefault("root", defaults.Root)
	v.SetDefault("bareReposDir", defaults.BareReposDir)
	v.SetDefault("agentWorkDir", defaults.AgentWorkDir)
	v.SetDefault("defaultBase", defaults.DefaultBase)
	v.SetDefault("repos", map[string]Repository{})
	v.SetDefault("agent", defaults.Agent)
	v.SetDefault("summon", defaults.Summon)

	if err := v.ReadInConfig(); err != nil && !missingConfig(err) {
		return nil, path, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, path, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.ApplyDefaults(); err != nil {
		return nil, path, err
	}
	return &cfg, path, nil
}

func (c *Config) ApplyDefaults() error {
	if c.Root == "" {
		root, err := DefaultRoot()
		if err != nil {
			return err
		}
		c.Root = root
	}
	root, err := ExpandPath(c.Root)
	if err != nil {
		return err
	}
	c.Root = root
	if c.BareReposDir == "" {
		c.BareReposDir = filepath.Join(c.Root, "bare-repos")
	}
	if c.AgentWorkDir == "" {
		c.AgentWorkDir = filepath.Join(c.Root, "agent-work")
	}
	c.BareReposDir, err = ExpandPath(c.BareReposDir)
	if err != nil {
		return err
	}
	c.AgentWorkDir, err = ExpandPath(c.AgentWorkDir)
	if err != nil {
		return err
	}
	if c.DefaultBase == "" {
		c.DefaultBase = DefaultBase
	}
	if c.Repos == nil {
		c.Repos = map[string]Repository{}
	}
	for name, repo := range c.Repos {
		if repo.Name == "" {
			repo.Name = name
		}
		if repo.BareRepoPath == "" {
			repo.BareRepoPath = c.BareRepoPath(name)
		}
		repo.BareRepoPath, err = ExpandPath(repo.BareRepoPath)
		if err != nil {
			return err
		}
		c.Repos[name] = repo
	}
	c.Agent.ApplyDefaults()
	c.Summon.ApplyDefaults()
	return nil
}

func (c Config) Save(path string) error {
	if path == "" {
		defaultPath, err := DefaultConfigPath()
		if err != nil {
			return err
		}
		path = defaultPath
	}
	copied := c
	if err := copied.ApplyDefaults(); err != nil {
		return err
	}
	data, err := yaml.Marshal(copied)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, DefaultConfigMode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c Config) EnsureRootDirs() error {
	for _, dir := range []string{c.Root, c.BareReposDir, c.AgentWorkDir} {
		if err := os.MkdirAll(dir, DefaultDirMode); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func (c Config) BareRepoPath(name string) string {
	return filepath.Join(c.BareReposDir, name+".git")
}

func (c *Config) RegisterRepository(name, url, defaultBranch string) (Repository, error) {
	if err := ValidateName("repo name", name); err != nil {
		return Repository{}, err
	}
	if err := ValidateGitURL(url); err != nil {
		return Repository{}, err
	}
	if c.Repos == nil {
		c.Repos = map[string]Repository{}
	}
	repo := Repository{
		Name:          name,
		URL:           url,
		BareRepoPath:  c.BareRepoPath(name),
		DefaultBranch: defaultBranch,
	}
	c.Repos[name] = repo
	return repo, nil
}

func (c *Config) UnregisterRepository(name string) {
	delete(c.Repos, name)
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		DefaultProvider: DefaultAgentProvider,
		Providers: map[string]AgentProviderConfig{
			"openai": {
				Model:     DefaultAgentModelOpenAI,
				APIKeyRef: "env:OPENAI_API_KEY",
			},
			"anthropic": {
				Model:     DefaultAgentModelAnthropic,
				APIKeyRef: "env:ANTHROPIC_API_KEY",
			},
		},
	}
}

func DefaultSummonConfig() SummonConfig {
	return SummonConfig{
		Default: "codex",
		Commands: map[string]string{
			"codex":  "codex",
			"claude": "claude",
			"cursor": "cursor-agent",
		},
	}
}

func (c *AgentConfig) ApplyDefaults() {
	defaults := DefaultAgentConfig()
	if c.DefaultProvider == "" {
		c.DefaultProvider = defaults.DefaultProvider
	}
	if c.Providers == nil {
		c.Providers = map[string]AgentProviderConfig{}
	}
	for provider, defaultConfig := range defaults.Providers {
		current := c.Providers[provider]
		if current.Model == "" {
			current.Model = defaultConfig.Model
		}
		if current.APIKeyRef == "" {
			current.APIKeyRef = defaultConfig.APIKeyRef
		}
		c.Providers[provider] = current
	}
}

func (c *SummonConfig) ApplyDefaults() {
	defaults := DefaultSummonConfig()
	if c.Default == "" {
		c.Default = defaults.Default
	}
	if c.Commands == nil {
		c.Commands = map[string]string{}
	}
	for summoner, command := range defaults.Commands {
		if c.Commands[summoner] == "" {
			c.Commands[summoner] = command
		}
	}
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return filepath.Abs(path)
}

func ValidateName(label, name string) error {
	if !safeNamePattern.MatchString(name) {
		return fmt.Errorf("%s %q must start with an alphanumeric character and contain only letters, numbers, dot, underscore, or dash", label, name)
	}
	return nil
}

func ValidateGitURL(raw string) error {
	if raw == "" {
		return errors.New("git URL is required")
	}
	if strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "ssh://") ||
		strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "file://") || strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return nil
	}
	return fmt.Errorf("unsupported git URL %q; use SSH, HTTP(S), file URL, or local path", raw)
}

func missingConfig(err error) bool {
	var notFound viper.ConfigFileNotFoundError
	return errors.As(err, &notFound) || os.IsNotExist(err)
}
