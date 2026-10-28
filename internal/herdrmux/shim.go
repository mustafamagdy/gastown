package herdrmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// shimScript is the `tmux` shim: GT_REAL_TMUX escapes to a real tmux, anything
// else goes to the emulator.
func shimScript(gtPath string) string {
	return fmt.Sprintf("#!/bin/sh\n[ -n \"$GT_REAL_TMUX\" ] && exec \"$GT_REAL_TMUX\" \"$@\"\nexec %s mux-tmux \"$@\"\n", shellQuote(gtPath))
}

// shimGtPath returns the absolute path of the gt binary: $GT_BIN if set,
// else the running executable with symlinks resolved.
func shimGtPath() (string, error) {
	if p := os.Getenv("GT_BIN"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// EnsureShim writes the `tmux` shim script that forwards to `gt mux-tmux` and
// returns the directory containing it (to be prepended to PATH). The script is
// only rewritten when its content changes.
func EnsureShim() (string, error) {
	gt, err := shimGtPath()
	if err != nil {
		return "", fmt.Errorf("locating gt executable: %w", err)
	}
	dir := filepath.Join(StateRoot(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "tmux")
	want := shimScript(gt)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == 0o755 {
			return dir, nil
		}
	}
	tmp, err := os.CreateTemp(dir, ".tmux.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(want); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}

// shellQuote quotes s for a POSIX shell if needed.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("_@%+=:,./-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
