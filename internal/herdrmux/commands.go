//go:build !windows

package herdrmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var aliases = map[string]string{
	"new": "new-session", "has": "has-session", "ls": "list-sessions",
	"lsp": "list-panes", "lsw": "list-windows", "display": "display-message",
	"send": "send-keys", "capturep": "capture-pane", "set": "set-option",
	"setw": "set-window-option", "show": "show-options", "showw": "show-window-options",
	"setenv": "set-environment", "showenv": "show-environment",
	"respawnp": "respawn-pane", "rename": "rename-session", "attach": "attach-session",
	"at": "attach-session", "switchc": "switch-client", "killp": "kill-pane",
	"killw": "kill-window", "bind": "bind-key", "unbind": "unbind-key",
	"selectw": "select-window", "neww": "new-window", "splitw": "split-window",
	"clearhist": "clear-history", "lsk": "list-keys", "refresh": "refresh-client",
	"resizew": "resize-window", "source": "source-file", "menu": "display-menu",
	"popup": "display-popup", "kill-ses": "kill-session",
}

func (m *mux) dispatch(name string, args []string) error {
	if a, ok := aliases[name]; ok {
		name = a
	}
	switch name {
	case "new-session":
		return m.cmdNewSession(args)
	case "has-session":
		return m.cmdHasSession(args)
	case "kill-session", "kill-pane", "kill-window":
		return m.cmdKillSession(name, args)
	case "kill-server":
		return m.cmdKillServer()
	case "list-sessions":
		return m.cmdListSessions(args)
	case "list-panes", "list-windows":
		return m.cmdListPanes(name, args)
	case "display-message":
		return m.cmdDisplayMessage(args)
	case "send-keys":
		return m.cmdSendKeys(args)
	case "capture-pane":
		return m.cmdCapturePane(args)
	case "set-environment":
		return m.cmdSetEnvironment(args)
	case "show-environment":
		return m.cmdShowEnvironment(args)
	case "set-option", "set-window-option":
		return m.cmdSetOption(name, args)
	case "show-options", "show-window-options":
		return m.cmdShowOptions(name, args)
	case "set-hook":
		return m.cmdSetHook(args)
	case "respawn-pane":
		return m.cmdRespawnPane(args)
	case "rename-session":
		return m.cmdRenameSession(args)
	case "attach-session":
		return m.cmdAttach(args, true)
	case "switch-client":
		return m.cmdAttach(args, false)
	case "run-shell":
		return m.cmdRunShell(args)
	}
	// clear-history, select-window, resize-window, bind-key, unbind-key,
	// display-menu, display-popup, source-file, refresh-client, list-keys,
	// new-window, split-window and anything unknown: exit 0, no output.
	return nil
}

// --- new-session ---------------------------------------------------------

func (m *mux) supervisorCommand(name string) string {
	return fmt.Sprintf("exec %s mux-supervise --sock %s --session %s",
		shellQuote(gtExecutable()), shellQuote(m.sock), shellQuote(name))
}

