package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/tracker"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	trackerImportRigs   []string
	trackerImportDryRun bool
	trackerSyncDryRun   bool
)

// trackerTimeout bounds one tracker command (az and gh calls included).
const trackerTimeout = 3 * time.Minute

// newProjectTracker builds a project's tracker. Tests replace it with one
// backed by a fake runner.
var newProjectTracker = func(cfg *config.TrackerConfig) (tracker.Tracker, error) {
	return tracker.New(cfg, nil)
}

var projectTrackerCmd = &cobra.Command{
	Use:   "tracker",
	Short: "Planning sync with Azure Boards or GitHub Issues",
	RunE:  requireSubcommand,
	Long: `Import ready items from the project's planning tracker as features and
write status back.

Configure the tracker in projects/<project>/project.json:

  "tracker": {"kind":"azure","org":"myorg","project":"myproj","ready_tag":"gt-ready",
              "active_state":"Active","done_state":"Resolved"}
  "tracker": {"kind":"github","repo":"owner/name","ready_label":"gt-ready"}

Without a tracker nothing changes. Azure authentication is az's own store
('gt azure login'); GitHub uses gh's.

  gt project tracker list xelta
  gt project tracker import xelta 1234 --rigs api,web
  gt project tracker sync xelta --dry-run

Write back is forward only and touches only imported items: Azure state, or the
gt-active label and open/closed on GitHub. gt posts no comments, edits no
fields and creates or deletes no items.`,
}

var projectTrackerListCmd = &cobra.Command{
	Use:   "list <project>",
	Short: "List ready items in the tracker",
	Args:  cobra.ExactArgs(1),
	RunE:  runProjectTrackerList,
}

var projectTrackerImportCmd = &cobra.Command{
	Use:   "import <project> <item-id> --rigs a,b [--dry-run]",
	Short: "Import a tracker item as a feature with tasks",
	Long: `Import a ready item as a project feature, through the same code as
'gt project feature create' and 'gt project feature task'.

The item's description becomes the epic description. Each child becomes a task:
with one rig all children go there; with several rigs a child needs a tag
(Azure) or label (GitHub) "rig:<name>". Children without one are listed as
unassigned and not created. Children already done in the tracker are skipped.
An item without children creates the feature and no tasks.

Importing an item again prints the existing feature and adds only children that
are new. Existing tasks are never changed or deleted.`,
	Args: cobra.ExactArgs(2),
	RunE: runProjectTrackerImport,
}

var projectTrackerSyncCmd = &cobra.Command{
	Use:   "sync <project>",
	Short: "Write feature status back to the tracker",
	Long: `Reconcile the tracker with the project's features. For each feature
imported from the tracker, the desired state is done when the feature has
landed, active when a task is in progress or hooked or a landing started, and
nothing otherwise. The daemon runs this for you.

Forward only: an item already at its done state, or moved by a human to a state
other than the one it had at import or the active state, is left alone. Use
--dry-run to print what would change.`,
	Args: cobra.ExactArgs(1),
	RunE: runProjectTrackerSync,
}

func init() {
	projectCmd.AddCommand(projectTrackerCmd)
	projectTrackerCmd.AddCommand(projectTrackerListCmd, projectTrackerImportCmd, projectTrackerSyncCmd)
	projectTrackerImportCmd.Flags().StringSliceVar(&trackerImportRigs, "rigs", nil, "Comma-separated rigs for the feature (required for a first import)")
	projectTrackerImportCmd.Flags().BoolVar(&trackerImportDryRun, "dry-run", false, "Print what would be created; change nothing")
	projectTrackerSyncCmd.Flags().BoolVar(&trackerSyncDryRun, "dry-run", false, "Print what would change; write nothing")
}

// loadProjectTracker returns the project's tracker, or an error if it has none.
func loadProjectTracker(townRoot, project string) (*config.TrackerConfig, tracker.Tracker, error) {
	pc, err := config.LoadProjectConfig(townRoot, project)
	if err != nil {
		return nil, nil, fmt.Errorf("project %q: %w", project, err)
	}
	cfg, err := config.ProjectTracker(pc)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, nil
	}
	tr, err := newProjectTracker(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cfg, tr, nil
}

func requireProjectTracker(townRoot, project string) (*config.TrackerConfig, tracker.Tracker, error) {
	cfg, tr, err := loadProjectTracker(townRoot, project)
	if err != nil {
		return nil, nil, err
	}
	if tr == nil {
		return nil, nil, fmt.Errorf("project %s has no tracker; add a \"tracker\" object to its project.json", project)
	}
	return cfg, tr, nil
}

