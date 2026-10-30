package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	projectCreatePrefix      string
	projectCreateDocs        string
	projectCreateMemory      string
	projectCreateDescription string
	projectAddRigMove        bool
	projectListJSON          bool
	projectShowJSON          bool
)

var projectCmd = &cobra.Command{
	Use:     "project",
	GroupID: GroupWorkspace,
	Short:   "Manage projects (named groups of rigs)",
	RunE:    requireSubcommand,
	Long: `Manage projects in the Gas Town workspace.

A project is a named group of rigs (git repos) that belong together. It has:
  - projects/<name>/project.json   Project config (prefix, docs, memory)
  - projects/<name>/CONTEXT.md     Shared context injected by 'gt prime'
  - projects/<name>/directives/    Project-level role directives
  - projects/<name>/.beads/        Project beads (features spanning rigs)

Rigs join a project with 'gt project add-rig'. Agents in those rigs get
GT_PROJECT in their environment and the project context when they prime.`,
}

var projectCreateCmd = &cobra.Command{
	Use:   "create <name> --prefix <prefix>",
	Short: "Create a new project",
	Long: `Create a new project with its own beads database.

This creates projects/<name>/ with a project.json, a starter CONTEXT.md and a
beads database using the given prefix, and registers the prefix in the town
routes. The prefix must not collide with any existing rig or town prefix.

Requires the Dolt server to be running (beads init needs it).

Examples:
  gt project create xelta --prefix xe
  gt project create xelta --prefix xe --docs ~/work/mbg/xelta/docs --memory mbg/xelta`,
	Args: cobra.ExactArgs(1),
	RunE: runProjectCreate,
}

var projectAddRigCmd = &cobra.Command{
	Use:   "add-rig <project> <rig>...",
	Short: "Add rigs to a project",
	Long: `Add existing rigs to a project.

A rig can belong to one project. Use --move to take a rig out of its current
project and add it to this one.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runProjectAddRig,
}

var projectRemoveRigCmd = &cobra.Command{
	Use:   "remove-rig <project> <rig>...",
	Short: "Remove rigs from a project",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runProjectRemoveRig,
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects",
	Long: `List projects in the workspace with their prefix and rig count.

Examples:
  gt project list
  gt project list --json   # Output as JSON for scripting`,
	Args: cobra.NoArgs,
	RunE: runProjectList,
}

var projectShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a project's config and rigs",
	Args:  cobra.ExactArgs(1),
	RunE:  runProjectShow,
}

func init() {
	rootCmd.AddCommand(projectCmd)
	projectCmd.AddCommand(projectCreateCmd)
	projectCmd.AddCommand(projectAddRigCmd)
	projectCmd.AddCommand(projectRemoveRigCmd)
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectShowCmd)

	projectCreateCmd.Flags().StringVar(&projectCreatePrefix, "prefix", "", "Project beads issue prefix (required)")
	projectCreateCmd.Flags().StringVar(&projectCreateDocs, "docs", "", "Path to the project docs folder (may use ~)")
	projectCreateCmd.Flags().StringVar(&projectCreateMemory, "memory", "", "Agent-brain memory key (e.g. mbg/xelta)")
	projectCreateCmd.Flags().StringVar(&projectCreateDescription, "description", "", "Short project description")
	_ = projectCreateCmd.MarkFlagRequired("prefix")

	projectAddRigCmd.Flags().BoolVar(&projectAddRigMove, "move", false, "Move rigs that already belong to another project")

	projectListCmd.Flags().BoolVar(&projectListJSON, "json", false, "Output as JSON")
	projectShowCmd.Flags().BoolVar(&projectShowJSON, "json", false, "Output as JSON")
}

// projectInfo is the JSON shape of a project in list and show output.
type projectInfo struct {
	config.ProjectConfig
	Rigs []string `json:"rigs"`
}

func loadProjectRigs(townRoot string) (*config.RigsConfig, string, error) {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return nil, rigsPath, fmt.Errorf("loading rigs config: %w", err)
	}
	return rigsConfig, rigsPath, nil
}

func runProjectCreate(cmd *cobra.Command, args []string) error {
	if err := deps.EnsureBeads(true); err != nil {
		return fmt.Errorf("beads dependency check failed: %w", err)
	}
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cfg := &config.ProjectConfig{
		Name:        args[0],
		Prefix:      projectCreatePrefix,
		Docs:        projectCreateDocs,
		Memory:      projectCreateMemory,
		Description: projectCreateDescription,
	}
	if err := createProject(townRoot, cfg, false); err != nil {
		return err
	}

	commitProjectChanges(townRoot, cfg.Name, "register project")
	fmt.Printf("%s Created project %s (prefix %s-)\n", style.Success.Render("✓"), style.Bold.Render(cfg.Name), cfg.Prefix)
	fmt.Printf("  Add rigs with: gt project add-rig %s <rig>...\n", cfg.Name)
	return nil
}

