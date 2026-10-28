//go:build !windows

package herdrmux

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mkTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	for _, d := range []string{"demo/.repo.git", "other", "plain"} {
		if err := os.MkdirAll(filepath.Join(town, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return town
}

func TestClassify(t *testing.T) {
	town := mkTown(t)
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")
	cases := []struct {
		name    string
		session string
		cwd     string
		env     map[string]string
		command string
		want    Placement
	}{
		{"mayor", "hq-mayor", "/x", map[string]string{"GT_ROLE": "mayor", "GT_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: town, Label: "mayor", Role: "mayor"}},
		{"deacon no env", "hq-deacon", "/x", nil, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: "/x", Label: "deacon"}},
		{"boot", "hq-boot", "/x", map[string]string{"GT_ROLE": "deacon/boot", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: town, Label: "boot", Role: "boot"}},
		{"dog", "hq-dog-alpha", "/x", map[string]string{"GT_ROLE": "dog", "GT_DOG_NAME": "alpha"}, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: "/x", Label: "dog-alpha", Role: "dog"}},
		{"witness uses .repo.git", "to-witness", "/x/witness", map[string]string{"GT_ROLE": "demo/witness", "GT_RIG": "demo", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "demo", GroupCwd: filepath.Join(town, "demo/.repo.git"), Label: "witness", Rig: "demo", Role: "witness"}},
		{"refinery from command export", "to-refinery", "/x", nil, "export GT_ROLE=demo/refinery GT_RIG=demo GT_ROOT='" + town + "' && exec claude",
			Placement{Kind: KindTab, Group: "demo", GroupCwd: filepath.Join(town, "demo/.repo.git"), Label: "refinery", Rig: "demo", Role: "refinery"}},
		{"mayor from env prefix", "hq-mayor", "/x", nil, "cd /w && env GT_ROLE=mayor BD_ACTOR=mayor claude",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: "/x", Label: "mayor", Role: "mayor"}},
		{"crew", "to-crew-max", "/x", map[string]string{"GT_ROLE": "demo/crew/max", "GT_RIG": "demo", "GT_CREW": "max", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "demo", GroupCwd: filepath.Join(town, "demo/.repo.git"), Label: "crew-max", Rig: "demo", Role: "crew"}},
		{"rig dir without .repo.git", "ot-witness", "/x", map[string]string{"GT_ROLE": "other/witness", "GT_RIG": "other", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "other", GroupCwd: filepath.Join(town, "other"), Label: "witness", Rig: "other", Role: "witness"}},
		{"rig dir missing", "zz-witness", "/x", map[string]string{"GT_RIG": "zz", "GT_ROLE": "witness", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindTab, Group: "zz", GroupCwd: "/x", Label: "witness", Rig: "zz", Role: "witness"}},
		{"polecat", "to-alpha", "/w/alpha", map[string]string{"GT_ROLE": "demo/polecats/alpha", "GT_RIG": "demo", "GT_POLECAT": "alpha", "GT_TOWN_ROOT": town}, "",
			Placement{Kind: KindWorktree, Group: "demo", GroupCwd: filepath.Join(town, "demo/.repo.git"), Label: "alpha", Rig: "demo", Role: "polecat"}},
		{"polecat label from GT_POLECAT only", "to-nux", "/w", map[string]string{"GT_POLECAT": "nux", "GT_RIG": "demo"}, "",
			Placement{Kind: KindWorktree, Group: "demo", GroupCwd: "/w", Label: "nux", Rig: "demo", Role: "polecat"}},
		{"name heuristic witness", "to-witness", "/x", nil, "",
			Placement{Kind: KindTab, Group: "to", GroupCwd: "/x", Label: "witness", Rig: "to", Role: "witness"}},
		{"name heuristic crew", "to-crew-max", "/x", nil, "",
			Placement{Kind: KindTab, Group: "to", GroupCwd: "/x", Label: "crew-max", Rig: "to", Role: "crew"}},
		{"unknown role with rig", "weird", "/x", map[string]string{"GT_RIG": "demo"}, "",
			Placement{Kind: KindTab, Group: "demo", GroupCwd: "/x", Label: "weird", Rig: "demo"}},
		{"no identity", "s", "/x", nil, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: "/x", Label: "s"}},
		{"town role beats stray GT_RIG", "hq-mayor", "/x", map[string]string{"GT_ROLE": "mayor", "GT_RIG": "demo"}, "",
			Placement{Kind: KindTab, Group: "hq", GroupCwd: "/x", Label: "mayor", Role: "mayor"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.session, c.cwd, c.env, c.command)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Classify(%q) =\n %+v\nwant\n %+v", c.session, got, c.want)
			}
		})
	}
}

