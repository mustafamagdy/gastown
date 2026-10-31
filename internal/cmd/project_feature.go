package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/projectmap"
	"github.com/steveyegge/gastown/internal/review"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	featureCreateRigs     []string
	featureCreateBranch   string
	featureTaskDesc       string
	featureTaskAfter      []string
	featureStatusJSON     bool
	featureRunDryRun      bool
	featureRunIfStarted   bool
	featureRunNoBoot      bool
	featureLandDryRun     bool
	featureRunSpawnPacing = 500 * time.Millisecond
)

var projectFeatureCmd = &cobra.Command{
	Use:   "feature",
	Short: "Multi-repo features of a project",
	RunE:  requireSubcommand,
	Long: `Manage features that span several rigs (repos) of a project.

A feature is an epic in the project beads plus a feature record
(projects/<project>/features/<epic-id>.json). The record holds one branch name
used in every rig, the base of each rig's branch and the list of rig tasks with
their order. Bead edges are not used: bd drops parent links across databases.

Task beads carry a "project_feature: <epic-id>" line. For such a bead, polecat
spawn, 'gt done' and 'gt mq submit' use the feature branch as base and MR
target, and fail instead of falling back to the default branch when the
record or the rig's branch cannot be resolved.

Workflow:
  gt project feature create xelta "Tenant credits" --rigs contracts,api,web
  gt project feature task xe-42 contracts "Add credit event schema"
  gt project feature task xe-42 api "Grant credits on signup"
  gt project feature order xe-42       # order tasks from the service map
  gt project feature run xe-42         # sling every ready task
  gt project feature status xe-42`,
}

var projectFeatureCreateCmd = &cobra.Command{
	Use:   "create <project> <title> --rigs a,b,c [--branch name]",
	Short: "Create a feature: epic, feature record and one branch per rig",
	Long: `Create a feature epic in the project beads and the same-named branch in
every listed rig.

The default branch name is feat/<epic-id>-<slug>. With --branch the name is
checked in all rigs before anything is created. Branches are cut from each
rig's default branch in its .repo.git and pushed to origin; if any rig fails,
the branches pushed so far are deleted again.`,
	Args: cobra.ExactArgs(2),
	RunE: runProjectFeatureCreate,
}

var projectFeatureAddRigCmd = &cobra.Command{
	Use:   "add-rig <feature> <rig>",
	Short: "Add a rig (and its feature branch) to a feature",
	Args:  cobra.ExactArgs(2),
	RunE:  runProjectFeatureAddRig,
}

var projectFeatureTaskCmd = &cobra.Command{
	Use:   "task <feature> <rig> <title> [-d description] [--after <task-id>]...",
	Short: "Create a task bead in a rig and record it in the feature",
	Args:  cobra.ExactArgs(3),
	RunE:  runProjectFeatureTask,
}

var projectFeatureOrderCmd = &cobra.Command{
	Use:   "order <feature>",
	Short: "Order tasks across rigs from the project's service map",
	Long: `Fill each task's 'after' list from the service map. A task in rig A waits
for all tasks of rig B when A calls B or consumes an event B publishes.
Rigs that depend on each other get no order between them (a warning is
printed). Explicit --after edges are kept.`,
	Args: cobra.ExactArgs(1),
	RunE: runProjectFeatureOrder,
}

var projectFeatureStatusCmd = &cobra.Command{
	Use:   "status <feature>",
	Short: "Show branches and task states of a feature",
	Args:  cobra.ExactArgs(1),
	RunE:  runProjectFeatureStatus,
}

var projectFeatureRunCmd = &cobra.Command{
	Use:   "run <feature>",
	Short: "Sling every ready task of a feature",
	Long: `Sling every task that is ready: bead open and all 'after' tasks closed.
Idempotent: tasks already slung are not touched. A task counts as done when
its bead is closed, which for merge queue work happens only after its MR has
merged into the feature branch.

The daemon runs this with --if-started on its heartbeat, so a feature that has
had its first task slung keeps advancing without further commands.`,
	Args: cobra.ExactArgs(1),
	RunE: runProjectFeatureRun,
}

