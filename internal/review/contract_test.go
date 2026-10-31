package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLavishContract drives a real lavish-axi session and pins the poll output
// gt parses (Tools policy: lavish-axi poll has no JSON mode, so its TOON output
// is pinned here). It is skipped when lavish-axi is not installed. The test
// uses its own state directory and a free port, and stops its server.
//
// The browser sends prompts with POST /api/<session key>/prompts and a
// same-origin Origin header (chrome-client.js submitQueuedOnce); this test
// does the same with the body the gt panel produces: queuePrompt appends
// "\n\nContext data:\n" and the data as JSON to the prompt text.
func TestLavishContract(t *testing.T) {
	if _, err := exec.LookPath(LavishBin); err != nil {
		t.Skip("lavish-axi is not installed")
	}
	t.Setenv("GT_DOLT_PORT", "3307")
	town := t.TempDir()
	port := freePort(t)
	if err := os.MkdirAll(filepath.Join(town, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"type":"town-settings","version":1,"review_port":%d}`, port)
	if err := os.WriteFile(filepath.Join(town, "settings", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{TownRoot: town}
	t.Cleanup(func() { _, _ = c.run(30*time.Second, "stop") })

	page := filepath.Join(town, "page.html")
	// The page goes through the same cleaning as an approval request, and
	// carries a script that must not survive.
	clean, removed, err := StripActiveContent("<!doctype html><html><body style=\"background:#fff\"><h1>Plan</h1><script>window.pageScript=1</script></body></html>")
	if err != nil || removed != 1 {
		t.Fatalf("StripActiveContent: removed=%d err=%v", removed, err)
	}
	html := InjectPanel(clean, KindApproval)
	if err := os.WriteFile(page, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	link, err := c.Open(page, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	key := link[strings.LastIndex(link, "/")+1:]
	if !strings.Contains(link, "/session/") || key == "" {
		t.Fatalf("unexpected session link %q", link)
	}

	// 1. A panel approval reaches poll as a gt-review prompt with its data.
	res := pollAfterPost(t, c, page, port, key, `{"prompts":[`+panelPrompt(t, DecisionApprove, "ship it")+`]}`)
	if res.Status != "feedback" || res.SessionEnded || len(res.Prompts) != 1 {
		t.Fatalf("poll result = %+v", res)
	}
	d, ok := res.Prompts[0].Panel()
	if !ok || d.Decision != DecisionApprove || d.Answer != "ship it" {
		t.Fatalf("panel decision = %+v ok=%v prompt=%q", d, ok, res.Prompts[0].Prompt)
	}
	if err := c.Reply(page, "Approved. The agent has been told."); err != nil {
		t.Fatalf("reply: %v", err)
	}

	// 2. Free-form annotation plus Send & End: the session ends with the feedback.
	body := `{"endSession":true,"prompts":[{"prompt":"make it \"wider\", please","selector":"h1","tag":"annotation","text":"make it wider"}]}`
	res = pollAfterPost(t, c, page, port, key, body)
	if res.Status != "feedback" || !res.SessionEnded || res.EndedBy != "user" || len(res.Prompts) != 1 {
		t.Fatalf("poll result after Send & End = %+v", res)
	}
	p := res.Prompts[0]
	if p.Tag != "annotation" || p.Selector != "h1" || p.Prompt != `make it "wider", please` || p.Text != "make it wider" {
		t.Fatalf("annotation prompt = %+v", p)
	}
	if _, ok := p.Panel(); ok {
		t.Fatal("annotation read as a panel decision")
	}

	// 3. A session the human ended is not reopened without --reopen.
	if _, err := c.Open(page, false); err != ErrUserEnded {
		t.Fatalf("open after user end: err = %v, want ErrUserEnded", err)
	}
	if _, err := c.Open(page, true); err != nil {
		t.Fatalf("open --reopen: %v", err)
	}
}

func panelPrompt(t *testing.T, decision, answer string) string {
	t.Helper()
	data, err := json.MarshalIndent(PanelData{Decision: decision, Answer: answer}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Approved: " + answer + "\n\nContext data:\n" + string(data)
	b, _ := json.Marshal(map[string]string{"prompt": prompt, "tag": PanelTag, "text": "Approved"})
	return string(b)
}

// pollAfterPost starts a real poll, posts a prompt body like the browser, and
// returns the parsed poll output.
func pollAfterPost(t *testing.T, c *Client, page string, port int, key, body string) *PollResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := c.PollCmd(ctx, page, false)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	time.Sleep(700 * time.Millisecond) // let the poll register as the listener

	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/%s/prompts", origin, key), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST prompts: %s", resp.Status)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("poll failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("poll did not return; stdout: %s", stdout.String())
	}
	res, err := ParsePoll(stdout.String())
	if err != nil {
		t.Fatalf("parsing poll output: %v\n%s", err, stdout.String())
	}
	return res
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
