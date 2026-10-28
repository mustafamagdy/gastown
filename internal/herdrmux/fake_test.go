//go:build !windows

package herdrmux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// fakeHerdr is an in-memory stand-in for the herdr CLI.
type fakeHerdr struct {
	mu        sync.Mutex
	t         *testing.T
	sock      string
	running   bool
	nextWS    int
	workspace []fakeWS
	calls     [][]string
	reads     map[string]string
	procs     map[string][]procInfo
	store     *Store
	supPID    int  // SupervisorPID the fake "supervisor" records
	shared    bool // client reports a shared (non-dedicated) herdr session
	// worktrees is the set of paths `worktree open` accepts; others fail
	// with worktree_not_found.
	worktrees   map[string]bool
	failTabList bool // `tab list` errors (herdr hiccup)
}

type fakeTab struct {
	ID, Label, Pane string
	Number          int
	Focused         bool
}

type fakeWS struct {
	ID, Label string
	Number    int
	Focused   bool
	Tabs      []fakeTab
	nextTab   int
	nextPane  int
	Worktree  string // checkout path when this is a linked-worktree workspace
}

func newFake(t *testing.T, sock string) (*fakeHerdr, func(args ...string) (string, string, int)) {
	t.Helper()
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_BIN", "/fake/gt")
	f := &fakeHerdr{t: t, sock: sock, nextWS: 2, reads: map[string]string{}, procs: map[string][]procInfo{}, supPID: 4000}
	f.workspace = []fakeWS{{ID: "w1", Label: "~", Number: 1, Focused: true,
		Tabs: []fakeTab{{ID: "w1:t1", Label: "1", Pane: "w1:p1", Number: 1, Focused: true}}, nextTab: 2, nextPane: 2}}
	f.worktrees = map[string]bool{}
	oldNew := newClientFunc
	oldSig, oldAlive := signalPID, pidAlive
	oldRW, oldSW := respawnWait, startWait
	respawnWait, startWait = 0, 300*1000*1000
	t.Cleanup(func() {
		newClientFunc = oldNew
		signalPID, pidAlive = oldSig, oldAlive
		respawnWait, startWait = oldRW, oldSW
	})
	newClientFunc = func(s string) *Client {
		f.store = NewStore(s)
		return &Client{Sock: s, Shared: f.shared, Bin: "herdr", store: f.store, exec: f.exec,
			spawn: func(context.Context) error { f.running = true; return nil }}
	}
	run := func(args ...string) (string, string, int) {
		var o, e bytes.Buffer
		code := Run(context.Background(), append([]string{"-u", "-L", sock}, args...), strings.NewReader(""), &o, &e)
		return o.String(), e.String(), code
	}
	return f, run
}

func ok(result any) ([]byte, []byte, int, error) {
	b, _ := json.Marshal(map[string]any{"id": "x", "result": result})
	return b, nil, 0, nil
}

func fail(code, msg string) ([]byte, []byte, int, error) {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	return nil, b, 1, nil
}

func (f *fakeHerdr) wsJSON(w fakeWS) map[string]any {
	return map[string]any{"workspace_id": w.ID, "label": w.Label, "number": w.Number, "focused": w.Focused, "pane_count": len(w.Tabs), "tab_count": len(w.Tabs)}
}

func (f *fakeHerdr) tabJSON(w fakeWS, t fakeTab) map[string]any {
	return map[string]any{"tab_id": t.ID, "workspace_id": w.ID, "label": t.Label, "number": t.Number, "focused": t.Focused}
}

// newWS adds a workspace with one root tab.
func (f *fakeHerdr) newWS(label string) *fakeWS {
	id := fmt.Sprintf("w%d", f.nextWS)
	w := fakeWS{ID: id, Label: label, Number: f.nextWS, nextTab: 2, nextPane: 2,
		Tabs: []fakeTab{{ID: id + ":t1", Label: "1", Pane: id + ":p1", Number: 1, Focused: true}}}
	f.nextWS++
	f.workspace = append(f.workspace, w)
	return &f.workspace[len(f.workspace)-1]
}

func (f *fakeHerdr) createJSON(w fakeWS, t fakeTab) map[string]any {
	return map[string]any{"workspace": f.wsJSON(w), "tab": f.tabJSON(w, t),
		"root_pane": map[string]any{"pane_id": t.Pane, "tab_id": t.ID, "workspace_id": w.ID}}
}

