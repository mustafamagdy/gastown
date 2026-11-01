package runtimeroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// QuotaTimeout bounds one quota-axi read.
const QuotaTimeout = 15 * time.Second

// quotaArgs are the quota-axi arguments for a routing read: JSON, reuse a
// reading younger than 60s, and never renew credentials (a read must not have
// side effects on the org's accounts).
var quotaArgs = []string{"--json", "--max-age", "60s", "--no-credential-refresh"}

// ProviderQuota is what routing knows about one quota-axi provider.
type ProviderQuota struct {
	// Known is true only for a fresh, set up provider that reports an
	// all_models figure. Percent is meaningful only when Known.
	Known   bool
	Percent float64
	// Why says why the figure is unknown (empty when Known).
	Why string
}

// QuotaReader reads live quota per provider.
type QuotaReader interface {
	Read(ctx context.Context) (map[string]ProviderQuota, error)
}

// ExecReader runs quota-axi. Env is the process environment (the town's org
// environment applied); Dir is the working directory.
type ExecReader struct {
	Env []string
	Dir string
	// Run replaces the real command, for tests. It returns quota-axi's stdout.
	Run func(ctx context.Context, env []string, dir string, args []string) ([]byte, error)
}

// Read runs quota-axi with a 15 second timeout and parses its JSON.
func (r *ExecReader) Read(ctx context.Context) (map[string]ProviderQuota, error) {
	ctx, cancel := context.WithTimeout(ctx, QuotaTimeout)
	defer cancel()
	run := r.Run
	if run == nil {
		run = runQuotaAxi
	}
	out, err := run(ctx, r.Env, r.Dir, quotaArgs)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("quota-axi timed out after %s", QuotaTimeout)
		}
		return nil, err
	}
	return ParseQuota(out)
}

func runQuotaAxi(ctx context.Context, env []string, dir string, args []string) ([]byte, error) {
	path, err := exec.LookPath("quota-axi")
	if err != nil {
		return nil, fmt.Errorf("quota-axi not found on PATH")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("quota-axi failed: %w: %s", err, firstLine(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("quota-axi failed: %w", err)
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

type availability struct {
	Scope                     string   `json:"scope"`
	Status                    string   `json:"status"`
	EffectivePercentRemaining *float64 `json:"effectivePercentRemaining"`
}

// quotaDoc is the part of the quota-axi --json document routing reads.
type quotaDoc struct {
	Providers []struct {
		Provider string `json:"provider"`
		NotSetUp bool   `json:"notSetUp"`
		State    struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"state"`
		QuotaSemantics struct {
			EffectiveAvailability []availability `json:"effectiveAvailability"`
		} `json:"quotaSemantics"`
	} `json:"providers"`
}

// ParseQuota extracts, per provider, the all_models effective percent
// remaining. A provider whose state is not "fresh", that is not set up, or that
// has no known all_models figure is reported as unknown, never guessed.
func ParseQuota(data []byte) (map[string]ProviderQuota, error) {
	var doc quotaDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing quota-axi output: %w", err)
	}
	if doc.Providers == nil {
		return nil, fmt.Errorf("parsing quota-axi output: no providers field")
	}
	out := make(map[string]ProviderQuota, len(doc.Providers))
	for _, p := range doc.Providers {
		out[p.Provider] = providerQuota(p.NotSetUp, p.State.Status, p.State.Error, p.QuotaSemantics.EffectiveAvailability)
	}
	return out, nil
}

func providerQuota(notSetUp bool, status, stateErr string, avail []availability) ProviderQuota {
	if notSetUp {
		return ProviderQuota{Why: "not set up"}
	}
	if status != "fresh" {
		why := "state " + status
		if status == "" {
			why = "state unknown"
		}
		if stateErr != "" {
			why += " (" + stateErr + ")"
		}
		return ProviderQuota{Why: why}
	}
	for _, a := range avail {
		if a.Scope != "all_models" {
			continue
		}
		if a.Status != "known" || a.EffectivePercentRemaining == nil {
			return ProviderQuota{Why: "all_models figure not known"}
		}
		return ProviderQuota{Known: true, Percent: *a.EffectivePercentRemaining}
	}
	return ProviderQuota{Why: "no all_models figure"}
}
