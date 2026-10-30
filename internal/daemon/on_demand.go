package daemon

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
)

func (d *Daemon) onDemandGrace() time.Duration {
	if d.config.OnDemandIdleGrace > 0 {
		return d.config.OnDemandIdleGrace
	}
	return DefaultOnDemandIdleGrace
}

func (d *Daemon) onDemandClock() time.Time {
	if d.onDemandNow != nil {
		return d.onDemandNow()
	}
	return time.Now()
}

// onDemandShouldRun decides whether an on-demand rig's agent (role is
// "witness" or "refinery") should be running now. It returns true when the
// caller should go on to start (or keep) the agent, false when it should not.
//
//   - Work exists: refresh the last-work time, run.
//   - No work, no session: do not start.
//   - No work, session running: keep it until the idle grace has elapsed, then
//     stop it (same kill path used for parked/docked rigs). The rig is not
//     parked or docked; the next piece of work restarts the agent.
//
// A refinery is never stopped while open or in-progress MRs exist, and a
// witness never while a polecat still has work assigned (working, stalled,
// stuck or review-needed), even if its session died. Both are checked only at
// stop time, since querying beads for every rig every heartbeat is costly.
// Running polecat sessions count as work on every heartbeat.
func (d *Daemon) onDemandShouldRun(rigName, role string) bool {
	townRoot := d.config.TownRoot
	key := role + ":" + rigName

	var hasWork bool
	var sessionName string
	if role == "witness" {
		hasWork = rig.WitnessHasWork(townRoot, rigName)
		sessionName = session.WitnessSessionName(session.PrefixFor(rigName))
	} else {
		hasWork = rig.RefineryHasWork(townRoot, rigName)
		sessionName = session.RefinerySessionName(session.PrefixFor(rigName))
	}
	now := d.onDemandClock()

	d.onDemandMu.Lock()
	defer d.onDemandMu.Unlock()
	if d.onDemandLastWork == nil {
		d.onDemandLastWork = make(map[string]time.Time)
	}

	if hasWork {
		d.onDemandLastWork[key] = now
		return true
	}

	running, _ := d.tmux.HasSession(sessionName)
	if !running {
		delete(d.onDemandLastWork, key)
		d.logger.Printf("On-demand rig %s has no work, not starting %s", rigName, role)
		return false
	}

	last, seen := d.onDemandLastWork[key]
	if !seen {
		// First observation (e.g. daemon restart or manual start): start the clock.
		d.onDemandLastWork[key] = now
		return true
	}
	if now.Sub(last) < d.onDemandGrace() {
		return true
	}

	if role == "witness" {
		active, err := d.onDemandActivePolecatCheck(rigName)
		if err != nil {
			d.logger.Printf("On-demand rig %s: cannot verify polecat state, keeping witness: %v", rigName, err)
			return true
		}
		if active {
			d.onDemandLastWork[key] = now
			return true
		}
	}

	if role == "refinery" {
		pending, err := d.onDemandPendingMRCheck(rigName)
		if err != nil {
			d.logger.Printf("On-demand rig %s: cannot verify merge queue, keeping refinery: %v", rigName, err)
			return true
		}
		if pending {
			d.onDemandLastWork[key] = now
			return true
		}
	}

	d.logger.Printf("On-demand rig %s idle for %s, stopping %s (%s)", rigName, now.Sub(last).Round(time.Second), role, sessionName)
	if err := d.tmux.KillSessionWithProcesses(sessionName); err != nil {
		d.logger.Printf("Error stopping idle %s %s: %v", role, sessionName, err)
		return true
	}
	delete(d.onDemandLastWork, key)
	return false
}

// onDemandPendingMRCheck reports whether the rig has open or in-progress
// merge-request beads.
func (d *Daemon) onDemandPendingMRCheck(rigName string) (bool, error) {
	if d.onDemandPendingMRs != nil {
		return d.onDemandPendingMRs(rigName)
	}
	b := beads.New(filepath.Join(d.config.TownRoot, rigName))
	for _, status := range []string{"open", "in_progress"} {
		issues, err := b.ListMergeRequests(beads.ListOptions{
			Label:    "gt:merge-request",
			Status:   status,
			Priority: -1,
			Rig:      rigName,
		})
		if err != nil {
			return false, fmt.Errorf("listing %s merge requests: %w", status, err)
		}
		if len(issues) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// onDemandActivePolecatCheck reports whether any of the rig's polecats still
// needs its witness: assigned work in progress, a dead session with work
// (stalled), or a request for help. Finished and idle polecats do not.
func (d *Daemon) onDemandActivePolecatCheck(rigName string) (bool, error) {
	if d.onDemandActivePolecats != nil {
		return d.onDemandActivePolecats(rigName)
	}
	if len(rig.PolecatNames(d.config.TownRoot, rigName)) == 0 {
		return false, nil
	}
	rigPath := filepath.Join(d.config.TownRoot, rigName)
	mgr := polecat.NewManager(&rig.Rig{Name: rigName, Path: rigPath}, git.NewGit(rigPath), d.tmux)
	polecats, err := mgr.List()
	if err != nil {
		return false, err
	}
	for _, p := range polecats {
		switch p.State {
		case polecat.StateWorking, polecat.StateStalled, polecat.StateStuck, polecat.StateReviewNeeded:
			return true, nil
		}
	}
	return false, nil
}