// findImported returns the feature imported from the tracker item, or nil.
func findImported(townRoot, project, kind, itemID string) *projectfeature.Record {
	recs, _ := projectfeature.List(townRoot, project)
	for _, r := range recs {
		if r.Tracker != nil && r.Tracker.Kind == kind && r.Tracker.ID == itemID {
			return r
		}
	}
	return nil
}

func runProjectTrackerList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project := args[0]
	_, tr, err := requireProjectTracker(townRoot, project)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), trackerTimeout)
	defer cancel()
	items, err := tr.ListReady(ctx)
	if err != nil {
		return fmt.Errorf("listing ready items: %w", err)
	}
	return printTrackerList(os.Stdout, townRoot, project, tr, items)
}

func printTrackerList(w io.Writer, townRoot, project string, tr tracker.Tracker, items []tracker.Item) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "No ready items.")
		return nil
	}
	for _, it := range items {
		mark := ""
		if rec := findImported(townRoot, project, tr.Kind(), it.ID); rec != nil {
			mark = "  [imported as " + rec.ID + "]"
		}
		fmt.Fprintf(w, "%-8s %-10s %s%s\n", it.ID, it.State, it.Title, mark)
	}
	return nil
}

// importOps are the writes an import performs; tests replace them.
type importOps struct {
	createFeature func(townRoot, project, title, description string, rigs []string, branch string, ref *projectfeature.TrackerRef) (*projectfeature.Record, []projectfeature.BranchResult, error)
	addTask       func(townRoot string, rec *projectfeature.Record, rig, title, description string, after []string, ref *projectfeature.TrackerRef) (string, error)
}

var realImportOps = importOps{createFeature: createFeature, addTask: addFeatureTask}

func runProjectTrackerImport(cmd *cobra.Command, args []string) error {
	if !trackerImportDryRun {
		if err := deps.EnsureBeads(true); err != nil {
			return fmt.Errorf("beads dependency check failed: %w", err)
		}
	}
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project, itemID := args[0], args[1]
	_, tr, err := requireProjectTracker(townRoot, project)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), trackerTimeout)
	defer cancel()
	return importTrackerItem(ctx, os.Stdout, townRoot, project, tr, itemID, trackerImportRigs, trackerImportDryRun, realImportOps)
}

