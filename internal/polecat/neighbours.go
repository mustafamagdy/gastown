package polecat

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
	"github.com/steveyegge/gastown/internal/style"
)

// NeighboursDirName is the folder inside a polecat's home directory
// (polecats/<name>/) holding read-only checkouts of its rig's neighbours.
const NeighboursDirName = "neighbours"

// NeighbourCheckout is one materialized read-only neighbour checkout.
type NeighbourCheckout struct {
	Rig    string
	Path   string
	Commit string // short commit the checkout is at
}

// NeighboursDir returns <polecatDir>/neighbours.
func NeighboursDir(polecatDir string) string {
	return filepath.Join(polecatDir, NeighboursDirName)
}

func gitBare(bare string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--git-dir", bare}, args...)...) //nolint:gosec // G204: args are constructed internally
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// setWritable adds or removes the write bits of a whole tree. Symlinks are
// left alone. Directories get owner write+exec so they can be walked.
func setWritable(root string, writable bool) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if writable {
			mode |= 0200
		} else {
			mode &^= 0222
		}
		if d.IsDir() && writable {
			mode |= 0100
		}
		if mode != info.Mode().Perm() {
			_ = os.Chmod(p, mode) //nolint:gosec // G302: permission change inside the polecat's own directory
		}
		return nil
	})
	// WalkDir visits a directory before its children, so a read-only directory
	// can block the walk below it; chmod directories again bottom-up.
	if writable {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0755) //nolint:gosec // G302: see above
			}
			return nil
		})
	}
}

// materializeNeighbour creates or refreshes one read-only checkout at path,
// a detached worktree of the neighbour's bare repo at ref.
func materializeNeighbour(bare, path, ref string) error {
	commit, err := gitBare(bare, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		setWritable(path, true)
		if err := exec.Command("git", "-C", path, "checkout", "--force", "--detach", commit).Run(); err == nil { //nolint:gosec // G204: internal args
			setWritable(path, false)
			return nil
		}
		removeNeighbour(bare, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if _, err := gitBare(bare, "worktree", "add", "--detach", path, commit); err != nil {
		_ = os.RemoveAll(path)
		return err
	}
	setWritable(path, false)
	return nil
}

// removeNeighbour removes one checkout and its worktree registration.
func removeNeighbour(bare, path string) {
	setWritable(path, true)
	if _, err := gitBare(bare, "worktree", "remove", "--force", path); err != nil {
		_ = os.RemoveAll(path)
	}
	_, _ = gitBare(bare, "worktree", "prune")
}

// SetupNeighbourCheckouts materializes (or refreshes) read-only checkouts of
// the neighbours of rigName under polecatDir/neighbours/<rig>, per the
// project's saved service map, capped by the project's neighbour_checkouts.
// It never fails the caller: problems are warned about and skipped.
func SetupNeighbourCheckouts(townRoot, rigName, polecatDir string) []NeighbourCheckout {
	return setupNeighbourCheckouts(townRoot, rigName, polecatDir, "", nil)
}

// SetupFeatureNeighbourCheckouts is SetupNeighbourCheckouts for a polecat
// working on a multi-repo feature: the other rigs of the feature come first
// and are checked out at the feature branch, where sibling tasks have merged
// work that is not on any default branch yet. A sibling repo is included even
// when the service map shows no edge to it (a shared contracts repo, say).
func SetupFeatureNeighbourCheckouts(townRoot, rigName, polecatDir, branch string, featureRigs []string) []NeighbourCheckout {
	return setupNeighbourCheckouts(townRoot, rigName, polecatDir, branch, featureRigs)
}

func setupNeighbourCheckouts(townRoot, rigName, polecatDir, branch string, featureRigs []string) []NeighbourCheckout {
	project, err := config.ProjectForRig(townRoot, rigName)
	if err != nil || project == "" {
		return nil
	}
	limit := config.DefaultNeighbourCheckouts
	if cfg, err := config.LoadProjectConfig(townRoot, project); err == nil {
		limit = cfg.NeighbourCheckoutLimit()
	}
	dir := NeighboursDir(polecatDir)
	m, err := projectmap.Load(townRoot, project)
	if err != nil || limit == 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "neighbour checkouts skipped: no service map for project %s (gt project map build %s)\n", project, project)
		}
		CleanupNeighbourCheckouts(townRoot, polecatDir)
		return nil
	}
	inFeature := map[string]bool{}
	var want []string
	for _, n := range featureRigs {
		if n != rigName && !inFeature[n] {
			inFeature[n] = true
			want = append(want, n)
		}
	}
	for _, n := range projectmap.Neighbours(m, rigName) {
		if !inFeature[n] {
			want = append(want, n)
		}
	}
	if len(want) > limit {
		want = want[:limit]
	}
	keep := map[string]bool{}
	var out []NeighbourCheckout
	for _, n := range want {
		nRig := filepath.Join(townRoot, n)
		bare := filepath.Join(nRig, ".repo.git")
		source := projectmap.DefaultBranch(nRig)
		if inFeature[n] {
			source = branch
			// Sibling tasks land on the remote; the bare repo may not have them yet.
			if _, err := gitBare(bare, "fetch", "-q", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
				style.PrintWarning("neighbour checkout %s: fetching %s: %v", n, branch, err)
			}
		}
		ref, _, err := projectmap.BranchRef(bare, source)
		if err != nil {
			style.PrintWarning("neighbour checkout %s skipped: %v", n, err)
			continue
		}
		path := filepath.Join(dir, n)
		if err := materializeNeighbour(bare, path, ref); err != nil {
			style.PrintWarning("neighbour checkout %s skipped: %v", n, err)
			continue
		}
		keep[n] = true
		out = append(out, NeighbourCheckout{Rig: n, Path: path, Commit: headShort(path)})
	}
	// Drop checkouts of rigs that are no longer wanted.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !keep[e.Name()] && e.IsDir() {
				removeNeighbour(filepath.Join(townRoot, e.Name(), ".repo.git"), filepath.Join(dir, e.Name()))
			}
		}
	}
	marker := filepath.Join(dir, neighbourBranchFile)
	if branch != "" && len(out) > 0 {
		if err := os.WriteFile(marker, []byte(branch+"\n"), 0644); err != nil {
			style.PrintWarning("neighbour checkouts: %v", err)
		}
	} else {
		_ = os.Remove(marker)
	}
	return out
}

