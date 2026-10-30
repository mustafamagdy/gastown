package projectfeature

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/vcs"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

type landFixture struct {
	t        *testing.T
	town     string
	root     string
	rec      *Record
	repos    map[string]RigRepo
	origins  map[string]string
	status   map[string]string
	out      *bytes.Buffer
	settings config.LandSettings
	testCmd  map[string]string
	closed   int
	landed   int
}

func newLandFixture(t *testing.T, rigs ...string) *landFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	f := &landFixture{
		t: t, town: t.TempDir(), root: t.TempDir(),
		repos: map[string]RigRepo{}, origins: map[string]string{},
		status: map[string]string{}, out: &bytes.Buffer{}, testCmd: map[string]string{},
		settings: config.LandSettings{MergeStrategy: "direct", RequireReview: true},
	}
	if err := config.SaveProjectConfig(f.town, &config.ProjectConfig{Name: "shop", Prefix: "sh"}); err != nil {
		t.Fatal(err)
	}
	f.rec = &Record{ID: "sh-1", Project: "shop", Title: "Credits", Branch: "feat/sh-1-credits",
		State: StateOpen, Rigs: map[string]RigState{}}
	for _, r := range rigs {
		origin := filepath.Join(f.root, r+"-origin.git")
		run(t, f.root, "init", "-q", "--bare", "-b", "main", origin)
		seed := filepath.Join(f.root, r+"-seed")
		run(t, f.root, "clone", "-q", origin, seed)
		run(t, seed, "checkout", "-q", "-b", "main")
		f.write(seed, "base.txt", "base\n", "base")
		run(t, seed, "push", "-q", "origin", "main")
		bare := filepath.Join(f.root, r+".git")
		run(t, f.root, "clone", "-q", "--bare", origin, bare)
		repo := RigRepo{Rig: r, GitDir: bare, Default: "main"}
		res, err := CreateBranches([]RigRepo{repo}, f.rec.Branch)
		if err != nil {
			t.Fatal(err)
		}
		f.repos[r] = repo
		f.origins[r] = origin
		f.rec.Rigs[r] = RigState{Base: "main", BaseCommit: res[0].BaseCommit, State: RigBranch}
	}
	return f
}

func (f *landFixture) write(dir, file, content, msg string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
		f.t.Fatal(err)
	}
	run(f.t, dir, "add", "-A")
	run(f.t, dir, "commit", "-q", "-m", msg)
}

// commit adds a commit to branch (feature or main) of a rig on its origin.
func (f *landFixture) commit(rig, branch, file, content string) {
	f.t.Helper()
	wd := filepath.Join(f.t.TempDir(), "w")
	run(f.t, f.root, "clone", "-q", "-b", branch, f.origins[rig], wd)
	f.write(wd, file, content, "change "+file)
	run(f.t, wd, "push", "-q", "origin", branch)
}

func (f *landFixture) save() {
	f.t.Helper()
	if err := Create(f.town, f.rec); err != nil {
		f.t.Fatal(err)
	}
}

func (f *landFixture) task(id, rig string, after ...string) {
	f.rec.Tasks = append(f.rec.Tasks, Task{ID: id, Rig: rig, After: after})
	f.status[id] = "closed"
}

func (f *landFixture) env() *LandEnv {
	rec, err := Load(f.town, "shop", "sh-1")
	if err != nil {
		f.t.Fatal(err)
	}
	return &LandEnv{
		TownRoot: f.town, Rec: rec, Settings: f.settings, Repos: f.repos,
		TaskStatus:  func() map[string]string { return f.status },
		TestCommand: func(rig string) string { return f.testCmd[rig] },
		CloseEpic:   func(string) error { f.closed++; return nil },
		OnLanded:    func() error { f.landed++; return nil },
		Out:         f.out,
	}
}

func (f *landFixture) load() *Record {
	rec, err := Load(f.town, "shop", "sh-1")
	if err != nil {
		f.t.Fatal(err)
	}
	return rec
}

func (f *landFixture) originHas(rig, branch string) bool {
	out := run(f.t, f.origins[rig], "branch", "--list", branch)
	return out != ""
}

