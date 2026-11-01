// Package runtimeroute picks a polecat's agent runtime from live quota, using
// a per-project policy (runtime_routing in project.json). It never blocks work:
// every failure falls back to the rig's normal agent with a warning.
package runtimeroute

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// HardLabel is the bead label that asks for the policy's hard agent.
const HardLabel = "hard"

// Input is one routing request.
type Input struct {
	// Explicit is an agent chosen by the caller (--agent). It always wins.
	Explicit string
	// Policy is the project's policy; nil means no policy (unchanged behaviour).
	Policy *config.RuntimeRoutingPolicy
	// PolicyErr is an invalid policy; routing fails open with a warning.
	PolicyErr error
	// Hard is true when the bead carries the "hard" label.
	Hard bool
}

// Candidate is one candidate with the quota that was seen for it.
type Candidate struct {
	Agent    string
	Provider string
	Quota    ProviderQuota
	// Dropped says why the candidate was not eligible (empty when eligible).
	Dropped string
}

// Decision is the result of routing.
type Decision struct {
	// Agent is the agent to start with. Empty means no override: the rig's
	// normal configured agent is used.
	Agent string
	// Routed is true when the policy was consulted (a real decision to record).
	Routed bool
	// Reason is a short explanation of the choice.
	Reason string
	// Warning is set when routing failed open or overrode a low quota.
	Warning string
	// Candidates lists the quota seen for each candidate (auto mode).
	Candidates []Candidate
}

// Decide applies the decision order:
//  1. explicit agent wins, no routing
//  2. no policy: unchanged
//  3. invalid policy: fail open
//  4. hard bead with a hard agent: hard agent, even when its quota is low
//  5. fixed default: that agent
//  6. auto: most remaining quota among candidates at or above avoid_below_pct,
//     ties by candidate order
//
// Quota is read only for steps 4 (to warn) and 6; a read failure or no eligible
// candidate fails open (Agent empty, Warning set).
func Decide(ctx context.Context, in Input, reader QuotaReader) Decision {
	if in.Explicit != "" {
		return Decision{Agent: in.Explicit, Reason: "explicit agent"}
	}
	if in.PolicyErr != nil {
		return Decision{Routed: true, Reason: "invalid policy", Warning: fmt.Sprintf("runtime routing skipped, using the rig's agent: %v", in.PolicyErr)}
	}
	if in.Policy == nil {
		return Decision{}
	}
	pol := in.Policy

	if in.Hard && pol.Hard != "" {
		d := Decision{Agent: pol.Hard, Routed: true, Reason: "hard bead"}
		if reader != nil {
			if q, err := reader.Read(ctx); err == nil {
				pq, ok := q[pol.Provider(pol.Hard)]
				switch {
				case !ok:
					d.Warning = fmt.Sprintf("hard agent %s is not reported by quota-axi; using it anyway", pol.Hard)
				case !pq.Known:
					d.Warning = fmt.Sprintf("hard agent %s has unknown quota (%s); using it anyway", pol.Hard, pq.Why)
				case pq.Percent <= 0 || pq.Percent < pol.AvoidBelowPct:
					d.Warning = fmt.Sprintf("hard agent %s has %s%% quota left, below avoid_below_pct %s%%; using it anyway",
						pol.Hard, pct(pq.Percent), pct(pol.AvoidBelowPct))
				}
			}
		}
		return d
	}

	if pol.Default != config.RuntimeRoutingAuto {
		return Decision{Agent: pol.Default, Routed: true, Reason: "fixed default"}
	}

	if reader == nil {
		return failOpen("no quota reader")
	}
	q, err := reader.Read(ctx)
	if err != nil {
		return failOpen(err.Error())
	}
	var cands []Candidate
	best := -1
	for _, name := range pol.Candidates {
		prov := pol.Provider(name)
		c := Candidate{Agent: name, Provider: prov}
		pq, ok := q[prov]
		switch {
		case !ok:
			c.Quota = ProviderQuota{Why: "provider not reported by quota-axi"}
			c.Dropped = "quota unknown"
		case !pq.Known:
			c.Quota = pq
			c.Dropped = "quota unknown"
		case pq.Percent <= 0:
			c.Quota = pq
			c.Dropped = "no quota left"
		case pq.Percent < pol.AvoidBelowPct:
			c.Quota = pq
			c.Dropped = fmt.Sprintf("below avoid_below_pct %s%%", pct(pol.AvoidBelowPct))
		default:
			c.Quota = pq
		}
		cands = append(cands, c)
		if c.Dropped == "" && (best < 0 || c.Quota.Percent > cands[best].Quota.Percent) {
			best = len(cands) - 1
		}
	}
	if best < 0 {
		d := failOpen("no candidate has enough known quota")
		d.Candidates = cands
		return d
	}
	return Decision{
		Agent:      cands[best].Agent,
		Routed:     true,
		Reason:     fmt.Sprintf("most quota left (%s%%)", pct(cands[best].Quota.Percent)),
		Candidates: cands,
	}
}

func failOpen(why string) Decision {
	return Decision{
		Routed:  true,
		Reason:  "fail open: " + why,
		Warning: fmt.Sprintf("runtime routing could not choose (%s); using the rig's agent", why),
	}
}

func pct(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
}

// Describe renders a candidate for display.
func (c Candidate) Describe() string {
	switch {
	case c.Quota.Known && c.Dropped == "":
		return fmt.Sprintf("%s%% remaining", pct(c.Quota.Percent))
	case c.Quota.Known:
		return fmt.Sprintf("%s%% remaining, dropped: %s", pct(c.Quota.Percent), c.Dropped)
	default:
		return "unknown: " + c.Quota.Why
	}
}