var projectFeatureLandCmd = &cobra.Command{
	Use:   "land <feature> [--dry-run]",
	Short: "Land a feature across its rigs as a linked set",
	Long: `Land the feature branch of every rig as one set.

Preconditions (all failures are reported, nothing changes if any fails): every
task bead is closed, no open merge request targets the feature branch, and
each rig's feature branch exists on origin and is ahead of its base. A rig
with no commits ahead is skipped. Each branch is then test-merged into its
current base (git merge-tree, no refs touched); any conflict stops everything.

Rigs land in dependency order (a rig whose tasks wait for another rig's tasks
lands later; ties alphabetical). The order is stored in the record.

merge_strategy "direct": each rig is merged --no-ff in a temporary worktree,
the rig's test command runs if it has one, and the base is pushed. A failure
holds the feature and keeps what already landed; re-run to resume.

merge_strategy "pr" (default): a PR is opened in every rig and each is updated
with links to its siblings. Re-run land (the daemon does this every 5 minutes
for a feature in state "landing") to advance: nothing merges until every PR is
passing and approved, then they merge in landing order. A rejected or failing
PR holds the whole set. A PR merged outside gt counts as landed.

When every rig is landed or skipped the epic is closed, the feature branches
are deleted and the service map is refreshed. --dry-run prints the plan and
the preflight results and changes nothing.`,
	Args: cobra.ExactArgs(1),
	RunE: runProjectFeatureLand,
}

func init() {
	projectCmd.AddCommand(projectFeatureCmd)
	projectFeatureCmd.AddCommand(projectFeatureCreateCmd, projectFeatureAddRigCmd, projectFeatureTaskCmd,
		projectFeatureOrderCmd, projectFeatureStatusCmd, projectFeatureRunCmd, projectFeatureLandCmd)
	projectFeatureLandCmd.Flags().BoolVar(&featureLandDryRun, "dry-run", false, "Print the plan and preflight results; change nothing")

	projectFeatureCreateCmd.Flags().StringSliceVar(&featureCreateRigs, "rigs", nil, "Comma-separated rigs the feature spans (required)")
	projectFeatureCreateCmd.Flags().StringVar(&featureCreateBranch, "branch", "", "Branch name used in every rig (default feat/<epic-id>-<slug>)")
	_ = projectFeatureCreateCmd.MarkFlagRequired("rigs")

	projectFeatureTaskCmd.Flags().StringVarP(&featureTaskDesc, "description", "d", "", "Task description")
	projectFeatureTaskCmd.Flags().StringSliceVar(&featureTaskAfter, "after", nil, "Task id this task waits for (repeatable)")

	projectFeatureStatusCmd.Flags().BoolVar(&featureStatusJSON, "json", false, "Output as JSON")

	projectFeatureRunCmd.Flags().BoolVar(&featureRunDryRun, "dry-run", false, "Show what would be slung")
	projectFeatureRunCmd.Flags().BoolVar(&featureRunIfStarted, "if-started", false, "Do nothing unless a task has already started (used by the daemon)")
	projectFeatureRunCmd.Flags().BoolVar(&featureRunNoBoot, "no-boot", false, "Do not wake rig agents")
}

// ---- pure helpers (unit tested) ----

// featureBranchName builds the default feature branch: feat/<epic-id>-<slug>.
func featureBranchName(epicID, title string) string {
	slug := beads.SanitizeBranchSegment(title)
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return "feat/" + epicID
	}
	return "feat/" + epicID + "-" + slug
}

// parseFeatureRigs trims and de-duplicates the --rigs list.
func parseFeatureRigs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if seen[r] {
			return nil, fmt.Errorf("rig %q listed twice", r)
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--rigs needs at least one rig")
	}
	return out, nil
}

// validateFeatureRigs checks that every rig exists and belongs to the project.
func validateFeatureRigs(rigsConfig *config.RigsConfig, project string, rigs []string) error {
	for _, name := range rigs {
		entry, ok := rigsConfig.Rigs[name]
		if !ok {
			return fmt.Errorf("rig %q not found", name)
		}
		if entry.Project != project {
			if entry.Project == "" {
				return fmt.Errorf("rig %q does not belong to project %q (add it with 'gt project add-rig %s %s')", name, project, project, name)
			}
			return fmt.Errorf("rig %q belongs to project %q, not %q", name, entry.Project, project)
		}
	}
	return nil
}

