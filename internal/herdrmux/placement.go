//go:build !windows

package herdrmux

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Placement kinds.
const (
	// KindTab: the session is a tab inside a group workspace.
	KindTab = "tab"
	// KindWorktree: the session is the workspace of a herdr linked worktree.
	KindWorktree = "worktree"
	// legacy state (Kind == ""): the session is a whole workspace labelled
	// with the session name.
)

// townGroup is the label of the workspace that holds town-level sessions.
const townGroup = "hq"

// Placement says where a session lives in herdr's hierarchy.
type Placement struct {
	Kind     string // KindTab or KindWorktree (worktree falls back to a tab)
	Group    string // label of the group workspace ("hq" or the rig name)
	GroupCwd string // cwd for the group workspace (and its root tab)
	Label    string // tab label (or worktree workspace label)
	Rig      string // rig name, empty for town-level sessions
	Role     string // normalised role, may be empty
}

// townRoles are the roles that never belong to a rig.
var townRoles = map[string]bool{"mayor": true, "deacon": true, "boot": true, "dog": true, "overseer": true}

// rigRoles are the roles that live in a rig.
var rigRoles = map[string]bool{"witness": true, "refinery": true, "crew": true, "polecat": true}

var envInCommand = regexp.MustCompile(`(?:^|[\s;&|(])(GT_ROLE|GT_RIG|GT_POLECAT|GT_CREW|GT_TOWN_ROOT|GT_ROOT|GT_DOG_NAME)=('[^']*'|"[^"]*"|[^\s;&|)]+)`)

// envFromCommand extracts GT_* identity variables written inline in a
// command string ("export GT_ROLE=x && ..." or "env GT_ROLE=x cmd"), for
// sessions created without -e.
func envFromCommand(command string) map[string]string {
	out := map[string]string{}
	for _, m := range envInCommand.FindAllStringSubmatch(command, -1) {
		v := m[2]
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, ok := out[m[1]]; !ok {
			out[m[1]] = v
		}
	}
	return out
}

// parseRole splits GT_ROLE ("mayor", "deacon/boot", "demo/witness",
// "demo/polecats/alpha", "demo/crew/max", "dog") into role, rig and worker.
func parseRole(v string) (role, rig, worker string) {
	parts := strings.Split(strings.Trim(v, "/"), "/")
	norm := func(s string) string {
		if s == "polecats" {
			return "polecat"
		}
		return s
	}
	switch {
	case len(parts) == 1:
		return norm(parts[0]), "", ""
	case len(parts) == 2 && parts[0] == "deacon" && parts[1] == "boot":
		return "boot", "", ""
	case len(parts) == 2 && parts[0] == "deacon":
		return "deacon", "", ""
	case len(parts) == 2:
		return norm(parts[1]), parts[0], ""
	default:
		return norm(parts[1]), parts[0], parts[2]
	}
}

var rigSessionName = regexp.MustCompile(`^([^-]+)-(witness|refinery|crew-.+)$`)

// shortLabel derives a tab label from a session name by stripping the town
// or rig prefix. Ambiguous names are returned unchanged.
func shortLabel(name, rig string) string {
	if rest, ok := strings.CutPrefix(name, "hq-"); ok && rest != "" {
		return rest
	}
	if rig != "" {
		if rest, ok := strings.CutPrefix(name, rig+"-"); ok && rest != "" {
			return rest
		}
	}
	if m := rigSessionName.FindStringSubmatch(name); m != nil {
		return m[2]
	}
	return name
}

// townRootOf finds the town root: GT_TOWN_ROOT, then GT_ROOT (what AgentEnv
// exports), from the session env, then from this process's environment.
func townRootOf(env map[string]string) string {
	for _, k := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if v := env[k]; v != "" {
			return v
		}
	}
	for _, k := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Classify decides where a session goes. env is the -e environment passed to
// new-session; command is the pane command (searched for inline GT_*
// assignments when env lacks them); cwd is the session working directory.
func Classify(name, cwd string, env map[string]string, command string) Placement {
	merged := map[string]string{}
	for k, v := range envFromCommand(command) {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}

	role, rig, worker := parseRole(merged["GT_ROLE"])
	if v := merged["GT_RIG"]; v != "" {
		rig = v
	}
	if role == "" || (!townRoles[role] && !rigRoles[role]) {
		if merged["GT_POLECAT"] != "" {
			role = "polecat"
		} else if merged["GT_CREW"] != "" {
			role = "crew"
		}
	}
	if worker == "" {
		switch role {
		case "polecat":
			worker = merged["GT_POLECAT"]
		case "crew":
			worker = merged["GT_CREW"]
		}
	}

	// Name-based fallbacks when the env says nothing.
	if rig == "" && !townRoles[role] && !strings.HasPrefix(name, "hq-") {
		if m := rigSessionName.FindStringSubmatch(name); m != nil {
			rig = m[1]
			if role == "" {
				role = strings.SplitN(m[2], "-", 2)[0]
			}
		}
	}
	if townRoles[role] {
		rig = ""
	}

	root := townRootOf(merged)
	p := Placement{Role: role, Rig: rig}
	if rig == "" {
		p.Kind = KindTab
		p.Group = townGroup
		p.Label = shortLabel(name, "")
		p.GroupCwd = cwd
		if root != "" && isDir(root) {
			p.GroupCwd = root
		}
		return p
	}

	p.Group = rig
	p.Kind = KindTab
	p.Label = shortLabel(name, rig)
	p.GroupCwd = cwd
	if root != "" {
		switch repo := filepath.Join(root, rig, ".repo.git"); {
		case isDir(repo):
			p.GroupCwd = repo
		case isDir(filepath.Join(root, rig)):
			p.GroupCwd = filepath.Join(root, rig)
		}
	}
	if role == "polecat" {
		p.Kind = KindWorktree
		if worker != "" {
			p.Label = worker
		}
	}
	return p
}
