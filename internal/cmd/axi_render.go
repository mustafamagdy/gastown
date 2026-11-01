package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/axiout"
	"github.com/steveyegge/gastown/internal/estop"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/review"
)

// AXI output for the read commands agents run most (see internal/axiout).
// Each renderer takes the data structure its command already builds and
// returns text; none of them reads rendered text, touches styles or does I/O.
// Hints are chosen from the state, at most three, and only name commands that
// exist.

// maxAxiHelp caps the help block.
const maxAxiHelp = 3

func axiHelp(w *axiout.Writer, cmds []string) {
	if len(cmds) > maxAxiHelp {
		cmds = cmds[:maxAxiHelp]
	}
	w.Help(cmds...)
}

// axiDur formats a duration the way gt mq list does: 5s, 12m, 3h, 2d.
func axiDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func axiUpDown(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

func axiOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// axiHome shortens a path under the home directory to ~/.
func axiHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// axiEstopText describes any active emergency stop in one line, empty when
// there is none.
func axiEstopText(townRoot string) string {
	var parts []string
	if estop.IsActive(townRoot) {
		if info := estop.Read(townRoot); info != nil {
			parts = append(parts, "town ("+info.Trigger+")")
		}
	}
	entries, _ := filepath.Glob(filepath.Join(townRoot, "ESTOP.*"))
	for _, entry := range entries {
		rigName := strings.TrimPrefix(filepath.Base(entry), "ESTOP.")
		if info := estop.ReadRig(townRoot, rigName); info != nil {
			parts = append(parts, "rig "+rigName+" ("+info.Trigger+")")
		}
	}
	return strings.Join(parts, " ")
}

// renderStatusAxi renders gt status. estop is the text of an active
// emergency stop, empty when none.
func renderStatusAxi(st TownStatus, estop string) string {
	w := axiout.New()
	w.KV("town", st.Name)
	w.KV("location", axiHome(st.Location))
	if estop != "" {
		w.KV("estop", estop)
	}

	unread := 0
	if st.Overseer != nil {
		unread += st.Overseer.UnreadMail
	}
	doltDown := false
	if st.Daemon != nil {
		w.KV("daemon", axiUpDown(st.Daemon.Running))
	}
	if st.Dolt != nil {
		state := axiUpDown(st.Dolt.Running || st.Dolt.Remote)
		if st.Dolt.PortConflict {
			state = "down (port taken by another town)"
		}
		w.KV("dolt", state)
		doltDown = !st.Dolt.Running && !st.Dolt.Remote
	}
	if st.Tmux != nil {
		if st.Tmux.Running {
			w.KV("tmux", fmt.Sprintf("up (%d sessions)", st.Tmux.SessionCount))
		} else {
			w.KV("tmux", "down")
		}
	}
	if st.DND != nil && st.DND.Enabled {
		w.KV("dnd", "on")
	}

	var agents [][]any
	down := 0
	addAgent := func(a AgentRuntime) {
		unread += a.UnreadMail
		work := a.HookBead
		if work == "" && a.HasWork {
			work = "yes"
		}
		// Idle agents that are down with nothing hooked or unread are
		// counted, not listed.
		if !a.Running && work == "" && a.UnreadMail == 0 {
			down++
			return
		}
		agents = append(agents, []any{a.Address, axiUpDown(a.Running), axiOrNone(work), a.UnreadMail})
	}
	for _, a := range st.Agents {
		addAgent(a)
	}
	var rigs [][]any
	pendingRig := ""
	for _, r := range st.Rigs {
		for _, a := range r.Agents {
			addAgent(a)
		}
		pending, blocked := 0, 0
		if r.MQ != nil {
			pending, blocked = r.MQ.Pending, r.MQ.Blocked
			if pending > 0 && pendingRig == "" {
				pendingRig = r.Name
			}
		}
		rigs = append(rigs, []any{r.Name, r.PolecatCount, r.CrewCount, pending, blocked})
	}
	w.Table("agents", []string{"agent", "state", "work", "unread"}, agents, 0)
	w.KV("agents_idle_down", down)
	w.Table("rigs", []string{"rig", "polecats", "crew", "mq_pending", "mq_blocked"}, rigs, 0)
	w.KV("hooked", st.Summary.ActiveHooks)
	w.KV("unread", unread)

	var help []string
	if doltDown {
		help = append(help, "gt dolt status")
	}
	if unread > 0 {
		help = append(help, "gt mail inbox")
	}
	if pendingRig != "" {
		help = append(help, "gt mq list "+pendingRig)
	}
	if len(st.Rigs) == 0 {
		help = append(help, "gt rig add <name> <git-url>")
	}
	if len(help) < maxAxiHelp {
		help = append(help, "gt hook")
	}
	axiHelp(w, help)
	return w.String()
}

// renderHookAxi renders gt hook (the hook status view).
func renderHookAxi(st MoleculeStatusInfo) string {
	w := axiout.New()
	w.KV("target", st.Target)
	if st.Role != "" {
		w.KV("role", st.Role)
	}
	if !st.HasWork || st.PinnedBead == nil {
		w.KV("hooked", "none")
		axiHelp(w, []string{"bd ready", "gt mail inbox"})
		return w.String()
	}
	b := st.PinnedBead
	w.KV("hooked", b.ID)
	w.KV("title", axiout.Truncate(b.Title, axiout.MaxText))
	w.KV("status", b.Status)
	w.KV("priority", fmt.Sprintf("P%d", b.Priority))
	if st.AttachedMolecule != "" {
		w.KV("molecule", st.AttachedMolecule)
	}
	if st.AttachedFormula != "" {
		w.KV("formula", st.AttachedFormula)
	}
	if p := st.Progress; p != nil {
		w.KV("steps", fmt.Sprintf("%d of %d done", p.DoneSteps, p.TotalSteps))
		if len(p.ReadySteps) > 0 {
			w.KV("ready_steps", strings.Join(p.ReadySteps, " "))
		}
	}

	var help []string
	switch {
	case st.AttachedMolecule == "" && st.AttachedFormula == "":
		help = []string{"bd show " + b.ID, "gt mol attach " + b.ID + " <molecule-id>"}
	case st.Progress != nil && st.Progress.Complete:
		help = []string{"gt done"}
	default:
		help = []string{"bd mol current " + b.ID, "bd show " + b.ID}
	}
	axiHelp(w, help)
	return w.String()
}

// axiMQRow is one merge request in gt mq list.
type axiMQRow struct {
	ID       string
	Priority int
	Status   string // ready, active, blocked, closed or the raw status
	Branch   string
	Target   string
	Age      string
	WaitsOn  string // first unresolved blocker, when blocked
	Git      string // OK, MISSING or ERR; empty unless --verify
}

// renderMQListAxi renders gt mq list for one rig.
func renderMQListAxi(rig string, rows []axiMQRow, verify bool) string {
	w := axiout.New()
	w.KV("rig", rig)
	cols := []string{"id", "pri", "status", "branch", "target", "age"}
	if verify {
		cols = append(cols, "git")
	}
	var data [][]any
	ready, missing := 0, 0
	blocker := ""
	for _, r := range rows {
		status := r.Status
		if r.WaitsOn != "" {
			status += " by " + r.WaitsOn
			if blocker == "" {
				blocker = r.WaitsOn
			}
		}
		if r.Status == "ready" {
			ready++
		}
		if r.Git == "MISSING" {
			missing++
		}
		row := []any{r.ID, fmt.Sprintf("P%d", r.Priority), status, r.Branch, axiOrNone(r.Target), r.Age}
		if verify {
			row = append(row, r.Git)
		}
		data = append(data, row)
	}
	w.Table("merge_requests", cols, data, 0)

	var help []string
	switch {
	case len(rows) == 0:
		help = []string{"gt mq list " + rig + " --status in_progress", "gt refinery status " + rig}
	default:
		if blocker != "" {
			help = append(help, "bd show "+blocker)
		}
		if missing > 0 {
			help = append(help, fmt.Sprintf("%d MR(s) lost their branch: gt mq list %s --json", missing, rig))
		}
		if ready > 0 {
			help = append(help, "gt refinery status "+rig)
		}
	}
	axiHelp(w, help)
	return w.String()
}

// renderPolecatListAxi renders gt polecat list. rigArg is the rig asked for,
// empty with --all.
func renderPolecatListAxi(rigArg string, items []PolecatListItem) string {
	w := axiout.New()
	hasIssue := false
	for _, p := range items {
		if p.Issue != "" {
			hasIssue = true
		}
	}
	cols := []string{"polecat", "state", "session"}
	if hasIssue {
		cols = append(cols, "issue")
	}
	cols = append(cols, "note")
	var rows [][]any
	needsRecovery, zombies := "", ""
	for _, p := range items {
		name := p.Rig + "/" + p.Name
		note := p.ReuseStatus
		if p.Zombie {
			note = "no worktree"
			if zombies == "" {
				zombies = name
			}
		}
		if p.NeedsRecovery && needsRecovery == "" {
			needsRecovery = name
		}
		row := []any{name, string(p.State), axiUpDown(p.SessionRunning)}
		if hasIssue {
			row = append(row, p.Issue)
		}
		rows = append(rows, append(row, axiout.Truncate(note, axiout.MaxText)))
	}
	w.Table("polecats", cols, rows, 0)

	var help []string
	switch {
	case len(items) == 0:
		target := rigArg
		if target == "" {
			target = "<rig>"
		}
		help = []string{"bd ready", "gt sling <bead> " + target}
	default:
		if needsRecovery != "" {
			help = append(help, "gt polecat status "+needsRecovery)
		}
		if zombies != "" {
			help = append(help, "gt polecat status "+zombies)
		}
		if len(help) == 0 {
			help = append(help, "gt polecat status "+items[0].Rig+"/"+items[0].Name)
		}
	}
	axiHelp(w, help)
	return w.String()
}

// renderFeatureStatusAxi renders gt project feature status.
func renderFeatureStatusAxi(out featureStatusOutput) string {
	w := axiout.New()
	w.KV("feature", out.ID)
	w.KV("title", axiout.Truncate(out.Title, axiout.MaxText))
	w.KV("project", out.Project)
	w.KV("state", out.State)
	w.KV("branch", out.Branch)
	if out.VisualReview != "" {
		w.KV("visual_review", out.VisualReview)
	}

	var rigNames []string
	for name := range out.Rigs {
		rigNames = append(rigNames, name)
	}
	sort.Strings(rigNames)
	rigCols := []string{"rig", "state", "base"}
	withDetail := false
	for _, name := range rigNames {
		rs := out.Rigs[name]
		if rs.PRURL != "" || rs.Error != "" {
			withDetail = true
		}
	}
	if withDetail {
		rigCols = append(rigCols, "detail")
	}
	var rigs [][]any
	for _, name := range rigNames {
		rs := out.Rigs[name]
		row := []any{name, rs.State, rs.Base}
		if withDetail {
			detail := rs.PRURL
			if rs.Error != "" {
				detail = axiout.Truncate(rs.Error, axiout.MaxText)
			}
			row = append(row, detail)
		}
		rigs = append(rigs, row)
	}
	w.Table("rigs", rigCols, rigs, 0)

	// Task state is derived from the bead status; the bead status itself is
	// only worth a column when it says something the state does not.
	taskCols := []string{"id", "rig", "state", "title"}
	withWaits := false
	for _, v := range out.Tasks {
		if len(v.BlockedBy) > 0 {
			withWaits = true
		}
	}
	if withWaits {
		taskCols = append(taskCols, "waits_for")
	}
	var tasks [][]any
	for _, v := range out.Tasks {
		row := []any{v.ID, v.Rig, v.State, axiout.Truncate(v.Title, axiout.MaxText)}
		if withWaits {
			row = append(row, strings.Join(v.BlockedBy, " "))
		}
		tasks = append(tasks, row)
	}
	w.Table("tasks", taskCols, tasks, 0)
	w.KV("tasks_ready", out.Counts[projectfeature.TaskReady])
	w.KV("tasks_running", out.Counts[projectfeature.TaskRunning])
	w.KV("tasks_merging", out.Counts[projectfeature.TaskMerging])
	w.KV("tasks_blocked", out.Counts[projectfeature.TaskBlocked])
	w.KV("tasks_done", out.Counts[projectfeature.TaskDone])
	var blocking []string
	for _, b := range out.Blocking {
		blocking = append(blocking, axiout.Truncate(b, 160))
	}
	w.List("blocking", blocking)

	var help []string
	switch out.State {
	case projectfeature.StateHeld:
		help = append(help, "gt project feature land "+out.ID)
	case projectfeature.StateOpen:
		if len(out.Ready) > 0 {
			help = append(help, "gt project feature run "+out.ID)
		}
		if len(out.Tasks) > 0 && out.Counts[projectfeature.TaskDone] == len(out.Tasks) {
			help = append(help, "gt project feature land "+out.ID)
		}
	}
	if out.VisualReview != "" && out.VisualReview != "approved" {
		help = append(help, "gt review list")
	}
	axiHelp(w, help)
	return w.String()
}

// renderReviewListAxi renders gt review list. now is passed in so tests are
// exact.
func renderReviewListAxi(recs []*review.Record, now time.Time) string {
	w := axiout.New()
	var rows [][]any
	firstOpen := ""
	for _, r := range recs {
		if r.State == review.StateOpen && firstOpen == "" {
			firstOpen = r.ID
		}
		rows = append(rows, []any{r.ID, r.Bead, r.Kind, r.State, len(r.Feedback), axiDur(now.Sub(r.CreatedAt))})
	}
	w.Table("reviews", []string{"id", "bead", "kind", "state", "feedback", "age"}, rows, 0)

	var help []string
	switch {
	case len(recs) == 0:
		help = []string{"gt review request <page.html>"}
	case firstOpen != "":
		help = []string{"gt review show " + firstOpen}
	default:
		help = []string{"gt review show " + recs[0].ID}
	}
	axiHelp(w, help)
	return w.String()
}