// waitStarted polls until the supervisor has recorded the child pid (or the
// pane died).
func (m *mux) waitStarted(name string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if st, err := m.store.Load(name); err == nil && (st.PanePID != 0 || st.Dead) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for the pane supervisor to start")
		}
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func parseEnvPairs(vals []string) map[string]string {
	env := map[string]string{}
	for _, kv := range vals {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}

func (m *mux) cmdNewSession(args []string) error {
	f, err := parseFlags("new-session", args, "dADEPX", "cefFnstxy")
	if err != nil {
		return err
	}
	name := f.val('s')
	if name == "" {
		name = "0"
	}
	if err := m.cl.StartServer(m.ctx); err != nil {
		return err
	}
	m.probed, m.up = true, true
	existing, err := m.trackedSessions()
	if err != nil {
		return err
	}
	for _, s := range existing {
		if s.Name == name {
			if f.has('A') {
				return nil
			}
			return cmdErrf("duplicate session: %s", name)
		}
	}
	cwd := f.val('c')
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	command := joinCommand(f.rest)
	if command == "" {
		command = defaultShellCommand()
	}
	env := parseEnvPairs(f.vals['e'])

	pl := Classify(name, cwd, env, command)
	pd, err := m.place(pl, cwd)
	if err != nil {
		return err
	}
	paneID := pd.Pane
	cleanup := func() {
		m.closePlaced(pd)
		_ = m.store.Delete(name)
	}
	if err := m.store.Update(name, true, func(st *SessionState) error {
		st.Cwd = cwd
		st.Env = env
		st.Command = command
		st.HerdrPaneID = paneID
		st.WorkspaceID, st.TabID, st.Kind, st.Label = pd.WS.ID, pd.TabID, pd.Kind, pd.Label
		st.PanePID, st.SupervisorPID = 0, 0
		st.Dead, st.DeadStatus = false, 0
		return nil
	}); err != nil {
		cleanup()
		return err
	}
	if err := m.cl.runInPane(m.ctx, paneID, m.supervisorCommand(name)); err != nil {
		cleanup()
		return err
	}
	if err := m.waitStarted(name, startWait); err != nil {
		cleanup()
		return err
	}
	if f.has('P') {
		format := f.val('F')
		if format == "" {
			format = "#{session_name}:"
		}
		s := &session{Name: name, WS: pd.WS, Tab: &tabInfo{ID: pd.TabID, WorkspaceID: pd.WS.ID}}
		s.State, _ = m.store.Load(name)
		if s.State == nil {
			s.State = &SessionState{}
		}
		fmt.Fprintln(m.out, m.render(format, s))
	}
	return nil
}

// --- has-session / kill-* / list-* ----------------------------------------

func (m *mux) cmdHasSession(args []string) error {
	f, err := parseFlags("has-session", args, "", "t")
	if err != nil {
		return err
	}
	_, err = m.findSession(f.val('t'))
	return err
}

func (m *mux) cmdKillSession(cmd string, args []string) error {
	f, err := parseFlags(cmd, args, "aC", "t")
	if err != nil {
		return err
	}
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	if err := m.closeSession(s); err != nil {
		return err
	}
	return m.store.Delete(s.Name)
}

func (m *mux) cmdKillServer() error {
	err := m.requireServer()
	if m.cl.Shared {
		// Not our server: close only the tabs and workspaces we created.
		if all, terr := m.trackedSessions(); terr == nil {
			for i := range all {
				_ = m.closeSession(&all[i])
			}
			m.dropFreeTabs()
		}
	} else {
		_ = m.cl.StopSession(m.ctx)
	}
	_ = m.store.RemoveAll()
	return err
}

func (m *mux) cmdListSessions(args []string) error {
	f, err := parseFlags("list-sessions", args, "", "Ff")
	if err != nil {
		return err
	}
	all, err := m.sessions()
	if err != nil {
		return err
	}
	format := f.val('F')
	if format == "" {
		format = "#{session_name}: #{session_windows} windows (created #{session_created})"
	}
	for i := range all {
		fmt.Fprintln(m.out, m.render(format, &all[i]))
	}
	return nil
}

func (m *mux) cmdListPanes(cmd string, args []string) error {
	f, err := parseFlags(cmd, args, "asf", "tFf")
	if err != nil {
		return err
	}
	format := f.val('F')
	if format == "" {
		if cmd == "list-windows" {
			format = "#{window_index}: #{window_name} (1 panes)"
		} else {
			format = "#{pane_index}: [80x24] [history 0/2000, 0 bytes] #{pane_id}"
		}
	}
	var targets []session
	if f.has('a') {
		targets, err = m.sessions()
		if err != nil {
			return err
		}
	} else {
		s, err := m.findSession(f.val('t'))
		if err != nil {
			return err
		}
		targets = []session{*s}
	}
	for i := range targets {
		fmt.Fprintln(m.out, m.render(format, &targets[i]))
	}
	return nil
}

func (m *mux) cmdDisplayMessage(args []string) error {
	f, err := parseFlags("display-message", args, "aIlNpv", "cdFt")
	if err != nil {
		return err
	}
	if !f.has('p') {
		return nil
	}
	format := strings.Join(f.rest, " ")
	if format == "" {
		format = f.val('F')
	}
	if format == "" {
		format = "[#{session_name}] #{window_index}:#{window_name}"
	}
	var s *session
	if t := f.val('t'); t != "" || currentPaneID() != "" {
		s, err = m.findSession(t)
		if err != nil {
			return err
		}
	} else if _, err := m.sessions(); err != nil {
		return err
	}
	fmt.Fprintln(m.out, m.render(format, s))
	return nil
}

// --- send-keys / capture-pane ----------------------------------------------

func (m *mux) touch(name string) {
	_ = m.store.Update(name, false, func(st *SessionState) error {
		st.Activity = time.Now().Unix()
		return nil
	})
}

func (m *mux) cmdSendKeys(args []string) error {
	f, err := parseFlags("send-keys", args, "lHRXMK", "tNc")
	if err != nil {
		return err
	}
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	if f.has('X') || len(f.rest) == 0 {
		return nil
	}
	pane := s.PaneID()
	var actions []keyAction
	if f.has('H') {
		actions = []keyAction{{Text: hexKeys(f.rest)}}
	} else {
		actions = MapKeys(f.rest, f.has('l'))
	}
	repeat := 1
	if n, err := strconv.Atoi(f.val('N')); err == nil && n > 1 {
		repeat = n
	}
	for r := 0; r < repeat; r++ {
		if err := m.flushKeys(pane, actions); err != nil {
			return err
		}
	}
	m.touch(s.Name)
	return nil
}

// flushKeys sends actions in order, batching adjacent keys and adjacent text.
func (m *mux) flushKeys(pane string, actions []keyAction) error {
	var keys []string
	var text strings.Builder
	flushKeys := func() error {
		if len(keys) == 0 {
			return nil
		}
		err := m.cl.sendKeys(m.ctx, pane, keys...)
		keys = nil
		return err
	}
	flushText := func() error {
		if text.Len() == 0 {
			return nil
		}
		err := m.cl.sendText(m.ctx, pane, text.String())
		text.Reset()
		return err
	}
	for _, a := range actions {
		if a.Key != "" {
			if err := flushText(); err != nil {
				return err
			}
			keys = append(keys, a.Key)
			continue
		}
		if err := flushKeys(); err != nil {
			return err
		}
		text.WriteString(a.Text)
	}
	if err := flushKeys(); err != nil {
		return err
	}
	return flushText()
}

func (m *mux) cmdCapturePane(args []string) error {
	f, err := parseFlags("capture-pane", args, "aepPqCJNT", "bEStF")
	if err != nil {
		return err
	}
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	if !f.has('p') {
		return nil // tmux would fill a paste buffer; nothing reads it here
	}
	source, lines := "visible", 0
	switch st := f.val('S'); st {
	case "", "0":
	case "-":
		source, lines = "recent", 100000
	default:
		if n, err := strconv.Atoi(st); err == nil && n < 0 {
			source, lines = "recent", -n
		}
	}
	if source == "recent" && f.has('J') {
		source = "recent-unwrapped"
	}
	out, err := m.cl.readPane(m.ctx, s.PaneID(), source, lines, f.has('e'))
	if err != nil {
		return err
	}
	// herdr ends lines with CR; tmux capture-pane never emits it, and screen
	// scrapers (submit verification, prompt detection) treat a stray CR as
	// text in the composer.
	out = strings.ReplaceAll(out, "\r", "")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	_, err = fmt.Fprint(m.out, out)
	return err
}

// --- environment / options / hooks -----------------------------------------

// scopedMaps runs fn on the env/options/hooks map selected by -g or -t.
// kind is "env", "options" or "hooks".
func (m *mux) update(global bool, target, kind string, fn func(map[string]string) map[string]string) error {
	if global {
		return m.store.UpdateGlobal(func(g *GlobalState) error {
			switch kind {
			case "env":
				g.Env = fn(g.Env)
			case "options":
				g.Options = fn(g.Options)
			default:
				g.Hooks = fn(g.Hooks)
			}
			return nil
		})
	}
	s, err := m.findSession(target)
	if err != nil {
		return err
	}
	return m.store.Update(s.Name, false, func(st *SessionState) error {
		switch kind {
		case "env":
			st.Env = fn(st.Env)
		case "options":
			st.Options = fn(st.Options)
		default:
			st.Hooks = fn(st.Hooks)
		}
		return nil
	})
}

func (m *mux) read(global bool, target, kind string) (map[string]string, error) {
	if global {
		g, err := m.store.LoadGlobal()
		if err != nil {
			return nil, err
		}
		switch kind {
		case "env":
			return g.Env, nil
		case "options":
			return g.Options, nil
		}
		return g.Hooks, nil
	}
	s, err := m.findSession(target)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "env":
		return s.State.Env, nil
	case "options":
		return s.State.Options, nil
	}
	return s.State.Hooks, nil
}

