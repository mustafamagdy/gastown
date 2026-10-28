//go:build !windows

package herdrmux

import (
	"os"
	"strings"
)

// roleEmoji decorates herdr tab / worktree-workspace labels. It is a local
// table (herdrmux imports no other gastown package). Sources:
//   - mayor, polecat, crew, dog: the web dashboard, internal/web/templates/convoy.html
//   - deacon, witness, refinery, boot: internal/constants (EmojiDeacon, ...),
//     because the dashboard has no icon for them; a test keeps them in sync.
//
// Unknown roles get no emoji.
var roleEmoji = map[string]string{
	"mayor":    "🎩",
	"polecat":  "🦨",
	"crew":     "👨‍💼", // ZWJ sequence
	"dog":      "🐕",
	"deacon":   "🐺",
	"witness":  "🦉",
	"refinery": "🏭",
	"boot":     "🐾",
}

// rigEmoji prefixes per-rig group workspace labels (dashboard "Rigs").
const rigEmoji = "🏗️"

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

// groupLabelMatches reports whether a workspace label is the group's decorated
// label or its legacy undecorated form.
func groupLabelMatches(have, group string) bool {
	return have == group || have == GroupLabel(group)
}
