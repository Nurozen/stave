package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
)

type Executor struct {
	Config           config.Config
	Git              *git.Client
	Out              io.Writer
	SummonLauncher   summon.Launcher
	AllowInteractive bool
}

func (e Executor) ExecutePlan(ctx context.Context, plan Plan) ([]ExecutionResult, error) {
	results := make([]ExecutionResult, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		result := ExecutionResult{
			Operation: op,
			Command:   EquivalentCommand(op),
		}
		if op.Type == OpSummon && !e.AllowInteractive {
			result.Message = "summon skipped because interactive launch is disabled"
			results = append(results, result)
			continue
		}
		if err := e.executeOperation(ctx, op); err != nil {
			result.Executed = true
			result.Message = err.Error()
			results = append(results, result)
			return results, err
		}
		result.Executed = true
		results = append(results, result)
	}
	return results, nil
}

func (e Executor) executeOperation(ctx context.Context, op Operation) error {
	out := e.Out
	if out == nil {
		out = io.Discard
	}
	client := e.Git
	if client == nil {
		client = git.New()
	}
	svc := space.NewService(e.Config, client, out)
	switch op.Type {
	case OpSpaceCreate:
		return svc.Create(ctx, space.CreateOptions{
			ID:         op.SpaceID,
			Kind:       op.Kind,
			SpecPath:   op.SpecPath,
			Edits:      repoRefsToSpecs(op.Edits),
			References: repoRefsToSpecs(op.References),
		})
	case OpSpaceAdd:
		mode := space.ModeEdit
		if op.Mode == string(space.ModeReference) {
			mode = space.ModeReference
		}
		return svc.AddRepo(ctx, space.AddOptions{
			SpaceID:  op.SpaceID,
			RepoName: op.Repo,
			Mode:     mode,
			Base:     op.Base,
			Ref:      firstNonEmpty(op.Ref, op.Base),
			Branch:   op.Branch,
			NoFetch:  op.NoFetch,
		})
	case OpSpaceSync:
		return svc.Sync(ctx, space.SyncOptions{SpaceID: op.SpaceID, ReferencesOnly: op.ReferencesOnly})
	case OpSpaceStatus:
		status, err := svc.Status(ctx, op.SpaceID)
		if err != nil {
			return err
		}
		writeStatus(out, svc.SpacePath(op.SpaceID), status)
		return nil
	case OpReposList:
		names := make([]string, 0, len(e.Config.Repos))
		for name := range e.Config.Repos {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			repo := e.Config.Repos[name]
			fmt.Fprintf(out, "%s\t%s\t%s\n", name, repo.URL, repo.BareRepoPath)
		}
		return nil
	case OpReposSync:
		if op.Repo != "" {
			return client.FetchAllPrune(ctx, e.Config.Repos[op.Repo].BareRepoPath)
		}
		names := make([]string, 0, len(e.Config.Repos))
		for name := range e.Config.Repos {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := client.FetchAllPrune(ctx, e.Config.Repos[name].BareRepoPath); err != nil {
				return err
			}
		}
		return nil
	case OpSummon:
		summoner := summon.ResolveName(e.Config, op.Summoner)
		svc := summon.NewService(e.Config, e.SummonLauncher, out)
		svc.Interactive = true
		return svc.Summon(ctx, summon.Options{SpaceID: op.SpaceID, Summoner: summoner})
	default:
		return fmt.Errorf("unsupported operation %q", op.Type)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func repoRefsToSpecs(refs []RepoRef) []space.RepoSpec {
	specs := make([]space.RepoSpec, 0, len(refs))
	for _, ref := range refs {
		specs = append(specs, space.RepoSpec{Name: ref.Name, Ref: ref.Ref})
	}
	return specs
}

func writeStatus(out io.Writer, spacePath string, status space.Status) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "space %s (%s)\npath: %s\n", status.Manifest.ID, status.Manifest.Kind, spacePath)
	if status.Manifest.SpecPath != "" {
		fmt.Fprintf(&b, "spec: %s/%s\n", spacePath, status.Manifest.SpecPath)
	}
	for _, repo := range status.Repos {
		state := "clean"
		if repo.Dirty {
			state = "dirty"
		}
		exists := "missing"
		if repo.Exists {
			exists = "present"
		}
		fmt.Fprintf(&b, "\n%s [%s] %s %s\n", repo.Repo.Name, repo.Repo.Mode, exists, state)
	}
	_, _ = out.Write(b.Bytes())
}
