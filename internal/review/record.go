// Package review is the visual review gate: a human reviews an agent's HTML
// page in the browser (lavish-axi), and the outcome flows back to the bead and
// the agent and can gate merging.
//
// The records under <town>/.runtime/reviews are the source of truth. Labels
// and comments on the bead are a mirror for people.
package review

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/lock"
)

// Review kinds.
const (
	KindPlan     = "plan"
	KindDesign   = "design"
	KindQuestion = "question"
	KindApproval = "approval"
)

// Review states.
const (
	StateOpen         = "open"         // waiting for the human
	StateApproved     = "approved"     // the human approved
	StateRejected     = "rejected"     // the human asked for changes
	StateAnswered     = "answered"     // a question was answered
	StateEnded        = "ended"        // the session ended without a decision
	StateDisconnected = "disconnected" // the browser went away; resumable
)

// Decisions sent by the page panel.
const (
	DecisionApprove = "approve"
	DecisionChanges = "changes"
	DecisionAnswer  = "answer"
)

// PanelTag is the prompt tag the gt panel uses.
const PanelTag = "gt-review"

// MaxAssetBytes caps the size of a page and its sibling files.
const MaxAssetBytes = 50 << 20

// Feedback is one piece of feedback from the human.
type Feedback struct {
	At       time.Time `json:"at"`
	Tag      string    `json:"tag,omitempty"`      // lavish prompt tag
	Decision string    `json:"decision,omitempty"` // approve, changes, answer (panel only)
	Text     string    `json:"text"`
	Selector string    `json:"selector,omitempty"`
}

// Record is one review request.
type Record struct {
	Version   int        `json:"version"`
	ID        string     `json:"id"`
	Bead      string     `json:"bead"`
	Kind      string     `json:"kind"`
	HTML      string     `json:"html"`   // the stored copy that lavish serves
	Source    string     `json:"source"` // the file the agent gave
	URL       string     `json:"url"`
	Requester string     `json:"requester"` // mail address of the asking agent
	State     string     `json:"state"`
	Feedback  []Feedback `json:"feedback,omitempty"`
	Error     string     `json:"error,omitempty"` // last problem the watcher saw
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

// Decided reports whether the human has settled the review.
func (r *Record) Decided() bool {
	switch r.State {
	case StateApproved, StateRejected, StateAnswered:
		return true
	}
	return false
}

// Watching reports whether the daemon should keep a poll running.
func (r *Record) Watching() bool { return r.State == StateOpen }

var idRegexp = regexp.MustCompile(`^rv-[0-9a-f]{8}$`)

// ValidID reports whether id has the shape of a review id.
func ValidID(id string) bool { return idRegexp.MatchString(id) }

// NewID returns a fresh review id.
func NewID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rv-" + hex.EncodeToString(b[:]), nil
}

// ValidKind reports whether kind is a known review kind.
func ValidKind(kind string) bool {
	switch kind {
	case KindPlan, KindDesign, KindQuestion, KindApproval:
		return true
	}
	return false
}

// ErrNotFound means no review has that id.
var ErrNotFound = errors.New("review not found")

// Dir returns the directory holding the town's review records and page copies.
func Dir(townRoot string) string { return filepath.Join(townRoot, ".runtime", "reviews") }

// AssetDir returns the directory holding the copy of a review's page.
func AssetDir(townRoot, id string) string { return filepath.Join(Dir(townRoot), id) }

func recordPath(townRoot, id string) string { return filepath.Join(Dir(townRoot), id+".json") }

func lockPath(townRoot string) string { return filepath.Join(Dir(townRoot), ".store.lock") }

func acquire(townRoot string) (func(), error) {
	if err := os.MkdirAll(Dir(townRoot), 0o755); err != nil {
		return nil, err
	}
	return lock.FlockAcquire(lockPath(townRoot))
}

func writeRecord(townRoot string, rec *Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(recordPath(townRoot, rec.ID), data, 0o644)
}

// Create stores a new record.
func Create(townRoot string, rec *Record) error {
	if !ValidID(rec.ID) {
		return fmt.Errorf("invalid review id %q", rec.ID)
	}
	release, err := acquire(townRoot)
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Stat(recordPath(townRoot, rec.ID)); err == nil {
		return fmt.Errorf("review %s already exists", rec.ID)
	}
	rec.Version = 1
	now := time.Now().UTC()
	rec.CreatedAt, rec.UpdatedAt = now, now
	return writeRecord(townRoot, rec)
}

// Load reads one record.
func Load(townRoot, id string) (*Record, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%w: %q is not a review id", ErrNotFound, id)
	}
	return load(recordPath(townRoot, id))
}

func load(path string) (*Record, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from a validated id
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, filepath.Base(path))
		}
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath.Base(path), err)
	}
	return &rec, nil
}

// Update changes a record under the store lock and saves it atomically.
func Update(townRoot, id string, fn func(*Record) error) (*Record, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%w: %q is not a review id", ErrNotFound, id)
	}
	release, err := acquire(townRoot)
	if err != nil {
		return nil, err
	}
	defer release()
	rec, err := load(recordPath(townRoot, id))
	if err != nil {
		return nil, err
	}
	if err := fn(rec); err != nil {
		return nil, err
	}
	rec.UpdatedAt = time.Now().UTC()
	if err := writeRecord(townRoot, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// List returns every readable record, oldest first, and one error per record
// that could not be read. A missing store is an empty list with no error.
func List(townRoot string) (recs []*Record, errs []error) {
	entries, err := os.ReadDir(Dir(townRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		rec, err := load(filepath.Join(Dir(townRoot), name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.Before(recs[j].CreatedAt) })
	return recs, errs
}

// HasOpen is a cheap check for the daemon: true when any review is open. It
// reads record files only when the store exists.
func HasOpen(townRoot string) bool {
	recs, _ := List(townRoot)
	for _, r := range recs {
		if r.Watching() {
			return true
		}
	}
	return false
}
