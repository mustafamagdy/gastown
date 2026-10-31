package projectfeature

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/vcs"
)

func TestLandVisualReviewGateDirect(t *testing.T) {
	f := newLandFixture(t, "api", "web")
	f.commit("api", f.rec.Branch, "api.txt", "x")
	f.commit("web", f.rec.Branch, "web.txt", "x")
	f.task("t1", "api")
	f.task("t2", "web", "t1")
	f.save()
	f.settings.RequireVisualReview = true

	approved, msg := false, "visual review pending: rv-1 waiting for the reviewer"
	env := func() *LandEnv {
		e := f.env()
		e.VisualGate = func() (bool, string) { return approved, msg }
		return e
	}
	rep, err := Land(context.Background(), env())
	if err != nil || rep.Outcome != OutcomeWaiting {
		t.Fatalf("rep=%+v err=%v\n%s", rep, err, f.out)
	}
	if !strings.Contains(f.out.String(), "visual review pending: rv-1") {
		t.Fatalf("message missing:\n%s", f.out)
	}
	if f.mainFile("api", "api.txt") || f.load().State == StateLanded {
		t.Fatal("landed without approval")
	}

	msg = "visual review rejected: rv-1"
	if rep, err = Land(context.Background(), env()); err != nil || rep.Outcome != OutcomeWaiting {
		t.Fatalf("rejected: rep=%+v err=%v", rep, err)
	}

	// No gate function at all fails closed.
	if rep, err = Land(context.Background(), f.env()); err != nil || rep.Outcome != OutcomeWaiting {
		t.Fatalf("no gate: rep=%+v err=%v", rep, err)
	}

	approved = true
	if rep, err = Land(context.Background(), env()); err != nil || rep.Outcome != OutcomeLanded {
		t.Fatalf("approved: rep=%+v err=%v\n%s", rep, err, f.out)
	}
}

func TestLandVisualReviewGateStopsPRMerges(t *testing.T) {
	f, fv := prFixture(t)
	f.settings.RequireVisualReview = true
	approved := true
	env := func() *LandEnv {
		e := f.env()
		e.VisualGate = func() (bool, string) { return approved, "visual review rejected: rv-2" }
		return e
	}
	// Open the PRs while the human review of the PRs is pending.
	old := NewProvider
	NewProvider = func(rig string, _ RigRepo, _ string) (vcs.Provider, error) {
		return pendingProv{fakeProv{fv, rig}}, nil
	}
	if rep, err := Land(context.Background(), env()); err != nil || rep.Outcome != OutcomeWaiting || len(fv.merged) != 0 {
		t.Fatalf("open: rep=%+v err=%v", rep, err)
	}
	// The PRs are now approved, but the visual review was rejected.
	NewProvider = old
	approved = false
	rep, err := Land(context.Background(), env())
	if err != nil || rep.Outcome != OutcomeWaiting || len(fv.merged) != 0 {
		t.Fatalf("gated: rep=%+v err=%v merged=%v", rep, err, fv.merged)
	}
	if !strings.Contains(f.out.String(), "visual review rejected: rv-2") {
		t.Fatalf("message missing:\n%s", f.out)
	}
	// Approved: the merges go ahead.
	approved = true
	if rep, err = Land(context.Background(), env()); err != nil || rep.Outcome != OutcomeLanded || len(fv.merged) != 2 {
		t.Fatalf("approved: rep=%+v err=%v merged=%v\n%s", rep, err, fv.merged, f.out)
	}
}
