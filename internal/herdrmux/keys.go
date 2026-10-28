//go:build !windows

package herdrmux

import (
	"encoding/hex"
	"strings"
)

// keyAction is one step of a send-keys: either a herdr key name (sent with
// `pane send-keys`) or literal text (sent with `pane send-text`).
type keyAction struct {
	Key  string // herdr key name, when non-empty
	Text string // literal text otherwise
}

// Keys herdr 0.9.3 rejects ("unsupported key"); sent as raw escape sequences.
var rawKeys = map[string]string{
	"DC": "\x1b[3~", "Delete": "\x1b[3~",
	"IC": "\x1b[2~", "Insert": "\x1b[2~",
	"Home": "\x1b[H", "End": "\x1b[F",
	"PageUp": "\x1b[5~", "PgUp": "\x1b[5~", "PPage": "\x1b[5~",
	"PageDown": "\x1b[6~", "PgDn": "\x1b[6~", "NPage": "\x1b[6~",
}

var namedKeys = map[string]string{
	"Enter": "enter", "C-m": "enter",
	"Escape": "esc", "C-[": "esc",
	"Tab": "tab", "C-i": "tab",
	"BTab": "shift+tab", "S-Tab": "shift+tab",
	"Space":  "space",
	"BSpace": "backspace", "C-h": "backspace",
	"Up": "up", "Down": "down", "Left": "left", "Right": "right",
}

// MapKey maps one tmux key name to an action. Names that are not recognized
// as keys are sent as literal text (as tmux does).
func MapKey(name string) keyAction {
	if k, ok := namedKeys[name]; ok {
		return keyAction{Key: k}
	}
	if s, ok := rawKeys[name]; ok {
		return keyAction{Text: s}
	}
	// Function keys F1..F12.
	if len(name) >= 2 && name[0] == 'F' {
		n := 0
		ok := true
		for _, r := range name[1:] {
			if r < '0' || r > '9' {
				ok = false
				break
			}
			n = n*10 + int(r-'0')
		}
		if ok && n >= 1 && n <= 12 {
			return keyAction{Key: "f" + name[1:]}
		}
	}
	// C-x / C-X
	if len(name) == 3 && name[0] == 'C' && name[1] == '-' {
		c := name[2]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c >= 'a' && c <= 'z' {
			return keyAction{Key: "ctrl+" + string(c)}
		}
	}
	// M-x
	if len(name) == 3 && name[0] == 'M' && name[1] == '-' {
		return keyAction{Key: "alt+" + string(name[2])}
	}
	// C-M-x / M-C-x
	if len(name) == 5 && (strings.HasPrefix(name, "C-M-") || strings.HasPrefix(name, "M-C-")) {
		c := name[4]
		if c >= 'a' && c <= 'z' {
			return keyAction{Key: "ctrl+alt+" + string(c)}
		}
	}
	return keyAction{Text: name}
}

// MapKeys maps a send-keys argument list.
func MapKeys(args []string, literal bool) []keyAction {
	var out []keyAction
	for _, a := range args {
		if literal {
			out = append(out, keyAction{Text: a})
			continue
		}
		out = append(out, MapKey(a))
	}
	return out
}

// hexKeys decodes send-keys -H arguments (hex byte values) to text.
func hexKeys(args []string) string {
	var b []byte
	for _, a := range args {
		a = strings.TrimPrefix(strings.TrimPrefix(a, "0x"), "0X")
		if v, err := hex.DecodeString(strings.Repeat("0", len(a)%2) + a); err == nil {
			b = append(b, v...)
		}
	}
	return string(b)
}
