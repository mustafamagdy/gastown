package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// LavishBin is the lavish-axi executable name, looked up on PATH.
const LavishBin = "lavish-axi"

// PollOwner is the label the daemon's poll gives lavish, so session listings
// show who is listening.
const PollOwner = "gt-review"

// idleTimeoutMS keeps the town's lavish server alive for a week with nothing
// connected. Lavish stops an idle server after 30 minutes by default; a review
// whose browser was closed has no poll, so its link would die while the human
// is away. Sessions themselves are stored on disk by lavish.
const idleTimeoutMS = 7 * 24 * 60 * 60 * 1000

// defaultDoltPort is the Dolt port when the town does not name one.
const defaultDoltPort = 3307

// portOffset puts the review port next to the Dolt port, one town one port.
const portOffset = 1080

// StateDir is lavish's state directory for the town. Towns never share one.
func StateDir(townRoot string) string { return filepath.Join(townRoot, ".runtime", "lavish") }

// TownSettings returns the town's review settings: the port lavish listens on
// and the public base URL ("" when unset).
func TownSettings(townRoot string) (port int, publicURL string) {
	var s *config.TownSettings
	if loaded, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); err == nil {
		s = loaded
	}
	if s != nil && s.ReviewPort > 0 {
		port = s.ReviewPort
	} else {
		dolt := config.ResolveDoltPort(townRoot)
		if dolt <= 0 {
			dolt = defaultDoltPort
		}
		port = dolt + portOffset
	}
	if s != nil {
		publicURL = strings.TrimSpace(s.ReviewPublicURL)
	}
	return port, publicURL
}

// Environ returns base with the lavish settings of a town applied. Every
// lavish call from gt uses it, so each town (org) gets its own server, state
// and port, and nothing is sent to lavish's telemetry.
func Environ(townRoot string, base []string) []string {
	port, publicURL := TownSettings(townRoot)
	set := map[string]string{
		"LAVISH_AXI_STATE_DIR":       StateDir(townRoot),
		"LAVISH_AXI_PORT":            strconv.Itoa(port),
		"LAVISH_AXI_TELEMETRY":       "0", // "0", "false" or "off" turn it off
		"LAVISH_AXI_NO_OPEN":         "1",
		"LAVISH_AXI_IDLE_TIMEOUT_MS": strconv.Itoa(idleTimeoutMS),
	}
	if publicURL != "" {
		// Lavish rejects requests whose Host header it does not know. The
		// public name (for example a Tailscale Serve name) must be allowed.
		if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
			set["LAVISH_AXI_ALLOWED_HOSTS"] = u.Hostname()
		}
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[k]; replaced {
			continue
		}
		if k == "LAVISH_AXI_ALLOWED_HOSTS" && set["LAVISH_AXI_ALLOWED_HOSTS"] == "" {
			continue // never inherit another org's allow list
		}
		out = append(out, kv)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// PublicLink rewrites the link lavish printed to the town's public base URL.
// With no public URL the link is returned as it is.
func PublicLink(lavishURL, publicBase string) string {
	if strings.TrimSpace(publicBase) == "" {
		return lavishURL
	}
	u, err := url.Parse(lavishURL)
	if err != nil {
		return lavishURL
	}
	base := strings.TrimRight(strings.TrimSpace(publicBase), "/")
	out := base + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// Client runs lavish-axi for one town.
type Client struct {
	TownRoot string
	Bin      string   // defaults to LavishBin
	Base     []string // base environment; defaults to os.Environ()
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return LavishBin
}

func (c *Client) env() []string {
	base := c.Base
	if base == nil {
		base = os.Environ()
	}
	return Environ(c.TownRoot, base)
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin(), args...) //nolint:gosec // G204: fixed binary, explicit args, no shell
	cmd.Env = c.env()
	cmd.Dir = c.TownRoot
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// run executes a short lavish command and returns stdout.
func (c *Client) run(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := c.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", c.bin(), args[0], err, firstLines(msg, 3))
	}
	return stdout.Bytes(), nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "; ")
}

