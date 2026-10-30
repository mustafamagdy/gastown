package projectfeature

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
)

func newTown(t *testing.T, project string) string {
	t.Helper()
	town := t.TempDir()
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: project, Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}
	return town
}

func sampleRecord() *Record {
	return &Record{
		ID: "xe-1", Project: "shop", Title: "Credits", Branch: "feat/xe-1-credits",
		Rigs: map[string]RigState{
			"api": {Base: "main", State: RigBranch},
			"web": {Base: "main", State: RigBranch},
		},
		Tasks: []Task{
			{ID: "ap-1", Rig: "api", Title: "endpoint"},
			{ID: "we-1", Rig: "web", Title: "ui"},
		},
	}
}

func TestRecordRoundTrip(t *testing.T) {
	town := newTown(t, "shop")
	rec := sampleRecord()
	if err := Create(town, rec); err != nil {
		t.Fatal(err)
	}
	if err := Create(town, rec); err == nil {
		t.Fatal("second Create should fail")
	}
	got, err := Load(town, "shop", "xe-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CurrentVersion || got.State != StateOpen || got.Branch != rec.Branch || len(got.Tasks) != 2 {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.Rigs["api"].Base != "main" {
		t.Fatalf("rig state lost: %+v", got.Rigs)
	}
	if _, err := Load(town, "shop", "xe-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	recs, errs := List(town, "shop")
	if len(recs) != 1 || len(errs) != 0 {
		t.Fatalf("List = %v %v", recs, errs)
	}
	f, task, err := ResolveForBead(town, "we-1")
	if err != nil || f.ID != "xe-1" || task.Rig != "web" {
		t.Fatalf("ResolveForBead = %v %v %v", f, task, err)
	}
	if _, _, err := ResolveForBead(town, "zz-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateIsSerialized(t *testing.T) {
	town := newTown(t, "shop")
	rec := sampleRecord()
	rec.Tasks = nil
	if err := Create(town, rec); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "ap-" + string(rune('a'+i))
			if _, err := Update(town, "shop", "xe-1", func(r *Record) error {
				return r.AddTask(Task{ID: id, Rig: "api", Title: id})
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, _ := Load(town, "shop", "xe-1")
	if len(got.Tasks) != 12 {
		t.Fatalf("lost updates: %d tasks", len(got.Tasks))
	}
}

func TestUpdateErrorWritesNothing(t *testing.T) {
	town := newTown(t, "shop")
	if err := Create(town, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	_, err := Update(town, "shop", "xe-1", func(r *Record) error {
		r.Title = "changed"
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	got, _ := Load(town, "shop", "xe-1")
	if got.Title != "Credits" {
		t.Fatalf("record changed despite error: %q", got.Title)
	}
}

func TestValidateRejectsBadRecords(t *testing.T) {
	r := sampleRecord()
	r.Tasks[0].Rig = "nope"
	if r.Validate() == nil {
		t.Fatal("task in unknown rig must fail")
	}
	r = sampleRecord()
	r.Tasks[0].After = []string{"missing"}
	if r.Validate() == nil {
		t.Fatal("unknown after must fail")
	}
	r = sampleRecord()
	r.ID = "../x"
	if r.Validate() == nil {
		t.Fatal("path-like id must fail")
	}
}

func TestMarker(t *testing.T) {
	d := AddMarker("Do the thing\nmore", "xe-1")
	if Marker(d) != "xe-1" || !HasMarker(d) {
		t.Fatalf("marker lost: %q", d)
	}
	d = AddMarker(d, "xe-2")
	if Marker(d) != "xe-2" || strings.Count(d, "project_feature") != 1 {
		t.Fatalf("marker not replaced: %q", d)
	}
	if Marker("Project-Feature: xe-3") != "xe-3" {
		t.Fatal("case and hyphen variants must match")
	}
	if HasMarker("no marker here\nintegration_branch: x") {
		t.Fatal("false positive")
	}
	if AddMarker("", "xe-1") != "project_feature: xe-1" {
		t.Fatal("empty description")
	}
}

func TestResolveTargetFailsClosed(t *testing.T) {
	town := newTown(t, "shop")
	if err := Create(town, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	desc := AddMarker("work", "xe-1")

	// unmarked: untouched behaviour
	if b, marked, err := ResolveTarget(town, "api", "ap-1", "plain"); b != "" || marked || err != nil {
		t.Fatalf("unmarked = %q %v %v", b, marked, err)
	}
	// marked and resolvable
	b, marked, err := ResolveTarget(town, "api", "ap-1", desc)
	if err != nil || !marked || b != "feat/xe-1-credits" {
		t.Fatalf("resolved = %q %v %v", b, marked, err)
	}
	// record missing
	if _, marked, err := ResolveTarget(town, "api", "ap-1", AddMarker("w", "xe-404")); err == nil || !marked {
		t.Fatalf("missing record must error, got %v %v", marked, err)
	}
	// empty marker value
	if _, marked, err := ResolveTarget(town, "api", "ap-1", "project_feature:"); err == nil || !marked {
		t.Fatal("empty marker must error")
	}
	// rig not in the feature
	if _, _, err := ResolveTarget(town, "billing", "bi-1", desc); err == nil {
		t.Fatal("rig outside feature must error")
	}
	// task registered in a different rig
	if _, _, err := ResolveTarget(town, "web", "ap-1", desc); err == nil {
		t.Fatal("task/rig mismatch must error")
	}
	// landed
	if _, err := Update(town, "shop", "xe-1", func(r *Record) error { r.State = StateLanded; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveTarget(town, "api", "ap-1", desc); err == nil {
		t.Fatal("landed feature must error")
	}
}

func svc(rig string, calls, pub, con []string) projectmap.Service {
	return projectmap.Service{Rig: rig, Name: rig, Calls: calls, Publishes: pub, Consumes: con}
}

func TestApplyOrder(t *testing.T) {
	m := &projectmap.Map{Services: []projectmap.Service{
		svc("contracts", nil, []string{"credit.granted"}, nil),
		svc("api", []string{"contracts"}, nil, nil),
		svc("web", []string{"api"}, nil, nil),
		svc("worker", nil, nil, []string{"credit.granted"}),
	}}
	rec := &Record{
		ID: "xe-1", Project: "shop", Branch: "b",
		Rigs: map[string]RigState{"contracts": {}, "api": {}, "web": {}, "worker": {}},
		Tasks: []Task{
			{ID: "t-web", Rig: "web"}, {ID: "t-api", Rig: "api"},
			{ID: "t-con", Rig: "contracts"}, {ID: "t-wrk", Rig: "worker"},
		},
	}
	if w := ApplyOrder(rec, m); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	want := map[string][]string{
		"t-web": {"t-api"}, "t-api": {"t-con"}, "t-con": nil, "t-wrk": {"t-con"},
	}
	for _, task := range rec.Tasks {
		if !reflect.DeepEqual(task.After, want[task.ID]) && !(len(task.After) == 0 && len(want[task.ID]) == 0) {
			t.Errorf("%s after = %v, want %v", task.ID, task.After, want[task.ID])
		}
	}
}

func TestApplyOrderCycleAndExplicit(t *testing.T) {
	m := &projectmap.Map{Services: []projectmap.Service{
		svc("a", []string{"b"}, nil, nil),
		svc("b", []string{"a"}, nil, nil),
		svc("c", []string{"a"}, nil, nil),
	}}
	rec := &Record{
		ID: "xe-1", Project: "shop", Branch: "b",
		Rigs: map[string]RigState{"a": {}, "b": {}, "c": {}},
		Tasks: []Task{
			{ID: "t-a", Rig: "a"}, {ID: "t-b", Rig: "b"}, {ID: "t-c", Rig: "c"},
		},
	}
	// explicit edge survives and is kept on re-run
	if err := rec.AddTask(Task{ID: "t-a2", Rig: "a", After: []string{"t-b"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := ApplyOrder(rec, m)
		if len(w) == 0 || !strings.Contains(w[0], "depend on each other") {
			t.Fatalf("want a cycle warning, got %v", w)
		}
		if len(rec.Task("t-a").After) != 0 || len(rec.Task("t-b").After) != 0 {
			t.Fatalf("cycle edges must be dropped: %v %v", rec.Task("t-a").After, rec.Task("t-b").After)
		}
		if !reflect.DeepEqual(rec.Task("t-a2").After, []string{"t-b"}) {
			t.Fatalf("explicit edge lost: %v", rec.Task("t-a2").After)
		}
		// c depends on a, which is outside a cycle with c: edge kept (to a's tasks)
		if !reflect.DeepEqual(rec.Task("t-c").After, []string{"t-a", "t-a2"}) {
			t.Fatalf("t-c after = %v", rec.Task("t-c").After)
		}
	}
}

func TestApplyOrderSkipsEdgeClosingExplicitCycle(t *testing.T) {
	m := &projectmap.Map{Services: []projectmap.Service{
		svc("a", []string{"b"}, nil, nil), svc("b", nil, nil, nil),
	}}
	rec := &Record{
		ID: "xe-1", Project: "shop", Branch: "b",
		Rigs:  map[string]RigState{"a": {}, "b": {}},
		Tasks: []Task{{ID: "t-a", Rig: "a"}},
	}
	// explicit: t-b waits for t-a, but the map says a depends on b
	if err := rec.AddTask(Task{ID: "t-b", Rig: "b", After: []string{"t-a"}}); err != nil {
		t.Fatal(err)
	}
	w := ApplyOrder(rec, m)
	if len(w) != 1 || !strings.Contains(w[0], "would make a cycle") {
		t.Fatalf("warnings = %v", w)
	}
	if len(rec.Task("t-a").After) != 0 {
		t.Fatalf("cycle edge added: %v", rec.Task("t-a").After)
	}
}

func TestClassifyAndReady(t *testing.T) {
	rec := &Record{
		ID: "xe-1", Project: "shop", Branch: "b",
		Rigs: map[string]RigState{"api": {}, "web": {}},
		Tasks: []Task{
			{ID: "t1", Rig: "api"},
			{ID: "t2", Rig: "api", After: []string{"t1"}},
			{ID: "t3", Rig: "web", After: []string{"t1", "t2"}},
			{ID: "t4", Rig: "web"},
			{ID: "t5", Rig: "web", After: []string{"t4"}},
			{ID: "t6", Rig: "web"},
		},
	}
	status := map[string]string{"t1": "closed", "t2": "open", "t3": "open", "t4": "hooked", "t5": "open", "t6": ""}
	views := Classify(rec, status)
	got := map[string]string{}
	for _, v := range views {
		got[v.ID] = v.State
	}
	want := map[string]string{"t1": TaskDone, "t2": TaskReady, "t3": TaskBlocked, "t4": TaskRunning, "t5": TaskBlocked, "t6": TaskUnknown}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	if r := ReadyTasks(views); len(r) != 1 || r[0].ID != "t2" {
		t.Fatalf("ready = %v", r)
	}
	if !Started(views) {
		t.Fatal("should be started")
	}
	if Started(Classify(rec, map[string]string{"t1": "open", "t2": "open"})) {
		t.Fatal("nothing started")
	}
	if views[2].BlockedBy[0] != "t2" || len(views[2].BlockedBy) != 1 {
		t.Fatalf("blocked by = %v", views[2].BlockedBy)
	}
}

// ---- branches ----

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo creates a bare upstream with one commit on main and a bare clone
// (the rig's .repo.git). It returns the RigRepo and the upstream path.
func newRepo(t *testing.T, rig string) (RigRepo, string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	upstream := filepath.Join(root, "upstream.git")
	bare := filepath.Join(root, "rig.repo.git")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "c")
	runGit(t, root, "clone", "-q", "--bare", work, upstream)
	runGit(t, root, "clone", "-q", "--bare", upstream, bare)
	return RigRepo{Rig: rig, GitDir: bare, Default: "main"}, upstream
}

func remoteHas(t *testing.T, r RigRepo, branch string) bool {
	t.Helper()
	ok, err := BranchExists(r, branch)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestCreateBranchesAll(t *testing.T) {
	a, _ := newRepo(t, "a")
	b, _ := newRepo(t, "b")
	res, err := CreateBranches([]RigRepo{a, b}, "feat/xe-1-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Base != "main" || len(res[0].BaseCommit) != 40 {
		t.Fatalf("results = %+v", res)
	}
	if !remoteHas(t, a, "feat/xe-1-x") || !remoteHas(t, b, "feat/xe-1-x") {
		t.Fatal("branch missing after create")
	}
}

func TestCreateBranchesChecksAllFirst(t *testing.T) {
	a, _ := newRepo(t, "a")
	b, _ := newRepo(t, "b")
	c, _ := newRepo(t, "c")
	// the branch already exists in the LAST rig only
	if _, err := CreateBranch(c, "feat/x"); err != nil {
		t.Fatal(err)
	}
	_, err := CreateBranches([]RigRepo{a, b, c}, "feat/x")
	if err == nil || !strings.Contains(err.Error(), "already exists in rig(s): c") {
		t.Fatalf("err = %v", err)
	}
	if remoteHas(t, a, "feat/x") || remoteHas(t, b, "feat/x") {
		t.Fatal("rigs a and b must be untouched when any rig has the branch")
	}
}

func TestCreateBranchesRollsBack(t *testing.T) {
	a, _ := newRepo(t, "a")
	b, _ := newRepo(t, "b")
	c, upC := newRepo(t, "c")
	hook := filepath.Join(upC, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := CreateBranches([]RigRepo{a, b, c}, "feat/y")
	if err == nil || !strings.Contains(err.Error(), "rig c") || !strings.Contains(err.Error(), "rolled back 2") {
		t.Fatalf("err = %v", err)
	}
	if remoteHas(t, a, "feat/y") || remoteHas(t, b, "feat/y") || remoteHas(t, c, "feat/y") {
		t.Fatal("pushed branches must be rolled back")
	}
}

func TestCreateBranchBadDefault(t *testing.T) {
	a, _ := newRepo(t, "a")
	a.Default = "nope"
	if _, err := CreateBranch(a, "feat/z"); err == nil {
		t.Fatal("missing default branch must error")
	}
}

// A closed task with an open MR has not reached the feature branch yet, so it
// must not release the tasks waiting for it.
func TestClassify_MergingDoesNotRelease(t *testing.T) {
	rec := &Record{Tasks: []Task{{ID: "a"}, {ID: "b", After: []string{"a"}}}}
	views := Classify(rec, map[string]string{"a": StatusMerging, "b": "open"})
	if views[0].State != TaskMerging || views[1].State != TaskBlocked {
		t.Fatalf("views = %+v", views)
	}
	if !Started(views) {
		t.Error("a merging task counts as started")
	}
	views = Classify(rec, map[string]string{"a": "closed", "b": "open"})
	if views[0].State != TaskDone || views[1].State != TaskReady {
		t.Fatalf("views = %+v", views)
	}
}
