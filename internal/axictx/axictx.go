// Package axictx collects the session-start context of the AXI tools a town
// lists in axi_tools, so gt prime can give it to every agent runtime instead of
// each tool installing its own per-runtime hook.
//
// Only tools in sessionViews are used. A tool is listed there only when it
// ships a session-start hook, and the command is what that hook runs:
//
//   - gh-axi and az-axi: `<tool> setup hooks` installs a SessionStart hook
//     (Claude Code, Codex) and an OpenCode plugin whose command is the bare
//     binary name, i.e. the tool's home view with no arguments
//     (axi-sdk-js installSessionStartHooks, resolvePortableHookCommand).
//   - lavish-axi and quota-axi have no `setup hooks` and no session view
//     (lavish-axi with no arguments prints its usage guide, quota-axi asks the
//     vendor), so they are not in the table.
//
// Tool output is data. It is capped, stripped of control characters, and never
// interpreted. Collect never returns an error: a failing tool yields a result
// with Err set and no text.
package axictx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
)

const (
	// DefaultTimeout bounds one tool's run.
	DefaultTimeout = 5 * time.Second
	// DefaultTTL is how long a cached view is reused.
	DefaultTTL = 10 * time.Minute
	// DefaultMaxBytes caps the text kept per tool.
	DefaultMaxBytes = 1500

	truncatedNote = "(truncated)"
)

// sessionViews maps a tool name to the arguments of its session-start context
// command. Only verified tools belong here.
var sessionViews = map[string][]string{
	"gh-axi": nil,
	"az-axi": nil,
}

// HasSessionView reports whether tool has a known session-start command.
func HasSessionView(tool string) bool {
	_, ok := sessionViews[tool]
	return ok
}

// Runner runs bin with args in dir with env and returns its stdout.
type Runner func(ctx context.Context, dir string, env []string, bin string, args ...string) ([]byte, error)

// Options configure Collect. Zero values take the defaults.
type Options struct {
	TownRoot string
	// Tools are axi_tools entries ("name" or "name@version"), in print order.
	Tools []string
	// Env is the full environment for the tools (org env already applied).
	Env      []string
	Timeout  time.Duration
	TTL      time.Duration
	MaxBytes int
	// Run and Now are seams for tests.
	Run Runner
	Now func() time.Time
}

// Result is one tool's outcome. Text is empty when the tool printed nothing or
// failed; Err is set only on failure.
type Result struct {
	Tool string
	Text string
	Err  error
}

func defaultRunner(ctx context.Context, dir string, env []string, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A grandchild that keeps the pipe open must not hold us past the timeout.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return stdout.Bytes(), err
}

// CacheDir is where views are cached for a town.
func CacheDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "axi-context")
}

// Collect runs the context command of each tool in opts.Tools that has one,
// in parallel, and returns the results in list order. It returns nothing when
// no listed tool has a session view.
func Collect(ctx context.Context, opts Options) []Result {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Run == nil {
		opts.Run = defaultRunner
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	var tools []string
	seen := map[string]bool{}
	for _, entry := range opts.Tools {
		name, _ := config.ParseAxiTool(entry)
		if !HasSessionView(name) || seen[name] {
			continue
		}
		seen[name] = true
		tools = append(tools, name)
	}
	if len(tools) == 0 {
		return nil
	}

	results := make([]Result, len(tools))
	var wg sync.WaitGroup
	for i, tool := range tools {
		wg.Add(1)
		go func(i int, tool string) {
			defer wg.Done()
			results[i] = collectOne(ctx, opts, tool)
		}(i, tool)
	}
	wg.Wait()
	return results
}

func collectOne(ctx context.Context, opts Options, tool string) Result {
	cachePath := filepath.Join(CacheDir(opts.TownRoot), tool+".txt")
	if text, ok := readFresh(cachePath, opts.Now(), opts.TTL); ok {
		return Result{Tool: tool, Text: text}
	}

	bin, ok := lookPath(tool, envValue(opts.Env, "PATH"))
	if !ok {
		return Result{Tool: tool, Err: errors.New("not on PATH")}
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	out, err := opts.Run(cctx, opts.TownRoot, opts.Env, bin, sessionViews[tool]...)
	if cctx.Err() != nil {
		return Result{Tool: tool, Err: fmt.Errorf("timed out after %s", opts.Timeout)}
	}
	if err != nil {
		return Result{Tool: tool, Err: errors.New("failed")}
	}

	text := capText(sanitize(string(out)), opts.MaxBytes)
	// Best effort: a cache write failure only costs a re-run next time.
	if err := os.MkdirAll(CacheDir(opts.TownRoot), 0o700); err == nil {
		_ = atomicfile.WriteFile(cachePath, []byte(text), 0o600)
	}
	return Result{Tool: tool, Text: text}
}

// readFresh returns the cached text when the file is younger than ttl.
func readFresh(path string, now time.Time, ttl time.Duration) (string, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	age := now.Sub(st.ModTime())
	if age < 0 || age >= ttl {
		return "", false
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is under the town runtime dir
	if err != nil {
		return "", false
	}
	return string(data), true
}

// sanitize drops control characters (including ESC, so no terminal escape
// sequences) other than newline and tab, and invalid UTF-8.
func sanitize(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// capText limits s to max bytes, cutting at a line boundary and appending a
// truncation note. A single first line longer than max is cut at a rune
// boundary.
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max+1], "\n")
	var head string
	if cut > 0 {
		head = s[:cut]
	} else {
		n := max
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		head = s[:n]
	}
	head = strings.TrimRight(head, " \t\n")
	if head == "" {
		return truncatedNote
	}
	return head + "\n" + truncatedNote
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func lookPath(name, pathVar string) (string, bool) {
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}
