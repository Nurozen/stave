package summon

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/Nurozen/stave/internal/config"
)

// The stave-saga skill ships inside the binary and is written into each saga
// space as a project-level Claude Code skill, so summoned sessions can run
// the coordinator loop on any machine without personal skills. It lives in
// this package (not internal/cli) so the agent executor can install it too.
//
//go:embed assets/skills/stave-saga/SKILL.md
var sagaSkill []byte

// InstallSagaSkill writes the embedded stave-saga skill into the space at
// .claude/skills/stave-saga/SKILL.md, the path defaultPrompt probes before
// launching Claude straight into the skill.
func InstallSagaSkill(spacePath string) error {
	skillDir := filepath.Join(spacePath, ".claude", "skills", SagaSkillName)
	if err := os.MkdirAll(skillDir, config.DefaultDirMode); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillDir, "SKILL.md"), sagaSkill, 0o644)
}
