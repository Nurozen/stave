package cli

import (
	"fmt"
	"os"

	"github.com/Nurozen/stave/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// configShowJSON is the resolved configuration exactly as config.Load hands
// it to every verb: defaults overlaid, `~` expanded, bareReposDir /
// agentWorkDir derived from root, repo records normalized. The agent section
// (API key references) is deliberately omitted.
type configShowJSON struct {
	ConfigPath   string                    `json:"configPath" yaml:"configPath"`
	Exists       bool                      `json:"exists" yaml:"exists"`
	Root         string                    `json:"root" yaml:"root"`
	BareReposDir string                    `json:"bareReposDir" yaml:"bareReposDir"`
	AgentWorkDir string                    `json:"agentWorkDir" yaml:"agentWorkDir"`
	DefaultBase  string                    `json:"defaultBase" yaml:"defaultBase"`
	Repos        map[string]configRepoJSON `json:"repos" yaml:"repos"`
	Memory       configMemoryJSON          `json:"memory" yaml:"memory"`
	Tethers      configTethersJSON         `json:"tethers" yaml:"tethers"`
	Summon       configSummonJSON          `json:"summon" yaml:"summon"`
}

type configRepoJSON struct {
	Name          string `json:"name" yaml:"name"`
	URL           string `json:"url" yaml:"url"`
	BareRepoPath  string `json:"bareRepoPath" yaml:"bareRepoPath"`
	DefaultBranch string `json:"defaultBranch,omitempty" yaml:"defaultBranch,omitempty"`
	Description   string `json:"description,omitempty" yaml:"description,omitempty"`
	MarmotVault   string `json:"marmotVault,omitempty" yaml:"marmotVault,omitempty"`
}

type configMemoryJSON struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Binary   string `json:"binary,omitempty" yaml:"binary,omitempty"`
	Default  bool   `json:"default" yaml:"default"`
}

type configTethersJSON struct {
	Enabled         bool `json:"enabled" yaml:"enabled"`
	StrongThreshold int  `json:"strongThreshold" yaml:"strongThreshold"`
}

type configSummonJSON struct {
	Default  string            `json:"default" yaml:"default"`
	Commands map[string]string `json:"commands" yaml:"commands"`
}

// buildConfigShow projects a loaded config onto the show document.
func buildConfigShow(cfg config.Config, path string, exists bool) configShowJSON {
	doc := configShowJSON{
		ConfigPath:   path,
		Exists:       exists,
		Root:         cfg.Root,
		BareReposDir: cfg.BareReposDir,
		AgentWorkDir: cfg.AgentWorkDir,
		DefaultBase:  cfg.DefaultBase,
		Repos:        make(map[string]configRepoJSON, len(cfg.Repos)),
		Memory:       configMemoryJSON{Provider: cfg.Memory.Provider, Binary: cfg.Memory.Binary, Default: cfg.Memory.Default},
		Tethers:      configTethersJSON{Enabled: cfg.Tethers.IsEnabled(), StrongThreshold: cfg.Tethers.StrongThreshold},
		Summon:       configSummonJSON{Default: cfg.Summon.Default, Commands: map[string]string{}},
	}
	for name, repo := range cfg.Repos {
		doc.Repos[name] = configRepoJSON{
			Name:          repo.Name,
			URL:           repo.URL,
			BareRepoPath:  repo.BareRepoPath,
			DefaultBranch: repo.DefaultBranch,
			Description:   repo.Description,
			MarmotVault:   repo.MarmotVault,
		}
	}
	for summoner, command := range cfg.Summon.Commands {
		doc.Summon.Commands[summoner] = command
	}
	return doc
}

// configCommand is the `stave config` group: inspect the resolved config
// without reading the YAML file by hand.
func (a *app) configCommand() *cobra.Command {
	cmd := groupCommand("config", "Inspect the resolved stave configuration")
	cmd.AddCommand(a.configShowCommand(), a.configPathCommand())
	return cmd
}

func (a *app) configShowCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the resolved configuration (defaults applied, paths expanded; agent secrets omitted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJSON(cmd, jsonOut, func() (any, error) {
				cfg, path, err := a.loadConfig()
				if err != nil {
					return nil, err
				}
				_, statErr := os.Stat(path)
				doc := buildConfigShow(*cfg, path, statErr == nil)
				if jsonOut {
					return doc, nil
				}
				return nil, writeConfigYAML(cmd, doc)
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON ({configPath, exists, root, bareReposDir, agentWorkDir, defaultBase, repos, memory, tethers, summon})")
	return cmd
}

// writeConfigYAML renders the show document as YAML: struct fields in
// declaration order, map keys (repos, summon commands) sorted by yaml.v3.
func writeConfigYAML(cmd *cobra.Command, doc configShowJSON) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), string(data))
	return err
}

func (a *app) configPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := a.configPath
			if path == "" {
				defaultPath, err := config.DefaultConfigPath()
				if err != nil {
					return err
				}
				path = defaultPath
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
}
