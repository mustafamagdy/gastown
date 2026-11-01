package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Tracker kinds.
const (
	TrackerAzure  = "azure"
	TrackerGitHub = "github"
)

// DefaultTrackerReady is the tag (Azure) or label (GitHub) that marks an item
// as ready to import when the config does not name one.
const DefaultTrackerReady = "gt-ready"

// TrackerConfig is a project's planning tracker, the "tracker" object of
// project.json. Azure fields and GitHub fields are checked per kind: a field of
// the other kind is an error.
//
//	{"kind":"azure","org":"geeksclub","project":"gt-pr-test","ready_tag":"gt-ready",
//	 "active_state":"Active","done_state":"Resolved"}
//	{"kind":"github","repo":"owner/name","ready_label":"gt-ready"}
type TrackerConfig struct {
	Kind string `json:"kind"`

	// Azure Boards.
	Org         string `json:"org,omitempty"`          // organization name or https URL
	Project     string `json:"project,omitempty"`      // Azure DevOps project
	ReadyTag    string `json:"ready_tag,omitempty"`    // default gt-ready
	ActiveState string `json:"active_state,omitempty"` // required: process templates differ
	DoneState   string `json:"done_state,omitempty"`   // required

	// GitHub Issues.
	Repo       string `json:"repo,omitempty"`        // owner/name
	ReadyLabel string `json:"ready_label,omitempty"` // default gt-ready
}

// OrgURL returns the Azure organization URL.
func (t *TrackerConfig) OrgURL() string {
	o := strings.TrimRight(strings.TrimSpace(t.Org), "/")
	if strings.HasPrefix(o, "https://") {
		return o
	}
	return "https://dev.azure.com/" + o
}

// Validate checks the tracker config and fills defaults.
func (t *TrackerConfig) Validate() error {
	switch t.Kind {
	case TrackerAzure:
		if t.Repo != "" || t.ReadyLabel != "" {
			return fmt.Errorf("tracker: repo and ready_label belong to kind \"github\", not \"azure\"")
		}
		for name, v := range map[string]string{"org": t.Org, "project": t.Project, "active_state": t.ActiveState, "done_state": t.DoneState} {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("tracker: %s is required for kind \"azure\"", name)
			}
		}
		if t.ActiveState == t.DoneState {
			return fmt.Errorf("tracker: active_state and done_state must differ")
		}
		if t.ReadyTag == "" {
			t.ReadyTag = DefaultTrackerReady
		}
	case TrackerGitHub:
		if t.Org != "" || t.Project != "" || t.ReadyTag != "" || t.ActiveState != "" || t.DoneState != "" {
			return fmt.Errorf("tracker: org, project, ready_tag, active_state and done_state belong to kind \"azure\", not \"github\"")
		}
		parts := strings.Split(t.Repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("tracker: repo must be owner/name, got %q", t.Repo)
		}
		if t.ReadyLabel == "" {
			t.ReadyLabel = DefaultTrackerReady
		}
	case "":
		return fmt.Errorf("tracker: kind is required (\"azure\" or \"github\")")
	default:
		return fmt.Errorf("tracker: unknown kind %q (want \"azure\" or \"github\")", t.Kind)
	}
	return nil
}

// ProjectTracker parses and validates a project's tracker. It returns
// (nil, nil) when the project has none. Unknown fields are errors.
func ProjectTracker(p *ProjectConfig) (*TrackerConfig, error) {
	if p == nil {
		return nil, nil
	}
	raw := bytes.TrimSpace(p.Tracker)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var t TrackerConfig
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("tracker: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}
