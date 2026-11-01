package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// Azure Boards via `az boards ... --output json` (azure-devops extension).
// Every call passes --organization, so az's default organization is never
// used.

type azureTracker struct {
	cfg config.TrackerConfig
	run Runner
}

func (a *azureTracker) Kind() string        { return config.TrackerAzure }
func (a *azureTracker) ActiveState() string { return a.cfg.ActiveState }
func (a *azureTracker) DoneState() string   { return a.cfg.DoneState }

func (a *azureTracker) az(ctx context.Context, args ...string) ([]byte, error) {
	args = append(args, "--organization", a.cfg.OrgURL(), "--output", "json")
	return a.run(ctx, "az", args...)
}

type azWorkItem struct {
	ID     int `json:"id"`
	Fields struct {
		Title       string `json:"System.Title"`
		Description string `json:"System.Description"`
		State       string `json:"System.State"`
		Tags        string `json:"System.Tags"`
	} `json:"fields"`
	Relations []struct {
		Rel string `json:"rel"`
		URL string `json:"url"`
	} `json:"relations"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"_links"`
}

func (w azWorkItem) tags() []string {
	var out []string
	for _, t := range strings.Split(w.Fields.Tags, ";") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (a *azureTracker) webURL(w azWorkItem) string {
	if w.Links.HTML.Href != "" {
		return w.Links.HTML.Href
	}
	return fmt.Sprintf("%s/%s/_workitems/edit/%d", a.cfg.OrgURL(), url.PathEscape(a.cfg.Project), w.ID)
}

func (a *azureTracker) toItem(w azWorkItem) Item {
	return Item{
		ID:          fmt.Sprint(w.ID),
		Title:       w.Fields.Title,
		Description: htmlToText(w.Fields.Description),
		State:       w.Fields.State,
		URL:         a.webURL(w),
		Tags:        w.tags(),
	}
}

// wiqlString quotes a WIQL string literal.
func wiqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (a *azureTracker) ListReady(ctx context.Context) ([]Item, error) {
	wiql := "SELECT [System.Id], [System.Title], [System.State], [System.Tags] FROM WorkItems" +
		" WHERE [System.TeamProject] = " + wiqlString(a.cfg.Project) +
		" AND [System.Tags] CONTAINS " + wiqlString(a.cfg.ReadyTag) +
		" AND [System.State] <> " + wiqlString(a.cfg.DoneState) +
		" ORDER BY [System.Id]"
	out, err := a.az(ctx, "boards", "query", "--wiql", wiql, "--project", a.cfg.Project)
	if err != nil {
		return nil, err
	}
	var rows []azWorkItem
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing az boards query: %w", err)
	}
	items := make([]Item, 0, len(rows))
	for _, w := range rows {
		it := a.toItem(w)
		// The query's own filter is a substring match on tags; keep exact ones.
		if !hasTag(it.Tags, a.cfg.ReadyTag) {
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

func (a *azureTracker) show(ctx context.Context, id string) (*azWorkItem, error) {
	if !validItemID(id) {
		return nil, fmt.Errorf("invalid work item id %q", id)
	}
	out, err := a.az(ctx, "boards", "work-item", "show", "--id", id)
	if err != nil {
		return nil, err
	}
	var w azWorkItem
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, fmt.Errorf("parsing az boards work-item show: %w", err)
	}
	return &w, nil
}

const azChildRel = "System.LinkTypes.Hierarchy-Forward"

func (a *azureTracker) Get(ctx context.Context, id string) (*Item, error) {
	w, err := a.show(ctx, id)
	if err != nil {
		return nil, err
	}
	it := a.toItem(*w)
	for _, r := range w.Relations {
		if r.Rel != azChildRel {
			continue
		}
		cid := r.URL[strings.LastIndex(r.URL, "/")+1:]
		if !validItemID(cid) {
			continue
		}
		cw, err := a.show(ctx, cid)
		if err != nil {
			return nil, fmt.Errorf("child %s: %w", cid, err)
		}
		c := a.toItem(*cw)
		it.Children = append(it.Children, Child{ID: c.ID, Title: c.Title, Description: c.Description, State: c.State, URL: c.URL, Tags: c.Tags})
	}
	return &it, nil
}

func (a *azureTracker) State(ctx context.Context, id string) (string, error) {
	w, err := a.show(ctx, id)
	if err != nil {
		return "", err
	}
	return w.Fields.State, nil
}

func (a *azureTracker) SetState(ctx context.Context, id, state string) error {
	if !validItemID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	if state != a.cfg.ActiveState && state != a.cfg.DoneState {
		return fmt.Errorf("refusing to set state %q: only %q and %q are written", state, a.cfg.ActiveState, a.cfg.DoneState)
	}
	_, err := a.az(ctx, "boards", "work-item", "update", "--id", id, "--state", state)
	return err
}

var (
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</h[1-6]>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// htmlToText turns an Azure rich-text field into plain text.
func htmlToText(s string) string {
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
