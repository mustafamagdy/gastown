package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Backend identifies the terminal multiplexer implementation Gas Town drives.
type Backend string

const (
	// BackendTmux is the default: real tmux.
	BackendTmux Backend = "tmux"
	// BackendHerdr runs sessions under herdr via the `gt mux-tmux` emulator.
	BackendHerdr Backend = "herdr"
)

// EnvMultiplexer is the environment variable that selects the backend.
const EnvMultiplexer = "GT_MULTIPLEXER"

// EnvGTBin overrides the gt executable used for the herdr backend (tests).
const EnvGTBin = "GT_BIN"

var (
	backend   = BackendTmux
	backendMu sync.RWMutex
)

// ParseBackend parses a backend name. Empty means tmux.
func ParseBackend(s string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "tmux":
		return BackendTmux, nil
	case "herdr":
		return BackendHerdr, nil
	}
	return "", fmt.Errorf("unknown multiplexer %q (valid: tmux, herdr)", s)
}

// SetBackend sets the package-level multiplexer backend.
func SetBackend(b Backend) {
	backendMu.Lock()
	backend = b
	backendMu.Unlock()
}

// GetBackend returns the current multiplexer backend.
func GetBackend() Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backend
}

// IsHerdr reports whether the herdr backend is active.
func IsHerdr() bool { return GetBackend() == BackendHerdr }

// gtExecutable returns the gt binary used to run the herdr emulator.
func gtExecutable() (string, error) {
	if p := os.Getenv(EnvGTBin); p != "" {
		return p, nil
	}
	return os.Executable()
}

// ExecArgv returns the binary path and full argv (argv[0] included) to run
// "tmux <args>" under the active backend. Intended for syscall.Exec callers.
// Args are used as given (no -u / -L injection).
func ExecArgv(args ...string) (path string, argv []string, err error) {
	if IsHerdr() {
		gt, err := gtExecutable()
		if err != nil {
			return "", nil, fmt.Errorf("locating gt executable: %w", err)
		}
		return gt, append([]string{gt, "mux-tmux"}, args...), nil
	}
	path, err = exec.LookPath("tmux")
	if err != nil {
		return "", nil, err
	}
	return path, append([]string{"tmux"}, args...), nil
}

// Program returns the program and leading args used to invoke the
// multiplexer CLI: ("tmux", nil) or (gt, ["mux-tmux"]).
func Program() (string, []string) {
	if IsHerdr() {
		if gt, err := gtExecutable(); err == nil {
			return gt, []string{"mux-tmux"}
		}
		return "gt", []string{"mux-tmux"}
	}
	return "tmux", nil
}

// newCmdContext is exec.CommandContext for the multiplexer CLI under the
// active backend. allArgs are the tmux argv (without the program name).
func newCmdContext(ctx context.Context, allArgs []string) *exec.Cmd {
	prog, pre := Program()
	return exec.CommandContext(ctx, prog, append(pre, allArgs...)...)
}

// CommandWithArgs builds an exec.Cmd for the multiplexer CLI with args used
// verbatim (no -u / -L injection). Use for callers that deliberately target
// a specific socket (-S) or the ambient $TMUX server.
func CommandWithArgs(args ...string) *exec.Cmd {
	cmd := newCmdContext(context.Background(), args)
	hideConsoleWindow(cmd)
	return cmd
}
