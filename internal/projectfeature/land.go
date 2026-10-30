package projectfeature

// Landing: the coordinator that lands one feature across its rigs as a linked
// set. It is driven by re-running 'gt project feature land' (by hand or by the
// daemon for features already in state "landing") and every step is
// idempotent: the record says what has landed, what has a PR and what is held.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/vcs"
)

// Land outcomes.
const (
	OutcomeLanded  = "landed"  // every rig landed or skipped; the feature is done
	OutcomeNoop    = "noop"    // the feature was already landed
	OutcomeHeld    = "held"    // landing stopped; a human must act, re-run to resume
	OutcomeWaiting = "waiting" // PRs wait for checks or review
	OutcomePlanned = "planned" // dry run
)

// LandReport is the result of Land.
type LandReport struct {
	Outcome string
	Order   []string
}

// LandEnv carries what Land needs. The cmd package fills the bead-facing
// callbacks so the coordinator itself is testable with temp repos and a fake
// vcs.Provider.
type LandEnv struct {
	TownRoot string
	Rec      *Record
	Settings config.LandSettings
	Repos    map[string]RigRepo // by rig

	// TaskStatus returns the live bead status of every task (id to status).
	TaskStatus func() map[string]string
	// OpenMRs returns the open or in-progress merge requests of a rig that
	// target the feature branch.
	OpenMRs func(rig, branch string) ([]string, error)
	// TestCommand returns the rig's test command for direct landing, or "".
	TestCommand func(rig string) string
	// CloseEpic closes the feature epic bead.
	CloseEpic func(reason string) error
	// OnLanded runs after the feature is landed (service map refresh).
	OnLanded func() error

	Out    io.Writer
	DryRun bool
}

// NewProvider resolves the PR provider of a rig: by name when the project
// sets vcs_provider, otherwise from the rig's origin remote. Tests replace it
// with a fake.
var NewProvider = func(rig string, repo RigRepo, name string) (vcs.Provider, error) {
	opts := vcs.Options{RepoDir: repo.GitDir}
	if name != "" {
		return vcs.New(name, opts)
	}
	url, err := git(repo.GitDir, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("rig %s: reading origin remote: %w", rig, err)
	}
	return vcs.ForRemote(url, opts)
}

// LandMergeMethod is the merge method used for PRs. A merge commit keeps the
// feature's identity visible in each repo's history.
const LandMergeMethod = vcs.MethodMerge

func (e *LandEnv) printf(format string, a ...any) {
	if e.Out != nil {
		fmt.Fprintf(e.Out, format, a...)
	}
}

func (e *LandEnv) update(fn func(*Record)) error {
	rec, err := Update(e.TownRoot, e.Rec.Project, e.Rec.ID, func(r *Record) error { fn(r); return nil })
	if err != nil {
		return err
	}
	e.Rec = rec
	return nil
}

func (e *LandEnv) setRig(rig string, fn func(*RigState)) error {
	return e.update(func(r *Record) {
		rs := r.Rigs[rig]
		fn(&rs)
		r.Rigs[rig] = rs
	})
}

func remoteRef(name string) string { return "refs/remotes/origin/" + name }

// LandingOrder orders rigs so that a rig whose tasks wait for another rig's
// tasks lands after it. Ties are alphabetical. Rigs in a dependency cycle
// stay alphabetical among themselves and a warning names them.
func LandingOrder(rec *Record, rigs []string) (order []string, warnings []string) {
	set := map[string]bool{}
	for _, r := range rigs {
		set[r] = true
	}
	deps := map[string][]string{}
	for _, t := range rec.Tasks {
		if !set[t.Rig] {
			continue
		}
		for _, a := range t.After {
			if pre := rec.Task(a); pre != nil && pre.Rig != t.Rig && set[pre.Rig] && !contains(deps[t.Rig], pre.Rig) {
				deps[t.Rig] = append(deps[t.Rig], pre.Rig)
			}
		}
	}
	sorted := append([]string(nil), rigs...)
	sort.Strings(sorted)
	acyclic, cycles := breakCycles(deps, sorted)
	for _, c := range cycles {
		warnings = append(warnings, fmt.Sprintf("rigs wait for each other (%s); landing them in alphabetical order", strings.Join(c, ", ")))
	}
	placed := map[string]bool{}
	for len(order) < len(sorted) {
		progressed := false
		for _, r := range sorted {
			if placed[r] {
				continue
			}
			ready := true
			for _, d := range acyclic[r] {
				if !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				placed[r] = true
				order = append(order, r)
				progressed = true
				break
			}
		}
		if !progressed { // cannot happen once cycles are removed; stay safe
			for _, r := range sorted {
				if !placed[r] {
					order = append(order, r)
				}
			}
			break
		}
	}
	return order, warnings
}

