package tracker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectfeature"
)

func syncTown(t *testing.T, ref *projectfeature.TrackerRef, state string, tasks ...projectfeature.Task) (string, *projectfeature.Record) {
	t.Helper()
	town := t.TempDir()
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}
	rec := &projectfeature.Record{
		ID: "xe-1", Project: "shop", Branch: "feat/xe-1-x", State: state, Tracker: ref,
		Rigs:  map[string]projectfeature.RigState{"api": {State: projectfeature.RigBranch}},
		Tasks: tasks,
	}
	if err := projectfeature.Create(town, rec); err != nil {
		t.Fatal(err)
	}
	return town, rec
}

func azRef() *projectfeature.TrackerRef {
	return &projectfeature.TrackerRef{Kind: "azure", ID: "12", ImportState: "New"}
}

func azStateJSON(state string) string {
	return `{"id":12,"fields":{"System.Title":"x","System.State":"` + state + `"}}`
}

func reload(t *testing.T, town string) *projectfeature.Record {
	t.Helper()
	rec, err := projectfeature.Load(town, "shop", "xe-1")
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestReconcileActiveThenNoWrite(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().on("work-item show", azStateJSON("New")).on("work-item update", "{}")
	tr, _ := New(azureCfg(), f.run)
	status := map[string]string{"ap-1": "in_progress"}

	rep, err := Reconcile(context.Background(), town, rec, status, tr, false)
	if err != nil || len(rep.Changes) != 1 || rep.Changes[0].To != "Active" || rep.Changes[0].From != "New" {
		t.Fatalf("rep = %+v, %v", rep, err)
	}
	if w := f.writes(); len(w) != 1 || !strings.Contains(w[0], "--id 12 --state Active") {
		t.Fatalf("writes = %v", w)
	}
	if reload(t, town).Tracker.Synced != DesiredActive {
		t.Error("synced state not stored")
	}

	// Second tick: nothing to do, no tracker call of any kind.
	n := len(f.calls)
	rec = reload(t, town)
	rep, err = Reconcile(context.Background(), town, rec, status, tr, false)
	if err != nil || len(rep.Changes) != 0 || len(f.calls) != n {
		t.Fatalf("second tick: rep=%+v err=%v calls=%v", rep, err, f.calls[n:])
	}
}

func TestReconcileNoDesiredMakesNoCall(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake()
	tr, _ := New(azureCfg(), f.run)
	for _, st := range []string{"open", "hooked_not"} {
		if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": st}, tr, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestReconcileHookedCountsAsActive(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().on("work-item show", azStateJSON("New")).on("work-item update", "{}")
	tr, _ := New(azureCfg(), f.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "hooked"}, tr, false); err != nil {
		t.Fatal(err)
	}
	if len(f.writes()) != 1 {
		t.Errorf("writes = %v", f.writes())
	}
}

func TestReconcileLandingStartedIsActive(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateLanding, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().on("work-item show", azStateJSON("New")).on("work-item update", "{}")
	tr, _ := New(azureCfg(), f.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "open"}, tr, false); err != nil {
		t.Fatal(err)
	}
	if len(f.writes()) != 1 {
		t.Errorf("writes = %v", f.writes())
	}
}

func TestReconcileLandedDone(t *testing.T) {
	for _, cur := range []string{"New", "Active"} {
		town, rec := syncTown(t, azRef(), projectfeature.StateLanded, projectfeature.Task{ID: "ap-1", Rig: "api"})
		f := newFake().on("work-item show", azStateJSON(cur)).on("work-item update", "{}")
		tr, _ := New(azureCfg(), f.run)
		rep, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "closed"}, tr, false)
		if err != nil || len(rep.Changes) != 1 || rep.Changes[0].To != "Resolved" {
			t.Fatalf("from %s: rep = %+v, %v", cur, rep, err)
		}
		if w := f.writes(); len(w) != 1 || !strings.Contains(w[0], "--state Resolved") {
			t.Errorf("from %s: writes = %v", cur, w)
		}
	}
}

