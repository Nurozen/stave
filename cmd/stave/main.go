package main

import (
	"fmt"
	"os"

	"github.com/Nurozen/stave/internal/cli"
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
	return cli.NewRootCommand().Execute()
}
