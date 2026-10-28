//go:build !windows

package herdrmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// execFunc runs a herdr binary and returns its stdout, stderr and exit code.
// err is non-nil only when the process could not be run at all.
type execFunc func(ctx context.Context, bin string, args []string, env []string) (stdout, stderr []byte, code int, err error)

// HerdrError is a structured error reported by the herdr CLI
// ({"error":{"code","message"}} on stderr).
type HerdrError struct {
	Code    string
	Message string
}

func (e *HerdrError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Client is a thin wrapper over the herdr CLI bound to one named herdr
// session. It always passes --session and scrubs HERDR_* from the child
// environment so a call can never reach the user's own herdr session.
type Client struct {
	Sock string // herdr session name
	// Shared is true when the herdr session is not dedicated to this socket
	// (town setting herdr_session, e.g. the user's own "default" session):
	// the server must never be stopped, only our workspaces closed.
	Shared bool
	Bin    string // herdr binary
	store  *Store
	exec   execFunc
	// spawn starts the detached headless server; nil means the real one.
	spawn func(ctx context.Context) error
}

// newClientFunc builds the Client for a socket name. Tests replace it.
var newClientFunc = func(sock string) *Client {
	sock = herdrSessionName(sock)
	target := HerdrSessionFor(sock)
	return &Client{Sock: target, Shared: target != sock, Bin: herdrBin(), store: NewStore(sock), exec: realExec}
}

func herdrBin() string {
	if b := os.Getenv("GT_HERDR_BIN"); b != "" {
		return b
	}
	return "herdr"
}

// scrubbedEnv returns the process environment without HERDR_* variables.
func scrubbedEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func realExec(ctx context.Context, bin string, args []string, env []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return so.Bytes(), se.Bytes(), ee.ExitCode(), nil
		}
		return nil, nil, -1, err
	}
	return so.Bytes(), se.Bytes(), 0, nil
}

// raw runs `herdr <args>` (no --session added) and returns stdout.
func (c *Client) raw(ctx context.Context, args ...string) ([]byte, error) {
	so, se, code, err := c.exec(ctx, c.Bin, args, scrubbedEnv())
	if err != nil {
		return nil, fmt.Errorf("herdr: %w", err)
	}
	if code != 0 {
		return so, parseHerdrError(se, so, code)
	}
	return so, nil
}

func parseHerdrError(stderr, stdout []byte, code int) error {
	for _, b := range [][]byte{stderr, stdout} {
		var e struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(b), &e) == nil && e.Error != nil {
			return &HerdrError{Code: e.Error.Code, Message: e.Error.Message}
		}
	}
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = fmt.Sprintf("herdr exited with status %d", code)
	}
	return &HerdrError{Message: msg}
}

// sessionRaw runs `herdr --session <sock> <args>` and returns stdout.
func (c *Client) sessionRaw(ctx context.Context, args ...string) ([]byte, error) {
	return c.raw(ctx, append([]string{"--session", c.Sock}, args...)...)
}

// call runs a control command and decodes the JSON .result into out
// (out may be nil).
func (c *Client) call(ctx context.Context, out any, args ...string) error {
	so, err := c.sessionRaw(ctx, args...)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(so), &env); err != nil {
		return fmt.Errorf("herdr %s: bad JSON: %w", strings.Join(args, " "), err)
	}
	return json.Unmarshal(env.Result, out)
}

// SessionInfo is one entry of `herdr session list --json`.
type SessionInfo struct {
	Name       string `json:"name"`
	Running    bool   `json:"running"`
	SocketPath string `json:"socket_path"`
}

// SessionStatus reports whether this client's named session has a running
// server, and its socket path (for error messages).
func (c *Client) SessionStatus(ctx context.Context) (running bool, socketPath string, err error) {
	so, err := c.raw(ctx, "session", "list", "--json")
	if err != nil {
		return false, "", err
	}
	var l struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(so), &l); err != nil {
		return false, "", fmt.Errorf("herdr session list: %w", err)
	}
	for _, s := range l.Sessions {
		if s.Name == c.Sock {
			return s.Running, s.SocketPath, nil
		}
	}
	return false, "", nil
}

