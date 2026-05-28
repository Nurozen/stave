package agent

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
)

func BuildContext(cfg config.Config) (Context, error) {
	ctx := Context{DefaultBase: cfg.DefaultBase}

	repoNames := make([]string, 0, len(cfg.Repos))
	for name := range cfg.Repos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)
	for _, name := range repoNames {
		repo := cfg.Repos[name]
		ctx.Repos = append(ctx.Repos, RepoContext{
			Name:          name,
			URL:           repo.URL,
			DefaultBranch: repo.DefaultBranch,
		})
	}

	entries, err := os.ReadDir(cfg.AgentWorkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ctx, nil
		}
		return Context{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".archive" {
			continue
		}
		spacePath := filepath.Join(cfg.AgentWorkDir, entry.Name())
		manifest, err := space.LoadManifest(spacePath)
		if err != nil {
			continue
		}
		spaceCtx := SpaceContext{
			ID:       manifest.ID,
			Kind:     manifest.Kind,
			SpecPath: manifest.SpecPath,
		}
		for _, repo := range manifest.Repos {
			spaceCtx.Repos = append(spaceCtx.Repos, SpaceRepoContext{
				Name:   repo.Name,
				Mode:   string(repo.Mode),
				Path:   repo.Path,
				Base:   repo.Base,
				Ref:    repo.Ref,
				Branch: repo.Branch,
			})
		}
		ctx.Spaces = append(ctx.Spaces, spaceCtx)
	}
	sort.Slice(ctx.Spaces, func(i, j int) bool {
		return ctx.Spaces[i].ID < ctx.Spaces[j].ID
	})
	return ctx, nil
}
