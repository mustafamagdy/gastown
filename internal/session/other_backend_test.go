package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

func withBackend(t *testing.T, b tmux.Backend) {
	t.Helper()
	old := tmux.GetBackend()
	tmux.SetBackend(b)
	t.Cleanup(func() { tmux.SetBackend(old) })
}

func TestListInactive_HerdrActive_FindsTmux(t *testing.T) {
	withBackend(t, tmux.BackendHerdr)
	oldL, oldR := otherLookPath, otherRunTmux
	t.Cleanup(func() { otherLookPath, otherRunTmux = oldL, oldR })
	otherLookPath = func(string) (string, error) { return "/bin/tmux", nil }
	var got []string
	otherRunTmux = func(bin string, args ...string) ([]byte, error) {
		got = args
		return []byte("hq-mayor\nhq-deacon\n"), nil
	}
	s := ListInactiveBackendSessions("gt-abc")
	if s == nil || !reflect.DeepEqual(s.Sessions, []string{"hq-mayor", "hq-deacon"}) {
		t.Fatalf("got %+v", s)
	}
	if !reflect.DeepEqual(got, []string{"-L", "gt-abc", "list-sessions", "-F", "#{session_name}"}) {
		t.Errorf("args %v", got)
	}
	if s.StopCmd != "tmux -L gt-abc kill-server" {
		t.Errorf("stop cmd %q", s.StopCmd)
	}
}

func TestListInactive_HerdrActive_NoServerOrBinary(t *testing.T) {
	withBackend(t, tmux.BackendHerdr)
	oldL, oldR := otherLookPath, otherRunTmux
	t.Cleanup(func() { otherLookPath, otherRunTmux = oldL, oldR })
	otherLookPath = func(string) (string, error) { return "/bin/tmux", nil }
	otherRunTmux = func(string, ...string) ([]byte, error) { return nil, errors.New("exit 1") }
	if s := ListInactiveBackendSessions("x"); s != nil {
		t.Errorf("want nil, got %+v", s)
	}
	otherLookPath = func(string) (string, error) { return "", errors.New("nope") }
	if s := ListInactiveBackendSessions("x"); s != nil {
		t.Errorf("want nil, got %+v", s)
	}
}

func TestListInactive_TmuxActive_FindsHerdr(t *testing.T) {
	withBackend(t, tmux.BackendTmux)
	oldL, oldH, oldS := otherLookPath, otherRunHerdrmux, otherStateRoot
	t.Cleanup(func() { otherLookPath, otherRunHerdrmux, otherStateRoot = oldL, oldH, oldS })
	root := t.TempDir()
	otherStateRoot = func() string { return root }
	otherLookPath = func(string) (string, error) { return "/bin/herdr", nil }
	calls := 0
	otherRunHerdrmux = func(argv []string) ([]byte, int) { calls++; return []byte("hq-mayor\n"), 0 }

	if s := ListInactiveBackendSessions("gt-abc"); s != nil || calls != 0 {
		t.Fatalf("expected no check without state, got %+v calls=%d", s, calls)
	}
	dir := filepath.Join(root, "gt-abc", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hq-mayor.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := ListInactiveBackendSessions("gt-abc")
	if s == nil || s.Backend != tmux.BackendHerdr || s.StopCmd != "GT_MULTIPLEXER=herdr gt down" {
		t.Fatalf("got %+v", s)
	}
	otherLookPath = func(string) (string, error) { return "", errors.New("nope") }
	if s := ListInactiveBackendSessions("gt-abc"); s != nil {
		t.Errorf("want nil without herdr")
	}
}

func TestResolveTownSocket(t *testing.T) {
	t.Setenv("GT_TMUX_SOCKET", "custom")
	if got := ResolveTownSocket("/x/gt"); got != "custom" {
		t.Errorf("got %q", got)
	}
	t.Setenv("GT_TMUX_SOCKET", "auto")
	if got := ResolveTownSocket("/x/gt"); got != TownSocketName("/x/gt") {
		t.Errorf("got %q", got)
	}
}
