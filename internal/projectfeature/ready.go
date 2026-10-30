package projectfeature

// StatusMerging is the status callers report for a task whose bead is closed
// while an MR for it is still open.
const StatusMerging = "merging"

// Task run states derived from bead status.
const (
	TaskDone    = "done"    // bead closed and nothing left to merge
	TaskMerging = "merging" // bead closed, its MR not merged yet
	TaskRunning = "running" // hooked, pinned or in progress
	TaskReady   = "ready"   // open, every 'after' task done
	TaskBlocked = "blocked" // open, some 'after' task not done
	TaskUnknown = "unknown" // status unreadable or not a dispatchable status
)

// TaskView is a task with its live state.
type TaskView struct {
	Task
	Status    string   `json:"status"`               // bead status, "" if unreadable
	State     string   `json:"state"`                // Task* constants
	BlockedBy []string `json:"blocked_by,omitempty"` // 'after' tasks not yet done
}

// Classify computes the state of every task from the bead statuses (task id
// to status). The cached record never holds a status: only the beads decide.
//
// gt done closes a task's bead as soon as the polecat submits, before the
// refinery merges, so a closed bead alone does not mean the work is on the
// feature branch. Callers report StatusMerging while an MR is open, and only
// a done task releases the tasks waiting for it.
func Classify(rec *Record, status map[string]string) []TaskView {
	done := func(id string) bool { return status[id] == "closed" }
	out := make([]TaskView, 0, len(rec.Tasks))
	for _, t := range rec.Tasks {
		v := TaskView{Task: t, Status: status[t.ID]}
		switch v.Status {
		case "closed":
			v.State = TaskDone
		case StatusMerging:
			v.State = TaskMerging
		case "hooked", "pinned", "in_progress":
			v.State = TaskRunning
		case "open":
			for _, a := range t.After {
				if !done(a) {
					v.BlockedBy = append(v.BlockedBy, a)
				}
			}
			if len(v.BlockedBy) == 0 {
				v.State = TaskReady
			} else {
				v.State = TaskBlocked
			}
		default:
			v.State = TaskUnknown
		}
		out = append(out, v)
	}
	return out
}

// Started reports whether any task has left the open state (running or done).
func Started(views []TaskView) bool {
	for _, v := range views {
		if v.State == TaskRunning || v.State == TaskMerging || v.State == TaskDone {
			return true
		}
	}
	return false
}

// ReadyTasks returns the tasks that can be dispatched now.
func ReadyTasks(views []TaskView) []TaskView {
	var out []TaskView
	for _, v := range views {
		if v.State == TaskReady {
			out = append(out, v)
		}
	}
	return out
}
