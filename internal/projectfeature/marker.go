package projectfeature

import "strings"

// MarkerKey is the description key that marks a bead as a task of a feature.
// It uses the same "key: value" line convention as beads/fields.go.
const MarkerKey = "project_feature"

// Marker returns the epic id from a bead description, or "" if the bead is
// not marked. The key match is case-insensitive and accepts a hyphen.
func Marker(description string) string {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		if key == MarkerKey || key == "project-feature" {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

// HasMarker reports whether the description contains a marker line, even one
// with an empty value (which resolution treats as an error).
func HasMarker(description string) bool {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		if key == MarkerKey || key == "project-feature" {
			return true
		}
	}
	return false
}

// AddMarker returns the description with the marker line first, replacing an
// existing one.
func AddMarker(description, epicID string) string {
	line := MarkerKey + ": " + epicID
	var out []string
	for _, l := range strings.Split(description, "\n") {
		t := strings.TrimSpace(l)
		if i := strings.Index(t, ":"); i >= 0 {
			k := strings.ToLower(strings.TrimSpace(t[:i]))
			if k == MarkerKey || k == "project-feature" {
				continue
			}
		}
		out = append(out, l)
	}
	rest := strings.TrimLeft(strings.Join(out, "\n"), "\n")
	if rest == "" {
		return line
	}
	return line + "\n\n" + rest
}