func importTrackerItem(ctx context.Context, w io.Writer, townRoot, project string, tr tracker.Tracker, itemID string, rigInput []string, dryRun bool, ops importOps) error {
	item, err := tr.Get(ctx, itemID)
	if err != nil {
		return fmt.Errorf("reading item %s: %w", itemID, err)
	}
	if item.State == tr.DoneState() {
		return fmt.Errorf("item %s is already %s in the tracker; nothing to import", item.ID, item.State)
	}

	existing := findImported(townRoot, project, tr.Kind(), item.ID)
	var rigs []string
	if existing != nil {
		rigs = featureRigNames(existing)
		if len(rigInput) > 0 {
			given, err := parseFeatureRigs(rigInput)
			if err != nil {
				return err
			}
			for _, r := range given {
				if _, ok := existing.Rigs[r]; !ok {
					return fmt.Errorf("rig %s is not part of feature %s (rigs: %s)", r, existing.ID, strings.Join(rigs, ", "))
				}
			}
			rigs = given
		}
	} else {
		if len(rigInput) == 0 {
			return fmt.Errorf("--rigs is required for a first import")
		}
		if rigs, err = parseFeatureRigs(rigInput); err != nil {
			return err
		}
	}

	plan := tracker.PlanChildren(existing, item.Children, rigs, tr.DoneState())

	if existing != nil {
		fmt.Fprintf(w, "Item %s is already imported as feature %s (%s)\n", item.ID, existing.ID, existing.Title)
	} else if dryRun {
		fmt.Fprintf(w, "Would create feature %q from %s item %s in rigs %s\n", item.Title, tr.Kind(), item.ID, strings.Join(rigs, ", "))
	}
	if existing != nil && existing.State != projectfeature.StateOpen && len(plan.Create) > 0 {
		fmt.Fprintf(w, "Feature %s is %s: %d new children not added (tasks can only be added while it is open)\n", existing.ID, existing.State, len(plan.Create))
		plan.Create = nil
	}

	rec := existing
	if !dryRun {
		if rec == nil {
			ref := &projectfeature.TrackerRef{Kind: tr.Kind(), ID: item.ID, URL: item.URL, ImportState: item.State}
			var results []projectfeature.BranchResult
			rec, results, err = ops.createFeature(townRoot, project, item.Title, item.Description, rigs, "", ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "Created feature %s from %s item %s\n  Branch: %s\n", rec.ID, tr.Kind(), item.ID, rec.Branch)
			for _, r := range results {
				fmt.Fprintf(w, "  %-16s from %s @ %s\n", r.Rig, r.Base, short(r.BaseCommit))
			}
		}
	}

	var failed error
	for _, cp := range plan.Create {
		if dryRun {
			fmt.Fprintf(w, "Would add task in rig %s: %s (%s %s)\n", cp.Rig, cp.Child.Title, tr.Kind(), cp.Child.ID)
			continue
		}
		cref := &projectfeature.TrackerRef{Kind: tr.Kind(), ID: cp.Child.ID, URL: cp.Child.URL, ImportState: cp.Child.State}
		id, err := ops.addTask(townRoot, rec, cp.Rig, strings.TrimSpace(cp.Child.Title), cp.Child.Description, nil, cref)
		if err != nil {
			failed = fmt.Errorf("adding task for child %s: %w", cp.Child.ID, err)
			break
		}
		fmt.Fprintf(w, "Added task %s in rig %s from child %s: %s\n", id, cp.Rig, cp.Child.ID, cp.Child.Title)
	}

	if len(plan.Existing) > 0 {
		fmt.Fprintf(w, "%d children already tasks of the feature, unchanged\n", len(plan.Existing))
	}
	if len(plan.Done) > 0 {
		fmt.Fprintf(w, "%d children already %s in the tracker, skipped:", len(plan.Done), tr.DoneState())
		for _, c := range plan.Done {
			fmt.Fprintf(w, " %s", c.ID)
		}
		fmt.Fprintln(w)
	}
	if len(plan.Unassigned) > 0 {
		fmt.Fprintf(w, "%d children unassigned and NOT created (several rigs; tag them %s<rig> with one of %s):\n",
			len(plan.Unassigned), tracker.RigTagPrefix, strings.Join(rigs, ", "))
		for _, c := range plan.Unassigned {
			fmt.Fprintf(w, "  %s  %s\n", c.ID, c.Title)
		}
	}
	if len(item.Children) == 0 {
		fmt.Fprintln(w, "Item has no children: feature created with no tasks")
	}
	return failed
}

func runProjectTrackerSync(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	project := args[0]
	_, tr, err := loadProjectTracker(townRoot, project)
	if err != nil {
		return err
	}
	if tr == nil {
		return nil // no tracker: nothing changes
	}
	ctx, cancel := context.WithTimeout(context.Background(), trackerTimeout)
	defer cancel()
	return syncProjectTracker(ctx, os.Stdout, townRoot, project, tr, trackerSyncDryRun, func(rec *projectfeature.Record) map[string]string {
		return featureTaskStatuses(townRoot, rec)
	})
}

// syncProjectTracker reconciles every imported feature of the project. A
// failure on one feature does not stop the others; the returned error is one
// line.
func syncProjectTracker(ctx context.Context, w io.Writer, townRoot, project string, tr tracker.Tracker, dryRun bool, statuses func(*projectfeature.Record) map[string]string) error {
	recs, _ := projectfeature.List(townRoot, project)
	var failures []string
	for _, rec := range recs {
		if rec.Tracker == nil || !tracker.NeedsSync(rec) {
			continue
		}
		rep, err := tracker.Reconcile(ctx, townRoot, rec, statuses(rec), tr, dryRun)
		verb := "set"
		if dryRun {
			verb = "would set"
		}
		for _, c := range rep.Changes {
			who := "feature " + rec.ID
			if c.Task != "" {
				who = "task " + c.Task
			}
			fmt.Fprintf(w, "%s: %s item %s %s -> %s\n", who, verb, c.Item, c.From, c.To)
		}
		for _, l := range rep.Left {
			fmt.Fprintln(w, l)
		}
		if err != nil {
			failures = append(failures, strings.ReplaceAll(err.Error(), "\n", "; "))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("tracker sync: %s", strings.Join(failures, "; "))
	}
	return nil
}