// validateFeatureBranch rejects branch names that would hit protected or
// reserved branches.
func validateFeatureBranch(branch string) error {
	if err := validateBranchName(branch); err != nil {
		return err
	}
	switch branch {
	case "main", "master", "develop", "HEAD":
		return fmt.Errorf("feature branch %q is a default branch name", branch)
	}
	return nil
}

// featureRigRepo locates the git repository a rig's feature branch lives in.
func featureRigRepo(townRoot, rigName string) (projectfeature.RigRepo, error) {
	rigPath := filepath.Join(townRoot, rigName)
	def := "main"
	if cfg, err := rig.LoadRigConfig(rigPath); err == nil && cfg.DefaultBranch != "" {
		def = cfg.DefaultBranch
	}
	for _, dir := range []string{filepath.Join(rigPath, ".repo.git"), filepath.Join(rigPath, "mayor", "rig", ".git")} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return projectfeature.RigRepo{Rig: rigName, GitDir: dir, Default: def}, nil
		}
	}
	return projectfeature.RigRepo{}, fmt.Errorf("rig %s has no repo (.repo.git or mayor/rig)", rigName)
}

// featureTargetForBead resolves the branch a bead's work must use. marked is
// false for beads that do not belong to a feature (behave as before). A bead
// listed in a feature record counts as marked even if its marker line is gone
// or the bead cannot be read, so such beads never fall back silently.
func featureTargetForBead(townRoot, rigName, beadID string, issue *beads.Issue) (branch string, marked bool, err error) {
	desc := ""
	if issue != nil {
		desc = issue.Description
	}
	if !projectfeature.HasMarker(desc) {
		if rec, _, rerr := projectfeature.ResolveForBead(townRoot, beadID); rerr == nil {
			desc = projectfeature.AddMarker(desc, rec.ID)
		}
	}
	return projectfeature.ResolveTarget(townRoot, rigName, beadID, desc)
}

// featureBaseForSpawn returns the "origin/<branch>" base a polecat for a
// feature task must start from, or "" for beads outside a feature. The branch
// must exist on the rig's origin; any doubt is an error.
func featureBaseForSpawn(townRoot, rigName, rigPath, hookBead, explicitBase, resumeBranch string) (string, error) {
	if hookBead == "" || resumeBranch != "" {
		return "", nil
	}
	var issue *beads.Issue
	if iss, err := beads.New(rigPath).Show(hookBead); err == nil {
		issue = iss
	}
	branch, marked, err := featureTargetForBead(townRoot, rigName, hookBead, issue)
	if err != nil {
		return "", err
	}
	if !marked {
		return "", nil
	}
	if explicitBase != "" && strings.TrimPrefix(explicitBase, "origin/") != branch {
		return "", fmt.Errorf("bead %s belongs to a feature: its base is %s, not %s (--base-branch)", hookBead, branch, explicitBase)
	}
	repo, err := featureRigRepo(townRoot, rigName)
	if err != nil {
		return "", fmt.Errorf("feature branch %s of bead %s: %w", branch, hookBead, err)
	}
	ok, err := projectfeature.BranchExists(repo, branch)
	if err != nil {
		return "", fmt.Errorf("checking feature branch %s in rig %s: %w", branch, rigName, err)
	}
	if !ok {
		return "", fmt.Errorf("feature branch %s does not exist in rig %s; refusing to start bead %s from the default branch", branch, rigName, hookBead)
	}
	return "origin/" + branch, nil
}

// ---- bead helpers ----

