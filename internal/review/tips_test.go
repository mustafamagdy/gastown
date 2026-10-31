package review

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeCommits replaces CurrentCommit with a map from tip name to commit. A
// missing name is an unreadable tip.
func fakeCommits(t *testing.T, commits map[string]string) {
	t.Helper()
	prev := CurrentCommit
	CurrentCommit = func(tip Tip) (string, error) {
		c, ok := commits[tip.Name]
		if !ok {
			return "", fmt.Errorf("no such repo")
		}
		return c, nil
	}
	t.Cleanup(func() { CurrentCommit = prev })
}

func newTipRec(t *testing.T, town, id, bead, state string, tips ...Tip) {
	t.Helper()
	newRec(t, town, id, bead, KindApproval, state, time.Hour)
	if _, err := Update(town, id, func(r *Record) error { r.Tips = tips; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureGateApprovalStopsCountingWhenCodeMoves(t *testing.T) {
	town := t.TempDir()
	beads := []string{"epic-1", "task-1"}
	newTipRec(t, town, "rv-00000001", "task-1", StateApproved,
		Tip{Name: "api", Repo: "/r/api", Branch: "feat", Commit: "aaaa1111"},
		Tip{Name: "web", Repo: "/r/web", Branch: "feat", Commit: "bbbb2222"})

	fakeCommits(t, map[string]string{"api": "aaaa1111", "web": "bbbb2222"})
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("unchanged code: %+v", g)
	}

	fakeCommits(t, map[string]string{"api": "aaaa1111", "web": "cccc3333"})
	g := FeatureGate(town, beads)
	if g.OK() || g.Status != GatePending {
		t.Fatalf("moved code still approved: %+v", g)
	}
	for _, want := range []string{"code changed since approval", "web bbbb2222 -> cccc3333", "gt review request"} {
		if !strings.Contains(g.Message, want) {
			t.Errorf("message %q lacks %q", g.Message, want)
		}
	}
	if blocked, why := MergeBlock(town, "task-1"); !blocked || !strings.Contains(why, "code changed since approval") {
		t.Errorf("MergeBlock = %v, %q", blocked, why)
	}

	// A newer approval for the new code counts again.
	newTipRec(t, town, "rv-00000002", "task-1", StateApproved, Tip{Name: "web", Repo: "/r/web", Branch: "feat", Commit: "cccc3333"})
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("new approval: %+v", g)
	}
	if blocked, why := MergeBlock(town, "task-1"); blocked {
		t.Errorf("MergeBlock blocked a fresh approval: %s", why)
	}
}

func TestGateFailsClosedWhenTipUnreadable(t *testing.T) {
	town := t.TempDir()
	newTipRec(t, town, "rv-00000001", "task-1", StateApproved, Tip{Name: "api", Repo: "/gone", Commit: "aaaa1111"})
	fakeCommits(t, map[string]string{})
	if g := FeatureGate(town, []string{"task-1"}); g.OK() || !strings.Contains(g.Message, "cannot be checked") {
		t.Fatalf("unreadable tip approved: %+v", g)
	}
	if blocked, _ := MergeBlock(town, "task-1"); !blocked {
		t.Fatal("unreadable tip must block the merge")
	}
}

func TestGateWithoutTipsKeepsOldBehaviour(t *testing.T) {
	town := t.TempDir()
	newRec(t, town, "rv-00000001", "task-1", KindApproval, StateApproved, time.Hour)
	fakeCommits(t, map[string]string{})
	if g := FeatureGate(town, []string{"task-1"}); !g.OK() {
		t.Fatalf("no tips: %+v", g)
	}
	if blocked, _ := MergeBlock(town, "task-1"); blocked {
		t.Fatal("no tips must not block")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestBranchTipFollowsOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "-b", "feat")
	gitIn(t, origin, "commit", "-q", "--allow-empty", "-m", "one")
	clone := t.TempDir()
	gitIn(t, clone, "init", "-q")
	gitIn(t, clone, "remote", "add", "origin", origin)

	tip, err := BranchTip("api", clone+"/.git", "feat")
	if err != nil || tip.Commit == "" {
		t.Fatalf("BranchTip = %+v, %v", tip, err)
	}
	rec := &Record{Tips: []Tip{tip}}
	if moved, err := CodeChanged(rec); moved != "" || err != nil {
		t.Fatalf("unchanged: %q, %v", moved, err)
	}
	gitIn(t, origin, "commit", "-q", "--allow-empty", "-m", "two")
	if moved, err := CodeChanged(rec); moved == "" || err != nil {
		t.Fatalf("changed: %q, %v", moved, err)
	}
	if _, err := BranchTip("api", clone+"/.git", "nope"); err == nil {
		t.Fatal("missing branch must be an error")
	}
}

func TestHeadTipAndCodeChangedWithRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if tip, err := HeadTip(t.TempDir()); tip != nil || err != nil {
		t.Fatalf("not a repo: %v, %v", tip, err)
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "one")
	tip, err := HeadTip(dir)
	if err != nil || tip == nil || tip.Commit == "" {
		t.Fatalf("HeadTip = %v, %v", tip, err)
	}
	rec := &Record{Tips: []Tip{*tip}}
	if moved, err := CodeChanged(rec); moved != "" || err != nil {
		t.Fatalf("unchanged: %q, %v", moved, err)
	}
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "two")
	if moved, err := CodeChanged(rec); moved == "" || err != nil {
		t.Fatalf("changed: %q, %v", moved, err)
	}
}