func TestReconcileForwardOnly(t *testing.T) {
	cases := []struct {
		name    string
		landed  bool
		current string
		left    bool // logged as left alone
	}{
		{"already done, wants done", true, "Resolved", false},
		{"already done, wants active", false, "Resolved", false},
		{"human moved to other state, wants active", false, "Blocked", true},
		{"human moved to other state, wants done", true, "Removed", true},
		{"already active, wants active", false, "Active", false},
	}
	for _, c := range cases {
		state := projectfeature.StateOpen
		if c.landed {
			state = projectfeature.StateLanded
		}
		town, rec := syncTown(t, azRef(), state, projectfeature.Task{ID: "ap-1", Rig: "api"})
		f := newFake().on("work-item show", azStateJSON(c.current)).on("work-item update", "{}")
		tr, _ := New(azureCfg(), f.run)
		rep, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "in_progress"}, tr, false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(f.writes()) != 0 || len(rep.Changes) != 0 {
			t.Errorf("%s: wrote %v", c.name, f.writes())
		}
		if (len(rep.Left) == 1) != c.left {
			t.Errorf("%s: left = %v", c.name, rep.Left)
		}
		// Handled: the next tick makes no call even for a left-alone item.
		n := len(f.calls)
		if _, err := Reconcile(context.Background(), town, reload(t, town), map[string]string{"ap-1": "in_progress"}, tr, false); err != nil || len(f.calls) != n {
			t.Errorf("%s: second tick called the tracker: %v", c.name, f.calls[n:])
		}
	}
}

func TestReconcileNeverMovesBack(t *testing.T) {
	ref := azRef()
	ref.Synced = DesiredDone
	town, rec := syncTown(t, ref, projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake()
	tr, _ := New(azureCfg(), f.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "in_progress"}, tr, false); err != nil || len(f.calls) != 0 {
		t.Errorf("calls = %v err = %v", f.calls, err)
	}
}

func TestReconcileFailureRetries(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().on("work-item show", azStateJSON("New")).on("work-item update", "{}")
	f.fail["work-item update"] = errors.New("az: boom")
	tr, _ := New(azureCfg(), f.run)
	status := map[string]string{"ap-1": "in_progress"}
	_, err := Reconcile(context.Background(), town, rec, status, tr, false)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if reload(t, town).Tracker.Synced != "" {
		t.Error("failed write was recorded as synced")
	}
	delete(f.fail, "work-item update")
	rep, err := Reconcile(context.Background(), town, reload(t, town), status, tr, false)
	if err != nil || len(rep.Changes) != 1 {
		t.Fatalf("retry: %+v %v", rep, err)
	}

	// A failed read is also an error that leaves the record alone.
	town, rec = syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f2 := newFake().on("work-item show", "")
	f2.fail["work-item show"] = errors.New("net down")
	tr2, _ := New(azureCfg(), f2.run)
	if _, err := Reconcile(context.Background(), town, rec, status, tr2, false); err == nil {
		t.Error("read failure not reported")
	}
}

func TestReconcileDryRunWritesNothing(t *testing.T) {
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().on("work-item show", azStateJSON("New"))
	tr, _ := New(azureCfg(), f.run)
	rep, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "in_progress"}, tr, true)
	if err != nil || len(rep.Changes) != 1 {
		t.Fatalf("rep = %+v %v", rep, err)
	}
	if len(f.writes()) != 0 {
		t.Errorf("dry run wrote: %v", f.writes())
	}
	if reload(t, town).Tracker.Synced != "" {
		t.Error("dry run stored synced state")
	}
}

func TestReconcileChildTasks(t *testing.T) {
	child := func(id, cid, imp string) projectfeature.Task {
		return projectfeature.Task{ID: id, Rig: "api", Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: cid, ImportState: imp}}
	}
	// Feature item itself stays untouched (no desired state beyond none);
	// only the closed task's child moves, and an untracked task is ignored.
	town, rec := syncTown(t, azRef(), projectfeature.StateOpen,
		child("ap-1", "13", "New"), child("ap-2", "14", "New"), projectfeature.Task{ID: "ap-3", Rig: "api"})
	f := newFake().
		on("--id 12 ", azStateJSON("New")).on("--id 13 ", `{"id":13,"fields":{"System.State":"New"}}`).
		on("--id 14 ", `{"id":14,"fields":{"System.State":"New"}}`).on("work-item update", "{}")
	tr, _ := New(azureCfg(), f.run)
	status := map[string]string{"ap-1": "closed", "ap-2": "in_progress", "ap-3": "closed"}
	rep, err := Reconcile(context.Background(), town, rec, status, tr, false)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, w := range f.writes() {
		kinds = append(kinds, w)
	}
	// feature -> Active (work started), child 13 -> Resolved. Child 14 is not closed.
	if len(kinds) != 2 || !strings.Contains(kinds[0], "--id 12 --state Active") || !strings.Contains(kinds[1], "--id 13 --state Resolved") {
		t.Fatalf("writes = %v (rep %+v)", kinds, rep)
	}
	got := reload(t, town)
	if got.Task("ap-1").Tracker.Synced != DesiredDone || got.Task("ap-2").Tracker.Synced != "" {
		t.Errorf("child synced = %q / %q", got.Task("ap-1").Tracker.Synced, got.Task("ap-2").Tracker.Synced)
	}
}

