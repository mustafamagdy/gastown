package runtimeroute

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

type fakeReader struct {
	q     map[string]ProviderQuota
	err   error
	calls int
}

func (f *fakeReader) Read(context.Context) (map[string]ProviderQuota, error) {
	f.calls++
	return f.q, f.err
}

func known(p float64) ProviderQuota { return ProviderQuota{Known: true, Percent: p} }

func auto(avoid float64, cands ...string) *config.RuntimeRoutingPolicy {
	return &config.RuntimeRoutingPolicy{Default: "auto", AvoidBelowPct: avoid, Candidates: cands, Hard: "claude"}
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		in        Input
		reader    *fakeReader
		wantAgent string
		wantRoute bool
		wantWarn  string // substring; "" means no warning
		wantCalls int
	}{
		{
			name:      "explicit agent wins, no quota read",
			in:        Input{Explicit: "gemini", Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{},
			wantAgent: "gemini",
		},
		{
			name:   "no policy is unchanged",
			in:     Input{},
			reader: &fakeReader{},
		},
		{
			name:      "invalid policy fails open",
			in:        Input{PolicyErr: errors.New("bad field")},
			reader:    &fakeReader{},
			wantRoute: true,
			wantWarn:  "bad field",
		},
		{
			name:      "hard bead uses hard agent",
			in:        Input{Policy: auto(20, "claude", "codex"), Hard: true},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(80)}},
			wantAgent: "claude",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "hard bead uses hard agent even when low, with a warning",
			in:        Input{Policy: auto(20, "claude", "codex"), Hard: true},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(5)}},
			wantAgent: "claude",
			wantRoute: true,
			wantWarn:  "below avoid_below_pct",
			wantCalls: 1,
		},
		{
			name:      "hard bead survives a quota read failure",
			in:        Input{Policy: auto(20, "claude"), Hard: true},
			reader:    &fakeReader{err: errors.New("boom")},
			wantAgent: "claude",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "hard label without hard agent follows the default",
			in:        Input{Policy: &config.RuntimeRoutingPolicy{Default: "codex"}, Hard: true},
			reader:    &fakeReader{},
			wantAgent: "codex",
			wantRoute: true,
		},
		{
			name:      "fixed default, no quota read",
			in:        Input{Policy: &config.RuntimeRoutingPolicy{Default: "codex"}},
			reader:    &fakeReader{},
			wantAgent: "codex",
			wantRoute: true,
		},
		{
			name:      "auto picks the highest",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(30), "codex": known(70)}},
			wantAgent: "codex",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "auto ties go to candidate order",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(50), "codex": known(50)}},
			wantAgent: "claude",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "auto drops candidates under the threshold",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(10), "codex": known(25)}},
			wantAgent: "codex",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "exactly at the threshold stays eligible",
			in:        Input{Policy: auto(20, "claude")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(20)}},
			wantAgent: "claude",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "unknown quota is not a candidate",
			in:        Input{Policy: auto(20, "codex", "claude")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"codex": {Why: "state auth_required"}, "claude": known(26)}},
			wantAgent: "claude",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "provider mapping is used",
			in:        Input{Policy: &config.RuntimeRoutingPolicy{Default: "auto", Candidates: []string{"claude-sonnet", "codex"}, Providers: map[string]string{"claude-sonnet": "claude"}}},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(90), "codex": known(40)}},
			wantAgent: "claude-sonnet",
			wantRoute: true,
			wantCalls: 1,
		},
		{
			name:      "fail open: quota-axi missing or failing",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{err: errors.New("quota-axi not found on PATH")},
			wantRoute: true,
			wantWarn:  "quota-axi not found",
			wantCalls: 1,
		},
		{
			name:      "fail open: every candidate under the threshold",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": known(5), "codex": known(10)}},
			wantRoute: true,
			wantWarn:  "no candidate",
			wantCalls: 1,
		},
		{
			name:      "fail open: every candidate unknown or missing",
			in:        Input{Policy: auto(20, "claude", "codex")},
			reader:    &fakeReader{q: map[string]ProviderQuota{"claude": {Why: "not set up"}}},
			wantRoute: true,
			wantWarn:  "no candidate",
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(ctx, tt.in, tt.reader)
			if d.Agent != tt.wantAgent {
				t.Errorf("Agent = %q, want %q (reason %q)", d.Agent, tt.wantAgent, d.Reason)
			}
			if d.Routed != tt.wantRoute {
				t.Errorf("Routed = %v, want %v", d.Routed, tt.wantRoute)
			}
			if tt.wantWarn == "" && d.Warning != "" {
				t.Errorf("unexpected warning %q", d.Warning)
			}
			if tt.wantWarn != "" && !strings.Contains(d.Warning, tt.wantWarn) {
				t.Errorf("Warning = %q, want substring %q", d.Warning, tt.wantWarn)
			}
			if tt.reader.calls != tt.wantCalls {
				t.Errorf("quota reads = %d, want %d", tt.reader.calls, tt.wantCalls)
			}
		})
	}
}

