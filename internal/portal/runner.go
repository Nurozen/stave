package portal

import (
	"context"
	"fmt"
	"slices"
)

type driverRunner struct {
	base     Runner
	driver   Driver
	programs []string
}

func NewDockerRunner(base Runner) Runner {
	return driverRunner{base: defaultRunner(base), driver: DriverDocker, programs: []string{"docker"}}
}

func NewDevcontainerRunner(base Runner) Runner {
	return driverRunner{base: defaultRunner(base), driver: DriverDevcontainer, programs: []string{"devcontainer", "sh"}}
}

func NewSSHRunner(base Runner) Runner {
	return driverRunner{base: defaultRunner(base), driver: DriverSSH, programs: []string{"ssh", "rsync"}}
}

func NewRuntimeRunner(driver Driver, base Runner) (Runner, error) {
	switch driver {
	case DriverDocker:
		return NewDockerRunner(base), nil
	case DriverDevcontainer:
		return NewDevcontainerRunner(base), nil
	case DriverSSH, DriverEC2Attach:
		return NewSSHRunner(base), nil
	default:
		return nil, fmt.Errorf("driver %q is not supported by runtime runner", driver)
	}
}

func (r driverRunner) LookPath(name string) (string, error) {
	if !slices.Contains(r.programs, name) {
		return "", fmt.Errorf("%s runner cannot resolve %q", r.driver, name)
	}
	return r.base.LookPath(name)
}

func (r driverRunner) Run(ctx context.Context, command Command) (RunResult, error) {
	if !slices.Contains(r.programs, command.Program) {
		return RunResult{}, fmt.Errorf("%s runner cannot execute %q", r.driver, command.Program)
	}
	return r.base.Run(ctx, command)
}

func defaultRunner(base Runner) Runner {
	if base == nil {
		return localRunner{}
	}
	return base
}
