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

func TestHasPolecats(t *testing.T) {
	town := t.TempDir()
	if HasPolecats(town, "r") {
		t.Fatal("no polecats dir: want false")
	}
	mkdir(t, town, "r", "polecats", ".hidden")
	if err := os.WriteFile(filepath.Join(town, "r", "polecats", "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasPolecats(town, "r") {
		t.Fatal("only hidden dirs and files: want false")
	}
	mkdir(t, town, "r", "polecats", "toast")
	if !HasPolecats(town, "r") || !WitnessHasWork(town, "r") {
		t.Fatal("polecat dir present: want true")
	}
}

func TestRefineryHasWork(t *testing.T) {
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
		t.Fatal("polecat present: refinery has work")
	}
}
