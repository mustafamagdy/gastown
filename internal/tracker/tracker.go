// Package tracker is the planning sync layer: it reads ready items from the
// org's planning tracker (Azure Boards or GitHub Issues) and writes a small,
// forward-only status back. Beads stays the only working tracker inside gt.
//
// It shells out to the stable machine interfaces of the platform CLIs
// (`az ... --output json`, `gh ... --json`) with exec args, never through a
// shell, and never logs or embeds credentials. Azure authentication is az's own
// per-organization store (filled by 'gt azure login'); GitHub uses gh's.
//
// gt only ever changes an item's state (Azure), or its gt-active label and
// open/closed status (GitHub). It posts no comments, edits no fields and
// creates or deletes no items.
package tracker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// ActiveLabel is the GitHub label that marks an issue as being worked on.
const ActiveLabel = "gt-active"

// GitHub pseudo states. Azure uses the project's own state names.
const (
	GitHubOpen   = "open"
	GitHubActive = "active"
	GitHubClosed = "closed"
)

// Child is a child item (Azure child work item, GitHub sub-issue).
type Child struct {
	ID          string
	Title       string
	Description string
	State       string
	URL         string
	Tags        []string // Azure tags, GitHub labels
}

// Item is a tracker item.
type Item struct {
	ID          string
	Title       string
	Description string
	State       string
	URL         string
	Tags        []string
	Children    []Child // only filled by Get
}

// Tracker is one project's planning tracker.
type Tracker interface {
	Kind() string
	// ActiveState and DoneState are the states gt writes.
	ActiveState() string
	DoneState() string
	// ListReady returns open items carrying the ready tag or label. Children
	// are not filled.
	ListReady(ctx context.Context) ([]Item, error)
	// Get returns one item with its children.
	Get(ctx context.Context, id string) (*Item, error)
	// State returns only the item's current state: one read, no children.
	State(ctx context.Context, id string) (string, error)
	// SetState moves the item to the active or the done state.
	SetState(ctx context.Context, id, state string) error
}

// Runner runs a CLI and returns its stdout. It is the seam tests replace.
type Runner func(ctx context.Context, bin string, args ...string) ([]byte, error)

// ExecRunner is the production Runner: exec args, no shell.
func ExecRunner(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: bin is a fixed CLI name, args are exec args
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		s := strings.TrimSpace(stderr.String())
		if len(s) > 500 {
			s = s[:500] + "..."
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", bin, subcommand(args), err, s)
	}
	return stdout.Bytes(), nil
}

// subcommand returns the leading non-flag words of args, for error messages.
func subcommand(args []string) string {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(words) >= 3 {
			break
		}
		words = append(words, a)
	}
	return strings.Join(words, " ")
}

// New builds the tracker of a validated config. A nil run uses ExecRunner.
func New(cfg *config.TrackerConfig, run Runner) (Tracker, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no tracker configured")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if run == nil {
		run = ExecRunner
	}
	switch cfg.Kind {
	case config.TrackerAzure:
		return &azureTracker{cfg: *cfg, run: run}, nil
	case config.TrackerGitHub:
		return &githubTracker{cfg: *cfg, run: run}, nil
	}
	return nil, fmt.Errorf("unknown tracker kind %q", cfg.Kind)
}

// validItemID reports whether id is a plain number, so it can never be read as
// a flag by the CLI.
func validItemID(id string) bool {
	if id == "" || len(id) > 12 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}
