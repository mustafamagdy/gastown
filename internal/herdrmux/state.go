// Package herdrmux emulates the subset of the tmux CLI that Gas Town uses,
// carrying each command out against a herdr server. See
// docs/design/herdr-backend.md.
package herdrmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// SessionState is everything tmux would remember about a session that herdr
// does not: environment, options, hooks, timestamps and pane process status.
type SessionState struct {
	Name        string            `json:"name"`
	Created     int64             `json:"created"`  // unix seconds
	Activity    int64             `json:"activity"` // unix seconds
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	Hooks       map[string]string `json:"hooks,omitempty"`
	Command     string            `json:"command,omitempty"`       // shell command line the supervisor runs
	HerdrPaneID string            `json:"herdr_pane_id,omitempty"` // e.g. "w2:p1"
	// WorkspaceID / TabID / Kind / Label say where the session lives in
	// herdr. Kind is KindTab (a tab in a group workspace), KindWorktree (the
	// workspace of a linked worktree) or "" (legacy: a whole workspace
	// labelled with the session name). Label is the short tab/workspace name.
	WorkspaceID   string `json:"workspace_id,omitempty"`
	TabID         string `json:"tab_id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Label         string `json:"label,omitempty"`
	SupervisorPID int    `json:"supervisor_pid,omitempty"`
	PanePID       int    `json:"pane_pid,omitempty"` // child process pid (tmux #{pane_pid})
	// ScreenHash fingerprints the visible screen at the last activity query;
	// a change marks the session active (herdr has no activity timestamp).
	ScreenHash string `json:"screen_hash,omitempty"`
	Dead       bool   `json:"dead,omitempty"`
	DeadStatus int    `json:"dead_status,omitempty"`
	// RespawnKill is set by respawn-pane -k so the supervisor kills the
	// running child before starting the new Command.
	RespawnKill bool `json:"respawn_kill,omitempty"`
	// RespawnSeq increments on every respawn request; the supervisor compares
	// it to the last value it acted on.
	RespawnSeq int `json:"respawn_seq,omitempty"`
}

// GlobalState holds server-wide (-g) environment and options.
type GlobalState struct {
	Env     map[string]string `json:"env,omitempty"`
	Options map[string]string `json:"options,omitempty"`
	Hooks   map[string]string `json:"hooks,omitempty"`
}

// ErrNoState is returned when a session has no state file.
var ErrNoState = errors.New("no session state")

// StateRoot returns the root directory for all herdrmux state:
// $XDG_STATE_HOME/gt-herdr (default ~/.local/state/gt-herdr).
// GT_HERDR_STATE_DIR overrides it (tests).
func StateRoot() string {
	if d := os.Getenv("GT_HERDR_STATE_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "gt-herdr")
}

// Store is the state for one herdr session (one tmux socket).
type Store struct {
	Dir string // StateRoot()/<sock>
}

// NewStore returns the store for the given socket / herdr session name.
func NewStore(sock string) *Store {
	if sock == "" {
		sock = "default"
	}
	return &Store{Dir: filepath.Join(StateRoot(), sock)}
}

func (s *Store) sessionPath(name string) string {
	return filepath.Join(s.Dir, "sessions", name+".json")
}

func (s *Store) globalPath() string { return filepath.Join(s.Dir, "global.json") }

// LogPath is where the detached herdr server's output goes.
func (s *Store) LogPath() string { return filepath.Join(s.Dir, "server.log") }

func (s *Store) lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fl := flock.New(path + ".lock")
	if err := fl.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = fl.Unlock() }, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a session's state. Returns ErrNoState if it does not exist.
func (s *Store) Load(name string) (*SessionState, error) {
	var st SessionState
	if err := readJSON(s.sessionPath(name), &st); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoState
		}
		return nil, err
	}
	return &st, nil
}

// Update applies fn to a session's state under an exclusive lock and saves it.
// If the state does not exist and create is false, ErrNoState is returned;
// if create is true, fn receives a fresh state with Name set.
func (s *Store) Update(name string, create bool, fn func(*SessionState) error) error {
	path := s.sessionPath(name)
	unlock, err := s.lock(path)
	if err != nil {
		return err
	}
	defer unlock()

	st, err := s.Load(name)
	if errors.Is(err, ErrNoState) {
		if !create {
			return ErrNoState
		}
		now := time.Now().Unix()
		st = &SessionState{Name: name, Created: now, Activity: now}
	} else if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return writeJSON(path, st)
}

// Delete removes a session's state.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.sessionPath(name))
	_ = os.Remove(s.sessionPath(name) + ".lock")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Rename moves a session's state to a new name.
func (s *Store) Rename(oldName, newName string) error {
	if err := os.MkdirAll(filepath.Dir(s.sessionPath(newName)), 0o700); err != nil {
		return err
	}
	if err := os.Rename(s.sessionPath(oldName), s.sessionPath(newName)); err != nil {
		return err
	}
	_ = os.Remove(s.sessionPath(oldName) + ".lock")
	return s.Update(newName, false, func(st *SessionState) error {
		st.Name = newName
		return nil
	})
}

// List returns the names of all sessions with state, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".json") {
			names = append(names, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// LoadGlobal reads the global state (empty if none).
func (s *Store) LoadGlobal() (*GlobalState, error) {
	var g GlobalState
	if err := readJSON(s.globalPath(), &g); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &g, nil
}

// UpdateGlobal applies fn to the global state under an exclusive lock.
func (s *Store) UpdateGlobal(fn func(*GlobalState) error) error {
	path := s.globalPath()
	unlock, err := s.lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	g, err := s.LoadGlobal()
	if err != nil {
		return err
	}
	if err := fn(g); err != nil {
		return err
	}
	return writeJSON(path, g)
}

// RemoveAll deletes all state for this socket (kill-server).
func (s *Store) RemoveAll() error { return os.RemoveAll(s.Dir) }

// sessionMapFile records which herdr session a socket's sessions live in when
// it is not the socket's own dedicated herdr session (town setting
// "herdr_session").
const sessionMapFile = "herdr-session"

// SetHerdrSession records that sessions on the given tmux socket live in the
// named herdr session. An empty name removes the mapping, so the socket uses
// its own dedicated herdr session.
func SetHerdrSession(sock, herdrSession string) error {
	path := filepath.Join(NewStore(sock).Dir, sessionMapFile)
	herdrSession = strings.TrimSpace(herdrSession)
	if herdrSession == "" || herdrSession == sock {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == herdrSession {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(herdrSession), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HerdrSessionFor returns the herdr session that backs a tmux socket: the
// mapping set by SetHerdrSession, else the socket's own name.
func HerdrSessionFor(sock string) string {
	data, err := os.ReadFile(filepath.Join(NewStore(sock).Dir, sessionMapFile))
	if err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name
		}
	}
	return sock
}

// --- groups --------------------------------------------------------------

// groupRec tracks one group workspace ("hq" or a rig) created by us.
type groupRec struct {
	WorkspaceID string `json:"workspace_id"`
	// FreeTab / FreePane are the workspace's initial shell tab while no
	// session has claimed it yet; the first session placed there reuses it.
	FreeTab  string `json:"free_tab,omitempty"`
	FreePane string `json:"free_pane,omitempty"`
}

func (s *Store) groupsPath() string { return filepath.Join(s.Dir, "groups.json") }

// UpdateGroups applies fn to the label -> group record map under an
// exclusive lock held for the whole call, so concurrent new-sessions
// serialise their group creation.
func (s *Store) UpdateGroups(fn func(map[string]groupRec) error) error {
	path := s.groupsPath()
	unlock, err := s.lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	g := map[string]groupRec{}
	if err := readJSON(path, &g); err != nil && !errors.Is(err, os.ErrNotExist) {
		g = map[string]groupRec{} // unreadable record: start over, groups are re-found by id+label
	}
	if g == nil {
		g = map[string]groupRec{}
	}
	if err := fn(g); err != nil {
		return err
	}
	return writeJSON(path, g)
}

// LoadGroups reads the group records (empty if none).
func (s *Store) LoadGroups() map[string]groupRec {
	g := map[string]groupRec{}
	_ = readJSON(s.groupsPath(), &g)
	if g == nil {
		g = map[string]groupRec{}
	}
	return g
}
