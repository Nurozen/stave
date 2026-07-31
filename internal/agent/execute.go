package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/git"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
)

type Executor struct {
	Config           config.Config
	Git              *git.Client
	Out              io.Writer
	SummonLauncher   summon.Launcher
	PortalRunner     portal.Runner
	AllowInteractive bool
}

// executableOperations pins every operation type executeOperation can run.
// ExecutePlan preflights whole plans against it so an unsupported operation
// fails BEFORE the first mutation, never mid-plan.
var executableOperations = map[string]bool{
	OpSpaceCreate:          true,
	OpSpaceAdd:             true,
	OpSpaceSync:            true,
	OpSpaceStatus:          true,
	OpReposList:            true,
	OpReposSync:            true,
	OpSummon:               true,
	OpSagaCreate:           true,
	OpSagaStatus:           true,
	OpSagaAdd:              true,
	OpPortalInit:           true,
	OpPortalAttach:         true,
	OpPortalConfigure:      true,
	OpPortalList:           true,
	OpPortalStatus:         true,
	OpPortalDoctor:         true,
	OpPortalInspect:        true,
	OpPortalAuthStatus:     true,
	OpPortalLogs:           true,
	OpPortalAuthLogin:      true,
	OpPortalAuthInherit:    true,
	OpPortalAuthRevoke:     true,
	OpPortalUp:             true,
	OpPortalSync:           true,
	OpPortalSummon:         true,
	OpPortalDown:           true,
	OpPortalDetach:         true,
	OpPortalDestroyPreview: true,
}

