package daemon

import (
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectfeature"
)

func featureTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh"}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, state string, tasks int) {
		rec := &projectfeature.Record{
			ID: id, Project: "shop", Branch: "feat/" + id, State: state,
			Rigs: map[string]projectfeature.RigState{"api": {State: projectfeature.RigBranch}},
		}
		for i := 0; i < tasks; i++ {
			rec.Tasks = append(rec.Tasks, projectfeature.Task{ID: id + "-t" + string(rune('a'+i)), Rig: "api"})
		}
		if err := projectfeature.Create(town, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("sh-1", projectfeature.StateOpen, 2)
	mk("sh-2", projectfeature.StateLanded, 1)
	mk("sh-3", projectfeature.StateOpen, 0)
	return town
}

func TestDueFeaturesThrottlesAndSkips(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: featureTown(t)}, logger: log.New(io.Discard, "", 0)}
	now := time.Now()
	if due := d.dueFeatures(now); len(due) != 1 || due[0] != "sh-1" {
		t.Fatalf("due = %v, want only the open feature with tasks", due)
	}
	if due := d.dueFeatures(now.Add(30 * time.Second)); len(due) != 0 {
		t.Fatalf("throttled call returned %v", due)
	}
	if due := d.dueFeatures(now.Add(featureRunInterval + time.Second)); len(due) != 1 {
		t.Fatalf("after interval due = %v", due)
	}
}

func TestRunFeatureLogsErrorsWithoutPanic(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(io.Discard, "", 0)}
	called := ""
	d.featureRunFn = func(id string) ([]byte, error) { called = id; return []byte("boom"), errors.New("fail") }
	d.runFeature("sh-1")
	if called != "sh-1" {
		t.Fatal("run function not called")
	}
}

func TestDueLandingsOnlyAdvancesLandingFeatures(t *testing.T) {
	town := featureTown(t) // sh-1 open, sh-2 landed, sh-3 open
	mk := func(id, state string) {
		rec := &projectfeature.Record{
			ID: id, Project: "shop", Branch: "feat/" + id, State: state,
			Rigs: map[string]projectfeature.RigState{"api": {State: projectfeature.RigBranch}},
		}
		if err := projectfeature.Create(town, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("sh-4", projectfeature.StateLanding)
	mk("sh-5", projectfeature.StateHeld)
	d := &Daemon{config: &Config{TownRoot: town}, logger: log.New(io.Discard, "", 0)}
	now := time.Now()
	if due := d.dueLandings(now); len(due) != 1 || due[0] != "sh-4" {
		t.Fatalf("due = %v, want only the landing feature (never open or held)", due)
	}
	if due := d.dueLandings(now.Add(4 * time.Minute)); len(due) != 0 {
		t.Fatalf("throttled call returned %v", due)
	}
	if due := d.dueLandings(now.Add(featureLandInterval + time.Second)); len(due) != 1 {
		t.Fatalf("after interval due = %v", due)
	}
}

func TestAdvanceLandingsNeverStartsAnOpenFeature(t *testing.T) {
	d := &Daemon{config: &Config{TownRoot: featureTown(t)}, logger: log.New(io.Discard, "", 0)}
	called := make(chan string, 4)
	d.featureLandFn = func(id string) ([]byte, error) { called <- id; return nil, nil }
	d.advanceLandings()
	time.Sleep(50 * time.Millisecond)
	select {
	case id := <-called:
		t.Fatalf("land started for %s; the daemon must only advance landing features", id)
	default:
	}
}

func TestDueTrackerSyncsOnlyImportedAndUnfinished(t *testing.T) {
	town := featureTown(t) // plain features without a tracker ref
	d := &Daemon{config: &Config{TownRoot: town}, logger: log.New(io.Discard, "", 0)}
	if due := d.dueTrackerSyncs(time.Now()); len(due) != 0 {
		t.Fatalf("features without tracker_ref are due: %v", due)
	}
	mk := func(id, synced string) {
		rec := &projectfeature.Record{
			ID: id, Project: "shop", Branch: "feat/" + id,
			Rigs:    map[string]projectfeature.RigState{"api": {State: projectfeature.RigBranch}},
			Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: "12", Synced: synced},
		}
		if err := projectfeature.Create(town, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("sh-8", "done") // everything written: not due
	now := time.Now()
	if due := d.dueTrackerSyncs(now); len(due) != 0 {
		t.Fatalf("finished feature is due: %v", due)
	}
	mk("sh-9", "")
	mk("sh-10", "active")
	if due := d.dueTrackerSyncs(now); len(due) != 1 || due[0] != "shop" {
		t.Fatalf("due = %v, want the project once", due)
	}
	if due := d.dueTrackerSyncs(now.Add(time.Minute)); len(due) != 0 {
		t.Fatalf("throttled call returned %v", due)
	}
	if due := d.dueTrackerSyncs(now.Add(trackerSyncInterval + time.Second)); len(due) != 1 {
		t.Fatalf("after interval due = %v", due)
	}
}

func TestTrackerSyncFailureOnlyLogsAndDoesNotBlockLanding(t *testing.T) {
	var logs strings.Builder
	d := &Daemon{config: &Config{TownRoot: featureTown(t)}, logger: log.New(&logs, "", 0)}
	d.trackerSyncFn = func(project string) ([]byte, error) { return []byte("tracker sync: x\nsecond"), errors.New("exit 1") }
	d.syncTracker("shop")
	if n := strings.Count(strings.TrimSpace(logs.String()), "\n"); n != 0 || !strings.Contains(logs.String(), "tracker sync shop failed") {
		t.Errorf("want one log line, got %q", logs.String())
	}
	// A running sync does not keep the landing tick from starting.
	d.trackerSyncing.Store(true)
	d.featureLandFn = func(id string) ([]byte, error) { return nil, nil }
	d.advanceLandings() // must return immediately
}
