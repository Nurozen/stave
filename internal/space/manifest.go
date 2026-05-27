package space

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ManifestName = ".stave.yaml"
	AgentsName   = "AGENTS.md"
)

type RepoMode string

const (
	ModeEdit      RepoMode = "edit"
	ModeReference RepoMode = "reference"
)

type Manifest struct {
	ID         string         `yaml:"id"`
	Kind       string         `yaml:"kind,omitempty"`
	CreatedAt  time.Time      `yaml:"createdAt"`
	TicketPath string         `yaml:"ticketPath,omitempty"`
	Repos      []RepoManifest `yaml:"repos"`
}

type RepoManifest struct {
	Name         string   `yaml:"name"`
	Mode         RepoMode `yaml:"mode"`
	Path         string   `yaml:"path"`
	Base         string   `yaml:"base,omitempty"`
	Ref          string   `yaml:"ref,omitempty"`
	Branch       string   `yaml:"branch,omitempty"`
	BareRepoPath string   `yaml:"bareRepoPath"`
}

func LoadManifest(spacePath string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(spacePath, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func SaveManifest(spacePath string, manifest Manifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(spacePath, ManifestName), data, 0o644)
}

func (m Manifest) FindRepo(name string) (RepoManifest, int, bool) {
	for i, repo := range m.Repos {
		if repo.Name == name {
			return repo, i, true
		}
	}
	return RepoManifest{}, -1, false
}

func (m Manifest) HasPath(path string) bool {
	for _, repo := range m.Repos {
		if repo.Path == path {
			return true
		}
	}
	return false
}
