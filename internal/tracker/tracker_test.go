package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

const azQueryJSON = `[
 {"fields":{"System.Id":12,"System.Title":"Tenant credits","System.State":"New","System.Tags":"gt-ready; billing","System.WorkItemType":"Feature"},
  "id":12,"rel":null,"url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/12"},
 {"fields":{"System.Id":15,"System.Title":"Other","System.State":"New","System.Tags":"gt-ready-not","System.WorkItemType":"Feature"},
  "id":15,"rel":null,"url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/15"}]`

const azShow12 = `{"id":12,"rev":3,
 "fields":{"System.Title":"Tenant credits","System.State":"New","System.Tags":"gt-ready; billing",
  "System.Description":"<div>Add <b>credits</b> &amp; limits<br>second line</div>"},
 "relations":[
  {"rel":"System.LinkTypes.Hierarchy-Forward","url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/13","attributes":{"name":"Child"}},
  {"rel":"System.LinkTypes.Hierarchy-Forward","url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/14","attributes":{"name":"Child"}},
  {"rel":"System.LinkTypes.Related","url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/99","attributes":{"name":"Related"}}],
 "url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/12",
 "_links":{"html":{"href":"https://dev.azure.com/geeksclub/gt-pr-test/_workitems/edit/12"}}}`

const azShow13 = `{"id":13,"fields":{"System.Title":"API part","System.State":"New","System.Tags":"rig:api","System.Description":"<p>do api</p>"},"relations":[{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"https://dev.azure.com/geeksclub/_apis/wit/workItems/12"}]}`
const azShow14 = `{"id":14,"fields":{"System.Title":"Web part","System.State":"Resolved","System.Tags":"rig:web"}}`

func azureCfg() *config.TrackerConfig {
	return &config.TrackerConfig{Kind: "azure", Org: "geeksclub", Project: "gt-pr-test", ActiveState: "Active", DoneState: "Resolved"}
}

