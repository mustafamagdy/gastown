// Package projectmap builds and stores the service map of a project: which
// service each repo (rig) owns, the APIs it serves and calls, the events it
// publishes and consumes, and its database. The map is stored at
// <town>/projects/<project>/service-map.json (plus a rendered service-map.md)
// and agents are shown only their own entry and their neighbours.
package projectmap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
)

// CurrentVersion is the schema version of the stored map.
const CurrentVersion = 1

// Endpoint is one HTTP operation a service serves.
type Endpoint struct {
	Method string `json:"method"` // upper case, e.g. "GET"
	Path   string `json:"path"`
}

func (e Endpoint) String() string { return e.Method + " " + e.Path }

// Service is the map entry of one rig.
type Service struct {
	Rig       string     `json:"rig"`
	Repo      string     `json:"repo,omitempty"` // last element of the git URL, without .git
	Name      string     `json:"name"`
	Owns      string     `json:"owns,omitempty"`
	Serves    []Endpoint `json:"serves,omitempty"`
	Calls     []string   `json:"calls,omitempty"` // rig names where resolvable, otherwise as written
	Publishes []string   `json:"publishes,omitempty"`
	Consumes  []string   `json:"consumes,omitempty"`
	Database  string     `json:"database,omitempty"`
	Source    string     `json:"source,omitempty"` // contributing sources, e.g. "manifest,openapi"
	Commit    string     `json:"commit,omitempty"` // scanned commit of the rig's default branch
}

// Map is the service map of a project.
type Map struct {
	Version     int       `json:"version"`
	Project     string    `json:"project"`
	GeneratedAt time.Time `json:"generated_at"`
	Services    []Service `json:"services"` // sorted by Rig
}

// Path returns the location of a project's service-map.json.
func Path(townRoot, project string) string {
	return filepath.Join(config.ProjectDir(townRoot, project), "service-map.json")
}

// MarkdownPath returns the location of a project's rendered service-map.md.
func MarkdownPath(townRoot, project string) string {
	return filepath.Join(config.ProjectDir(townRoot, project), "service-map.md")
}

// OverrideDir returns the directory of hand-written per-rig overrides.
func OverrideDir(townRoot, project string) string {
	return filepath.Join(config.ProjectDir(townRoot, project), "services")
}

// Load reads a project's stored map. The error wraps os.ErrNotExist when no
// map has been built yet.
func Load(townRoot, project string) (*Map, error) {
	data, err := os.ReadFile(Path(townRoot, project)) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		return nil, err
	}
	var m Map
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing service map: %w", err)
	}
	return &m, nil
}

// Save writes the map (JSON and the rendered markdown) atomically.
func Save(townRoot string, m *Map) error {
	if m == nil || m.Project == "" {
		return fmt.Errorf("service map has no project")
	}
	if m.Version == 0 {
		m.Version = CurrentVersion
	}
	sortServices(m)
	if err := os.MkdirAll(config.ProjectDir(townRoot, m.Project), 0755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding service map: %w", err)
	}
	if err := atomicfile.WriteFile(Path(townRoot, m.Project), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing service map: %w", err)
	}
	if err := atomicfile.WriteFile(MarkdownPath(townRoot, m.Project), []byte(RenderMarkdown(m)), 0644); err != nil {
		return fmt.Errorf("writing service map markdown: %w", err)
	}
	return nil
}

func sortServices(m *Map) {
	sort.Slice(m.Services, func(i, j int) bool { return m.Services[i].Rig < m.Services[j].Rig })
}

// Find returns the entry of a rig.
func (m *Map) Find(rig string) *Service {
	for i := range m.Services {
		if m.Services[i].Rig == rig {
			return &m.Services[i]
		}
	}
	return nil
}

// normKey makes names comparable across hyphen and underscore spellings
// ("billing-svc" in a manifest vs the rig "billing_svc").
func normKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

// resolver maps a name as written in a manifest to a rig of the map.
type resolver map[string]string

func newResolver(services []Service) resolver {
	r := resolver{}
	// Weakest first so that a rig name always beats a service or repo name.
	for _, pass := range []func(Service) string{
		func(s Service) string { return s.Name },
		func(s Service) string { return s.Repo },
		func(s Service) string { return s.Rig },
	} {
		for _, s := range services {
			if k := normKey(pass(s)); k != "" {
				r[k] = s.Rig
			}
		}
	}
	return r
}

// rig returns the rig a name refers to and whether it was found.
func (r resolver) rig(name string) (string, bool) {
	rig, ok := r[normKey(name)]
	return rig, ok
}

// normalizeCalls rewrites call targets to rig names where they resolve.
func normalizeCalls(services []Service) {
	r := newResolver(services)
	for i := range services {
		for j, c := range services[i].Calls {
			if rig, ok := r.rig(c); ok {
				services[i].Calls[j] = rig
			}
		}
		services[i].Calls = dedupSorted(services[i].Calls)
	}
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Relation describes how a neighbour is connected to a rig.
type Relation struct {
	Rig       string
	Calls     bool     // the rig calls the neighbour
	CalledBy  bool     // the neighbour calls the rig
	Consumers []string // events the rig publishes that the neighbour consumes
	Producers []string // events the rig consumes that the neighbour publishes
}

// Relations returns the neighbours of a rig with the reasons, sorted by rig.
func Relations(m *Map, rig string) []Relation {
	self := m.Find(rig)
	if self == nil {
		return nil
	}
	r := newResolver(m.Services)
	selfCalls := map[string]bool{}
	for _, c := range self.Calls {
		if t, ok := r.rig(c); ok {
			selfCalls[t] = true
		}
	}
	pub := toSet(self.Publishes)
	con := toSet(self.Consumes)

	var out []Relation
	for _, o := range m.Services {
		if o.Rig == rig {
			continue
		}
		rel := Relation{Rig: o.Rig, Calls: selfCalls[o.Rig]}
		for _, c := range o.Calls {
			if t, ok := r.rig(c); ok && t == rig {
				rel.CalledBy = true
			}
		}
		for _, e := range dedupSorted(o.Consumes) {
			if pub[e] {
				rel.Consumers = append(rel.Consumers, e)
			}
		}
		for _, e := range dedupSorted(o.Publishes) {
			if con[e] {
				rel.Producers = append(rel.Producers, e)
			}
		}
		if rel.Calls || rel.CalledBy || len(rel.Consumers) > 0 || len(rel.Producers) > 0 {
			out = append(out, rel)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rig < out[j].Rig })
	return out
}

// Neighbours returns the rigs connected to rig: rigs it calls, rigs that call
// it, rigs consuming events it publishes and rigs publishing events it
// consumes. Sorted, deduplicated, excluding rig itself.
func Neighbours(m *Map, rig string) []string {
	var out []string
	for _, rel := range Relations(m, rig) {
		out = append(out, rel.Rig)
	}
	return out
}

func toSet(in []string) map[string]bool {
	s := make(map[string]bool, len(in))
	for _, v := range in {
		s[strings.TrimSpace(v)] = true
	}
	return s
}
