package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

const (
	maxPrimeMemoryLines = 40
	maxPrimeDocFiles    = 15
)

// agentBrainDir returns the agent-brain checkout: $AGENT_BRAIN_DIR or ~/.agent-brain.
func agentBrainDir() string {
	if d := os.Getenv("AGENT_BRAIN_DIR"); d != "" {
		return util.ExpandHome(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agent-brain")
}

// projectMemoryDir returns memory/<key> inside agent-brain, "" for an unusable key.
func projectMemoryDir(key string) string {
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" || strings.Contains(key, "..") {
		return ""
	}
	base := agentBrainDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "memory", filepath.FromSlash(key))
}

// outputProjectBrainContext prints the project memory index and docs listing
// for agents whose rig belongs to a project. It never fails prime.
func outputProjectBrainContext(ctx RoleContext) {
	project, _ := config.ProjectForRig(ctx.TownRoot, ctx.Rig)
	if project == "" {
		return
	}
	cfg, err := config.LoadProjectConfig(ctx.TownRoot, project)
	if err != nil {
		return
	}
	fmt.Print(renderProjectBrain(cfg))
}

func renderProjectBrain(cfg *config.ProjectConfig) string {
	var b strings.Builder
	if dir := projectMemoryDir(cfg.Memory); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			fmt.Fprintf(&b, "\n## Project memory (%s)\n\n", strings.Trim(cfg.Memory, "/"))
			var lines []string
			if data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md")); err == nil { //nolint:gosec // G304: path from trusted config
				for _, l := range strings.Split(string(data), "\n") {
					if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "#") {
						lines = append(lines, l)
					}
				}
			}
			if len(lines) == 0 {
				b.WriteString("(no entries yet)\n")
			}
			for i, l := range lines {
				if i >= maxPrimeMemoryLines {
					fmt.Fprintf(&b, "- and %d more in %s\n", len(lines)-maxPrimeMemoryLines, dir)
					break
				}
				b.WriteString(l + "\n")
			}
			fmt.Fprintf(&b, "\nRead the linked files from %s when relevant.\n", dir)
			b.WriteString("Save durable project facts there in the same format (one fact per file plus an index line in MEMORY.md), never secrets.\n")
		}
	}
	if cfg.Docs != "" {
		docs := util.ExpandHome(cfg.Docs)
		if entries, err := os.ReadDir(docs); err == nil {
			var names []string
			for _, e := range entries {
				if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			if len(names) > 0 {
				more := ""
				if len(names) > maxPrimeDocFiles {
					more = fmt.Sprintf(", and %d more", len(names)-maxPrimeDocFiles)
					names = names[:maxPrimeDocFiles]
				}
				fmt.Fprintf(&b, "\nProject docs: %s\n%s%s\n", docs, strings.Join(names, ", "), more)
			}
		}
	}
	return b.String()
}

// renderNeighbourCheckouts lists a polecat's read-only neighbour checkouts
// relative to its worktree (polecats/<name>/<rig>/ -> ../neighbours/<rig>).
func renderNeighbourCheckouts(townRoot, rig, polecatName string) string {
	if polecatName == "" {
		return ""
	}
	polecatDir := filepath.Join(townRoot, rig, "polecats", polecatName)
	checkouts := polecat.ListNeighbourCheckouts(polecatDir)
	if len(checkouts) == 0 {
		return ""
	}
	clone := filepath.Join(polecatDir, rig)
	var b strings.Builder
	b.WriteString("\n")
	for _, c := range checkouts {
		rel, err := filepath.Rel(clone, c.Path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "Read-only code: %s (%s)\n", filepath.ToSlash(rel), c.Commit)
	}
	if branch := polecat.NeighbourFeatureBranch(polecatDir); branch != "" {
		fmt.Fprintf(&b, "Rigs of your feature are at branch %s, with work sibling tasks have merged; other rigs are at their default branch. Read them to match contracts and call sites; do not edit them.\n", branch)
	} else {
		b.WriteString("These are read-only snapshots of each neighbour's default branch for reading contracts and call sites, not for edits.\n")
	}
	return b.String()
}

var projectBrainRulesCmd = &cobra.Command{
	Use:   "brain-rules <project>",
	Short: "Print agent-brain folder rules for a project's rigs",
	Long: `Print agent-brain folder rules, one TOML inline table per line, mapping every
rig path of the project to the project's memory key (org/project).

Paste the lines into config/brain.toml of agent-brain under the machine's
"rules". Nothing is modified.

Example:
  gt project brain-rules shop`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		out, err := renderBrainRules(townRoot, args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), out)
		return err
	},
}

func init() {
	projectCmd.AddCommand(projectBrainRulesCmd)
}

func renderBrainRules(townRoot, project string) (string, error) {
	cfg, err := config.LoadProjectConfig(townRoot, project)
	if err != nil {
		return "", fmt.Errorf("project %q: %w", project, err)
	}
	parts := strings.Split(strings.Trim(strings.TrimSpace(cfg.Memory), "/"), "/")
	if cfg.Memory == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("project %q has no memory key of the form org/project (set it with gt project create --memory)", project)
	}
	rigsConfig, _, err := loadProjectRigs(townRoot)
	if err != nil {
		return "", err
	}
	path := townRoot
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, townRoot); err == nil && !strings.HasPrefix(rel, "..") {
			path = "~"
			if rel != "." {
				path = "~/" + filepath.ToSlash(rel)
			}
		}
	}
	var b strings.Builder
	for _, rig := range config.RigsInProject(rigsConfig, project) {
		fmt.Fprintf(&b, "{ path = %q, org = %q, project = %q },\n", path+"/"+rig, parts[0], parts[1])
	}
	b.WriteString("# Rules match by path: put more specific rules before generic ones (for example before a rule covering the whole town).\n")
	return b.String(), nil
}