// StartServer starts a detached headless herdr server for this session if it
// is not running, and waits (<=5s) until it answers.
func (c *Client) StartServer(ctx context.Context) error {
	running, _, err := c.SessionStatus(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	spawn := c.spawn
	if spawn == nil {
		spawn = c.spawnReal
	}
	if err := spawn(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := c.call(ctx, nil, "workspace", "list"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for herdr server to start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *Client) spawnReal(ctx context.Context) error {
	if err := os.MkdirAll(c.store.Dir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(c.store.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(c.Bin, "--session", c.Sock, "server")
	cmd.Env = scrubbedEnv()
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting herdr server: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// StopSession stops and deletes the named herdr session (kill-server).
func (c *Client) StopSession(ctx context.Context) error {
	_, _ = c.raw(ctx, "session", "stop", c.Sock, "--json")
	_, err := c.raw(ctx, "session", "delete", c.Sock, "--json")
	return err
}

// --- typed helpers -------------------------------------------------------

type wsInfo struct {
	ID        string `json:"workspace_id"`
	Label     string `json:"label"`
	Number    int    `json:"number"`
	Focused   bool   `json:"focused"`
	PaneCount int    `json:"pane_count"`
}

type paneInfo struct {
	ID            string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	Cwd           string `json:"cwd"`
	ForegroundCwd string `json:"foreground_cwd"`
	Focused       bool   `json:"focused"`
}

type procInfo struct {
	Argv    []string `json:"argv"`
	Cmdline string   `json:"cmdline"`
	Cwd     string   `json:"cwd"`
	Name    string   `json:"name"`
	PID     int      `json:"pid"`
}

func (c *Client) listWorkspaces(ctx context.Context) ([]wsInfo, error) {
	var r struct {
		Workspaces []wsInfo `json:"workspaces"`
	}
	if err := c.call(ctx, &r, "workspace", "list"); err != nil {
		return nil, err
	}
	return r.Workspaces, nil
}

// created is what workspace/tab/worktree creation reports.
type created struct {
	WS     wsInfo
	TabID  string
	PaneID string
}

type createResult struct {
	Workspace wsInfo  `json:"workspace"`
	Tab       tabInfo `json:"tab"`
	RootPane  struct {
		ID    string `json:"pane_id"`
		TabID string `json:"tab_id"`
		WSID  string `json:"workspace_id"`
	} `json:"root_pane"`
	AlreadyOpen bool `json:"already_open"`
}

func (r createResult) created() created {
	c := created{WS: r.Workspace, TabID: r.Tab.ID, PaneID: r.RootPane.ID}
	if c.TabID == "" {
		c.TabID = r.RootPane.TabID
	}
	if c.WS.ID == "" {
		c.WS.ID = r.RootPane.WSID
		if c.WS.ID == "" {
			c.WS.ID = r.Tab.WorkspaceID
		}
	}
	return c
}

func (c *Client) createWorkspace(ctx context.Context, label, cwd string, env map[string]string) (created, error) {
	args := []string{"workspace", "create", "--label", label, "--no-focus"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "--env", k+"="+env[k])
	}
	var r createResult
	if err := c.call(ctx, &r, args...); err != nil {
		return created{}, err
	}
	return r.created(), nil
}

type tabInfo struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	Focused     bool   `json:"focused"`
}

// listTabs lists the tabs of one workspace, or of every workspace when ws is empty.
func (c *Client) listTabs(ctx context.Context, ws string) ([]tabInfo, error) {
	args := []string{"tab", "list"}
	if ws != "" {
		args = append(args, "--workspace", ws)
	}
	var r struct {
		Tabs []tabInfo `json:"tabs"`
	}
	if err := c.call(ctx, &r, args...); err != nil {
		return nil, err
	}
	return r.Tabs, nil
}

func (c *Client) createTab(ctx context.Context, ws, label, cwd string) (created, error) {
	args := []string{"tab", "create", "--workspace", ws, "--label", label, "--no-focus"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	var r createResult
	if err := c.call(ctx, &r, args...); err != nil {
		return created{}, err
	}
	out := r.created()
	if out.WS.ID == "" {
		out.WS.ID = ws
	}
	return out, nil
}

// openWorktree opens an existing linked git worktree as its own workspace
// nested under ws. It never creates or removes worktrees on disk.
func (c *Client) openWorktree(ctx context.Context, ws, path, label string) (created, error) {
	var r createResult
	if err := c.call(ctx, &r, "worktree", "open", "--workspace", ws, "--path", path, "--label", label, "--no-focus"); err != nil {
		return created{}, err
	}
	return r.created(), nil
}

func (c *Client) closeTab(ctx context.Context, id string) error {
	_, err := c.sessionRaw(ctx, "tab", "close", id)
	return err
}

func (c *Client) renameTab(ctx context.Context, id, label string) error {
	_, err := c.sessionRaw(ctx, "tab", "rename", id, label)
	return err
}

func (c *Client) focusTab(ctx context.Context, id string) error {
	_, err := c.sessionRaw(ctx, "tab", "focus", id)
	return err
}

func (c *Client) listPanes(ctx context.Context, ws string) ([]paneInfo, error) {
	args := []string{"pane", "list"}
	if ws != "" {
		args = append(args, "--workspace", ws)
	}
	var r struct {
		Panes []paneInfo `json:"panes"`
	}
	if err := c.call(ctx, &r, args...); err != nil {
		return nil, err
	}
	return r.Panes, nil
}

func (c *Client) getPane(ctx context.Context, id string) (paneInfo, error) {
	var r struct {
		Pane paneInfo `json:"pane"`
	}
	if err := c.call(ctx, &r, "pane", "get", id); err != nil {
		return paneInfo{}, err
	}
	return r.Pane, nil
}

func (c *Client) processInfo(ctx context.Context, id string) ([]procInfo, int, error) {
	var r struct {
		ProcessInfo struct {
			Foreground []procInfo `json:"foreground_processes"`
			ShellPID   int        `json:"shell_pid"`
		} `json:"process_info"`
	}
	if err := c.call(ctx, &r, "pane", "process-info", "--pane", id); err != nil {
		return nil, 0, err
	}
	return r.ProcessInfo.Foreground, r.ProcessInfo.ShellPID, nil
}

func (c *Client) readPane(ctx context.Context, id, source string, lines int, ansi bool) (string, error) {
	args := []string{"pane", "read", id, "--source", source}
	if lines > 0 {
		args = append(args, "--lines", fmt.Sprint(lines))
	}
	if ansi {
		args = append(args, "--format", "ansi")
	}
	so, err := c.sessionRaw(ctx, args...)
	return string(so), err
}

func (c *Client) sendText(ctx context.Context, id, text string) error {
	_, err := c.sessionRaw(ctx, "pane", "send-text", id, text)
	return err
}

func (c *Client) sendKeys(ctx context.Context, id string, keys ...string) error {
	_, err := c.sessionRaw(ctx, append([]string{"pane", "send-keys", id}, keys...)...)
	return err
}

func (c *Client) runInPane(ctx context.Context, id, cmd string) error {
	_, err := c.sessionRaw(ctx, "pane", "run", id, cmd)
	return err
}

func (c *Client) closeWorkspace(ctx context.Context, id string) error {
	_, err := c.sessionRaw(ctx, "workspace", "close", id)
	return err
}

func (c *Client) renameWorkspace(ctx context.Context, id, label string) error {
	_, err := c.sessionRaw(ctx, "workspace", "rename", id, label)
	return err
}

func (c *Client) focusWorkspace(ctx context.Context, id string) error {
	_, err := c.sessionRaw(ctx, "workspace", "focus", id)
	return err
}

func isNotFound(err error) bool {
	var he *HerdrError
	if errors.As(err, &he) {
		return strings.Contains(he.Code, "not_found") || strings.Contains(he.Message, "not found")
	}
	return false
}
