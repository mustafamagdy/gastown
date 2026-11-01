package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/axictx"
	"github.com/steveyegge/gastown/internal/config"
)

func writeAxiTownSettings(t *testing.T, tools []string) string {
	t.Helper()
	town := t.TempDir()
	settings := config.NewTownSettings()
	settings.AxiTools = tools
	if err := os.MkdirAll(filepath.Join(town, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(town), settings); err != nil {
		t.Fatal(err)
	}
	return town
}

func stubAxiCollect(t *testing.T, fn func(axictx.Options) []axictx.Result) *int {
	t.Helper()
	calls := 0
	orig := collectAxiContext
	collectAxiContext = func(_ context.Context, o axictx.Options) []axictx.Result {
		calls++
		return fn(o)
	}
	t.Cleanup(func() { collectAxiContext = orig })
	return &calls
}

func TestOutputAxiContext_NoAxiToolsRunsNothing(t *testing.T) {
	town := writeAxiTownSettings(t, nil)
	calls := stubAxiCollect(t, func(axictx.Options) []axictx.Result { return nil })
	out := captureStdout(t, func() { outputAxiContext(RoleContext{TownRoot: town}) })
	if out != "" || *calls != 0 {
		t.Errorf("out=%q calls=%d", out, *calls)
	}
}

func TestOutputAxiContext_OnlyToolsWithoutViewRunsNothing(t *testing.T) {
	town := writeAxiTownSettings(t, []string{"lavish-axi", "quota-axi@1.0.0"})
	calls := stubAxiCollect(t, func(axictx.Options) []axictx.Result { return nil })
	out := captureStdout(t, func() { outputAxiContext(RoleContext{TownRoot: town}) })
	if out != "" || *calls != 0 {
		t.Errorf("out=%q calls=%d", out, *calls)
	}
}

func TestOutputAxiContext_BlocksAndFailureLine(t *testing.T) {
	town := writeAxiTownSettings(t, []string{"gh-axi@0.1.37", "lavish-axi", "az-axi"})
	var got axictx.Options
	stubAxiCollect(t, func(o axictx.Options) []axictx.Result {
		got = o
		return []axictx.Result{
			{Tool: "gh-axi", Text: "issues: 2 open"},
			{Tool: "az-axi", Err: errors.New("timed out after 5s")},
		}
	})
	out := captureStdout(t, func() { outputAxiContext(RoleContext{TownRoot: town}) })
	if !strings.Contains(out, "## AXI context: gh-axi\n\nissues: 2 open") {
		t.Errorf("missing gh-axi block: %q", out)
	}
	if !strings.Contains(out, "az-axi unavailable (timed out after 5s)") {
		t.Errorf("missing failure line: %q", out)
	}
	if strings.Contains(out, "lavish-axi") {
		t.Errorf("tool without a view must not appear: %q", out)
	}
	if strings.Join(got.Tools, ",") != "gh-axi@0.1.37,az-axi" || got.TownRoot != town {
		t.Errorf("options = %+v", got)
	}
}

func TestOutputAxiContext_DryRunDoesNotRun(t *testing.T) {
	town := writeAxiTownSettings(t, []string{"gh-axi"})
	calls := stubAxiCollect(t, func(axictx.Options) []axictx.Result { return nil })
	prevDry, prevExplain := primeDryRun, primeExplain
	primeDryRun, primeExplain = true, true
	t.Cleanup(func() { primeDryRun, primeExplain = prevDry, prevExplain })
	out := captureStdout(t, func() { outputAxiContext(RoleContext{TownRoot: town}) })
	if *calls != 0 || !strings.Contains(out, "[EXPLAIN] AXI context") {
		t.Errorf("out=%q calls=%d", out, *calls)
	}
}