func TestDecideNilReaderFailsOpen(t *testing.T) {
	d := Decide(context.Background(), Input{Policy: auto(20, "claude")}, nil)
	if d.Agent != "" || d.Warning == "" {
		t.Errorf("got %+v, want fail open with a warning", d)
	}
}

func TestParseQuotaFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/quota.json")
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParseQuota(data)
	if err != nil {
		t.Fatal(err)
	}
	if c := q["claude"]; !c.Known || c.Percent != 26 {
		t.Errorf("claude = %+v, want known 26", c)
	}
	for _, name := range []string{"codex", "cursor", "copilot"} {
		if q[name].Known {
			t.Errorf("%s must be unknown, got %+v", name, q[name])
		}
		if q[name].Why == "" {
			t.Errorf("%s needs a reason", name)
		}
	}
	if !strings.Contains(q["cursor"].Why, "not set up") {
		t.Errorf("cursor why = %q", q["cursor"].Why)
	}
	if !strings.Contains(q["codex"].Why, "auth_required") {
		t.Errorf("codex why = %q", q["codex"].Why)
	}
}

func TestParseQuotaEdgeCases(t *testing.T) {
	if _, err := ParseQuota([]byte("not json")); err == nil {
		t.Error("bad JSON must error")
	}
	if _, err := ParseQuota([]byte(`{"x":1}`)); err == nil {
		t.Error("missing providers must error")
	}
	stale := `{"providers":[{"provider":"claude","state":{"status":"stale"},
		"quotaSemantics":{"effectiveAvailability":[{"scope":"all_models","status":"known","effectivePercentRemaining":50}]}}]}`
	q, err := ParseQuota([]byte(stale))
	if err != nil || q["claude"].Known {
		t.Errorf("stale provider must be unknown, got %+v err %v", q["claude"], err)
	}
	noFigure := `{"providers":[{"provider":"claude","state":{"status":"fresh"},
		"quotaSemantics":{"effectiveAvailability":[{"scope":"model:x","status":"known","effectivePercentRemaining":50}]}}]}`
	q, _ = ParseQuota([]byte(noFigure))
	if q["claude"].Known {
		t.Errorf("no all_models figure must be unknown, got %+v", q["claude"])
	}
	nullPct := `{"providers":[{"provider":"claude","state":{"status":"fresh"},
		"quotaSemantics":{"effectiveAvailability":[{"scope":"all_models","status":"unknown"}]}}]}`
	q, _ = ParseQuota([]byte(nullPct))
	if q["claude"].Known {
		t.Errorf("unknown all_models must be unknown, got %+v", q["claude"])
	}
}

func TestExecReader(t *testing.T) {
	data, _ := os.ReadFile("testdata/quota.json")
	var gotArgs []string
	var gotEnv []string
	r := &ExecReader{Env: []string{"A=1"}, Dir: "/x", Run: func(_ context.Context, env []string, dir string, args []string) ([]byte, error) {
		gotArgs, gotEnv = args, env
		return data, nil
	}}
	q, err := r.Read(context.Background())
	if err != nil || !q["claude"].Known {
		t.Fatalf("Read: %v %+v", err, q)
	}
	if strings.Join(gotArgs, " ") != "--json --max-age 60s --no-credential-refresh" {
		t.Errorf("args = %v", gotArgs)
	}
	if len(gotEnv) != 1 || gotEnv[0] != "A=1" {
		t.Errorf("env = %v", gotEnv)
	}

	r.Run = func(context.Context, []string, string, []string) ([]byte, error) { return []byte("garbage"), nil }
	if _, err := r.Read(context.Background()); err == nil {
		t.Error("bad JSON must error")
	}
	r.Run = func(context.Context, []string, string, []string) ([]byte, error) { return nil, errors.New("exit 1") }
	if _, err := r.Read(context.Background()); err == nil {
		t.Error("run error must propagate")
	}
	r.Run = func(ctx context.Context, _ []string, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	if _, err := r.Read(ctx); err == nil {
		t.Error("timeout must error")
	}
}