func (f *landFixture) mainFile(rig, file string) bool {
	cmd := exec.Command("git", "--git-dir="+f.origins[rig], "cat-file", "-e", "main:"+file)
	return cmd.Run() == nil
}

// ---- landing order ----

func TestLandingOrder(t *testing.T) {
	rec := &Record{Tasks: []Task{
		{ID: "t-web", Rig: "web", After: []string{"t-api"}},
		{ID: "t-api", Rig: "api", After: []string{"t-contracts"}},
		{ID: "t-contracts", Rig: "contracts"},
		{ID: "t-docs", Rig: "docs"},
	}}
	order, warns := LandingOrder(rec, []string{"web", "docs", "api", "contracts"})
	if got := strings.Join(order, ","); got != "contracts,api,docs,web" {
		t.Fatalf("order = %s", got)
	}
	if len(warns) != 0 {
		t.Fatalf("warns = %v", warns)
	}

	cyc := &Record{Tasks: []Task{
		{ID: "a1", Rig: "a", After: []string{"b1"}},
		{ID: "b1", Rig: "b", After: []string{"a1"}},
		{ID: "c1", Rig: "c", After: []string{"b1"}},
		{ID: "z1", Rig: "z"},
	}}
	order, warns = LandingOrder(cyc, []string{"c", "b", "a", "z"})
	if got := strings.Join(order, ","); got != "a,b,c,z" {
		t.Fatalf("cycle order = %s", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "a, b") {
		t.Fatalf("warns = %v", warns)
	}
}

// ---- preconditions ----

func TestLandPreconditionsReportEverythingAndChangeNothing(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.task("t1", "api")
	f.task("t2", "web")
	f.status["t2"] = "in_progress"
	f.save()
	// web: delete the feature branch on origin so it is missing.
	run(t, f.origins["web"], "branch", "-D", f.rec.Branch)
	before, _ := os.ReadFile(Path(f.town, "shop", "sh-1"))

	env := f.env()
	env.OpenMRs = func(rig, branch string) ([]string, error) {
		if rig == "api" {
			return []string{"gt-mr-1"}, nil
		}
		return nil, nil
	}
	_, err := Land(context.Background(), env)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"task t2", "gt-mr-1", "rig web: branch feat/sh-1-credits does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
	after, _ := os.ReadFile(Path(f.town, "shop", "sh-1"))
	if string(before) != string(after) {
		t.Fatal("record changed")
	}
}

func TestLandSkipsRigsWithoutCommitsAndErrorsWhenAllSkipped(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.task("t1", "api")
	f.save()
	if _, err := Land(context.Background(), f.env()); err == nil || !strings.Contains(err.Error(), "nothing to land") {
		t.Fatalf("err = %v", err)
	}

	f.commit("api", f.rec.Branch, "api.txt", "x")
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	rec := f.load()
	if rec.Rigs["web"].State != RigSkipped || rec.Rigs["api"].State != RigLanded {
		t.Fatalf("rigs = %+v", rec.Rigs)
	}
	if strings.Join(rec.LandingOrder, ",") != "api" {
		t.Fatalf("order = %v", rec.LandingOrder)
	}
}

func TestLandConflictPreflightStopsEverything(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "base.txt", "feature\n")
	f.commit("web", "main", "base.txt", "main\n")
	f.task("t1", "api")
	f.task("t2", "web")
	f.save()
	_, err := Land(context.Background(), f.env())
	if err == nil || !strings.Contains(err.Error(), "rig web") || !strings.Contains(err.Error(), "base.txt") {
		t.Fatalf("err = %v", err)
	}
	if f.mainFile("api", "api.txt") {
		t.Fatal("api landed despite conflict in web")
	}
	if f.load().State != StateOpen {
		t.Fatalf("state = %s", f.load().State)
	}
}

// ---- direct strategy ----

