package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Azure DevOps via `az repos pr ... --output json` (azure-devops extension).
// The argument shapes follow the az documentation and have not been run
// against a real organization from this code.

const (
	defaultAzurePoll    = 3 * time.Second
	defaultAzureTimeout = 5 * time.Minute

	voteApprove = 10
	voteReject  = -10
)

type azureProvider struct {
	opts Options
}

func newAzure(opts Options) *azureProvider {
	if opts.MergePollInterval <= 0 {
		opts.MergePollInterval = defaultAzurePoll
	}
	if opts.MergeTimeout <= 0 {
		opts.MergeTimeout = defaultAzureTimeout
	}
	return &azureProvider{opts: opts}
}

type azPR struct {
	PullRequestID int    `json:"pullRequestId"`
	Status        string `json:"status"` // active | completed | abandoned
	MergeStatus   string `json:"mergeStatus"`
	IsDraft       bool   `json:"isDraft"`
	SourceRefName string `json:"sourceRefName"`
	TargetRefName string `json:"targetRefName"`
	Repository    struct {
		WebURL string `json:"webUrl"`
	} `json:"repository"`
	LastMergeSourceCommit *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	LastMergeCommit *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
	Reviewers []azReviewer `json:"reviewers"`
}

type azReviewer struct {
	DisplayName string `json:"displayName"`
	Vote        int    `json:"vote"`
	IsRequired  bool   `json:"isRequired"`
	IsContainer bool   `json:"isContainer"`
}

func (a azPR) toPR(fallbackWeb string) *PR {
	pr := &PR{
		ID:     strconv.Itoa(a.PullRequestID),
		Number: a.PullRequestID,
		Head:   normalizeRef(a.SourceRefName),
		Base:   normalizeRef(a.TargetRefName),
		Draft:  a.IsDraft,
	}
	web := a.Repository.WebURL
	if web == "" {
		web = fallbackWeb
	}
	if web != "" {
		pr.URL = fmt.Sprintf("%s/pullrequest/%d", strings.TrimRight(web, "/"), a.PullRequestID)
	}
	if a.LastMergeSourceCommit != nil {
		pr.HeadSHA = a.LastMergeSourceCommit.CommitID
	}
	switch strings.ToLower(a.Status) {
	case "completed":
		pr.State = StateMerged
		if a.LastMergeCommit != nil {
			pr.MergeCommit = a.LastMergeCommit.CommitID
		}
	case "abandoned":
		pr.State = StateClosed
	default:
		pr.State = StateOpen
	}
	return pr
}

// resolve finds the Azure location for dir.
func (p *azureProvider) resolve(ctx context.Context, dir string) (AzureLocation, error) {
	dir = firstDir(dir, p.opts.RepoDir)
	remote := p.opts.RemoteURL
	if remote == "" {
		out, err := run(ctx, dir, "git", "remote", "get-url", "origin")
		if err != nil {
			return AzureLocation{}, fmt.Errorf("resolving Azure DevOps remote: %w", err)
		}
		remote = strings.TrimSpace(string(out))
	}
	return ParseAzureRemote(remote)
}

// scopeArgs are the flags selecting org/project/repo (for create and list).
func (l AzureLocation) scopeArgs() []string {
	return []string{"--organization", l.OrgURL, "--project", l.Project, "--repository", l.Repo}
}

// orgArgs are the flags for commands addressed by PR id (show, update,
// reviewer list, policy list), which accept only an organization.
func (l AzureLocation) orgArgs() []string {
	return []string{"--organization", l.OrgURL}
}

func (p *azureProvider) az(ctx context.Context, dir string, args ...string) ([]byte, error) {
	args = append(args, "--output", "json")
	return run(ctx, firstDir(dir, p.opts.RepoDir), "az", args...)
}

func boolStr(b bool) string { return strconv.FormatBool(b) }