func flagVal(a []string, name string) string {
	for i := 0; i+1 < len(a); i++ {
		if a[i] == name {
			return a[i+1]
		}
	}
	return ""
}

// findTab locates a tab by id.
func (f *fakeHerdr) findTab(id string) (wi, ti int) {
	for i, w := range f.workspace {
		for j, t := range w.Tabs {
			if t.ID == id {
				return i, j
			}
		}
	}
	return -1, -1
}

// wsLabels returns the labels of all workspaces except the user's "~".
func (f *fakeHerdr) wsLabels() []string {
	var out []string
	for _, w := range f.workspace {
		if w.Label != "~" {
			out = append(out, w.Label)
		}
	}
	return out
}

// tabLabels returns the tab labels of the workspace with the given label.
func (f *fakeHerdr) tabLabels(ws string) []string {
	var out []string
	for _, w := range f.workspace {
		if w.Label == ws {
			for _, t := range w.Tabs {
				out = append(out, t.Label)
			}
		}
	}
	return out
}

func (f *fakeHerdr) exec(ctx context.Context, bin string, args []string, env []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	for _, e := range env {
		if strings.HasPrefix(e, "HERDR_") {
			f.t.Errorf("HERDR_ var leaked to child env: %s", e)
		}
	}
	if len(args) >= 2 && args[0] == "session" && args[1] == "list" {
		return ok2(map[string]any{"sessions": []map[string]any{
			{"name": "default", "running": true, "socket_path": "/h/herdr.sock"},
			{"name": f.sock, "running": f.running, "socket_path": "/h/sessions/" + f.sock + "/herdr.sock"},
		}})
	}
	if len(args) >= 2 && args[0] == "session" && (args[1] == "stop" || args[1] == "delete") {
		if args[2] != f.sock {
			f.t.Errorf("session %s on wrong name %s", args[1], args[2])
		}
		f.running = false
		f.workspace = nil
		return []byte("{}"), nil, 0, nil
	}
	if len(args) >= 1 && args[0] == "--version" {
		return []byte("herdr 9.9.9\n"), nil, 0, nil
	}
	if len(args) < 2 || args[0] != "--session" || args[1] != f.sock {
		f.t.Errorf("herdr call without --session %s: %v", f.sock, args)
		return fail("bad", "no session")
	}
	if !f.running {
		return nil, []byte("cannot connect"), 1, nil
	}
	a := args[2:]
	switch a[0] + " " + a[1] {
	case "workspace list":
		var l []map[string]any
		for _, w := range f.workspace {
			l = append(l, f.wsJSON(w))
		}
		return ok(map[string]any{"workspaces": l})
	case "workspace create":
		w := f.newWS(flagVal(a, "--label"))
		return ok(f.createJSON(*w, w.Tabs[0]))
	case "workspace close":
		for i, w := range f.workspace {
			if w.ID == a[2] {
				f.workspace = append(f.workspace[:i], f.workspace[i+1:]...)
				return ok(map[string]any{"type": "ok"})
			}
		}
		return fail("workspace_not_found", "workspace not found")
	case "workspace rename":
		for i := range f.workspace {
			if f.workspace[i].ID == a[2] {
				f.workspace[i].Label = a[3]
			}
		}
		return ok(map[string]any{})
	case "workspace focus":
		for i := range f.workspace {
			f.workspace[i].Focused = f.workspace[i].ID == a[2]
		}
		return ok(map[string]any{})
	case "tab list":
		if f.failTabs() {
			return fail("internal", "boom")
		}
		var l []map[string]any
		for _, w := range f.workspace {
			if ws := flagVal(a, "--workspace"); ws != "" && ws != w.ID {
				continue
			}
			for _, t := range w.Tabs {
				l = append(l, f.tabJSON(w, t))
			}
		}
		return ok(map[string]any{"tabs": l})
	case "tab create":
		ws := flagVal(a, "--workspace")
		for i := range f.workspace {
			w := &f.workspace[i]
			if w.ID != ws {
				continue
			}
			t := fakeTab{ID: fmt.Sprintf("%s:t%d", w.ID, w.nextTab), Label: flagVal(a, "--label"), Number: w.nextTab,
				Pane: fmt.Sprintf("%s:p%d", w.ID, w.nextPane)}
			w.nextTab++
			w.nextPane++
			w.Tabs = append(w.Tabs, t)
			return ok(f.createJSON(*w, t))
		}
		return fail("workspace_not_found", "workspace not found")
	case "tab close":
		wi, ti := f.findTab(a[2])
		if wi < 0 {
			return fail("tab_not_found", "tab "+a[2]+" not found")
		}
		w := &f.workspace[wi]
		w.Tabs = append(w.Tabs[:ti], w.Tabs[ti+1:]...)
		if len(w.Tabs) == 0 { // herdr closes a workspace with its last tab
			f.workspace = append(f.workspace[:wi], f.workspace[wi+1:]...)
		}
		return ok(map[string]any{"type": "ok"})
	case "tab rename":
		wi, ti := f.findTab(a[2])
		if wi < 0 {
			return fail("tab_not_found", "tab "+a[2]+" not found")
		}
		f.workspace[wi].Tabs[ti].Label = strings.Join(a[3:], " ")
		return ok(map[string]any{})
	case "tab focus":
		wi, ti := f.findTab(a[2])
		if wi < 0 {
			return fail("tab_not_found", "tab "+a[2]+" not found")
		}
		for j := range f.workspace[wi].Tabs {
			f.workspace[wi].Tabs[j].Focused = j == ti
		}
		return ok(map[string]any{})
	case "worktree open":
		path := flagVal(a, "--path")
		if !f.worktrees[path] {
			return fail("worktree_not_found", "worktree path not found")
		}
		for _, w := range f.workspace {
			if w.Worktree == path {
				r := f.createJSON(w, w.Tabs[0])
				r["already_open"] = true
				return ok(r)
			}
		}
		w := f.newWS(flagVal(a, "--label"))
		w.Worktree = path
		r := f.createJSON(*w, w.Tabs[0])
		r["already_open"] = false
		return ok(r)
	case "pane list":
		var l []map[string]any
		for _, w := range f.workspace {
			if len(a) > 3 && a[3] != w.ID {
				continue
			}
			for _, t := range w.Tabs {
				l = append(l, map[string]any{"pane_id": t.Pane, "workspace_id": w.ID, "tab_id": t.ID})
			}
		}
		return ok(map[string]any{"panes": l})
	case "pane get":
		for _, w := range f.workspace {
			for _, t := range w.Tabs {
				if t.Pane == a[2] {
					return ok(map[string]any{"pane": map[string]any{"pane_id": t.Pane, "workspace_id": w.ID}})
				}
			}
		}
		return fail("pane_not_found", "pane "+a[2]+" not found")
	case "pane process-info":
		return ok(map[string]any{"process_info": map[string]any{"foreground_processes": f.procs[a[3]], "shell_pid": 1}})
	case "pane read":
		return []byte(f.reads[strings.Join(a[2:], " ")]), nil, 0, nil
	case "pane run":
		if strings.Contains(a[3], "mux-supervise") {
			// Simulate the supervisor coming up.
			var name string
			fields := strings.Fields(a[3])
			for i, x := range fields {
				if x == "--session" {
					name = strings.Trim(fields[i+1], "'")
				}
			}
			_ = f.store.Update(name, false, func(st *SessionState) error {
				st.PanePID, st.SupervisorPID = 5000, f.supPID
				return nil
			})
		}
		return ok(map[string]any{})
	case "pane send-text", "pane send-keys":
		return ok(map[string]any{})
	}
	f.t.Errorf("fake herdr: unhandled %v", a)
	return fail("unhandled", strings.Join(a, " "))
}

func ok2(v any) ([]byte, []byte, int, error) {
	b, _ := json.Marshal(v)
	return b, nil, 0, nil
}

// herdrCalls returns recorded session calls whose first words start with prefix.
func (f *fakeHerdr) herdrCalls(prefix string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 2 && c[0] == "--session" && strings.HasPrefix(strings.Join(c[2:], " "), prefix) {
			out = append(out, c[2:])
		}
	}
	return out
}

var _ = syscall.SIGUSR1

func (f *fakeHerdr) failTabs() bool { return f.failTabList }
