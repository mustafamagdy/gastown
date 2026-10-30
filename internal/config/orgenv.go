package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// OrgEnvConfig declares where a town's org environment comes from. Exactly one
// of Direnv or File must be set.
//
// The org environment is how every agent of the town acts as the org's
// accounts (GitHub user, Azure DevOps PAT, ...) instead of the maintainer's
// ambient shell.
type OrgEnvConfig struct {
	// Direnv is a directory whose .envrc supplies the environment. The env is
	// the output of `direnv exec <dir> env -0`. May use "~".
	Direnv string `json:"direnv,omitempty"`

	// File is a dotenv file (KEY=VALUE lines). May use "~".
	File string `json:"file,omitempty"`

	// Vars is an optional allow-list of variable names to take from the
	// source. When empty, every variable that differs from gt's own process
	// environment is taken (minus the protected identity/session variables).
	Vars []string `json:"vars,omitempty"`
}

// Validate checks that exactly one source is configured.
func (c *OrgEnvConfig) Validate() error {
	switch {
	case c.Direnv == "" && c.File == "":
		return errors.New("org_env needs one of \"direnv\" or \"file\"")
	case c.Direnv != "" && c.File != "":
		return errors.New("org_env takes only one of \"direnv\" or \"file\", not both")
	}
	return nil
}

// OrgEnv is a resolved org environment.
type OrgEnv struct {
	// Source describes where the env came from, e.g. "direnv:/home/u/work/mbg".
	Source string
	// Vars are the variables to apply on top of an agent's env. PATH, when
	// present, is already merged (new entries prepended, none dropped).
	Vars map[string]string
	// File is the private shell file the vars were written to by
	// OrgEnvForTown (empty when not written). Agent startup commands source
	// it instead of carrying the secret values on the command line.
	File string
}

// Names returns the variable names, sorted. Values are never exposed this way.
func (o *OrgEnv) Names() []string {
	names := make([]string, 0, len(o.Vars))
	for k := range o.Vars {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Environ overlays the org env on a KEY=VALUE environment list. PATH in the
// org env wins (it is already merged against gt's PATH).
func (o *OrgEnv) Environ(base []string) []string {
	if o == nil || len(o.Vars) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(o.Vars))
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := o.Vars[k]; ok {
			continue
		}
		out = append(out, e)
	}
	for _, k := range o.Names() {
		out = append(out, k+"="+o.Vars[k])
	}
	return out
}

// orgEnvProtectedPrefixes and orgEnvProtectedNames are the identity/session
// variables gt sets itself; an org env never overrides them.
var (
	orgEnvProtectedPrefixes = []string{"GT_", "BD_", "BEADS_", "TMUX", "HERDR_"}
	orgEnvProtectedNames    = map[string]bool{
		"CLAUDE_CONFIG_DIR": true, "HOME": true, "USER": true,
		"PWD": true, "SHLVL": true, "_": true, "OLDPWD": true,
	}
)

// IsOrgEnvProtected reports whether an org env may not set the variable.
func IsOrgEnvProtected(name string) bool {
	if orgEnvProtectedNames[name] {
		return true
	}
	for _, p := range orgEnvProtectedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ParseDotenv parses dotenv content: KEY=VALUE lines, "#" comments, optional
// "export " prefix, matching single or double quotes stripped. No variable
// interpolation or escape processing. Malformed lines are skipped.
func ParseDotenv(content string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		out[key] = unquoteDotenv(strings.TrimSpace(val))
	}
	return out
}

