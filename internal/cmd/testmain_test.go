//go:build !integration

package cmd

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tests that run the real bd against a t.TempDir() .beads would otherwise
	// make bd auto-start a dolt sql-server that outlives the test (hq-j47).
	_ = os.Setenv("BEADS_DOLT_AUTO_START", "0")
	os.Exit(m.Run())
}