func (p *azureProvider) CreatePR(ctx context.Context, req CreatePRRequest) (*PR, error) {
	if req.Head == "" || req.Base == "" {
		return nil, fmt.Errorf("create PR: head and base are required")
	}
	method, err := normalizeMethod(req.MergeMethod)
	if err != nil {
		return nil, err
	}
	loc, err := p.resolve(ctx, req.RepoDir)
	if err != nil {
		return nil, err
	}
	dir := firstDir(req.RepoDir, p.opts.RepoDir)
	if existing, err := p.find(ctx, dir, loc, req.Head, req.Base); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	args := []string{"repos", "pr", "create",
		"--source-branch", req.Head, "--target-branch", req.Base,
		"--title", req.Title, "--description", req.Body,
		"--draft", boolStr(req.Draft)}
	if req.AutoComplete {
		args = append(args, "--auto-complete", "true", "--squash", boolStr(method == MethodSquash))
	}
	args = append(args, loc.scopeArgs()...)
	out, err := p.az(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var a azPR
	if err := json.Unmarshal(out, &a); err != nil {
		return nil, fmt.Errorf("parsing az repos pr create output: %w", err)
	}
	return a.toPR(loc.webBase()), nil
}

func (l AzureLocation) webBase() string {
	return fmt.Sprintf("%s/%s/_git/%s", l.OrgURL, urlPathEscape(l.Project), urlPathEscape(l.Repo))
}

func urlPathEscape(s string) string { return strings.ReplaceAll(s, " ", "%20") }

func (p *azureProvider) FindPR(ctx context.Context, head, base string) (*PR, error) {
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	return p.find(ctx, p.opts.RepoDir, loc, head, base)
}

func (p *azureProvider) find(ctx context.Context, dir string, loc AzureLocation, head, base string) (*PR, error) {
	args := []string{"repos", "pr", "list", "--source-branch", head, "--target-branch", base,
		"--status", "active"}
	args = append(args, loc.scopeArgs()...)
	out, err := p.az(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var list []azPR
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parsing az repos pr list output: %w", err)
	}
	for _, a := range list {
		pr := a.toPR(loc.webBase())
		if pr.Head == normalizeRef(head) && pr.Base == normalizeRef(base) && pr.State == StateOpen {
			return pr, nil
		}
	}
	return nil, nil
}

func (p *azureProvider) GetPR(ctx context.Context, id string) (*PR, error) {
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	a, err := p.show(ctx, loc, id)
	if err != nil {
		return nil, err
	}
	return a.toPR(loc.webBase()), nil
}

func (p *azureProvider) UpdatePRBody(ctx context.Context, pr *PR, body string) error {
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return err
	}
	args := append([]string{"repos", "pr", "update", "--id", pr.ID, "--description", body}, loc.orgArgs()...)
	_, err = p.az(ctx, "", args...)
	return err
}

func (p *azureProvider) show(ctx context.Context, loc AzureLocation, id string) (*azPR, error) {
	args := append([]string{"repos", "pr", "show", "--id", id}, loc.orgArgs()...)
	out, err := p.az(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	var a azPR
	if err := json.Unmarshal(out, &a); err != nil {
		return nil, fmt.Errorf("parsing az repos pr show output: %w", err)
	}
	return &a, nil
}

// azPolicy is one entry of `az repos pr policy list`.
type azPolicy struct {
	Status        string `json:"status"` // approved | rejected | running | queued | notApplicable | broken
	Configuration struct {
		IsBlocking bool `json:"isBlocking"`
		Type       struct {
			DisplayName string `json:"displayName"`
		} `json:"type"`
		Settings struct {
			DisplayName string `json:"displayName"`
		} `json:"settings"`
	} `json:"configuration"`
}

func (a azPolicy) name() string {
	if n := a.Configuration.Settings.DisplayName; n != "" {
		return n
	}
	return a.Configuration.Type.DisplayName
}

// isReviewPolicy reports reviewer-count policies, which belong to ReviewState.
func (a azPolicy) isReviewPolicy() bool {
	n := strings.ToLower(a.Configuration.Type.DisplayName)
	return strings.Contains(n, "minimum number of reviewers") || strings.Contains(n, "required reviewers")
}

func (p *azureProvider) Checks(ctx context.Context, pr *PR) (CheckSummary, error) {
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return CheckSummary{}, err
	}
	args := append([]string{"repos", "pr", "policy", "list", "--id", pr.ID}, loc.orgArgs()...)
	out, err := p.az(ctx, "", args...)
	if err != nil {
		return CheckSummary{}, err
	}
	var pols []azPolicy
	if err := json.Unmarshal(out, &pols); err != nil {
		return CheckSummary{}, fmt.Errorf("parsing az repos pr policy list output: %w", err)
	}
	return summarizeAzurePolicies(pols), nil
}