// createProject validates cfg, initializes the project beads database, registers
// the route and writes project.json plus a starter CONTEXT.md. skipDoltCheck
// skips the Dolt reachability check (tests only). On failure after the project
// directory was created, the directory is removed again.
func createProject(townRoot string, cfg *config.ProjectConfig, skipDoltCheck bool) error {
	if err := config.ValidateProjectName(cfg.Name); err != nil {
		return err
	}
	cfg.Prefix = strings.TrimSuffix(cfg.Prefix, "-")
	cfg.Type = "project"
	cfg.Version = config.CurrentProjectVersion

	projectDir := config.ProjectDir(townRoot, cfg.Name)
	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("project %q already exists", cfg.Name)
	}

	// A project's beads database is named after it, so it must not collide with a rig.
	rigsConfig, err := config.LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
	if err != nil {
		rigsConfig = &config.RigsConfig{Version: 1, Rigs: make(map[string]config.RigEntry)}
	}
	if _, exists := rigsConfig.Rigs[cfg.Name]; exists {
		return fmt.Errorf("a rig named %q already exists; choose a different project name", cfg.Name)
	}

	// Prefix must be unique across all routes (rigs and town). CheckPrefixAvailable
	// compares by first path component, which is "projects" for every project,
	// so check the routes directly.
	routes, err := beads.LoadRoutes(filepath.Join(townRoot, ".beads"))
	if err != nil {
		return fmt.Errorf("loading routes: %w", err)
	}
	for _, r := range routes {
		if strings.EqualFold(r.Prefix, cfg.Prefix+"-") {
			return fmt.Errorf("prefix %q is already used by path %q; use --prefix to specify a different prefix", cfg.Prefix, r.Path)
		}
	}

	// Validate everything else (prefix format) before touching the filesystem.
	if err := config.ValidateProjectConfig(cfg); err != nil {
		return err
	}

	if !skipDoltCheck {
		if running, _, err := doltserver.IsRunning(townRoot); err != nil {
			return fmt.Errorf("checking Dolt server: %w", err)
		} else if !running {
			return fmt.Errorf("Dolt server is not running (required for beads init); start it with 'gt up' or 'gt dolt start'")
		}
	}

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(projectDir) }

	mgr := rig.NewManager(townRoot, rigsConfig, git.NewGit(townRoot))
	if err := mgr.InitBeads(projectDir, cfg.Prefix, cfg.Name); err != nil {
		cleanup()
		return fmt.Errorf("initializing project beads: %w", err)
	}
	route := beads.Route{Prefix: cfg.Prefix + "-", Path: filepath.ToSlash(filepath.Join("projects", cfg.Name))}
	if err := beads.AppendRoute(townRoot, route); err != nil {
		cleanup()
		return fmt.Errorf("registering route: %w", err)
	}
	if err := config.SaveProjectConfig(townRoot, cfg); err != nil {
		cleanup()
		_ = beads.RemoveRoute(townRoot, route.Prefix)
		return err
	}
	if err := writeProjectContextIfMissing(townRoot, cfg); err != nil {
		return err
	}
	return nil
}

// writeProjectContextIfMissing writes a starter CONTEXT.md for the project.
// An existing CONTEXT.md is never overwritten.
func writeProjectContextIfMissing(townRoot string, cfg *config.ProjectConfig) error {
	path := config.ProjectContextPath(townRoot, cfg.Name)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Project: %s\n\n", cfg.Name)
	if cfg.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", cfg.Description)
	}
	fmt.Fprintf(&b, "- Beads prefix: `%s-` (features that span repos)\n", cfg.Prefix)
	if cfg.Docs != "" {
		fmt.Fprintf(&b, "- Docs: %s\n", cfg.Docs)
	}
	if cfg.Memory != "" {
		fmt.Fprintf(&b, "- Memory (agent-brain): %s\n", cfg.Memory)
	}
	b.WriteString("\n## Rigs\n\n<!-- List the repos in this project and what each one does. -->\n")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("writing CONTEXT.md: %w", err)
	}
	return nil
}

// assignRigsToProject sets Project on each named rig. It validates all rigs
// before changing any, so a failure leaves rigsConfig untouched.
func assignRigsToProject(rigsConfig *config.RigsConfig, project string, rigNames []string, move bool) error {
	for _, name := range rigNames {
		entry, ok := rigsConfig.Rigs[name]
		if !ok {
			return fmt.Errorf("rig %q not found", name)
		}
		if entry.Project != "" && entry.Project != project && !move {
			return fmt.Errorf("rig %q already belongs to project %q; use --move to move it", name, entry.Project)
		}
	}
	for _, name := range rigNames {
		entry := rigsConfig.Rigs[name]
		entry.Project = project
		rigsConfig.Rigs[name] = entry
	}
	return nil
}

