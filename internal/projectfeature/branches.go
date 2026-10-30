package projectfeature

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const gitTimeout = 2 * time.Minute

// RigRepo is the git repository a feature branch is created in: a rig's
// .repo.git, whose origin remote is the upstream repo.
type RigRepo struct {
	Rig     string
	GitDir  string // path of the bare repo (or any repo with an origin remote)
	Default string // default branch of the rig; the feature branch is cut from it
}

// BranchResult is the outcome of creating the feature branch in one rig.
type BranchResult struct {
	Rig        string
	Base       string
	BaseCommit string
}

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// BranchExists reports whether the branch exists on the repo's origin.
func BranchExists(r RigRepo, branch string) (bool, error) {
	out, err := git(r.GitDir, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// CheckBranchFree returns an error if the branch exists on origin in any of
// the repos. Every repo is checked before the caller changes anything.
func CheckBranchFree(repos []RigRepo, branch string) error {
	var taken []string
	for _, r := range repos {
		ok, err := BranchExists(r, branch)
		if err != nil {
			return fmt.Errorf("rig %s: checking branch %s: %w", r.Rig, branch, err)
		}
		if ok {
			taken = append(taken, r.Rig)
		}
	}
	if len(taken) > 0 {
		return fmt.Errorf("branch %s already exists in rig(s): %s", branch, strings.Join(taken, ", "))
	}
	return nil
}

// CreateBranches creates and pushes branch from each rig's default branch.
// It checks all rigs first and creates nothing if the branch exists in any.
// If a creation fails, the branches pushed so far are deleted again.
func CreateBranches(repos []RigRepo, branch string) ([]BranchResult, error) {
	if err := CheckBranchFree(repos, branch); err != nil {
		return nil, err
	}
	var done []RigRepo
	var results []BranchResult
	for _, r := range repos {
		res, err := createBranch(r, branch)
		if err != nil {
			rbErr := DeleteBranches(done, branch)
			if rbErr != nil {
				return nil, fmt.Errorf("rig %s: %w (rollback incomplete: %v)", r.Rig, err, rbErr)
			}
			return nil, fmt.Errorf("rig %s: %w (rolled back %d rig(s))", r.Rig, err, len(done))
		}
		done = append(done, r)
		results = append(results, *res)
	}
	return results, nil
}

// CreateBranch creates the branch in one repo (used by add-rig).
func CreateBranch(r RigRepo, branch string) (*BranchResult, error) {
	if err := CheckBranchFree([]RigRepo{r}, branch); err != nil {
		return nil, err
	}
	return createBranch(r, branch)
}

func createBranch(r RigRepo, branch string) (*BranchResult, error) {
	if r.Default == "" {
		return nil, fmt.Errorf("no default branch")
	}
	if _, err := git(r.GitDir, "fetch", "--quiet", "origin", "+refs/heads/"+r.Default+":refs/remotes/origin/"+r.Default); err != nil {
		return nil, fmt.Errorf("fetching %s: %w", r.Default, err)
	}
	sha, err := git(r.GitDir, "rev-parse", "--verify", "refs/remotes/origin/"+r.Default+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolving origin/%s: %w", r.Default, err)
	}
	if _, err := git(r.GitDir, "push", "--quiet", "origin", sha+":refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("pushing %s: %w", branch, err)
	}
	return &BranchResult{Rig: r.Rig, Base: r.Default, BaseCommit: sha}, nil
}

// DeleteBranches deletes the branch from origin in each repo. It tries all
// repos and returns the joined errors.
func DeleteBranches(repos []RigRepo, branch string) error {
	var errs []string
	for _, r := range repos {
		if _, err := git(r.GitDir, "push", "--quiet", "origin", "--delete", branch); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.Rig, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RemoteBranchExists is BranchExists for callers that only have a git dir.
func RemoteBranchExists(gitDir, branch string) (bool, error) {
	return BranchExists(RigRepo{GitDir: gitDir}, branch)
}
