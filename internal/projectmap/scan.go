package projectmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

const gitTimeout = 30 * time.Second

// manifest is the TOML shape of service.toml and of the per-rig override file.
// Serves entries are written as "GET /orders".
type manifest struct {
	Name      string   `toml:"name"`
	Owns      string   `toml:"owns"`
	Serves    []string `toml:"serves"`
	Calls     []string `toml:"calls"`
	Publishes []string `toml:"publishes"`
	Consumes  []string `toml:"consumes"`
	Database  string   `toml:"database"`
}

// manifestPaths and openAPIPaths are looked up in this order.
var (
	manifestPaths = []string{"service.toml", ".gt/service.toml"}
	openAPIPaths  = []string{
		"openapi.yaml", "openapi.yml", "openapi.json",
		"api/openapi.yaml", "api/openapi.yml", "api/openapi.json",
		"docs/openapi.yaml", "docs/openapi.yml", "docs/openapi.json",
	}
	fallbackPaths = []string{"package.json", "go.mod"}
)

func git(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// refCommit resolves the tip of a branch in a bare repo: origin/<branch> when
// it exists (the rig's .repo.git keeps remote-tracking refs), else <branch>.
func refCommit(bare, branch string) (string, error) {
	for _, ref := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if out, err := git(bare, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("branch %q not found in %s", branch, bare)
}

// BranchRef returns the ref to read a rig's default branch from in its bare
// repo (origin/<branch> if present, else <branch>) and the commit it points at.
func BranchRef(bare, branch string) (ref, commit string, err error) {
	for _, r := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if out, gerr := git(bare, "rev-parse", "--verify", "--quiet", r+"^{commit}"); gerr == nil {
			return strings.TrimPrefix(strings.TrimPrefix(r, "refs/remotes/"), "refs/heads/"), strings.TrimSpace(string(out)), nil
		}
	}
	return "", "", fmt.Errorf("branch %q not found in %s", branch, bare)
}

// DefaultBranch reads default_branch from <rigPath>/config.json (main if unset).
func DefaultBranch(rigPath string) string { return rigDefaultBranch(rigPath) }

// repoFiles reads the interesting files present at a commit.
func repoFiles(bare, commit string) (map[string][]byte, error) {
	var wanted []string
	wanted = append(wanted, manifestPaths...)
	wanted = append(wanted, openAPIPaths...)
	wanted = append(wanted, fallbackPaths...)
	out, err := git(bare, append([]string{"ls-tree", "--name-only", commit, "--"}, wanted...)...)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p == "" {
			continue
		}
		data, err := git(bare, "show", commit+":"+p)
		if err != nil {
			continue
		}
		files[p] = data
	}
	return files, nil
}

// repoName derives the repo name from a git URL: last path element, no .git.
func repoName(gitURL string) string {
	s := strings.TrimSuffix(strings.TrimSpace(gitURL), "/")
	if u, err := url.Parse(s); err == nil && u.Path != "" && strings.Contains(s, "://") {
		s = u.Path
	}
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSuffix(s, ".git")
}

// scanRig builds the entry of one rig from the default branch tip in its bare
// repo, merged with the override file (override > manifest > OpenAPI > fallback).
func scanRig(rig, repo, bare, branch, overridePath string) (Service, error) {
	svc := Service{Rig: rig, Repo: repo}
	commit, err := refCommit(bare, branch)
	if err != nil {
		return svc, err
	}
	svc.Commit = commit
	files, err := repoFiles(bare, commit)
	if err != nil {
		return svc, err
	}

	var sources []string
	var mf manifest
	for _, p := range manifestPaths {
		if data, ok := files[p]; ok {
			if _, err := toml.Decode(string(data), &mf); err != nil {
				return svc, fmt.Errorf("parsing %s: %w", p, err)
			}
			sources = append(sources, "manifest")
			break
		}
	}
	var spec []Endpoint
	for _, p := range openAPIPaths {
		if data, ok := files[p]; ok {
			if eps, err := parseOpenAPI(data); err == nil && len(eps) > 0 {
				spec = eps
				sources = append(sources, "openapi")
				break
			}
		}
	}

	var ov manifest
	if data, err := os.ReadFile(overridePath); err == nil { //nolint:gosec // G304: path is constructed internally
		if _, err := toml.Decode(string(data), &ov); err != nil {
			return svc, fmt.Errorf("parsing %s: %w", overridePath, err)
		}
		sources = append([]string{"override"}, sources...)
	}

	svc.Name = firstNonEmpty(ov.Name, mf.Name, fallbackName(files), repo, rig)
	svc.Owns = firstNonEmpty(ov.Owns, mf.Owns)
	svc.Database = firstNonEmpty(ov.Database, mf.Database)
	svc.Calls = pickList(ov.Calls, mf.Calls)
	svc.Publishes = pickList(ov.Publishes, mf.Publishes)
	svc.Consumes = pickList(ov.Consumes, mf.Consumes)
	svc.Serves = spec
	if s := pickList(ov.Serves, mf.Serves); len(s) > 0 {
		svc.Serves = nil
		for _, e := range s {
			svc.Serves = append(svc.Serves, parseEndpoint(e))
		}
	}
	sortEndpoints(svc.Serves)
	svc.Source = strings.Join(sources, ",")
	return svc, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// pickList returns the first non-empty list, so an override wins field by field.
func pickList(lists ...[]string) []string {
	for _, l := range lists {
		if l = dedupSorted(append([]string(nil), l...)); len(l) > 0 {
			return l
		}
	}
	return nil
}

func parseEndpoint(s string) Endpoint {
	f := strings.Fields(s)
	if len(f) >= 2 {
		return Endpoint{Method: strings.ToUpper(f[0]), Path: f[1]}
	}
	return Endpoint{Method: "ANY", Path: strings.TrimSpace(s)}
}

func sortEndpoints(eps []Endpoint) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Path != eps[j].Path {
			return eps[i].Path < eps[j].Path
		}
		return eps[i].Method < eps[j].Method
	})
}

// fallbackName names a service from package.json "name" or the go.mod module.
func fallbackName(files map[string][]byte) string {
	if data, ok := files["package.json"]; ok {
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Name != "" {
			return pkg.Name
		}
	}
	if data, ok := files["go.mod"]; ok {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
				return path.Base(strings.Trim(f[1], `"`))
			}
		}
	}
	return ""
}

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

// parseOpenAPI extracts the served endpoints from an OpenAPI document (YAML
// or JSON; JSON is valid YAML).
func parseOpenAPI(data []byte) ([]Endpoint, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var eps []Endpoint
	for p, ops := range doc.Paths {
		for m := range ops {
			if httpMethods[m] {
				eps = append(eps, Endpoint{Method: strings.ToUpper(m), Path: p})
			}
		}
	}
	sortEndpoints(eps)
	return eps, nil
}

// rigDefaultBranch reads default_branch from <rigPath>/config.json (main if unset).
func rigDefaultBranch(rigPath string) string {
	data, err := os.ReadFile(filepath.Join(rigPath, "config.json")) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		return "main"
	}
	var cfg struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.DefaultBranch == "" {
		return "main"
	}
	return cfg.DefaultBranch
}