func TestLandDryRunChangesNothing(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "web.txt", "x")
	f.task("t1", "api")
	f.task("t2", "web", "t1")
	f.save()
	before, _ := os.ReadFile(Path(f.town, "shop", "sh-1"))
	env := f.env()
	env.DryRun = true
	rep, err := Land(context.Background(), env)
	if err != nil || rep.Outcome != OutcomePlanned {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	after, _ := os.ReadFile(Path(f.town, "shop", "sh-1"))
	if string(before) != string(after) || f.mainFile("api", "api.txt") {
		t.Fatal("dry run changed something")
	}
	if !strings.Contains(f.out.String(), "1. api") || !strings.Contains(f.out.String(), "2. web") {
		t.Fatalf("plan:\n%s", f.out)
	}
}

func TestLandDirectHappyPath(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "web.txt", "x")
	f.task("t1", "web", "t2")
	f.task("t2", "api")
	f.save()
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if strings.Join(rep.Order, ",") != "api,web" {
		t.Fatalf("order = %v", rep.Order)
	}
	for _, r := range []string{"api", "web"} {
		file := r + ".txt"
		if !f.mainFile(r, file) {
			t.Fatalf("%s not on main", r)
		}
		if f.originHas(r, f.rec.Branch) {
			t.Fatalf("%s: feature branch still on origin", r)
		}
		if got := run(t, f.origins[r], "log", "-1", "--format=%s", "main"); !strings.Contains(got, "sh-1") {
			t.Fatalf("merge subject %q does not name the feature", got)
		}
		if f.load().Rigs[r].LandedCommit == "" {
			t.Fatalf("%s: no landed commit", r)
		}
	}
	rec := f.load()
	if rec.State != StateLanded || f.closed != 1 || f.landed != 1 {
		t.Fatalf("state=%s closed=%d landed=%d", rec.State, f.closed, f.landed)
	}
	// Local branch gone from the rig repo too.
	if out := run(t, f.repos["api"].GitDir, "branch", "--list", f.rec.Branch); out != "" {
		t.Fatalf("local branch left: %s", out)
	}

	f.out.Reset()
	rep, err = Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeNoop || !strings.Contains(f.out.String(), "already landed") {
		t.Fatalf("rep=%+v err=%v out=%s", rep, err, f.out)
	}
	if f.closed != 1 {
		t.Fatal("epic closed twice")
	}
}

func TestLandDirectFailureThenResume(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "web.txt", "x")
	f.task("t1", "api")
	f.task("t2", "web", "t1")
	f.save()
	f.testCmd["web"] = "echo broken; exit 1"

	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeHeld {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	rec := f.load()
	if rec.State != StateHeld || rec.Rigs["api"].State != RigLanded || rec.Rigs["web"].State != RigHeld {
		t.Fatalf("rec = %+v", rec)
	}
	if !strings.Contains(rec.Rigs["web"].Error, "tests failed") {
		t.Fatalf("error = %q", rec.Rigs["web"].Error)
	}
	if f.mainFile("web", "web.txt") || !f.mainFile("api", "api.txt") {
		t.Fatal("wrong rigs landed")
	}
	if !strings.Contains(f.out.String(), "Landed:     api") || !strings.Contains(f.out.String(), "Not landed: web") {
		t.Fatalf("summary missing:\n%s", f.out)
	}
	apiCommit := rec.Rigs["api"].LandedCommit

	f.testCmd["web"] = "true"
	f.out.Reset()
	rep, err = Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("resume: rep=%+v err=%v\n%s", rep, err, f.out)
	}
	rec = f.load()
	if rec.Rigs["api"].LandedCommit != apiCommit || rec.Rigs["web"].State != RigLanded || rec.State != StateLanded {
		t.Fatalf("rec = %+v", rec)
	}
}

// ---- PR strategy ----

type fakePR struct {
	pr       vcs.PR
	checks   vcs.CheckSummary
	review   string
	mergeErr error
}

type fakeVCS struct {
	prs     map[string]*fakePR // by rig
	creates int
	bodies  map[string]string
	merged  []string
}

func newFakeVCS() *fakeVCS { return &fakeVCS{prs: map[string]*fakePR{}, bodies: map[string]string{}} }

