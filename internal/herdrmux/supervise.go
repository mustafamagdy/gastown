//go:build !windows

package herdrmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	hookTimeout      = 30 * time.Second
	killGracePeriod  = 2 * time.Second
	shutdownGrace    = 3 * time.Second
	hookPaneDied     = "pane-died"
	hookAfterRespawn = "after-respawn-pane"
)

// childProc is one running child of the supervisor.
type childProc struct {
	pid  int
	done chan int // receives the exit status once
}

type supervisor struct {
	store   *Store
	sock    string
	session string
	paneID  string // herdr pane id, e.g. "w2:p1"
	shimDir string
	ttyFd   int // controlling terminal fd, or -1 when stdin is not a terminal
	sigCh   chan os.Signal

	mu   sync.Mutex
	pgid int // process group of the running child, 0 if none
}

// Supervise runs inside a herdr pane and gives it tmux-like pane semantics:
// it runs the session's command as a child (the foreground process group of
// the pane terminal), records pid / death in the session state, fires hooks,
// honours remain-on-exit and handles respawn requests (SIGUSR1). It returns
// the process exit code.
func Supervise(ctx context.Context, sock, session string) int {
	store := NewStore(sock)
	st, err := store.Load(session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mux-supervise: session %q: %v\n", session, err)
		return 1
	}

	s := &supervisor{store: store, sock: sock, session: session, ttyFd: -1}
	s.paneID = os.Getenv("HERDR_PANE_ID")
	if s.paneID == "" {
		s.paneID = st.HerdrPaneID
	}
	if dir, err := EnsureShim(); err == nil {
		s.shimDir = dir
	}
	if isTerminalFd(int(os.Stdin.Fd())) {
		s.ttyFd = int(os.Stdin.Fd())
	}

	rootCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The supervisor must survive terminal job-control signals, but it must
	// not *ignore* them: ignored signals are inherited across exec and the
	// child would stop reacting to Ctrl-C. Caught signals are reset to the
	// default on exec, so we catch and drop them.
	s.sigCh = make(chan os.Signal, 16)
	signal.Notify(s.sigCh, syscall.SIGINT, syscall.SIGTSTP, syscall.SIGTTOU, syscall.SIGTTIN,
		syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1)
	defer signal.Stop(s.sigCh)
	usr1 := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-rootCtx.Done():
				return
			case sig := <-s.sigCh:
				switch sig {
				case syscall.SIGUSR1:
					select {
					case usr1 <- struct{}{}:
					default:
					}
				case syscall.SIGTERM, syscall.SIGHUP:
					s.mu.Lock()
					pgid := s.pgid
					s.mu.Unlock()
					if pgid > 0 {
						_ = syscall.Kill(-pgid, sig.(syscall.Signal))
					}
					cancel()
				}
			}
		}
	}()

	pid := os.Getpid()
	_ = store.Update(session, false, func(st *SessionState) error {
		st.SupervisorPID = pid
		if s.paneID != "" {
			st.HerdrPaneID = s.paneID
		}
		return nil
	})

	lastSeq := st.RespawnSeq
	var child *childProc
	var childDone chan int
	startFrom := func(st *SessionState) {
		c, err := s.start(st)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mux-supervise: %v\n", err)
			child, childDone = nil, make(chan int, 1)
			childDone <- 127
			return
		}
		child, childDone = c, c.done
	}
	startFrom(st)

	for {
		select {
		case <-rootCtx.Done():
			s.shutdown(child, childDone)
			return 128 + int(syscall.SIGTERM)

		case status := <-childDone:
			child, childDone = nil, nil
			s.setPgid(0)
			s.reclaimForeground()
			s.recordDeath(status)
			cur, err := s.store.Load(session)
			if err != nil {
				return status
			}
			s.runHook(rootCtx, cur, hookPaneDied)
			if s.remainOnExit(cur) {
				fmt.Fprintf(os.Stdout, "Pane is dead (status %d)\r\n", status)
				continue
			}
			return status

		case <-usr1:
			cur, err := s.store.Load(session)
			if err != nil || cur.RespawnSeq == lastSeq {
				continue
			}
			lastSeq = cur.RespawnSeq
			if child != nil {
				if !cur.RespawnKill {
					continue // tmux refuses to respawn a live pane without -k
				}
				s.killChild(child, childDone)
				child, childDone = nil, nil
				s.setPgid(0)
				s.reclaimForeground()
			}
			startFrom(cur)
			s.runHook(rootCtx, cur, hookAfterRespawn)
		}
	}
}

