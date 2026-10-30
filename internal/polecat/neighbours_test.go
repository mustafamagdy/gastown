package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
)

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// addCommit commits a file in the work repo and refreshes the rig's bare repo.
func addCommit(t *testing.T, work, bare, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "-C", work, "add", "-A")
	runGit(t, "-C", work, "commit", "-q", "-m", "c "+file)
	runGit(t, "--git-dir", bare, "fetch", "-q", work, "+refs/heads/main:refs/heads/main")
}

// neighbourTown builds a town with project "shop": rig "orders" calls
// billing, shipping and stock; each neighbour has a bare repo with one file.
func neighbourTown(t *testing.T, limit *int) (town string, bare map[string]string, work map[string]string) {
	t.Helper()
	town = t.TempDir()
	bare, work = map[string]string{}, map[string]string{}
	rigs := map[string]config.RigEntry{"orders": {GitURL: "file:///r/orders.git", Project: "shop"}}
	svcs := []projectmap.Service{{Rig: "orders", Name: "orders", Calls: []string{"billing", "shipping", "stock"}}}
	for _, n := range []string{"billing", "shipping", "stock"} {
		w := filepath.Join(t.TempDir(), n)
		if err := os.MkdirAll(filepath.Join(w, "src"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w, "src", "api.txt"), []byte(n), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, "-C", w, "init", "-q", "-b", "main")
		runGit(t, "-C", w, "add", "-A")
		runGit(t, "-C", w, "commit", "-q", "-m", "init")
		b := filepath.Join(town, n, ".repo.git")
		if err := os.MkdirAll(filepath.Dir(b), 0755); err != nil {
			t.Fatal(err)
		}
		runGit(t, "clone", "-q", "--bare", w, b)
		bare[n], work[n] = b, w
		rigs[n] = config.RigEntry{GitURL: "file:///r/" + n + ".git", Project: "shop"}
		svcs = append(svcs, projectmap.Service{Rig: n, Name: n})
	}
	if err := config.SaveRigsConfig(filepath.Join(town, "mayor", "rigs.json"), &config.RigsConfig{Version: config.CurrentRigsVersion, Rigs: rigs}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh", NeighbourCheckouts: limit}); err != nil {
		t.Fatal(err)
	}
	if err := projectmap.Save(town, &projectmap.Map{Project: "shop", Services: svcs}); err != nil {
		t.Fatal(err)
	}
	return town, bare, work
}

func TestNeighbourCheckoutsLifecycle(t *testing.T) {
	town, bare, work := neighbourTown(t, nil)
	pdir := filepath.Join(town, "orders", "polecats", "toast")
	if err := os.MkdirAll(filepath.Join(pdir, "orders"), 0755); err != nil {
		t.Fatal(err)
	}

	got := SetupNeighbourCheckouts(town, "orders", pdir)
	if len(got) != 3 || got[0].Rig != "billing" {
		t.Fatalf("checkouts = %+v", got)
	}
	file := filepath.Join(pdir, "neighbours", "billing", "src", "api.txt")
	if b, err := os.ReadFile(file); err != nil || string(b) != "billing" {
		t.Fatalf("read: %v %q", err, b)
	}
	// Read-only: no write bits on files or dirs.
	for _, p := range []string{file, filepath.Dir(file), filepath.Join(pdir, "neighbours", "billing")} {
		if fi, _ := os.Stat(p); fi.Mode().Perm()&0222 != 0 {
			t.Errorf("%s is writable: %v", p, fi.Mode())
		}
	}

	// Refresh to the latest default branch commit.
	addCommit(t, work["billing"], bare["billing"], "new.txt", "v2")
	got = SetupNeighbourCheckouts(town, "orders", pdir)
	if len(got) != 3 {
		t.Fatalf("refresh = %+v", got)
	}
	newFile := filepath.Join(pdir, "neighbours", "billing", "new.txt")
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("refresh did not update checkout: %v", err)
	}
	if fi, _ := os.Stat(newFile); fi.Mode().Perm()&0222 != 0 {
		t.Errorf("refreshed file writable: %v", fi.Mode())
	}
	listed := ListNeighbourCheckouts(pdir)
	if len(listed) != 3 || listed[0].Commit == "" {
		t.Errorf("list = %+v", listed)
	}

	// Cleanup removes checkouts, registrations, and leaves the dir deletable.
	CleanupNeighbourCheckouts(town, pdir)
	if _, err := os.Stat(filepath.Join(pdir, "neighbours")); !os.IsNotExist(err) {
		t.Errorf("neighbours dir should be gone: %v", err)
	}
	out, _ := exec.Command("git", "--git-dir", bare["billing"], "worktree", "list").Output()
	if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Errorf("worktree registration left behind: %s", out)
	}
}

