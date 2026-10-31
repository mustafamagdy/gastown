package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// Bead labels that mirror the review state.
const (
	LabelPending  = "review:pending"
	LabelApproved = "review:approved"
	LabelRejected = "review:rejected"
)

// Mirror shows review state on the bead. The record stays the source of truth.
type Mirror interface {
	// Set adds one label (if not empty), removes others, and adds a comment
	// (if not empty).
	Set(bead, addLabel string, removeLabels []string, comment string) error
}

// BeadMirror writes to the bead, resolving which database it lives in (rig,
// project or town) by its prefix through the town's routes.
type BeadMirror struct{ TownRoot string }

// Set implements Mirror.
func (m BeadMirror) Set(bead, addLabel string, removeLabels []string, comment string) error {
	b := beads.New(m.TownRoot)
	opts := beads.UpdateOptions{RemoveLabels: removeLabels}
	if addLabel != "" {
		opts.AddLabels = []string{addLabel}
	}
	var errs []error
	if addLabel != "" || len(removeLabels) > 0 {
		if err := b.Update(bead, opts); err != nil {
			errs = append(errs, fmt.Errorf("labels: %w", err))
		}
	}
	if comment != "" {
		if err := b.AddComment(bead, comment); err != nil {
			errs = append(errs, fmt.Errorf("comment: %w", err))
		}
	}
	return errors.Join(errs...)
}

// labelsFor returns the label to add and the labels to remove for a state.
func labelsFor(state string) (add string, remove []string) {
	all := []string{LabelPending, LabelApproved, LabelRejected}
	switch state {
	case StateOpen, StateDisconnected:
		add = LabelPending
	case StateApproved:
		add = LabelApproved
	case StateRejected:
		add = LabelRejected
	}
	for _, l := range all {
		if l != add {
			remove = append(remove, l)
		}
	}
	return add, remove
}

// SetBeadState mirrors a state change to the bead.
func SetBeadState(m Mirror, rec *Record, comment string) error {
	add, remove := labelsFor(rec.State)
	return m.Set(rec.Bead, add, remove, comment)
}

// Notifier tells the requester about an outcome: a nudge when their session
// is alive, otherwise mail to the mayor (mail survives the session).
type Notifier struct {
	TownRoot string
	GtBin    string                    // defaults to "gt"
	Alive    func(address string) bool // reports whether the agent's session is up
}

// Notify delivers message about rec. subject is used for mail only.
func (n Notifier) Notify(rec *Record, subject, message string) error {
	gt := n.GtBin
	if gt == "" {
		gt = "gt"
	}
	var cmd *exec.Cmd
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if rec.Requester != "" && n.Alive != nil && n.Alive(rec.Requester) {
		cmd = exec.CommandContext(ctx, gt, "nudge", rec.Requester, message, "--mode", "queue") //nolint:gosec // G204: fixed binary, explicit args, no shell
	} else {
		body := message
		if rec.Requester != "" {
			body += "\n\n(The agent that asked, " + rec.Requester + ", has no live session.)"
		}
		cmd = exec.CommandContext(ctx, gt, "mail", "send", "mayor/", "-s", subject, "-m", body) //nolint:gosec // G204: fixed binary, explicit args, no shell
	}
	cmd.Dir = n.TownRoot
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", gt, cmd.Args[1], err, firstLines(strings.TrimSpace(out.String()), 3))
	}
	return nil
}

// Engine keeps one `lavish-axi poll` per open review and acts on what the
// human sends. It is deterministic: no model is involved.
type Engine struct {
	TownRoot string
	Client   *Client
	Mirror   Mirror
	// Notify tells the requester (or the mayor). Nil disables it.
	Notify func(rec *Record, subject, message string) error
	Logf   func(format string, args ...any)

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewEngine returns an engine for a town.
func NewEngine(townRoot string, client *Client, mirror Mirror, notify func(*Record, string, string) error, logf func(string, ...any)) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{TownRoot: townRoot, Client: client, Mirror: mirror, Notify: notify, Logf: logf,
		running: map[string]bool{}, ctx: ctx, cancel: cancel}
}

// Tick makes sure every open review has a poll running. It does nothing, and
// reads nothing but the record directory, when no review is open.
func (e *Engine) Tick() {
	recs, errs := List(e.TownRoot)
	for _, err := range errs {
		e.Logf("review: %v", err)
	}
	for _, rec := range recs {
		if !rec.Watching() {
			continue
		}
		e.mu.Lock()
		if e.running[rec.ID] || e.ctx.Err() != nil {
			e.mu.Unlock()
			continue
		}
		e.running[rec.ID] = true
		e.wg.Add(1)
		e.mu.Unlock()
		go e.watch(rec.ID)
	}
}

// Stop ends the polls and waits for them. Feedback not yet delivered stays
// queued in lavish for the next poll.
func (e *Engine) Stop() {
	e.cancel()
	e.wg.Wait()
}

// Running returns the ids with a poll running (for tests and status).
func (e *Engine) Running() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for id := range e.running {
		ids = append(ids, id)
	}
	return ids
}

func (e *Engine) watch(id string) {
	defer func() {
		e.mu.Lock()
		delete(e.running, id)
		e.mu.Unlock()
		e.wg.Done()
	}()
	for e.ctx.Err() == nil {
		rec, err := Load(e.TownRoot, id)
		if err != nil {
			e.Logf("review %s: %v", id, err)
			return
		}
		if !rec.Watching() {
			return
		}
		res, err := e.pollOnce(rec)
		if err != nil {
			if e.ctx.Err() != nil {
				return
			}
			e.Logf("review %s: poll failed: %v", id, err)
			_, _ = Update(e.TownRoot, id, func(r *Record) error { r.Error = err.Error(); return nil })
			return // the next tick tries again
		}
		e.Handle(id, res)
	}
}

