// Package projectfeature stores multi-repo features of a project.
//
// A feature is one epic in the project beads that spans several rigs (repos).
// Its source of truth is a feature record, a JSON file at
// <town>/projects/<project>/features/<epic-id>.json. The record, not bead
// edges, ties the epic to its tasks and to one same-named branch per rig:
// bd drops parent links across databases, and an epic holds only one
// integration branch and base, so neither can describe a feature that spans
// several rig databases.
//
// A task bead carries a "project_feature: <epic-id>" line in its description
// (see Marker). Target resolution (ResolveTarget) fails closed: a marked bead
// whose record or rig cannot be resolved is an error, never a silent fall back
// to the default branch.
package projectfeature

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
)

// CurrentVersion is the schema version of a feature record.
const CurrentVersion = 1

// Feature states.
const (
	StateOpen    = "open"
	StateLanding = "landing"
	StateLanded  = "landed"
	StateHeld    = "held"
)

// Per-rig states. RigBranch is the only one set by feature creation; the
// others are reserved for landing.
const (
	RigBranch  = "branch"  // feature branch exists in the rig
	RigPROpen  = "pr_open" // PR from the feature branch is open
	RigLanded  = "landed"  // PR merged
	RigHeld    = "held"    // PR rejected or blocked
	RigPending = "pending" // reserved: not yet branched
)

// ErrNotFound is returned (wrapped) when a feature record does not exist.
var ErrNotFound = errors.New("feature not found")

// RigState is the state of the feature in one rig.
type RigState struct {
	Base         string `json:"base"`                    // branch the feature branch was cut from
	BaseCommit   string `json:"base_commit,omitempty"`   // commit of Base at cut time
	State        string `json:"state"`                   // Rig* constants
	PRURL        string `json:"pr_url,omitempty"`        // set by landing
	PRID         string `json:"pr_id,omitempty"`         // set by landing
	LandedCommit string `json:"landed_commit,omitempty"` // set by landing
	Error        string `json:"error,omitempty"`         // last landing error
}

// Task is one rig bead of the feature. Its status is NOT stored: the bead's
// own status is authoritative and is always read from beads.
type Task struct {
	ID     string   `json:"id"` // rig bead id
	Rig    string   `json:"rig"`
	Title  string   `json:"title"`
	After  []string `json:"after,omitempty"`          // task ids that must be closed first (effective: explicit + derived)
	Manual []string `json:"after_explicit,omitempty"` // the subset given explicitly; kept when 'order' recomputes After
}

// Record is a feature record.
type Record struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"` // epic bead id
	Project   string              `json:"project"`
	Title     string              `json:"title"`
	Branch    string              `json:"branch"`
	State     string              `json:"state"`
	CreatedAt time.Time           `json:"created_at"`
	Rigs      map[string]RigState `json:"rigs"`
	Tasks     []Task              `json:"tasks"`
}

var idRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidID reports whether id is usable as an epic id and file name.
func ValidID(id string) bool { return len(id) <= 128 && idRegexp.MatchString(id) }

// Dir returns the directory holding a project's feature records.
func Dir(townRoot, project string) string {
	return filepath.Join(config.ProjectDir(townRoot, project), "features")
}

// Path returns the file of a feature record.
func Path(townRoot, project, id string) string {
	return filepath.Join(Dir(townRoot, project), id+".json")
}

// Task returns the task with the given bead id, or nil.
func (r *Record) Task(id string) *Task {
	for i := range r.Tasks {
		if r.Tasks[i].ID == id {
			return &r.Tasks[i]
		}
	}
	return nil
}

// Validate checks the record's internal consistency.
func (r *Record) Validate() error {
	if !ValidID(r.ID) {
		return fmt.Errorf("invalid feature id %q", r.ID)
	}
	if r.Project == "" {
		return fmt.Errorf("feature %s has no project", r.ID)
	}
	if r.Branch == "" {
		return fmt.Errorf("feature %s has no branch", r.ID)
	}
	seen := map[string]bool{}
	for _, t := range r.Tasks {
		if t.ID == "" || t.Rig == "" {
			return fmt.Errorf("feature %s has a task without id or rig", r.ID)
		}
		if seen[t.ID] {
			return fmt.Errorf("feature %s lists task %s twice", r.ID, t.ID)
		}
		seen[t.ID] = true
		if _, ok := r.Rigs[t.Rig]; !ok {
			return fmt.Errorf("feature %s task %s is in rig %q, which is not part of the feature", r.ID, t.ID, t.Rig)
		}
	}
	for _, t := range r.Tasks {
		for _, a := range t.After {
			if !seen[a] {
				return fmt.Errorf("feature %s task %s waits for unknown task %s", r.ID, t.ID, a)
			}
		}
	}
	return nil
}