// start launches st.Command and records it in the state.
func (s *supervisor) start(st *SessionState) (*childProc, error) {
	command := st.Command
	if command == "" {
		command = "exec " + shellOrDefault()
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = st.Cwd
	cmd.Env = s.buildEnv(st)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if s.ttyFd >= 0 {
		// Go blocks signals around the child's TIOCSPGRP, so no SIGTTOU.
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = s.ttyFd
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", command, err)
	}
	c := &childProc{pid: cmd.Process.Pid, done: make(chan int, 1)}
	s.setPgid(c.pid)
	go func() {
		err := cmd.Wait()
		c.done <- exitStatus(cmd, err)
	}()
	_ = s.store.Update(s.session, false, func(st *SessionState) error {
		st.PanePID = c.pid
		st.Dead = false
		st.DeadStatus = 0
		return nil
	})
	return c, nil
}

func exitStatus(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		if err != nil {
			return 1
		}
		return 0
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

func shellOrDefault() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// buildEnv builds the environment for the child and for hooks: the pane's own
// environment, global then session overrides, and the tmux-compat variables.
func (s *supervisor) buildEnv(st *SessionState) []string {
	env := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			order = append(order, k)
		}
		env[k] = v
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}
	if g, err := s.store.LoadGlobal(); err == nil {
		for k, v := range g.Env {
			set(k, v)
		}
	}
	for k, v := range st.Env {
		set(k, v)
	}
	set("TMUX", fmt.Sprintf("/herdr/%s,%d,0", s.sock, os.Getpid()))
	if s.paneID != "" {
		set("TMUX_PANE", "%"+strings.ReplaceAll(s.paneID, ":", "."))
	}
	set("GT_MULTIPLEXER", "herdr")
	if s.shimDir != "" {
		if p := env["PATH"]; p != "" {
			set("PATH", s.shimDir+string(os.PathListSeparator)+p)
		} else {
			set("PATH", s.shimDir)
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

func (s *supervisor) setPgid(pgid int) {
	s.mu.Lock()
	s.pgid = pgid
	s.mu.Unlock()
}

func (s *supervisor) recordDeath(status int) {
	_ = s.store.Update(s.session, false, func(st *SessionState) error {
		st.Dead = true
		st.DeadStatus = status
		return nil
	})
}

func (s *supervisor) remainOnExit(st *SessionState) bool {
	v, ok := st.Options["remain-on-exit"]
	if !ok {
		if g, err := s.store.LoadGlobal(); err == nil {
			v = g.Options["remain-on-exit"]
		}
	}
	return v == "on"
}

// runHook runs the named hook (session hooks override global ones) via
// /bin/sh -c with a 30s timeout.
func (s *supervisor) runHook(ctx context.Context, st *SessionState, name string) {
	hook, ok := st.Hooks[name]
	if !ok {
		if g, err := s.store.LoadGlobal(); err == nil {
			hook = g.Hooks[name]
		}
	}
	if strings.TrimSpace(hook) == "" {
		return
	}
	hctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	cmd := exec.CommandContext(hctx, "/bin/sh", "-c", hook)
	cmd.Dir = st.Cwd
	cmd.Env = s.buildEnv(st)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil && hctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mux-supervise: hook %s: %v\n", name, err)
	}
}

// killChild terminates the child's process group: SIGTERM, then SIGKILL after
// killGracePeriod. It returns once the child has exited.
func (s *supervisor) killChild(c *childProc, done chan int) {
	_ = syscall.Kill(-c.pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(killGracePeriod):
		_ = syscall.Kill(-c.pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(killGracePeriod):
		}
	}
}

// shutdown is called after SIGTERM/SIGHUP was forwarded to the child group.
func (s *supervisor) shutdown(c *childProc, done chan int) {
	if c == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		_ = syscall.Kill(-c.pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(killGracePeriod):
		}
	}
	s.reclaimForeground()
	s.recordDeath(128 + int(syscall.SIGTERM))
}

// reclaimForeground makes the supervisor's own process group the terminal's
// foreground group again after the child group is gone.
func (s *supervisor) reclaimForeground() {
	if s.ttyFd < 0 {
		return
	}
	// tcsetpgrp from a background group raises SIGTTOU; ignore it for the call
	// only (an ignored signal would be inherited by later children), then
	// catch it again.
	signal.Ignore(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(s.ttyFd, unix.TIOCSPGRP, syscall.Getpgrp())
	signal.Notify(s.sigCh, syscall.SIGTTOU)
}

func isTerminalFd(fd int) bool {
	_, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil
}
