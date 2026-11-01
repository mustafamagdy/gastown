package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RuntimeRoutingAuto is the policy default that picks an agent from live quota.
const RuntimeRoutingAuto = "auto"

// RuntimeRoutingPolicy is a project's quota-based agent routing policy, stored
// as "runtime_routing" in projects/<name>/project.json.
type RuntimeRoutingPolicy struct {
	// Default is "auto" (pick from live quota) or a fixed gt agent name.
	Default string `json:"default"`
	// Hard is the agent for beads labelled "hard". Optional.
	Hard string `json:"hard,omitempty"`
	// AvoidBelowPct drops candidates with less than this percent of quota left.
	AvoidBelowPct float64 `json:"avoid_below_pct,omitempty"`
	// Candidates are gt agent names in preference order.
	Candidates []string `json:"candidates,omitempty"`
	// Providers maps a gt agent name to its quota-axi provider. An agent not
	// listed uses its own name as the provider.
	Providers map[string]string `json:"providers,omitempty"`
}

// Provider returns the quota-axi provider of a gt agent name.
func (p *RuntimeRoutingPolicy) Provider(agent string) string {
	if v := p.Providers[agent]; v != "" {
		return v
	}
	return agent
}

// Validate checks the policy for internal consistency.
func (p *RuntimeRoutingPolicy) Validate() error {
	if p.AvoidBelowPct < 0 || p.AvoidBelowPct > 100 {
		return fmt.Errorf("runtime_routing: avoid_below_pct %v is outside 0-100", p.AvoidBelowPct)
	}
	if strings.TrimSpace(p.Default) == "" {
		return fmt.Errorf("runtime_routing: default is required (\"auto\" or an agent name)")
	}
	if p.Default == RuntimeRoutingAuto && len(p.Candidates) == 0 {
		return fmt.Errorf("runtime_routing: default \"auto\" needs a non-empty candidates list")
	}
	for _, c := range p.Candidates {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("runtime_routing: candidates contains an empty agent name")
		}
	}
	return nil
}

// ProjectRuntimeRouting parses and validates a project's runtime_routing
// policy. It returns (nil, nil) when the project has none. Unknown fields,
// "auto" without candidates, and a percentage outside 0-100 are errors.
func ProjectRuntimeRouting(p *ProjectConfig) (*RuntimeRoutingPolicy, error) {
	if p == nil {
		return nil, nil
	}
	raw := bytes.TrimSpace(p.RuntimeRouting)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var pol RuntimeRoutingPolicy
	if err := dec.Decode(&pol); err != nil {
		return nil, fmt.Errorf("runtime_routing: %w", err)
	}
	if err := pol.Validate(); err != nil {
		return nil, err
	}
	return &pol, nil
}
