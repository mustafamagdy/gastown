package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/workspace"
)

var orgEnvJSON bool

var orgCmd = &cobra.Command{
	Use:     "org",
	GroupID: GroupConfig,
	Short:   "Inspect the org this town belongs to",
	RunE:    requireSubcommand,
	Long: `Inspect the org this town belongs to.

A town is one org. Its settings (settings/config.json) can declare an org
environment (org_env) that every agent session of the town starts with, and
the AXI tools (axi_tools) its agents use.`,
}

var orgEnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Show the resolved org environment (names only)",
	Long: `Resolve the town's org_env setting and print its source and the variable
names it provides. Values are never printed.

Examples:
  gt org env
  gt org env --json`,
	Args: cobra.NoArgs,
	RunE: runOrgEnv,
}

func init() {
	rootCmd.AddCommand(orgCmd)
	orgCmd.AddCommand(orgEnvCmd)
	orgEnvCmd.Flags().BoolVar(&orgEnvJSON, "json", false, "Output as JSON")
}

type orgEnvInfo struct {
	Configured bool     `json:"configured"`
	Source     string   `json:"source,omitempty"`
	Count      int      `json:"count"`
	Vars       []string `json:"vars"`
	Error      string   `json:"error,omitempty"`
}

func runOrgEnv(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	info := orgEnvInfo{Vars: []string{}}
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}
	info.Configured = settings.OrgEnv != nil
	var resolveErr error
	if info.Configured {
		var env *config.OrgEnv
		env, resolveErr = config.ResolveOrgEnvConfig(settings.OrgEnv, os.Environ())
		if resolveErr != nil {
			info.Error = resolveErr.Error()
		} else {
			info.Source = env.Source
			info.Vars = env.Names()
			info.Count = len(info.Vars)
		}
	}

	if orgEnvJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			return err
		}
		return resolveErr
	}

	if !info.Configured {
		fmt.Println("No org_env configured in settings/config.json")
		return nil
	}
	if resolveErr != nil {
		return fmt.Errorf("resolving org_env: %w", resolveErr)
	}
	fmt.Printf("Source: %s\n", info.Source)
	fmt.Printf("Variables: %d\n", info.Count)
	for _, n := range info.Vars {
		fmt.Printf("  %s\n", n)
	}
	return nil
}
