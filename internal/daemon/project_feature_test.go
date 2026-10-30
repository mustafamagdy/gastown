package daemon

import (
	"errors"
	"io"
	"log"
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
