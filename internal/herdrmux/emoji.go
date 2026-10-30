//go:build !windows

package herdrmux

import (
	"os"
	"strings"
)

// roleEmoji decorates herdr tab / worktree-workspace labels. Every entry is a
// single codepoint with default emoji presentation (no ZWJ sequences, no
// U+FE0F variation selector, Unicode <= 9) so terminals render it at a
// predictable two-cell width; the dashboard's richer icons (👨‍💼, 🏗️, 🦨)
// misrender or drift column widths in terminal tab bars.
//
// Unknown roles get no emoji.
var roleEmoji = map[string]string{
	"mayor":    "👑",
	"deacon":   "🔔",
	"boot":     "👢",
	"dog":      "🐶",
	"witness":  "👀",
	"refinery": "🔀",
	"polecat":  "🐱",
	"crew":     "👷",
}

// rigEmoji prefixes per-rig group workspace labels.
const rigEmoji = "📦"

// emojiEnabled is false when GT_HERDR_EMOJI=0 (or false/off/no).
func emojiEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GT_HERDR_EMOJI"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// decorate returns "<emoji> <label>", or label unchanged when there is no
// emoji or decoration is disabled.
func decorate(emoji, label string) string {
	if emoji == "" || !emojiEnabled() {
		return label
	}
	return emoji + " " + label
}

// DisplayLabel is the herdr tab / worktree-workspace label for a role.
func DisplayLabel(role, label string) string { return decorate(roleEmoji[role], label) }

// GroupLabel is the herdr label of a group workspace: "hq" stays plain, a rig
// gets the rig emoji.
func GroupLabel(group string) string {
	if group == townGroup {
		return group
	}
	return decorate(rigEmoji, group)
}

// groupLabelMatches reports whether a workspace label is the group's label,
// either plain or "<emoji> group" with any single-token prefix, so workspaces
// labelled by builds with a different emoji are still reused (and relabelled).
func groupLabelMatches(have, group string) bool {
	if have == group {
		return true
	}
	i := strings.IndexByte(have, ' ')
	return i > 0 && have[i+1:] == group
}
