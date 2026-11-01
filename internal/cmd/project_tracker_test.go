package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/tracker"
)

type cmdFake struct {
	calls []string
	reply map[string]string
	order []string
}

func (f *cmdFake) on(k, v string) *cmdFake { f.reply[k] = v; f.order = append(f.order, k); return f }

func (f *cmdFake) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	line := bin + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for _, k := range f.order {
		if strings.Contains(line, k) {
			return []byte(f.reply[k]), nil
		}
	}
	return nil, fmt.Errorf("unexpected call %s", line)
}

func (f *cmdFake) writes() (n int) {
	for _, c := range f.calls {
		if strings.Contains(c, "work-item update") || strings.Contains(c, "issue edit") || strings.Contains(c, "issue close") {
			n++
		}
	}
	return
}

func newCmdFake() *cmdFake { return &cmdFake{reply: map[string]string{}} }

const trAzShow12 = `{"id":12,"fields":{"System.Title":"Tenant credits","System.State":"New","System.Tags":"gt-ready","System.Description":"<p>Epic text</p>"},
 "relations":[{"rel":"System.LinkTypes.Hierarchy-Forward","url":"https://dev.azure.com/o/_apis/wit/workItems/13"},
              {"rel":"System.LinkTypes.Hierarchy-Forward","url":"https://dev.azure.com/o/_apis/wit/workItems/14"},
              {"rel":"System.LinkTypes.Hierarchy-Forward","url":"https://dev.azure.com/o/_apis/wit/workItems/15"}]}`
const trAzShow13 = `{"id":13,"fields":{"System.Title":"API part","System.State":"New","System.Tags":"rig:api","System.Description":"<p>do api</p>"}}`
const trAzShow14 = `{"id":14,"fields":{"System.Title":"Web part","System.State":"New","System.Tags":"rig:web"}}`
const trAzShow15 = `{"id":15,"fields":{"System.Title":"Loose part","System.State":"New","System.Tags":""}}`

func trackerCmdTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}
	return town
}

