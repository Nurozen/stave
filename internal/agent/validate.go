package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/portal"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
)

// plannedSpace records what the plan's earlier operations promise about one
// space so later operations can validate against state that does not exist on
// disk yet: whether the plan creates it (and as what kind), the repo paths the
// plan occupies inside it, and the edit-branch names the plan creates.
type plannedSpace struct {
	// created marks a space_create/saga_create earlier in the plan; a record
	// without it only accumulates space_add effects on an on-disk space.
	created bool
	isSaga  bool
	paths   map[string]bool
	// branches holds bare branch names ("stave/<id>/<repo>").
	branches map[string]bool
}

func newPlannedSpace() *plannedSpace {
	return &plannedSpace{paths: map[string]bool{}, branches: map[string]bool{}}
}

// plannedCreated reports whether the plan creates space id.
func plannedCreated(planned map[string]*plannedSpace, id string) bool {
	record := planned[id]
	return record != nil && record.created
}

// recordPlannedOp folds one already-validated operation into the planned-space
// records. It is the single reducer shared by ValidatePlan and the
// dispatcher's queue-time base-sugar preflight (plannedSpacesFromOps).
func recordPlannedOp(planned map[string]*plannedSpace, op Operation) {
	switch op.Type {
	case OpSpaceCreate:
		record := newPlannedSpace()
		record.created = true
		for _, ref := range op.Edits {
			record.paths[ref.Name] = true
			record.branches[space.DefaultBranch(op.SpaceID, ref.Name)] = true
		}
		for _, ref := range op.References {
			record.paths[filepath.Join("references", ref.Name)] = true
		}
		planned[op.SpaceID] = record
	case OpSagaCreate:
		record := newPlannedSpace()
		record.created = true
		record.isSaga = true
		for _, ref := range op.References {
			record.paths[filepath.Join("references", ref.Name)] = true
		}
		planned[op.SagaID] = record
	case OpSpaceAdd:
		record := planned[op.SpaceID]
		if record == nil {
			record = newPlannedSpace()
			planned[op.SpaceID] = record
		}
		if op.Mode == string(space.ModeReference) {
			record.paths[filepath.Join("references", op.Repo)] = true
			return
		}
		record.paths[op.Repo] = true
		branch := op.Branch
		if branch == "" {
			branch = space.DefaultBranch(op.SpaceID, op.Repo)
		}
		record.branches[branch] = true
	}
}

// plannedSpacesFromOps replays recordPlannedOp over a whole operation list.
func plannedSpacesFromOps(ops []Operation) map[string]*plannedSpace {
	planned := map[string]*plannedSpace{}
	for _, op := range ops {
		recordPlannedOp(planned, op)
	}
	return planned
}

func ValidatePlan(cfg config.Config, plan Plan) error {
	if len(plan.Operations) == 0 {
		return nil
	}
	plannedSpaces := map[string]*plannedSpace{}
	plannedPortals := map[string]string{}
	plannedAuth := map[string]bool{}
	for i, op := range plan.Operations {
		if err := validateOperation(cfg, op, plannedSpaces, plannedPortals, plannedAuth); err != nil {
			return fmt.Errorf("operation %d (%s): %w", i+1, op.Type, err)
		}
		recordPlannedOp(plannedSpaces, op)
		if op.Type == OpPortalInit || op.Type == OpPortalAttach {
			plannedPortals[portalPlanKey(op.SpaceID, op.PortalID)] = op.Driver
		}
		if op.Type == OpPortalAuthLogin || op.Type == OpPortalAuthInherit {
			plannedAuth[portalAuthPlanKey(op.SpaceID, op.PortalID, op.Provider)] = true
		}
	}
	return nil
}

