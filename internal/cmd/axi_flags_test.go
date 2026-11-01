package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// The covered commands, and only those, take --axi.
func TestAxiFlagOnCoveredCommands(t *testing.T) {
	covered := []*cobra.Command{statusCmd, hookCmd, hookStatusCmd, mqListCmd, polecatListCmd, projectFeatureStatusCmd, reviewListCmd}
	for _, c := range covered {
		if c.Flags().Lookup("axi") == nil {
			t.Errorf("%s has no --axi flag", c.CommandPath())
		}
		if f := c.Flags().Lookup("axi"); f != nil && f.DefValue != "false" {
			t.Errorf("%s: --axi defaults to %s", c.CommandPath(), f.DefValue)
		}
	}
	if mqStatusCmd.Flags().Lookup("axi") != nil {
		t.Error("gt mq status is not covered and must not take --axi")
	}
}
