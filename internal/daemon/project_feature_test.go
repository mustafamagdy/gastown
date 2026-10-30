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