func trAzureTracker(t *testing.T, f *cmdFake) tracker.Tracker {
	t.Helper()
	tr, err := tracker.New(&config.TrackerConfig{Kind: "azure", Org: "o", Project: "p", ActiveState: "Active", DoneState: "Resolved"}, f.run)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// fakeImportOps creates records and tasks the way the real ones do, without
// beads or git.
func fakeImportOps(t *testing.T, created *int) importOps {
	t.Helper()
	next := 0
	return importOps{
		createFeature: func(town, project, title, desc string, rigs []string, branch string, ref *projectfeature.TrackerRef) (*projectfeature.Record, []projectfeature.BranchResult, error) {
			*created++
			rec := &projectfeature.Record{ID: "xe-100", Project: project, Title: title, Branch: "feat/xe-100", Rigs: map[string]projectfeature.RigState{}, Tracker: ref}
			for _, r := range rigs {
				rec.Rigs[r] = projectfeature.RigState{State: projectfeature.RigBranch}
			}
			if desc != "Epic text" {
				t.Errorf("epic description = %q", desc)
			}
			return rec, nil, projectfeature.Create(town, rec)
		},
		addTask: func(town string, rec *projectfeature.Record, rig, title, desc string, after []string, ref *projectfeature.TrackerRef) (string, error) {
			next++
			id := fmt.Sprintf("%s-%d", rig, next)
			_, err := projectfeature.Update(town, rec.Project, rec.ID, func(r *projectfeature.Record) error {
				return r.AddTask(projectfeature.Task{ID: id, Rig: rig, Title: title, Tracker: ref})
			})
			return id, err
		},
	}
}

func azImportFake() *cmdFake {
	return newCmdFake().on("--id 12 ", trAzShow12).on("--id 13 ", trAzShow13).on("--id 14 ", trAzShow14).on("--id 15 ", trAzShow15)
}

func TestTrackerImportMapsAndIsIdempotent(t *testing.T) {
	town := trackerCmdTown(t)
	f := azImportFake()
	tr := trAzureTracker(t, f)
	created := 0
	ops := fakeImportOps(t, &created)
	var out bytes.Buffer

	if err := importTrackerItem(context.Background(), &out, town, "shop", tr, "12", []string{"api", "web"}, false, ops); err != nil {
		t.Fatal(err)
	}
	rec, _ := projectfeature.Load(town, "shop", "xe-100")
	if created != 1 || len(rec.Tasks) != 2 {
		t.Fatalf("created=%d tasks=%+v", created, rec.Tasks)
	}
	if rec.Tracker == nil || rec.Tracker.ID != "12" || rec.Tracker.Kind != "azure" || rec.Tracker.ImportState != "New" {
		t.Errorf("feature ref = %+v", rec.Tracker)
	}
	if rec.Tasks[0].Rig != "api" || rec.Tasks[0].Tracker.ID != "13" || rec.Tasks[1].Rig != "web" || rec.Tasks[1].Tracker.ID != "14" {
		t.Errorf("tasks = %+v", rec.Tasks)
	}
	s := out.String()
	if !strings.Contains(s, "1 children unassigned and NOT created") || !strings.Contains(s, "15  Loose part") {
		t.Errorf("output lacks unassigned child:\n%s", s)
	}
	if f.writes() != 0 {
		t.Error("import wrote to the tracker")
	}

	// Re-import: nothing created, existing tasks untouched.
	out.Reset()
	if err := importTrackerItem(context.Background(), &out, town, "shop", tr, "12", nil, false, ops); err != nil {
		t.Fatal(err)
	}
	rec2, _ := projectfeature.Load(town, "shop", "xe-100")
	if created != 1 || len(rec2.Tasks) != 2 || !strings.Contains(out.String(), "already imported as feature xe-100") || !strings.Contains(out.String(), "2 children already tasks") {
		t.Errorf("re-import created=%d tasks=%d\n%s", created, len(rec2.Tasks), out.String())
	}

	// A child that gets its rig tag later is added; old tasks are unchanged.
	f.on("--id 15 ", strings.Replace(trAzShow15, `"System.Tags":""`, `"System.Tags":"rig:web"`, 1))
	f.order = append([]string{"--id 15 "}, f.order...)
	out.Reset()
	if err := importTrackerItem(context.Background(), &out, town, "shop", tr, "12", nil, false, ops); err != nil {
		t.Fatal(err)
	}
	rec3, _ := projectfeature.Load(town, "shop", "xe-100")
	if len(rec3.Tasks) != 3 || rec3.Tasks[0].ID != rec2.Tasks[0].ID || rec3.Tasks[2].Tracker.ID != "15" || rec3.Tasks[2].Rig != "web" {
		t.Errorf("tasks after re-import = %+v", rec3.Tasks)
	}
}

func TestTrackerImportSingleRigTakesAllChildren(t *testing.T) {
	town := trackerCmdTown(t)
	tr := trAzureTracker(t, azImportFake())
	created := 0
	var out bytes.Buffer
	if err := importTrackerItem(context.Background(), &out, town, "shop", tr, "12", []string{"api"}, false, fakeImportOps(t, &created)); err != nil {
		t.Fatal(err)
	}
	rec, _ := projectfeature.Load(town, "shop", "xe-100")
	if len(rec.Tasks) != 3 || strings.Contains(out.String(), "unassigned") {
		t.Errorf("tasks = %+v\n%s", rec.Tasks, out.String())
	}
}

func TestTrackerImportNoChildrenAndDryRun(t *testing.T) {
	town := trackerCmdTown(t)
	f := newCmdFake().on("--id 20 ", `{"id":20,"fields":{"System.Title":"Lonely","System.State":"New","System.Description":"Epic text"}}`)
	tr := trAzureTracker(t, f)
	created := 0
	var out bytes.Buffer
	if err := importTrackerItem(context.Background(), &out, town, "shop", tr, "20", []string{"api", "web"}, false, fakeImportOps(t, &created)); err != nil {
		t.Fatal(err)
	}
	rec, _ := projectfeature.Load(town, "shop", "xe-100")
	if len(rec.Tasks) != 0 || !strings.Contains(out.String(), "no children: feature created with no tasks") {
		t.Errorf("tasks=%v\n%s", rec.Tasks, out.String())
	}

	// Dry run on a fresh town: reads only, creates nothing.
	town2 := trackerCmdTown(t)
	tr2 := trAzureTracker(t, azImportFake())
	created = 0
	out.Reset()
	if err := importTrackerItem(context.Background(), &out, town2, "shop", tr2, "12", []string{"api", "web"}, true, fakeImportOps(t, &created)); err != nil {
		t.Fatal(err)
	}
	if recs, _ := projectfeature.List(town2, "shop"); created != 0 || len(recs) != 0 {
		t.Errorf("dry run created=%d records=%d", created, len(recs))
	}
	if !strings.Contains(out.String(), "Would create feature") || !strings.Contains(out.String(), "Would add task in rig api") {
		t.Errorf("dry run output:\n%s", out.String())
	}
}

func TestTrackerImportRequiresRigsAndRefusesDone(t *testing.T) {
	town := trackerCmdTown(t)
	tr := trAzureTracker(t, azImportFake())
	created := 0
	if err := importTrackerItem(context.Background(), &bytes.Buffer{}, town, "shop", tr, "12", nil, false, fakeImportOps(t, &created)); err == nil || !strings.Contains(err.Error(), "--rigs") {
		t.Errorf("err = %v", err)
	}
	f := newCmdFake().on("--id 30 ", `{"id":30,"fields":{"System.Title":"Done","System.State":"Resolved"}}`)
	if err := importTrackerItem(context.Background(), &bytes.Buffer{}, town, "shop", trAzureTracker(t, f), "30", []string{"api"}, false, fakeImportOps(t, &created)); err == nil {
		t.Error("done item imported")
	}
	if created != 0 {
		t.Error("created something")
	}
}

func TestTrackerListMarksImported(t *testing.T) {
	town := trackerCmdTown(t)
	rec := &projectfeature.Record{ID: "xe-9", Project: "shop", Branch: "b", Rigs: map[string]projectfeature.RigState{}, Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: "12"}}
	if err := projectfeature.Create(town, rec); err != nil {
		t.Fatal(err)
	}
	f := newCmdFake().on("boards query", `[{"id":12,"fields":{"System.Title":"One","System.State":"New","System.Tags":"gt-ready"}},{"id":13,"fields":{"System.Title":"Two","System.State":"New","System.Tags":"gt-ready"}}]`)
	tr := trAzureTracker(t, f)
	items, err := tr.ListReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printTrackerList(&out, town, "shop", tr, items); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "[imported as xe-9]") || strings.Contains(lines[1], "imported") {
		t.Errorf("list:\n%s", out.String())
	}
}

