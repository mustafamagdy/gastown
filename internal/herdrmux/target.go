package herdrmux

import (
	"os"
	"sort"
	"strings"
)

// Target is a parsed tmux -t argument.
type Target struct {
	Raw     string
	Empty   bool   // no target: use the current pane
	PaneID  string // herdr pane id when the target was %paneid
	Session string // session name (or "$N" id)
	Exact   bool   // "=" prefix: no prefix matching
	Window  string // window part, if any
	Pane    string // pane index part, if any
}

// EncodePaneID turns a herdr pane id ("w2:p1") into a tmux-style pane id
// ("%w2.p1").
func EncodePaneID(id string) string {
	return "%" + strings.ReplaceAll(id, ":", ".")
}

// DecodePaneID is the inverse of EncodePaneID. The leading % is optional.
func DecodePaneID(s string) string {
	s = strings.TrimPrefix(s, "%")
	return strings.ReplaceAll(s, ".", ":")
}

// ParseTarget parses a tmux target: name, =name, name:, name:0, name:0.0,
// %paneid, or empty.
func ParseTarget(s string) Target {
	t := Target{Raw: s}
	if s == "" {
		t.Empty = true
		return t
	}
	if strings.HasPrefix(s, "%") {
		t.PaneID = DecodePaneID(s)
		return t
	}
	if strings.HasPrefix(s, "=") {
		t.Exact = true
		s = s[1:]
	}
	sess, rest, hasColon := strings.Cut(s, ":")
	t.Session = sess
	if hasColon && rest != "" {
		win, pane, hasDot := strings.Cut(rest, ".")
		t.Window = win
		if hasDot {
			t.Pane = pane
		}
	}
	if t.Session == "" && t.Window == "" {
		t.Empty = true
	}
	return t
}

// currentPaneID returns the herdr pane id of the calling pane from the
// environment (TMUX_PANE as set by the supervisor, else HERDR_PANE_ID).
func currentPaneID() string {
	if p := os.Getenv("TMUX_PANE"); p != "" {
		return DecodePaneID(p)
	}
	return os.Getenv("HERDR_PANE_ID")
}

// matchSession picks the session named by t from the candidate names, using
// tmux rules: exact match first, then (unless "=") a unique prefix.
func matchSession(t Target, names []string) (string, bool) {
	for _, n := range names {
		if n == t.Session {
			return n, true
		}
	}
	if t.Exact || t.Session == "" {
		return "", false
	}
	var found []string
	for _, n := range names {
		if strings.HasPrefix(n, t.Session) {
			found = append(found, n)
		}
	}
	sort.Strings(found)
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}
