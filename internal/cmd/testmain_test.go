//go:build !integration

package cmd

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

func TestMain(m *testing.M) {
	// Tests that run the real bd against a t.TempDir() .beads would otherwise
	// make bd auto-start a dolt sql-server that outlives the test (hq-j47).
	_ = os.Setenv("BEADS_DOLT_AUTO_START", "0")

	// Commands run in process from internal/cmd. Town detection finds the
	// repo's internal/mayor package directory as a town root, and the
	// registry setup would then start a herdr server named internal-<hash>
	// when GT_MULTIPLEXER=herdr is set in the caller's environment. Tests
	// use tmux, so pin it.
	_ = os.Setenv(tmux.EnvMultiplexer, string(tmux.BackendTmux))

	code := m.Run()

	// Safety net: if a test still switched to herdr, stop that server so no
	// process outlives the test run.
	if tmux.IsHerdr() {
		_ = tmux.NewTmux().KillServer()
	}
	os.Exit(code)
}
