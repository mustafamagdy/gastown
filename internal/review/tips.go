package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const gitTimeout = 30 * time.Second

// Tip is one piece of code a review was about, with the commit the reviewer
// saw when the review was requested.
type Tip struct {
	Name   string `json:"name"`             // rig name, or "head"
	Repo   string `json:"repo"`             // git dir (feature branch) or work dir (head)
	Branch string `json:"branch,omitempty"` // feature branch on origin; empty means HEAD of Repo
	Commit string `json:"commit"`
}

// CurrentCommit reads the commit a tip points at now. It is a variable so
// tests can replace it.
var CurrentCommit = currentCommit

func currentCommit(t Tip) (string, error) {
	if t.Branch == "" {
		return runGit("-C", t.Repo, "rev-parse", "HEAD")
	}
	out, err := runGit("--git-dir="+t.Repo, "ls-remote", "origin", "refs/heads/"+t.Branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("branch %s not found on origin", t.Branch)
	}
	return fields[0], nil
}

// BranchTip returns the tip of a feature branch on the origin of gitDir.
func BranchTip(name, gitDir, branch string) (Tip, error) {
	t := Tip{Name: name, Repo: gitDir, Branch: branch}
	c, err := currentCommit(t)
	if err != nil {
		return Tip{}, fmt.Errorf("reading branch %s of %s: %w", branch, name, err)
	}
	t.Commit = c
	return t, nil
}

// HeadTip returns the HEAD of dir, or nil when dir is not in a git work tree.
func HeadTip(dir string) (*Tip, error) {
	top, err := runGit("-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil //nolint:nilerr // not a repo: nothing to record
	}
	t := Tip{Name: "head", Repo: top}
	if t.Commit, err = currentCommit(t); err != nil {
		return nil, fmt.Errorf("reading HEAD of %s: %w", top, err)
	}
	return &t, nil
}

// CodeChanged compares the tips recorded with a review to the code now. It
// returns a short description of what moved, or "" when nothing did. An error
// means a tip could not be read; callers must then fail closed. A record with
// no tips was made where nothing could be recorded, and never reports a change.
func CodeChanged(rec *Record) (string, error) {
	var moved []string
	for _, t := range rec.Tips {
		now, err := CurrentCommit(t)
		if err != nil {
			return "", fmt.Errorf("cannot read %s: %w", t.Name, err)
		}
		if now != t.Commit {
			moved = append(moved, fmt.Sprintf("%s %s -> %s", t.Name, short(t.Commit), short(now)))
		}
	}
	return strings.Join(moved, ", "), nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func runGit(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
