package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const ghPRFields = "number,url,state,headRefName,baseRefName,headRefOid,mergeCommit,isDraft"

type githubProvider struct {
	opts Options
}

func newGitHub(opts Options) *githubProvider { return &githubProvider{opts: opts} }

type ghPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	HeadRefOid  string `json:"headRefOid"`
	IsDraft     bool   `json:"isDraft"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
}

func (g ghPR) toPR() *PR {
	pr := &PR{
		ID:      strconv.Itoa(g.Number),
		Number:  g.Number,
		URL:     g.URL,
		Head:    g.HeadRefName,
		Base:    g.BaseRefName,
		HeadSHA: g.HeadRefOid,
		Draft:   g.IsDraft,
	}
	switch strings.ToUpper(g.State) {
	case "MERGED":
		pr.State = StateMerged
	case "CLOSED":
		pr.State = StateClosed
	default:
		pr.State = StateOpen
	}
	if g.MergeCommit != nil {
		pr.MergeCommit = g.MergeCommit.Oid
	}
	return pr
}

func (p *githubProvider) gh(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return run(ctx, firstDir(dir, p.opts.RepoDir), "gh", args...)
}

func (p *githubProvider) CreatePR(ctx context.Context, req CreatePRRequest) (*PR, error) {
	if req.Head == "" || req.Base == "" {
		return nil, fmt.Errorf("create PR: head and base are required")
	}
	method, err := normalizeMethod(req.MergeMethod)
	if err != nil {
		return nil, err
	}
	dir := firstDir(req.RepoDir, p.opts.RepoDir)
	existing, err := p.findPR(ctx, dir, req.Head, req.Base)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	args := []string{"pr", "create", "--head", req.Head, "--base", req.Base,
		"--title", req.Title, "--body", req.Body}
	if req.Draft {
		args = append(args, "--draft")
	}
	out, err := p.gh(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	// gh pr create prints the PR URL as its last line (it has no --json).
	url := lastLine(string(out))
	if !strings.HasPrefix(url, "http") {
		return nil, fmt.Errorf("gh pr create: unexpected output %q", truncate(string(out), 200))
	}
	pr, err := p.getPR(ctx, dir, url)
	if err != nil {
		return nil, err
	}
	if req.AutoComplete {
		flag := "--merge"
		if method == MethodSquash {
			flag = "--squash"
		}
		if _, err := p.gh(ctx, dir, "pr", "merge", pr.ID, "--auto", flag); err != nil {
			return pr, fmt.Errorf("PR %s created but enabling auto-merge failed: %w", pr.URL, err)
		}
	}
	return pr, nil
}

func (p *githubProvider) FindPR(ctx context.Context, head, base string) (*PR, error) {
	return p.findPR(ctx, p.opts.RepoDir, head, base)
}

func (p *githubProvider) findPR(ctx context.Context, dir, head, base string) (*PR, error) {
	out, err := p.gh(ctx, dir, "pr", "list", "--head", head, "--base", base,
		"--state", "open", "--json", ghPRFields, "--limit", "10")
	if err != nil {
		return nil, err
	}
	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parsing gh pr list output: %w", err)
	}
	for _, g := range prs {
		// gh --head matches by branch name only; confirm both ends.
		if g.HeadRefName == head && g.BaseRefName == base {
			return g.toPR(), nil
		}
	}
	return nil, nil
}

func (p *githubProvider) GetPR(ctx context.Context, id string) (*PR, error) {
	return p.getPR(ctx, p.opts.RepoDir, id)
}

func (p *githubProvider) getPR(ctx context.Context, dir, id string) (*PR, error) {
	out, err := p.gh(ctx, dir, "pr", "view", id, "--json", ghPRFields)
	if err != nil {
		return nil, err
	}
	var g ghPR
	if err := json.Unmarshal(out, &g); err != nil {
		return nil, fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return g.toPR(), nil
}

func (p *githubProvider) Checks(ctx context.Context, pr *PR) (CheckSummary, error) {
	// gh pr checks exits non-zero (8) while checks are pending and 1 on
	// failures, but still prints the JSON; trust the output when it parses.
	out, runErr := p.gh(ctx, "", "pr", "checks", pr.ID, "--json", "name,bucket,state")
	var rows []struct {
		Name   string `json:"name"`
		Bucket string `json:"bucket"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		if ce, ok := runErr.(*CmdError); ok && strings.Contains(ce.Stderr, "no checks reported") {
			return CheckSummary{State: ChecksNone}, nil
		}
		if runErr != nil {
			return CheckSummary{}, runErr
		}
		return CheckSummary{}, fmt.Errorf("parsing gh pr checks output: %w", err)
	}
	return summarizeGHChecks(rows2checks(rows)), nil
}

type ghCheck struct{ name, bucket string }

func rows2checks(rows []struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	State  string `json:"state"`
}) []ghCheck {
	cs := make([]ghCheck, 0, len(rows))
	for _, r := range rows {
		cs = append(cs, ghCheck{r.Name, strings.ToLower(r.Bucket)})
	}
	return cs
}

func summarizeGHChecks(cs []ghCheck) CheckSummary {
	if len(cs) == 0 {
		return CheckSummary{State: ChecksNone}
	}
	s := CheckSummary{State: ChecksPassing}
	pending := false
	for _, c := range cs {
		switch c.bucket {
		case "fail", "cancel":
			s.Failing = append(s.Failing, c.name)
		case "pending":
			pending = true
		}
	}
	switch {
	case len(s.Failing) > 0:
		s.State = ChecksFailing
	case pending:
		s.State = ChecksPending
	}
	return s
}

func (p *githubProvider) ReviewState(ctx context.Context, pr *PR) (string, error) {
	out, err := p.gh(ctx, "", "pr", "view", pr.ID, "--json", "reviewDecision")
	if err != nil {
		return "", err
	}
	var v struct {
		ReviewDecision string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parsing gh pr view output: %w", err)
	}
	// An empty decision means the repo does not require reviews and nobody
	// has reviewed; that is pending, not approved.
	switch strings.ToUpper(v.ReviewDecision) {
	case "APPROVED":
		return ReviewApproved, nil
	case "CHANGES_REQUESTED":
		return ReviewRejected, nil
	}
	return ReviewPending, nil
}

func (p *githubProvider) Merge(ctx context.Context, pr *PR, opts MergeOptions) error {
	method, err := normalizeMethod(opts.Method)
	if err != nil {
		return err
	}
	args := []string{"pr", "merge", pr.ID, "--" + method}
	if opts.ExpectHead != "" {
		args = append(args, "--match-head-commit", opts.ExpectHead)
	}
	if _, err := p.gh(ctx, "", args...); err != nil {
		if ce, ok := err.(*CmdError); ok && opts.ExpectHead != "" &&
			strings.Contains(strings.ToLower(ce.Stderr), "head branch was modified") {
			return fmt.Errorf("%w: %v", ErrHeadMoved, err)
		}
		return err
	}
	got, err := p.getPR(ctx, "", pr.ID)
	if err != nil {
		return err
	}
	if got.State != StateMerged {
		return fmt.Errorf("%w: PR %s is %s after merge request (merge queue or auto-merge?)", ErrNotMerged, pr.ID, got.State)
	}
	pr.State, pr.MergeCommit = got.State, got.MergeCommit
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
