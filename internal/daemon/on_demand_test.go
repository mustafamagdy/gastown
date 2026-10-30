package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

type onDemandEnv struct {
	*ghostTestEnv
	now time.Time
	mrs bool
}

func setupOnDemand(t *testing.T) *onDemandEnv {
	t.Helper()
	e := &onDemandEnv{ghostTestEnv: setupGhostTest(t), now: time.Unix(1_000_000, 0)}
	e.daemon.onDemandNow = func() time.Time { return e.now }
	e.daemon.onDemandPendingMRs = func(string) (bool, error) { return e.mrs, nil }
	return e
}

func (e *onDemandEnv) polecat(t *testing.T, rig string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(e.daemon.config.TownRoot, rig, "polecats", "toast"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestOnDemand_StartsOnlyWithWork(t *testing.T) {
	e := setupOnDemand(t)
	if e.daemon.onDemandShouldRun("shop", "witness") {
		t.Fatal("witness must not start without polecats")
	}
	if e.daemon.onDemandShouldRun("shop", "refinery") {
		t.Fatal("refinery must not start without work")
	}
	e.polecat(t, "shop")
	if !e.daemon.onDemandShouldRun("shop", "witness") || !e.daemon.onDemandShouldRun("shop", "refinery") {
		t.Fatal("polecat present: both should run")
	}
}

func TestOnDemand_StopsAfterGraceNeverWithWork(t *testing.T) {
	e := setupOnDemand(t)
	ws := session.WitnessSessionName(session.PrefixFor("shop"))
	e.addSessions(t, ws)

	// First observation starts the clock; no kill (daemon restart case).
	if !e.daemon.onDemandShouldRun("shop", "witness") {
		t.Fatal("first idle observation must keep the session")
	}
	e.now = e.now.Add(9 * time.Minute)
	if !e.daemon.onDemandShouldRun("shop", "witness") {
		t.Fatal("within grace must keep the session")
	}
	if len(readKills(t, e.tmuxLog)) != 0 {
		t.Fatal("unexpected kill within grace")
	}

	// Work resets the clock; never stopped while polecats exist.
	e.polecat(t, "shop")
	e.now = e.now.Add(time.Hour)
	if !e.daemon.onDemandShouldRun("shop", "witness") || len(readKills(t, e.tmuxLog)) != 0 {
		t.Fatal("must not stop with polecats")
	}
	if err := os.RemoveAll(filepath.Join(e.daemon.config.TownRoot, "shop", "polecats")); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(9 * time.Minute)
	if !e.daemon.onDemandShouldRun("shop", "witness") {
		t.Fatal("grace restarts when work ends")
	}
	e.now = e.now.Add(2 * time.Minute)
	if e.daemon.onDemandShouldRun("shop", "witness") {
		t.Fatal("past grace: should stop")
	}
	kills := readKills(t, e.tmuxLog)
	if len(kills) != 1 || kills[0] != ws {
		t.Fatalf("kills = %v, want [%s]", kills, ws)
	}
}

func TestOnDemand_RefineryKeptWhileMRsPending(t *testing.T) {
	e := setupOnDemand(t)
	rs := session.RefinerySessionName(session.PrefixFor("shop"))
	e.addSessions(t, rs)
	e.daemon.onDemandShouldRun("shop", "refinery")
	e.now = e.now.Add(time.Hour)
	e.mrs = true
	if !e.daemon.onDemandShouldRun("shop", "refinery") || len(readKills(t, e.tmuxLog)) != 0 {
		t.Fatal("must not stop refinery with pending MRs")
	}
	e.mrs = false
	e.now = e.now.Add(time.Hour)
	if e.daemon.onDemandShouldRun("shop", "refinery") {
		t.Fatal("no MRs, past grace: should stop")
	}
	if kills := readKills(t, e.tmuxLog); len(kills) != 1 || kills[0] != rs {
		t.Fatalf("kills = %v", kills)
	}
}

func TestOnDemandGraceConfig(t *testing.T) {
	d := &Daemon{config: &Config{}}
	if d.onDemandGrace() != DefaultOnDemandIdleGrace {
		t.Fatal("default grace")
	}
	d.config.OnDemandIdleGrace = time.Minute
	if d.onDemandGrace() != time.Minute {
		t.Fatal("configured grace")
	}
}
