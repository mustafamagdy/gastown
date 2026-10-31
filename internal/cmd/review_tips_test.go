package cmd

import (
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/review"
)

func TestReviewTips(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	town := t.TempDir()

	// Kinds other than approval record nothing.
	if tips, err := reviewTips(town, "gt-1", review.KindPlan); tips != nil || err != nil {
		t.Fatalf("plan: %v, %v", tips, err)
	}

	// Outside a repo there is nothing to record.
	t.Chdir(t.TempDir())
	if tips, err := reviewTips(town, "gt-1", review.KindApproval); tips != nil || err != nil {
		t.Fatalf("not a repo: %v, %v", tips, err)
	}

	// Inside a repo, with no feature, the HEAD of the working directory is recorded.
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "one")
	t.Chdir(repo)
	tips, err := reviewTips(town, "gt-1", review.KindApproval)
	if err != nil || len(tips) != 1 || tips[0].Commit == "" || tips[0].Branch != "" {
		t.Fatalf("repo: %+v, %v", tips, err)
	}
}
