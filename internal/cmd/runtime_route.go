package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/runtimeroute"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/townlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

var projectRouteHard bool

var projectRouteCmd = &cobra.Command{
	Use:   "route <project> [--hard]",
	Short: "Explain which agent runtime a polecat would get now",
	Long: `Show the project's runtime_routing policy, the live quota of each candidate
(or why it is unknown) and the agent a polecat started now would get. With
--hard, show the choice for a bead labelled "hard".

This only reads: it writes nothing and records no decision. A polecat started
with an explicit --agent is never routed.

Examples:
  gt project route shop
  gt project route shop --hard`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		return runProjectRoute(cmd.OutOrStdout(), townRoot, args[0], projectRouteHard)
	},
}

func init() {
	projectCmd.AddCommand(projectRouteCmd)
	projectRouteCmd.Flags().BoolVar(&projectRouteHard, "hard", false, "Show the choice for a bead labelled \"hard\"")
}

// newQuotaReader is a seam for tests. It builds the quota reader with the
// town's org environment, so quota-axi reads the org's accounts and no other.
var newQuotaReader = func(townRoot string) runtimeroute.QuotaReader {
	env := os.Environ()
	if org := config.OrgEnvForTown(townRoot); org != nil {
		env = org.Environ(env)
	}
	return &runtimeroute.ExecReader{Env: env, Dir: townRoot}
}

func runProjectRoute(w io.Writer, townRoot, project string, hard bool) error {
	cfg, err := config.LoadProjectConfig(townRoot, project)
	if err != nil {
		return fmt.Errorf("project %q: %w", project, err)
	}
	pol, perr := config.ProjectRuntimeRouting(cfg)
	if perr != nil {
		fmt.Fprintf(w, "Policy: invalid: %v\n", perr)
		return perr
	}
	if pol == nil {
		fmt.Fprintf(w, "Project %s has no runtime_routing policy; polecats use the rig's agent.\n", project)
		return nil
	}

	fmt.Fprintf(w, "Policy:  default=%s", pol.Default)
	if pol.Hard != "" {
		fmt.Fprintf(w, " hard=%s", pol.Hard)
	}
	fmt.Fprintf(w, " avoid_below_pct=%s\n", fmtPct(pol.AvoidBelowPct))
	for _, c := range pol.Candidates {
		fmt.Fprintf(w, "  candidate %s (provider %s)\n", c, pol.Provider(c))
	}

	d := runtimeroute.Decide(context.Background(), runtimeroute.Input{Policy: pol, Hard: hard}, newQuotaReader(townRoot))
	if len(d.Candidates) > 0 {
		fmt.Fprintln(w, "Quota:")
		for _, c := range d.Candidates {
			fmt.Fprintf(w, "  %-14s %s\n", c.Agent, c.Describe())
		}
	}
	if d.Warning != "" {
		fmt.Fprintf(w, "Warning: %s\n", d.Warning)
	}
	if d.Agent == "" {
		fmt.Fprintf(w, "Choice:  none (rig's normal agent), %s\n", d.Reason)
	} else {
		fmt.Fprintf(w, "Choice:  %s (%s)\n", d.Agent, d.Reason)
	}
	return nil
}

func fmtPct(v float64) string { return fmt.Sprintf("%g", v) }

// routePolecatAgent applies the project's runtime routing to a polecat start
// that has no explicit agent and returns the agent to use ("" keeps the rig's
// normal agent). It never fails: any problem is a warning and "" (or the
// explicit agent unchanged). A chosen agent that cannot be resolved falls back
// with a warning too, so routing never stops a spawn.
func routePolecatAgent(townRoot, rigName, rigPath, hookBead, explicit string) string {
	if explicit != "" {
		return explicit
	}
	project, err := config.ProjectForRig(townRoot, rigName)
	if err != nil || project == "" {
		return ""
	}
	cfg, err := config.LoadProjectConfig(townRoot, project)
	if err != nil {
		return ""
	}
	pol, perr := config.ProjectRuntimeRouting(cfg)
	if pol == nil && perr == nil {
		return ""
	}

	hard := false
	if hookBead != "" {
		if iss, err := beads.New(rigPath).Show(hookBead); err == nil && iss != nil {
			hard = beads.HasLabel(iss, runtimeroute.HardLabel)
		}
	}

	d := runtimeroute.Decide(context.Background(), runtimeroute.Input{Policy: pol, PolicyErr: perr, Hard: hard}, newQuotaReader(townRoot))
	if d.Warning != "" {
		style.PrintWarning("%s", d.Warning)
	}
	agent := d.Agent
	reason := d.Reason
	if agent != "" {
		if _, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, agent); err != nil {
			style.PrintWarning("runtime routing chose agent %q but it is not usable (%v); using the rig's agent", agent, err)
			reason = fmt.Sprintf("agent %s not configured, fell back", agent)
			agent = ""
		}
	}
	recordRouteDecision(townRoot, rigName, hookBead, project, agent, reason)
	return agent
}

// recordRouteDecision writes the decision to the town log and the audit events.
func recordRouteDecision(townRoot, rigName, hookBead, project, agent, reason string) {
	chosen := agent
	if chosen == "" {
		chosen = "(rig default)"
	}
	ctx := fmt.Sprintf("agent=%s reason=%s project=%s", chosen, reason, project)
	if hookBead != "" {
		ctx += " bead=" + hookBead
	}
	_ = townlog.NewLogger(townRoot).Log(townlog.EventRoute, rigName, ctx)
	_ = events.LogAudit(events.TypeRuntimeRoute, "gt", map[string]interface{}{
		"rig": rigName, "project": project, "bead": hookBead, "agent": agent, "reason": reason,
	})
}
