package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeClock() (func(time.Duration), *time.Duration) {
	var total time.Duration
	return func(d time.Duration) { total += d }, &total
}

func TestWaitForDaemonStart_UpAfterSlowInit(t *testing.T) {
	sleep, total := fakeClock()
	calls := 0
	probe := func() (bool, int, error) {
		calls++
		return calls > 20, 42, nil // ready after ~2s, well past the old 300ms grace
	}
	state, pid, err := waitForDaemonStart(30*time.Second, 100*time.Millisecond, probe, func() bool { return false }, sleep)
	if err != nil || state != daemonUp || pid != 42 {
		t.Fatalf("got state=%v pid=%d err=%v, want up/42/nil", state, pid, err)
	}
	if *total >= 3*time.Second {
		t.Fatalf("waited %v, expected to return as soon as ready", *total)
	}
}

func TestWaitForDaemonStart_TimeoutAliveIsStarting(t *testing.T) {
	sleep, total := fakeClock()
	state, _, err := waitForDaemonStart(time.Second, 100*time.Millisecond,
		func() (bool, int, error) { return false, 0, nil }, func() bool { return false }, sleep)
	if err != nil || state != daemonStarting {
		t.Fatalf("got state=%v err=%v, want starting", state, err)
	}
	if *total < time.Second {
		t.Fatalf("returned after %v, before the timeout", *total)
	}
	if err := daemonStartResult(t.TempDir(), 7, state, nil); !errors.Is(err, errDaemonStillStarting) {
		t.Fatalf("daemonStartResult = %v, want errDaemonStillStarting", err)
	}
}

func TestWaitForDaemonStart_ExitedFailsEarly(t *testing.T) {
	sleep, total := fakeClock()
	n := 0
	state, _, _ := waitForDaemonStart(30*time.Second, 100*time.Millisecond,
		func() (bool, int, error) { return false, 0, nil },
		func() bool { n++; return n > 3 }, sleep)
	if state != daemonExited {
		t.Fatalf("state = %v, want exited", state)
	}
	if *total > time.Second {
		t.Fatalf("waited %v for an exited process", *total)
	}
}

func TestDaemonStartResult_ExitedShowsLogTail(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "daemon", "daemon.log"), []byte("a\nb\nsomething broke\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := daemonStartResult(townRoot, 9, daemonExited, nil)
	if err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Fatalf("err = %v, want log tail included", err)
	}
	if errors.Is(err, errDaemonStillStarting) {
		t.Fatal("exited must not be reported as still starting")
	}
}
