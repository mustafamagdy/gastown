package tracker

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/projectfeature"
)

// Desired states of the reconciler.
const (
	DesiredNone   = ""
	DesiredActive = "active"
	DesiredDone   = "done"
)

func rank(desired string) int {
	switch desired {
	case DesiredActive:
		return 1
	case DesiredDone:
		return 2
	}
	return 0
}

// Desired is the state the feature's tracker item should have: done when the
// feature has landed, active when work started (a task is running, merging or
// done, or a landing began), otherwise none.
func Desired(rec *projectfeature.Record, views []projectfeature.TaskView) string {
	if rec.State == projectfeature.StateLanded {
		return DesiredDone
	}
	if rec.State == projectfeature.StateLanding || rec.State == projectfeature.StateHeld {
		return DesiredActive
	}
	for _, v := range views {
		switch v.State {
		case projectfeature.TaskRunning, projectfeature.TaskMerging, projectfeature.TaskDone:
			return DesiredActive
		}
	}
	return DesiredNone
}

// NeedsSync is a cheap check on the record alone: false when everything the
// reconciler could ever write has been written.
func NeedsSync(rec *projectfeature.Record) bool {
	if rec.Tracker != nil && rec.Tracker.Synced != DesiredDone {
		return true
	}
	for _, t := range rec.Tasks {
		if t.Tracker != nil && t.Tracker.Synced != DesiredDone {
			return true
		}
	}
	return false
}

// Change is one write the reconciler made (or would make).
type Change struct {
	Task string // task bead id, empty for the feature itself
	Item string // tracker item id
	From string
	To   string // tracker state
}

// Report is the outcome of one reconcile pass.
type Report struct {
	Changes []Change
	Left    []string // one line per item left alone on purpose
}

// Reconcile brings the tracker items of one feature up to date. taskStatus is
// the live bead status per task id (as featureTaskStatuses reads it).
//
// Rules: forward only, only the imported item (and tracked child tasks whose
// own bead is closed), never over a state a human chose, one read at most per
// item and none when the stored Synced state already covers the desired state.
// A failed call leaves Synced unchanged so the next pass retries it.
func Reconcile(ctx context.Context, townRoot string, rec *projectfeature.Record, taskStatus map[string]string, tr Tracker, dryRun bool) (Report, error) {
	var rep Report
	var errs []error
	views := projectfeature.Classify(rec, taskStatus)

	step := func(taskID string, ref *projectfeature.TrackerRef, desired string) {
		if ref == nil || rank(desired) <= rank(ref.Synced) {
			return
		}
		label := rec.ID
		if taskID != "" {
			label = taskID
		}
		cur, err := tr.State(ctx, ref.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: reading %s: %w", label, ref.ID, err))
			return
		}
		target := tr.ActiveState()
		if desired == DesiredDone {
			target = tr.DoneState()
		}
		switch {
		case cur == target || cur == tr.DoneState():
			// Already there, or finished: nothing to write.
		case cur == ref.ImportState || cur == tr.ActiveState():
			ch := Change{Task: taskID, Item: ref.ID, From: cur, To: target}
			if dryRun {
				rep.Changes = append(rep.Changes, ch)
				return
			}
			if err := tr.SetState(ctx, ref.ID, target); err != nil {
				errs = append(errs, fmt.Errorf("%s: setting %s to %s: %w", label, ref.ID, target, err))
				return
			}
			rep.Changes = append(rep.Changes, ch)
		default:
			rep.Left = append(rep.Left, fmt.Sprintf("%s: item %s is %q (not %q or %q), left alone", label, ref.ID, cur, ref.ImportState, tr.ActiveState()))
		}
		if dryRun {
			return
		}
		// Remember what was handled, including "left alone", so the next tick
		// does not read the tracker again.
		if _, err := projectfeature.Update(townRoot, rec.Project, rec.ID, func(r *projectfeature.Record) error {
			target := r.Tracker
			if taskID != "" {
				t := r.Task(taskID)
				if t == nil {
					return nil
				}
				target = t.Tracker
			}
			if target != nil && rank(desired) > rank(target.Synced) {
				target.Synced = desired
			}
			return nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("%s: saving synced state: %w", label, err))
		}
	}

	step("", rec.Tracker, Desired(rec, views))
	for _, v := range views {
		if v.State == projectfeature.TaskDone {
			step(v.ID, v.Tracker, DesiredDone)
		}
	}
	return rep, errors.Join(errs...)
}
