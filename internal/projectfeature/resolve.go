package projectfeature

import (
	"errors"
	"fmt"
	"sort"
)

// ResolveTarget returns the branch a bead's work must be based on and merged
// into in the given rig.
//
// marked is false when the bead description has no project_feature marker; the
// caller then behaves as before. When it is true the answer is final: any
// failure to resolve (no record, rig not in the feature, task registered for
// another rig, feature landed) is an error and the caller must stop, not fall
// back to the default branch.
func ResolveTarget(townRoot, rig, beadID, description string) (branch string, marked bool, err error) {
	if !HasMarker(description) {
		return "", false, nil
	}
	epicID := Marker(description)
	if epicID == "" {
		return "", true, fmt.Errorf("bead %s has an empty %s marker", beadID, MarkerKey)
	}
	rec, err := Find(townRoot, epicID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", true, fmt.Errorf("bead %s belongs to feature %s but its feature record is missing; refusing to fall back to the default branch: %w", beadID, epicID, err)
		}
		return "", true, fmt.Errorf("bead %s belongs to feature %s but its record cannot be read: %w", beadID, epicID, err)
	}
	if rec.State == StateLanded {
		return "", true, fmt.Errorf("feature %s is already landed; bead %s cannot start new work on it", epicID, beadID)
	}
	rs, ok := rec.Rigs[rig]
	if !ok {
		return "", true, fmt.Errorf("bead %s belongs to feature %s, which has no branch in rig %q (rigs: %v); add it with 'gt project feature add-rig %s %s'", beadID, epicID, rig, rigNames(rec), epicID, rig)
	}
	if rs.State == RigHeld || rs.State == RigLanded {
		return "", true, fmt.Errorf("feature %s is %s in rig %q; bead %s cannot start new work there", epicID, rs.State, rig, beadID)
	}
	if rec.Branch == "" {
		return "", true, fmt.Errorf("feature %s has no branch", epicID)
	}
	if t := rec.Task(beadID); t != nil && t.Rig != rig {
		return "", true, fmt.Errorf("bead %s is registered for rig %q in feature %s, not %q", beadID, t.Rig, epicID, rig)
	}
	return rec.Branch, true, nil
}

func rigNames(rec *Record) []string {
	var out []string
	for r := range rec.Rigs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