func createFeatureBead(dir, title, issueType, description string) (string, error) {
	args := []string{"create", "--title=" + title, "--type=" + issueType, "--silent"}
	if description != "" {
		args = append(args, "--description="+description)
	}
	out, err := BdCmd(args...).Dir(dir).WithAutoCommit().Output()
	if err != nil {
		return "", fmt.Errorf("creating %s bead: %w", issueType, err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.ContainsAny(id, " \n\t") {
		return "", fmt.Errorf("bd create returned unexpected output %q", id)
	}
	return id, nil
}

func closeFeatureBead(dir, id, reason string) {
	if err := BdCmd("close", id, "--reason="+reason).Dir(dir).WithAutoCommit().Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not close %s: %v\n", id, err)
	}
}

// featureTaskStatuses reads the live bead status of every task.
func featureTaskStatuses(townRoot string, rec *projectfeature.Record) map[string]string {
	status := make(map[string]string, len(rec.Tasks))
	rigs := map[string]bool{}
	for _, t := range rec.Tasks {
		info, err := getBeadInfoFromTownRoot(townRoot, t.ID)
		if err != nil {
			continue // unknown
		}
		status[t.ID] = info.Status
		rigs[t.Rig] = true
	}
	// A closed bead is not merged work: gt done closes it before the refinery
	// merges. If the queue cannot be read, assume every closed task of the rig
	// is still merging, so dependents wait rather than start without its code.
	merging := map[string]bool{}
	unreadable := map[string]bool{}
	for rig := range rigs {
		mrs, err := findOpenMRsForIntegration(beads.New(filepath.Join(townRoot, rig)), rec.Branch)
		if err != nil {
			unreadable[rig] = true
			continue
		}
		for _, mr := range mrs {
			if f := beads.ParseMRFields(mr); f != nil {
				merging[f.SourceIssue] = true
			}
		}
	}
	for _, t := range rec.Tasks {
		if status[t.ID] == "closed" && (merging[t.ID] || unreadable[t.Rig]) {
			status[t.ID] = projectfeature.StatusMerging
		}
	}
	return status
}

// setupFeatureNeighbours gives a polecat on a feature task read-only checkouts
// of the feature's other rigs at the feature branch. The polecat manager
// already made default branch checkouts; it does not know the work bead.
func setupFeatureNeighbours(townRoot string, info *SpawnedPolecatInfo, hookBead string) {
	if hookBead == "" {
		return
	}
	rec, _, err := projectfeature.ResolveForBead(townRoot, hookBead)
	if err != nil || rec == nil {
		return
	}
	rigs := make([]string, 0, len(rec.Rigs))
	for rig := range rec.Rigs {
		rigs = append(rigs, rig)
	}
	sort.Strings(rigs)
	polecatDir := filepath.Join(townRoot, info.RigName, "polecats", info.PolecatName)
	polecat.SetupFeatureNeighbourCheckouts(townRoot, info.RigName, polecatDir, rec.Branch, rigs)
}

func loadFeature(townRoot, id string) (*projectfeature.Record, error) {
	rec, err := projectfeature.Find(townRoot, id)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// ---- commands ----

func runProjectFeatureCreate(cmd *cobra.Command, args []string) error {
	if err := deps.EnsureBeads(true); err != nil {
		return fmt.Errorf("beads dependency check failed: %w", err)
	}
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project, title := args[0], strings.TrimSpace(args[1])
	if title == "" {
		return fmt.Errorf("feature title is required")
	}
	if _, err := config.LoadProjectConfig(townRoot, project); err != nil {
		return fmt.Errorf("project %q: %w", project, err)
	}
	rigs, err := parseFeatureRigs(featureCreateRigs)
	if err != nil {
		return err
	}
	rigsConfig, _, err := loadProjectRigs(townRoot)
	if err != nil {
		return err
	}
	if err := validateFeatureRigs(rigsConfig, project, rigs); err != nil {
		return err
	}
	repos := make([]projectfeature.RigRepo, 0, len(rigs))
	for _, r := range rigs {
		repo, err := featureRigRepo(townRoot, r)
		if err != nil {
			return err
		}
		repos = append(repos, repo)
	}

	// With an explicit branch, check all rigs before creating anything.
	branch := featureCreateBranch
	if branch != "" {
		if err := validateFeatureBranch(branch); err != nil {
			return err
		}
		if err := projectfeature.CheckBranchFree(repos, branch); err != nil {
			return err
		}
	}

	projectDir := config.ProjectDir(townRoot, project)
	desc := fmt.Sprintf("Multi-repo feature across rigs: %s.\nTracked by gt project feature; record: projects/%s/features/.", strings.Join(rigs, ", "), project)
	epicID, err := createFeatureBead(projectDir, title, "epic", desc)
	if err != nil {
		return err
	}
	abort := func(reason string) {
		closeFeatureBead(projectDir, epicID, "Feature creation failed: "+reason)
	}

	// The default name needs the epic id; check all rigs now, before any push.
	if branch == "" {
		branch = featureBranchName(epicID, title)
		if err := validateFeatureBranch(branch); err != nil {
			abort(err.Error())
			return err
		}
		if err := projectfeature.CheckBranchFree(repos, branch); err != nil {
			abort(err.Error())
			return err
		}
	}

	results, err := projectfeature.CreateBranches(repos, branch)
	if err != nil {
		abort(err.Error())
		return fmt.Errorf("creating branches: %w", err)
	}

	rec := &projectfeature.Record{
		ID: epicID, Project: project, Title: title, Branch: branch,
		State: projectfeature.StateOpen, CreatedAt: time.Now().UTC(),
		Rigs: map[string]projectfeature.RigState{}, Tasks: []projectfeature.Task{},
	}
	for _, r := range results {
		rec.Rigs[r.Rig] = projectfeature.RigState{Base: r.Base, BaseCommit: r.BaseCommit, State: projectfeature.RigBranch}
	}
	if err := projectfeature.Create(townRoot, rec); err != nil {
		if delErr := projectfeature.DeleteBranches(repos, branch); delErr != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not delete branches after failed save: %v\n", delErr)
		}
		abort(err.Error())
		return fmt.Errorf("saving feature record: %w", err)
	}

	fmt.Printf("%s Created feature %s in project %s\n", style.Success.Render("✓"), style.Bold.Render(epicID), project)
	fmt.Printf("  Branch: %s\n", branch)
	for _, r := range results {
		fmt.Printf("  %-16s from %s @ %s\n", r.Rig, r.Base, short(r.BaseCommit))
	}
	fmt.Printf("  Add tasks with: gt project feature task %s <rig> \"<title>\"\n", epicID)
	return nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func runProjectFeatureAddRig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	rigName := args[1]
	if rec.State != projectfeature.StateOpen {
		return fmt.Errorf("feature %s is %s; rigs can only be added while it is open", rec.ID, rec.State)
	}
	if _, ok := rec.Rigs[rigName]; ok {
		return fmt.Errorf("rig %s is already part of feature %s", rigName, rec.ID)
	}
	rigsConfig, _, err := loadProjectRigs(townRoot)
	if err != nil {
		return err
	}
	if err := validateFeatureRigs(rigsConfig, rec.Project, []string{rigName}); err != nil {
		return err
	}
	repo, err := featureRigRepo(townRoot, rigName)
	if err != nil {
		return err
	}
	res, err := projectfeature.CreateBranch(repo, rec.Branch)
	if err != nil {
		return err
	}
	_, err = projectfeature.Update(townRoot, rec.Project, rec.ID, func(r *projectfeature.Record) error {
		if _, ok := r.Rigs[rigName]; ok {
			return fmt.Errorf("rig %s is already part of feature %s", rigName, r.ID)
		}
		r.Rigs[rigName] = projectfeature.RigState{Base: res.Base, BaseCommit: res.BaseCommit, State: projectfeature.RigBranch}
		return nil
	})
	if err != nil {
		if delErr := projectfeature.DeleteBranches([]projectfeature.RigRepo{repo}, rec.Branch); delErr != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not delete branch after failed save: %v\n", delErr)
		}
		return err
	}
	fmt.Printf("%s Added rig %s to feature %s (branch %s from %s @ %s)\n",
		style.Success.Render("✓"), rigName, rec.ID, rec.Branch, res.Base, short(res.BaseCommit))
	return nil
}

