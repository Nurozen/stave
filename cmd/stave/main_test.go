package main

import (
	"os"
	"testing"
)

func TestRunHelp(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"stave", "--help"}
	if err := run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestMainHelp(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"stave", "--help"}
	main()
}
