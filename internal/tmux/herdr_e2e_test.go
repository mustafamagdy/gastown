//go:build !windows

package tmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Herdr end-to-end tests: drive the real *Tmux API against a real herdr server
// via `gt mux-tmux`. Enable with GT_HERDR_IT=1 and GT_BIN=<gt binary>.

const fakeAgentScript = `#!/bin/bash
log="$1"
echo "START pid=$$ FOO=$GT_E2E_FOO BAR=$GT_E2E_BAR" >> "$log"
while true; do
  printf '> '
  IFS= read -r line || exit 0
  if [ "$line" = quit ]; then exit 3; fi
  echo "GOT:$line" >> "$log"
  echo "echo-out:$line"
done
`

type herdrE2E struct {
	t    *testing.T
	tm   *Tmux
	sock string
	dir  string
}

func herdrCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("herdr", args...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HERDR_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	return cmd
}

func newHerdrE2E(t *testing.T) *herdrE2E {
	t.Helper()
	if os.Getenv("GT_HERDR_IT") != "1" {
		t.Skip("set GT_HERDR_IT=1 to run")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not on PATH")
	}
	gt := os.Getenv("GT_BIN")
	if st, err := os.Stat(gt); gt == "" || err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		t.Skip("GT_BIN must point to an executable gt binary")
	}
	sock := fmt.Sprintf("gt-e2e-%08x", rand.Uint32())
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") {
			k, _, _ := strings.Cut(kv, "=")
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	prev := GetBackend()
	SetBackend(BackendHerdr)
	t.Cleanup(func() {
		SetBackend(prev)
		_ = herdrCmd("session", "stop", sock, "--json").Run()
		_ = herdrCmd("session", "delete", sock, "--json").Run()
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.sh"), []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return &herdrE2E{t: t, tm: NewTmuxWithSocket(sock), sock: sock, dir: dir}
}

func (h *herdrE2E) agentCmd(log string) string {
	return fmt.Sprintf("exec bash %s %s", filepath.Join(h.dir, "agent.sh"), log)
}

// start creates a session running the fake agent and returns its log path.
func (h *herdrE2E) start(name string, env map[string]string) string {
	h.t.Helper()
	return h.startIn(name, h.dir, env)
}

// startIn is start with an explicit working directory.
func (h *herdrE2E) startIn(name, cwd string, env map[string]string) string {
	h.t.Helper()
	log := filepath.Join(h.dir, name+".log")
	if err := h.tm.NewSessionWithCommandAndEnv(name, cwd, h.agentCmd(log), env); err != nil {
		h.t.Fatalf("NewSessionWithCommandAndEnv(%s): %v", name, err)
	}
	h.t.Cleanup(func() { _ = h.tm.KillSession(name) })
	h.waitLog(log, "START", 1)
	return log
}

func (h *herdrE2E) readLog(log string) string {
	b, _ := os.ReadFile(log)
	return string(b)
}

// waitLog waits until the log contains substr at least n times.
func (h *herdrE2E) waitLog(log, substr string, n int) string {
	h.t.Helper()
	var s string
	if !poll(10*time.Second, func() bool {
		s = h.readLog(log)
		return strings.Count(s, substr) >= n
	}) {
		h.t.Fatalf("timeout waiting for %d x %q in log; got:\n%s", n, substr, s)
	}
	return s
}

func poll(d time.Duration, f func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return f()
}

func (h *herdrE2E) startPIDs(log string) []int {
	var pids []int
	for _, l := range strings.Split(h.readLog(log), "\n") {
		if strings.HasPrefix(l, "START pid=") {
			f := strings.Fields(l)[1]
			p, _ := strconv.Atoi(strings.TrimPrefix(f, "pid="))
			pids = append(pids, p)
		}
	}
	return pids
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// zombies count as dead
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		if i := strings.LastIndex(string(b), ") "); i >= 0 && len(b) > i+2 && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

func (h *herdrE2E) paneDead(name string) string {
	out, err := h.tm.run("display-message", "-p", "-t", name, "#{pane_dead}")
	if err != nil {
		return "err:" + err.Error()
	}
	return strings.TrimSpace(out)
}

func TestHerdrE2E(t *testing.T) {
	h := newHerdrE2E(t)
	tm := h.tm

	// 10 first: nothing running yet.
	t.Run("EmptyServer", func(t *testing.T) {
		h.t = t
		names, err := tm.ListSessions()
		if err != nil || len(names) != 0 {
			t.Fatalf("ListSessions on empty socket = %v, %v; want empty, nil", names, err)
		}
		ok, err := tm.HasSession("nope")
		if err != nil || ok {
			t.Fatalf("HasSession(nope) = %v, %v", ok, err)
		}
		set, err := tm.GetSessionSet()
		if err != nil {
			t.Fatalf("GetSessionSet: %v", err)
		}
		if set.Has("nope") {
			t.Fatal("empty set has nope")
		}
	})

	t.Run("Lifecycle", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-life", map[string]string{"GT_E2E_FOO": "foo-val", "GT_E2E_BAR": "bar val"})
		if ok, err := tm.HasSession("e2e-life"); err != nil || !ok {
			t.Fatalf("HasSession = %v, %v", ok, err)
		}
		if ok, err := tm.HasSession("e2e-unknown"); err != nil || ok {
			t.Fatalf("HasSession(unknown) = %v, %v", ok, err)
		}
		names, err := tm.ListSessions()
		if err != nil || !contains(names, "e2e-life") {
			t.Fatalf("ListSessions = %v, %v", names, err)
		}
		set, err := tm.GetSessionSet()
		if err != nil || !set.Has("e2e-life") {
			t.Fatalf("GetSessionSet err=%v", err)
		}
		if err := tm.NewSessionWithCommandAndEnv("e2e-life", h.dir, "sleep 5", nil); !errors.Is(err, ErrSessionExists) {
			t.Fatalf("duplicate = %v, want ErrSessionExists", err)
		}
		if err := tm.NewSession("e2e-life", h.dir); !errors.Is(err, ErrSessionExists) {
			t.Fatalf("duplicate NewSession = %v, want ErrSessionExists", err)
		}
		// env visible in child (scenario 2)
		s := h.readLog(log)
		if !strings.Contains(s, "FOO=foo-val") || !strings.Contains(s, "BAR=bar val") {
			t.Fatalf("creation env not visible in child: %q", s)
		}
	})

	t.Run("Env", func(t *testing.T) {
		h.t = t
		h.start("e2e-env", map[string]string{"GT_E2E_FOO": "x"})
		if err := tm.SetEnvironment("e2e-env", "GT_E2E_K", "v1 with space"); err != nil {
			t.Fatal(err)
		}
		if v, err := tm.GetEnvironment("e2e-env", "GT_E2E_K"); err != nil || v != "v1 with space" {
			t.Fatalf("GetEnvironment = %q, %v", v, err)
		}
		if v, err := tm.GetEnvironment("e2e-env", "GT_E2E_FOO"); err != nil || v != "x" {
			t.Fatalf("creation env via GetEnvironment = %q, %v", v, err)
		}
		all, err := tm.GetAllEnvironment("e2e-env")
		if err != nil || all["GT_E2E_K"] != "v1 with space" || all["GT_E2E_FOO"] != "x" {
			t.Fatalf("GetAllEnvironment = %v, %v", all, err)
		}
		if _, err := tm.GetEnvironment("e2e-env", "GT_E2E_MISSING"); err == nil {
			t.Fatal("missing var should error")
		} else if !isMissingEnvironmentError(err, "GT_E2E_MISSING") {
			t.Fatalf("missing var error not recognised as missing: %v", err)
		}
	})

	t.Run("Introspection", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-intro", nil)
		pids := h.startPIDs(log)
		pid, err := tm.GetPanePID("e2e-intro")
		if err != nil || pid != strconv.Itoa(pids[0]) {
			t.Fatalf("GetPanePID = %q, %v; agent pid %v", pid, err, pids)
		}
		id, err := tm.GetPaneID("e2e-intro")
		if err != nil || !strings.HasPrefix(id, "%") {
			t.Fatalf("GetPaneID = %q, %v", id, err)
		}
		if p2, err := tm.GetPanePID(id); err != nil || p2 != pid {
			t.Fatalf("GetPanePID(%s) = %q, %v", id, p2, err)
		}
		var cmd string
		if !poll(5*time.Second, func() bool { cmd, err = tm.GetPaneCommand("e2e-intro"); return err == nil && cmd == "bash" }) {
			t.Fatalf("GetPaneCommand = %q, %v; want bash", cmd, err)
		}
		wd, err := tm.GetPaneWorkDir("e2e-intro")
		if r, _ := filepath.EvalSymlinks(h.dir); err != nil || (wd != h.dir && wd != r) {
			t.Fatalf("GetPaneWorkDir = %q, %v; want %q", wd, err, h.dir)
		}
		info, err := tm.GetSessionInfo("e2e-intro")
		if err != nil || info.Name != "e2e-intro" || info.Windows != 1 || info.Created == "" {
			t.Fatalf("GetSessionInfo = %+v, %v", info, err)
		}
		act, err := tm.GetSessionActivity("e2e-intro")
		if err != nil || time.Since(act) > time.Minute || act.Before(time.Now().Add(-time.Minute)) {
			t.Fatalf("GetSessionActivity = %v, %v", act, err)
		}
		if !tm.IsRuntimeRunning("e2e-intro", []string{"bash"}) {
			t.Fatal("IsRuntimeRunning bash = false")
		}
		if tm.IsRuntimeRunning("e2e-intro", []string{"definitely-not-a-proc"}) {
			t.Fatal("IsRuntimeRunning bogus = true")
		}
		_ = tm.SetEnvironment("e2e-intro", "GT_PROCESS_NAMES", "bash")
		if alive, err := tm.IsAgentAliveChecked("e2e-intro"); err != nil || !alive {
			t.Fatalf("IsAgentAlive = %v, %v", alive, err)
		}
		if st := tm.CheckSessionHealth("e2e-intro", time.Minute); st != SessionHealthy {
			t.Fatalf("CheckSessionHealth = %v", st)
		}
		if st := tm.CheckSessionHealth("e2e-ghost", 0); st != SessionDead {
			t.Fatalf("CheckSessionHealth(ghost) = %v", st)
		}
		if err := tm.WaitForCommand("e2e-intro", []string{"zsh", "sh"}, 5*time.Second); err != nil {
			t.Fatalf("WaitForCommand: %v", err)
		}
	})

	t.Run("InputOutput", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-io", nil)
		if err := tm.SendKeys("e2e-io", "hello world"); err != nil {
			t.Fatal(err)
		}
		h.waitLog(log, "GOT:hello world", 1)
		if err := tm.NudgeSession("e2e-io", "nudged message"); err != nil {
			t.Fatal(err)
		}
		h.waitLog(log, "GOT:nudged message", 1)
		var out string
		if !poll(5*time.Second, func() bool {
			var err error
			out, err = tm.CapturePane("e2e-io", 50)
			return err == nil && strings.Contains(out, "echo-out:nudged message") && strings.Contains(out, ">")
		}) {
			t.Fatalf("CapturePane missing output:\n%s", out)
		}
		lines, err := tm.CapturePaneLines("e2e-io", 50)
		if err != nil || !strings.Contains(strings.Join(lines, "\n"), "echo-out:hello world") {
			t.Fatalf("CapturePaneLines = %v, %v", lines, err)
		}
		if all, err := tm.CapturePaneAll("e2e-io"); err != nil || !strings.Contains(all, "echo-out:hello world") {
			t.Fatalf("CapturePaneAll = %q, %v", all, err)
		}
	})

	t.Run("CrashAndAutoRespawn", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-crash", nil)
		if err := tm.SetRemainOnExit("e2e-crash", true); err != nil {
			t.Fatal(err)
		}
		if err := tm.SetAutoRespawnHook("e2e-crash"); err != nil {
			t.Fatal(err)
		}
		if got := h.paneDead("e2e-crash"); got != "0" {
			t.Fatalf("pane_dead before quit = %q", got)
		}
		if err := tm.SendKeys("e2e-crash", "quit"); err != nil {
			t.Fatal(err)
		}
		// Hook sleeps 3s before respawning, so pane_dead=1 should be observable.
		if !poll(3*time.Second, func() bool { return h.paneDead("e2e-crash") == "1" }) {
			t.Errorf("pane_dead never became 1 (last %q)", h.paneDead("e2e-crash"))
		}
		if st, _ := tm.run("display-message", "-p", "-t", "e2e-crash", "#{pane_dead_status}"); strings.TrimSpace(st) != "3" {
			t.Errorf("pane_dead_status = %q, want 3", st)
		}
		h.waitLog(log, "START", 2)
		pids := h.startPIDs(log)
		if len(pids) < 2 || pids[0] == pids[1] {
			t.Fatalf("expected new pid after respawn: %v", pids)
		}
		if !poll(5*time.Second, func() bool { return h.paneDead("e2e-crash") == "0" }) {
			t.Fatalf("pane_dead after respawn = %q", h.paneDead("e2e-crash"))
		}
		if pid, _ := tm.GetPanePID("e2e-crash"); pid != strconv.Itoa(pids[1]) {
			t.Errorf("pane pid after respawn = %q, want %d", pid, pids[1])
		}
		// remain-on-exit is re-enabled by the hook; agent still usable.
		if err := tm.SendKeys("e2e-crash", "after-respawn"); err != nil {
			t.Fatal(err)
		}
		h.waitLog(log, "GOT:after-respawn", 1)
	})

	t.Run("CustomPaneDiedHook", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-hook", nil)
		out := filepath.Join(h.dir, "hook.out")
		if err := tm.SetRemainOnExit("e2e-hook", true); err != nil {
			t.Fatal(err)
		}
		hook := fmt.Sprintf(`run-shell "echo #{pane_dead_status} > %s"`, out)
		if _, err := tm.run("set-hook", "-t", "e2e-hook", "pane-died", hook); err != nil {
			t.Fatal(err)
		}
		_ = tm.SendKeys("e2e-hook", "quit")
		var got string
		if !poll(10*time.Second, func() bool {
			b, _ := os.ReadFile(out)
			got = strings.TrimSpace(string(b))
			return got != ""
		}) {
			t.Fatalf("pane-died hook did not run; log:\n%s", h.readLog(log))
		}
		if got != "3" {
			t.Errorf("hook saw pane_dead_status=%q, want 3", got)
		}
		if h.paneDead("e2e-hook") != "1" {
			t.Errorf("pane_dead = %q, want 1", h.paneDead("e2e-hook"))
		}
		if st := tm.CheckSessionHealth("e2e-hook", 0); st != AgentDead {
			t.Errorf("CheckSessionHealth on dead pane = %v, want agent-dead", st)
		}
	})

	t.Run("SetPaneDiedHookRuns", func(t *testing.T) {
		h.t = t
		// Real SetPaneDiedHook shells out to `gt log crash` (needs a town);
		// here we only verify it is accepted and stored.
		h.start("e2e-pdh", nil)
		if err := tm.SetPaneDiedHook("e2e-pdh", "gastown/Toast"); err != nil {
			t.Fatalf("SetPaneDiedHook: %v", err)
		}
	})

	t.Run("RespawnPane", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-resp", nil)
		old := h.startPIDs(log)[0]
		id, err := tm.GetPaneID("e2e-resp")
		if err != nil {
			t.Fatal(err)
		}
		if err := tm.RespawnPane(id, "echo respawned-marker; exec sleep 300"); err != nil {
			t.Fatal(err)
		}
		if !poll(10*time.Second, func() bool { return !pidAlive(old) }) {
			t.Errorf("old agent pid %d still alive after respawn -k", old)
		}
		var out string
		if !poll(10*time.Second, func() bool {
			out, _ = tm.CapturePane("e2e-resp", 30)
			return strings.Contains(out, "respawned-marker")
		}) {
			t.Fatalf("respawned output not visible:\n%s", out)
		}
		var pid string
		if !poll(5*time.Second, func() bool {
			pid, _ = tm.GetPanePID("e2e-resp")
			return pid != "" && pid != strconv.Itoa(old)
		}) {
			t.Fatalf("pane pid unchanged after respawn: %s", pid)
		}
		var cmd string
		if !poll(5*time.Second, func() bool { cmd, _ = tm.GetPaneCommand("e2e-resp"); return cmd == "sleep" }) {
			t.Errorf("GetPaneCommand after respawn = %q, want sleep", cmd)
		}
		// With workdir variant.
		if err := tm.RespawnPaneWithWorkDir(id, h.dir, "pwd; exec sleep 300"); err != nil {
			t.Fatal(err)
		}
		if !poll(10*time.Second, func() bool {
			out, _ = tm.CapturePane("e2e-resp", 30)
			return strings.Contains(out, filepath.Base(h.dir))
		}) {
			t.Errorf("workdir respawn output:\n%s", out)
		}
	})

	t.Run("RenameSession", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-ren-old", nil)
		if err := tm.SetRemainOnExit("e2e-ren-old", true); err != nil {
			t.Fatal(err)
		}
		if err := tm.SetEnvironment("e2e-ren-old", "GT_E2E_R", "keep"); err != nil {
			t.Fatal(err)
		}
		if err := tm.RenameSession("e2e-ren-old", "e2e-ren-new"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tm.KillSession("e2e-ren-new") })
		if ok, _ := tm.HasSession("e2e-ren-new"); !ok {
			t.Fatal("new name missing")
		}
		if ok, _ := tm.HasSession("e2e-ren-old"); ok {
			t.Fatal("old name still present")
		}
		if v, err := tm.GetEnvironment("e2e-ren-new", "GT_E2E_R"); err != nil || v != "keep" {
			t.Errorf("env after rename = %q, %v", v, err)
		}
		if err := tm.SendKeys("e2e-ren-new", "after-rename"); err != nil {
			t.Fatal(err)
		}
		h.waitLog(log, "GOT:after-rename", 1)
		_ = tm.SendKeys("e2e-ren-new", "quit")
		if !poll(8*time.Second, func() bool { return h.paneDead("e2e-ren-new") == "1" }) {
			t.Errorf("pane death not recorded after rename; pane_dead=%q", h.paneDead("e2e-ren-new"))
		}
	})

	t.Run("Layout", func(t *testing.T) {
		h.t = t
		town := t.TempDir()
		git := func(dir string, args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-c", "user.email=e2e@example.com", "-c", "user.name=e2e"}, args...)...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		seed := filepath.Join(h.dir, "seed")
		if err := os.MkdirAll(seed, 0o755); err != nil {
			t.Fatal(err)
		}
		git(seed, "init", "-q", "-b", "main")
		git(seed, "commit", "-q", "--allow-empty", "-m", "init")
		if err := os.MkdirAll(filepath.Join(town, "demo"), 0o755); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(town, "demo", ".repo.git")
		git(seed, "clone", "-q", "--bare", seed, repo)
		pcDir := filepath.Join(town, "demo", "polecats-alpha")
		git(repo, "worktree", "add", "-q", pcDir, "-b", "pc-alpha", "main")

		base := map[string]string{"GT_TOWN_ROOT": town}
		with := func(kv ...string) map[string]string {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i]] = kv[i+1]
			}
			return m
		}
		h.start("hq-mayor", with("GT_ROLE", "mayor"))
		h.startIn("to-witness", h.dir, with("GT_ROLE", "demo/witness", "GT_RIG", "demo"))
		h.startIn("to-alpha", pcDir, with("GT_ROLE", "demo/polecats/alpha", "GT_RIG", "demo", "GT_POLECAT", "alpha"))

		type ws struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
			WT    *struct {
				Linked bool `json:"is_linked_worktree"`
			} `json:"worktree"`
		}
		type tab struct {
			ID    string `json:"tab_id"`
			WS    string `json:"workspace_id"`
			Label string `json:"label"`
		}
		listWS := func() map[string]ws {
			out, err := herdrCmd("--session", h.sock, "workspace", "list").Output()
			if err != nil {
				t.Fatalf("workspace list: %v", err)
			}
			var r struct {
				Result struct {
					Workspaces []ws `json:"workspaces"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatal(err)
			}
			m := map[string]ws{}
			for _, w := range r.Result.Workspaces {
				m[w.Label] = w
			}
			return m
		}
		tabLabels := func(wsID string) []string {
			out, err := herdrCmd("--session", h.sock, "tab", "list", "--workspace", wsID).Output()
			if err != nil {
				return nil
			}
			var r struct {
				Result struct {
					Tabs []tab `json:"tabs"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatal(err)
			}
			var l []string
			for _, x := range r.Result.Tabs {
				l = append(l, x.Label)
			}
			return l
		}

		w := listWS()
		if len(w) != 3 {
			t.Fatalf("workspaces = %v, want hq, demo and the polecat worktree", w)
		}
		if got := tabLabels(w["hq"].ID); len(got) != 1 || got[0] != "🎩 mayor" {
			t.Errorf("hq tabs = %v, want [🎩 mayor]", got)
		}
		if got := tabLabels(w["🏗️ demo"].ID); len(got) != 1 || got[0] != "🦉 witness" {
			t.Errorf("demo tabs = %v, want [🦉 witness]", got)
		}
		if a, ok := w["🦨 alpha"]; !ok || a.WT == nil || !a.WT.Linked {
			t.Errorf("polecat workspace = %+v, want a linked worktree workspace labelled 🦨 alpha", a)
		}
		names, err := tm.ListSessions()
		if err != nil || len(names) != 3 {
			t.Errorf("ListSessions = %v, %v", names, err)
		}

		// A second town-level session becomes another tab in the same hq workspace.
		h.start("hq-dog-alpha", with("GT_ROLE", "dog", "GT_DOG_NAME", "alpha"))
		w = listWS()
		if len(w) != 3 {
			t.Errorf("workspaces after dog = %d, want 3", len(w))
		}
		if got := tabLabels(w["hq"].ID); len(got) != 2 || got[1] != "🐕 dog-alpha" {
			t.Errorf("hq tabs = %v, want [🎩 mayor 🐕 dog-alpha]", got)
		}

		// Killing a session closes only its own tab / workspace.
		if err := tm.KillSession("hq-dog-alpha"); err != nil {
			t.Fatal(err)
		}
		if got := tabLabels(listWS()["hq"].ID); len(got) != 1 || got[0] != "🎩 mayor" {
			t.Errorf("hq tabs after dog kill = %v", got)
		}
		if err := tm.KillSession("to-alpha"); err != nil {
			t.Fatal(err)
		}
		w = listWS()
		if _, ok := w["🦨 alpha"]; ok || len(w) != 2 {
			t.Errorf("workspaces after polecat kill = %v", w)
		}
		if _, err := os.Stat(pcDir); err != nil {
			t.Errorf("git worktree must survive session kill: %v", err)
		}
		if err := tm.KillSession("to-witness"); err != nil {
			t.Fatal(err)
		}
		w = listWS()
		if _, ok := w["demo"]; ok || len(w) != 1 {
			t.Errorf("workspaces after witness kill = %v", w)
		}
		if ok, _ := tm.HasSession("hq-mayor"); !ok {
			t.Error("mayor must still exist")
		}
		if err := tm.KillSession("hq-mayor"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("CtrlC", func(t *testing.T) {
		h.t = t
		marker := filepath.Join(h.dir, "ctrlc.out")
		script := fmt.Sprintf(`trap 'echo trapped >> %s' INT; echo ready >> %s; sleep 100; echo sleep-ended >> %s; sleep 60`, marker, marker, marker)
		if err := tm.NewSessionWithCommandAndEnv("e2e-ctrlc", h.dir, "exec bash -c \""+script+"\"", nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tm.KillSession("e2e-ctrlc") })
		if !poll(5*time.Second, func() bool { return strings.Contains(h.readLog(marker), "ready") }) {
			t.Fatal("script never became ready")
		}
		time.Sleep(300 * time.Millisecond) // let sleep start
		if err := tm.SendKeysRaw("e2e-ctrlc", "C-c"); err != nil {
			t.Fatal(err)
		}
		var s string
		if !poll(8*time.Second, func() bool { b, _ := os.ReadFile(marker); s = string(b); return strings.Contains(s, "sleep-ended") }) {
			t.Fatalf("C-c did not interrupt sleep 100; marker=%q", s)
		}
	})

	t.Run("KillSessionWithProcesses", func(t *testing.T) {
		h.t = t
		log := h.start("e2e-kill", nil)
		pid := h.startPIDs(log)[0]
		if err := tm.SetAutoRespawnHook("e2e-kill"); err != nil {
			t.Fatal(err)
		}
		before := workspaceCount(t, h.sock)
		if err := tm.KillSessionWithProcesses("e2e-kill"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := tm.HasSession("e2e-kill"); ok {
			t.Fatal("session still exists")
		}
		if !poll(5*time.Second, func() bool { return !pidAlive(pid) }) {
			t.Fatalf("agent pid %d still alive", pid)
		}
		if after := workspaceCount(t, h.sock); after != before-1 {
			t.Errorf("herdr workspaces %d -> %d, want one fewer", before, after)
		}
		time.Sleep(4 * time.Second) // outlive the 3s respawn hook debounce
		if ok, _ := tm.HasSession("e2e-kill"); ok {
			t.Fatal("session resurrected by respawn hook")
		}
	})
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func workspaceCount(t *testing.T, sock string) int {
	t.Helper()
	out, err := herdrCmd("--session", sock, "workspace", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr workspace list: %v %s", err, out)
	}
	return strings.Count(string(out), `"workspace_id"`)
}