func runProjectFeatureTask(cmd *cobra.Command, args []string) error {
	if err := deps.EnsureBeads(true); err != nil {
		return fmt.Errorf("beads dependency check failed: %w", err)
	}
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	rigName, title := args[1], strings.TrimSpace(args[2])
	if title == "" {
		return fmt.Errorf("task title is required")
	}
	if rec.State != projectfeature.StateOpen {
		return fmt.Errorf("feature %s is %s; tasks can only be added while it is open", rec.ID, rec.State)
	}
	if _, ok := rec.Rigs[rigName]; !ok {
		return fmt.Errorf("rig %s is not part of feature %s (rigs: %s); add it with 'gt project feature add-rig %s %s'",
			rigName, rec.ID, strings.Join(featureRigNames(rec), ", "), rec.ID, rigName)
	}
	for _, a := range featureTaskAfter {
		if rec.Task(a) == nil {
			return fmt.Errorf("--after %s: no such task in feature %s", a, rec.ID)
		}
	}
	desc := projectfeature.AddMarker(featureTaskDesc, rec.ID)
	id, err := createFeatureBead(filepath.Join(townRoot, rigName), title, "task", desc)
	if err != nil {
		return err
	}
	_, err = projectfeature.Update(townRoot, rec.Project, rec.ID, func(r *projectfeature.Record) error {
		return r.AddTask(projectfeature.Task{ID: id, Rig: rigName, Title: title, After: featureTaskAfter})
	})
	if err != nil {
		return fmt.Errorf("bead %s was created but could not be recorded in feature %s: %w", id, rec.ID, err)
	}
	fmt.Printf("%s Added task %s in rig %s to feature %s\n", style.Success.Render("✓"), style.Bold.Render(id), rigName, rec.ID)
	return nil
}

