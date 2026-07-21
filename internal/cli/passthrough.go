package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// parsePassthroughArgs parses command arguments after Cobra stops at the first
// positional. Stave-owned flags are still applied to cmd.Flags(), while
// unknown trailing arguments are preserved for a summoned agent.
//
// passthroughEnabled is evaluated as flags are parsed so
// `--summon codex --yolo` enables forwarding, while a typo without --summon is
// still rejected. A literal `--` forces every remaining argument through to
// the agent, including names that collide with Stave flags.
func parsePassthroughArgs(
	cmd *cobra.Command,
	raw []string,
	minPositionals int,
	maxPositionals int,
	passthroughEnabled func() bool,
) ([]string, []string, error) {
	positionals := make([]string, 0, maxPositionals)
	extra := make([]string, 0)
	forwardOnly := false
	forwardingStarted := false

	for i := 0; i < len(raw); {
		token := raw[i]
		if forwardOnly {
			extra = append(extra, token)
			i++
			continue
		}
		if token == "--" {
			if !passthroughEnabled() {
				return nil, nil, fmt.Errorf("agent arguments require --summon")
			}
			forwardOnly = true
			forwardingStarted = true
			i++
			continue
		}

		flag, value, hasValue, recognized := lookupCommandFlag(cmd, token)
		if recognized {
			if flag.Name == "help" {
				return nil, nil, pflag.ErrHelp
			}
			consumed := 1
			if !hasValue {
				if flag.NoOptDefVal != "" {
					value = flag.NoOptDefVal
				} else {
					if i+1 >= len(raw) {
						return nil, nil, fmt.Errorf("flag needs an argument: %s", token)
					}
					value = raw[i+1]
					consumed = 2
				}
			}
			if err := cmd.Flags().Set(flag.Name, value); err != nil {
				return nil, nil, err
			}
			i += consumed
			continue
		}

		if !looksLikeFlag(token) && len(positionals) < maxPositionals && !forwardingStarted {
			positionals = append(positionals, token)
			i++
			continue
		}
		if !passthroughEnabled() {
			if looksLikeFlag(token) {
				return nil, nil, fmt.Errorf("unknown flag: %s\nRun '%s --help' for usage", token, cmd.CommandPath())
			}
			return nil, nil, fmt.Errorf("unexpected argument %q", token)
		}
		extra = append(extra, token)
		forwardingStarted = true
		i++
	}

	if len(positionals) < minPositionals {
		return nil, nil, fmt.Errorf("requires at least %d arg(s), only received %d", minPositionals, len(positionals))
	}
	return positionals, extra, nil
}

func lookupCommandFlag(cmd *cobra.Command, token string) (*pflag.Flag, string, bool, bool) {
	if strings.HasPrefix(token, "--") && len(token) > 2 {
		nameValue := token[2:]
		name, value, hasValue := strings.Cut(nameValue, "=")
		flag := cmd.Flags().Lookup(name)
		return flag, value, hasValue, flag != nil
	}
	if strings.HasPrefix(token, "-") && len(token) > 1 && token != "-" {
		flag := cmd.Flags().ShorthandLookup(token[1:2])
		if flag == nil {
			return nil, "", false, false
		}
		remainder := token[2:]
		if strings.HasPrefix(remainder, "=") {
			return flag, remainder[1:], true, true
		}
		if remainder != "" {
			return flag, remainder, true, true
		}
		return flag, "", false, true
	}
	return nil, "", false, false
}

func looksLikeFlag(value string) bool {
	return len(value) > 1 && value[0] == '-'
}
