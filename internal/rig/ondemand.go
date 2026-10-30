package rig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HasPolecats reports whether the rig has at least one polecat directory
// under <townRoot>/<rig>/polecats/ (hidden entries are ignored).
// This is the "witness has work" signal for on-demand rigs.
func HasPolecats(townRoot, rigName string) bool {
	entries, err := os.ReadDir(filepath.Join(townRoot, rigName, "polecats"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
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
	return HasPolecats(townRoot, rigName)
}

// RefineryHasWork reports whether an on-demand rig needs a refinery right now:
// a pending refinery event for the rig, or live polecats that will submit MRs.
// Open MR beads are deliberately not queried here (too costly per heartbeat
// across ~120 rigs); callers that are about to stop a refinery check the queue
// themselves.
func RefineryHasWork(townRoot, rigName string) bool {
	return HasPendingRefineryEvents(townRoot, rigName) || HasPolecats(townRoot, rigName)
}
