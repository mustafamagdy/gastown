package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

// CurrentProjectVersion is the current schema version for ProjectConfig.
const CurrentProjectVersion = 1

// ProjectConfig represents a project: a named group of rigs in a town.
// It is stored at <townRoot>/projects/<name>/project.json. Which rigs belong
// to the project is recorded on the rigs (RigEntry.Project), not here.
type ProjectConfig struct {
	Type        string `json:"type"`                  // "project"
	Version     int    `json:"version"`               // schema version
	Name        string `json:"name"`                  // project name (also the directory name)
	Prefix      string `json:"prefix"`                // project beads prefix, without trailing hyphen
	Docs        string `json:"docs,omitempty"`        // path to the project docs folder (may use ~)
	Memory      string `json:"memory,omitempty"`      // agent-brain key, e.g. "mbg/xelta"
	Description string `json:"description,omitempty"` // short human description

	// OnDemand makes the project's rigs run their witness and refinery only
	// while they have work. nil means true (the default for projects).
	OnDemand *bool `json:"on_demand,omitempty"`
}

// IsOnDemand reports whether the project's rigs run agents on demand (default true).
func (p *ProjectConfig) IsOnDemand() bool {
	return p == nil || p.OnDemand == nil || *p.OnDemand
}

// projectPrefixRegexp mirrors the beads prefix rules used for rigs: starts with
// a letter, alphanumeric with optional hyphens, max 20 chars.
var projectPrefixRegexp = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,19}$`)

// ValidateProjectName checks a project name using the same rules as rig names:
// hyphens, dots, spaces and path separators break agent ID parsing and paths.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	if strings.ContainsAny(name, "-. /\\") {
		return fmt.Errorf("project name %q contains invalid characters; hyphens, dots, spaces, and path separators are not allowed (underscores are allowed)", name)
	}
	if strings.EqualFold(name, "hq") {
		return fmt.Errorf("project name %q is reserved for town-level infrastructure", name)
	}
	return nil
}

// ProjectsDir returns the directory holding all projects of a town.
func ProjectsDir(townRoot string) string {
	return filepath.Join(townRoot, "projects")
}

// ProjectDir returns the directory of a project: <townRoot>/projects/<name>.
func ProjectDir(townRoot, name string) string {
	return filepath.Join(ProjectsDir(townRoot), name)
}

// ProjectConfigPath returns the path of a project's project.json.
func ProjectConfigPath(townRoot, name string) string {
	return filepath.Join(ProjectDir(townRoot, name), "project.json")
}

// ProjectContextPath returns the path of a project's CONTEXT.md.
func ProjectContextPath(townRoot, name string) string {
	return filepath.Join(ProjectDir(townRoot, name), "CONTEXT.md")
}

// LoadProjectConfig loads a project's config. Returns ErrNotFound (wrapped)
// when the project does not exist.
func LoadProjectConfig(townRoot, name string) (*ProjectConfig, error) {
	if err := ValidateProjectName(name); err != nil {
		return nil, err
	}
	path := ProjectConfigPath(townRoot, name)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading project config: %w", err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing project config: %w", err)
	}
	if err := ValidateProjectConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveProjectConfig saves a project's config atomically, creating the project
// directory if needed.
func SaveProjectConfig(townRoot string, cfg *ProjectConfig) error {
	if cfg == nil {
		return fmt.Errorf("project config is nil")
	}
	if cfg.Type == "" {
		cfg.Type = "project"
	}
	if cfg.Version == 0 {
		cfg.Version = CurrentProjectVersion
	}
	if err := ValidateProjectConfig(cfg); err != nil {
		return err
	}
	dir := ProjectDir(townRoot, cfg.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding project config: %w", err)
	}
	if err := atomicfile.WriteFile(ProjectConfigPath(townRoot, cfg.Name), data, 0644); err != nil {
		return fmt.Errorf("writing project config: %w", err)
	}
	return nil
}

// ValidateProjectConfig checks a project config for a valid type, version, name and prefix.
func ValidateProjectConfig(cfg *ProjectConfig) error {
	if cfg.Type != "project" {
		return fmt.Errorf("%w: expected type 'project', got %q", ErrInvalidType, cfg.Type)
	}
	if cfg.Version > CurrentProjectVersion {
		return fmt.Errorf("%w: project config version %d is newer than supported %d", ErrInvalidVersion, cfg.Version, CurrentProjectVersion)
	}
	if err := ValidateProjectName(cfg.Name); err != nil {
		return err
	}
	if !projectPrefixRegexp.MatchString(cfg.Prefix) {
		return fmt.Errorf("invalid project prefix %q: must be alphanumeric with optional hyphens, start with a letter, max 20 chars", cfg.Prefix)
	}
	return nil
}

// ListProjects returns the names of all projects in a town, sorted.
// A directory counts as a project when it contains a project.json.
func ListProjects(townRoot string) ([]string, error) {
	entries, err := os.ReadDir(ProjectsDir(townRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading projects directory: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(ProjectConfigPath(townRoot, e.Name())); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// ProjectForRig returns the project a rig belongs to, or "" if it belongs to
// none (or the rig or rigs.json does not exist).
func ProjectForRig(townRoot, rigName string) (string, error) {
	if townRoot == "" || rigName == "" {
		return "", nil
	}
	rigsConfig, err := LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return rigsConfig.Rigs[rigName].Project, nil
}

// RigsInProject returns the names of the rigs belonging to a project, sorted.
func RigsInProject(rigsConfig *RigsConfig, project string) []string {
	var names []string
	for name, entry := range rigsConfig.Rigs {
		if entry.Project == project {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// RigOnDemand reports whether a rig runs its witness and refinery only while
// it has work: true iff the rig belongs to a project whose OnDemand is unset
// or true. Rigs outside projects (or with unreadable project config) are
// always-on, matching upstream behaviour.
func RigOnDemand(townRoot, rigName string) bool {
	project, err := ProjectForRig(townRoot, rigName)
	if err != nil || project == "" {
		return false
	}
	cfg, err := LoadProjectConfig(townRoot, project)
	if err != nil {
		return false
	}
	return cfg.IsOnDemand()
}