// neighbourBranchFile records the feature branch of a polecat's feature
// neighbours, so prime can say what the snapshots contain.
const neighbourBranchFile = ".feature-branch"

// NeighbourFeatureBranch returns the feature branch the polecat's neighbour
// checkouts were taken from, "" when they are default branch snapshots.
func NeighbourFeatureBranch(polecatDir string) string {
	data, err := os.ReadFile(filepath.Join(NeighboursDir(polecatDir), neighbourBranchFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// CleanupNeighbourCheckouts removes every neighbour checkout of a polecat and
// leaves its tree writable so the polecat directory can be deleted. Errors are
// not returned: the main removal must not fail because of neighbour cleanup.
func CleanupNeighbourCheckouts(townRoot, polecatDir string) {
	dir := NeighboursDir(polecatDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	setWritable(dir, true)
	for _, e := range entries {
		if e.IsDir() {
			removeNeighbour(filepath.Join(townRoot, e.Name(), ".repo.git"), filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(filepath.Join(dir, neighbourBranchFile))
	_ = os.Remove(dir)
}

// ListNeighbourCheckouts returns the materialized checkouts under polecatDir.
func ListNeighbourCheckouts(polecatDir string) []NeighbourCheckout {
	entries, err := os.ReadDir(NeighboursDir(polecatDir))
	if err != nil {
		return nil
	}
	var out []NeighbourCheckout
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(NeighboursDir(polecatDir), e.Name())
		if c := headShort(p); c != "" {
			out = append(out, NeighbourCheckout{Rig: e.Name(), Path: p, Commit: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rig < out[j].Rig })
	return out
}

func headShort(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// setupNeighbours and cleanupNeighbours are the Manager hooks.
func (m *Manager) setupNeighbours(name string) {
	polecatDir := m.polecatDir(name)
	if m.clonePath(name) == polecatDir {
		return // old layout: no room beside the worktree
	}
	SetupNeighbourCheckouts(filepath.Dir(m.rig.Path), m.rig.Name, polecatDir)
}

func (m *Manager) cleanupNeighbours(name string) {
	CleanupNeighbourCheckouts(filepath.Dir(m.rig.Path), m.polecatDir(name))
}
