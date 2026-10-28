package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/session"
)

// MultiplexerOrphansCheck warns when sessions are still running on the
// inactive multiplexer backend (e.g. after switching tmux -> herdr).
type MultiplexerOrphansCheck struct {
	BaseCheck
	// list returns stray sessions for a socket; nil uses the real lookup.
	list func(sock string) *session.StraySessions
	// socket resolves the town socket; nil uses session.ResolveTownSocket.
	socket func(townRoot string) string
}

// NewMultiplexerOrphansCheck creates the check.
func NewMultiplexerOrphansCheck() *MultiplexerOrphansCheck {
	return &MultiplexerOrphansCheck{
		BaseCheck: BaseCheck{
			CheckName:        "multiplexer-orphans",
			CheckDescription: "Detect sessions left running on the inactive multiplexer backend",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run implements Check.
func (c *MultiplexerOrphansCheck) Run(ctx *CheckContext) *CheckResult {
	list := c.list
	if list == nil {
		list = session.ListInactiveBackendSessions
	}
	socket := c.socket
	if socket == nil {
		socket = session.ResolveTownSocket
	}
	s := list(socket(ctx.TownRoot))
	if s == nil || len(s.Sessions) == 0 {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "no sessions on the inactive multiplexer backend"}
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d session(s) running on the inactive %s backend", len(s.Sessions), s.Backend),
		Details: append([]string(nil), s.Sessions...),
		FixHint: fmt.Sprintf("Stop them with: %s", s.StopCmd),
	}
}