func ensure(mp map[string]string) map[string]string {
	if mp == nil {
		return map[string]string{}
	}
	return mp
}

func (m *mux) cmdSetEnvironment(args []string) error {
	f, err := parseFlags("set-environment", args, "gruhF", "t")
	if err != nil {
		return err
	}
	if len(f.rest) == 0 {
		return cmdErrf("usage: set-environment [-Fhgru] [-t target-session] name [value]")
	}
	key := f.rest[0]
	unset := f.has('u') || f.has('r')
	val := ""
	if len(f.rest) > 1 {
		val = f.rest[1]
	} else if !unset {
		return cmdErrf("empty value")
	}
	return m.update(f.has('g'), f.val('t'), "env", func(mp map[string]string) map[string]string {
		mp = ensure(mp)
		if unset {
			delete(mp, key)
		} else {
			mp[key] = val
		}
		return mp
	})
}

func (m *mux) cmdShowEnvironment(args []string) error {
	f, err := parseFlags("show-environment", args, "gsh", "t")
	if err != nil {
		return err
	}
	env, err := m.read(f.has('g'), f.val('t'), "env")
	if err != nil {
		return err
	}
	if len(f.rest) > 0 {
		k := f.rest[0]
		v, ok := env[k]
		if !ok {
			return cmdErrf("unknown variable: %s", k)
		}
		fmt.Fprintf(m.out, "%s=%s\n", k, v)
		return nil
	}
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(m.out, "%s=%s\n", k, env[k])
	}
	return nil
}

