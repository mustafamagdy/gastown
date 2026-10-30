package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
)

// mapTestTown creates a town with project "shop" and a rig with a bare repo
// holding a service.toml and openapi.yaml.
func mapTestTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	work := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"service.toml": "name = \"orders-svc\"\nowns = \"orders\"\ncalls = [\"billing-svc\"]\n",
		"openapi.yaml": "paths:\n  /orders:\n    get: {}\n",
	}
	for p, c := range files {
		if err := os.WriteFile(filepath.Join(work, p), []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	bare := filepath.Join(town, "orders_svc", ".repo.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", work, "init", "-q", "-b", "main"},
		{"-C", work, "add", "-A"},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "c"},
		{"-C", work, "clone", "-q", "--bare", work, bare},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rc := &config.RigsConfig{Version: config.CurrentRigsVersion, Rigs: map[string]config.RigEntry{
		"orders_svc":  {GitURL: "file:///r/orders-svc.git", Project: "shop"},
		"billing_svc": {GitURL: "file:///r/billing-svc.git", Project: "shop"},
	}}
	if err := config.SaveRigsConfig(filepath.Join(town, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh"}); err != nil {
		t.Fatal(err)
	}
	return town
}

func TestProjectMapBuildAndShow(t *testing.T) {
	t.Parallel()
	town := mapTestTown(t)

	var show bytes.Buffer
	if err := runProjectMapShow(&show, town, "shop", "", false); err == nil || !strings.Contains(err.Error(), "gt project map build shop") {
		t.Fatalf("expected build hint before first build, got %v", err)
	}

	var out bytes.Buffer
	if err := runProjectMapBuild(&out, town, "shop", false, ""); err != nil {
		t.Fatal(err)
	}
	// billing_svc has no repo: reported, not fatal.
	if !strings.Contains(out.String(), "orders_svc scanned") || !strings.Contains(out.String(), "billing_svc") {
		t.Errorf("build output: %s", out.String())
	}
	out.Reset()
	if err := runProjectMapBuild(&out, town, "shop", false, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "orders_svc unchanged") {
		t.Errorf("second build should skip: %s", out.String())
	}

	show.Reset()
	if err := runProjectMapShow(&show, town, "shop", "orders_svc", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(show.String(), "orders-svc (rig orders_svc): owns orders") || !strings.Contains(show.String(), "GET /orders") {
		t.Errorf("show --rig: %s", show.String())
	}

	show.Reset()
	if err := runProjectMapShow(&show, town, "shop", "", true); err != nil {
		t.Fatal(err)
	}
	var m projectmap.Map
	if err := json.Unmarshal(show.Bytes(), &m); err != nil || len(m.Services) != 1 {
		t.Errorf("show --json: %v %s", err, show.String())
	}
	if err := runProjectMapShow(&show, town, "shop", "nope", false); err == nil {
		t.Error("expected error for unknown rig")
	}
}

func TestServiceMapPrimeSection(t *testing.T) {
	// Not parallel: captures os.Stdout.
	town := mapTestTown(t)
	ctx := RoleContext{Role: RolePolecat, TownRoot: town, Rig: "orders_svc"}

	out := captureStdout(t, func() { outputServiceMapContext(ctx) })
	if !strings.Contains(out, "gt project map build shop") {
		t.Errorf("missing-map hint absent: %q", out)
	}

	// Rig outside any project prints nothing.
	if out := captureStdout(t, func() { outputServiceMapContext(RoleContext{TownRoot: town, Rig: "other"}) }); out != "" {
		t.Errorf("unexpected output: %q", out)
	}

	m := &projectmap.Map{Project: "shop", Services: []projectmap.Service{{Rig: "orders_svc", Name: "orders-svc", Owns: "orders"}}}
	for i := 0; i < 12; i++ {
		m.Services = append(m.Services, projectmap.Service{Rig: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("c%02d", i), Calls: []string{"orders_svc"}})
	}
	if err := projectmap.Save(town, m); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { outputServiceMapContext(ctx) })
	if !strings.Contains(out, "## Service map: orders_svc and its neighbours") ||
		!strings.Contains(out, "and 2 more (gt project map show shop --rig orders_svc)") {
		t.Errorf("section: %s", out)
	}
	if lines := strings.Count(out, "\n"); lines > 60 {
		t.Errorf("section too long: %d lines", lines)
	}
}