func TestShortLabel(t *testing.T) {
	for _, c := range []struct{ name, rig, want string }{
		{"hq-mayor", "", "mayor"}, {"hq-dog-alpha", "", "dog-alpha"}, {"to-witness", "demo", "witness"},
		{"demo-alpha", "demo", "alpha"}, {"to-crew-max", "", "crew-max"}, {"plain", "", "plain"}, {"hq-", "", "hq-"},
	} {
		if got := shortLabel(c.name, c.rig); got != c.want {
			t.Errorf("shortLabel(%q,%q) = %q, want %q", c.name, c.rig, got, c.want)
		}
	}
}

func TestEnvFromCommand(t *testing.T) {
	got := envFromCommand(`export GT_ROLE="demo/witness" GT_RIG=demo; GT_CREW='max' exec claude`)
	want := map[string]string{"GT_ROLE": "demo/witness", "GT_RIG": "demo", "GT_CREW": "max"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

// --- behaviour against the fake herdr --------------------------------------

func mustRun(t *testing.T, run func(...string) (string, string, int), args ...string) string {
	t.Helper()
	o, e, code := run(args...)
	if code != 0 {
		t.Fatalf("%v: code=%d stderr=%q", args, code, e)
	}
	return o
}

func TestGroupReuseAndKillOnlyTab(t *testing.T) {
	f, run := newFake(t, "gt-p1")
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	// First session reuses the group's root shell tab.
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Fatalf("tabs after first = %v", got)
	}
	mustRun(t, run, "new-session", "-d", "-s", "hq-deacon", "-c", "/work", "-e", "GT_ROLE=deacon")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor", "deacon"}) {
		t.Fatalf("tabs after second = %v", got)
	}
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"hq"}) {
		t.Fatalf("workspaces = %v, want a single hq", got)
	}
	if calls := f.herdrCalls("tab create"); len(calls) != 1 || flagVal(calls[0], "--cwd") != "/work" {
		t.Errorf("tab create calls = %v", calls)
	}
	st1, _ := f.store.Load("hq-mayor")
	st2, _ := f.store.Load("hq-deacon")
	if st1.Kind != KindTab || st1.WorkspaceID != st2.WorkspaceID || st1.TabID == st2.TabID || st1.Label != "mayor" {
		t.Errorf("states = %+v %+v", st1, st2)
	}

	out := mustRun(t, run, "list-sessions", "-F", "#{session_name} #{session_id} #{window_name}")
	if out != "hq-deacon $2002 deacon\nhq-mayor $2001 mayor\n" {
		t.Errorf("list-sessions = %q", out)
	}
	// targets resolve by $id too
	if got := mustRun(t, run, "display-message", "-p", "-t", "$2002", "#{session_name}"); got != "hq-deacon\n" {
		t.Errorf("$id target = %q", got)
	}

	// Attached only when the tab AND workspace are focused.
	if got := mustRun(t, run, "display-message", "-p", "-t", "hq-deacon", "#{session_attached}"); got != "0\n" {
		t.Errorf("attached before = %q", got)
	}
	mustRun(t, run, "switch-client", "-t", "hq-deacon")
	if got := mustRun(t, run, "list-sessions", "-F", "#{session_name}=#{session_attached}"); got != "hq-deacon=1\nhq-mayor=0\n" {
		t.Errorf("attached after = %q", got)
	}
	if len(f.herdrCalls("tab focus "+st2.TabID)) != 1 {
		t.Error("switch-client should focus the tab")
	}

	// kill-session closes only that tab.
	mustRun(t, run, "kill-session", "-t", "=hq-deacon")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Errorf("tabs after kill = %v", got)
	}
	if len(f.herdrCalls("workspace close")) != 0 {
		t.Error("kill-session on a tab must not close the workspace")
	}
	if _, _, code := run("has-session", "-t", "=hq-mayor"); code != 0 {
		t.Error("other session lost")
	}
	if _, err := f.store.Load("hq-deacon"); err == nil {
		t.Error("state not deleted")
	}

	// rename renames the tab
	mustRun(t, run, "rename-session", "-t", "hq-mayor", "hq-boss")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"boss"}) {
		t.Errorf("tab after rename = %v", got)
	}
	if got := mustRun(t, run, "display-message", "-p", "-t", "hq-boss", "#{window_name}"); got != "boss\n" {
		t.Errorf("window_name = %q", got)
	}

	// Closing the last tab makes herdr drop the group; a new session recreates it.
	mustRun(t, run, "kill-session", "-t", "=hq-boss")
	if len(f.wsLabels()) != 0 {
		t.Errorf("group should be gone: %v", f.wsLabels())
	}
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Errorf("recreated group tabs = %v", got)
	}
}

