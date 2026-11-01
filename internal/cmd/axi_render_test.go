package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/review"
)

func checkAxi(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Error("output holds an ANSI escape")
	}
}

func TestRenderStatusAxi(t *testing.T) {
	st := TownStatus{
		Name:     "demo",
		Location: "/srv/demo",
		Daemon:   &ServiceInfo{Running: true, PID: 1},
		Dolt:     &DoltInfo{Running: false, Port: 3307},
		Tmux:     &TmuxInfo{Running: true, SessionCount: 2},
		Overseer: &OverseerInfo{Name: "me", UnreadMail: 2},
		Agents: []AgentRuntime{
			{Address: "mayor/", Running: true},
			{Address: "deacon/", Running: false},
		},
		Rigs: []RigStatus{{
			Name: "web", PolecatCount: 1, CrewCount: 0,
			Agents: []AgentRuntime{
				{Address: "web/witness", Running: false, UnreadMail: 1},
				{Address: "web/Toast", Running: true, HasWork: true, HookBead: "wb-1"},
				{Address: "web/refinery", Running: false},
			},
			MQ: &MQSummary{Pending: 3, Blocked: 1},
		}},
		Summary: StatusSum{ActiveHooks: 1},
	}
	want := "town: demo\nlocation: /srv/demo\ndaemon: up\ndolt: down\ntmux: up (2 sessions)\n" +
		"agents[3]{agent,state,work,unread}:\n  mayor/,up,none,0\n  web/witness,down,none,1\n  web/Toast,up,wb-1,0\n" +
		"agents_idle_down: 2\n" +
		"rigs[1]{rig,polecats,crew,mq_pending,mq_blocked}:\n  web,1,0,3,1\n" +
		"hooked: 1\nunread: 3\n" +
		"help[3]:\n  gt dolt status\n  gt mail inbox\n  gt mq list web\n"
	checkAxi(t, renderStatusAxi(st, ""), want)

	empty := TownStatus{Name: "t", Location: "/t"}
	checkAxi(t, renderStatusAxi(empty, "town (manual)"),
		"town: t\nlocation: /t\nestop: town (manual)\nagents: 0\nagents_idle_down: 0\nrigs: 0\nhooked: 0\nunread: 0\n"+
			"help[2]:\n  gt rig add <name> <git-url>\n  gt hook\n")
}

func TestRenderHookAxi(t *testing.T) {
	checkAxi(t, renderHookAxi(MoleculeStatusInfo{Target: "web/polecats/Toast", Role: "polecat"}),
		"target: web/polecats/Toast\nrole: polecat\nhooked: none\nhelp[2]:\n  bd ready\n  gt mail inbox\n")

	naked := MoleculeStatusInfo{
		Target: "web/polecats/Toast", Role: "polecat", HasWork: true,
		PinnedBead: &beads.Issue{ID: "wb-1", Title: "Fix the thing", Status: "hooked", Priority: 2},
	}
	checkAxi(t, renderHookAxi(naked),
		"target: web/polecats/Toast\nrole: polecat\nhooked: wb-1\ntitle: Fix the thing\nstatus: hooked\npriority: P2\n"+
			"help[2]:\n  bd show wb-1\n  gt mol attach wb-1 <molecule-id>\n")

	working := naked
	working.AttachedMolecule = "mol-9"
	working.Progress = &MoleculeProgressInfo{TotalSteps: 5, DoneSteps: 2, ReadySteps: []string{"s3", "s4"}}
	checkAxi(t, renderHookAxi(working),
		"target: web/polecats/Toast\nrole: polecat\nhooked: wb-1\ntitle: Fix the thing\nstatus: hooked\npriority: P2\n"+
			"molecule: mol-9\nsteps: 2 of 5 done\nready_steps: s3 s4\nhelp[2]:\n  bd mol current wb-1\n  bd show wb-1\n")

	working.Progress.Complete = true
	if !strings.HasSuffix(renderHookAxi(working), "help[1]:\n  gt done\n") {
		t.Error("a complete molecule should hint gt done")
	}
}

func TestRenderMQListAxi(t *testing.T) {
	checkAxi(t, renderMQListAxi("web", nil, false),
		"rig: web\nmerge_requests: 0\nhelp[2]:\n  gt mq list web --status in_progress\n  gt refinery status web\n")

	rows := []axiMQRow{
		{ID: "wb-mr1", Priority: 1, Status: "ready", Branch: "polecat/a", Target: "main", Age: "5m", Git: "OK"},
		{ID: "wb-mr2", Priority: 2, Status: "blocked", WaitsOn: "wb-mr1", Branch: "polecat/b", Age: "2h", Git: "MISSING"},
	}
	checkAxi(t, renderMQListAxi("web", rows, true),
		"rig: web\nmerge_requests[2]{id,pri,status,branch,target,age,git}:\n"+
			"  wb-mr1,P1,ready,polecat/a,main,5m,OK\n  wb-mr2,P2,blocked by wb-mr1,polecat/b,none,2h,MISSING\n"+
			"help[3]:\n  bd show wb-mr1\n  1 MR(s) lost their branch: gt mq list web --json\n  gt refinery status web\n")
}

