//go:build !windows

package herdrmux

import (
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// The local table must track internal/constants for the roles the web
// dashboard has no icon for.
func TestRoleEmojiMatchesConstants(t *testing.T) {
	for role, want := range map[string]string{
		"deacon":   constants.EmojiDeacon,
		"witness":  constants.EmojiWitness,
		"refinery": constants.EmojiRefinery,
		"boot":     constants.EmojiBoot,
	} {
		if roleEmoji[role] != want {
			t.Errorf("roleEmoji[%q] = %q, constants has %q", role, roleEmoji[role], want)
		}
	}
}

func TestDisplayLabel(t *testing.T) {
	cases := []struct{ role, label, want string }{
		{"mayor", "mayor", "🎩 mayor"},
		{"dog", "dog-alpha", "🐕 dog-alpha"},
		{"witness", "witness", "🦉 witness"},
		{"crew", "crew-max", "👨‍💼 crew-max"},
		{"polecat", "rust", "🦨 rust"},
		{"", "weird", "weird"},
		{"overseer", "x", "x"},
	}
	for _, c := range cases {
		if got := DisplayLabel(c.role, c.label); got != c.want {
			t.Errorf("DisplayLabel(%q,%q) = %q, want %q", c.role, c.label, got, c.want)
		}
	}
	if GroupLabel("hq") != "hq" || GroupLabel("tododemo") != "🏗️ tododemo" {
		t.Errorf("GroupLabel: %q %q", GroupLabel("hq"), GroupLabel("tododemo"))
	}
	t.Setenv("GT_HERDR_EMOJI", "0")
	if DisplayLabel("mayor", "mayor") != "mayor" || GroupLabel("demo") != "demo" {
		t.Error("GT_HERDR_EMOJI=0 must disable decoration")
	}
}

func TestEmojiDisabledPlacement(t *testing.T) {
	t.Setenv("GT_HERDR_EMOJI", "0")
	f, run := newFake(t, "gt-e0")
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Errorf("tabs = %v", got)
	}
}

// A group created by an older build has an undecorated workspace label; it
// is still found by tracked id, reused, and renamed to the decorated label.
func TestLegacyGroupLabelReusedAndRenamed(t *testing.T) {
	town := mkTown(t)
	f, run := newFake(t, "gt-e1")
	rigEnv := []string{"-e", "GT_RIG=demo", "-e", "GT_TOWN_ROOT=" + town}
	t.Setenv("GT_HERDR_EMOJI", "0")
	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-witness", "-e", "GT_ROLE=demo/witness"}, rigEnv...)...)
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"demo"}) {
		t.Fatalf("legacy workspaces = %v", got)
	}
	t.Setenv("GT_HERDR_EMOJI", "1")
	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-refinery", "-e", "GT_ROLE=demo/refinery"}, rigEnv...)...)
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"🏗️ demo"}) {
		t.Fatalf("workspaces after reuse = %v (want one, renamed)", got)
	}
	if got := f.tabLabels("🏗️ demo"); !reflect.DeepEqual(got, []string{"witness", "🏭 refinery"}) {
		t.Errorf("tabs = %v", got)
	}
}

func TestRenameKeepsEmojiAndWindowNamePlain(t *testing.T) {
	f, run := newFake(t, "gt-e2")
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	mustRun(t, run, "rename-session", "-t", "hq-mayor", "hq-boss")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"🎩 boss"}) {
		t.Errorf("tab after rename = %v", got)
	}
	if got := mustRun(t, run, "display-message", "-p", "-t", "hq-boss", "#{window_name}"); got != "boss\n" {
		t.Errorf("window_name = %q, want plain", got)
	}
	// Session identity is state-based, not label-based.
	if _, _, code := run("has-session", "-t", "=hq-boss"); code != 0 {
		t.Error("renamed session must still resolve")
	}
}
