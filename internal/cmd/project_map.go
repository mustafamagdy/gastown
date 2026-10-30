package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/projectmap"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	projectMapForce bool
	projectMapRig   string
	projectMapJSON  bool
)

// maxPrimeNeighbours caps the neighbours shown in the prime service map section.
const maxPrimeNeighbours = 10

var projectMapCmd = &cobra.Command{
	Use:   "map",
	Short: "Build and show a project's service map",
	RunE:  requireSubcommand,
	Long: `The service map says which service each repo of a project owns, the APIs it
serves and calls, the events it publishes and consumes, and its database.

It is stored at projects/<project>/service-map.json (and service-map.md) and
built from, in order of precedence:
  1. projects/<project>/services/<rig>.toml   hand-written overrides
  2. service.toml or .gt/service.toml in the repo
  3. openapi.yaml|yml|json at the repo root, api/ or docs/
  4. package.json name, go.mod module or the repo name
read from each rig's default branch in its .repo.git (no checkout needed).

Agents in a project rig see only their own entry and their neighbours.`,
}

var projectMapBuildCmd = &cobra.Command{
	Use:   "build <project>",
	Short: "Build or refresh the service map",
	Long: `Scan the project's rigs and write the service map.

Rigs whose default branch has not moved since the last build are skipped
unless --force is given.

Examples:
  gt project map build shop
  gt project map build shop --force
  gt project map build shop --rig orders_svc`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		return runProjectMapBuild(cmd.OutOrStdout(), townRoot, args[0], projectMapForce, projectMapRig)
	},
}

var projectMapShowCmd = &cobra.Command{
	Use:   "show <project>",
	Short: "Show the service map",
	Long: `Show the service map of a project, or with --rig one rig's entry plus its
neighbours (the same view agents get from gt prime).

Examples:
  gt project map show shop
  gt project map show shop --rig orders_svc
  gt project map show shop --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		return runProjectMapShow(cmd.OutOrStdout(), townRoot, args[0], projectMapRig, projectMapJSON)
	},
}

func init() {
	projectCmd.AddCommand(projectMapCmd)
	projectMapCmd.AddCommand(projectMapBuildCmd)
	projectMapCmd.AddCommand(projectMapShowCmd)

	projectMapBuildCmd.Flags().BoolVar(&projectMapForce, "force", false, "Rescan rigs even when their default branch is unchanged")
	projectMapBuildCmd.Flags().StringVar(&projectMapRig, "rig", "", "Only rescan this rig")
	projectMapShowCmd.Flags().StringVar(&projectMapRig, "rig", "", "Show only this rig and its neighbours")
	projectMapShowCmd.Flags().BoolVar(&projectMapJSON, "json", false, "Output as JSON")
}

func runProjectMapBuild(w io.Writer, townRoot, project string, force bool, rig string) error {
	if _, err := config.LoadProjectConfig(townRoot, project); err != nil {
		return fmt.Errorf("project %q: %w", project, err)
	}
	m, results, err := projectmap.Build(townRoot, project, projectmap.BuildOptions{Force: force, Rig: rig})
	if err != nil {
		return err
	}
	for _, r := range results {
		switch {
		case r.Err != nil:
			fmt.Fprintf(w, "  %s %s: %v\n", style.Warning.Render("!"), r.Rig, r.Err)
		case r.Status == "scanned":
			fmt.Fprintf(w, "  %s %s scanned\n", style.Success.Render("✓"), r.Rig)
		default:
			fmt.Fprintf(w, "  %s %s %s\n", style.Dim.Render("-"), r.Rig, r.Status)
		}
	}
	fmt.Fprintf(w, "%s Service map for %s: %d services (%s)\n", style.Success.Render("✓"), project, len(m.Services), projectmap.Path(townRoot, project))
	return nil
}

func runProjectMapShow(w io.Writer, townRoot, project, rig string, asJSON bool) error {
	if _, err := config.LoadProjectConfig(townRoot, project); err != nil {
		return fmt.Errorf("project %q: %w", project, err)
	}
	m, err := projectmap.Load(townRoot, project)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no service map for %q yet; run: gt project map build %s", project, project)
		}
		return err
	}
	if rig != "" && m.Find(rig) == nil {
		return fmt.Errorf("rig %q has no entry in the service map of %q", rig, project)
	}

	if asJSON {
		out := any(m)
		if rig != "" {
			sub := *m
			sub.Services = []projectmap.Service{*m.Find(rig)}
			for _, n := range projectmap.Neighbours(m, rig) {
				sub.Services = append(sub.Services, *m.Find(n))
			}
			out = &sub
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if rig == "" {
		_, err := io.WriteString(w, projectmap.RenderMarkdown(m))
		return err
	}
	_, err = io.WriteString(w, projectmap.RenderNeighbourhood(m, rig, 1<<30, ""))
	return err
}

// outputServiceMapContext prints the agent's own service map entry and its
// neighbours after the project context. It never fails prime: a missing or
// unreadable map just yields a hint line.
func outputServiceMapContext(ctx RoleContext) {
	project, _ := config.ProjectForRig(ctx.TownRoot, ctx.Rig)
	if project == "" {
		return
	}
	fmt.Print(renderServiceMapContext(ctx.TownRoot, project, ctx.Rig))
	fmt.Print(renderNeighbourCheckouts(ctx.TownRoot, ctx.Rig, ctx.Polecat))
}

func renderServiceMapContext(townRoot, project, rig string) string {
	hint := fmt.Sprintf("gt project map show %s --rig %s", project, rig)
	m, err := projectmap.Load(townRoot, project)
	if err != nil || m.Find(rig) == nil {
		return fmt.Sprintf("\n## Service map: %s\n\nNo service map yet. Run `gt project map build %s`.\n", rig, project)
	}
	return fmt.Sprintf("\n## Service map: %s and its neighbours\n\n%s", rig, projectmap.RenderNeighbourhood(m, rig, maxPrimeNeighbours, hint))
}
