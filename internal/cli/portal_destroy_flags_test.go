package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func findSubcommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cur := root
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cur.Name())
		}
		cur = next
	}
	return cur
}

// BF2: the dead --delete-remote-data flag is removed; --delete-volumes stays.
func TestPortalDestroyDropsDeleteRemoteDataFlag(t *testing.T) {
	root := newRootCommand(&app{})
	destroy := findSubcommand(t, root, "portal", "destroy")
	if destroy.Flags().Lookup("delete-remote-data") != nil {
		t.Fatal("--delete-remote-data should have been removed")
	}
	if destroy.Flags().Lookup("delete-volumes") == nil {
		t.Fatal("--delete-volumes must still be registered")
	}
}
