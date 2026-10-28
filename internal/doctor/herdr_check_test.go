package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

func setHerdrForTest(t *testing.T, sock string) {
	t.Helper()
	prevB, prevS := tmux.GetBackend(), tmux.GetDefaultSocket()
	tmux.SetBackend(tmux.BackendHerdr)
	tmux.SetDefaultSocket(sock)
	t.Cleanup(func() { tmux.SetBackend(prevB); tmux.SetDefaultSocket(prevS) })
}

func TestTmuxOnlyChecksSkippedUnderHerdr(t *testing.T) {
	setHerdrForTest(t, "gt-x")
	checks := []Check{NewTmuxGlobalEnvCheck(), NewLinkedPaneCheck(), NewSocketSplitBrainCheck(), NewThemeCheck()}
	for _, c := range checks {
		r := c.Run(&CheckContext{})
		if r.Status != StatusOK || r.Message != "skipped (herdr backend)" {
			t.Errorf("%s: %v %q", c.Name(), r.Status, r.Message)
		}
	}
}

func TestHerdrCheck(t *testing.T) {
	sessions := `{"sessions":[{"name":"default","running":true},{"name":"gt-x","running":true}]}`
	mk := func(listOut string, listErr error) *HerdrCheck {
		c := NewHerdrCheck()
		c.lookPath = func(string) (string, error) { return "/fake/herdr", nil }
		c.runHerdr = func(bin string, args ...string) ([]byte, error) {
			if args[0] == "--version" {
				return []byte("herdr 0.9.3\n"), nil
			}
			return []byte(listOut), listErr
		}
		return c
	}

	// tmux backend: skipped.
	if r := mk(sessions, nil).Run(&CheckContext{}); r.Status != StatusOK || !strings.Contains(r.Message, "skipped") {
		t.Errorf("tmux mode: %+v", r)
	}

	setHerdrForTest(t, "gt-x")
	if r := mk(sessions, nil).Run(&CheckContext{}); r.Status != StatusOK || !strings.Contains(r.Message, "running") {
		t.Errorf("running: %+v", r)
	}
	if r := mk(`{"sessions":[{"name":"default","running":true}]}`, nil).Run(&CheckContext{}); r.Status != StatusWarning {
		t.Errorf("not running should warn: %+v", r)
	}
	if r := mk("", errors.New("boom")).Run(&CheckContext{}); r.Status != StatusError {
		t.Errorf("list failure should error: %+v", r)
	}
	c := mk(sessions, nil)
	c.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if r := c.Run(&CheckContext{}); r.Status != StatusError {
		t.Errorf("missing binary should error: %+v", r)
	}
}
