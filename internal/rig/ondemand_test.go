package rig

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubAlive makes PolecatSessionAlive report the given polecats as running.
func stubAlive(t *testing.T, alive ...string) {
	t.Helper()
	orig := PolecatSessionAlive
	t.Cleanup(func() { PolecatSessionAlive = orig })
	PolecatSessionAlive = func(_, polecat string) bool {
		for _, a := range alive {
			if a == polecat {
				return true
			}
		}
		return false
	}
}

func TestHasLivePolecats(t *testing.T) {
	stubAlive(t, "toast")
	town := t.TempDir()
	if HasLivePolecats(town, "r") || PolecatNames(town, "r") != nil {
		t.Fatal("no polecats dir: want none")
	}
	mkdir(t, town, "r", "polecats", ".hidden")
	if err := os.WriteFile(filepath.Join(town, "r", "polecats", "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mkdir(t, town, "r", "polecats", "furiosa") // finished, session gone
	if names := PolecatNames(town, "r"); len(names) != 1 || names[0] != "furiosa" {
		t.Fatalf("PolecatNames = %v", names)
	}
	if HasLivePolecats(town, "r") || WitnessHasWork(town, "r") {
		t.Fatal("polecat dir without a session: want false")
	}
	mkdir(t, town, "r", "polecats", "toast")
	if !HasLivePolecats(town, "r") || !WitnessHasWork(town, "r") {
		t.Fatal("running polecat: want true")
	}
}

func TestRefineryHasWork(t *testing.T) {
	stubAlive(t, "toast")
	town := t.TempDir()
	if RefineryHasWork(town, "r") {
		t.Fatal("idle rig: want false")
	}
	dir := mkdir(t, town, "events", "refinery")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1.event", `{"payload":{"rig":"other"}}`)
	write("2.event", `{"payload":{}}`)
	write("3.txt", `{"payload":{"rig":"r"}}`)
	if HasPendingRefineryEvents(town, "r") || RefineryHasWork(town, "r") {
		t.Fatal("events for other/no rig must not count")
	}
	write("4.event", `{"payload":{"rig":"r"}}`)
	if !HasPendingRefineryEvents(town, "r") || !RefineryHasWork(town, "r") {
		t.Fatal("pending event for rig: want true")
	}
	town2 := t.TempDir()
	mkdir(t, town2, "r", "polecats", "toast")
	if !RefineryHasWork(town2, "r") {
		t.Fatal("running polecat: refinery has work")
	}
}