func TestAzureListReadyAndGet(t *testing.T) {
	f := newFake().
		on("boards query", azQueryJSON).
		on("--id 12 ", azShow12).on("--id 13 ", azShow13).on("--id 14 ", azShow14)
	tr, err := New(azureCfg(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	items, err := tr.ListReady(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != "12" || items[0].State != "New" {
		t.Fatalf("ListReady = %+v, %v", items, err)
	}
	q := f.calls[0]
	for _, want := range []string{"az boards query", "--wiql", "--organization https://dev.azure.com/geeksclub", "--project gt-pr-test", "--output json", "'gt-ready'"} {
		if !strings.Contains(q, want) {
			t.Errorf("query call %q lacks %q", q, want)
		}
	}

	it, err := tr.Get(context.Background(), "12")
	if err != nil {
		t.Fatal(err)
	}
	if it.Description != "Add credits & limits\nsecond line" {
		t.Errorf("description = %q", it.Description)
	}
	if it.URL != "https://dev.azure.com/geeksclub/gt-pr-test/_workitems/edit/12" {
		t.Errorf("url = %q", it.URL)
	}
	if len(it.Children) != 2 || it.Children[0].ID != "13" || it.Children[0].Tags[0] != "rig:api" || it.Children[1].State != "Resolved" {
		t.Fatalf("children = %+v", it.Children)
	}
	if it.Children[0].URL != "https://dev.azure.com/geeksclub/gt-pr-test/_workitems/edit/13" {
		t.Errorf("fallback child url = %q", it.Children[0].URL)
	}
	for _, c := range f.calls {
		if !strings.Contains(c, "--organization https://dev.azure.com/geeksclub") {
			t.Errorf("call without --organization: %s", c)
		}
	}
	if len(f.writes()) != 0 {
		t.Errorf("reads wrote: %v", f.writes())
	}
}

func TestAzureSetState(t *testing.T) {
	f := newFake().on("work-item update", `{"id":12}`)
	tr, _ := New(azureCfg(), f.run)
	if err := tr.SetState(context.Background(), "12", "Active"); err != nil {
		t.Fatal(err)
	}
	want := "az boards work-item update --id 12 --state Active --organization https://dev.azure.com/geeksclub --output json"
	if len(f.calls) != 1 || f.calls[0] != want {
		t.Errorf("call = %v, want %s", f.calls, want)
	}
	for _, bad := range []string{"Closed", "New"} {
		if err := tr.SetState(context.Background(), "12", bad); err == nil {
			t.Errorf("state %q should be refused", bad)
		}
	}
	if err := tr.SetState(context.Background(), "--open", "Active"); err == nil {
		t.Error("flag-like id accepted")
	}
	if len(f.calls) != 1 {
		t.Errorf("refused calls reached the CLI: %v", f.calls)
	}
}

const ghListJSON = `[{"number":7,"title":"Credits epic","body":"Epic body","state":"OPEN","url":"https://github.com/o/r/issues/7","labels":[{"name":"gt-ready","color":"ededed"}]}]`
const ghView7 = `{"number":7,"title":"Credits epic","body":"Epic body\n- [ ] #99","state":"OPEN","url":"https://github.com/o/r/issues/7","labels":[{"name":"gt-ready"},{"name":"gt-active"}],"subIssues":[{"number":8},{"number":9}]}`
const ghView8 = `{"number":8,"title":"API","body":"api work","state":"OPEN","url":"https://github.com/o/r/issues/8","labels":[{"name":"rig:api"}]}`
const ghView9 = `{"number":9,"title":"Web","body":"","state":"CLOSED","url":"https://github.com/o/r/issues/9","labels":[]}`

func githubCfg() *config.TrackerConfig {
	return &config.TrackerConfig{Kind: "github", Repo: "o/r"}
}

func TestGitHubListGetAndWrites(t *testing.T) {
	f := newFake().
		on("issue list", ghListJSON).
		on("issue view 7 ", ghView7).on("issue view 8 ", ghView8).on("issue view 9 ", ghView9).
		on("issue edit", "").on("issue close", "")
	tr, err := New(githubCfg(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	items, err := tr.ListReady(context.Background())
	if err != nil || len(items) != 1 || items[0].State != GitHubOpen || items[0].Tags[0] != "gt-ready" {
		t.Fatalf("ListReady = %+v, %v", items, err)
	}
	if !strings.Contains(f.calls[0], "--repo o/r") || !strings.Contains(f.calls[0], "--label gt-ready") || !strings.Contains(f.calls[0], "--json") {
		t.Errorf("list call = %s", f.calls[0])
	}
	it, err := tr.Get(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if it.State != GitHubActive {
		t.Errorf("state = %q, want active (gt-active label)", it.State)
	}
	// Sub-issues win over the task list in the body (#99 is not a child).
	if len(it.Children) != 2 || it.Children[0].ID != "8" || it.Children[1].State != GitHubClosed {
		t.Fatalf("children = %+v", it.Children)
	}

	if err := tr.SetState(context.Background(), "7", GitHubActive); err != nil {
		t.Fatal(err)
	}
	if err := tr.SetState(context.Background(), "7", GitHubClosed); err != nil {
		t.Fatal(err)
	}
	w := f.writes()
	if len(w) != 2 || w[0] != "gh issue edit 7 --add-label gt-active --repo o/r" || w[1] != "gh issue close 7 --repo o/r" {
		t.Errorf("writes = %v", w)
	}
	if err := tr.SetState(context.Background(), "7", "open"); err == nil {
		t.Error("state open should be refused")
	}
}

func TestGitHubTaskListFallback(t *testing.T) {
	parent := `{"number":7,"title":"E","body":"Plan\n- [ ] #8\n- [x] #9\n* [ ] #8\ntext #10","state":"OPEN","url":"u","labels":[],"subIssues":[]}`
	f := newFake().on("issue view 7 ", parent).on("issue view 8 ", ghView8).on("issue view 9 ", ghView9)
	tr, _ := New(githubCfg(), f.run)
	it, err := tr.Get(context.Background(), "7")
	if err != nil || len(it.Children) != 2 || it.Children[0].ID != "8" || it.Children[1].ID != "9" {
		t.Fatalf("children = %+v, %v", it, err)
	}
}