func featureRigNames(rec *projectfeature.Record) []string {
	names := make([]string, 0, len(rec.Rigs))
	for r := range rec.Rigs {
		names = append(names, r)
	}
	sort.Strings(names)
	return names
}

func runProjectFeatureOrder(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	m, err := projectmap.Load(townRoot, rec.Project)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project %s has no service map yet; build it with 'gt project map build %s'", rec.Project, rec.Project)
		}
		return err
	}
	var warnings []string
	updated, err := projectfeature.Update(townRoot, rec.Project, rec.ID, func(r *projectfeature.Record) error {
		warnings = projectfeature.ApplyOrder(r, m)
		return nil
	})
	if err != nil {
		return err
	}
	for _, w := range warnings {
		style.PrintWarning("%s", w)
	}
	edges := 0
	for _, t := range updated.Tasks {
		if len(t.After) > 0 {
			fmt.Printf("  %s (%s) after %s\n", t.ID, t.Rig, strings.Join(t.After, ", "))
			edges += len(t.After)
		}
	}
	fmt.Printf("%s Ordered feature %s: %d edge(s) across %d task(s)\n", style.Success.Render("✓"), updated.ID, edges, len(updated.Tasks))
	return nil
}

// featureStatusOutput is the JSON shape of 'status'.
type featureStatusOutput struct {
	ID      string                             `json:"id"`
	Project string                             `json:"project"`
	Title   string                             `json:"title"`
	Branch  string                             `json:"branch"`
	State   string                             `json:"state"`
	Rigs    map[string]projectfeature.RigState `json:"rigs"`
	Tasks   []projectfeature.TaskView          `json:"tasks"`
	Ready   []string                           `json:"ready"`
	Counts  map[string]int                     `json:"counts"`
	// LandingOrder and Blocking describe landing; Blocking says what stops it.
	LandingOrder []string `json:"landing_order,omitempty"`
	Blocking     []string `json:"blocking,omitempty"`
	// VisualReview is the visual review state of the epic and its tasks; empty
	// when no approval review exists and the project does not require one.
	VisualReview string `json:"visual_review,omitempty"`
}

// featureBeadIDs returns the epic and task bead ids of a feature.
func featureBeadIDs(rec *projectfeature.Record) []string {
	ids := []string{rec.ID}
	for _, t := range rec.Tasks {
		ids = append(ids, t.ID)
	}
	return ids
}

// featureVisualReview describes the visual review state of a feature on one
// line, or "" when there is nothing to say.
func featureVisualReview(townRoot string, rec *projectfeature.Record, required bool) string {
	g := review.FeatureGate(townRoot, featureBeadIDs(rec))
	if !required && len(g.Reviews) == 0 {
		return ""
	}
	line := g.Message
	if !required {
		line += " (not required by the project)"
	}
	return line
}

