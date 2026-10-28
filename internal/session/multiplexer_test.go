package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

func writeSettings(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveMultiplexer(t *testing.T) {
	root := t.TempDir()

	t.Setenv(tmux.EnvMultiplexer, "")
	if b, err := resolveMultiplexer(root); b != tmux.BackendTmux || err != nil {
		t.Errorf("missing settings: %v %v", b, err)
	}

	writeSettings(t, root, `{"type":"town-settings","version":1,"multiplexer":"herdr"}`)
	if b, err := resolveMultiplexer(root); b != tmux.BackendHerdr || err != nil {
		t.Errorf("settings herdr: %v %v", b, err)
	}

	t.Setenv(tmux.EnvMultiplexer, "tmux")
	if b, _ := resolveMultiplexer(root); b != tmux.BackendTmux {
		t.Errorf("env should beat settings, got %v", b)
	}

	t.Setenv(tmux.EnvMultiplexer, "bogus")
	if b, err := resolveMultiplexer(root); b != tmux.BackendTmux || err == nil {
		t.Errorf("bogus env: %v %v", b, err)
	}

	t.Setenv(tmux.EnvMultiplexer, "")
	writeSettings(t, root, `{not json`)
	if b, err := resolveMultiplexer(root); b != tmux.BackendTmux || err != nil {
		t.Errorf("corrupt settings should mean tmux: %v %v", b, err)
	}
	writeSettings(t, root, `{"multiplexer":"zellij"}`)
	if b, err := resolveMultiplexer(root); b != tmux.BackendTmux || err == nil {
		t.Errorf("unknown settings value should error: %v %v", b, err)
	}
}

func TestPrependPathIdempotent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	prependPath("/x/shim")
	prependPath("/x/shim")
	if got := os.Getenv("PATH"); !strings.HasPrefix(got, "/x/shim"+string(os.PathListSeparator)) || strings.Count(got, "/x/shim") != 1 {
		t.Errorf("PATH = %q", got)
	}
}