func (m *mux) cmdSetOption(cmd string, args []string) error {
	f, err := parseFlags(cmd, args, "agopqsuwF", "t")
	if err != nil {
		return err
	}
	if len(f.rest) == 0 {
		return cmdErrf("usage: %s [-aFgopqsuw] [-t target] option [value]", cmd)
	}
	opt := f.rest[0]
	val := ""
	if len(f.rest) > 1 {
		val = f.rest[1]
	}
	// -s (server) options and -g both live in the global state.
	global := f.has('g') || f.has('s')
	return m.update(global, f.val('t'), "options", func(mp map[string]string) map[string]string {
		mp = ensure(mp)
		switch {
		case f.has('u'):
			delete(mp, opt)
		case f.has('o') && mpHas(mp, opt):
		case f.has('a'):
			mp[opt] += val
		default:
			mp[opt] = val
		}
		return mp
	})
}

func mpHas(mp map[string]string, k string) bool { _, ok := mp[k]; return ok }

func (m *mux) cmdShowOptions(cmd string, args []string) error {
	f, err := parseFlags(cmd, args, "AgHpqsvw", "t")
	if err != nil {
		return err
	}
	opts, err := m.read(f.has('g') || f.has('s'), f.val('t'), "options")
	if err != nil {
		return err
	}
	if len(f.rest) > 0 {
		k := f.rest[0]
		v, ok := opts[k]
		if !ok {
			return nil
		}
		if f.has('v') {
			fmt.Fprintln(m.out, v)
		} else {
			fmt.Fprintf(m.out, "%s %s\n", k, v)
		}
		return nil
	}
	for _, k := range sortedKeys(opts) {
		if f.has('v') {
			fmt.Fprintln(m.out, opts[k])
		} else {
			fmt.Fprintf(m.out, "%s %s\n", k, opts[k])
		}
	}
	return nil
}

