package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Nurozen/stave/internal/cli"
)

// Build metadata, populated at release time via
// -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
// (see .goreleaser.yml). They stay in package main so those ldflags resolve.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	if err := run(); err != nil {
		code := 1
		if exitErr, ok := err.(interface {
			ExitCode() int
			Silent() bool
		}); ok {
			code = exitErr.ExitCode()
			if !exitErr.Silent() {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(code)
	}
}

func run() error {
	// Cancel in-flight subprocesses (ssh, docker, rsync) on Ctrl-C/SIGTERM
	// instead of leaving them orphaned.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cli.BuildVersion, cli.BuildCommit, cli.BuildDate = version, commit, date
	return cli.ExecuteContext(ctx)
}
