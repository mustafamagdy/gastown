package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// skipIfHerdr returns an OK "skipped" result for tmux-only checks when the
// herdr backend is active, or nil when the check should run normally.
func skipIfHerdr(name string) *CheckResult {
	if !tmux.IsHerdr() {
		return nil
	}
	return &CheckResult{
		Name:    name,
		Status:  StatusOK,
		Message: "skipped (herdr backend)",
	}
}

// HerdrCheck verifies the herdr installation when the herdr backend is active.
type HerdrCheck struct {
	BaseCheck
	// runHerdr runs the herdr binary; nil uses the real binary. For tests.
	runHerdr func(bin string, args ...string) ([]byte, error)
	// lookPath resolves the binary; nil uses exec.LookPath. For tests.
	lookPath func(file string) (string, error)
}

// NewHerdrCheck creates the herdr backend check.
func NewHerdrCheck() *HerdrCheck {
	return &HerdrCheck{
		BaseCheck: BaseCheck{
			CheckName:        "herdr",
			CheckDescription: "Verify herdr is installed and the town herdr session state (herdr backend only)",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// herdrBinary returns the herdr binary name (GT_HERDR_BIN overrides).
func herdrBinary() string {
	if b := os.Getenv("GT_HERDR_BIN"); b != "" {
		return b
	}
	return "herdr"
}

func defaultRunHerdr(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Never let an inherited herdr env redirect us to the user's own session.
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HERDR_") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env
	return cmd.Output()
}

// Run implements Check.
func (c *HerdrCheck) Run(ctx *CheckContext) *CheckResult {
	if !tmux.IsHerdr() {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: "skipped (tmux backend)"}
	}
	lookPath := c.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	run := c.runHerdr
	if run == nil {
		run = defaultRunHerdr
	}
	bin := herdrBinary()
	path, err := lookPath(bin)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("herdr binary %q not found", bin),
			Details: []string{err.Error()},
			FixHint: "Install herdr (https://herdr.dev), or set GT_HERDR_BIN to its path, or set multiplexer to \"tmux\"",
		}
	}
	verOut, err := run(path, "--version")
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "herdr --version failed",
			Details: []string{err.Error()},
		}
	}
	version := strings.TrimSpace(string(verOut))

	listOut, err := run(path, "session", "list", "--json")
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "herdr session list --json failed",
			Details: []string{version, err.Error()},
		}
	}
	sock := tmux.GetDefaultSocket()
	running, found, perr := townSessionRunning(listOut, sock)
	if perr != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "could not parse herdr session list output",
			Details: []string{version, perr.Error()},
		}
	}
	switch {
	case sock == "":
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: version + " (no town socket configured)"}
	case running:
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: fmt.Sprintf("%s; session %q running", version, sock)}
	default:
		state := "not running"
		if !found {
			state = "not created yet"
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%s; session %q %s", version, sock, state),
			FixHint: "Run 'gt up' to start the town",
		}
	}
}

// townSessionRunning parses `herdr session list --json` output and reports
// whether the named session exists and is running.
func townSessionRunning(data []byte, name string) (running, found bool, err error) {
	var out struct {
		Sessions []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return false, false, err
	}
	for _, s := range out.Sessions {
		if s.Name == name {
			return s.Running, true, nil
		}
	}
	return false, false, nil
}
