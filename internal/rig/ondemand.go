package rig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// PolecatSessionAlive reports whether a polecat's session is running. It is a
// variable so tests can stub it.
var PolecatSessionAlive = func(rigName, polecat string) bool {
	ok, err := tmux.NewTmux().HasSession(session.PolecatSessionName(session.PrefixFor(rigName), polecat))
	return err == nil && ok
}

// PolecatNames lists the rig's polecat directories under
// <townRoot>/<rig>/polecats/ (hidden entries and files are ignored).
func PolecatNames(townRoot, rigName string) []string {
	entries, err := os.ReadDir(filepath.Join(townRoot, rigName, "polecats"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}

// HasLivePolecats reports whether any of the rig's polecats has a running
// session. Polecat directories outlive their sessions (finished polecats are
// kept for reuse), so a directory alone is not work. A polecat whose session
// died with work still assigned is caught by the daemon's stop-time check.
func HasLivePolecats(townRoot, rigName string) bool {
	for _, name := range PolecatNames(townRoot, rigName) {
		if PolecatSessionAlive(rigName, name) {
			return true
		}
	}
	return false
}

// HasPendingRefineryEvents reports whether events/refinery holds a pending
// .event file addressed to the rig (payload.rig equals rigName). The refinery
// channel is town-wide, so events without a rig payload are not attributed to
// any rig here; emitters of MQ_SUBMIT/MERGE_READY events include rig=<name>.
func HasPendingRefineryEvents(townRoot, rigName string) bool {
	dir := filepath.Join(townRoot, "events", "refinery")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".event") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: path built from town events dir
		if err != nil {
			continue
		}
		var ev struct {
			Payload map[string]string `json:"payload"`
		}
		if json.Unmarshal(data, &ev) == nil && ev.Payload["rig"] == rigName {
			return true
		}
	}
	return false
}

// WitnessHasWork reports whether an on-demand rig needs a witness right now.
func WitnessHasWork(townRoot, rigName string) bool {
	return HasLivePolecats(townRoot, rigName)
}

// RefineryHasWork reports whether an on-demand rig needs a refinery right now:
// a pending refinery event for the rig, or running polecats that will submit
// MRs.
// Open MR beads are deliberately not queried here (too costly per heartbeat
// across ~120 rigs); callers that are about to stop a refinery check the queue
// themselves.
func RefineryHasWork(townRoot, rigName string) bool {
	return HasPendingRefineryEvents(townRoot, rigName) || HasLivePolecats(townRoot, rigName)
}
