package doctor

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestMultiplexerOrphansCheck(t *testing.T) {
	c := NewMultiplexerOrphansCheck()
	c.socket = func(string) string { return "gt-abc" }

	c.list = func(string) *session.StraySessions { return nil }
	if r := c.Run(&CheckContext{TownRoot: "/t"}); r.Status != StatusOK {
		t.Errorf("want OK, got %v", r.Status)
	}

	c.list = func(sock string) *session.StraySessions {
		return &session.StraySessions{Backend: tmux.BackendTmux, Socket: sock,
			Sessions: []string{"hq-mayor", "hq-deacon"}, StopCmd: "tmux -L gt-abc kill-server"}
	}
	r := c.Run(&CheckContext{TownRoot: "/t"})
	if r.Status != StatusWarning || len(r.Details) != 2 || !strings.Contains(r.FixHint, "tmux -L gt-abc kill-server") {
		t.Errorf("unexpected result %+v", r)
	}
}