func validateOperation(cfg config.Config, op Operation, plannedSpaces map[string]*plannedSpace, plannedPortals map[string]string, plannedAuth map[string]bool) error {
	switch op.Type {
	case OpSpaceCreate:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if op.Kind == space.KindSaga {
			return fmt.Errorf("kind %q is reserved for sagas; use stave_saga_create", space.KindSaga)
		}
		if plannedCreated(plannedSpaces, op.SpaceID) {
			return fmt.Errorf("space %q is already planned for creation", op.SpaceID)
		}
		if spaceExists(cfg, op.SpaceID) {
			return fmt.Errorf("space %q already exists", op.SpaceID)
		}
		paths := map[string]struct{}{}
		for _, ref := range op.Edits {
			if err := validateRepoRef(cfg, ref); err != nil {
				return err
			}
			if err := recordPath(paths, ref.Name); err != nil {
				return err
			}
		}
		for _, ref := range op.References {
			if err := validateRepoRef(cfg, ref); err != nil {
				return err
			}
			if err := recordPath(paths, filepath.Join("references", ref.Name)); err != nil {
				return err
			}
		}
		if op.SpecPath != "" {
			if _, err := os.Stat(op.SpecPath); err != nil {
				return fmt.Errorf("spec path %q: %w", op.SpecPath, err)
			}
		}
		for _, raw := range op.Memories {
			if err := validateMemorySpec(raw); err != nil {
				return err
			}
		}
		if op.MemoryFate != "" {
			switch strings.ToLower(op.MemoryFate) {
			case "keep", "destroy", "contribute":
			default:
				return fmt.Errorf("memory_fate %q must be keep, destroy, or contribute", op.MemoryFate)
			}
		}
	case OpSpaceAdd:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		planned := plannedCreated(plannedSpaces, op.SpaceID)
		if !spaceExists(cfg, op.SpaceID) && !planned {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
		if _, ok := cfg.Repos[op.Repo]; !ok {
			return fmt.Errorf("repo %q is not registered", op.Repo)
		}
		if op.Mode != string(space.ModeEdit) && op.Mode != string(space.ModeReference) {
			return fmt.Errorf("mode must be edit or reference")
		}
		if err := validateRefish("base", op.Base); err != nil {
			return err
		}
		if err := validateRefish("ref", op.Ref); err != nil {
			return err
		}
		if err := validateRefish("branch", op.Branch); err != nil {
			return err
		}
		repoPath := op.Repo
		if op.Mode == string(space.ModeReference) {
			repoPath = filepath.Join("references", op.Repo)
		}
		if planned {
			record := plannedSpaces[op.SpaceID]
			if record.isSaga && op.Mode == string(space.ModeEdit) {
				return fmt.Errorf("saga space %q holds no edit worktrees; add the repo to a member space instead", op.SpaceID)
			}
			if record.paths[repoPath] {
				return fmt.Errorf("repo path %q already exists in space %q", repoPath, op.SpaceID)
			}
			return nil
		}
		manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, op.SpaceID))
		if err != nil {
			return err
		}
		if manifest.Saga != nil && op.Mode == string(space.ModeEdit) {
			return fmt.Errorf("saga space %q holds no edit worktrees; add the repo to a member space instead", op.SpaceID)
		}
		if manifest.HasPath(repoPath) {
			return fmt.Errorf("repo path %q already exists in space %q", repoPath, op.SpaceID)
		}
		if record := plannedSpaces[op.SpaceID]; record != nil && record.paths[repoPath] {
			return fmt.Errorf("repo path %q is already planned for space %q", repoPath, op.SpaceID)
		}
	case OpSpaceSync, OpSpaceStatus:
		if err := config.ValidateSpaceID(op.SpaceID); err != nil {
			return err
		}
		if !spaceExists(cfg, op.SpaceID) {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
	case OpSummon:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if !spaceExists(cfg, op.SpaceID) && !plannedCreated(plannedSpaces, op.SpaceID) {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
		summoner := summon.ResolveName(cfg, op.Summoner)
		if err := summon.ValidateSummoner(summoner); err != nil {
			return err
		}
	case OpSagaCreate:
		if err := config.ValidateName("space id", op.SagaID); err != nil {
			return err
		}
		if plannedCreated(plannedSpaces, op.SagaID) {
			return fmt.Errorf("space %q is already planned for creation", op.SagaID)
		}
		if spaceExists(cfg, op.SagaID) {
			return fmt.Errorf("space %q already exists", op.SagaID)
		}
		if len(op.Edits) > 0 {
			return fmt.Errorf("sagas hold no edit worktrees; create members with stave_space_create and register them with stave_saga_add")
		}
		paths := map[string]struct{}{}
		for _, ref := range op.References {
			if err := validateRepoRef(cfg, ref); err != nil {
				return err
			}
			if err := recordPath(paths, filepath.Join("references", ref.Name)); err != nil {
				return err
			}
		}
		if op.SpecPath != "" {
			if _, err := os.Stat(op.SpecPath); err != nil {
				return fmt.Errorf("spec path %q: %w", op.SpecPath, err)
			}
		}
		for _, raw := range op.Memories {
			if err := validateMemorySpec(raw); err != nil {
				return err
			}
		}
	case OpSagaStatus:
		if err := config.ValidateSpaceID(op.SagaID); err != nil {
			return err
		}
		if record := plannedSpaces[op.SagaID]; record != nil && record.created {
			if !record.isSaga {
				return fmt.Errorf("space %q is not a saga", op.SagaID)
			}
			return nil
		}
		return validateSagaOnDisk(cfg, op.SagaID)
	case OpSagaAdd:
		if err := config.ValidateName("saga id", op.SagaID); err != nil {
			return err
		}
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if op.SagaID == op.SpaceID {
			return fmt.Errorf("saga %q cannot be its own member", op.SagaID)
		}
		if record := plannedSpaces[op.SagaID]; record != nil && record.created {
			if !record.isSaga {
				return fmt.Errorf("space %q is created in this plan but is not a saga; use stave_saga_create", op.SagaID)
			}
		} else if err := validateSagaOnDisk(cfg, op.SagaID); err != nil {
			return err
		}
		if record := plannedSpaces[op.SpaceID]; record != nil && record.created {
			if record.isSaga {
				return fmt.Errorf("space %q is itself a saga; sagas cannot be members", op.SpaceID)
			}
		} else if !spaceExists(cfg, op.SpaceID) {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
		for _, id := range op.After {
			if err := config.ValidateName("member id", id); err != nil {
				return err
			}
		}
	case OpReposList:
		return nil
	case OpReposSync:
		if op.Repo != "" {
			if _, ok := cfg.Repos[op.Repo]; !ok {
				return fmt.Errorf("repo %q is not registered", op.Repo)
			}
		}
	case OpPortalList:
		if op.SpaceID != "" {
			return validatePortalSpace(cfg, op.SpaceID, plannedSpaces)
		}
	case OpPortalStatus, OpPortalDoctor, OpPortalInspect, OpPortalAuthStatus, OpPortalLogs, OpPortalDestroyPreview:
		if err := validatePortalSpace(cfg, op.SpaceID, plannedSpaces); err != nil {
			return err
		}
		if op.Type == OpPortalLogs {
			if op.Follow {
				return fmt.Errorf("portal logs follow mode is unsupported for agent planning")
			}
			if op.Tail <= 0 {
				return fmt.Errorf("portal logs requires a positive tail")
			}
		}
		return validatePortalExists(cfg, op, plannedPortals)
	case OpPortalInit:
		if err := validatePortalSpace(cfg, op.SpaceID, plannedSpaces); err != nil {
			return err
		}
		if err := validatePortalID(op.PortalID); err != nil {
			return err
		}
		if op.Driver != string(portal.DriverDocker) && op.Driver != string(portal.DriverDevcontainer) {
			return fmt.Errorf("portal init driver must be docker or devcontainer")
		}
		if portalExists(cfg, op.SpaceID, op.PortalID) || plannedPortals[portalPlanKey(op.SpaceID, op.PortalID)] != "" {
			return fmt.Errorf("portal %q already exists in space %q", portalID(op.PortalID), op.SpaceID)
		}
		if op.Repo != "" {
			if _, ok := cfg.Repos[op.Repo]; !ok {
				return fmt.Errorf("repo %q is not registered", op.Repo)
			}
		}
		return validatePortalPaths(op)
	case OpPortalAttach:
		if err := validatePortalSpace(cfg, op.SpaceID, plannedSpaces); err != nil {
			return err
		}
		if err := validatePortalID(op.PortalID); err != nil {
			return err
		}
		if op.Driver != string(portal.DriverSSH) && op.Driver != string(portal.DriverEC2Attach) {
			return fmt.Errorf("portal attach driver must be ssh or ec2-attach")
		}
		if op.Driver == string(portal.DriverSSH) && op.Host == "" {
			return fmt.Errorf("ssh portal attach requires host")
		}
		if op.Driver == string(portal.DriverEC2Attach) && op.InstanceID == "" {
			return fmt.Errorf("ec2-attach portal attach requires instance_id")
		}
		if portalExists(cfg, op.SpaceID, op.PortalID) || plannedPortals[portalPlanKey(op.SpaceID, op.PortalID)] != "" {
			return fmt.Errorf("portal %q already exists in space %q", portalID(op.PortalID), op.SpaceID)
		}
		return validatePortalPaths(op)
	case OpPortalConfigure, OpPortalAuthLogin, OpPortalAuthInherit, OpPortalAuthRevoke, OpPortalUp, OpPortalSync, OpPortalSummon, OpPortalDown, OpPortalDetach:
		if err := validatePortalSpace(cfg, op.SpaceID, plannedSpaces); err != nil {
			return err
		}
		driver, err := portalDriverForPlan(cfg, op, plannedPortals)
		if err != nil {
			return err
		}
		if err := validatePortalPaths(op); err != nil {
			return err
		}
		if op.Type == OpPortalAuthInherit && op.Method == string(portal.AuthCopyCache) {
			return fmt.Errorf("agent-planned portal auth inherit cannot use copy-cache")
		}
		if op.Type == OpPortalDown && isAttachOnlyPortalDriver(driver) {
			return fmt.Errorf("portal down is only valid for Stave-owned local runtimes; use portal detach for attach-only portals")
		}
		if op.Type == OpPortalDetach && !isAttachOnlyPortalDriver(driver) {
			return fmt.Errorf("portal detach is only valid for attach-only portals")
		}
		if op.Type == OpPortalSync && op.Delete && op.MaxDelete <= 0 {
			return fmt.Errorf("portal sync delete requires a positive max_delete")
		}
		if op.Type == OpPortalSummon {
			if err := validatePortalSummonAuth(cfg, op, plannedAuth); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported operation")
	}
	return nil
}

func validateRepoRef(cfg config.Config, ref RepoRef) error {
	if _, ok := cfg.Repos[ref.Name]; !ok {
		return fmt.Errorf("repo %q is not registered", ref.Name)
	}
	return validateRefish("ref", ref.Ref)
}

func recordPath(paths map[string]struct{}, path string) error {
	if _, exists := paths[path]; exists {
		return fmt.Errorf("repo path %q is duplicated", path)
	}
	paths[path] = struct{}{}
	return nil
}

func validateRefish(label, value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s %q contains whitespace", label, value)
	}
	if value == "@" || strings.Contains(value, "@{") || strings.Contains(value, "..") {
		return fmt.Errorf("%s %q is not a valid git ref", label, value)
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") {
		return fmt.Errorf("%s %q is not a valid git ref", label, value)
	}
	if strings.ContainsAny(value, "~^:?*[\\") {
		return fmt.Errorf("%s %q contains invalid git ref characters", label, value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("%s %q is not a valid git ref", label, value)
		}
	}
	return nil
}

// validateSagaOnDisk requires id to be an on-disk space whose manifest marks
// it a saga.
func validateSagaOnDisk(cfg config.Config, id string) error {
	if !spaceExists(cfg, id) {
		return fmt.Errorf("saga %q does not exist", id)
	}
	manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, id))
	if err != nil {
		return err
	}
	if manifest.Saga == nil {
		return fmt.Errorf("space %q is not a saga", id)
	}
	return nil
}

