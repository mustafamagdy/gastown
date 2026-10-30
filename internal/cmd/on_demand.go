package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Fast start for on-demand rigs (rigs in a project). The daemon starts and
// stops these agents on its heartbeat; these helpers start them immediately
// when work appears. Failures are only logged: the daemon retries.
// Hooks are variables so tests can observe calls without spawning agents.
var (
	startWitnessForRigFn = func(r *rig.Rig) error {
		err := witness.NewManager(r).Start(false, "", nil)
		if errors.Is(err, witness.ErrAlreadyRunning) {
			return nil
		}
		return err
	}
	startRefineryForRigFn = func(r *rig.Rig) error {
		err := refinery.NewManager(r).Start(false, "")
		if errors.Is(err, refinery.ErrAlreadyRunning) {
			return nil
		}
		return err
	}
)

func ensureOnDemandWitness(townRoot, rigName string) {
	ensureOnDemandAgent(townRoot, rigName, "witness", startWitnessForRigFn)
}

func ensureOnDemandRefinery(townRoot, rigName string) {
	ensureOnDemandAgent(townRoot, rigName, "refinery", startRefineryForRigFn)
}

func ensureOnDemandAgent(townRoot, rigName, role string, start func(*rig.Rig) error) {
	if townRoot == "" {
		var err error
		if townRoot, err = workspace.FindFromCwd(); err != nil || townRoot == "" {
			return
		}
	}
	if !config.RigOnDemand(townRoot, rigName) {
		return
	}
	if blocked, _ := IsRigParkedOrDocked(townRoot, rigName); blocked {
		return
	}
	r := &rig.Rig{Name: rigName, Path: filepath.Join(townRoot, rigName)}
	if err := start(r); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not start on-demand %s for %s (daemon will retry): %v\n", role, rigName, err)
	}
}
