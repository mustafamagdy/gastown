package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/herdrmux"
	"github.com/steveyegge/gastown/internal/tmux"
)

// resolveMultiplexer picks the backend: GT_MULTIPLEXER env > town settings
// "multiplexer" > tmux. A missing or unreadable settings file means tmux.
// An invalid value is reported as an error and falls back to tmux.
func resolveMultiplexer(townRoot string) (tmux.Backend, error) {
	if v := strings.TrimSpace(os.Getenv(tmux.EnvMultiplexer)); v != "" {
		b, err := tmux.ParseBackend(v)
		if err != nil {
			return tmux.BackendTmux, fmt.Errorf("%s: %w", tmux.EnvMultiplexer, err)
		}
		return b, nil
	}
	data, err := os.ReadFile(config.TownSettingsPath(townRoot)) //nolint:gosec // G304: internal path
	if err != nil {
		return tmux.BackendTmux, nil
	}
	var s struct {
		Multiplexer string `json:"multiplexer"`
	}
	if json.Unmarshal(data, &s) != nil {
		return tmux.BackendTmux, nil
	}
	b, err := tmux.ParseBackend(s.Multiplexer)
	if err != nil {
		return tmux.BackendTmux, fmt.Errorf("settings/config.json multiplexer: %w", err)
	}
	return b, nil
}

// initMultiplexer resolves and activates the multiplexer backend, exports it
// to the environment for child processes, and installs the tmux shim on PATH
// when running under herdr.
func initMultiplexer(townRoot string) error {
	b, err := resolveMultiplexer(townRoot)
	tmux.SetBackend(b)
	_ = os.Setenv(tmux.EnvMultiplexer, string(b))
	if b != tmux.BackendHerdr {
		return err
	}
	dir, shimErr := herdrmux.EnsureShim()
	if shimErr != nil {
		return fmt.Errorf("herdr tmux shim: %w", joinErr(err, shimErr))
	}
	prependPath(dir)
	return err
}

func joinErr(a, b error) error {
	if a == nil {
		return b
	}
	return fmt.Errorf("%v; %w", a, b)
}

// prependPath puts dir at the front of PATH unless it is already there.
func prependPath(dir string) {
	if dir == "" {
		return
	}
	cur := os.Getenv("PATH")
	for _, p := range filepath.SplitList(cur) {
		if p == dir {
			return
		}
	}
	if cur == "" {
		_ = os.Setenv("PATH", dir)
		return
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+cur)
}
