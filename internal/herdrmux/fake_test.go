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
	supPID    int // SupervisorPID the fake "supervisor" records
	shared    bool // client reports a shared (non-dedicated) herdr session
}

type fakeWS struct {
	ID, Label, Pane string
	Number          int
	Focused         bool
}

func newFake(t *testing.T, sock string) (*fakeHerdr, func(args ...string) (string, string, int)) {
	t.Helper()
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_BIN", "/fake/gt")
	f := &fakeHerdr{t: t, sock: sock, nextWS: 2, reads: map[string]string{}, procs: map[string][]procInfo{}, supPID: 4000}
	f.workspace = []fakeWS{{ID: "w1", Label: "~", Pane: "w1:p1", Number: 1, Focused: true}}
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
	return map[string]any{"workspace_id": w.ID, "label": w.Label, "number": w.Number, "focused": w.Focused, "pane_count": 1}
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
		w := fakeWS{ID: fmt.Sprintf("w%d", f.nextWS), Number: f.nextWS}
		w.Pane = w.ID + ":p1"
		f.nextWS++
		for i := 2; i < len(a); i++ {
			if a[i] == "--label" {
				w.Label = a[i+1]
			}
		}
		f.workspace = append(f.workspace, w)
		return ok(map[string]any{"workspace": f.wsJSON(w), "root_pane": map[string]any{"pane_id": w.Pane}})
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
	case "pane list":
		var l []map[string]any
		for _, w := range f.workspace {
			if len(a) > 3 && a[3] != w.ID {
				continue
			}
			l = append(l, map[string]any{"pane_id": w.Pane, "workspace_id": w.ID})
		}
		return ok(map[string]any{"panes": l})
	case "pane get":
		for _, w := range f.workspace {
			if w.Pane == a[2] {
				return ok(map[string]any{"pane": map[string]any{"pane_id": w.Pane, "workspace_id": w.ID}})
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
