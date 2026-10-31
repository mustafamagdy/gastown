package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePollTabular(t *testing.T) {
	out := `session:
  file: /tmp/p.html
  status: feedback
prompts[2]{uid,prompt,selector,tag,text}:
  "","Approved: ok, fine\n\nContext data:\n{\n  \"decision\": \"approve\",\n  \"answer\": \"ok, fine\"\n}","",gt-review,Approved
  "","","h1",annotation,note on h1
next_step: "Apply the requested changes."
dom_snapshot: ""
`
	res, err := ParsePoll(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "feedback" || res.SessionEnded || len(res.Prompts) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	d, ok := res.Prompts[0].Panel()
	if !ok || d.Decision != DecisionApprove || d.Answer != "ok, fine" {
		t.Fatalf("panel decision = %+v ok=%v", d, ok)
	}
	if _, ok := res.Prompts[1].Panel(); ok {
		t.Fatal("annotation read as a panel decision")
	}
	if res.Prompts[1].Selector != "h1" || res.Prompts[1].Text != "note on h1" {
		t.Fatalf("annotation = %+v", res.Prompts[1])
	}
}

func TestParsePollListFormAndFailures(t *testing.T) {
	out := `session:
  file: /tmp/p.html
  status: feedback
  session_ended: true
  ended_by: user
prompts[2]:
  - uid: ""
    prompt: hello
    tag: gt-review
    text: x
    target:
      type: element
      label: "<div>"
  - uid: ""
    prompt: second
    tag: ""
    text: ""
artifact_failures[1]{kind,detail}:
  script-error,"boom: bad"
next_step: x
`
	res, err := ParsePoll(out)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SessionEnded || res.EndedBy != "user" || len(res.Prompts) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Prompts[0].Prompt != "hello" || res.Prompts[1].Prompt != "second" {
		t.Fatalf("prompts = %+v", res.Prompts)
	}
	if len(res.Failures) != 1 || res.Failures[0] != "script-error: boom: bad" {
		t.Fatalf("failures = %v", res.Failures)
	}
}

func TestParsePollOtherStatuses(t *testing.T) {
	res, err := ParsePoll("session:\n  file: /a.html\n  status: browser_disconnected\nnext_step: x\n")
	if err != nil || res.Status != "browser_disconnected" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = ParsePoll("session:\n  file: /a.html\n  status: ended\n  ended_by: user\nnext_step: x\n")
	if err != nil || res.Status != "ended" || res.EndedBy != "user" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ParsePoll("error: nope\ncode: NOT_FOUND\n"); err == nil {
		t.Fatal("error output must not parse as a result")
	}
}

func TestEnvironIsolatesTowns(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "3307")
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := []string{"PATH=/bin", "LAVISH_AXI_PORT=1", "LAVISH_AXI_ALLOWED_HOSTS=other.example", "LAVISH_AXI_TELEMETRY=1"}
	env := envMap(Environ(town, base))
	if env["LAVISH_AXI_STATE_DIR"] != filepath.Join(town, ".runtime", "lavish") {
		t.Fatalf("state dir = %q", env["LAVISH_AXI_STATE_DIR"])
	}
	if env["LAVISH_AXI_PORT"] != "4387" {
		t.Fatalf("port = %q (want Dolt default + 1080)", env["LAVISH_AXI_PORT"])
	}
	if env["LAVISH_AXI_TELEMETRY"] != "0" || env["LAVISH_AXI_NO_OPEN"] != "1" {
		t.Fatalf("telemetry/no-open = %q/%q", env["LAVISH_AXI_TELEMETRY"], env["LAVISH_AXI_NO_OPEN"])
	}
	if _, ok := env["LAVISH_AXI_ALLOWED_HOSTS"]; ok {
		t.Fatal("another org's allowed hosts leaked in")
	}
	if env["PATH"] != "/bin" {
		t.Fatal("PATH lost")
	}

	cfg := `{"type":"town-settings","version":1,"review_port":4500,"review_public_url":"https://gastown-test.tail616a3d.ts.net:8443"}`
	if err := os.WriteFile(filepath.Join(town, "settings", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	env = envMap(Environ(town, base))
	if env["LAVISH_AXI_PORT"] != "4500" || env["LAVISH_AXI_ALLOWED_HOSTS"] != "gastown-test.tail616a3d.ts.net" {
		t.Fatalf("port/hosts = %q/%q", env["LAVISH_AXI_PORT"], env["LAVISH_AXI_ALLOWED_HOSTS"])
	}
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestPublicLink(t *testing.T) {
	got := PublicLink("http://gastown-test.tail616a3d.ts.net:4388/session/abc123", "https://gastown-test.tail616a3d.ts.net:8443/")
	if got != "https://gastown-test.tail616a3d.ts.net:8443/session/abc123" {
		t.Fatalf("got %q", got)
	}
	if PublicLink("http://127.0.0.1:4387/session/x", "") != "http://127.0.0.1:4387/session/x" {
		t.Fatal("link changed without a public url")
	}
}

func TestInjectPanel(t *testing.T) {
	out := InjectPanel("<html><body><h1>x</h1></BODY></html>", KindApproval)
	i, j := strings.Index(out, "gt-review-panel"), strings.LastIndex(out, "</BODY>")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("panel not before </body>: %q", out)
	}
	for _, want := range []string{"Approve", "Request changes", "gt-review-answer", "queuePrompt", "sendQueuedPrompts", "'gt-review'"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel misses %q", want)
		}
	}
	q := InjectPanel("<body></body>", KindQuestion)
	if strings.Contains(q, "gt-review-approve") && strings.Contains(q, `id="gt-review-approve"`) {
		t.Error("question panel must show only the answer box")
	}
	if !strings.Contains(InjectPanel("<p>no body tag</p>", KindPlan), "gt-review-panel") {
		t.Error("page without </body> got no panel")
	}
}

func TestCopyPageAssetsAndCap(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "p.html"), []byte("<body></body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "img", "a.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	page, err := CopyPage(filepath.Join(src, "p.html"), dst)
	if err != nil {
		t.Fatal(err)
	}
	if page != filepath.Join(dst, "p.html") {
		t.Fatalf("page = %s", page)
	}
	if _, err := os.Stat(filepath.Join(dst, "img", "a.png")); err != nil {
		t.Fatal("sibling asset not copied")
	}
	if _, err := os.Lstat(filepath.Join(dst, "link")); err == nil {
		t.Fatal("symlink was followed or copied")
	}

	// Over the cap: a sparse file is enough, nothing is written to disk.
	big := t.TempDir()
	if err := os.WriteFile(filepath.Join(big, "p.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(big, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxAssetBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	dst2 := t.TempDir()
	if _, err := CopyPage(filepath.Join(big, "p.html"), dst2); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want size refusal, got %v", err)
	}
	if entries, _ := os.ReadDir(dst2); len(entries) != 0 {
		t.Fatal("files were copied before the size check")
	}
}

func newRec(t *testing.T, town, id, bead, kind, state string, age time.Duration) {
	t.Helper()
	rec := &Record{ID: id, Bead: bead, Kind: kind, State: state, HTML: "/x.html"}
	if err := Create(town, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(town, id, func(r *Record) error { r.CreatedAt = time.Now().Add(-age); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRoundTripAndHasOpen(t *testing.T) {
	town := t.TempDir()
	if HasOpen(town) {
		t.Fatal("empty town has open reviews")
	}
	newRec(t, town, "rv-00000001", "gt-1", KindPlan, StateApproved, time.Hour)
	if HasOpen(town) {
		t.Fatal("approved review counted as open")
	}
	newRec(t, town, "rv-00000002", "gt-1", KindPlan, StateOpen, time.Minute)
	if !HasOpen(town) {
		t.Fatal("open review not seen")
	}
	if err := Create(town, &Record{ID: "rv-00000002"}); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if _, err := Load(town, "../etc"); err == nil {
		t.Fatal("path-like id accepted")
	}
}

func TestFeatureGate(t *testing.T) {
	town := t.TempDir()
	beads := []string{"epic-1", "task-1", "task-2"}

	if g := FeatureGate(town, beads); g.Status != GatePending || !strings.Contains(g.Message, "no approved review") {
		t.Fatalf("no reviews: %+v", g)
	}
	newRec(t, town, "rv-00000001", "task-1", KindApproval, StateApproved, 3*time.Hour)
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("approved: %+v", g)
	}
	// Another kind of review does not count, and does not block.
	newRec(t, town, "rv-00000002", "task-2", KindPlan, StateOpen, time.Hour)
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("plan review changed the gate: %+v", g)
	}
	// An open approval review blocks.
	newRec(t, town, "rv-00000003", "task-2", KindApproval, StateOpen, time.Hour)
	if g := FeatureGate(town, beads); g.Status != GatePending || !strings.Contains(g.Message, "rv-00000003") {
		t.Fatalf("open: %+v", g)
	}
	// A rejected one blocks and says so.
	if _, err := Update(town, "rv-00000003", func(r *Record) error { r.State = StateRejected; return nil }); err != nil {
		t.Fatal(err)
	}
	if g := FeatureGate(town, beads); g.Status != GateRejected {
		t.Fatalf("rejected: %+v", g)
	}
	// A newer approved review on the same bead replaces the rejected one.
	newRec(t, town, "rv-00000004", "task-2", KindApproval, StateApproved, time.Minute)
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("superseded rejection still blocks: %+v", g)
	}
	// A cancelled review does not undo an approval.
	newRec(t, town, "rv-00000005", "task-2", KindApproval, StateEnded, time.Second)
	if g := FeatureGate(town, beads); !g.OK() {
		t.Fatalf("cancelled review undid the approval: %+v", g)
	}
}

func TestFeatureGateFailsClosedOnUnreadableStore(t *testing.T) {
	town := t.TempDir()
	newRec(t, town, "rv-00000001", "task-1", KindApproval, StateApproved, time.Hour)
	if err := os.WriteFile(filepath.Join(Dir(town), "rv-00000009.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := FeatureGate(town, []string{"task-1"})
	if g.OK() || !strings.Contains(g.Message, "unreadable") {
		t.Fatalf("store error must not approve: %+v", g)
	}
	if blocked, _ := MergeBlock(town, "task-1", ""); !blocked {
		t.Fatal("unreadable store must block merges")
	}
}

func TestMergeBlock(t *testing.T) {
	town := t.TempDir()
	if blocked, _ := MergeBlock(town, "gt-1", ""); blocked {
		t.Fatal("a town without reviews blocked a merge")
	}
	newRec(t, town, "rv-00000001", "gt-1", KindApproval, StateOpen, time.Hour)
	newRec(t, town, "rv-00000002", "gt-2", KindApproval, StateApproved, time.Hour)
	newRec(t, town, "rv-00000003", "gt-3", KindPlan, StateOpen, time.Hour)
	newRec(t, town, "rv-00000004", "gt-4", KindApproval, StateRejected, time.Hour)
	for bead, want := range map[string]bool{"gt-1": true, "gt-2": false, "gt-3": false, "gt-4": true, "gt-5": false, "": false} {
		if blocked, why := MergeBlock(town, bead, ""); blocked != want {
			t.Errorf("MergeBlock(%q) = %v (%s), want %v", bead, blocked, why, want)
		}
	}
}