func (m *mux) cmdSetHook(args []string) error {
	f, err := parseFlags("set-hook", args, "agpRuw", "t")
	if err != nil {
		return err
	}
	if len(f.rest) == 0 {
		return cmdErrf("usage: set-hook [-agpRuw] [-t target] hook [command]")
	}
	hook := f.rest[0]
	cmd := strings.Join(f.rest[1:], " ")
	if len(f.rest) == 1 && !f.has('u') && !f.has('R') {
		return cmdErrf("set-hook: missing command")
	}
	if f.has('R') {
		return nil
	}
	return m.update(f.has('g'), f.val('t'), "hooks", func(mp map[string]string) map[string]string {
		mp = ensure(mp)
		if f.has('u') {
			delete(mp, hook)
		} else {
			mp[hook] = cmd
		}
		return mp
	})
}

// --- respawn-pane / rename-session / attach ---------------------------------

func (m *mux) cmdRespawnPane(args []string) error {
	f, err := parseFlags("respawn-pane", args, "k", "cet")
	if err != nil {
		return err
	}
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	command := joinCommand(f.rest)
	env := parseEnvPairs(f.vals['e'])
	oldPID := s.State.PanePID
	var sup int
	if err := m.store.Update(s.Name, false, func(st *SessionState) error {
		if command != "" {
			st.Command = command
		}
		if c := f.val('c'); c != "" {
			st.Cwd = c
		}
		if len(env) > 0 {
			st.Env = ensure(st.Env)
			for k, v := range env {
				st.Env[k] = v
			}
		}
		st.RespawnKill = f.has('k')
		st.RespawnSeq++
		st.Dead = false
		st.Activity = time.Now().Unix()
		sup = st.SupervisorPID
		return nil
	}); err != nil {
		return err
	}
	if pidAlive(sup) {
		if err := signalPID(sup, syscall.SIGUSR1); err != nil {
			return fmt.Errorf("signalling supervisor: %w", err)
		}
		m.waitRespawned(s.Name, oldPID)
		return nil
	}
	return m.recreatePane(s)
}

