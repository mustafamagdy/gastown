package herdrmux

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// session is a tracked tmux-style session: a herdr workspace whose label is
// the session name and which has state in the Store.
type session struct {
	Name  string
	WS    wsInfo
	State *SessionState
}

// PaneID is the session's herdr pane id.
func (s *session) PaneID() string { return s.State.HerdrPaneID }

// Test seams for process signalling.
var (
	signalPID = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	pidAlive  = func(pid int) bool {
		if pid <= 0 {
			return false
		}
		err := syscall.Kill(pid, 0)
		return err == nil || err == syscall.EPERM
	}
	// respawnWait bounds how long respawn-pane waits for the supervisor to
	// start the new child.
	respawnWait = 3 * time.Second
	// startWait bounds how long new-session waits for the supervisor.
	startWait = 5 * time.Second
)

func workspaceOfPane(paneID string) string {
	if i := strings.IndexByte(paneID, ':'); i > 0 {
		return paneID[:i]
	}
	return ""
}

// requireServer fails with tmux's "no server running" error unless the herdr
// session's server is up.
func (m *mux) requireServer() error {
	if !m.probed {
		up, path, err := m.cl.SessionStatus(m.ctx)
		if err != nil {
			return err
		}
		m.probed, m.up, m.sockP = true, up, path
	}
	if !m.up {
		return m.noServer()
	}
	return nil
}

func (m *mux) noServer() error {
	p := m.sockP
	if p == "" {
		p = filepath.Join("/tmp", "herdr-"+m.sock, "herdr.sock")
	}
	return cmdErrf("no server running on %s", p)
}

// sessions lists tracked sessions. Like a tmux server that exits when its
// last session goes, an up server with no tracked sessions reports
// "no server running". State whose workspace is gone is pruned.
func (m *mux) sessions() ([]session, error) {
	if err := m.requireServer(); err != nil {
		return nil, err
	}
	out, err := m.trackedSessions()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, m.noServer()
	}
	return out, nil
}

func (m *mux) trackedSessions() ([]session, error) {
	names, err := m.store.List()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	wss, err := m.cl.listWorkspaces(m.ctx)
	if err != nil {
		return nil, err
	}
	var out []session
	for _, n := range names {
		st, err := m.store.Load(n)
		if err != nil {
			continue
		}
		wid := workspaceOfPane(st.HerdrPaneID)
		var found *wsInfo
		for i := range wss {
			if wss[i].Label == n && (wid == "" || wss[i].ID == wid) {
				found = &wss[i]
				break
			}
		}
		if found == nil {
			_ = m.store.Delete(n)
			continue
		}
		out = append(out, session{Name: n, WS: *found, State: st})
	}
	return out, nil
}

// findSession resolves a -t target to a tracked session.
func (m *mux) findSession(raw string) (*session, error) {
	t := ParseTarget(raw)
	all, err := m.sessions()
	if err != nil {
		return nil, err
	}
	switch {
	case t.Empty:
		id := currentPaneID()
		if id == "" {
			return nil, cmdErrf("no current target")
		}
		return findByPane(all, id, EncodePaneID(id))
	case t.PaneID != "":
		return findByPane(all, t.PaneID, EncodePaneID(t.PaneID))
	}
	if strings.HasPrefix(t.Session, "$") {
		for i := range all {
			if fmt.Sprintf("$%d", all[i].WS.Number) == t.Session {
				return &all[i], nil
			}
		}
		return nil, cmdErrf("can't find session: %s", t.Session)
	}
	names := make([]string, len(all))
	for i := range all {
		names[i] = all[i].Name
	}
	n, ok := matchSession(t, names)
	if !ok {
		return nil, cmdErrf("can't find session: %s", t.Session)
	}
	for i := range all {
		if all[i].Name == n {
			return &all[i], nil
		}
	}
	return nil, cmdErrf("can't find session: %s", t.Session)
}

func findByPane(all []session, id, enc string) (*session, error) {
	for i := range all {
		if all[i].State.HerdrPaneID == id {
			return &all[i], nil
		}
	}
	return nil, cmdErrf("can't find pane: %s", enc)
}

// fmtCtx renders formats for one session.
type fmtCtx struct {
	m     *mux
	s     *session // nil: no session variables
	procs []procInfo
	procE error
	done  bool
}

func (f *fmtCtx) loadProcs() {
	if f.done {
		return
	}
	f.done = true
	if f.s == nil || f.s.PaneID() == "" {
		f.procE = fmt.Errorf("no pane")
		return
	}
	f.procs, _, f.procE = f.m.cl.processInfo(f.m.ctx, f.s.PaneID())
}

// currentCommand picks the foreground process that is not the supervisor.
func currentCommand(procs []procInfo) (name, cwd string) {
	var sup *procInfo
	for i := range procs {
		p := &procs[i]
		isSup := false
		for _, a := range p.Argv {
			if strings.Contains(a, "mux-supervise") {
				isSup = true
				break
			}
		}
		if strings.Contains(p.Cmdline, "mux-supervise") {
			isSup = true
		}
		if isSup {
			if sup == nil {
				sup = p
			}
			continue
		}
		return p.Name, p.Cwd
	}
	if sup != nil {
		return sup.Name, sup.Cwd
	}
	return "", ""
}

func (f *fmtCtx) lookup(name string) string {
	if strings.HasPrefix(name, "@") {
		if f.s != nil {
			if v, ok := f.s.State.Options[name]; ok {
				return v
			}
		}
		if g, err := f.m.store.LoadGlobal(); err == nil {
			return g.Options[name]
		}
		return ""
	}
	s := f.s
	switch name {
	case "client_tty":
		return ""
	case "pid":
		return "0"
	case "pane_in_mode", "pane_index", "window_index":
		return "0"
	case "pane_active", "window_active":
		return "1"
	case "window_width", "pane_width":
		return "80"
	case "window_height", "pane_height":
		return "24"
	case "session_windows", "window_panes":
		return "1"
	}
	if s == nil {
		return ""
	}
	switch name {
	case "session_name", "window_name":
		return s.Name
	case "session_id":
		return fmt.Sprintf("$%d", s.WS.Number)
	case "window_id":
		return fmt.Sprintf("@%d", s.WS.Number)
	case "session_created":
		return fmt.Sprint(s.State.Created)
	case "session_activity", "window_activity":
		return fmt.Sprint(s.State.Activity)
	case "session_last_attached":
		return "0"
	case "session_attached":
		if s.WS.Focused {
			return "1"
		}
		return "0"
	case "pane_id":
		return EncodePaneID(s.PaneID())
	case "pane_pid":
		return fmt.Sprint(s.State.PanePID)
	case "pane_dead":
		if s.State.Dead {
			return "1"
		}
		return "0"
	case "pane_dead_status":
		if s.State.Dead {
			return fmt.Sprint(s.State.DeadStatus)
		}
		return ""
	case "pane_start_command":
		return s.State.Command
	case "pane_current_command":
		f.loadProcs()
		if f.procE == nil {
			if n, _ := currentCommand(f.procs); n != "" {
				return n
			}
		}
		return filepath.Base(strings.Fields(s.State.Command + " sh")[0])
	case "pane_current_path":
		f.loadProcs()
		if f.procE == nil {
			if _, c := currentCommand(f.procs); c != "" {
				return c
			}
		}
		return s.State.Cwd
	}
	return ""
}

// render expands a format for session s (which may be nil).
func (m *mux) render(format string, s *session) string {
	f := &fmtCtx{m: m, s: s}
	return RenderFormat(format, f.lookup)
}