// Load reads a feature record. The error wraps ErrNotFound when it is missing.
func Load(townRoot, project, id string) (*Record, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid feature id %q", id)
	}
	data, err := os.ReadFile(Path(townRoot, project, id)) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s in project %s", ErrNotFound, id, project)
		}
		return nil, fmt.Errorf("reading feature record: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parsing feature record %s: %w", id, err)
	}
	if rec.Rigs == nil {
		rec.Rigs = map[string]RigState{}
	}
	return &rec, nil
}

// Save validates and writes a record atomically. It does not lock; callers
// that read-modify-write use Update or Create.
func Save(townRoot string, rec *Record) error {
	if rec == nil {
		return fmt.Errorf("feature record is nil")
	}
	if rec.Version == 0 {
		rec.Version = CurrentVersion
	}
	if rec.State == "" {
		rec.State = StateOpen
	}
	if rec.Rigs == nil {
		rec.Rigs = map[string]RigState{}
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(townRoot, rec.Project), 0755); err != nil {
		return fmt.Errorf("creating features directory: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding feature record: %w", err)
	}
	if err := atomicfile.WriteFile(Path(townRoot, rec.Project, rec.ID), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing feature record: %w", err)
	}
	return nil
}

func acquire(townRoot, project, id string) (func(), error) {
	if err := os.MkdirAll(Dir(townRoot, project), 0755); err != nil {
		return nil, fmt.Errorf("creating features directory: %w", err)
	}
	return lock.FlockAcquire(filepath.Join(Dir(townRoot, project), "."+id+".lock"))
}

// Create writes a new record under the file lock. It fails if one exists.
func Create(townRoot string, rec *Record) error {
	if rec == nil || !ValidID(rec.ID) {
		return fmt.Errorf("invalid feature record")
	}
	release, err := acquire(townRoot, rec.Project, rec.ID)
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Stat(Path(townRoot, rec.Project, rec.ID)); err == nil {
		return fmt.Errorf("feature %s already exists", rec.ID)
	}
	return Save(townRoot, rec)
}

// Update loads a record, applies fn and saves the result, all under the file
// lock, so concurrent writers (CLI, daemon) do not lose each other's changes.
// If fn returns an error nothing is written.
func Update(townRoot, project, id string, fn func(*Record) error) (*Record, error) {
	release, err := acquire(townRoot, project, id)
	if err != nil {
		return nil, err
	}
	defer release()
	rec, err := Load(townRoot, project, id)
	if err != nil {
		return nil, err
	}
	if err := fn(rec); err != nil {
		return nil, err
	}
	if err := Save(townRoot, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// List returns the records of a project, sorted by id. Unreadable records are
// returned in errs rather than failing the listing.
func List(townRoot, project string) (recs []*Record, errs []error) {
	entries, err := os.ReadDir(Dir(townRoot, project))
	if err != nil {
		if !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		return nil, errs
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		rec, err := Load(townRoot, project, name[:len(name)-len(".json")])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	return recs, errs
}

// ListAll returns the records of every project of the town.
func ListAll(townRoot string) (recs []*Record, errs []error) {
	projects, err := config.ListProjects(townRoot)
	if err != nil {
		return nil, []error{err}
	}
	for _, p := range projects {
		r, e := List(townRoot, p)
		recs = append(recs, r...)
		errs = append(errs, e...)
	}
	return recs, errs
}

// Find returns the record of an epic id, searching every project.
func Find(townRoot, id string) (*Record, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid feature id %q", id)
	}
	projects, err := config.ListProjects(townRoot)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if _, err := os.Stat(Path(townRoot, p, id)); err == nil {
			return Load(townRoot, p, id)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// ResolveForBead finds the feature that lists beadID as a task, and the task.
// It scans the records, so it works without reading the bead.
func ResolveForBead(townRoot, beadID string) (*Record, *Task, error) {
	recs, errs := ListAll(townRoot)
	for _, rec := range recs {
		if t := rec.Task(beadID); t != nil {
			return rec, t, nil
		}
	}
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("%w: task %s (unreadable records: %v)", ErrNotFound, beadID, errs[0])
	}
	return nil, nil, fmt.Errorf("%w: no feature lists task %s", ErrNotFound, beadID)
}
