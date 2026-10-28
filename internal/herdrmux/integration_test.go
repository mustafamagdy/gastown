package herdrmux

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationHerdr drives a real herdr server in a throwaway named
// session. Enable with GT_HERDR_IT=1 (needs herdr and python3 on PATH). The
// pane supervisor is replaced by a tiny script that records a pane pid.
func TestIntegrationHerdr(t *testing.T) {
	if os.Getenv("GT_HERDR_IT") != "1" {
		t.Skip("set GT_HERDR_IT=1 to run")
	}
	for _, b := range []string{"herdr", "python3"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not on PATH", b)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") {
			k, _, _ := strings.Cut(kv, "=")
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	sock := fmt.Sprintf("gt-test-%d", rand.Intn(1<<30))
	state := t.TempDir()
	t.Setenv("GT_HERDR_STATE_DIR", state)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	// Fake `gt mux-supervise --sock S --session N`: records pid, runs the command.
	script := filepath.Join(t.TempDir(), "gt")
	body := `#!/bin/sh
sock=$3; name=$5
python3 - "$GT_HERDR_STATE_DIR/$sock/sessions/$name.json" $$ <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["pane_pid"]=int(sys.argv[2]); d["supervisor_pid"]=int(sys.argv[2])
json.dump(d,open(p,"w"))
PY
exec sh -c "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["command"])' "$GT_HERDR_STATE_DIR/$sock/sessions/$name.json")"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_BIN", script)
	t.Setenv("GT_HERDR_STATE_DIR", state)

	run := func(args ...string) (string, string, int) {
		var o, e bytes.Buffer
		code := Run(context.Background(), append([]string{"-u", "-L", sock}, args...), strings.NewReader(""), &o, &e)
		return o.String(), e.String(), code
	}
	t.Cleanup(func() {
		_ = exec.Command("herdr", "session", "stop", sock, "--json").Run()
		_ = exec.Command("herdr", "session", "delete", sock, "--json").Run()
	})

	if _, e, code := run("new-session", "-d", "-s", "it-one", "-c", "/tmp", "cat"); code != 0 {
		t.Fatalf("new-session: %s", e)
	}
	if out, e, code := run("has-session", "-t", "=it-one"); code != 0 {
		t.Fatalf("has-session: %d %s %s", code, out, e)
	}
	out, _, _ := run("list-sessions", "-F", "#{session_name}")
	if strings.TrimSpace(out) != "it-one" {
		t.Fatalf("list-sessions = %q", out)
	}
	run("send-keys", "-t", "=it-one", "hello-from-gt", "Enter")
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, _ = run("capture-pane", "-p", "-t", "=it-one")
		if strings.Count(out, "hello-from-gt") >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(out, "hello-from-gt") {
		t.Errorf("capture-pane = %q", out)
	}
	if out, _, _ := run("display-message", "-p", "-t", "=it-one", "#{pane_current_command}"); strings.TrimSpace(out) == "" {
		t.Errorf("pane_current_command empty")
	}
	if _, e, code := run("kill-session", "-t", "=it-one"); code != 0 {
		t.Fatalf("kill-session: %s", e)
	}
	if _, _, code := run("has-session", "-t", "=it-one"); code == 0 {
		t.Error("session should be gone")
	}
}