// featureBlockers says what keeps a feature from landing, from the record and
// the live task states alone (no network).
func featureBlockers(rec *projectfeature.Record, views []projectfeature.TaskView) []string {
	var out []string
	if rec.State == projectfeature.StateLanded {
		return nil
	}
	notDone := 0
	for _, v := range views {
		if v.State != projectfeature.TaskDone {
			notDone++
		}
	}
	if notDone > 0 {
		out = append(out, fmt.Sprintf("%d task(s) not closed", notDone))
	}
	for _, name := range featureRigNames(rec) {
		rs := rec.Rigs[name]
		switch {
		case rs.State == projectfeature.RigHeld:
			out = append(out, fmt.Sprintf("rig %s held: %s", name, rs.Error))
		case rs.State == projectfeature.RigPROpen:
			out = append(out, fmt.Sprintf("rig %s: PR waits for checks or review (%s)", name, rs.PRURL))
		}
	}
	if rec.State == projectfeature.StateHeld {
		out = append(out, "feature is held; fix the cause and re-run 'gt project feature land "+rec.ID+"'")
	}
	return out
}

func runProjectFeatureStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	views := projectfeature.Classify(rec, featureTaskStatuses(townRoot, rec))
	out := featureStatusOutput{
		ID: rec.ID, Project: rec.Project, Title: rec.Title, Branch: rec.Branch, State: rec.State,
		Rigs: rec.Rigs, Tasks: views, Ready: []string{}, Counts: map[string]int{},
	}
	for _, v := range views {
		out.Counts[v.State]++
	}
	for _, v := range projectfeature.ReadyTasks(views) {
		out.Ready = append(out.Ready, v.ID)
	}
	out.LandingOrder = rec.LandingOrder
	out.Blocking = featureBlockers(rec, views)
	requireVisual := false
	if pcfg, err := config.LoadProjectConfig(townRoot, rec.Project); err == nil {
		requireVisual = config.ProjectLandSettings(pcfg).RequireVisualReview
	}
	out.VisualReview = featureVisualReview(townRoot, rec, requireVisual)
	if featureStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s  %s\n", style.Bold.Render(rec.ID), rec.Title)
	fmt.Printf("  Project: %s   State: %s   Branch: %s\n\n", rec.Project, rec.State, rec.Branch)
	fmt.Println("Rigs:")
	for _, name := range featureRigNames(rec) {
		rs := rec.Rigs[name]
		line := fmt.Sprintf("  %-16s %-8s from %s @ %s", name, rs.State, rs.Base, short(rs.BaseCommit))
		if rs.PRURL != "" {
			line += "  " + rs.PRURL
		}
		if rs.LandedCommit != "" {
			line += "  landed " + short(rs.LandedCommit)
		}
		if rs.Error != "" {
			line += "  error: " + rs.Error
		}
		fmt.Println(line)
	}
	if len(rec.LandingOrder) > 0 {
		fmt.Printf("\nLanding order: %s\n", strings.Join(rec.LandingOrder, ", "))
	}
	if out.VisualReview != "" {
		fmt.Printf("Visual review: %s\n", out.VisualReview)
	}
	if len(out.Blocking) > 0 {
		fmt.Println("Blocking:")
		for _, b := range out.Blocking {
			fmt.Println("  - " + b)
		}
	}
	fmt.Println("\nTasks:")
	if len(views) == 0 {
		fmt.Println("  (none)")
	}
	for _, v := range views {
		status := v.Status
		if status == "" {
			status = "?"
		}
		line := fmt.Sprintf("  %-12s %-16s %-8s %-11s %s", v.ID, v.Rig, v.State, status, v.Title)
		if len(v.BlockedBy) > 0 {
			line += "  (waiting for " + strings.Join(v.BlockedBy, ", ") + ")"
		}
		fmt.Println(line)
	}
	fmt.Printf("\n%d ready, %d running, %d merging, %d blocked, %d done\n",
		out.Counts[projectfeature.TaskReady], out.Counts[projectfeature.TaskRunning],
		out.Counts[projectfeature.TaskMerging], out.Counts[projectfeature.TaskBlocked],
		out.Counts[projectfeature.TaskDone])
	return nil
}

