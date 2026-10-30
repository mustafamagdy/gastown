package config

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadRoleDirective loads role directive content from the directive file layout.
// Resolution order:
//  1. Town-level: <townRoot>/directives/<role>.md
//  2. Project-level: <townRoot>/projects/<project>/directives/<role>.md, where
//     <project> is the project the rig belongs to (skipped if it has none)
//  3. Rig-level:  <townRoot>/<rigName>/directives/<role>.md
//
// Existing tiers are concatenated (town, project, rig) separated by a newline,
// giving rig-level content the last word. If only one exists, that content is
// returned. Returns empty string if no directive files exist.
//
// Invalid or unreadable paths are treated as absent (no error).
func LoadRoleDirective(role, townRoot, rigName string) string {
	var parts []string

	// Town-level directive
	townPath := filepath.Join(townRoot, "directives", role+".md")
	if content, err := os.ReadFile(townPath); err == nil { //nolint:gosec // G304: path is from trusted config
		if s := strings.TrimSpace(string(content)); s != "" {
			parts = append(parts, s)
		}
	}

	// Project-level directive
	if rigName != "" {
		if project, _ := ProjectForRig(townRoot, rigName); project != "" {
			if s := readDirective(ProjectDirectivePath(townRoot, project, role)); s != "" {
				parts = append(parts, s)
			}
		}
	}

	// Rig-level directive (wins by appearing last)
	if rigName != "" {
		rigPath := filepath.Join(townRoot, rigName, "directives", role+".md")
		if content, err := os.ReadFile(rigPath); err == nil { //nolint:gosec // G304: path is from trusted config
			if s := strings.TrimSpace(string(content)); s != "" {
				parts = append(parts, s)
			}
		}
	}

	return strings.Join(parts, "\n")
}

// ProjectDirectivePath returns the path of a project-level role directive.
func ProjectDirectivePath(townRoot, project, role string) string {
	return filepath.Join(ProjectDir(townRoot, project), "directives", role+".md")
}

// readDirective returns the trimmed content of a directive file, or "" if it
// is absent, unreadable or empty.
func readDirective(path string) string {
	content, err := os.ReadFile(path) //nolint:gosec // G304: path is from trusted config
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}
