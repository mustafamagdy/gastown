package tmux

import (
	"context"
	"reflect"
	"testing"
)

func setBackendForTest(t *testing.T, b Backend) {
	t.Helper()
	prev := GetBackend()
	SetBackend(b)
	t.Cleanup(func() { SetBackend(prev) })
}

func TestParseBackend(t *testing.T) {
	cases := map[string]Backend{"": BackendTmux, "tmux": BackendTmux, " Herdr ": BackendHerdr, "herdr": BackendHerdr}
	for in, want := range cases {
		got, err := ParseBackend(in)
		if err != nil || got != want {
			t.Errorf("ParseBackend(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseBackend("screen"); err == nil {
		t.Error("expected error for unknown backend")
	}
}

func TestHerdrCommandArgv(t *testing.T) {
	setBackendForTest(t, BackendHerdr)
	t.Setenv(EnvGTBin, "/fake/gt")
	prev := GetDefaultSocket()
	SetDefaultSocket("gt-abc123")
	t.Cleanup(func() { SetDefaultSocket(prev) })

	want := []string{"/fake/gt", "mux-tmux", "-u", "-L", "gt-abc123", "has-session", "-t", "=x"}

	cmd := BuildCommandContext(context.Background(), "has-session", "-t", "=x")
	if cmd.Path != "/fake/gt" || !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("BuildCommandContext argv = %v (path %s), want %v", cmd.Args, cmd.Path, want)
	}

	cmd = NewTmuxWithSocket("gt-abc123").commandContext(context.Background(), "has-session", "-t", "=x")
	if cmd.Path != "/fake/gt" || !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("commandContext argv = %v, want %v", cmd.Args, want)
	}

	cmd = CommandWithArgs("display-menu", "x")
	if !reflect.DeepEqual(cmd.Args, []string{"/fake/gt", "mux-tmux", "display-menu", "x"}) {
		t.Errorf("CommandWithArgs argv = %v", cmd.Args)
	}
}

func TestTmuxCommandArgvUnchanged(t *testing.T) {
	setBackendForTest(t, BackendTmux)
	cmd := NewTmuxWithSocket("s").commandContext(context.Background(), "list-sessions")
	if !reflect.DeepEqual(cmd.Args, []string{"tmux", "-u", "-L", "s", "list-sessions"}) {
		t.Errorf("argv = %v", cmd.Args)
	}
}

func TestExecArgv(t *testing.T) {
	setBackendForTest(t, BackendHerdr)
	t.Setenv(EnvGTBin, "/fake/gt")
	path, argv, err := ExecArgv("-u", "attach-session", "-t", "x")
	if err != nil || path != "/fake/gt" ||
		!reflect.DeepEqual(argv, []string{"/fake/gt", "mux-tmux", "-u", "attach-session", "-t", "x"}) {
		t.Errorf("herdr ExecArgv = %q %v %v", path, argv, err)
	}

	SetBackend(BackendTmux)
	path, argv, err = ExecArgv("-V")
	if err != nil {
		t.Skip("tmux not installed")
	}
	if path == "" || !reflect.DeepEqual(argv, []string{"tmux", "-V"}) {
		t.Errorf("tmux ExecArgv = %q %v", path, argv)
	}
}

func TestHerdrEnvParsing(t *testing.T) {
	t.Setenv("TMUX", "/herdr/gt-abc123,4242,0")
	t.Setenv("TMUX_PANE", "%w2.p1")
	if !IsInsideTmux() {
		t.Error("IsInsideTmux should be true")
	}
	if got := SocketFromEnv(); got != "gt-abc123" {
		t.Errorf("SocketFromEnv = %q", got)
	}
	prev := GetDefaultSocket()
	SetDefaultSocket("gt-abc123")
	t.Cleanup(func() { SetDefaultSocket(prev) })
	if !IsInSameSocket() {
		t.Error("IsInSameSocket should be true")
	}
	SetDefaultSocket("other")
	if IsInSameSocket() {
		t.Error("IsInSameSocket should be false for other socket")
	}
}

func TestSocketGuardNoopInHerdr(t *testing.T) {
	setBackendForTest(t, BackendHerdr)
	if err := NewTmuxWithSocket("gt-nonexistent-guard").ensureNewSessionSocketSafe(); err != nil {
		t.Errorf("guard should be a no-op: %v", err)
	}
}
