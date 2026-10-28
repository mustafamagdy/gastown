//go:build !windows

package herdrmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runShellTimeout bounds a foreground run-shell, like the supervisor's hook timeout.
const runShellTimeout = 30 * time.Second

// RunHook executes a stored tmux hook command (e.g. the value of a pane-died
// hook) for the given session. Hooks are tmux command lines, so they are
// parsed and dispatched through the emulator; run-shell inside a hook
// targets the hook's session, as in tmux.
func RunHook(ctx context.Context, sock, session, hook string, stdout, stderr io.Writer) error {
	args, err := splitCommandString(hook)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return nil
	}
	if a, ok := aliases[args[0]]; ok {
		args[0] = a
	}
	if args[0] == "run-shell" && !hasFlag(args[1:], "-t") {
		args = append([]string{"run-shell", "-t", session}, args[1:]...)
	}
	argv := append([]string{"-L", sock}, args...)
	if code := Run(ctx, argv, nil, stdout, stderr); code != 0 {
		return fmt.Errorf("hook %q exited %d", args[0], code)
	}
	return nil
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return false
}

// cmdRunShell implements run-shell [-b] [-t target] command: the command's
// formats are expanded for the target session, then it runs under /bin/sh
// with the tmux shim on PATH so nested `tmux` calls reach the emulator.
func (m *mux) cmdRunShell(args []string) error {
	f, err := parseFlags("run-shell", args, "bCE", "dct")
	if err != nil {
		return err
	}
	if len(f.rest) == 0 {
		return nil
	}
	var s *session
	if t := f.val('t'); t != "" {
		if s, err = m.findSession(t); err != nil {
			// Hooks fire while the pane is going away; the stored state
			// is enough to expand #{pane_dead_status} and friends.
			st, lerr := m.store.Load(strings.TrimPrefix(t, "="))
			if lerr != nil {
				return err
			}
			s = &session{Name: st.Name, State: st}
		}
	}
	command := m.render(strings.Join(f.rest, " "), s)

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = m.runShellEnv()
	if s != nil && s.State.Cwd != "" {
		cmd.Dir = s.State.Cwd
	}
	if c := f.val('c'); c != "" {
		cmd.Dir = c
	}
	if f.has('b') {
		detach(cmd)
		return cmd.Start()
	}
	ctx, cancel := context.WithTimeout(m.ctx, runShellTimeout)
	defer cancel()
	cmd.Stdout, cmd.Stderr = m.out, m.errw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return cmdErrf("'%s' returned %v", command, err)
		}
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return cmdErrf("'%s' timed out", command)
	}
}

// runShellEnv is the environment for run-shell: ours, with the tmux shim
// first on PATH and TMUX pointing at this socket.
func (m *mux) runShellEnv() []string {
	env := scrubbedEnv()
	if dir, err := EnsureShim(); err == nil {
		env = setEnv(env, "PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return setEnv(env, "TMUX", fmt.Sprintf("/herdr/%s,%d,0", m.sock, os.Getpid()))
}

func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+val)
}

// splitCommandString splits a tmux command line into words, following tmux's
// quoting: single quotes are literal, double quotes allow \" \\ \$ escapes,
// a backslash outside quotes escapes the next character.
func splitCommandString(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`, s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			cur.WriteByte(s[i])
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