func TestVanishedGroupIsRecreated(t *testing.T) {
	f, run := newFake(t, "gt-p2")
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor")
	mustRun(t, run, "new-session", "-d", "-s", "hq-deacon")
	f.workspace = f.workspace[:1] // the user closed the whole hq workspace
	if _, _, code := run("has-session", "-t", "=hq-mayor"); code == 0 {
		t.Error("session should be gone")
	}
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor")
	mustRun(t, run, "new-session", "-d", "-s", "hq-deacon")
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"hq"}) {
		t.Errorf("workspaces = %v", got)
	}
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"mayor", "deacon"}) {
		t.Errorf("tabs = %v", got)
	}
}

func TestRigGroupsAndPolecatWorktree(t *testing.T) {
	f, run := newFake(t, "gt-p3")
	town := mkTown(t)
	f.worktrees["/w/alpha"] = true
	rigEnv := []string{"-e", "GT_RIG=demo", "-e", "GT_TOWN_ROOT=" + town}

	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-witness", "-e", "GT_ROLE=demo/witness"}, rigEnv...)...)
	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-refinery", "-e", "GT_ROLE=demo/refinery"}, rigEnv...)...)
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"demo", "hq"}) {
		t.Fatalf("workspaces = %v", got)
	}
	if got := f.tabLabels("demo"); !reflect.DeepEqual(got, []string{"witness", "refinery"}) {
		t.Errorf("rig tabs = %v", got)
	}
	creates := f.herdrCalls("workspace create")
	if len(creates) != 2 || flagVal(creates[0], "--cwd") != filepath.Join(town, "demo/.repo.git") {
		t.Errorf("workspace create calls = %v", creates)
	}

	// Polecat: a linked worktree workspace nested under the rig workspace.
	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-alpha", "-c", "/w/alpha",
		"-e", "GT_ROLE=demo/polecats/alpha", "-e", "GT_POLECAT=alpha"}, rigEnv...)...)
	opens := f.herdrCalls("worktree open")
	if len(opens) != 1 || flagVal(opens[0], "--workspace") != "w2" || flagVal(opens[0], "--path") != "/w/alpha" ||
		flagVal(opens[0], "--label") != "alpha" || !strings.Contains(strings.Join(opens[0], " "), "--no-focus") {
		t.Errorf("worktree open = %v", opens)
	}
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"demo", "hq", "alpha"}) {
		t.Errorf("workspaces = %v", got)
	}
	st, _ := f.store.Load("to-alpha")
	if st.Kind != KindWorktree || st.Label != "alpha" || st.HerdrPaneID == "" {
		t.Errorf("polecat state = %+v", st)
	}
	if got := mustRun(t, run, "display-message", "-p", "-t", "to-alpha", "#{window_name}"); got != "alpha\n" {
		t.Errorf("window_name = %q", got)
	}

	// Polecat whose cwd is not a worktree falls back to a tab in the rig workspace.
	mustRun(t, run, append([]string{"new-session", "-d", "-s", "to-beta", "-c", "/not/a/worktree",
		"-e", "GT_ROLE=demo/polecats/beta", "-e", "GT_POLECAT=beta"}, rigEnv...)...)
	st, _ = f.store.Load("to-beta")
	if st.Kind != KindTab || st.Label != "beta" {
		t.Errorf("fallback state = %+v", st)
	}
	if got := f.tabLabels("demo"); !reflect.DeepEqual(got, []string{"witness", "refinery", "beta"}) {
		t.Errorf("rig tabs = %v", got)
	}

	// Killing a worktree session closes its workspace only; a tab session only its tab.
	mustRun(t, run, "kill-session", "-t", "=to-alpha")
	if got := f.wsLabels(); !reflect.DeepEqual(got, []string{"demo", "hq"}) {
		t.Errorf("after polecat kill = %v", got)
	}
	mustRun(t, run, "kill-session", "-t", "=to-beta")
	if got := f.tabLabels("demo"); !reflect.DeepEqual(got, []string{"witness", "refinery"}) {
		t.Errorf("after tab kill = %v", got)
	}
	for _, c := range f.calls {
		if len(c) > 3 && c[2] == "worktree" && c[3] == "remove" {
			t.Error("worktree remove must never be called")
		}
	}
}

func TestPolecatFirstThenTabReusesRootTab(t *testing.T) {
	f, run := newFake(t, "gt-p4")
	f.worktrees["/w/alpha"] = true
	mustRun(t, run, "new-session", "-d", "-s", "to-alpha", "-c", "/w/alpha", "-e", "GT_ROLE=demo/polecats/alpha", "-e", "GT_POLECAT=alpha")
	// The rig workspace was created as parent; its idle shell tab is kept for the next tab session.
	if got := f.tabLabels("demo"); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("rig tabs = %v", got)
	}
	mustRun(t, run, "new-session", "-d", "-s", "to-witness", "-e", "GT_ROLE=demo/witness", "-e", "GT_RIG=demo")
	if got := f.tabLabels("demo"); !reflect.DeepEqual(got, []string{"witness"}) {
		t.Errorf("rig tabs = %v", got)
	}
}

