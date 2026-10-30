package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
)

func setupOnDemandTown(t *testing.T, onDemand bool) string {
	t.Helper()
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	od := onDemand
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "xelta", Prefix: "xe", OnDemand: &od}); err != nil {
		t.Fatal(err)
	}
	rc := &config.RigsConfig{Version: config.CurrentRigsVersion, Rigs: map[string]config.RigEntry{
		"in":  {GitURL: "https://example.com/in.git", Project: "xelta"},
		"out": {GitURL: "https://example.com/out.git"},
	}}
	if err := config.SaveRigsConfig(filepath.Join(town, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
	return town
}

func TestUpOnDemandIdle(t *testing.T) {
	orig := rig.PolecatSessionAlive
	t.Cleanup(func() { rig.PolecatSessionAlive = orig })
	rig.PolecatSessionAlive = func(_, polecat string) bool { return polecat == "toast" }
	town := setupOnDemandTown(t, true)
	in := &rig.Rig{Name: "in", Path: filepath.Join(town, "in")}
	out := &rig.Rig{Name: "out", Path: filepath.Join(town, "out")}

	if !upOnDemandIdle("in", in, rig.WitnessHasWork) || !upOnDemandIdle("in", in, rig.RefineryHasWork) {
		t.Fatal("idle project rig should be skipped")
	}
	if upOnDemandIdle("out", out, rig.WitnessHasWork) {
		t.Fatal("rig outside a project is never skipped")
	}
	if err := os.MkdirAll(filepath.Join(town, "in", "polecats", "toast"), 0o755); err != nil {
		t.Fatal(err)
	}
	if upOnDemandIdle("in", in, rig.WitnessHasWork) || upOnDemandIdle("in", in, rig.RefineryHasWork) {
		t.Fatal("project rig with a running polecat must start agents")
	}
}

func TestEnsureOnDemandAgentFastStart(t *testing.T) {
	town := setupOnDemandTown(t, true)
	var started []string
	fn := func(r *rig.Rig) error { started = append(started, r.Name); return nil }

	ensureOnDemandAgent(town, "in", "witness", fn)
	ensureOnDemandAgent(town, "out", "witness", fn)
	if len(started) != 1 || started[0] != "in" {
		t.Fatalf("started = %v, want only [in]", started)
	}

	town2 := setupOnDemandTown(t, false)
	started = nil
	ensureOnDemandAgent(town2, "in", "refinery", fn)
	if len(started) != 0 {
		t.Fatal("on_demand=false project must not fast start")
	}
}
