package agent

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/portal"
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
		spaceCtx.Portals = portalSummaries(spacePath)
		ctx.Spaces = append(ctx.Spaces, spaceCtx)
	}
	sort.Slice(ctx.Spaces, func(i, j int) bool {
		return ctx.Spaces[i].ID < ctx.Spaces[j].ID
	})
	return ctx, nil
}

func portalSummaries(spacePath string) []PortalContext {
	manifest, err := portal.LoadManifest(spacePath)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(manifest.Portals))
	for id := range manifest.Portals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	portals := make([]PortalContext, 0, len(ids))
	for _, id := range ids {
		item := manifest.Portals[id]
		providers := make([]string, 0, len(item.Auth.Providers))
		for _, provider := range item.Auth.Providers {
			providers = append(providers, provider.Provider)
		}
		sort.Strings(providers)
		portals = append(portals, PortalContext{
			ID:             portalID(item.ID),
			Driver:         string(item.Driver),
			SyncMode:       string(item.Workspace.SyncMode),
			LocalPath:      item.Workspace.LocalPath,
			RemoteRoot:     item.Workspace.RemoteRoot,
			ContainerRoot:  item.Workspace.ContainerRoot,
			AuthMode:       string(item.Auth.Mode),
			Providers:      providers,
			ManifestExists: true,
			Ownership: PortalOwnershipContext{
				CreatedContainer: item.Ownership.CreatedContainer,
			},
			Runtime: PortalRuntimeContext{
				Engine:           item.Runtime.Engine,
				Image:            item.Runtime.Image,
				ContainerName:    item.Runtime.ContainerName,
				ProjectName:      item.Runtime.ProjectName,
				Service:          item.Runtime.Service,
				DevcontainerPath: item.Runtime.DevcontainerPath,
				ComposeFiles:     append([]string(nil), item.Runtime.ComposeFiles...),
			},
			Target: PortalTargetContext{
				Host:          item.Target.Host,
				InstanceID:    item.Target.InstanceID,
				Region:        item.Target.Region,
				DockerContext: item.Target.DockerContext,
			},
		})
	}
	return portals
}
