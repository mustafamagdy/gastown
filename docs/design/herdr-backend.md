# Herdr multiplexer backend

Status: in progress (fork `mustafamagdy/gastown`, branch `herdr-backend`)

## Goal

Let a town run every agent session under [herdr](https://herdr.dev) instead of
tmux, selected by configuration. tmux remains the default and its behaviour
must not change.

## Approach: tmux-compatible emulation

Gas Town talks to tmux almost exclusively through tmux CLI argv, built in one
place (`(*tmux.Tmux).commandContext` / `tmux.BuildCommandContext`), plus a few
shell formulas/plugins that call `tmux` directly. The surface actually used is
small (~25 subcommands, ~25 format variables). So instead of rewriting the
~150 methods of `*tmux.Tmux` and ~176 call sites, the herdr backend is a
**tmux CLI emulator**: it accepts the same argv gastown would pass to `tmux`
and carries it out against a herdr server, writing stdout/stderr and exit codes
the way tmux would (including the stderr phrases `wrapError` matches:
`no server running`, `duplicate session`, `can't find session`).

This keeps upstream-merge conflicts small: the existing tmux code paths are
untouched; the switch happens at the argv boundary.

```
*tmux.Tmux method ──argv──► commandContext ─┬─ backend=tmux  ─► exec tmux -u -L sock …
                                            └─ backend=herdr ─► exec gt mux-tmux -L sock …
shell formulas/plugins ─► `tmux` on PATH ───────► shim script ─► gt mux-tmux …
                                                                   │
                                          internal/herdrmux.Run(argv)
                                                                   │
                                     herdr --session <sock> <api cmd> (JSON)
```

## Components

### 1. Backend selection — `internal/tmux/backend.go`

```go
type Backend string
const (BackendTmux Backend = "tmux"; BackendHerdr Backend = "herdr")
func SetBackend(Backend); func GetBackend() Backend   // mutex-guarded like defaultSocket
func IsHerdr() bool
```

Resolution (in `session.InitRegistry`, before `SetDefaultSocket`):
1. env `GT_MULTIPLEXER` (`tmux`|`herdr`) wins;
2. else town settings `settings/config.json` field `"multiplexer"`;
3. else `tmux`.

`InitRegistry` also exports `GT_MULTIPLEXER=<resolved>` into the process env so
children (daemon-spawned processes, `gt` invoked from panes) agree.

Field: `TownSettings.Multiplexer string \`json:"multiplexer,omitempty"\`` in
`internal/config/types.go`.

### 2. argv switch — `internal/tmux/tmux.go`

`commandContext` and `BuildCommandContext`: when `IsHerdr()`, build
`exec.CommandContext(ctx, gtExecutable(), "mux-tmux", <same args incl. -u -L sock>…)`
instead of `tmux`. `gtExecutable()` = `os.Executable()`, overridable with env
`GT_BIN` (tests). Nothing else in the `Tmux` methods changes.

Direct `exec.Command("tmux", …)` / `exec.LookPath("tmux")` call sites outside
the package are converted to `tmux.BuildCommand(…)` (which is also correct for
tmux: it applies the town socket). Interactive ones (attach/switch-client via
`syscall.Exec`) go through a new `tmux.ExecArgv(args) (path string, argv []string)`
helper that returns the right binary + argv for the backend.

### 3. Emulator — `internal/herdrmux`

`func Run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int`

`argv` is tmux argv *without* the leading `tmux` (e.g. `-u -L gt-ab12cd has-session -t =hq-mayor`).
Global flags: `-u` (ignored), `-L name` (herdr session name), `-S path`
(socket path → basename used as name), `-V` (prints `tmux 3.4 (herdr <ver>)`).

**Mapping model**

| tmux | herdr |
|------|-------|
| server / socket `-L X` | herdr named session `X` (`herdr --session X …`), headless server auto-started on `new-session` |
| session `name` | workspace whose **label** == `name` (unlabeled / foreign workspaces are ignored) |
| window 0 / pane 0 | the workspace's root pane (gastown uses one pane per session) |
| `pane_id` `%…` | `%` + herdr pane id with `:` → `.` (e.g. `w2:p1` → `%w2.p1`) |

Targets accepted for `-t`: `name`, `=name`, `name:`, `name:0`, `name:0.0`,
`%paneid`, and empty (= current pane via `TMUX_PANE`/`HERDR_PANE_ID`).

**Herdr client** (`client.go`): thin wrapper over the `herdr` CLI
(`herdr --session <name> <group> <cmd> …`), parses JSON results / JSON errors
(stderr, exit 1). **Always pass `--session`** and scrub inherited
`HERDR_SOCKET_PATH`/`HERDR_*` from the child env so a call can never land on
the user's own herdr session. Binary from env `GT_HERDR_BIN` or `herdr` on PATH.
Server autostart: if `herdr session list --json` shows the session not
running, start `herdr --session X server` detached (setsid, stdio → log file
under the state dir) and poll until it answers (≤5s).

**State store** (`state.go`): herdr has no session environment, options,
hooks or creation time, so the emulator keeps them in
`$XDG_STATE_HOME/gt-herdr/<sock>/` (default `~/.local/state/gt-herdr/<sock>/`):
- `sessions/<name>.json`: `{created, activity, cwd, env{}, options{}, hooks{}, command, pane_pid, child_pid, dead, dead_status, herdr_pane_id}`
- `global.json`: global env + global options.
Writes are atomic (temp+rename) and guarded by a flock per file.

**Supervisor** (`supervise.go`, invoked as `gt mux-supervise --sock S --session N`):
the process that runs inside the herdr pane, giving tmux-like pane semantics
herdr lacks:
- reads command/cwd/env from the session state, starts the child in its own
  process group as the terminal foreground group (so Ctrl-C reaches it and
  herdr `process-info` reports it as the foreground process);
- child env = session env + `TMUX=/herdr/<sock>,<supervisorpid>,0`,
  `TMUX_PANE=%<pane>`, `GT_MULTIPLEXER=herdr`, and PATH prefixed with the shim
  dir (component 5) — so existing `TMUX`/`TMUX_PANE` readers keep working;
- records `pane_pid` (= child pid, matching tmux where pane_pid is the pane's
  process) in state;
- on child exit: records `dead=true, dead_status`; runs the `pane-died` hook
  (stored by `set-hook`) via `sh -c`; if option `remain-on-exit` is on, stays
  alive showing `Pane is dead (status N)` and waits; else exits (herdr then
  closes the pane / workspace, matching tmux session teardown);
- on SIGUSR1 (sent by `respawn-pane`): re-reads the spec from state, kills the
  current child's process group (if `-k`), and starts the new command in the
  same pane. This makes `respawn-pane` atomic even when called from inside the
  pane being respawned.

**Commands to emulate** (everything else: exit 0, no output, unless noted)

| tmux command | behaviour |
|---|---|
| `-V` | version line |
| `new-session -d -s N [-c dir] [-e K=V]… [-x/-y …] [cmd…]` | `duplicate session` if exists; else autostart server, `workspace create --label N --cwd dir --no-focus`, write state (env, cmd = cmd or `$SHELL`), `pane run <pane> "exec <gt> mux-supervise …"`, wait until state has `pane_pid` (≤5s) |
| `has-session -t T` | exit 0/1 (`can't find session: T`) |
| `kill-session -t T` | `workspace close`, delete state |
| `kill-server` | `herdr session stop <sock>` + remove state dir |
| `list-sessions [-F fmt]` | one line per labeled workspace; no server → stderr `no server running on …`, exit 1 |
| `list-panes [-a] [-s] [-t T] [-F fmt]` | one pane per session |
| `list-windows [-t T] [-F fmt]` | one window (`0`) per session |
| `display-message -p [-t T] fmt` | render fmt; without `-p` → no-op |
| `send-keys [-t T] [-l] [-H] keys…` | `-l`: `pane send-text`; else map key names (`Enter`/`C-m`→`enter`, `Escape`→`esc`, `C-c`→`ctrl+c`, `C-x`→`ctrl+x`, `Up`… , `BSpace`→`backspace`, `Tab`, `Space`); unknown words are sent as literal text; touches `activity` |
| `capture-pane -p [-t T] [-S n] [-E n] [-J] [-e]` | `pane read --source recent[-unwrapped] --lines N [--format ansi]`; `-S -` → large N |
| `clear-history` | no-op |
| `set-environment [-g] [-u] [-t T] K [V]` | state env set/unset |
| `show-environment [-g] [-t T] [K]` | `K=V` lines (`-K` for unset); missing K → exit 1 `unknown variable: K` |
| `set-option`/`set-window-option`/`set -g` | store in state options |
| `show-options [-g] [-v] [-t T] [opt]` | from state |
| `set-hook [-g] [-u] [-t T] name [cmd]` | store; only `pane-died` / `after-*respawn*` are acted on (by the supervisor) |
| `respawn-pane [-k] [-t T] [-c dir] [-e K=V]… [cmd…]` | update spec in state, SIGUSR1 the supervisor; if supervisor gone, recreate pane |
| `rename-session -t T new` | `workspace rename` + move state file |
| `attach-session -t T` / `switch-client -t T` | `workspace focus <ws>` then (attach only) exec interactive `herdr --session S` with stdio inherited |
| `select-window`, `resize-window`, `bind-key`, `unbind-key`, `display-menu`, `display-popup`, `source-file`, `refresh-client`, `reopen`… | exit 0 no-op |
| `list-keys` | no output, exit 0 |

**Format variables** (`format.go`): render `#{name}` and the
`#{==:#{a},b}` / `#{?cond,a,b}` forms actually used. Values:
`session_name`, `session_id` (`$`+workspace number), `session_created`,
`session_activity`, `session_last_attached`, `session_attached` (1 if focused
by a client else 0), `session_windows` (1), `window_index` (0),
`window_name` (session name), `window_activity`, `window_width` (80),
`pane_id`, `pane_index` (0), `pane_pid`, `pane_current_command` (name of the
foreground process from `pane process-info`, ignoring the supervisor),
`pane_current_path` (foreground cwd), `pane_dead` (0/1), `pane_dead_status`,
`pane_start_command`, `pane_in_mode` (0), `client_tty` (empty), `pid`
(server pid if known else 0). Unknown variables render as empty string.

### 4. CLI entry points — `internal/cmd/mux.go`

Hidden cobra commands, **registered so they skip town discovery / heavy
PersistentPreRun** (they must be fast and work from any cwd):
- `gt mux-tmux <tmux argv…>` → `os.Exit(herdrmux.Run(...))` (DisableFlagParsing).
- `gt mux-supervise --sock S --session N`.
- `gt mux-shim-path` → prints the shim dir (creating it if needed).

### 5. Shim for non-Go callers

`herdrmux.EnsureShim() (dir string, error)` writes
`$XDG_STATE_HOME/gt-herdr/bin/tmux`:
```sh
#!/bin/sh
[ -n "$GT_REAL_TMUX" ] && exec "$GT_REAL_TMUX" "$@"
exec <abs gt path> mux-tmux "$@"
```
In herdr mode, `InitRegistry` prepends this dir to the process `PATH` (so the
daemon, plugins, formulas and dogs it spawns see it), and the supervisor
prepends it for agent panes. Formulas/plugins therefore need no edits.

### 6. Rest of the codebase

- Doctor: tmux-only checks (`tmux-global-env`, `linked-panes`,
  `socket-split-brain`, `themes`) report OK/skipped under herdr; add a
  `herdr` check (binary present, server reachable, protocol version).
- Web dashboard (`internal/web`): route its tmux execs through
  `tmux.BuildCommand` so it follows the backend.
- Themes / status line / key bindings: no-ops under herdr (herdr has its own
  UI and keybindings).

## Safety rules for development

- Never run commands against the user's default herdr session. Tests and
  experiments use throwaway named sessions (`herdr --session gt-test-<rand>`)
  and delete them afterwards (`herdr session stop X; herdr session delete X`).
- Never `herdr server stop` without `--session`.
- Unit tests must not require herdr; use a fake `herdr` binary
  (`GT_HERDR_BIN`) or an interface seam. Integration tests that need real herdr
  skip when `herdr` is not on PATH.