func spaceExists(cfg config.Config, id string) bool {
	if config.ValidateSpaceID(id) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(cfg.AgentWorkDir, id, space.ManifestName))
	return err == nil
}

func validatePortalSpace(cfg config.Config, id string, plannedSpaces map[string]*plannedSpace) error {
	if err := config.ValidateName("space id", id); err != nil {
		return err
	}
	if !spaceExists(cfg, id) && !plannedCreated(plannedSpaces, id) {
		return fmt.Errorf("space %q does not exist", id)
	}
	return nil
}

func validatePortalID(id string) error {
	if id == "" {
		return nil
	}
	return config.ValidateName("portal id", id)
}

func portalPlanKey(spaceID, portalIDValue string) string {
	return spaceID + "\x00" + portalID(portalIDValue)
}

func portalAuthPlanKey(spaceID, portalIDValue, provider string) string {
	return portalPlanKey(spaceID, portalIDValue) + "\x00" + provider
}

func portalExists(cfg config.Config, spaceID, portalIDValue string) bool {
	manifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, spaceID))
	if err != nil {
		return false
	}
	_, ok := manifest.Portals[portalID(portalIDValue)]
	return ok
}

func portalDriverForPlan(cfg config.Config, op Operation, plannedPortals map[string]string) (string, error) {
	key := portalPlanKey(op.SpaceID, op.PortalID)
	if driver := plannedPortals[key]; driver != "" {
		return driver, nil
	}
	manifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, op.SpaceID))
	if err != nil {
		return "", fmt.Errorf("portal %q does not exist in space %q", portalID(op.PortalID), op.SpaceID)
	}
	item, ok := manifest.Portals[portalID(op.PortalID)]
	if !ok {
		return "", fmt.Errorf("portal %q does not exist in space %q", portalID(op.PortalID), op.SpaceID)
	}
	return string(item.Driver), nil
}

