package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectfeature"
)

func TestFeatureBranchName(t *testing.T) {
	cases := map[string]string{
		"Tenant credits":                  "feat/xe-42-tenant-credits",
		"  !!! ":                          "feat/xe-42",
		strings.Repeat("long title ", 10): "feat/xe-42-long-title-long-title-long-title-long-ti",
	}
	for title, want := range cases {
		got := featureBranchName("xe-42", title)
		if got != want {
			t.Errorf("featureBranchName(%q) = %q, want %q", title, got, want)
		}
		if err := validateFeatureBranch(got); err != nil {
			t.Errorf("default branch %q invalid: %v", got, err)
		}
	}
}

func TestParseFeatureRigs(t *testing.T) {
	got, err := parseFeatureRigs([]string{" api", "web ", ""})
	if err != nil || len(got) != 2 || got[0] != "api" || got[1] != "web" {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseFeatureRigs([]string{"api", "api"}); err == nil {
		t.Error("duplicate rig must fail")
	}
	if _, err := parseFeatureRigs([]string{"", " "}); err == nil {
		t.Error("empty list must fail")
	}
}

func TestValidateFeatureRigs(t *testing.T) {
	rc := &config.RigsConfig{Rigs: map[string]config.RigEntry{
		"api":   {Project: "shop"},
		"other": {Project: "blog"},
		"free":  {},
	}}
	if err := validateFeatureRigs(rc, "shop", []string{"api"}); err != nil {
		t.Errorf("api: %v", err)
	}
	for _, rig := range []string{"missing", "other", "free"} {
		if err := validateFeatureRigs(rc, "shop", []string{"api", rig}); err == nil {
			t.Errorf("rig %q must be rejected", rig)
		}
	}
}

func TestValidateFeatureBranch(t *testing.T) {
	for _, b := range []string{"", "main", "master", "has space", "a..b"} {
		if validateFeatureBranch(b) == nil {
			t.Errorf("branch %q must be rejected", b)
		}
	}
	if err := validateFeatureBranch("feat/xe-1-x"); err != nil {
		t.Error(err)
	}
}

func featureTestTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}
	rec := &projectfeature.Record{
		ID: "xe-1", Project: "shop", Branch: "feat/xe-1-x",
		Rigs:  map[string]projectfeature.RigState{"api": {State: projectfeature.RigBranch}},
		Tasks: []projectfeature.Task{{ID: "ap-1", Rig: "api"}},
	}
	if err := projectfeature.Create(town, rec); err != nil {
		t.Fatal(err)
	}
	return town
}

func TestFeatureTargetForBeadFailsClosed(t *testing.T) {
	town := featureTestTown(t)

	// unmarked, unlisted: unchanged behaviour
	if b, marked, err := featureTargetForBead(town, "api", "ap-9", &beads.Issue{Description: "plain"}); b != "" || marked || err != nil {
		t.Fatalf("unmarked = %q %v %v", b, marked, err)
	}
	if _, marked, err := featureTargetForBead(town, "api", "ap-9", nil); marked || err != nil {
		t.Fatalf("nil issue unlisted = %v %v", marked, err)
	}
	// marked
	issue := &beads.Issue{Description: projectfeature.AddMarker("x", "xe-1")}
	if b, marked, err := featureTargetForBead(town, "api", "ap-1", issue); b != "feat/xe-1-x" || !marked || err != nil {
		t.Fatalf("marked = %q %v %v", b, marked, err)
	}
	// listed in the record but bead unreadable or marker removed: still a feature task
	for _, iss := range []*beads.Issue{nil, {Description: "marker removed"}} {
		if b, marked, err := featureTargetForBead(town, "api", "ap-1", iss); b != "feat/xe-1-x" || !marked || err != nil {
			t.Fatalf("listed bead = %q %v %v", b, marked, err)
		}
	}
	// marked but wrong rig / missing record: error, never main
	if _, marked, err := featureTargetForBead(town, "web", "ap-1", issue); err == nil || !marked {
		t.Fatalf("wrong rig must error, got %v %v", marked, err)
	}
	ghost := &beads.Issue{Description: projectfeature.AddMarker("x", "xe-404")}
	if _, marked, err := featureTargetForBead(town, "api", "ap-2", ghost); err == nil || !marked {
		t.Fatalf("missing record must error, got %v %v", marked, err)
	}
}

func TestFeatureBaseForSpawnSkips(t *testing.T) {
	town := featureTestTown(t)
	for _, tc := range []struct{ hook, resume string }{{"", ""}, {"ap-1", "feat/resume"}} {
		if b, err := featureBaseForSpawn(town, "api", t.TempDir(), tc.hook, "", tc.resume); b != "" || err != nil {
			t.Errorf("%+v = %q %v", tc, b, err)
		}
	}
}

func TestFeatureRigRepo(t *testing.T) {
	town := t.TempDir()
	if _, err := featureRigRepo(town, "api"); err == nil {
		t.Fatal("rig without repo must error")
	}
	if err := os.MkdirAll(filepath.Join(town, "api", ".repo.git"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := featureRigRepo(town, "api")
	if err != nil || r.Default != "main" || !strings.HasSuffix(r.GitDir, ".repo.git") {
		t.Fatalf("repo = %+v %v", r, err)
	}
}

func TestFeatureBlockers(t *testing.T) {
	rec := &projectfeature.Record{
		ID: "sh-1", State: projectfeature.StateHeld,
		Rigs: map[string]projectfeature.RigState{
			"api": {State: projectfeature.RigPROpen, PRURL: "https://x/1"},
			"web": {State: projectfeature.RigHeld, Error: "review rejected"},
		},
	}
	views := []projectfeature.TaskView{{State: projectfeature.TaskDone}, {State: projectfeature.TaskRunning}}
	got := strings.Join(featureBlockers(rec, views), "\n")
	for _, want := range []string{"1 task(s) not closed", "rig api: PR waits", "rig web held: review rejected", "feature is held"} {
		if !strings.Contains(got, want) {
			t.Errorf("blockers missing %q:\n%s", want, got)
		}
	}
	rec.State = projectfeature.StateLanded
	if b := featureBlockers(rec, views); b != nil {
		t.Errorf("landed feature has blockers: %v", b)
	}
}