// Open starts or resumes the lavish session of a page and returns its link
// as lavish printed it. It never blocks on feedback. reopen resumes a session
// the human ended; pass it only when a person asked for that.
func (c *Client) Open(html string, reopen bool) (string, error) {
	args := []string{html, "--no-open"}
	if reopen {
		args = append(args, "--reopen")
	}
	out, err := c.run(60*time.Second, args...)
	if err != nil {
		return "", err
	}
	doc, err := decodeTOON(string(out))
	if err != nil {
		return "", fmt.Errorf("reading lavish-axi output: %w", err)
	}
	sess, _ := doc["session"].(map[string]any)
	link, _ := sess["url"].(string)
	status, _ := sess["status"].(string)
	if link == "" {
		return "", errors.New("lavish-axi printed no session url")
	}
	if status == "user-ended" {
		return link, ErrUserEnded
	}
	return link, nil
}

// ErrUserEnded means the human ended the session in the browser, so lavish
// refuses to reopen it without an explicit request.
var ErrUserEnded = errors.New("the session was ended by the reviewer")

// Reply shows a short message in the browser so it stops showing Working.
func (c *Client) Reply(html, message string) error {
	_, err := c.run(30*time.Second, "reply", html, "--agent-reply", message)
	return err
}

// End ends the lavish session as the agent. Ending it this way still allows a
// plain reopen later.
func (c *Client) End(html string) error {
	_, err := c.run(30*time.Second, "end", html)
	return err
}

// PollCmd builds the long poll for a page. The caller starts and owns it.
func (c *Client) PollCmd(ctx context.Context, html string, takeover bool) *exec.Cmd {
	args := []string{"poll", html, "--owner", PollOwner}
	if takeover {
		args = append(args, "--takeover")
	}
	return c.command(ctx, args...)
}

// Prompt is one prompt in poll output.
type Prompt struct {
	Tag      string
	Text     string // short label
	Prompt   string // full text, includes the "Context data" block when data was sent
	Selector string
}

// PollResult is the parsed output of one finished poll.
type PollResult struct {
	Status       string // feedback, ended, browser_disconnected, waiting
	SessionEnded bool
	EndedBy      string
	Prompts      []Prompt
	Failures     []string // fatal artifact failures, "kind: detail"
}

// ParsePoll reads the stdout of `lavish-axi poll`. Contract tests pin the
// format against the real tool.
func ParsePoll(stdout string) (*PollResult, error) {
	doc, err := decodeTOON(stdout)
	if err != nil {
		return nil, fmt.Errorf("reading poll output: %w", err)
	}
	sess, ok := doc["session"].(map[string]any)
	if !ok {
		return nil, errors.New("poll output has no session")
	}
	res := &PollResult{}
	res.Status, _ = sess["status"].(string)
	if res.Status == "" {
		return nil, errors.New("poll output has no session status")
	}
	res.SessionEnded = sess["session_ended"] == "true"
	res.EndedBy, _ = sess["ended_by"].(string)
	if res.Status == "ended" && res.EndedBy == "" {
		res.EndedBy = "agent"
	}
	if list, ok := doc["prompts"].([]any); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			p := Prompt{}
			p.Tag, _ = m["tag"].(string)
			p.Text, _ = m["text"].(string)
			p.Prompt, _ = m["prompt"].(string)
			p.Selector, _ = m["selector"].(string)
			res.Prompts = append(res.Prompts, p)
		}
	}
	if list, ok := doc["artifact_failures"].([]any); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			kind, _ := m["kind"].(string)
			detail, _ := m["detail"].(string)
			res.Failures = append(res.Failures, strings.TrimSpace(kind+": "+detail))
		}
	}
	return res, nil
}

// PanelData is what the gt panel sends as the prompt's data.
type PanelData struct {
	Decision string `json:"decision"`
	Answer   string `json:"answer"`
}

const contextMarker = "Context data:\n"

// Panel reads the panel decision out of a prompt. ok is false for prompts
// that do not come from the gt panel.
func (p Prompt) Panel() (PanelData, bool) {
	if p.Tag != PanelTag {
		return PanelData{}, false
	}
	i := strings.Index(p.Prompt, contextMarker)
	if i < 0 {
		return PanelData{}, false
	}
	var d PanelData
	if err := json.Unmarshal([]byte(p.Prompt[i+len(contextMarker):]), &d); err != nil {
		return PanelData{}, false
	}
	switch d.Decision {
	case DecisionApprove, DecisionChanges, DecisionAnswer:
		return d, true
	}
	return PanelData{}, false
}