func summarizeAzurePolicies(pols []azPolicy) CheckSummary {
	s := CheckSummary{State: ChecksNone}
	pending, seen := false, false
	for _, pol := range pols {
		if pol.isReviewPolicy() {
			continue
		}
		switch strings.ToLower(pol.Status) {
		case "approved":
			seen = true
		case "notapplicable":
			// does not count
		case "running", "queued":
			seen, pending = true, true
		case "rejected", "broken":
			if pol.Configuration.IsBlocking {
				seen = true
				s.Failing = append(s.Failing, pol.name())
			}
		}
	}
	switch {
	case len(s.Failing) > 0:
		s.State = ChecksFailing
	case pending:
		s.State = ChecksPending
	case seen:
		s.State = ChecksPassing
	}
	return s
}

func (p *azureProvider) ReviewState(ctx context.Context, pr *PR) (string, error) {
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return "", err
	}
	args := append([]string{"repos", "pr", "reviewer", "list", "--id", pr.ID}, loc.orgArgs()...)
	out, err := p.az(ctx, "", args...)
	if err != nil {
		return "", err
	}
	var rs []azReviewer
	if err := json.Unmarshal(out, &rs); err != nil {
		return "", fmt.Errorf("parsing az repos pr reviewer list output: %w", err)
	}
	return reviewStateFromVotes(rs), nil
}

// reviewStateFromVotes: any vote <= -10 rejects; otherwise approved needs at
// least one vote >= 10 and every required (non-group) reviewer at >= 10.
func reviewStateFromVotes(rs []azReviewer) string {
	approvals := 0
	requiredOK := true
	for _, r := range rs {
		if r.Vote <= voteReject {
			return ReviewRejected
		}
		if r.Vote >= voteApprove {
			approvals++
		} else if r.IsRequired && !r.IsContainer {
			requiredOK = false
		}
	}
	if approvals > 0 && requiredOK {
		return ReviewApproved
	}
	return ReviewPending
}

func (p *azureProvider) Merge(ctx context.Context, pr *PR, opts MergeOptions) error {
	method, err := normalizeMethod(opts.Method)
	if err != nil {
		return err
	}
	loc, err := p.resolve(ctx, "")
	if err != nil {
		return err
	}
	if opts.ExpectHead != "" {
		// az has no match-head flag; check immediately before completing.
		cur, err := p.show(ctx, loc, pr.ID)
		if err != nil {
			return err
		}
		if cur.LastMergeSourceCommit == nil || cur.LastMergeSourceCommit.CommitID != opts.ExpectHead {
			return fmt.Errorf("%w: PR %s head is not %s", ErrHeadMoved, pr.ID, opts.ExpectHead)
		}
	}
	args := []string{"repos", "pr", "update", "--id", pr.ID, "--status", "completed",
		"--squash", boolStr(method == MethodSquash)}
	args = append(args, loc.orgArgs()...)
	if _, err := p.az(ctx, "", args...); err != nil {
		return err
	}

	// Completion is asynchronous: poll until status is completed.
	deadline := time.NewTimer(p.opts.MergeTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(p.opts.MergePollInterval)
	defer tick.Stop()
	for {
		cur, err := p.show(ctx, loc, pr.ID)
		if err != nil {
			return err
		}
		got := cur.toPR(loc.webBase())
		switch got.State {
		case StateMerged:
			pr.State, pr.MergeCommit = got.State, got.MergeCommit
			return nil
		case StateClosed:
			return fmt.Errorf("%w: PR %s was abandoned", ErrNotMerged, pr.ID)
		}
		switch strings.ToLower(cur.MergeStatus) {
		case "conflicts", "rejectedbypolicy", "failure":
			return fmt.Errorf("%w: PR %s merge status %s", ErrNotMerged, pr.ID, cur.MergeStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: PR %s not completed within %s", ErrNotMerged, pr.ID, p.opts.MergeTimeout)
		case <-tick.C:
		}
	}
}
