//go:build !windows

package herdrmux

import (
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want Target
	}{
		{"", Target{Raw: "", Empty: true}},
		{"foo", Target{Raw: "foo", Session: "foo"}},
		{"=foo", Target{Raw: "=foo", Session: "foo", Exact: true}},
		{"foo:", Target{Raw: "foo:", Session: "foo"}},
		{"=foo:0", Target{Raw: "=foo:0", Session: "foo", Exact: true, Window: "0"}},
		{"foo:0.1", Target{Raw: "foo:0.1", Session: "foo", Window: "0", Pane: "1"}},
		{"%w2.p1", Target{Raw: "%w2.p1", PaneID: "w2:p1"}},
	}
	for _, c := range cases {
		if got := ParseTarget(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	if EncodePaneID("w2:p1") != "%w2.p1" || DecodePaneID("%w2.p1") != "w2:p1" {
		t.Error("pane id round trip")
	}
	if n, ok := matchSession(ParseTarget("gt-d"), []string{"gt-deacon", "hq"}); !ok || n != "gt-deacon" {
		t.Error("unique prefix should match")
	}
	if _, ok := matchSession(ParseTarget("=gt-d"), []string{"gt-deacon"}); ok {
		t.Error("exact must not prefix match")
	}
}

func TestRenderFormat(t *testing.T) {
	vars := map[string]string{"session_name": "hq", "pane_dead": "0", "x": "1", "empty": ""}
	look := func(n string) string { return vars[n] }
	cases := map[string]string{
		"#{session_name}":                       "hq",
		"a#{session_name}b":                     "ahqb",
		"#{unknown}|":                           "|",
		"##{x}":                                 "#{x}",
		"#{==:#{session_name},hq}":              "1",
		"#{==:#{session_name},zz}":              "0",
		"#{!=:#{session_name},zz}":              "1",
		"#{?pane_dead,dead,alive}":              "alive",
		"#{?x,yes,no}":                          "yes",
		"#{?#{==:#{session_name},hq},A,B}":      "A",
		"#{?#{==:#{session_name},no},A,#{x}-B}": "1-B",
		"#{session_name}\t#{pane_dead}":         "hq\t0",
		"#{&&:#{x},#{session_name}}":            "1",
		"#{||:#{empty},#{pane_dead}}":           "0",
		"plain # text":                          "plain # text",
	}
	for in, want := range cases {
		if got := RenderFormat(in, look); got != want {
			t.Errorf("RenderFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapKey(t *testing.T) {
	cases := map[string]keyAction{
		"Enter": {Key: "enter"}, "C-m": {Key: "enter"}, "Escape": {Key: "esc"},
		"C-c": {Key: "ctrl+c"}, "C-x": {Key: "ctrl+x"}, "C-U": {Key: "ctrl+u"},
		"Up": {Key: "up"}, "BSpace": {Key: "backspace"}, "Tab": {Key: "tab"},
		"Space": {Key: "space"}, "BTab": {Key: "shift+tab"}, "F5": {Key: "f5"},
		"M-x": {Key: "alt+x"}, "Delete": {Text: "\x1b[3~"}, "Home": {Text: "\x1b[H"},
		"hello world": {Text: "hello world"}, "Enterx": {Text: "Enterx"},
	}
	for in, want := range cases {
		if got := MapKey(in); got != want {
			t.Errorf("MapKey(%q) = %+v, want %+v", in, got, want)
		}
	}
	if hexKeys([]string{"68", "69", "0a"}) != "hi\n" {
		t.Error("hexKeys")
	}
}

func TestJoinCommand(t *testing.T) {
	if joinCommand([]string{"echo hi && ls"}) != "echo hi && ls" {
		t.Error("single arg verbatim")
	}
	if got := joinCommand([]string{"echo", "it's", "a b"}); got != `echo 'it'\''s' 'a b'` {
		t.Errorf("got %q", got)
	}
}

func TestNoServerAndErrorPhrases(t *testing.T) {
	_, run := newFake(t, "gt-t1")
	for _, args := range [][]string{
		{"list-sessions"}, {"has-session", "-t", "=x"}, {"list-panes", "-a"},
	} {
		_, e, code := run(args...)
		if code != 1 || !strings.Contains(e, "no server running on") {
			t.Errorf("%v: code=%d stderr=%q", args, code, e)
		}
	}
	// kill-server with no server also reports it.
	if _, e, code := run("kill-server"); code != 1 || !strings.Contains(e, "no server running") {
		t.Errorf("kill-server: %d %q", code, e)
	}
}

func TestSessionLifecycle(t *testing.T) {
	f, run := newFake(t, "gt-t2")
	f.procs["w2:p1"] = []procInfo{{Argv: []string{"/bin/bash"}, Name: "bash", Cwd: "/work", PID: 99}}

	if _, e, code := run("new-session", "-d", "-s", "hq-mayor", "-c", "/work", "-e", "A=1", "-e", "B=two words", "echo", "hi there"); code != 0 {
		t.Fatalf("new-session: %d %s", code, e)
	}
	if !f.running {
		t.Error("server not autostarted")
	}
	st, err := f.store.Load("hq-mayor")
	if err != nil {
		t.Fatal(err)
	}
	if st.Command != "echo 'hi there'" || st.Cwd != "/work" || st.Env["B"] != "two words" || st.HerdrPaneID != "w2:p1" {
		t.Errorf("state = %+v", st)
	}
	if st.PanePID != 5000 {
		t.Errorf("pane pid = %d", st.PanePID)
	}
	runs := f.herdrCalls("pane run")
	if len(runs) != 1 || runs[0][3] != "exec /fake/gt mux-supervise --sock gt-t2 --session hq-mayor" {
		t.Errorf("pane run = %v", runs)
	}

	if _, e, code := run("new-session", "-d", "-s", "hq-mayor"); code != 1 || !strings.Contains(e, "duplicate session: hq-mayor") {
		t.Errorf("dup: %d %q", code, e)
	}
	if _, _, code := run("has-session", "-t", "=hq-mayor"); code != 0 {
		t.Error("has-session should succeed")
	}
	if _, e, code := run("has-session", "-t", "=nope"); code != 1 || !strings.Contains(e, "can't find session: nope") {
		t.Errorf("missing: %d %q", code, e)
	}
	// foreign workspace "~" is ignored
	if _, _, code := run("has-session", "-t", "=~"); code == 0 {
		t.Error("untracked workspace must not be a session")
	}

	out, _, _ := run("list-sessions", "-F", "#{session_name} #{session_id} #{session_windows} #{session_attached}")
	if out != "hq-mayor $2001 1 0\n" {
		t.Errorf("list-sessions = %q", out)
	}

	out, _, _ = run("display-message", "-p", "-t", "hq-mayor", "#{pane_id} #{pane_pid} #{pane_current_command} #{pane_current_path} #{pane_dead} #{pane_start_command} #{window_index}")
	if out != "%w2.p1 5000 bash /work 0 echo 'hi there' 0\n" {
		t.Errorf("display-message = %q", out)
	}
	out, _, _ = run("display-message", "-p", "-t", "=hq-mayor", "#{?#{==:#{session_name},hq-mayor},yes,no}")
	if out != "yes\n" {
		t.Errorf("conditional = %q", out)
	}
	out, _, _ = run("list-panes", "-s", "-t", "hq-mayor", "-F", "#{pane_id}\t#{pane_current_command}\t#{pane_pid}")
	if out != "%w2.p1\tbash\t5000\n" {
		t.Errorf("list-panes = %q", out)
	}
	out, _, _ = run("list-panes", "-a", "-F", "#{pane_pid} #{session_name}")
	if out != "5000 hq-mayor\n" {
		t.Errorf("list-panes -a = %q", out)
	}
	out, _, _ = run("list-windows", "-t", "hq-mayor", "-F", "#{window_index}:#{window_name}")
	if out != "0:mayor\n" {
		t.Errorf("list-windows = %q", out)
	}
	// target by pane id
	out, _, code := run("display-message", "-p", "-t", "%w2.p1", "#{session_name}")
	if code != 0 || out != "hq-mayor\n" {
		t.Errorf("pane target = %q", out)
	}
	// current pane via TMUX_PANE
	t.Setenv("TMUX_PANE", "%w2.p1")
	out, _, _ = run("display-message", "-p", "#{session_name}")
	if out != "hq-mayor\n" {
		t.Errorf("TMUX_PANE target = %q", out)
	}
	t.Setenv("TMUX_PANE", "")

	// supervisor foreground is skipped
	f.procs["w2:p1"] = []procInfo{
		{Argv: []string{"/fake/gt", "mux-supervise", "--sock", "x"}, Name: "gt", PID: 5},
		{Argv: []string{"node", "claude"}, Name: "node", Cwd: "/w", PID: 6},
	}
	out, _, _ = run("display-message", "-p", "-t", "hq-mayor", "#{pane_current_command}")
	if out != "node\n" {
		t.Errorf("fg = %q", out)
	}
	f.procs["w2:p1"] = []procInfo{{Argv: []string{"/fake/gt", "mux-supervise"}, Name: "gt"}}
	out, _, _ = run("display-message", "-p", "-t", "hq-mayor", "#{pane_current_command}")
	if out != "gt\n" {
		t.Errorf("supervisor-only = %q", out)
	}

	// rename
	if _, e, code := run("rename-session", "-t", "hq-mayor", "hq-boss"); code != 0 {
		t.Fatalf("rename: %s", e)
	}
	if _, err := f.store.Load("hq-boss"); err != nil {
		t.Error("state not moved")
	}
	if _, _, code := run("has-session", "-t", "=hq-boss"); code != 0 {
		t.Error("renamed session missing")
	}

	// attach/switch focus
	if _, _, code := run("switch-client", "-t", "hq-boss"); code != 0 || len(f.herdrCalls("workspace focus w2")) != 1 {
		t.Error("switch-client should focus")
	}
	out, _, _ = run("list-sessions", "-F", "#{session_attached}")
	if out != "1\n" {
		t.Errorf("attached = %q", out)
	}

	// kill
	if _, e, code := run("kill-session", "-t", "=hq-boss"); code != 0 {
		t.Fatalf("kill: %s", e)
	}
	if _, err := f.store.Load("hq-boss"); err == nil {
		t.Error("state not deleted")
	}
	if _, e, code := run("list-sessions"); code != 1 || !strings.Contains(e, "no server running") {
		t.Errorf("after last kill: %d %q", code, e)
	}
}

func TestStalePrune(t *testing.T) {
	f, run := newFake(t, "gt-t3")
	run("new-session", "-d", "-s", "a")
	f.workspace = f.workspace[:1] // workspace vanished (pane exited)
	if _, _, code := run("has-session", "-t", "=a"); code == 0 {
		t.Error("session should be gone")
	}
	if _, err := f.store.Load("a"); err == nil {
		t.Error("stale state should be pruned")
	}
	if _, _, code := run("new-session", "-d", "-s", "a"); code != 0 {
		t.Error("recreate after prune failed")
	}
}

func TestSendKeysAndCapture(t *testing.T) {
	f, run := newFake(t, "gt-t4")
	run("new-session", "-d", "-s", "s")

	run("send-keys", "-t", "=s", "echo hi", "Enter")
	run("send-keys", "-t", "=s", "-l", "Enter")
	run("send-keys", "-t", "=s", "C-c")
	run("send-keys", "-t", "=s", "Escape", "Escape", "text")
	texts := f.herdrCalls("pane send-text")
	keys := f.herdrCalls("pane send-keys")
	if len(texts) != 3 || texts[0][3] != "echo hi" || texts[1][3] != "Enter" || texts[2][3] != "text" {
		t.Errorf("send-text calls = %v", texts)
	}
	if !reflect.DeepEqual(keys[0], []string{"pane", "send-keys", "w2:p1", "enter"}) {
		t.Errorf("keys[0] = %v", keys[0])
	}
	if !reflect.DeepEqual(keys[1], []string{"pane", "send-keys", "w2:p1", "ctrl+c"}) {
		t.Errorf("ctrl+c not sent: %v", keys)
	}
	last := keys[len(keys)-1]
	if !reflect.DeepEqual(last, []string{"pane", "send-keys", "w2:p1", "esc", "esc"}) {
		t.Errorf("batched keys = %v", last)
	}
	st, _ := f.store.Load("s")
	if st.Activity == 0 {
		t.Error("activity not touched")
	}

	f.reads["w2:p1 --source visible"] = "visible text\r\n"
	f.reads["w2:p1 --source recent --lines 50"] = "recent\n"
	f.reads["w2:p1 --source recent-unwrapped --lines 100000 --format ansi"] = "all\n"
	out, _, _ := run("capture-pane", "-p", "-t", "s")
	if out != "visible text\n" {
		t.Errorf("visible = %q", out)
	}
	out, _, _ = run("capture-pane", "-p", "-t", "s", "-S", "-50")
	if out != "recent\n" {
		t.Errorf("recent = %q", out)
	}
	out, _, _ = run("capture-pane", "-p", "-J", "-e", "-t", "s", "-S", "-")
	if out != "all\n" {
		t.Errorf("all = %q", out)
	}
	if out, _, code := run("capture-pane", "-t", "s"); code != 0 || out != "" {
		t.Errorf("capture without -p: %q", out)
	}
	if _, e, code := run("send-keys", "-t", "nope", "x"); code != 1 || !strings.Contains(e, "can't find session: nope") {
		t.Errorf("send to missing: %q", e)
	}
}

func TestEnvironmentAndOptions(t *testing.T) {
	_, run := newFake(t, "gt-t5")
	// global state works without a server
	if _, e, code := run("set-environment", "-g", "FOO", "bar"); code != 0 {
		t.Fatal(e)
	}
	if out, _, _ := run("show-environment", "-g", "FOO"); out != "FOO=bar\n" {
		t.Errorf("global env = %q", out)
	}
	if _, e, code := run("show-environment", "-g", "NOPE"); code != 1 || !strings.Contains(e, "unknown variable: NOPE") {
		t.Errorf("unknown var: %d %q", code, e)
	}
	run("set-environment", "-g", "-u", "FOO")
	if _, _, code := run("show-environment", "-g", "FOO"); code != 1 {
		t.Error("unset failed")
	}
	run("set-option", "-g", "remain-on-exit", "on")
	if out, _, _ := run("show-options", "-gv", "remain-on-exit"); out != "on\n" {
		t.Errorf("global opt = %q", out)
	}

	run("new-session", "-d", "-s", "s")
	run("set-environment", "-t", "s", "GT_ROLE", "mayor")
	run("set-environment", "-t", "s", "GT_X", "1")
	run("set-environment", "-t", "s", "-u", "GT_X")
	if out, _, _ := run("show-environment", "-t", "s", "GT_ROLE"); out != "GT_ROLE=mayor\n" {
		t.Errorf("session env = %q", out)
	}
	if out, _, _ := run("show-environment", "-t", "s"); out != "GT_ROLE=mayor\n" {
		t.Errorf("session env all = %q", out)
	}
	if _, e, code := run("show-environment", "-t", "s", "GT_X"); code != 1 || !strings.Contains(e, "unknown variable") {
		t.Errorf("unset session var: %q", e)
	}
	run("set-option", "-t", "s", "remain-on-exit", "on")
	run("set-option", "-t", "s", "@gt_flag", "yes")
	run("set-window-option", "-t", "s", "automatic-rename", "off")
	if out, _, _ := run("show-options", "-v", "-t", "s", "remain-on-exit"); out != "on\n" {
		t.Errorf("session opt = %q", out)
	}
	if out, _, _ := run("show-options", "-t", "s"); out != "@gt_flag yes\nautomatic-rename off\nremain-on-exit on\n" {
		t.Errorf("show-options = %q", out)
	}
	if out, _, _ := run("display-message", "-p", "-t", "s", "#{@gt_flag}"); out != "yes\n" {
		t.Errorf("user option format = %q", out)
	}
	if out, _, code := run("show-options", "-v", "-t", "s", "missing"); code != 0 || out != "" {
		t.Errorf("missing opt = %q", out)
	}
	run("set-option", "-t", "s", "-u", "remain-on-exit")
	if out, _, _ := run("show-options", "-v", "-t", "s", "remain-on-exit"); out != "" {
		t.Errorf("unset opt = %q", out)
	}
	run("set-hook", "-t", "s", "pane-died", "run-shell 'echo died'")
	run("set-hook", "-g", "after-respawn-pane", "run-shell x")
}

func TestHooksState(t *testing.T) {
	f, run := newFake(t, "gt-t6")
	run("new-session", "-d", "-s", "s")
	if _, e, code := run("set-hook", "-t", "s", "pane-died", "run-shell 'echo died'"); code != 0 {
		t.Fatal(e)
	}
	run("set-hook", "-g", "after-respawn-pane", "run-shell x")
	st, _ := f.store.Load("s")
	if st.Hooks["pane-died"] != "run-shell 'echo died'" {
		t.Errorf("hooks = %v", st.Hooks)
	}
	g, _ := f.store.LoadGlobal()
	if g.Hooks["after-respawn-pane"] != "run-shell x" {
		t.Errorf("global hooks = %v", g.Hooks)
	}
	run("set-hook", "-u", "-t", "s", "pane-died")
	st, _ = f.store.Load("s")
	if _, ok := st.Hooks["pane-died"]; ok {
		t.Error("hook not unset")
	}
}

func TestRespawnPane(t *testing.T) {
	f, run := newFake(t, "gt-t7")
	run("new-session", "-d", "-s", "s", "-c", "/a", "old-cmd")

	var signalled []int
	signalPID = func(pid int, sig syscall.Signal) error {
		if sig != syscall.SIGUSR1 {
			t.Errorf("signal = %v", sig)
		}
		signalled = append(signalled, pid)
		return nil
	}
	pidAlive = func(pid int) bool { return pid == 4000 }

	if _, e, code := run("respawn-pane", "-k", "-t", "s", "-c", "/b", "-e", "K=V", "new-cmd", "arg"); code != 0 {
		t.Fatal(e)
	}
	st, _ := f.store.Load("s")
	if st.Command != "new-cmd arg" || st.Cwd != "/b" || st.Env["K"] != "V" || !st.RespawnKill || st.RespawnSeq != 1 || st.Dead {
		t.Errorf("state = %+v", st)
	}
	if !reflect.DeepEqual(signalled, []int{4000}) {
		t.Errorf("signalled = %v", signalled)
	}
	// without a command the old one is kept
	run("respawn-pane", "-t", "s")
	st, _ = f.store.Load("s")
	if st.Command != "new-cmd arg" || st.RespawnKill || st.RespawnSeq != 2 {
		t.Errorf("state = %+v", st)
	}

	// dead supervisor: pane is relaunched
	pidAlive = func(int) bool { return false }
	before := len(f.herdrCalls("pane run"))
	if _, e, code := run("respawn-pane", "-k", "-t", "s"); code != 0 {
		t.Fatal(e)
	}
	if len(f.herdrCalls("pane run")) != before+1 {
		t.Error("expected supervisor relaunch")
	}
}

func TestKillServerAndVersion(t *testing.T) {
	f, run := newFake(t, "gt-t8")
	run("new-session", "-d", "-s", "s")
	if _, e, code := run("kill-server"); code != 0 {
		t.Fatalf("kill-server: %s", e)
	}
	if f.running {
		t.Error("session not stopped")
	}
	if _, err := f.store.Load("s"); err == nil {
		t.Error("state not removed")
	}
	if out, _, _ := run("-V"); !strings.HasPrefix(out, "tmux 3.4 (herdr 9.9.9)") {
		t.Errorf("version = %q", out)
	}
	// -S uses basename; unknown commands are no-ops
	if _, _, code := run("bind-key", "-n", "F1", "foo"); code != 0 {
		t.Error("bind-key should be a no-op")
	}
}

func TestSockFromTMUXEnv(t *testing.T) {
	if got := sockFromTMUXEnv("/herdr/gt-ab12,123,0"); got != "gt-ab12" {
		t.Errorf("got %q", got)
	}
	if sockFromTMUXEnv("/tmp/tmux-1000/default,1,0") != "" {
		t.Error("tmux socket should not match")
	}
}
