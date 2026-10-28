//go:build !windows

package herdrmux

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// superviseFixture creates a state dir with one session and returns the store.
func superviseFixture(t *testing.T, cmd string, mutate func(*SessionState)) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GT_HERDR_STATE_DIR", root)
	t.Setenv("GT_BIN", "/bin/true")
	t.Setenv("HERDR_PANE_ID", "w2:p1")
	store := NewStore("sock")
	out := t.TempDir()
	err := store.Update("sess", true, func(st *SessionState) error {
		st.Command = cmd
		st.Cwd = out
		if mutate != nil {
			mutate(st)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, out
}

func runSupervise(ctx context.Context) <-chan int {
	ch := make(chan int, 1)
	go func() { ch <- Supervise(ctx, "sock", "sess") }()
	return ch
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func waitExit(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("Supervise did not return")
		return -1
	}
}

func TestSuperviseChildEnvAndExit(t *testing.T) {
	store, out := superviseFixture(t, `echo "$TMUX|$TMUX_PANE|$GT_MULTIPLEXER|$FOO|$BAR|$PATH" > out.txt; exit 3`, func(st *SessionState) {
		st.Env = map[string]string{"FOO": "session", "BAR": "s"}
	})
	if err := store.UpdateGlobal(func(g *GlobalState) error {
		g.Env = map[string]string{"FOO": "global", "ONLY_GLOBAL": "g"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	code := waitExit(t, runSupervise(context.Background()))
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	got := readFile(filepath.Join(out, "out.txt"))
	parts := strings.SplitN(got, "|", 6)
	if len(parts) != 6 {
		t.Fatalf("unexpected output %q", got)
	}
	if want := "/herdr/sock," + itoa(os.Getpid()) + ",0"; parts[0] != want {
		t.Errorf("TMUX = %q, want %q", parts[0], want)
	}
	if parts[1] != "%w2.p1" {
		t.Errorf("TMUX_PANE = %q", parts[1])
	}
	if parts[2] != "herdr" {
		t.Errorf("GT_MULTIPLEXER = %q", parts[2])
	}
	if parts[3] != "session" || parts[4] != "s" {
		t.Errorf("env override wrong: FOO=%q BAR=%q", parts[3], parts[4])
	}
	if !strings.HasPrefix(parts[5], filepath.Join(StateRoot(), "bin")+":") {
		t.Errorf("PATH not prefixed with shim dir: %q", parts[5])
	}

	st, err := store.Load("sess")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Dead || st.DeadStatus != 3 {
		t.Errorf("dead=%v status=%d, want dead status 3", st.Dead, st.DeadStatus)
	}
	if st.PanePID <= 0 || st.SupervisorPID != os.Getpid() || st.HerdrPaneID != "w2:p1" {
		t.Errorf("state not recorded: %+v", st)
	}
}

func TestSupervisePaneDiedHookSessionOverridesGlobal(t *testing.T) {
	store, out := superviseFixture(t, `exit 5`, func(st *SessionState) {
		st.Hooks = map[string]string{"pane-died": `run-shell "echo session-hook > hook.txt"`}
	})
	_ = store.UpdateGlobal(func(g *GlobalState) error {
		g.Hooks = map[string]string{"pane-died": `run-shell "echo global-hook > hook.txt"`}
		return nil
	})
	if code := waitExit(t, runSupervise(context.Background())); code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	if got := readFile(filepath.Join(out, "hook.txt")); got != "session-hook" {
		t.Errorf("hook output = %q", got)
	}
}

func TestSuperviseGlobalHookAndRemainOnExit(t *testing.T) {
	store, out := superviseFixture(t, `exit 4`, nil)
	_ = store.UpdateGlobal(func(g *GlobalState) error {
		g.Hooks = map[string]string{"pane-died": `run-shell "echo status=#{pane_dead_status} > hook.txt"`}
		g.Options = map[string]string{"remain-on-exit": "on"}
		return nil
	})
	ch := runSupervise(context.Background())
	waitFor(t, "hook", func() bool { return readFile(filepath.Join(out, "hook.txt")) == "status=4" })
	waitFor(t, "dead state", func() bool {
		st, err := store.Load("sess")
		return err == nil && st.Dead && st.DeadStatus == 4
	})
	select {
	case c := <-ch:
		t.Fatalf("supervisor exited (%d) despite remain-on-exit", c)
	case <-time.After(300 * time.Millisecond):
	}
	// SIGTERM ends a remaining supervisor.
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	waitExit(t, ch)
}

func TestSuperviseRespawnKill(t *testing.T) {
	store, out := superviseFixture(t, `echo $$ > first.pid; exec sleep 60`, func(st *SessionState) {
		st.Env = map[string]string{"GEN": "one"}
	})
	ch := runSupervise(context.Background())
	waitFor(t, "first child", func() bool {
		st, err := store.Load("sess")
		return err == nil && st.PanePID > 0 && readFile(filepath.Join(out, "first.pid")) != ""
	})
	st, _ := store.Load("sess")
	first := st.PanePID
	if readFile(filepath.Join(out, "first.pid")) != itoa(first) {
		t.Errorf("pane_pid %d != child pid %s", first, readFile(filepath.Join(out, "first.pid")))
	}

	if err := store.Update("sess", false, func(st *SessionState) error {
		st.Command = `echo "$GEN" > second.txt; exec sleep 60`
		st.Env = map[string]string{"GEN": "two"}
		st.Hooks = map[string]string{"after-respawn-pane": `run-shell "echo hooked > after.txt"`}
		st.RespawnKill = true
		st.RespawnSeq++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)

	waitFor(t, "respawned child", func() bool {
		return readFile(filepath.Join(out, "second.txt")) == "two" &&
			readFile(filepath.Join(out, "after.txt")) == "hooked"
	})
	st, _ = store.Load("sess")
	if st.PanePID == first || st.Dead {
		t.Errorf("state after respawn: pane_pid=%d (was %d) dead=%v", st.PanePID, first, st.Dead)
	}
	if err := syscall.Kill(first, 0); err == nil {
		// Might be a zombie reaped already; give it a moment.
		waitFor(t, "old child gone", func() bool { return syscall.Kill(first, 0) != nil })
	}

	// Shutdown forwards SIGTERM to the child group.
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	waitExit(t, ch)
	if err := syscall.Kill(st.PanePID, 0); err == nil {
		waitFor(t, "second child gone", func() bool { return syscall.Kill(st.PanePID, 0) != nil })
	}
}

func TestSuperviseRespawnWithoutKillKeepsLiveChild(t *testing.T) {
	store, out := superviseFixture(t, `echo up >> runs.txt; exec sleep 60`, nil)
	ch := runSupervise(context.Background())
	waitFor(t, "child", func() bool { return readFile(filepath.Join(out, "runs.txt")) != "" })
	_ = store.Update("sess", false, func(st *SessionState) error {
		st.RespawnSeq++
		return nil
	})
	_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	time.Sleep(300 * time.Millisecond)
	if got := readFile(filepath.Join(out, "runs.txt")); got != "up" {
		t.Errorf("child respawned without -k: %q", got)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	waitExit(t, ch)
}

func TestSuperviseRespawnAfterDeathWithRemain(t *testing.T) {
	store, out := superviseFixture(t, `echo run >> runs.txt; exit 1`, func(st *SessionState) {
		st.Options = map[string]string{"remain-on-exit": "on"}
	})
	ch := runSupervise(context.Background())
	waitFor(t, "dead", func() bool {
		st, err := store.Load("sess")
		return err == nil && st.Dead
	})
	_ = store.Update("sess", false, func(st *SessionState) error {
		st.RespawnSeq++
		return nil
	})
	_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	waitFor(t, "second run", func() bool { return strings.Count(readFile(filepath.Join(out, "runs.txt")), "run") == 2 })
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	waitExit(t, ch)
}

func TestSuperviseBadCwdReports127(t *testing.T) {
	_, _ = superviseFixture(t, `true`, func(st *SessionState) { st.Cwd = "/nonexistent-dir-xyz" })
	if code := waitExit(t, runSupervise(context.Background())); code != 127 {
		t.Errorf("exit code = %d, want 127", code)
	}
}

func TestSuperviseMissingSession(t *testing.T) {
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	if code := Supervise(context.Background(), "sock", "nope"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