func validatePortalExists(cfg config.Config, op Operation, plannedPortals map[string]string) error {
	_, err := portalDriverForPlan(cfg, op, plannedPortals)
	return err
}

func validatePortalSummonAuth(cfg config.Config, op Operation, plannedAuth map[string]bool) error {
	provider := op.Summoner
	if provider == "" {
		provider = summon.Codex
	}
	if plannedAuth[portalAuthPlanKey(op.SpaceID, op.PortalID, provider)] {
		return nil
	}
	manifest, err := portal.LoadManifest(filepath.Join(cfg.AgentWorkDir, op.SpaceID))
	if err != nil {
		return fmt.Errorf("portal summon requires confirmed %s auth or a preceding portal auth login/inherit operation", provider)
	}
	item, ok := manifest.Portals[portalID(op.PortalID)]
	if !ok {
		return fmt.Errorf("portal %q does not exist in space %q", portalID(op.PortalID), op.SpaceID)
	}
	for _, auth := range item.Auth.Providers {
		if auth.Provider == provider && auth.Status == portal.AuthOK {
			return nil
		}
	}
	return fmt.Errorf("portal summon requires confirmed %s auth or a preceding portal auth login/inherit operation", provider)
}

func isAttachOnlyPortalDriver(driver string) bool {
	return driver == string(portal.DriverSSH) || driver == string(portal.DriverEC2Attach)
}