func TestSharedKillServerClosesOnlyTracked(t *testing.T) {
	f, run := newFake(t, "gt-p5")
	f.shared = true
	f.worktrees["/w/alpha"] = true
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor", "-e", "GT_ROLE=mayor")
	mustRun(t, run, "new-session", "-d", "-s", "hq-deacon", "-e", "GT_ROLE=deacon")
	mustRun(t, run, "new-session", "-d", "-s", "to-alpha", "-c", "/w/alpha", "-e", "GT_ROLE=demo/polecats/alpha", "-e", "GT_POLECAT=alpha")
	// a foreign tab the user added to our group must survive
	_, _, _, _ = f.exec(nil, "herdr", []string{"--session", "gt-p5", "tab", "create", "--workspace", "w2", "--label", "user-tab"}, nil)
	mustRun(t, run, "kill-server")
	if !f.running {
		t.Error("shared server must not stop")
	}
	if got := f.tabLabels("hq"); !reflect.DeepEqual(got, []string{"user-tab"}) {
		t.Errorf("hq tabs after kill-server = %v", got)
	}
	for _, w := range f.wsLabels() {
		if w == "alpha" {
			t.Error("polecat workspace should be closed")
		}
	}
	if len(f.workspace) == 0 || f.workspace[0].Label != "~" {
		t.Errorf("user workspace lost: %+v", f.workspace)
	}
}

func TestLegacyStateStillTracked(t *testing.T) {
	f, run := newFake(t, "gt-p6")
	// A town started before the layout change: one workspace per session, no Kind.
	mustRun(t, run, "new-session", "-d", "-s", "boot")
	w := f.newWS("legacy-s")
	pane := w.Tabs[0].Pane
	if err := f.store.Update("legacy-s", true, func(st *SessionState) error {
		st.HerdrPaneID = pane // no WorkspaceID/TabID/Kind
		st.PanePID = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, run, "list-sessions", "-F", "#{session_name}:#{session_id}:#{window_name}"); !strings.Contains(got, "legacy-s:$"+w.ID[1:]+":legacy-s\n") {
		t.Errorf("list-sessions = %q", got)
	}
	mustRun(t, run, "rename-session", "-t", "legacy-s", "legacy-t")
	if f.workspace[len(f.workspace)-1].Label != "legacy-t" {
		t.Error("legacy rename should rename the workspace")
	}
	mustRun(t, run, "kill-session", "-t", "legacy-t")
	if len(f.herdrCalls("tab close")) != 0 {
		t.Error("legacy kill must close the workspace, not a tab")
	}
	if _, _, code := run("has-session", "-t", "=legacy-t"); code == 0 {
		t.Error("legacy session should be gone")
	}
	if _, _, code := run("has-session", "-t", "=boot"); code != 0 {
		t.Error("other session lost")
	}
}

func TestPruneOnlyOnDefiniteAbsence(t *testing.T) {
	f, run := newFake(t, "gt-p7")
	f.worktrees["/w/alpha"] = true
	mustRun(t, run, "new-session", "-d", "-s", "hq-mayor")
	mustRun(t, run, "new-session", "-d", "-s", "hq-deacon")
	mustRun(t, run, "new-session", "-d", "-s", "to-alpha", "-c", "/w/alpha", "-e", "GT_ROLE=demo/polecats/alpha", "-e", "GT_POLECAT=alpha")

	// herdr failing to answer must not prune anything.
	f.failTabList = true
	if _, _, code := run("has-session", "-t", "=hq-mayor"); code == 0 {
		t.Error("an erroring herdr cannot confirm a session")
	}
	f.failTabList = false
	for _, n := range []string{"hq-mayor", "hq-deacon", "to-alpha"} {
		if _, err := f.store.Load(n); err != nil {
			t.Errorf("state %s pruned on herdr error", n)
		}
	}

	// A successful listing that lacks one tab prunes just that session.
	st, _ := f.store.Load("hq-deacon")
	wi, ti := f.findTab(st.TabID)
	f.workspace[wi].Tabs = append(f.workspace[wi].Tabs[:ti], f.workspace[wi].Tabs[ti+1:]...)
	if _, _, code := run("has-session", "-t", "=hq-deacon"); code == 0 {
		t.Error("deacon should be gone")
	}
	if _, err := f.store.Load("hq-deacon"); err == nil {
		t.Error("deacon state should be pruned")
	}
	for _, n := range []string{"hq-mayor", "to-alpha"} {
		if _, err := f.store.Load(n); err != nil {
			t.Errorf("state %s wrongly pruned", n)
		}
	}
}
