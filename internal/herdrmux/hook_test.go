//go:build !windows

package herdrmux

import (
	"reflect"
	"testing"
)

func TestSplitCommandString(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`set-option -t s remain-on-exit on`, []string{"set-option", "-t", "s", "remain-on-exit", "on"}},
		{`run-shell "gt log crash --agent 'a b' --exit-code #{pane_dead_status}"`,
			[]string{"run-shell", "gt log crash --agent 'a b' --exit-code #{pane_dead_status}"}},
		// The auto-respawn hook gastown installs (buildAutoRespawnHookCmd).
		{`run-shell -b "sleep 3 && tmux -L gt-1 list-panes -t 'hq-mayor' -F '##{pane_dead}' 2>/dev/null | grep -q 1 && tmux -L gt-1 respawn-pane -k -t 'hq-mayor' || true"`,
			[]string{"run-shell", "-b", "sleep 3 && tmux -L gt-1 list-panes -t 'hq-mayor' -F '##{pane_dead}' 2>/dev/null | grep -q 1 && tmux -L gt-1 respawn-pane -k -t 'hq-mayor' || true"}},
		{`echo "a \"q\" \$x" 'lit\n' b\ c`, []string{"echo", `a "q" $x`, `lit\n`, "b c"}},
		{`  `, nil},
	}
	for _, tt := range tests {
		got, err := splitCommandString(tt.in)
		if err != nil {
			t.Fatalf("%q: %v", tt.in, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q:\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
	if _, err := splitCommandString(`run-shell "unterminated`); err == nil {
		t.Error("expected error for unterminated quote")
	}
}

func TestRenderEscapedFormatSurvivesForNestedTmux(t *testing.T) {
	// run-shell expands formats once; ## must leave #{pane_dead} for the
	// nested tmux list-panes to evaluate later.
	got := RenderFormat(`tmux list-panes -F '##{pane_dead}' --exit #{pane_dead_status}`, func(n string) string {
		if n == "pane_dead_status" {
			return "3"
		}
		return ""
	})
	if want := `tmux list-panes -F '#{pane_dead}' --exit 3`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
