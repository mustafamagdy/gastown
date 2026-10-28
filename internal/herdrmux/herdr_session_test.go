//go:build !windows

package herdrmux

import "testing"

func TestHerdrSessionMapping(t *testing.T) {
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	if got := HerdrSessionFor("gt-ab12"); got != "gt-ab12" {
		t.Fatalf("unmapped = %q, want own name", got)
	}
	if err := SetHerdrSession("gt-ab12", "default"); err != nil {
		t.Fatal(err)
	}
	if got := HerdrSessionFor("gt-ab12"); got != "default" {
		t.Errorf("mapped = %q, want default", got)
	}
	c := newClientFunc("gt-ab12")
	if c.Sock != "default" || !c.Shared {
		t.Errorf("client = {Sock:%q Shared:%v}, want shared default", c.Sock, c.Shared)
	}
	if c.store.Dir != NewStore("gt-ab12").Dir {
		t.Errorf("state must stay keyed by the tmux socket, got %s", c.store.Dir)
	}
	if err := SetHerdrSession("gt-ab12", ""); err != nil {
		t.Fatal(err)
	}
	if got := HerdrSessionFor("gt-ab12"); got != "gt-ab12" {
		t.Errorf("after clear = %q", got)
	}
	if c := newClientFunc("gt-ab12"); c.Shared {
		t.Error("cleared mapping should give a dedicated client")
	}
}

func TestKillServerSharedSessionKeepsServer(t *testing.T) {
	f, run := newFake(t, "gt-t9")
	f.shared = true
	run("new-session", "-d", "-s", "s")
	if _, e, code := run("kill-server"); code != 0 {
		t.Fatalf("kill-server: %s", e)
	}
	if !f.running {
		t.Error("shared herdr server must not be stopped")
	}
	for _, c := range f.calls {
		if len(c) >= 2 && c[0] == "session" && (c[1] == "stop" || c[1] == "delete") {
			t.Errorf("unexpected %v on a shared session", c)
		}
	}
	var labels []string
	for _, w := range f.workspace {
		labels = append(labels, w.Label)
	}
	if len(labels) != 1 || labels[0] != "~" {
		t.Errorf("workspaces after kill-server = %v, want only the user's", labels)
	}
}