func runProjectFeatureRun(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	if rec.State != projectfeature.StateOpen {
		if featureRunIfStarted {
			return nil
		}
		return fmt.Errorf("feature %s is %s; only open features run tasks", rec.ID, rec.State)
	}
	views := projectfeature.Classify(rec, featureTaskStatuses(townRoot, rec))
	if featureRunIfStarted && !projectfeature.Started(views) {
		return nil
	}
	ready := projectfeature.ReadyTasks(views)
	if len(ready) == 0 {
		if !featureRunIfStarted {
			fmt.Printf("No ready tasks in feature %s.\n", rec.ID)
		}
		return nil
	}

	rigsConfig, _, err := loadProjectRigs(townRoot)
	if err != nil {
		return err
	}
	if featureRunDryRun {
		for _, v := range ready {
			fmt.Printf("Would sling %s (%s) to rig %s, base %s\n", v.ID, v.Title, v.Rig, rec.Branch)
		}
		return nil
	}

	ok, failed := 0, 0
	slung := map[string]bool{}
	for i, v := range ready {
		if err := validateFeatureRigs(rigsConfig, rec.Project, []string{v.Rig}); err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), v.ID, err)
			failed++
			continue
		}
		fmt.Printf("[%d/%d] Slinging %s → %s (base %s)...\n", i+1, len(ready), v.ID, v.Rig, rec.Branch)
		_, err := executeSling(SlingParams{
			BeadID:        v.ID,
			RigName:       v.Rig,
			FormulaName:   resolveFormula("", false, townRoot, v.Rig),
			BaseBranch:    rec.Branch,
			NoConvoy:      true, // the feature epic is the organizing structure
			NoBoot:        true,
			CallerContext: "project-feature",
			TownRoot:      townRoot,
			BeadsDir:      filepath.Join(townRoot, ".beads"),
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), v.ID, err)
			failed++
			continue
		}
		ok++
		slung[v.Rig] = true
		if i < len(ready)-1 {
			time.Sleep(featureRunSpawnPacing)
		}
	}
	if !featureRunNoBoot {
		for r := range slung {
			wakeRigAgents(r)
		}
	}
	fmt.Printf("%s Slung %d/%d ready task(s) of feature %s\n", style.Bold.Render("📊"), ok, len(ready), rec.ID)
	if ok == 0 && failed > 0 {
		return fmt.Errorf("all %d dispatch attempts failed for feature %s", failed, rec.ID)
	}
	return nil
}

func runProjectFeatureLand(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rec, err := loadFeature(townRoot, args[0])
	if err != nil {
		return err
	}
	pcfg, err := config.LoadProjectConfig(townRoot, rec.Project)
	if err != nil {
		return fmt.Errorf("project %q: %w", rec.Project, err)
	}
	repos := map[string]projectfeature.RigRepo{}
	for name := range rec.Rigs {
		repo, err := featureRigRepo(townRoot, name)
		if err != nil {
			return err
		}
		repos[name] = repo
	}
	projectDir := config.ProjectDir(townRoot, rec.Project)
	env := &projectfeature.LandEnv{
		TownRoot:   townRoot,
		Rec:        rec,
		Settings:   config.ProjectLandSettings(pcfg),
		Repos:      repos,
		TaskStatus: func() map[string]string { return featureTaskStatuses(townRoot, rec) },
		OpenMRs: func(rig, branch string) ([]string, error) {
			mrs, err := findOpenMRsForIntegration(beads.New(filepath.Join(townRoot, rig)), branch)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(mrs))
			for _, mr := range mrs {
				ids = append(ids, mr.ID)
			}
			return ids, nil
		},
		TestCommand: func(rig string) string { return getTestCommand(filepath.Join(townRoot, rig)) },
		CloseEpic: func(reason string) error {
			return BdCmd("close", rec.ID, "--reason="+reason).Dir(projectDir).WithAutoCommit().Run()
		},
		OnLanded: func() error {
			_, _, err := projectmap.Build(townRoot, rec.Project, projectmap.BuildOptions{})
			return err
		},
		VisualGate: func() (bool, string) {
			g := review.FeatureGate(townRoot, featureBeadIDs(rec))
			return g.OK(), g.Message
		},
		Out:    os.Stdout,
		DryRun: featureLandDryRun,
	}
	report, err := projectfeature.Land(context.Background(), env)
	if err != nil {
		return err
	}
	if report.Outcome == projectfeature.OutcomeHeld {
		return fmt.Errorf("feature %s is held; fix the cause and re-run 'gt project feature land %s'", rec.ID, rec.ID)
	}
	return nil
}
