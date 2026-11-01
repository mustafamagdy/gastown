package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/gastown/internal/axictx"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

// collectAxiContext is a seam for tests.
var collectAxiContext = axictx.Collect

// outputAxiContext prints the session-start context of the town's AXI tools
// (axi_tools), one block per tool. It never fails: a tool that does not answer
// prints one dim line, and a town with no axi_tools prints and runs nothing.
func outputAxiContext(ctx RoleContext) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(ctx.TownRoot))
	if err != nil || len(settings.AxiTools) == 0 {
		return
	}
	var tools []string
	for _, entry := range settings.AxiTools {
		if name, _ := config.ParseAxiTool(entry); axictx.HasSessionView(name) {
			tools = append(tools, entry)
		}
	}
	if len(tools) == 0 {
		return
	}
	if primeDryRun {
		explain(true, fmt.Sprintf("AXI context: would run the session view of %v (skipped in dry-run)", tools))
		return
	}

	env := os.Environ()
	var orgVars map[string]string
	if org := config.OrgEnvForTown(ctx.TownRoot); org != nil {
		env = org.Environ(env)
		orgVars = org.Vars
	}
	results := collectAxiContext(context.Background(), axictx.Options{
		TownRoot: ctx.TownRoot,
		WorkDir:  ctx.WorkDir,
		OrgVars:  orgVars,
		Tools:    tools,
		Env:      env,
	})
	for _, r := range results {
		if r.Err != nil {
			fmt.Println()
			fmt.Println(style.Dim.Render(fmt.Sprintf("AXI context: %s unavailable (%v)", r.Tool, r.Err)))
			continue
		}
		if r.Text == "" {
			continue
		}
		fmt.Println()
		fmt.Printf("## AXI context: %s\n\n", r.Tool)
		fmt.Println(r.Text)
	}
}
