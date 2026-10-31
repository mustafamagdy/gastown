package review

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLavish installs a fake lavish-axi on PATH, in the style of the fake gh
// of the vcs tests. Every call is logged with the lavish environment. For the
// command word (poll, reply, end, or the page for open):
//
//	poll.<n>.out / poll.<n>.exit  stdout and exit code of the n-th poll
//	                              (with no file the poll waits, like the real one)
//	open.out                      stdout of an open
func fakeLavish(t *testing.T) (dir string, calls func() []string) {
	t.Helper()
	dir = t.TempDir()
	bin := t.TempDir()
	script := `#!/bin/sh
printf 'state=%s port=%s telemetry=%s noopen=%s hosts=%s args=%s\n' "$LAVISH_AXI_STATE_DIR" "$LAVISH_AXI_PORT" "$LAVISH_AXI_TELEMETRY" "$LAVISH_AXI_NO_OPEN" "$LAVISH_AXI_ALLOWED_HOSTS" "$*" >> "$FAKE_DIR/calls.log"
case "$1" in
  poll)
    c=$(cat "$FAKE_DIR/poll.count" 2>/dev/null || echo 0); c=$((c+1)); echo $c > "$FAKE_DIR/poll.count"
    if [ -f "$FAKE_DIR/poll.$c.out" ]; then
      cat "$FAKE_DIR/poll.$c.out"
      [ -f "$FAKE_DIR/poll.$c.err" ] && cat "$FAKE_DIR/poll.$c.err" >&2
      [ -f "$FAKE_DIR/poll.$c.exit" ] && exit $(cat "$FAKE_DIR/poll.$c.exit")
      exit 0
    fi
    exec sleep 30 ;;
  reply|end) exit 0 ;;
  *) [ -f "$FAKE_DIR/open.out" ] && cat "$FAKE_DIR/open.out"; exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "lavish-axi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir, func() []string {
		b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		s := strings.TrimSpace(string(b))
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
}

func writeFake(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fakeMirror struct {
	mu    sync.Mutex
	calls []string
}

func (m *fakeMirror) Set(bead, add string, remove []string, comment string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, bead+"|+"+add+"|-"+strings.Join(remove, ",")+"|"+comment)
	return nil
}

func (m *fakeMirror) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

type fakeNotify struct {
	mu   sync.Mutex
	msgs []string
}

func (n *fakeNotify) fn(rec *Record, subject, msg string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, rec.Requester+"|"+subject+"|"+msg)
	return nil
}

func (n *fakeNotify) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...)
}

func engineFor(t *testing.T) (*Engine, string, *fakeMirror, *fakeNotify, string, func() []string) {
	t.Helper()
	t.Setenv("GT_DOLT_PORT", "3307")
	town := t.TempDir()
	fdir, calls := fakeLavish(t)
	m, n := &fakeMirror{}, &fakeNotify{}
	eng := NewEngine(town, &Client{TownRoot: town}, m, n.fn, t.Logf)
	t.Cleanup(eng.Stop)
	return eng, town, m, n, fdir, calls
}

func mkOpen(t *testing.T, town, id, kind string) *Record {
	t.Helper()
	page := filepath.Join(AssetDir(town, id), "p.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("<body></body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &Record{ID: id, Bead: "gt-1", Kind: kind, HTML: page, State: StateOpen, Requester: "rig/polecats/nux"}
	if err := Create(town, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func waitState(t *testing.T, town, id string, states ...string) *Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := Load(town, id)
		if err == nil {
			for _, s := range states {
				if rec.State == s {
					return rec
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	rec, _ := Load(town, id)
	t.Fatalf("review %s did not reach %v; now %+v", id, states, rec)
	return nil
}

func pollOut(status, extra string) string {
	return "session:\n  file: /x/p.html\n  status: " + status + "\n" + extra + "next_step: x\n"
}

const approveRow = `prompts[1]{uid,prompt,selector,tag,text}:
  "","Approved: ship it\n\nContext data:\n{\n  \"decision\": \"approve\",\n  \"answer\": \"ship it\"\n}","",gt-review,Approved
`

func TestTickIsNoopWithoutOpenReviews(t *testing.T) {
	eng, town, _, _, _, calls := engineFor(t)
	eng.Tick()
	newRec(t, town, "rv-00000001", "gt-1", KindPlan, StateApproved, time.Hour)
	eng.Tick()
	time.Sleep(100 * time.Millisecond)
	if len(eng.Running()) != 0 || len(calls()) != 0 {
		t.Fatalf("started work with nothing open: running=%v calls=%v", eng.Running(), calls())
	}
}

func TestApproveFlow(t *testing.T) {
	eng, town, mir, nt, fdir, calls := engineFor(t)
	rec := mkOpen(t, town, "rv-0000000a", KindApproval)
	writeFake(t, fdir, "poll.1.out", pollOut("feedback", approveRow))
	eng.Tick()
	got := waitState(t, town, rec.ID, StateApproved)
	if got.DecidedAt == nil || len(got.Feedback) != 1 || got.Feedback[0].Decision != DecisionApprove || got.Feedback[0].Text != "ship it" {
		t.Fatalf("record = %+v", got)
	}
	// Mirror: pending replaced by approved, comment holds the feedback.
	deadline := time.Now().Add(5 * time.Second)
	for len(nt.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	m := mir.all()
	if len(m) != 1 || !strings.Contains(m[0], "gt-1|+review:approved|-review:pending,review:rejected") || !strings.Contains(m[0], "ship it") {
		t.Fatalf("mirror = %v", m)
	}
	if msgs := nt.all(); len(msgs) != 1 || !strings.HasPrefix(msgs[0], "rig/polecats/nux|") || !strings.Contains(msgs[0], "APPROVED") {
		t.Fatalf("notify = %v", msgs)
	}
	// The browser stops showing Working: a reply was sent, and every call used the town's env.
	var polled, replied bool
	for _, c := range calls() {
		if !strings.Contains(c, "state="+StateDir(town)) || !strings.Contains(c, "port=4387") || !strings.Contains(c, "telemetry=0") || !strings.Contains(c, "noopen=1") {
			t.Errorf("call without the town's lavish env: %s", c)
		}
		polled = polled || strings.Contains(c, "args=poll ") && strings.Contains(c, "--owner gt-review")
		replied = replied || strings.Contains(c, "args=reply ") && strings.Contains(c, "--agent-reply")
	}
	if !polled || !replied {
		t.Fatalf("polled=%v replied=%v calls=%v", polled, replied, calls())
	}
	// The decided review is not polled again.
	time.Sleep(100 * time.Millisecond)
	eng.Tick()
	time.Sleep(100 * time.Millisecond)
	if n := len(eng.Running()); n != 0 {
		t.Fatalf("decided review is still watched (%d)", n)
	}
}

func TestChangesRejectedAndQuestionAnswered(t *testing.T) {
	eng, town, mir, _, fdir, _ := engineFor(t)
	mkOpen(t, town, "rv-0000000b", KindDesign)
	writeFake(t, fdir, "poll.1.out", pollOut("feedback", `prompts[1]{uid,prompt,selector,tag,text}:
  "","Changes requested: too wide\n\nContext data:\n{\"decision\":\"changes\",\"answer\":\"too wide\"}","",gt-review,"Changes requested"
`))
	eng.Tick()
	waitState(t, town, "rv-0000000b", StateRejected)
	if m := mir.all(); len(m) != 1 || !strings.Contains(m[0], "+review:rejected") {
		t.Fatalf("mirror = %v", m)
	}

	// A question is answered by the answer decision.
	mkOpen(t, town, "rv-0000000c", KindQuestion)
	writeFake(t, fdir, "poll.2.out", pollOut("feedback", `prompts[1]{uid,prompt,selector,tag,text}:
  "","Answer: blue\n\nContext data:\n{\"decision\":\"answer\",\"answer\":\"blue\"}","",gt-review,Answer
`))
	eng.Tick()
	rec := waitState(t, town, "rv-0000000c", StateAnswered)
	if rec.Feedback[0].Text != "blue" {
		t.Fatalf("answer = %+v", rec.Feedback)
	}
}

func TestFreeFormFeedbackKeepsReviewOpen(t *testing.T) {
	eng, town, mir, nt, fdir, calls := engineFor(t)
	mkOpen(t, town, "rv-0000000d", KindPlan)
	writeFake(t, fdir, "poll.1.out", pollOut("feedback", `prompts[1]{uid,prompt,selector,tag,text}:
  "","fix the title","h1",annotation,"fix the title"
`))
	eng.Tick()
	deadline := time.Now().Add(10 * time.Second)
	for len(nt.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	rec, err := Load(town, "rv-0000000d")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateOpen || len(rec.Feedback) != 1 || rec.Feedback[0].Selector != "h1" {
		t.Fatalf("record = %+v", rec)
	}
	if m := mir.all(); len(m) != 1 || !strings.Contains(m[0], "+review:pending") {
		t.Fatalf("mirror = %v", m)
	}
	// It keeps polling (a second poll starts and waits).
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, c := range calls() {
			if strings.Contains(c, "args=poll ") {
				n++
			}
		}
		if n >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("review stopped being polled: %v", calls())
}

func TestSendAndEndAndDisconnectAndFailures(t *testing.T) {
	eng, town, _, nt, fdir, calls := engineFor(t)

	// Send & End with an approval: decided, and no reply to an ended session.
	mkOpen(t, town, "rv-0000000e", KindApproval)
	writeFake(t, fdir, "poll.1.out", "session:\n  file: /x/p.html\n  status: feedback\n  session_ended: true\n  ended_by: user\n"+approveRow+"next_step: x\n")
	eng.Tick()
	waitState(t, town, "rv-0000000e", StateApproved)

	// Send & End without a decision.
	mkOpen(t, town, "rv-0000000f", KindPlan)
	writeFake(t, fdir, "poll.2.out", "session:\n  file: /x/p.html\n  status: feedback\n  session_ended: true\n  ended_by: user\nprompts[1]{uid,prompt,selector,tag,text}:\n  \"\",\"looks odd\",\"\",annotation,\"looks odd\"\nnext_step: x\n")
	eng.Tick()
	rec := waitState(t, town, "rv-0000000f", StateEnded)
	if len(rec.Feedback) != 1 || rec.Feedback[0].Text != "looks odd" {
		t.Fatalf("feedback lost on end: %+v", rec.Feedback)
	}

	// Browser disconnected: kept resumable, not polled again.
	mkOpen(t, town, "rv-00000010", KindPlan)
	writeFake(t, fdir, "poll.3.out", pollOut("browser_disconnected", ""))
	eng.Tick()
	waitState(t, town, "rv-00000010", StateDisconnected)
	time.Sleep(150 * time.Millisecond)
	before := len(calls())
	eng.Tick()
	time.Sleep(150 * time.Millisecond)
	if len(calls()) != before {
		t.Fatalf("a disconnected review was polled or reopened by the daemon: %v", calls()[before:])
	}

	// Artifact failure: recorded, requester told, review stays open.
	mkOpen(t, town, "rv-00000011", KindPlan)
	writeFake(t, fdir, "poll.4.out", pollOut("feedback", "artifact_failures[1]{kind,detail}:\n  script-error,\"boom\"\n"))
	eng.Tick()
	deadline := time.Now().Add(10 * time.Second)
	var got *Record
	for time.Now().Before(deadline) {
		got, _ = Load(town, "rv-00000011")
		if got != nil && len(got.Feedback) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil || got.State != StateOpen || got.Feedback[0].Tag != "artifact_failure" || !strings.Contains(got.Feedback[0].Text, "boom") {
		t.Fatalf("artifact failure record = %+v", got)
	}
	found := false
	for _, m := range nt.all() {
		found = found || strings.Contains(m, "failed to render")
	}
	if !found {
		t.Fatalf("requester not told about the failure: %v", nt.all())
	}
}

func TestListenerActiveTakesOver(t *testing.T) {
	eng, town, _, _, fdir, calls := engineFor(t)
	mkOpen(t, town, "rv-00000012", KindApproval)
	writeFake(t, fdir, "poll.1.out", "")
	writeFake(t, fdir, "poll.1.err", "error: already listening\ncode: LISTENER_ACTIVE\n")
	writeFake(t, fdir, "poll.1.exit", "1")
	writeFake(t, fdir, "poll.2.out", pollOut("feedback", approveRow))
	eng.Tick()
	waitState(t, town, "rv-00000012", StateApproved)
	cs := calls()
	var polls []string
	for _, c := range cs {
		if strings.Contains(c, "args=poll ") {
			polls = append(polls, c)
		}
	}
	if len(polls) != 2 || strings.Contains(polls[0], "--takeover") || !strings.Contains(polls[1], "--takeover") {
		t.Fatalf("want a plain poll then a takeover, got %v", polls)
	}
}

func TestCancelledReviewIsNotReportedAgain(t *testing.T) {
	eng, town, mir, nt, _, _ := engineFor(t)
	rec := mkOpen(t, town, "rv-00000013", KindPlan)
	if _, err := Update(town, rec.ID, func(r *Record) error { r.State = StateEnded; return nil }); err != nil {
		t.Fatal(err)
	}
	eng.Handle(rec.ID, &PollResult{Status: "ended", EndedBy: "agent"})
	if len(mir.all()) != 0 || len(nt.all()) != 0 {
		t.Fatalf("cancel was reported: %v %v", mir.all(), nt.all())
	}
}