// unassignRigsFromProject clears Project on each named rig, which must
// currently belong to the project. It validates all rigs before changing any.
func unassignRigsFromProject(rigsConfig *config.RigsConfig, project string, rigNames []string) error {
	for _, name := range rigNames {
		entry, ok := rigsConfig.Rigs[name]
		if !ok {
			return fmt.Errorf("rig %q not found", name)
		}
		if entry.Project != project {
			return fmt.Errorf("rig %q does not belong to project %q", name, project)
		}
	}
	for _, name := range rigNames {
		entry := rigsConfig.Rigs[name]
		entry.Project = ""
		rigsConfig.Rigs[name] = entry
	}
	return nil
}

func runProjectAddRig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project, rigNames := args[0], args[1:]
	if _, err := config.LoadProjectConfig(townRoot, project); err != nil {
		return fmt.Errorf("project %q: %w", project, err)
	}
	rigsConfig, rigsPath, err := loadProjectRigs(townRoot)
	if err != nil {
		return err
	}
	if err := assignRigsToProject(rigsConfig, project, rigNames, projectAddRigMove); err != nil {
		return err
	}
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}
	commitProjectChanges(townRoot, project, "add rigs to project")
	fmt.Printf("%s Added %s to project %s\n", style.Success.Render("✓"), strings.Join(rigNames, ", "), style.Bold.Render(project))
	return nil
}

func runProjectRemoveRig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project, rigNames := args[0], args[1:]
	rigsConfig, rigsPath, err := loadProjectRigs(townRoot)
	if err != nil {
		return err
	}
	if err := unassignRigsFromProject(rigsConfig, project, rigNames); err != nil {
		return err
	}
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}
	commitProjectChanges(townRoot, project, "remove rigs from project")
	fmt.Printf("%s Removed %s from project %s\n", style.Success.Render("✓"), strings.Join(rigNames, ", "), style.Bold.Render(project))
	return nil
}

func runProjectList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	names, err := config.ListProjects(townRoot)
	if err != nil {
		return err
	}
	rigsConfig, _, rigsErr := loadProjectRigs(townRoot)

	infos := make([]projectInfo, 0, len(names))
	for _, name := range names {
		cfg, err := config.LoadProjectConfig(townRoot, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping project %s: %v\n", name, err)
			continue
		}
		info := projectInfo{ProjectConfig: *cfg, Rigs: []string{}}
		if rigsErr == nil {
			info.Rigs = append(info.Rigs, config.RigsInProject(rigsConfig, name)...)
		}
		infos = append(infos, info)
	}

	if projectListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(infos)
	}

	if len(infos) == 0 {
		fmt.Println("No projects. Create one with: gt project create <name> --prefix <prefix>")
		return nil
	}
	fmt.Printf("Projects in %s:\n\n", townRoot)
	for _, info := range infos {
		fmt.Printf("  %s  prefix=%s-  rigs=%d\n", style.Bold.Render(info.Name), info.Prefix, len(info.Rigs))
	}
	return nil
}

func runProjectShow(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	cfg, err := config.LoadProjectConfig(townRoot, args[0])
	if err != nil {
		return fmt.Errorf("project %q: %w", args[0], err)
	}
	info := projectInfo{ProjectConfig: *cfg, Rigs: []string{}}
	if rigsConfig, _, err := loadProjectRigs(townRoot); err == nil {
		info.Rigs = append(info.Rigs, config.RigsInProject(rigsConfig, cfg.Name)...)
	}

	if projectShowJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	fmt.Printf("%s\n", style.Bold.Render(info.Name))
	if info.Description != "" {
		fmt.Printf("  Description: %s\n", info.Description)
	}
	fmt.Printf("  Prefix:      %s-\n", info.Prefix)
	if info.Docs != "" {
		fmt.Printf("  Docs:        %s\n", info.Docs)
	}
	if info.Memory != "" {
		fmt.Printf("  Memory:      %s\n", info.Memory)
	}
	fmt.Printf("  Directory:   %s\n", config.ProjectDir(townRoot, info.Name))
	fmt.Printf("  Rigs (%d):\n", len(info.Rigs))
	for _, r := range info.Rigs {
		fmt.Printf("    %s\n", r)
	}
	return nil
}

// commitProjectChanges commits the town-level files touched by project
// commands (rigs.json, routes.jsonl, the project's config and context) to the
// town repo, like commitTownConfigChanges does for rig add.
func commitProjectChanges(townRoot, project, action string) {
	g := git.NewGit(townRoot)

	files := []string{
		filepath.Join("mayor", "rigs.json"),
		filepath.Join(".beads", "routes.jsonl"),
		filepath.Join("projects", project, "project.json"),
		filepath.Join("projects", project, "CONTEXT.md"),
	}
	// Stage each file on its own so one ignored or missing file does not
	// block the rest.
	staged := 0
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(townRoot, f)); err != nil {
			continue
		}
		if err := g.Add(f); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not stage %s: %v\n", f, err)
			continue
		}
		staged++
	}
	if staged == 0 {
		return
	}

	msg := fmt.Sprintf("chore: %s %s in town config", action, project)
	if err := g.Commit(msg); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "  Warning: could not commit town config files: %v\n", err)
		}
	}
}