type fakeProv struct {
	f   *fakeVCS
	rig string
}

func (p fakeProv) CreatePR(_ context.Context, req vcs.CreatePRRequest) (*vcs.PR, error) {
	if e, ok := p.f.prs[p.rig]; ok && e.pr.State == vcs.StateOpen {
		pr := e.pr
		return &pr, nil
	}
	p.f.creates++
	e := &fakePR{
		pr: vcs.PR{ID: p.rig + "-1", URL: "https://x/" + p.rig + "/pull/1", Head: req.Head, Base: req.Base,
			HeadSHA: "head-" + p.rig, State: vcs.StateOpen},
		checks: vcs.CheckSummary{State: vcs.ChecksPassing}, review: vcs.ReviewApproved,
	}
	p.f.prs[p.rig] = e
	p.f.bodies[p.rig] = req.Body
	pr := e.pr
	return &pr, nil
}
func (p fakeProv) FindPR(context.Context, string, string) (*vcs.PR, error) { return nil, nil }
func (p fakeProv) GetPR(_ context.Context, id string) (*vcs.PR, error) {
	e := p.f.prs[p.rig]
	if e == nil {
		return nil, errors.New("no such PR")
	}
	pr := e.pr
	return &pr, nil
}
func (p fakeProv) Checks(context.Context, *vcs.PR) (vcs.CheckSummary, error) {
	return p.f.prs[p.rig].checks, nil
}
func (p fakeProv) ReviewState(context.Context, *vcs.PR) (string, error) {
	return p.f.prs[p.rig].review, nil
}
func (p fakeProv) Merge(_ context.Context, pr *vcs.PR, opts vcs.MergeOptions) error {
	e := p.f.prs[p.rig]
	if e.mergeErr != nil {
		return e.mergeErr
	}
	if opts.ExpectHead != "" && opts.ExpectHead != e.pr.HeadSHA {
		return vcs.ErrHeadMoved
	}
	e.pr.State, e.pr.MergeCommit = vcs.StateMerged, "mc-"+p.rig
	pr.State, pr.MergeCommit = e.pr.State, e.pr.MergeCommit
	p.f.merged = append(p.f.merged, p.rig)
	return nil
}
func (p fakeProv) UpdatePRBody(_ context.Context, pr *vcs.PR, body string) error {
	p.f.bodies[p.rig] = body
	return nil
}

func prFixture(t *testing.T) (*landFixture, *fakeVCS) {
	f := newLandFixture(t, "api", "web")
	f.settings = config.LandSettings{MergeStrategy: "pr", RequireReview: true}
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "web.txt", "x")
	f.task("t1", "api")
	f.task("t2", "web", "t1")
	f.save()
	fv := newFakeVCS()
	old := NewProvider
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) { return fakeProv{fv, rig}, nil }
	t.Cleanup(func() { NewProvider = old })
	return f, fv
}

func TestLandPROpenIsIdempotentAndLinksSiblings(t *testing.T) {
	f, fv := prFixture(t)
	// Reviews stay pending so nothing merges.
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) {
		return pendingProv{fakeProv{fv, rig}}, nil
	}
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeWaiting {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if fv.creates != 2 {
		t.Fatalf("creates = %d", fv.creates)
	}
	rec := f.load()
	if rec.State != StateLanding || rec.Rigs["api"].PRID == "" || rec.Rigs["web"].State != RigPROpen {
		t.Fatalf("rec = %+v", rec)
	}
	if !strings.Contains(fv.bodies["api"], "https://x/web/pull/1") || !strings.Contains(fv.bodies["web"], "https://x/api/pull/1") ||
		!strings.Contains(fv.bodies["api"], "sh-1") || !strings.Contains(fv.bodies["api"], "api, web") {
		t.Fatalf("bodies = %v", fv.bodies)
	}
	if rec.LandingOrder[0] != "api" {
		t.Fatalf("order = %v", rec.LandingOrder)
	}
	rep, err = Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeWaiting || fv.creates != 2 {
		t.Fatalf("second run: rep=%+v err=%v creates=%d", rep, err, fv.creates)
	}
}