// pollOnce runs one poll to completion. On LISTENER_ACTIVE (a poll from an
// earlier daemon is still there) it takes over once. When lavish lost the
// session it opens it again and polls once more.
func (e *Engine) pollOnce(rec *Record) (*PollResult, error) {
	takeover, reopened := false, false
	for {
		cmd := e.Client.PollCmd(e.ctx, rec.HTML, takeover)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		all := stdout.String() + "\n" + stderr.String()
		if e.ctx.Err() != nil {
			return nil, e.ctx.Err()
		}
		switch {
		case err == nil:
			return ParsePoll(stdout.String())
		case strings.Contains(all, "LISTENER_ACTIVE") && !takeover:
			takeover = true
		case strings.Contains(all, "NOT_FOUND") && !reopened:
			reopened = true
			if _, oerr := e.Client.Open(rec.HTML, false); oerr != nil {
				return nil, fmt.Errorf("session was lost and could not be reopened: %w", oerr)
			}
		default:
			return nil, fmt.Errorf("%w: %s", err, firstLines(strings.TrimSpace(stderr.String()+" "+stdout.String()), 3))
		}
	}
}

// Handle applies one parsed poll result to the record, the bead and the
// requester. It is exported so tests can drive it without a poll.
func (e *Engine) Handle(id string, res *PollResult) {
	if cur, err := Load(e.TownRoot, id); err == nil && cur.State == StateEnded {
		return // cancelled; the poll only reported the end
	}
	var decision *PanelData
	var fresh []Feedback
	now := time.Now().UTC()
	for _, p := range res.Prompts {
		fb := Feedback{At: now, Tag: p.Tag, Selector: p.Selector, Text: strings.TrimSpace(p.Prompt)}
		if fb.Text == "" {
			fb.Text = p.Text
		}
		if d, ok := p.Panel(); ok {
			dd := d
			decision = &dd
			fb.Decision = d.Decision
			fb.Text = strings.TrimSpace(d.Answer)
			if fb.Text == "" {
				fb.Text = p.Text
			}
		}
		fresh = append(fresh, fb)
	}
	for _, f := range res.Failures {
		fresh = append(fresh, Feedback{At: now, Tag: "artifact_failure", Text: f})
	}

	var outcome, reply string
	rec, err := Update(e.TownRoot, id, func(r *Record) error {
		r.Feedback = append(r.Feedback, fresh...)
		r.Error = strings.Join(res.Failures, "; ")
		switch {
		case res.Status == "browser_disconnected":
			r.State = StateDisconnected
			outcome = "the reviewer's window closed; resume with 'gt review reopen " + r.ID + "'"
		case decision != nil && decision.Decision == DecisionApprove:
			r.State = StateApproved
			outcome, reply = "APPROVED", "Approved. The agent has been told."
		case decision != nil && decision.Decision == DecisionChanges:
			r.State = StateRejected
			outcome, reply = "CHANGES REQUESTED", "Changes requested. The agent has been told."
		case decision != nil && decision.Decision == DecisionAnswer && r.Kind == KindQuestion:
			r.State = StateAnswered
			outcome, reply = "ANSWERED", "Answer received. The agent has been told."
		case res.SessionEnded || res.Status == "ended":
			r.State = StateEnded
			outcome = "the session ended without a decision"
		case len(res.Failures) > 0:
			outcome, reply = "the page failed to render", "Failure reported to the agent."
		default:
			outcome, reply = "feedback", "Feedback received. The agent has been told."
		}
		if r.Decided() && r.DecidedAt == nil {
			t := now
			r.DecidedAt = &t
		}
		return nil
	})
	if err != nil {
		e.Logf("review %s: saving result: %v", id, err)
		return
	}

	summary := feedbackSummary(fresh)
	if e.Mirror != nil {
		comment := fmt.Sprintf("Review %s (%s): %s.", rec.ID, rec.Kind, outcome)
		if summary != "" {
			comment += "\n\n" + clip(summary, 4000)
		}
		if err := SetBeadState(e.Mirror, rec, comment); err != nil {
			e.Logf("review %s: mirroring to %s: %v", id, rec.Bead, err)
		}
	}
	if reply != "" && !res.SessionEnded && res.Status != "ended" {
		if err := e.Client.Reply(rec.HTML, reply); err != nil {
			e.Logf("review %s: reply: %v", id, err)
		}
	}
	if e.Notify != nil && res.Status != "browser_disconnected" {
		msg := fmt.Sprintf("Review %s (%s) on %s: %s.", rec.ID, rec.Kind, rec.Bead, outcome)
		if summary != "" {
			msg += "\n" + clip(summary, 1500)
		}
		msg += "\nFull record: gt review show " + rec.ID
		subject := fmt.Sprintf("Review %s on %s: %s", rec.ID, rec.Bead, outcome)
		if err := e.Notify(rec, subject, msg); err != nil {
			e.Logf("review %s: notify: %v", id, err)
		}
	}
}

func feedbackSummary(fbs []Feedback) string {
	var lines []string
	for _, f := range fbs {
		who := f.Tag
		if f.Decision != "" {
			who = f.Decision
		}
		line := "- "
		if who != "" {
			line += who + ": "
		}
		if f.Selector != "" {
			line += "[" + f.Selector + "] "
		}
		line += strings.ReplaceAll(strings.TrimSpace(f.Text), "\n", " ")
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " ..."
}
