package herdrmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureShim(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GT_HERDR_STATE_DIR", root)
	t.Setenv("GT_BIN", "/opt/it's/gt")

	dir, err := EnsureShim()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "bin"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	path := filepath.Join(dir, "tmux")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"#!/bin/sh\n", `GT_REAL_TMUX`, `'/opt/it'\''s/gt' mux-tmux "$@"`} {
		if !strings.Contains(s, want) {
			t.Errorf("shim missing %q:\n%s", want, s)
		}
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}

	// Idempotent: unchanged content is not rewritten.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureShim(); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(path)
	if fi2.ModTime().After(old.Add(time.Minute)) {
		t.Errorf("shim rewritten although unchanged (mtime %v)", fi2.ModTime())
	}

	// Changed gt path: rewritten.
	t.Setenv("GT_BIN", "/other/gt")
	if _, err := EnsureShim(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "exec /other/gt mux-tmux") {
		t.Errorf("shim not updated:\n%s", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover temp files in shim dir: %v", entries)
	}
}

func TestEnsureShimDefaultsToExecutable(t *testing.T) {
	t.Setenv("GT_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("GT_BIN", "")
	dir, err := EnsureShim()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "tmux"))
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if !strings.Contains(string(data), exe) {
		t.Errorf("shim does not reference %s:\n%s", exe, data)
	}
}