// ---- git helpers ----

// gitCode runs git and returns stdout, the exit code and an error. The error
// is nil only for exit code 0. workDir, when set, is the working directory
// (the git dir is then discovered); otherwise gitDir is used.
func gitCode(workDir, gitDir string, extraEnv []string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := args
	if workDir == "" {
		full = append([]string{"--git-dir=" + gitDir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = workDir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), code, err
}

// fetchRig updates the remote-tracking refs of the base and feature branches.
// A missing feature branch is not an error here (callers check existence).
func fetchRig(r RigRepo, base, branch string, withBranch bool) error {
	args := []string{"fetch", "--quiet", "origin", "+refs/heads/" + base + ":" + remoteRef(base)}
	if withBranch {
		args = append(args, "+refs/heads/"+branch+":"+remoteRef(branch))
	}
	_, err := git(r.GitDir, args...)
	return err
}

// conflictFiles test-merges the feature branch into the base without touching
// any ref. It returns the conflicting files (empty when the merge is clean).
func conflictFiles(r RigRepo, base, branch string) ([]string, error) {
	out, code, err := gitCode("", r.GitDir, nil, "merge-tree", "--write-tree", "--name-only", remoteRef(base), remoteRef(branch))
	switch code {
	case 0:
		return nil, nil
	case 1:
		var files []string
		lines := strings.Split(out, "\n")
		for _, l := range lines[1:] { // line 0 is the tree id
			if strings.TrimSpace(l) == "" {
				break
			}
			files = append(files, l)
		}
		if len(files) == 0 {
			files = []string{"(conflicts reported without file names)"}
		}
		return files, nil
	}
	return nil, err
}

// ---- Land ----

type rigPlan struct {
	fresh   map[string]bool // rigs without a PR that still need preconditions
	skipped []string        // newly found to have nothing to land
}

// Land runs one pass of the coordinator. It is safe to re-run at any time.
func Land(ctx context.Context, env *LandEnv) (*LandReport, error) {
	rec := env.Rec
	if rec.State == StateLanded {
		env.printf("Feature %s is already landed; nothing to do.\n", rec.ID)
		return &LandReport{Outcome: OutcomeNoop, Order: rec.LandingOrder}, nil
	}
	strategy := env.Settings.MergeStrategy
	if strategy != "pr" && strategy != "direct" {
		return nil, fmt.Errorf("unknown merge_strategy %q (want pr or direct)", strategy)
	}
	if !env.DryRun {
		if err := os.MkdirAll(Dir(env.TownRoot, rec.Project), 0755); err != nil {
			return nil, err
		}
		release, ok, err := lock.FlockTryAcquire(filepath.Join(Dir(env.TownRoot, rec.Project), "."+rec.ID+".land.lock"))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("another land of feature %s is running", rec.ID)
		}
		defer release()
		// Re-read under the lock: the other land may just have finished.
		fresh, err := Load(env.TownRoot, rec.Project, rec.ID)
		if err != nil {
			return nil, err
		}
		env.Rec = fresh
		rec = fresh
		if rec.State == StateLanded {
			env.printf("Feature %s is already landed; nothing to do.\n", rec.ID)
			return &LandReport{Outcome: OutcomeNoop, Order: rec.LandingOrder}, nil
		}
	}

	plan, err := env.preflight(ctx)
	if err != nil {
		return nil, err
	}

	// Active rigs: everything that is not skipped.
	var active []string
	for name, rs := range rec.Rigs {
		if rs.State != RigSkipped && !plan.isSkipped(name) {
			active = append(active, name)
		}
	}
	sort.Strings(active)
	order, warnings := env.order(active)
	for _, w := range warnings {
		env.printf("Warning: %s\n", w)
	}

	if env.DryRun {
		env.printPlan(strategy, order, plan)
		return &LandReport{Outcome: OutcomePlanned, Order: order}, nil
	}

	if err := env.update(func(r *Record) {
		for _, s := range plan.skipped {
			rs := r.Rigs[s]
			rs.State, rs.Error = RigSkipped, ""
			r.Rigs[s] = rs
		}
		r.LandingOrder = order
		r.State = StateLanding
	}); err != nil {
		return nil, err
	}

	var report *LandReport
	if strategy == "direct" {
		report, err = env.landDirect(ctx, order)
	} else {
		report, err = env.landPR(ctx, order)
	}
	if err != nil {
		return nil, err
	}
	report.Order = order
	if report.Outcome == OutcomeLanded {
		if err := env.complete(); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func (p *rigPlan) isSkipped(rig string) bool {
	for _, s := range p.skipped {
		if s == rig {
			return true
		}
	}
	return false
}

func (e *LandEnv) order(active []string) ([]string, []string) {
	if len(e.Rec.LandingOrder) > 0 && sameSet(e.Rec.LandingOrder, active) {
		return append([]string(nil), e.Rec.LandingOrder...), nil
	}
	return LandingOrder(e.Rec, active)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

// preflight checks the preconditions of every rig that has no PR yet and
// test-merges each into its current base. It reports every failure at once and
// changes nothing.
func (e *LandEnv) preflight(ctx context.Context) (*rigPlan, error) {
	rec := e.Rec
	plan := &rigPlan{fresh: map[string]bool{}}
	var names []string
	for name, rs := range rec.Rigs {
		if rs.State == RigLanded || rs.State == RigSkipped || rs.PRID != "" {
			continue
		}
		plan.fresh[name] = true
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	if len(names) > 0 {
		status := map[string]string{}
		if e.TaskStatus != nil {
			status = e.TaskStatus()
		}
		for _, t := range rec.Tasks {
			if status[t.ID] != "closed" {
				st := status[t.ID]
				if st == "" {
					st = "unreadable"
				}
				problems = append(problems, fmt.Sprintf("task %s (rig %s) is not closed (%s)", t.ID, t.Rig, st))
			}
		}
	}
	var withCommits []string
	for _, name := range names {
		rs := rec.Rigs[name]
		repo, ok := e.Repos[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("rig %s: no repository", name))
			continue
		}
		if e.OpenMRs != nil {
			mrs, err := e.OpenMRs(name, rec.Branch)
			if err != nil {
				problems = append(problems, fmt.Sprintf("rig %s: listing merge requests: %v", name, err))
			} else if len(mrs) > 0 {
				problems = append(problems, fmt.Sprintf("rig %s: open merge request(s) target %s: %s", name, rec.Branch, strings.Join(mrs, ", ")))
			}
		}
		exists, err := BranchExists(repo, rec.Branch)
		if err != nil {
			problems = append(problems, fmt.Sprintf("rig %s: checking branch %s: %v", name, rec.Branch, err))
			continue
		}
		if !exists {
			problems = append(problems, fmt.Sprintf("rig %s: branch %s does not exist on origin", name, rec.Branch))
			continue
		}
		if err := fetchRig(repo, rs.Base, rec.Branch, true); err != nil {
			problems = append(problems, fmt.Sprintf("rig %s: %v", name, err))
			continue
		}
		out, err := git(repo.GitDir, "rev-list", "--count", remoteRef(rs.Base)+".."+remoteRef(rec.Branch))
		if err != nil {
			problems = append(problems, fmt.Sprintf("rig %s: %v", name, err))
			continue
		}
		if out == "0" {
			plan.skipped = append(plan.skipped, name)
			continue
		}
		withCommits = append(withCommits, name)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("feature %s cannot land:\n  - %s", rec.ID, strings.Join(problems, "\n  - "))
	}

	// Anything left to land at all?
	anyActive := len(withCommits) > 0
	for name, rs := range rec.Rigs {
		if plan.fresh[name] {
			continue
		}
		if rs.State != RigSkipped {
			anyActive = true
		}
	}
	if !anyActive {
		return nil, fmt.Errorf("feature %s has no commits ahead of base in any rig; nothing to land", rec.ID)
	}

	var conflicts []string
	for _, name := range withCommits {
		files, err := conflictFiles(e.Repos[name], rec.Rigs[name].Base, rec.Branch)
		if err != nil {
			conflicts = append(conflicts, fmt.Sprintf("rig %s: test merge failed: %v", name, err))
			continue
		}
		if len(files) > 0 {
			conflicts = append(conflicts, fmt.Sprintf("rig %s: %s would conflict with %s in: %s", name, rec.Branch, rec.Rigs[name].Base, strings.Join(files, ", ")))
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("feature %s has merge conflicts; nothing was landed:\n  - %s", rec.ID, strings.Join(conflicts, "\n  - "))
	}
	return plan, nil
}

func (e *LandEnv) printPlan(strategy string, order []string, plan *rigPlan) {
	e.printf("Dry run: feature %s (%s), merge_strategy %s\n", e.Rec.ID, e.Rec.Title, strategy)
	e.printf("Preflight passed: tasks closed, no open merge requests, branches ahead, no conflicts.\n")
	e.printf("Landing order:\n")
	for i, rig := range order {
		rs := e.Rec.Rigs[rig]
		action := ""
		switch {
		case rs.State == RigLanded:
			action = "already landed"
		case rs.PRID != "":
			action = "advance " + rs.PRURL
		case strategy == "direct":
			action = "merge --no-ff into " + rs.Base + " and push"
		default:
			action = "open PR into " + rs.Base
		}
		e.printf("  %d. %-16s %s\n", i+1, rig, action)
	}
	for _, s := range plan.skipped {
		e.printf("  -  %-16s skipped (no commits ahead of %s)\n", s, e.Rec.Rigs[s].Base)
	}
	for name, rs := range e.Rec.Rigs {
		if rs.State == RigSkipped && !plan.isSkipped(name) {
			e.printf("  -  %-16s skipped\n", name)
		}
	}
	e.printf("Nothing was changed.\n")
}

// ---- direct strategy ----

func (e *LandEnv) landDirect(ctx context.Context, order []string) (*LandReport, error) {
	for i, rig := range order {
		if e.Rec.Rigs[rig].State == RigLanded {
			continue
		}
		e.printf("[%d/%d] Landing %s ...\n", i+1, len(order), rig)
		sha, err := e.mergeDirect(rig, i+1, len(order), order)
		if err != nil {
			_ = e.setRig(rig, func(rs *RigState) { rs.State, rs.Error = RigHeld, err.Error() })
			_ = e.update(func(r *Record) { r.State = StateHeld })
			e.printf("  %s held: %v\n", rig, err)
			e.printSummary()
			return &LandReport{Outcome: OutcomeHeld}, nil
		}
		if err := e.setRig(rig, func(rs *RigState) { rs.State, rs.LandedCommit, rs.Error = RigLanded, sha, "" }); err != nil {
			return nil, err
		}
		e.printf("  %s landed at %s\n", rig, short8(sha))
	}
	return &LandReport{Outcome: OutcomeLanded}, nil
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// mergeDirect merges the feature branch into the base in a temporary worktree
// of the rig's repo, runs the rig's tests and pushes the base.
func (e *LandEnv) mergeDirect(rig string, n, m int, order []string) (string, error) {
	rec := e.Rec
	repo, ok := e.Repos[rig]
	if !ok {
		return "", fmt.Errorf("no repository")
	}
	base := rec.Rigs[rig].Base
	if err := fetchRig(repo, base, rec.Branch, true); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "gt-land-"+rig+"-")
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = git(repo.GitDir, "worktree", "remove", "--force", tmp)
		_ = os.RemoveAll(tmp)
		_, _ = git(repo.GitDir, "worktree", "prune")
	}()
	if _, err := git(repo.GitDir, "worktree", "add", "--detach", tmp, remoteRef(base)); err != nil {
		return "", err
	}
	var ident []string
	if name, _, _ := gitCode(tmp, "", nil, "config", "user.name"); name == "" {
		ident = append(ident, "GIT_AUTHOR_NAME=Gas Town", "GIT_COMMITTER_NAME=Gas Town")
	}
	if mail, _, _ := gitCode(tmp, "", nil, "config", "user.email"); mail == "" {
		ident = append(ident, "GIT_AUTHOR_EMAIL=gt@localhost", "GIT_COMMITTER_EMAIL=gt@localhost")
	}
	msg := fmt.Sprintf("Merge feature %s: %s (%s %d of %d)\n\nFeature: %s\nBranch: %s\nLanding order: %s",
		rec.ID, rec.Title, rig, n, m, rec.ID, rec.Branch, strings.Join(order, ", "))
	if _, _, err := gitCode(tmp, "", ident, "merge", "--no-ff", "-m", msg, remoteRef(rec.Branch)); err != nil {
		_, _, _ = gitCode(tmp, "", nil, "merge", "--abort")
		return "", fmt.Errorf("merge: %w", err)
	}
	if e.TestCommand != nil {
		if cmdline := e.TestCommand(rig); cmdline != "" {
			e.printf("  running tests: %s\n", cmdline)
			c := exec.Command("sh", "-c", cmdline) //nolint:gosec // G204: operator-controlled rig config
			c.Dir = tmp
			var buf bytes.Buffer
			c.Stdout, c.Stderr = &buf, &buf
			if err := c.Run(); err != nil {
				tail := buf.String()
				if len(tail) > 800 {
					tail = tail[len(tail)-800:]
				}
				return "", fmt.Errorf("tests failed: %v: %s", err, strings.TrimSpace(tail))
			}
		}
	}
	sha, _, err := gitCode(tmp, "", nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, _, err := gitCode(tmp, "", []string{"GT_INTEGRATION_LAND=1"}, "push", "origin", "HEAD:refs/heads/"+base); err != nil {
		return "", fmt.Errorf("push: %w", err)
	}
	return sha, nil
}

// ---- PR strategy ----

func (e *LandEnv) provider(rig string) (vcs.Provider, error) {
	repo, ok := e.Repos[rig]
	if !ok {
		return nil, fmt.Errorf("rig %s: no repository", rig)
	}
	return NewProvider(rig, repo, e.Settings.VCSProvider)
}

func (e *LandEnv) landPR(ctx context.Context, order []string) (*LandReport, error) {
	if rep, err := e.openPRs(ctx, order); rep != nil || err != nil {
		return rep, err
	}
	return e.advancePRs(ctx, order)
}

func (e *LandEnv) prBody(rig string, n, m int, order []string, links map[string]string) string {
	rec := e.Rec
	var b strings.Builder
	fmt.Fprintf(&b, "Part of multi-repo feature %s: %s\n\n", rec.ID, rec.Title)
	fmt.Fprintf(&b, "This PR is %s, %d of %d. Landing order: %s.\n", rig, n, m, strings.Join(order, ", "))
	if len(order) > 1 {
		b.WriteString("\nSibling repositories:\n")
		for _, o := range order {
			if o == rig {
				continue
			}
			if u := links[o]; u != "" {
				fmt.Fprintf(&b, "- %s: %s\n", o, u)
			} else {
				fmt.Fprintf(&b, "- %s\n", o)
			}
		}
		b.WriteString("\nThese PRs land together; none merges until all are approved and passing.\n")
	}
	return b.String()
}

// openPRs is stage a: a PR for every rig that has none. It returns a non-nil
// report when it had to stop.
func (e *LandEnv) openPRs(ctx context.Context, order []string) (*LandReport, error) {
	rec := e.Rec
	created := false
	m := len(order)
	for i, rig := range order {
		rs := rec.Rigs[rig]
		if rs.PRID != "" || rs.State == RigLanded {
			continue
		}
		prov, err := e.provider(rig)
		if err != nil {
			return e.holdRig(rig, fmt.Errorf("provider: %w", err))
		}
		pr, err := prov.CreatePR(ctx, vcs.CreatePRRequest{
			RepoDir:     e.Repos[rig].GitDir,
			Head:        rec.Branch,
			Base:        rs.Base,
			Title:       fmt.Sprintf("%s (%s %d of %d)", rec.Title, rig, i+1, m),
			Body:        e.prBody(rig, i+1, m, order, nil),
			MergeMethod: LandMergeMethod,
		})
		if err != nil {
			return e.holdRig(rig, fmt.Errorf("opening PR: %w", err))
		}
		if err := e.setRig(rig, func(s *RigState) {
			s.State, s.PRURL, s.PRID, s.PRHead, s.Error = RigPROpen, pr.URL, pr.ID, pr.HeadSHA, ""
		}); err != nil {
			return nil, err
		}
		rec = e.Rec
		created = true
		e.printf("  %s: opened %s\n", rig, pr.URL)
	}
	if !created {
		return nil, nil
	}
	links := map[string]string{}
	for _, rig := range order {
		links[rig] = rec.Rigs[rig].PRURL
	}
	for i, rig := range order {
		rs := rec.Rigs[rig]
		if rs.PRID == "" || rs.State == RigLanded {
			continue
		}
		prov, err := e.provider(rig)
		if err == nil {
			err = prov.UpdatePRBody(ctx, &vcs.PR{ID: rs.PRID, URL: rs.PRURL}, e.prBody(rig, i+1, m, order, links))
		}
		if err != nil {
			e.printf("  Warning: could not link sibling PRs in %s: %v\n", rig, err)
		}
	}
	return nil, nil
}

func (e *LandEnv) holdRig(rig string, cause error) (*LandReport, error) {
	_ = e.setRig(rig, func(rs *RigState) { rs.State, rs.Error = RigHeld, cause.Error() })
	_ = e.update(func(r *Record) { r.State = StateHeld })
	e.printf("  %s held: %v\n", rig, cause)
	e.printSummary()
	return &LandReport{Outcome: OutcomeHeld}, nil
}

type prEval struct {
	rig     string
	pr      *vcs.PR
	merged  bool
	blocked []string
	pending []string
}

// readPR evaluates one open PR: state, base, checks and review.
func (e *LandEnv) readPR(ctx context.Context, rig string) (*prEval, error) {
	rs := e.Rec.Rigs[rig]
	prov, err := e.provider(rig)
	if err != nil {
		return nil, err
	}
	pr, err := prov.GetPR(ctx, rs.PRID)
	if err != nil {
		return nil, fmt.Errorf("rig %s: reading PR %s: %w", rig, rs.PRID, err)
	}
	ev := &prEval{rig: rig, pr: pr}
	switch pr.State {
	case vcs.StateMerged:
		ev.merged = true
		return ev, nil
	case vcs.StateClosed:
		ev.blocked = append(ev.blocked, "PR was closed without merging")
		return ev, nil
	}
	if pr.Base != "" && pr.Base != rs.Base {
		ev.blocked = append(ev.blocked, fmt.Sprintf("PR targets %s, expected %s", pr.Base, rs.Base))
	}
	cs, err := prov.Checks(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("rig %s: reading checks: %w", rig, err)
	}
	switch cs.State {
	case vcs.ChecksFailing:
		ev.blocked = append(ev.blocked, "checks failing: "+strings.Join(cs.Failing, ", "))
	case vcs.ChecksPending:
		ev.pending = append(ev.pending, "checks pending")
	}
	if e.Settings.RequireReview {
		rv, err := prov.ReviewState(ctx, pr)
		if err != nil {
			return nil, fmt.Errorf("rig %s: reading review state: %w", rig, err)
		}
		switch rv {
		case vcs.ReviewRejected:
			ev.blocked = append(ev.blocked, "review rejected")
		case vcs.ReviewPending:
			ev.pending = append(ev.pending, "review pending")
		}
	}
	return ev, nil
}

// advancePRs is stage b: merge in landing order once every PR is passing and
// approved; hold the whole set when any PR is rejected or failing.
func (e *LandEnv) advancePRs(ctx context.Context, order []string) (*LandReport, error) {
	var evals []*prEval
	for _, rig := range order {
		if e.Rec.Rigs[rig].State == RigLanded {
			continue
		}
		ev, err := e.readPR(ctx, rig)
		if err != nil {
			return nil, err // transient: stay in landing, retry on the next run
		}
		evals = append(evals, ev)
	}

	// Record what was seen: merged-outside PRs, evaluated heads.
	for _, ev := range evals {
		ev := ev
		if err := e.setRig(ev.rig, func(rs *RigState) {
			if ev.merged {
				rs.State, rs.LandedCommit, rs.Error = RigLanded, ev.pr.MergeCommit, ""
				return
			}
			if ev.pr.HeadSHA != "" {
				rs.PRHead = ev.pr.HeadSHA
			}
			if ev.pr.URL != "" {
				rs.PRURL = ev.pr.URL
			}
		}); err != nil {
			return nil, err
		}
		if ev.merged {
			e.printf("  %s: PR %s already merged\n", ev.rig, ev.pr.URL)
		}
	}

	var blocked, pending []*prEval
	for _, ev := range evals {
		switch {
		case ev.merged:
		case len(ev.blocked) > 0:
			blocked = append(blocked, ev)
		case len(ev.pending) > 0:
			pending = append(pending, ev)
		}
	}
	if len(blocked) > 0 {
		for _, ev := range blocked {
			ev := ev
			_ = e.setRig(ev.rig, func(rs *RigState) { rs.State, rs.Error = RigHeld, strings.Join(ev.blocked, "; ") })
			e.printf("  %s: %s\n", ev.rig, strings.Join(ev.blocked, "; "))
		}
		_ = e.update(func(r *Record) { r.State = StateHeld })
		e.printf("Feature %s is held: nothing merges until the blocked PR(s) are fixed. Re-run land to resume.\n", e.Rec.ID)
		e.printSummary()
		return &LandReport{Outcome: OutcomeHeld}, nil
	}
	if len(pending) > 0 {
		_ = e.update(func(r *Record) {
			r.State = StateLanding
			for _, ev := range pending {
				rs := r.Rigs[ev.rig]
				rs.State, rs.Error = RigPROpen, ""
				r.Rigs[ev.rig] = rs
			}
		})
		for _, ev := range pending {
			e.printf("  %s: waiting (%s) %s\n", ev.rig, strings.Join(ev.pending, ", "), ev.pr.URL)
		}
		e.printf("Waiting: no PR merges until all are passing and approved.\n")
		return &LandReport{Outcome: OutcomeWaiting}, nil
	}

	// Everything passing and approved: merge in order.
	heads := map[string]string{}
	for _, ev := range evals {
		if !ev.merged {
			heads[ev.rig] = ev.pr.HeadSHA
		}
	}
	for _, rig := range order {
		if e.Rec.Rigs[rig].State == RigLanded {
			continue
		}
		ev, err := e.readPR(ctx, rig)
		if err != nil {
			return nil, err
		}
		switch {
		case ev.merged:
			_ = e.setRig(rig, func(rs *RigState) { rs.State, rs.LandedCommit, rs.Error = RigLanded, ev.pr.MergeCommit, "" })
			continue
		case len(ev.blocked) > 0:
			return e.holdRig(rig, errors.New(strings.Join(ev.blocked, "; ")))
		case len(ev.pending) > 0:
			e.printf("  %s: waiting again (%s)\n", rig, strings.Join(ev.pending, ", "))
			return &LandReport{Outcome: OutcomeWaiting}, nil
		}
		want := heads[rig]
		if want != "" && ev.pr.HeadSHA != "" && ev.pr.HeadSHA != want {
			return e.holdRig(rig, fmt.Errorf("%w: head is %s, was %s when approved", vcs.ErrHeadMoved, short8(ev.pr.HeadSHA), short8(want)))
		}
		rs := e.Rec.Rigs[rig]
		if repo, ok := e.Repos[rig]; ok {
			if err := fetchRig(repo, rs.Base, e.Rec.Branch, true); err == nil {
				if files, cerr := conflictFiles(repo, rs.Base, e.Rec.Branch); cerr == nil && len(files) > 0 {
					return e.holdRig(rig, fmt.Errorf("base %s moved and now conflicts in: %s", rs.Base, strings.Join(files, ", ")))
				}
			}
		}
		prov, err := e.provider(rig)
		if err != nil {
			return nil, err
		}
		pr := ev.pr
		if err := prov.Merge(ctx, pr, vcs.MergeOptions{Method: LandMergeMethod, ExpectHead: want}); err != nil {
			return e.holdRig(rig, fmt.Errorf("merging PR %s: %w", rs.PRURL, err))
		}
		if err := e.setRig(rig, func(s *RigState) { s.State, s.LandedCommit, s.Error = RigLanded, pr.MergeCommit, "" }); err != nil {
			return nil, err
		}
		e.printf("  %s: merged %s\n", rig, rs.PRURL)
	}
	return &LandReport{Outcome: OutcomeLanded}, nil
}

// ---- completion ----

func (e *LandEnv) complete() error {
	rec := e.Rec
	if e.CloseEpic != nil {
		if err := e.CloseEpic("Feature landed in all repositories"); err != nil {
			return fmt.Errorf("closing epic %s: %w", rec.ID, err)
		}
	}
	var rigs []string
	for name := range rec.Rigs {
		rigs = append(rigs, name)
	}
	sort.Strings(rigs)
	for _, name := range rigs {
		repo, ok := e.Repos[name]
		if !ok {
			continue
		}
		if exists, err := BranchExists(repo, rec.Branch); err != nil {
			e.printf("Warning: rig %s: checking branch %s: %v\n", name, rec.Branch, err)
		} else if exists {
			if err := DeleteBranches([]RigRepo{repo}, rec.Branch); err != nil {
				e.printf("Warning: could not delete origin branch %s: %v\n", rec.Branch, err)
			}
		}
		if _, code, _ := gitCode("", repo.GitDir, nil, "show-ref", "--verify", "--quiet", "refs/heads/"+rec.Branch); code == 0 {
			if _, err := git(repo.GitDir, "branch", "-D", rec.Branch); err != nil {
				e.printf("Warning: rig %s: could not delete local branch %s: %v\n", name, rec.Branch, err)
			}
		}
		_, _, _ = gitCode("", repo.GitDir, nil, "update-ref", "-d", remoteRef(rec.Branch))
	}
	if err := e.update(func(r *Record) { r.State = StateLanded }); err != nil {
		return err
	}
	if e.OnLanded != nil {
		if err := e.OnLanded(); err != nil {
			e.printf("Warning: service map refresh failed: %v\n", err)
		}
	}
	e.printf("Feature %s landed.\n", rec.ID)
	e.printSummary()
	return nil
}

func (e *LandEnv) printSummary() {
	rec := e.Rec
	var landed, not []string
	order := rec.LandingOrder
	for _, rig := range order {
		rs := rec.Rigs[rig]
		switch rs.State {
		case RigLanded:
			landed = append(landed, fmt.Sprintf("%s (%s)", rig, short8(rs.LandedCommit)))
		default:
			s := rig + " (" + rs.State
			if rs.Error != "" {
				s += ": " + rs.Error
			}
			not = append(not, s+")")
		}
	}
	if len(landed) > 0 {
		e.printf("Landed:     %s\n", strings.Join(landed, ", "))
	}
	if len(not) > 0 {
		e.printf("Not landed: %s\n", strings.Join(not, ", "))
	}
	for name, rs := range rec.Rigs {
		if rs.State == RigSkipped {
			e.printf("Skipped:    %s (nothing ahead of %s)\n", name, rs.Base)
		}
	}
}