func TestReconcileMergingChildIsNotDone(t *testing.T) {
	task := projectfeature.Task{ID: "ap-1", Rig: "api", Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: "13", ImportState: "New"}}
	town, rec := syncTown(t, nil, projectfeature.StateOpen, task)
	f := newFake()
	tr, _ := New(azureCfg(), f.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": projectfeature.StatusMerging}, tr, false); err != nil || len(f.calls) != 0 {
		t.Errorf("calls = %v err = %v", f.calls, err)
	}
}

func TestReconcileGitHub(t *testing.T) {
	ref := &projectfeature.TrackerRef{Kind: "github", ID: "7", ImportState: "open"}
	town, rec := syncTown(t, ref, projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f := newFake().
		on("issue view 7 --json number,state,labels", `{"number":7,"state":"OPEN","labels":[]}`).
		on("issue edit", "").on("issue close", "")
	tr, _ := New(githubCfg(), f.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "in_progress"}, tr, false); err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 1 || w[0] != "gh issue edit 7 --add-label gt-active --repo o/r" {
		t.Fatalf("writes = %v", w)
	}

	// Human closed it meanwhile: leave alone, never reopen or relabel.
	town, rec = syncTown(t, ref, projectfeature.StateOpen, projectfeature.Task{ID: "ap-1", Rig: "api"})
	f2 := newFake().on("issue view 7", `{"number":7,"state":"CLOSED","labels":[]}`)
	tr2, _ := New(githubCfg(), f2.run)
	if _, err := Reconcile(context.Background(), town, rec, map[string]string{"ap-1": "in_progress"}, tr2, false); err != nil || len(f2.writes()) != 0 {
		t.Errorf("closed issue: err=%v writes=%v", err, f2.writes())
	}

	// Landed: close with no comment.
	rec2 := &projectfeature.Record{ID: "xe-1", Project: "shop", State: projectfeature.StateLanded, Tracker: &projectfeature.TrackerRef{Kind: "github", ID: "7", ImportState: "open", Synced: DesiredActive}}
	f3 := newFake().on("issue view 7", `{"number":7,"state":"OPEN","labels":[{"name":"gt-active"}]}`).on("issue close", "")
	tr3, _ := New(githubCfg(), f3.run)
	town, _ = syncTown(t, rec2.Tracker, projectfeature.StateLanded)
	if _, err := Reconcile(context.Background(), town, reload(t, town), nil, tr3, false); err != nil {
		t.Fatal(err)
	}
	if w := f3.writes(); len(w) != 1 || w[0] != "gh issue close 7 --repo o/r" {
		t.Errorf("writes = %v", w)
	}
}

func TestPlanChildren(t *testing.T) {
	kids := []Child{
		{ID: "1", Title: "a", State: "New", Tags: []string{"rig:api"}},
		{ID: "2", Title: "b", State: "New", Tags: []string{"Rig:web", "x"}},
		{ID: "3", Title: "c", State: "New"},
		{ID: "4", Title: "d", State: "New", Tags: []string{"rig:nope"}},
		{ID: "5", Title: "e", State: "Resolved", Tags: []string{"rig:api"}},
	}
	p := PlanChildren(nil, kids, []string{"api", "web"}, "Resolved")
	if len(p.Create) != 2 || p.Create[0].Rig != "api" || p.Create[1].Rig != "web" {
		t.Errorf("create = %+v", p.Create)
	}
	if len(p.Unassigned) != 2 || p.Unassigned[0].ID != "3" || p.Unassigned[1].ID != "4" {
		t.Errorf("unassigned = %+v", p.Unassigned)
	}
	if len(p.Done) != 1 || p.Done[0].ID != "5" {
		t.Errorf("done = %+v", p.Done)
	}

	// One rig: everything goes there, tags are not needed.
	p = PlanChildren(nil, kids[:4], []string{"api"}, "Resolved")
	if len(p.Create) != 4 || len(p.Unassigned) != 0 {
		t.Errorf("single rig = %+v", p)
	}

	// Known children are never planned again.
	rec := &projectfeature.Record{Tasks: []projectfeature.Task{{ID: "ap-1", Tracker: &projectfeature.TrackerRef{ID: "1"}}}}
	p = PlanChildren(rec, kids[:2], []string{"api", "web"}, "Resolved")
	if len(p.Existing) != 1 || len(p.Create) != 1 || p.Create[0].Child.ID != "2" {
		t.Errorf("re-import = %+v", p)
	}
}