// waitRespawned waits (bounded) for the supervisor to report a new child.
func (m *mux) waitRespawned(name string, oldPID int) {
	deadline := time.Now().Add(respawnWait)
	for time.Now().Before(deadline) {
		if st, err := m.store.Load(name); err == nil && st.PanePID != 0 && st.PanePID != oldPID {
			return
		}
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// recreatePane restarts a session whose supervisor is gone: reuse the
// existing pane if herdr still has it, else make a new workspace with the
// same label.
func (m *mux) recreatePane(s *session) error {
	paneID := s.PaneID()
	panes, err := m.cl.listPanes(m.ctx, s.WS.ID)
	if err != nil {
		return err
	}
	have := false
	for _, p := range panes {
		if p.ID == paneID {
			have = true
		}
	}
	var newWS, newTab, newKind, newLabel string
	if !have {
		st, _ := m.store.Load(s.Name)
		cwd := ""
		var env map[string]string
		command := ""
		if st != nil {
			cwd, env, command = st.Cwd, st.Env, st.Command
		}
		if s.State.Kind == "" {
			// legacy one-workspace-per-session state
			c, err := m.cl.createWorkspace(m.ctx, s.Name, cwd, nil)
			if err != nil {
				return err
			}
			paneID = c.PaneID
			newWS = c.WS.ID
			if len(panes) == 0 {
				_ = m.cl.closeWorkspace(m.ctx, s.WS.ID)
			}
		} else {
			pd, err := m.place(Classify(s.Name, cwd, env, command), cwd)
			if err != nil {
				return err
			}
			paneID = pd.Pane
			newWS, newTab, newKind, newLabel = pd.WS.ID, pd.TabID, pd.Kind, pd.Label
			if s.State.Kind == KindTab {
				_ = m.cl.closeTab(m.ctx, s.State.TabID)
			} else {
				_ = m.cl.closeWorkspace(m.ctx, s.WS.ID)
			}
		}
	}
	if err := m.store.Update(s.Name, false, func(st *SessionState) error {
		st.HerdrPaneID = paneID
		if newWS != "" {
			st.WorkspaceID, st.TabID = newWS, newTab
			if newKind != "" {
				st.Kind, st.Label = newKind, newLabel
			}
		}
		st.PanePID, st.SupervisorPID = 0, 0
		st.Dead = false
		return nil
	}); err != nil {
		return err
	}
	if err := m.cl.runInPane(m.ctx, paneID, m.supervisorCommand(s.Name)); err != nil {
		return err
	}
	return m.waitStarted(s.Name, startWait)
}

func (m *mux) cmdRenameSession(args []string) error {
	f, err := parseFlags("rename-session", args, "", "t")
	if err != nil {
		return err
	}
	if len(f.rest) == 0 {
		return cmdErrf("usage: rename-session [-t target-session] new-name")
	}
	newName := f.rest[0]
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	if newName == s.Name {
		return nil
	}
	if _, err := m.store.Load(newName); err == nil {
		return cmdErrf("duplicate session: %s", newName)
	} else if !errors.Is(err, ErrNoState) {
		return err
	}
	label := newName
	if s.State.Kind != "" {
		label = Classify(newName, s.State.Cwd, s.State.Env, s.State.Command).Label
		if s.State.Kind == KindWorktree {
			label = s.State.Label // the worktree workspace keeps its worker name
		}
	}
	if s.State.Kind == KindTab {
		if label != s.State.Label {
			if err := m.cl.renameTab(m.ctx, s.State.TabID, label); err != nil {
				return err
			}
		}
	} else if label != s.WS.Label {
		if err := m.cl.renameWorkspace(m.ctx, s.WS.ID, label); err != nil {
			return err
		}
	}
	if err := m.store.Rename(s.Name, newName); err != nil {
		return err
	}
	if s.State.Kind != "" {
		return m.store.Update(newName, false, func(st *SessionState) error {
			st.Label = label
			return nil
		})
	}
	return nil
}

// attachExec replaces the process with an interactive herdr client. Tests
// replace it.
var attachExec = func(bin, sock string) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return err
	}
	return syscall.Exec(path, []string{bin, "--session", sock}, scrubbedEnv())
}

func (m *mux) cmdAttach(args []string, attach bool) error {
	cmd := "switch-client"
	if attach {
		cmd = "attach-session"
	}
	f, err := parseFlags(cmd, args, "dErxZlnp", "ctT")
	if err != nil {
		return err
	}
	s, err := m.findSession(f.val('t'))
	if err != nil {
		return err
	}
	if err := m.cl.focusWorkspace(m.ctx, s.WS.ID); err != nil {
		return err
	}
	if s.State.Kind == KindTab {
		if err := m.cl.focusTab(m.ctx, s.State.TabID); err != nil {
			return err
		}
	}
	if !attach {
		return nil
	}
	if m.insideTargetSession() {
		return nil // focusing is enough; exec'ing herdr here would nest it
	}
	if in, ok := m.stdin.(*os.File); ok {
		if fi, err := in.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return attachExec(m.cl.Bin, m.cl.Sock)
		}
	}
	return nil
}

// insideTargetSession reports whether the caller runs in a pane of the herdr
// session the town lives in.
func (m *mux) insideTargetSession() bool {
	sockPath := os.Getenv("HERDR_SOCKET_PATH")
	if sockPath == "" {
		return false
	}
	_, path, err := m.cl.SessionStatus(m.ctx)
	return err == nil && path != "" && filepath.Clean(path) == filepath.Clean(sockPath)
}
