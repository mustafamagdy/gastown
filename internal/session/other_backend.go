package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/herdrmux"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TownSocketName returns the per-town tmux socket name derived from the town
// path (used when GT_TMUX_SOCKET is unset).
func TownSocketName(townRoot string) string { return townSocketName(townRoot) }

// ResolveTownSocket returns the tmux socket name for the town:
//
//	GT_TMUX_SOCKET unset / "default" / "auto" -> derived from the town path
//	any other value                           -> that value as-is
//
// InitRegistry uses this so the two cannot drift.
func ResolveTownSocket(townRoot string) string {
	switch s := os.Getenv("GT_TMUX_SOCKET"); s {
	case "", "default", "auto":
		return townSocketName(townRoot)
	default:
		return s
	}
}

// StraySessions describes sessions still alive on the inactive multiplexer
// backend after the town's backend setting was switched.
type StraySessions struct {
	Backend  tmux.Backend // the inactive backend the sessions live on
	Socket   string
	Sessions []string
	StopCmd  string // exact command that stops them
}

// Injection points for tests.
var (
	otherLookPath = exec.LookPath
	// otherRunTmux runs the real tmux binary and returns stdout.
	otherRunTmux = func(bin string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out bytes.Buffer
		c := exec.CommandContext(ctx, bin, args...)
		c.Stdout = &out
		if err := c.Run(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	// otherRunHerdrmux runs the in-process emulator and returns stdout.
	otherRunHerdrmux = func(argv []string) ([]byte, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out, errb bytes.Buffer
		code := herdrmux.Run(ctx, argv, nil, &out, &errb)
		return out.Bytes(), code
	}
	otherStateRoot = herdrmux.StateRoot
)

// ListInactiveBackendSessions lists live sessions for the town socket on the
// backend that is NOT currently active. It never starts a herdr server.
// Errors and "nothing there" both yield nil.
func ListInactiveBackendSessions(sock string) *StraySessions {
	if sock == "" {
		return nil
	}
	if tmux.IsHerdr() {
		bin, err := otherLookPath("tmux")
		if err != nil {
			return nil
		}
		out, err := otherRunTmux(bin, "-L", sock, "list-sessions", "-F", "#{session_name}")
		if err != nil { // includes "no server running"
			return nil
		}
		names := splitLines(string(out))
		if len(names) == 0 {
			return nil
		}
		return &StraySessions{
			Backend: tmux.BackendTmux, Socket: sock, Sessions: names,
			StopCmd: fmt.Sprintf("tmux -L %s kill-server", sock),
		}
	}
	// tmux active: only ask the emulator if herdr exists and state is present.
	if _, err := otherLookPath("herdr"); err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(otherStateRoot(), sock, "sessions"))
	if err != nil || len(entries) == 0 {
		return nil
	}
	out, code := otherRunHerdrmux([]string{"-L", sock, "list-sessions", "-F", "#{session_name}"})
	if code != 0 {
		return nil
	}
	names := splitLines(string(out))
	if len(names) == 0 {
		return nil
	}
	return &StraySessions{
		Backend: tmux.BackendHerdr, Socket: sock, Sessions: names,
		StopCmd: "GT_MULTIPLEXER=herdr gt down",
	}
}

func splitLines(s string) []string {
	var r []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			r = append(r, l)
		}
	}
	return r
}

// Describe returns a one-line human summary.
func (s *StraySessions) Describe() string {
	return fmt.Sprintf("%d session(s) still running on the inactive %s backend (socket %s): %s",
		len(s.Sessions), s.Backend, s.Socket, strings.Join(s.Sessions, ", "))
}
