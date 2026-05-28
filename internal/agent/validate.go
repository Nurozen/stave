package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nurozen/stave/internal/config"
	"github.com/Nurozen/stave/internal/space"
	"github.com/Nurozen/stave/internal/summon"
)

func ValidatePlan(cfg config.Config, plan Plan) error {
	if len(plan.Operations) == 0 {
		return nil
	}
	plannedSpaces := map[string]bool{}
	for i, op := range plan.Operations {
		if err := validateOperation(cfg, op, plannedSpaces); err != nil {
			return fmt.Errorf("operation %d (%s): %w", i+1, op.Type, err)
		}
		if op.Type == OpSpaceCreate {
			plannedSpaces[op.SpaceID] = true
		}
	}
	return nil
}

func validateOperation(cfg config.Config, op Operation, plannedSpaces map[string]bool) error {
	switch op.Type {
	case OpSpaceCreate:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if plannedSpaces[op.SpaceID] {
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
	case OpSpaceAdd:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if !spaceExists(cfg, op.SpaceID) {
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
		manifest, err := space.LoadManifest(filepath.Join(cfg.AgentWorkDir, op.SpaceID))
		if err != nil {
			return err
		}
		repoPath := op.Repo
		if op.Mode == string(space.ModeReference) {
			repoPath = filepath.Join("references", op.Repo)
		}
		if manifest.HasPath(repoPath) {
			return fmt.Errorf("repo path %q already exists in space %q", repoPath, op.SpaceID)
		}
	case OpSpaceSync, OpSpaceStatus:
		if !spaceExists(cfg, op.SpaceID) {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
	case OpSummon:
		if err := config.ValidateName("space id", op.SpaceID); err != nil {
			return err
		}
		if !spaceExists(cfg, op.SpaceID) && !plannedSpaces[op.SpaceID] {
			return fmt.Errorf("space %q does not exist", op.SpaceID)
		}
		summoner := summon.ResolveName(cfg, op.Summoner)
		if err := summon.ValidateSummoner(summoner); err != nil {
			return err
		}
	case OpReposList:
		return nil
	case OpReposSync:
		if op.Repo != "" {
			if _, ok := cfg.Repos[op.Repo]; !ok {
				return fmt.Errorf("repo %q is not registered", op.Repo)
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

func spaceExists(cfg config.Config, id string) bool {
	_, err := os.Stat(filepath.Join(cfg.AgentWorkDir, id, space.ManifestName))
	return err == nil
}