func unquoteDotenv(v string) string {
	if len(v) >= 2 {
		if q := v[0]; (q == '"' || q == '\'') && v[len(v)-1] == q {
			return v[1 : len(v)-1]
		}
	}
	// Unquoted: a trailing " #comment" is not part of the value.
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// parseEnvNul parses `env -0` output (NUL separated KEY=VALUE).
func parseEnvNul(data []byte) map[string]string {
	out := make(map[string]string)
	for _, rec := range bytes.Split(data, []byte{0}) {
		k, v, ok := strings.Cut(string(rec), "=")
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// direnvTimeout bounds `direnv exec`; a first load can run slow hooks.
const direnvTimeout = 30 * time.Second

func loadDirenv(dir string) (map[string]string, error) {
	bin, err := exec.LookPath("direnv")
	if err != nil {
		return nil, fmt.Errorf("direnv not found on PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), direnvTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec", dir, "env", "-0")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg != "" {
			return nil, fmt.Errorf("direnv exec %s: %w: %s", dir, err, msg)
		}
		return nil, fmt.Errorf("direnv exec %s: %w", dir, err)
	}
	return parseEnvNul(out), nil
}

// mergePath returns base with the entries of org that base lacks prepended
// (in org order). Nothing from base is ever dropped.
func mergePath(base, org string) string {
	have := make(map[string]bool)
	for _, p := range filepath.SplitList(base) {
		have[p] = true
	}
	var added []string
	for _, p := range filepath.SplitList(org) {
		if p != "" && !have[p] {
			have[p] = true
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return base
	}
	if base == "" {
		return strings.Join(added, string(os.PathListSeparator))
	}
	return strings.Join(added, string(os.PathListSeparator)) + string(os.PathListSeparator) + base
}

// ResolveOrgEnvConfig resolves cfg against base (gt's own KEY=VALUE env).
// It does not cache; see OrgEnvForTown for the per-process cached lookup.
func ResolveOrgEnvConfig(cfg *OrgEnvConfig, base []string) (*OrgEnv, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var (
		raw    map[string]string
		source string
		err    error
	)
	if cfg.Direnv != "" {
		dir := expandHome(cfg.Direnv)
		source = "direnv:" + dir
		raw, err = loadDirenv(dir)
	} else {
		path := expandHome(cfg.File)
		source = "file:" + path
		var data []byte
		data, err = os.ReadFile(path) //nolint:gosec // G304: path comes from town settings
		if err == nil {
			raw = ParseDotenv(string(data))
		}
	}
	if err != nil {
		return nil, err
	}

	baseMap := make(map[string]string, len(base))
	for _, e := range base {
		if k, v, ok := strings.Cut(e, "="); ok {
			baseMap[k] = v
		}
	}

	allow := make(map[string]bool, len(cfg.Vars))
	for _, v := range cfg.Vars {
		allow[v] = true
	}

	vars := make(map[string]string)
	for k, v := range raw {
		if IsOrgEnvProtected(k) {
			continue
		}
		if len(allow) > 0 && !allow[k] {
			continue
		}
		if k == "PATH" {
			if merged := mergePath(baseMap["PATH"], v); merged != baseMap["PATH"] {
				vars["PATH"] = merged
			}
			continue
		}
		if len(allow) == 0 {
			if bv, ok := baseMap[k]; ok && bv == v {
				continue
			}
		}
		vars[k] = v
	}
	return &OrgEnv{Source: source, Vars: vars}, nil
}

// LoadOrgEnv resolves the town's org env from its settings, uncached.
// Returns (nil, nil) when the town has no org_env configured.
func LoadOrgEnv(townRoot string) (*OrgEnv, error) {
	settings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
	if err != nil {
		return nil, fmt.Errorf("loading town settings: %w", err)
	}
	if settings.OrgEnv == nil {
		return nil, nil
	}
	return ResolveOrgEnvConfig(settings.OrgEnv, os.Environ())
}

// OrgEnvFilePath is where a town's resolved org env is written for agents to
// source. It lives under .runtime/, which is gitignored.
func OrgEnvFilePath(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "org-env.sh")
}

var shellVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellSingleQuote quotes s for POSIX sh, always with single quotes.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderOrgEnvFile renders one `export KEY='value'` line per var, sorted.
func renderOrgEnvFile(env *OrgEnv) string {
	var b strings.Builder
	for _, k := range env.Names() {
		if !shellVarName.MatchString(k) {
			continue
		}
		b.WriteString("export " + k + "=" + shellSingleQuote(env.Vars[k]) + "\n")
	}
	return b.String()
}

// writeOrgEnvFile atomically writes the org env file (dir 0700, file 0600).
func writeOrgEnvFile(path string, env *OrgEnv) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".org-env-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(renderOrgEnvFile(env)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// OrgEnvSourcePrefix returns the shell snippet that loads the org env file
// named by GT_ORG_ENV_FILE in env, or "" when there is none. Builders emit it
// BEFORE gt's own exports so gt's variables still win.
func OrgEnvSourcePrefix(env map[string]string) string {
	f := env["GT_ORG_ENV_FILE"]
	if f == "" || runtime.GOOS == "windows" {
		return ""
	}
	q := shellSingleQuote(f)
	return "[ -r " + q + " ] && . " + q + "; "
}

type orgEnvCacheEntry struct {
	env *OrgEnv
}

var (
	orgEnvMu    sync.Mutex
	orgEnvCache = map[string]*orgEnvCacheEntry{}
)

// OrgEnvForTown returns the town's org env, resolved once per gt process
// (direnv can be slow). A failure prints one warning to stderr (names and
// counts only, never values) and yields nil so agent start is never blocked.
func OrgEnvForTown(townRoot string) *OrgEnv {
	if townRoot == "" {
		return nil
	}
	orgEnvMu.Lock()
	defer orgEnvMu.Unlock()
	if e, ok := orgEnvCache[townRoot]; ok {
		return e.env
	}
	env, err := LoadOrgEnv(townRoot)
	if err != nil {
		// An existing org-env.sh is left alone but not referenced.
		fmt.Fprintf(os.Stderr, "warning: org env unavailable, agents start without it: %v\n", err)
		env = nil
	} else if env != nil && len(env.Vars) > 0 {
		path := OrgEnvFilePath(townRoot)
		if werr := writeOrgEnvFile(path, env); werr != nil {
			fmt.Fprintf(os.Stderr, "warning: org env not written, agents start without it: %v\n", werr)
			env = nil
		} else {
			env.File = path
		}
	}
	orgEnvCache[townRoot] = &orgEnvCacheEntry{env: env}
	return env
}

// ResetOrgEnvCache drops cached org envs. For tests.
func ResetOrgEnvCache() {
	orgEnvMu.Lock()
	defer orgEnvMu.Unlock()
	orgEnvCache = map[string]*orgEnvCacheEntry{}
}

// ParseAxiTool splits an axi_tools entry "name" or "name@version".
func ParseAxiTool(entry string) (name, version string) {
	name, version, _ = strings.Cut(strings.TrimSpace(entry), "@")
	return name, version
}
