package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// GitHub Issues via `gh issue ... --json`.
//
// Children are the issue's sub-issues (the `subIssues` JSON field of
// `gh issue view`). Only their numbers are read from that field; each one is
// then viewed on its own for title, body, state and labels. When an issue has
// no sub-issues, task-list links in its body ("- [ ] #12") are used instead.

type githubTracker struct {
	cfg config.TrackerConfig
	run Runner
}

func (g *githubTracker) Kind() string        { return config.TrackerGitHub }
func (g *githubTracker) ActiveState() string { return GitHubActive }
func (g *githubTracker) DoneState() string   { return GitHubClosed }

func (g *githubTracker) gh(ctx context.Context, args ...string) ([]byte, error) {
	args = append(args, "--repo", g.cfg.Repo)
	return g.run(ctx, "gh", args...)
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"` // OPEN | CLOSED
	URL    string `json:"url"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	SubIssues []struct {
		Number int `json:"number"`
	} `json:"subIssues"`
}

const ghFields = "number,title,body,state,url,labels"

func (i ghIssue) toItem() Item {
	it := Item{ID: strconv.Itoa(i.Number), Title: i.Title, Description: strings.TrimSpace(i.Body), URL: i.URL}
	for _, l := range i.Labels {
		it.Tags = append(it.Tags, l.Name)
	}
	switch {
	case strings.EqualFold(i.State, "closed"):
		it.State = GitHubClosed
	case hasTag(it.Tags, ActiveLabel):
		it.State = GitHubActive
	default:
		it.State = GitHubOpen
	}
	return it
}

func (g *githubTracker) ListReady(ctx context.Context) ([]Item, error) {
	out, err := g.gh(ctx, "issue", "list", "--label", g.cfg.ReadyLabel, "--state", "open", "--limit", "100", "--json", ghFields)
	if err != nil {
		return nil, err
	}
	var rows []ghIssue
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing gh issue list: %w", err)
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.toItem())
	}
	return items, nil
}

func (g *githubTracker) view(ctx context.Context, id, fields string) (*ghIssue, error) {
	if !validItemID(id) {
		return nil, fmt.Errorf("invalid issue number %q", id)
	}
	out, err := g.gh(ctx, "issue", "view", id, "--json", fields)
	if err != nil {
		return nil, err
	}
	var i ghIssue
	if err := json.Unmarshal(out, &i); err != nil {
		return nil, fmt.Errorf("parsing gh issue view: %w", err)
	}
	return &i, nil
}

// taskListRef matches "- [ ] #12" and "- [x] #12" (also "* ").
var taskListRef = regexp.MustCompile(`(?m)^\s*[-*]\s+\[[ xX]\]\s+#(\d+)\b`)

func (g *githubTracker) Get(ctx context.Context, id string) (*Item, error) {
	i, err := g.view(ctx, id, ghFields+",subIssues")
	if err != nil {
		return nil, err
	}
	it := i.toItem()
	var nums []string
	for _, s := range i.SubIssues {
		nums = append(nums, strconv.Itoa(s.Number))
	}
	if len(nums) == 0 {
		for _, m := range taskListRef.FindAllStringSubmatch(i.Body, -1) {
			nums = append(nums, m[1])
		}
	}
	seen := map[string]bool{it.ID: true}
	for _, n := range nums {
		if seen[n] {
			continue
		}
		seen[n] = true
		c, err := g.view(ctx, n, ghFields)
		if err != nil {
			return nil, fmt.Errorf("child #%s: %w", n, err)
		}
		ci := c.toItem()
		it.Children = append(it.Children, Child{ID: ci.ID, Title: ci.Title, Description: ci.Description, State: ci.State, URL: ci.URL, Tags: ci.Tags})
	}
	return &it, nil
}

func (g *githubTracker) State(ctx context.Context, id string) (string, error) {
	i, err := g.view(ctx, id, "number,state,labels")
	if err != nil {
		return "", err
	}
	return i.toItem().State, nil
}

func (g *githubTracker) SetState(ctx context.Context, id, state string) error {
	if !validItemID(id) {
		return fmt.Errorf("invalid issue number %q", id)
	}
	var err error
	switch state {
	case GitHubActive:
		_, err = g.gh(ctx, "issue", "edit", id, "--add-label", ActiveLabel)
	case GitHubClosed:
		_, err = g.gh(ctx, "issue", "close", id) // no --comment, no --reason
	default:
		return fmt.Errorf("refusing to set state %q: only %q and %q are written", state, GitHubActive, GitHubClosed)
	}
	return err
}
