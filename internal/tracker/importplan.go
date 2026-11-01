package tracker

import (
	"strings"

	"github.com/steveyegge/gastown/internal/projectfeature"
)

// RigTagPrefix is the tag (Azure) or label (GitHub) prefix that assigns a child
// to a rig: "rig:orders_svc".
const RigTagPrefix = "rig:"

// ChildPlan is a child that becomes a task.
type ChildPlan struct {
	Child Child
	Rig   string
}

// ImportPlan says what importing the item's children does.
type ImportPlan struct {
	Create     []ChildPlan // new tasks
	Existing   []Child     // already tasks of the feature, never touched
	Unassigned []Child     // several rigs and no usable rig tag: not created
	Done       []Child     // already in the done state in the tracker: not created
}

// PlanChildren maps the children of an item to rigs. rec is the existing
// feature (nil for a first import); a child already recorded as a task is
// Existing. With one rig every child goes there; with several, a child needs a
// "rig:<name>" tag naming one of them.
func PlanChildren(rec *projectfeature.Record, children []Child, rigs []string, doneState string) ImportPlan {
	var plan ImportPlan
	known := map[string]bool{}
	if rec != nil {
		for _, t := range rec.Tasks {
			if t.Tracker != nil {
				known[t.Tracker.ID] = true
			}
		}
	}
	for _, c := range children {
		switch {
		case known[c.ID]:
			plan.Existing = append(plan.Existing, c)
			continue
		case c.State == doneState:
			plan.Done = append(plan.Done, c)
			continue
		}
		rig := ""
		if len(rigs) == 1 {
			rig = rigs[0]
		} else {
			rig = rigFromTags(c.Tags, rigs)
		}
		if rig == "" {
			plan.Unassigned = append(plan.Unassigned, c)
			continue
		}
		plan.Create = append(plan.Create, ChildPlan{Child: c, Rig: rig})
	}
	return plan
}

func rigFromTags(tags, rigs []string) string {
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if len(t) <= len(RigTagPrefix) || !strings.EqualFold(t[:len(RigTagPrefix)], RigTagPrefix) {
			continue
		}
		name := strings.TrimSpace(t[len(RigTagPrefix):])
		for _, r := range rigs {
			if r == name {
				return r
			}
		}
	}
	return ""
}
