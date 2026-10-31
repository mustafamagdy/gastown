//go:build !windows

// Package herdrmux emulates the subset of the tmux CLI that Gas Town uses,
// carrying each command out against a herdr server. See
// docs/design/herdr-backend.md.
package herdrmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cmdError is an error printed verbatim to stderr (as tmux would) with exit 1.
type cmdError struct{ msg string }

func (e *cmdError) Error() string { return e.msg }

func cmdErrf(format string, a ...any) error { return &cmdError{msg: fmt.Sprintf(format, a...)} }

// mux is the per-invocation emulator state.
type mux struct {
	ctx    context.Context
	stdin  io.Reader
	out    io.Writer
	errw   io.Writer
	sock   string
	cl     *Client
	store  *Store
	probed bool
	up     bool
	sockP  string // herdr socket path, for error messages
}

// Run executes a tmux command line (argv without the leading "tmux") against
// herdr and returns the process exit code tmux would have produced.
func Run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sock := ""
	i := 0
	version := false
loop:
	for ; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "-u", "-2", "-C", "-CC", "-D", "-l", "-N", "-v", "-vv":
		case "-V":
			version = true
		case "-L", "-S", "-f", "-c", "-T":
			if i+1 >= len(argv) {
				fmt.Fprintf(stderr, "option requires an argument -- %s\n", a[1:])
				return 1
			}
			i++
			switch a {
			case "-L":
				sock = argv[i]
			case "-S":
				sock = filepath.Base(argv[i])
			}
		default:
			break loop
		}
	}
	if sock == "" {
		sock = sockFromTMUXEnv(os.Getenv("TMUX"))
	}
	sock = herdrSessionName(sock)
	m := &mux{ctx: ctx, stdin: stdin, out: stdout, errw: stderr, sock: sock}
	m.cl = newClientFunc(sock)
	m.store = m.cl.store
	if m.store == nil {
		m.store = NewStore(sock)
	}
	if version {
		fmt.Fprintf(stdout, "tmux 3.4 (herdr %s)\n", m.herdrVersion())
		return 0
	}
	if i >= len(argv) {
		return 0
	}
	name, args := argv[i], argv[i+1:]
	if err := m.dispatch(name, args); err != nil {
		var ce *cmdError
		if errors.As(err, &ce) {
			fmt.Fprintln(stderr, ce.msg)
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		return 1
	}
	return 0
}

// defaultSock is the herdr session used when no socket is given. It must never
// be herdr's own "default" session, which belongs to the user.
const defaultSock = "gt-default"

// herdrSessionName maps a tmux socket name to the herdr session that backs it.
func herdrSessionName(sock string) string {
	if sock == "" || sock == "default" {
		return defaultSock
	}
	return sock
}

// sockFromTMUXEnv extracts the socket name from TMUX=/herdr/<sock>,pid,0.
func sockFromTMUXEnv(v string) string {
	const prefix = "/herdr/"
	if !strings.HasPrefix(v, prefix) {
		return ""
	}
	v = v[len(prefix):]
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return v
}

func (m *mux) herdrVersion() string {
	so, err := m.cl.raw(m.ctx, "--version")
	if err != nil {
		return "unknown"
	}
	f := strings.Fields(string(so))
	if len(f) == 0 {
		return "unknown"
	}
	return f[len(f)-1]
}

// --- flag parsing --------------------------------------------------------

type flagSet struct {
	bools map[byte]int
	vals  map[byte][]string
	rest  []string
}

func (f *flagSet) has(c byte) bool { return f.bools[c] > 0 }

func (f *flagSet) val(c byte) string {
	v := f.vals[c]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// parseFlags parses getopt-style flags: boolChars take no argument, valChars
// take one. Parsing stops at the first non-flag argument or "--".
func parseFlags(cmd string, args []string, boolChars, valChars string) (*flagSet, error) {
	f := &flagSet{bools: map[byte]int{}, vals: map[byte][]string{}}
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			switch {
			case strings.IndexByte(valChars, c) >= 0:
				var v string
				if j+1 < len(a) {
					v = a[j+1:]
				} else if i+1 < len(args) {
					i++
					v = args[i]
				} else {
					return nil, cmdErrf("command %s: option requires an argument -- %c", cmd, c)
				}
				f.vals[c] = append(f.vals[c], v)
				j = len(a)
			case strings.IndexByte(boolChars, c) >= 0:
				f.bools[c]++
			default:
				return nil, cmdErrf("command %s: unknown flag -%c", cmd, c)
			}
		}
	}
	f.rest = args[i:]
	return f, nil
}

// --- helpers -------------------------------------------------------------

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinCommand joins tmux command argv into one shell command line: a single
// argument is used verbatim, several are shell-quoted and joined.
func joinCommand(args []string) string {
	switch len(args) {
	case 0:
		return ""
	case 1:
		return args[0]
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// defaultShellCommand is the command for a pane created without one.
func defaultShellCommand() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return "exec " + shellQuote(sh) + " -l"
}

// gtExecutable returns the gt binary: $GT_BIN, else os.Executable().
func gtExecutable() string {
	if b := os.Getenv("GT_BIN"); b != "" && !strings.HasSuffix(b, ".test") {
		return b
	}
	// Under go test the running binary is the test binary. Never use it.
	if p, err := os.Executable(); err == nil && !strings.HasSuffix(p, ".test") {
		return p
	}
	return "gt"
}