func TestTrackerSyncCommand(t *testing.T) {
	town := trackerCmdTown(t)
	mk := func(id, item string) {
		rec := &projectfeature.Record{ID: id, Project: "shop", Branch: "b-" + id, Rigs: map[string]projectfeature.RigState{"api": {}},
			Tasks:   []projectfeature.Task{{ID: "ap-" + id, Rig: "api"}},
			Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: item, ImportState: "New"}}
		if err := projectfeature.Create(town, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("xe-1", "12")
	mk("xe-2", "13")
	// A feature without a tracker ref is never touched.
	plain := &projectfeature.Record{ID: "xe-3", Project: "shop", Branch: "b3", Rigs: map[string]projectfeature.RigState{}}
	if err := projectfeature.Create(town, plain); err != nil {
		t.Fatal(err)
	}

	f := newCmdFake().
		on("--id 12 --state", "{}").
		on("--id 12 ", `{"id":12,"fields":{"System.State":"New"}}`).
		on("--id 13 ", `{"id":13,"fields":{"System.State":"New"}}`)
	tr := trAzureTracker(t, f)
	statuses := func(rec *projectfeature.Record) map[string]string {
		if rec.ID == "xe-1" {
			return map[string]string{"ap-xe-1": "in_progress"}
		}
		return map[string]string{"ap-xe-2": "open"}
	}

	var out bytes.Buffer
	if err := syncProjectTracker(context.Background(), &out, town, "shop", tr, true, statuses); err != nil {
		t.Fatal(err)
	}
	if f.writes() != 0 || !strings.Contains(out.String(), "would set item 12 New -> Active") {
		t.Errorf("dry run: writes=%d\n%s", f.writes(), out.String())
	}

	out.Reset()
	if err := syncProjectTracker(context.Background(), &out, town, "shop", tr, false, statuses); err != nil {
		t.Fatal(err)
	}
	if f.writes() != 1 {
		t.Fatalf("writes = %d, calls %v", f.writes(), f.calls)
	}
	// Synced: a second run makes no tracker call for xe-1; xe-2 has nothing
	// desired and never calls.
	n := len(f.calls)
	if err := syncProjectTracker(context.Background(), &out, town, "shop", tr, false, statuses); err != nil || len(f.calls) != n {
		t.Errorf("second run: err=%v calls=%v", err, f.calls[n:])
	}
}

func TestTrackerSyncFailureIsOneLineAndOthersContinue(t *testing.T) {
	town := trackerCmdTown(t)
	for _, id := range []string{"xe-1", "xe-2"} {
		item := map[string]string{"xe-1": "12", "xe-2": "13"}[id]
		rec := &projectfeature.Record{ID: id, Project: "shop", Branch: "b-" + id, Rigs: map[string]projectfeature.RigState{"api": {}},
			Tasks:   []projectfeature.Task{{ID: "ap-" + id, Rig: "api"}},
			Tracker: &projectfeature.TrackerRef{Kind: "azure", ID: item, ImportState: "New"}}
		if err := projectfeature.Create(town, rec); err != nil {
			t.Fatal(err)
		}
	}
	f := newCmdFake().on("--id 13 --state", "{}").on("--id 13 ", `{"id":13,"fields":{"System.State":"New"}}`)
	// item 12 is not answered: the fake errors for it.
	tr := trAzureTracker(t, f)
	statuses := func(rec *projectfeature.Record) map[string]string {
		return map[string]string{"ap-" + rec.ID: "in_progress"}
	}
	err := syncProjectTracker(context.Background(), &bytes.Buffer{}, town, "shop", tr, false, statuses)
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
	if f.writes() != 1 {
		t.Errorf("the other feature was blocked: writes=%d", f.writes())
	}
}
