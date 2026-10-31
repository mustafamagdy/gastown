package review

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Gate statuses.
const (
	GateApproved = "approved"
	GatePending  = "pending"
	GateRejected = "rejected"
)

// GateResult is the visual review verdict for a set of beads.
type GateResult struct {
	Status  string
	Message string   // one line a person can act on
	Reviews []string // ids of the reviews that decided it
}

// OK reports whether the gate lets the work through.
func (g GateResult) OK() bool { return g.Status == GateApproved }

// latestApprovals keeps, per bead, the newest "approval" review. A newer
// request replaces an older one, so a rejected review does not block forever
// once the agent fixed the page and asked again.
func latestApprovals(recs []*Record, beadIDs []string) map[string]*Record {
	want := map[string]bool{}
	for _, b := range beadIDs {
		want[b] = true
	}
	latest := map[string]*Record{}
	for _, r := range recs {
		// An ended review (cancelled, or ended without a decision) says
		// nothing either way, so it does not replace an earlier decision.
		if r.Kind != KindApproval || !want[r.Bead] || r.State == StateEnded {
			continue
		}
		if cur := latest[r.Bead]; cur == nil || !r.CreatedAt.Before(cur.CreatedAt) {
			latest[r.Bead] = r
		}
	}
	return latest
}

func blocking(state string) bool {
	return state == StateOpen || state == StateDisconnected || state == StateRejected
}

// FeatureGate decides whether a feature (its epic and task beads) is visually
// approved: at least one approved approval review, and no open or rejected
// one. An approval counts only while the code is the same as when the review
// was requested (see Record.Tips). If the store or a recorded commit cannot be
// read the answer is "pending" (fail closed).
func FeatureGate(townRoot string, beadIDs []string) GateResult {
	recs, errs := List(townRoot)
	if len(errs) > 0 {
		return GateResult{Status: GatePending, Message: fmt.Sprintf("visual review cannot be checked: the review store is unreadable (%v)", errs[0])}
	}
	latest := latestApprovals(recs, beadIDs)
	list := make([]*Record, 0, len(latest))
	for _, r := range latest {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	var ids, rejected, open, approved, stale, unread []string
	for _, r := range list {
		ids = append(ids, r.ID)
		switch {
		case r.State == StateRejected:
			rejected = append(rejected, fmt.Sprintf("%s (%s)", r.ID, r.Bead))
		case blocking(r.State):
			open = append(open, fmt.Sprintf("%s (%s)", r.ID, r.Bead))
		case r.State == StateApproved:
			moved, err := CodeChanged(r)
			switch {
			case err != nil:
				unread = append(unread, fmt.Sprintf("%s (%s): %v", r.ID, r.Bead, err))
			case moved != "":
				stale = append(stale, fmt.Sprintf("%s (%s): %s", r.ID, r.Bead, moved))
			default:
				approved = append(approved, r.ID)
			}
		}
	}
	switch {
	case len(rejected) > 0:
		return GateResult{Status: GateRejected, Reviews: ids,
			Message: "visual review rejected: " + strings.Join(rejected, ", ") + "; fix the page and run 'gt review request' again"}
	case len(open) > 0:
		return GateResult{Status: GatePending, Reviews: ids,
			Message: "visual review pending: " + strings.Join(open, ", ") + " waiting for the reviewer"}
	case len(unread) > 0:
		return GateResult{Status: GatePending, Reviews: ids,
			Message: "visual review cannot be checked: " + strings.Join(unread, "; ")}
	case len(approved) == 0 && len(stale) > 0:
		return GateResult{Status: GatePending, Reviews: ids,
			Message: "visual review out of date: the code changed since approval (" + strings.Join(stale, "; ") + "); run 'gt review request <page.html> --bead <id> --kind approval' for a new review"}
	case len(approved) == 0:
		return GateResult{Status: GatePending,
			Message: "visual review required: no approved review yet; run 'gt review request <page.html> --bead <id> --kind approval'"}
	}
	return GateResult{Status: GateApproved, Reviews: approved, Message: "visual review approved: " + strings.Join(approved, ", ")}
}

// MergeBlock says whether the refinery must leave a merge request queued
// because its source bead has an open or rejected approval review. It applies
// whether or not a project asks for visual review, since such a review exists
// only when someone requested it. An approval whose recorded code has moved
// also blocks. An unreadable store blocks (fail closed);
// a town with no store never blocks.
//
// mrCommit is the commit the merge request submitted. A review of a working
// directory is compared to it, not to the directory, which is usually gone by
// now. A review of a feature branch is left to FeatureGate: other tasks move
// that branch, and the feature lands as a whole.
func MergeBlock(townRoot, sourceBead, mrCommit string) (bool, string) {
	if sourceBead == "" {
		return false, ""
	}
	if _, err := os.Stat(Dir(townRoot)); err != nil {
		if os.IsNotExist(err) {
			return false, ""
		}
		return true, fmt.Sprintf("review store unreadable: %v", err)
	}
	recs, errs := List(townRoot)
	if len(errs) > 0 {
		return true, fmt.Sprintf("review store unreadable: %v", errs[0])
	}
	r := latestApprovals(recs, []string{sourceBead})[sourceBead]
	if r == nil {
		return false, ""
	}
	if blocking(r.State) {
		return true, fmt.Sprintf("approval review %s on %s is %s", r.ID, sourceBead, r.State)
	}
	if r.State == StateApproved {
		for _, t := range r.Tips {
			if t.Branch != "" {
				continue
			}
			if mrCommit == "" {
				return true, fmt.Sprintf("approval review %s on %s cannot be checked: the merge request has no commit", r.ID, sourceBead)
			}
			if t.Commit != mrCommit {
				return true, fmt.Sprintf("the code changed since approval review %s on %s (%s -> %s); request a new review", r.ID, sourceBead, short(t.Commit), short(mrCommit))
			}
		}
	}
	return false, ""
}