// pendingProv reports a pending review for every PR.
type pendingProv struct{ fakeProv }

func (p pendingProv) ReviewState(context.Context, *vcs.PR) (string, error) {
	return vcs.ReviewPending, nil
}

func TestLandPROneRejectedHoldsAll(t *testing.T) {
	f, fv := prFixture(t)
	// First pass: open both (approved by default would merge), so open via a
	// provider whose web review is rejected from the start.
	old := NewProvider
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) {
		if rig == "web" {
			return rejectProv{fakeProv{fv, rig}}, nil
		}
		return fakeProv{fv, rig}, nil
	}
	defer func() { NewProvider = old }()
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeHeld {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if len(fv.merged) != 0 {
		t.Fatalf("merged = %v", fv.merged)
	}
	rec := f.load()
	if rec.State != StateHeld || rec.Rigs["web"].Error == "" || rec.Rigs["api"].State != RigPROpen {
		t.Fatalf("rec = %+v", rec)
	}
	if f.originHas("api", f.rec.Branch) == false {
		t.Fatal("branch deleted while held")
	}
}

type rejectProv struct{ fakeProv }

func (p rejectProv) ReviewState(context.Context, *vcs.PR) (string, error) {
	return vcs.ReviewRejected, nil
}

func TestLandPRAllApprovedMergesInOrderAndCompletes(t *testing.T) {
	f, fv := prFixture(t)
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if strings.Join(fv.merged, ",") != "api,web" {
		t.Fatalf("merged = %v", fv.merged)
	}
	rec := f.load()
	if rec.State != StateLanded || rec.Rigs["api"].LandedCommit != "mc-api" || f.closed != 1 || f.landed != 1 {
		t.Fatalf("rec = %+v closed=%d", rec, f.closed)
	}
	for _, r := range []string{"api", "web"} {
		if f.originHas(r, f.rec.Branch) {
			t.Fatalf("%s: branch not deleted", r)
		}
	}
}

func TestLandPRHeadMovedHoldsAndKeepsRestOpen(t *testing.T) {
	f, fv := prFixture(t)
	old := NewProvider
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) {
		if rig == "web" {
			return moveProv{fakeProv{fv, rig}}, nil
		}
		return fakeProv{fv, rig}, nil
	}
	defer func() { NewProvider = old }()
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeHeld {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	rec := f.load()
	if rec.State != StateHeld || rec.Rigs["api"].State != RigLanded || rec.Rigs["web"].State != RigHeld {
		t.Fatalf("rec = %+v", rec)
	}
	if !strings.Contains(rec.Rigs["web"].Error, "head moved") || fv.prs["web"].pr.State != vcs.StateOpen {
		t.Fatalf("web error=%q", rec.Rigs["web"].Error)
	}
	if f.closed != 0 {
		t.Fatal("epic closed on held feature")
	}
}

type moveProv struct{ fakeProv }

func (p moveProv) Merge(context.Context, *vcs.PR, vcs.MergeOptions) error {
	return fmt.Errorf("%w: pushed after approval", vcs.ErrHeadMoved)
}

func TestLandPRMergedOutsideCountsAsLanded(t *testing.T) {
	f, fv := prFixture(t)
	old := NewProvider
	// Open both first with a pending review.
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) {
		return pendingProv{fakeProv{fv, rig}}, nil
	}
	if rep, err := Land(context.Background(), f.env()); err != nil || rep.Outcome != OutcomeWaiting {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) { return fakeProv{fv, rig}, nil }
	defer func() { NewProvider = old }()
	fv.prs["api"].pr.State, fv.prs["api"].pr.MergeCommit = vcs.StateMerged, "outside"
	rep, err := Land(context.Background(), f.env())
	if err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if strings.Join(fv.merged, ",") != "web" {
		t.Fatalf("merged = %v (api must not be merged again)", fv.merged)
	}
	if f.load().Rigs["api"].LandedCommit != "outside" {
		t.Fatalf("rec = %+v", f.load().Rigs["api"])
	}
}