func TestNeighbourCheckoutsRemovableWhenReadOnly(t *testing.T) {
	town, _, _ := neighbourTown(t, nil)
	pdir := filepath.Join(town, "orders", "polecats", "toast")
	if err := os.MkdirAll(pdir, 0755); err != nil {
		t.Fatal(err)
	}
	SetupNeighbourCheckouts(town, "orders", pdir)
	if os.RemoveAll(pdir) == nil {
		t.Skip("running as a user that ignores permissions")
	}
	if err := forceRemoveDir(pdir); err != nil {
		t.Fatalf("forceRemoveDir: %v", err)
	}
}

func TestNeighbourCheckoutsCapAndDisable(t *testing.T) {
	two := 2
	town, _, _ := neighbourTown(t, &two)
	pdir := filepath.Join(town, "orders", "polecats", "toast")
	if got := SetupNeighbourCheckouts(town, "orders", pdir); len(got) != 2 || got[1].Rig != "shipping" {
		t.Fatalf("cap 2: %+v", got)
	}

	zero := 0
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh", NeighbourCheckouts: &zero}); err != nil {
		t.Fatal(err)
	}
	if got := SetupNeighbourCheckouts(town, "orders", pdir); len(got) != 0 {
		t.Fatalf("disabled: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(pdir, "neighbours")); !os.IsNotExist(err) {
		t.Errorf("disabling should clean existing checkouts")
	}
}

func TestNeighbourCheckoutsSkips(t *testing.T) {
	town, _, _ := neighbourTown(t, nil)
	pdir := filepath.Join(town, "orders", "polecats", "toast")
	// Rig outside a project.
	if got := SetupNeighbourCheckouts(town, "unknown", pdir); got != nil {
		t.Errorf("non-project rig: %+v", got)
	}
	// No saved map.
	_ = os.Remove(projectmap.Path(town, "shop"))
	if got := SetupNeighbourCheckouts(town, "orders", pdir); got != nil {
		t.Errorf("no map: %+v", got)
	}
}

// A feature polecat sees sibling rigs at the feature branch, even a rig the
// service map has no edge to, and the feature's rigs fill the cap first.
func TestFeatureNeighbourCheckouts(t *testing.T) {
	limit := 2
	town, bare, work := neighbourTown(t, &limit)
	const branch = "feat/sh-1-x"
	runGit(t, "-C", work["stock"], "checkout", "-q", "-b", branch)
	addCommit(t, work["stock"], bare["stock"], "src/new.txt", "unmerged")
	runGit(t, "-C", work["stock"], "checkout", "-q", "main")

	polecatDir := filepath.Join(town, "orders", "polecats", "toast")
	got := SetupFeatureNeighbourCheckouts(town, "orders", polecatDir, branch, []string{"orders", "stock"})
	if len(got) != 2 || got[0].Rig != "stock" {
		t.Fatalf("checkouts = %+v, want stock first, cap 2", got)
	}
	if _, err := os.Stat(filepath.Join(polecatDir, "neighbours", "stock", "src", "new.txt")); err != nil {
		t.Errorf("sibling's feature branch work missing: %v", err)
	}
	if b := NeighbourFeatureBranch(polecatDir); b != branch {
		t.Errorf("NeighbourFeatureBranch = %q", b)
	}
	if len(ListNeighbourCheckouts(polecatDir)) != 2 {
		t.Errorf("list = %+v", ListNeighbourCheckouts(polecatDir))
	}
	SetupNeighbourCheckouts(town, "orders", polecatDir)
	if b := NeighbourFeatureBranch(polecatDir); b != "" {
		t.Errorf("default checkout must clear the branch marker, got %q", b)
	}
	CleanupNeighbourCheckouts(town, polecatDir)
	if _, err := os.Stat(filepath.Join(polecatDir, "neighbours")); !os.IsNotExist(err) {
		t.Errorf("neighbours dir not removed: %v", err)
	}
}