func (e Executor) ExecutePlan(ctx context.Context, plan Plan) ([]ExecutionResult, error) {
	// Capability preflight: every operation must have an executor case before
	// anything runs, so a plan can never mutate state and then die on an
	// unsupported operation.
	for _, op := range plan.Operations {
		if !executableOperations[op.Type] {
			return nil, fmt.Errorf("plan contains operation %q, which has no executor; nothing was executed", op.Type)
		}
	}
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
		if op.Type == OpPortalSummon && !e.AllowInteractive {
			result.Message = "portal summon skipped because interactive launch is disabled"
			results = append(results, result)
			continue
		}
		if err := e.executeOperation(ctx, op); err != nil {
			result.Executed = true
			result.Message = RedactText(err.Error())
			results = append(results, result)
			return results, fmt.Errorf("%s", RedactText(err.Error()))
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
			Memories:   op.Memories,
		})
	case OpSpaceAdd:
		mode := space.ModeEdit
		if op.Mode == string(space.ModeReference) {
			mode = space.ModeReference
		}
		return svc.AddRepo(ctx, space.AddOptions{
			SpaceID:    op.SpaceID,
			RepoName:   op.Repo,
			Mode:       mode,
			Base:       op.Base,
			Ref:        firstNonEmpty(op.Ref, op.Base),
			Branch:     op.Branch,
			NoFetch:    op.NoFetch,
			LinkMemory: true, // S4 space add parity: mirror the CLI default
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
	case OpSagaCreate:
		if err := svc.CreateSaga(ctx, space.SagaCreateOptions{
			ID:         op.SagaID,
			SpecPath:   op.SpecPath,
			References: repoRefsToSpecs(op.References),
			Memories:   op.Memories,
		}); err != nil {
			return err
		}
		// Parity with `stave saga create`: every saga space carries the
		// embedded stave-saga skill so summoned sessions run the coordinator
		// loop (skill.go lives in summon for exactly this call site).
		return summon.InstallSagaSkill(svc.SpacePath(op.SagaID))
	case OpSagaAdd:
		return svc.SagaAdd(ctx, op.SagaID, op.SpaceID, op.After, false)
	case OpSagaStatus:
		status, err := svc.SagaStatus(ctx, op.SagaID)
		if err != nil {
			return err
		}
		writeSagaStatus(out, status)
		return nil
	case OpSummon:
		summoner := summon.ResolveName(e.Config, op.Summoner)
		svc := summon.NewService(e.Config, e.SummonLauncher, out)
		svc.Interactive = true
		return svc.Summon(ctx, summon.Options{SpaceID: op.SpaceID, Summoner: summoner})
	case OpPortalInit:
		svc := portal.NewService(e.Config, e.PortalRunner, nil)
		if op.Driver == string(portal.DriverDevcontainer) {
			_, err := svc.InitDevcontainer(ctx, portal.InitDevcontainerOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, DevcontainerPath: op.DevcontainerPath, ComposeFiles: op.ComposeFiles, Service: op.Service, ContainerRoot: op.ContainerRoot, Preset: op.Preset})
			return err
		}
		driver := portal.Driver(op.Driver)
		if driver == "" {
			driver = portal.DriverDocker
		}
		_, err := svc.InitContainer(ctx, portal.InitContainerOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Engine: driver, Image: op.Image, ContainerRoot: op.ContainerRoot, Preset: op.Preset})
		return err
	case OpPortalAttach:
		if op.Driver == string(portal.DriverEC2Attach) || op.Driver == "ec2" {
			return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
				return svc.AttachEC2(ctx, portal.AttachEC2Options{SpaceID: op.SpaceID, PortalID: op.PortalID, InstanceID: op.InstanceID, Host: op.Host, Port: op.Port, Region: op.Region, Profile: op.Profile, SSHUser: op.SSHUser, IdentityPath: op.IdentityPath, KnownHostsPath: op.KnownHostsPath, StrictHostKey: op.StrictHostKey, RemoteRoot: op.RemoteRoot, SyncMode: portal.SyncMode(op.SyncMode), Preset: op.Preset})
			})
		}
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.AttachSSH(ctx, portal.AttachSSHOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Host: op.Host, Port: op.Port, IdentityPath: op.IdentityPath, KnownHostsPath: op.KnownHostsPath, StrictHostKey: op.StrictHostKey, RemoteRoot: op.RemoteRoot, SyncMode: portal.SyncMode(op.SyncMode), Preset: op.Preset})
		})
	case OpPortalConfigure:
		svc := portal.NewService(e.Config, e.PortalRunner, nil)
		_, err := svc.Configure(portal.ConfigureOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, SyncMode: portal.SyncMode(op.SyncMode), ContainerRoot: op.ContainerRoot, RemoteRoot: op.RemoteRoot, Host: op.Host, Agent: op.Agent, AuthMode: portal.AuthMode(op.Method)})
		return err
	case OpPortalAuthLogin:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanAuthLogin(ctx, portal.AuthCommandOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Provider: op.Provider, Method: portal.AuthMode(op.Method)})
		})
	case OpPortalAuthInherit:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanAuthInherit(ctx, portal.AuthCommandOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Provider: op.Provider, Method: portal.AuthMode(op.Method), Yes: true})
		})
	case OpPortalAuthRevoke:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanAuthRevoke(ctx, portal.AuthCommandOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Provider: op.Provider, Target: op.Target, Yes: true})
		})
	case OpPortalUp:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanUp(ctx, portal.UpOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		})
	case OpPortalSync:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanSync(ctx, portal.SyncOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Direction: portal.SyncDirection(op.Direction), Mode: portal.SyncMode(op.SyncMode), ReferencesOnly: op.ReferencesOnly, Include: op.Include, Exclude: op.Exclude, Delete: op.Delete, MaxDelete: op.MaxDelete, AllowDirty: op.AllowDirty, Yes: op.Delete})
		})
	case OpPortalSummon:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanSummon(ctx, portal.SummonOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, With: op.Summoner, Mode: op.Mode, Permission: op.Permission, Prompt: op.HandoffPrompt})
		})
	case OpPortalDown:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanDown(ctx, portal.DownOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Timeout: op.Timeout, Force: op.Force})
		})
	case OpPortalDetach:
		svc := portal.NewService(e.Config, e.PortalRunner, nil)
		_, err := svc.Detach(portal.DetachOptions{SpaceID: op.SpaceID, PortalID: op.PortalID})
		return err
	case OpPortalLogs:
		return e.executePortalPlan(ctx, out, func(svc portal.Service) (portal.Plan, error) {
			return svc.PlanLogs(ctx, portal.LogsOptions{SpaceID: op.SpaceID, PortalID: op.PortalID, Agent: op.Agent, Tail: op.Tail, Follow: op.Follow})
		})
	case OpPortalList, OpPortalStatus, OpPortalDoctor, OpPortalInspect, OpPortalAuthStatus, OpPortalDestroyPreview:
		// Read-only portal operations execute against the executor's runner so
		// their answers reflect live runtime state.
		svc := portal.NewService(e.Config, e.PortalRunner, nil)
		payload, _, err := portalReadPayload(ctx, svc, op)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\n", data)
		return nil
	default:
		if isPortalOperation(op) {
			return fmt.Errorf("portal operation %q is unsupported for execution", op.Type)
		}
		return fmt.Errorf("unsupported operation %q", op.Type)
	}
}

func (e Executor) executePortalPlan(ctx context.Context, out io.Writer, build func(portal.Service) (portal.Plan, error)) error {
	svc := portal.NewService(e.Config, e.PortalRunner, nil)
	plan, err := build(svc)
	if err != nil {
		return err
	}
	for _, command := range plan.Commands {
		fmt.Fprintf(out, "%s\n", command.String())
		if e.PortalRunner != nil {
			command.Stream = true
			result, err := e.PortalRunner.Run(ctx, command)
			if result.Stdout != "" {
				_, _ = fmt.Fprint(out, result.Stdout)
			}
			if result.Stderr != "" {
				_, _ = fmt.Fprint(out, result.Stderr)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
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

func writeSagaStatus(out io.Writer, status space.SagaStatus) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "saga %s (%d members)\n", status.SagaID, len(status.Members))
	for _, member := range status.Members {
		fmt.Fprintf(&b, "%s [%s]", member.ID, member.State)
		if len(member.After) > 0 {
			fmt.Fprintf(&b, " after: %s", strings.Join(member.After, ", "))
		}
		fmt.Fprintln(&b)
		for _, repo := range member.Repos {
			fmt.Fprintf(&b, "  %s branch %s base %s (%s)\n", repo.Name, repo.Branch, repo.Base, repo.BaseHealth)
		}
	}
	for _, note := range status.Notes {
		fmt.Fprintf(&b, "note [%s] %s\n", note.Kind, note.Text)
	}
	_, _ = out.Write(b.Bytes())
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
