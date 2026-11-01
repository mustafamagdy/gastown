package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/runtimeroute"
)

type stubQuota struct {
	q     map[string]runtimeroute.ProviderQuota
	err   error
	calls int
}

func (s *stubQuota) Read(context.Context) (map[string]runtimeroute.ProviderQuota, error) {
	s.calls++
	return s.q, s.err
}

// routeTestTown creates a town with project "shop" holding the given
// runtime_routing JSON, and a rig "orders_svc" in it. Not parallel-safe: it
// swaps the newQuotaReader seam.
func routeTestTown(t *testing.T, policy string, stub *stubQuota) string {
	t.Helper()
	town := t.TempDir()
	rc := &config.RigsConfig{Version: config.CurrentRigsVersion, Rigs: map[string]config.RigEntry{
		"orders_svc": {GitURL: "file:///r/o.git", Project: "shop"},
		"solo":       {GitURL: "file:///r/s.git"},
	}}
	if err := config.SaveRigsConfig(filepath.Join(town, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
	pc := &config.ProjectConfig{Name: "shop", Prefix: "sh"}
	if policy != "" {
		pc.RuntimeRouting = []byte(policy)
	}
	if err := config.SaveProjectConfig(town, pc); err != nil {
		t.Fatal(err)
	}
	old := newQuotaReader
	newQuotaReader = func(string) runtimeroute.QuotaReader { return stub }
	t.Cleanup(func() { newQuotaReader = old })
	return town
}

const routeAuto = `{"default":"auto","avoid_below_pct":20,"candidates":["claude","codex"]}`

func TestRoutePolecatAgentExplicitBypassesRouting(t *testing.T) {
	stub := &stubQuota{q: map[string]runtimeroute.ProviderQuota{"claude": {Known: true, Percent: 90}}}
	town := routeTestTown(t, routeAuto, stub)
	rig := filepath.Join(town, "orders_svc")
	if got := routePolecatAgent(town, "orders_svc", rig, "", "gemini"); got != "gemini" {
		t.Errorf("explicit agent = %q, want gemini", got)
	}
	if stub.calls != 0 {
		t.Errorf("explicit agent must not read quota, got %d reads", stub.calls)
	}
}

func TestRoutePolecatAgentUnchangedWithoutPolicy(t *testing.T) {
	stub := &stubQuota{}
	town := routeTestTown(t, "", stub)
	rig := filepath.Join(town, "orders_svc")
	if got := routePolecatAgent(town, "orders_svc", rig, "", ""); got != "" {
		t.Errorf("no policy: got %q, want none", got)
	}
	if got := routePolecatAgent(town, "solo", filepath.Join(town, "solo"), "", ""); got != "" {
		t.Errorf("rig outside a project: got %q, want none", got)
	}
	if stub.calls != 0 {
		t.Errorf("unexpected quota reads: %d", stub.calls)
	}
}

func TestRoutePolecatAgentPicksFromQuotaAndRecords(t *testing.T) {
	stub := &stubQuota{q: map[string]runtimeroute.ProviderQuota{
		"claude": {Known: true, Percent: 26}, "codex": {Known: true, Percent: 60},
	}}
	town := routeTestTown(t, routeAuto, stub)
	rig := filepath.Join(town, "orders_svc")
	if got := routePolecatAgent(town, "orders_svc", rig, "", ""); got != "codex" {
		t.Errorf("got %q, want codex", got)
	}
	data, err := os.ReadFile(filepath.Join(town, "logs", "town.log"))
	if err != nil {
		t.Fatalf("decision not recorded in the town log: %v", err)
	}
	if !strings.Contains(string(data), "[route] orders_svc") || !strings.Contains(string(data), "reason=") {
		t.Errorf("town log = %q", data)
	}
}

func TestRoutePolecatAgentFailsOpenOnBadQuotaAndBadPolicy(t *testing.T) {
	stub := &stubQuota{err: errors.New("quota-axi not found on PATH")}
	town := routeTestTown(t, routeAuto, stub)
	rig := filepath.Join(town, "orders_svc")
	if got := routePolecatAgent(town, "orders_svc", rig, "", ""); got != "" {
		t.Errorf("quota failure: got %q, want no override", got)
	}

	town = routeTestTown(t, `{"default":"auto","candidates":["claude"],"nope":1}`, &stubQuota{})
	if got := routePolecatAgent(town, "orders_svc", filepath.Join(town, "orders_svc"), "", ""); got != "" {
		t.Errorf("bad policy: got %q, want no override", got)
	}
}

func TestRoutePolecatAgentUnconfiguredAgentFallsBack(t *testing.T) {
	town := routeTestTown(t, `{"default":"no-such-agent-xyz"}`, &stubQuota{})
	if got := routePolecatAgent(town, "orders_svc", filepath.Join(town, "orders_svc"), "", ""); got != "" {
		t.Errorf("unconfigured agent: got %q, want fallback to no override", got)
	}
}

func TestRunProjectRouteExplainsAndWritesNothing(t *testing.T) {
	stub := &stubQuota{q: map[string]runtimeroute.ProviderQuota{
		"claude": {Known: true, Percent: 26},
		"codex":  {Why: "state auth_required (Codex sign-in required)"},
	}}
	town := routeTestTown(t, routeAuto, stub)
	var out bytes.Buffer
	if err := runProjectRoute(&out, town, "shop", false); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"default=auto", "claude", "26% remaining", "unknown: state auth_required", "Choice:  claude"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	if _, err := os.Stat(filepath.Join(town, "logs")); err == nil {
		t.Error("explain must not write the town log")
	}

	out.Reset()
	if err := runProjectRoute(&out, town, "shop", true); err != nil {
		t.Fatal(err)
	}
}

func TestRunProjectRouteSurfacesPolicyErrors(t *testing.T) {
	town := routeTestTown(t, `{"default":"auto"}`, &stubQuota{})
	var out bytes.Buffer
	if err := runProjectRoute(&out, town, "shop", false); err == nil || !strings.Contains(err.Error(), "candidates") {
		t.Errorf("want candidates error, got %v", err)
	}
	town = routeTestTown(t, "", &stubQuota{})
	out.Reset()
	if err := runProjectRoute(&out, town, "shop", false); err != nil || !strings.Contains(out.String(), "no runtime_routing") {
		t.Errorf("no policy: %v %q", err, out.String())
	}
}