func TestRenderMQListAxiRowCap(t *testing.T) {
	var rows []axiMQRow
	for i := 0; i < 25; i++ {
		rows = append(rows, axiMQRow{ID: "m", Status: "active", Age: "1m"})
	}
	out := renderMQListAxi("web", rows, false)
	if !strings.Contains(out, "merge_requests[20]{") || !strings.Contains(out, "showing: 20 of 25\n") {
		t.Errorf("row cap not applied:\n%s", out)
	}
}

func TestRenderPolecatListAxi(t *testing.T) {
	checkAxi(t, renderPolecatListAxi("web", nil),
		"polecats: 0\nhelp[2]:\n  bd ready\n  gt sling <bead> web\n")
	checkAxi(t, renderPolecatListAxi("", nil),
		"polecats: 0\nhelp[2]:\n  bd ready\n  gt sling <bead> <rig>\n")

	items := []PolecatListItem{
		{Rig: "web", Name: "Toast", State: polecat.StateWorking, Issue: "wb-1", SessionRunning: true},
		{Rig: "web", Name: "Jam", State: polecat.StateIdle, ReuseStatus: "idle-recovery-needed", NeedsRecovery: true},
		{Rig: "web", Name: "Ghost", State: polecat.StateZombie, SessionRunning: true, Zombie: true},
	}
	checkAxi(t, renderPolecatListAxi("web", items),
		"polecats[3]{polecat,state,session,issue,note}:\n"+
			"  web/Toast,working,up,wb-1,\"\"\n  web/Jam,idle,down,\"\",idle-recovery-needed\n  web/Ghost,zombie,up,\"\",no worktree\n"+
			"help[2]:\n  gt polecat status web/Jam\n  gt polecat status web/Ghost\n")
}

func TestRenderFeatureStatusAxi(t *testing.T) {
	out := featureStatusOutput{
		ID: "sh-1", Project: "shop", Title: "Credits", Branch: "feat/sh-1-credits", State: projectfeature.StateHeld,
		Rigs: map[string]projectfeature.RigState{
			"contracts": {State: projectfeature.RigLanded, Base: "main"},
			"api":       {State: projectfeature.RigHeld, Base: "main", Error: "merge conflict"},
		},
		Tasks: []projectfeature.TaskView{
			{Task: projectfeature.Task{ID: "ct-1", Rig: "contracts", Title: "Schema"}, State: projectfeature.TaskDone},
			{Task: projectfeature.Task{ID: "ap-1", Rig: "api", Title: "Use it"}, State: projectfeature.TaskBlocked, BlockedBy: []string{"ct-1"}},
		},
		Counts:   map[string]int{projectfeature.TaskDone: 1, projectfeature.TaskBlocked: 1},
		Blocking: []string{"rig api held: merge conflict"},
	}
	checkAxi(t, renderFeatureStatusAxi(out),
		"feature: sh-1\ntitle: Credits\nproject: shop\nstate: held\nbranch: feat/sh-1-credits\n"+
			"rigs[2]{rig,state,base,detail}:\n  api,held,main,merge conflict\n  contracts,landed,main,\"\"\n"+
			"tasks[2]{id,rig,state,title,waits_for}:\n  ct-1,contracts,done,Schema,\"\"\n  ap-1,api,blocked,Use it,ct-1\n"+
			"tasks_ready: 0\ntasks_running: 0\ntasks_merging: 0\ntasks_blocked: 1\ntasks_done: 1\n"+
			"blocking[1]:\n  \"rig api held: merge conflict\"\n"+
			"help[1]:\n  gt project feature land sh-1\n")

	open := featureStatusOutput{
		ID: "sh-2", Project: "shop", Title: "T", Branch: "b", State: projectfeature.StateOpen,
		Rigs:   map[string]projectfeature.RigState{},
		Ready:  []string{"x-1"},
		Counts: map[string]int{projectfeature.TaskReady: 1},
	}
	got := renderFeatureStatusAxi(open)
	if !strings.Contains(got, "rigs: 0\ntasks: 0\n") || !strings.HasSuffix(got, "help[1]:\n  gt project feature run sh-2\n") {
		t.Errorf("open feature with a ready task:\n%s", got)
	}
}

func TestRenderReviewListAxi(t *testing.T) {
	checkAxi(t, renderReviewListAxi(nil, time.Now()),
		"reviews: 0\nhelp[1]:\n  gt review request <page.html>\n")

	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	recs := []*review.Record{
		{ID: "rv-1", Bead: "hq-1", Kind: "approval", State: review.StateApproved, CreatedAt: now.Add(-26 * time.Hour)},
		{ID: "rv-2", Bead: "hq-2", Kind: "question", State: review.StateOpen, CreatedAt: now.Add(-5 * time.Minute),
			Feedback: []review.Feedback{{Text: "hm"}}},
	}
	checkAxi(t, renderReviewListAxi(recs, now),
		"reviews[2]{id,bead,kind,state,feedback,age}:\n  rv-1,hq-1,approval,approved,0,1d\n  rv-2,hq-2,question,open,1,5m\n"+
			"help[1]:\n  gt review show rv-2\n")
}