var bodyClose = regexp.MustCompile(`(?i)</body\s*>`)

// InjectPanel adds the gt review panel before the last </body> of the page
// (at the end when the page has none).
func InjectPanel(page, kind string) string {
	panel := panelHTML(kind)
	locs := bodyClose.FindAllStringIndex(page, -1)
	if len(locs) == 0 {
		return page + "\n" + panel + "\n"
	}
	at := locs[len(locs)-1][0]
	return page[:at] + panel + "\n" + page[at:]
}

func panelHTML(kind string) string {
	buttons := `<button type="button" id="gt-review-approve">Approve</button>
<button type="button" id="gt-review-changes">Request changes</button>
<button type="button" id="gt-review-answer-send">Send answer</button>`
	label := "Answer or comments"
	if kind == KindQuestion {
		buttons = `<button type="button" id="gt-review-answer-send">Send answer</button>`
		label = "Your answer"
	}
	return `<!-- gt review panel -->
<div id="gt-review-panel" role="region" aria-label="Review" style="position:fixed;left:0;right:0;bottom:0;z-index:2147483000;display:flex;flex-wrap:wrap;gap:8px;align-items:center;padding:10px 16px;background:#1f2937;color:#f9fafb;font:14px system-ui,sans-serif;box-shadow:0 -2px 8px rgba(0,0,0,.3)">
<strong>Review (` + kind + `)</strong>
<label style="flex:1 1 280px;display:flex;gap:8px;align-items:center">` + label + `
<textarea id="gt-review-answer" rows="2" style="flex:1;min-width:160px;padding:6px;border-radius:4px;border:1px solid #6b7280;background:#111827;color:#f9fafb"></textarea></label>
` + buttons + `
<span id="gt-review-status" role="status" aria-live="polite"></span>
</div>
<script>
(function () {
  var statusEl = document.getElementById('gt-review-status');
  function say(t) { if (statusEl) statusEl.textContent = t; }
  function send(decision) {
    var box = document.getElementById('gt-review-answer');
    var answer = box ? box.value.trim() : '';
    if (decision === 'answer' && !answer) { say('Write an answer first.'); return; }
    if (!window.lavish || !window.lavish.queuePrompt || !window.lavish.sendQueuedPrompts) {
      say('Open this page through its review link.');
      return;
    }
    var label = decision === 'approve' ? 'Approved' : decision === 'changes' ? 'Changes requested' : 'Answer';
    window.lavish.queuePrompt(label + (answer ? ': ' + answer : ''), {
      tag: 'gt-review',
      text: label,
      queueKey: 'gt-review',
      element: document.getElementById('gt-review-panel'),
      data: { decision: decision, answer: answer }
    });
    window.lavish.sendQueuedPrompts();
    say('Sent.');
  }
  var a = document.getElementById('gt-review-approve');
  var c = document.getElementById('gt-review-changes');
  var s = document.getElementById('gt-review-answer-send');
  if (a) a.onclick = function () { send('approve'); };
  if (c) c.onclick = function () { send('changes'); };
  if (s) s.onclick = function () { send('answer'); };
})();
</script>`
}

// CopyPage copies an HTML page and its sibling files (the page's directory)
// into dst, refusing when they are larger than MaxAssetBytes. It returns the
// path of the page inside dst. Symbolic links are skipped. The caller injects
// the panel into the copy afterwards.
func CopyPage(src, dst string) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() || !strings.HasSuffix(strings.ToLower(abs), ".html") {
		return "", fmt.Errorf("%s is not an .html file", src)
	}
	root := filepath.Dir(abs)
	var total int64
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if (d.Name() == ".git" && p != root) || p == dst {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		total += fi.Size()
		if total > MaxAssetBytes {
			return fmt.Errorf("the page and the files next to it are larger than %d MB; put the page in a folder with only what it needs", MaxAssetBytes>>20)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if (d.Name() == ".git" && p != root) || p == dst {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
	if err != nil {
		return "", err
	}
	return filepath.Join(dst, filepath.Base(abs)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // G304: walked from the page's own directory
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // G304: inside the store
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