func validatePortalPaths(op Operation) error {
	for label, value := range map[string]string{
		"identity_path":     op.IdentityPath,
		"remote_root":       op.RemoteRoot,
		"container_root":    op.ContainerRoot,
		"devcontainer_path": op.DevcontainerPath,
		"workdir":           op.Workdir,
	} {
		if err := validateSafePortalPath(label, value); err != nil {
			return err
		}
	}
	for _, value := range op.ComposeFiles {
		if err := validateSafePortalPath("compose_file", value); err != nil {
			return err
		}
	}
	return nil
}

func validateSafePortalPath(label, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s %q contains unsafe characters", label, value)
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return fmt.Errorf("%s %q is not a safe portal path", label, value)
	}
	if strings.Contains(value, "*") || strings.Contains(value, "?") {
		return fmt.Errorf("%s %q must not contain glob characters", label, value)
	}
	return nil
}

// validateMemorySpec accepts "." or [provider:]id using the same name charset
// as space/repo ids (config.ValidateName).
func validateMemorySpec(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("memory spec is empty")
	}
	if raw == "." {
		return nil
	}
	provider, spec, found := strings.Cut(raw, ":")
	if !found {
		if raw == "marmot" {
			return nil
		}
		return config.ValidateName("memory id", raw)
	}
	if err := config.ValidateName("memory provider", provider); err != nil {
		return err
	}
	if spec == "" || spec == "." {
		return nil
	}
	return config.ValidateName("memory id", spec)
}
