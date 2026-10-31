package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/projectfeature"
	"github.com/steveyegge/gastown/internal/review"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	reviewBead     string
	reviewKind     string
	reviewListJSON bool
	reviewShowJSON bool
)

var reviewCmd = &cobra.Command{
	Use:     "review",
	GroupID: GroupWork,
	Short:   "Ask a human to review an HTML page in the browser",
	Long: `Visual review gate. An agent writes an HTML page (a plan, a design, a
question, or a result to approve) and asks a human to review it in the
browser with lavish-axi. The outcome is recorded on the review, mirrored on
the bead (labels review:pending, review:approved, review:rejected and a
comment with the feedback), and sent to the agent that asked.

The gt daemon watches open reviews; no agent has to wait for the answer.
Reviews are stored in <town>/.runtime/reviews.

With the project setting require_visual_review, a feature lands only after an
approved "approval" review. The refinery never merges a merge request whose
source bead has an open or rejected approval review.`,
}

var reviewRequestCmd = &cobra.Command{
	Use:         "request <page.html>",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Ask for a review of an HTML page (does not wait)",
	Long: `Copies the page (and the files next to it, up to 50 MB) into the town so
it survives polecat cleanup, adds a small panel with Approve, Request changes
and an answer box, opens a lavish session, prints one link and returns at
once. Give the link to the human; the outcome comes back as a nudge or mail.

Kinds: plan, design, question (answer box only), approval (gates merging).`,
	Args: cobra.ExactArgs(1),
	RunE: runReviewRequest,
}

var reviewListCmd = &cobra.Command{
	Use:   "list",
	Short: "List reviews",
	Args:  cobra.NoArgs,
	RunE:  runReviewList,
}

var reviewShowCmd = &cobra.Command{
	Use:         "show <id>",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Show one review with its feedback",
	Args:        cobra.ExactArgs(1),
	RunE:        runReviewShow,
}

var reviewCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel a review and end its lavish session",
	Args:  cobra.ExactArgs(1),
	RunE:  runReviewCancel,
}

var reviewReopenCmd = &cobra.Command{
	Use:   "reopen <id>",
	Short: "Resume a review whose window closed or was ended",
	Long: `Resumes the lavish session of a review in state disconnected or ended and
watches it again. The daemon never reopens a review by itself.`,
	Args: cobra.ExactArgs(1),
	RunE: runReviewReopen,
}

var reviewWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch open reviews in the foreground (the daemon does this normally)",
	Long: `Runs the same watcher the daemon runs, in this terminal, until interrupted.
Useful in a town without a running daemon, and for testing.`,
	Args: cobra.NoArgs,
	RunE: runReviewWatch,
}

func init() {
	reviewRequestCmd.Flags().StringVar(&reviewBead, "bead", "", "Bead the review is about (required)")
	reviewRequestCmd.Flags().StringVar(&reviewKind, "kind", "", "plan, design, question or approval (required)")
	_ = reviewRequestCmd.MarkFlagRequired("bead")
	_ = reviewRequestCmd.MarkFlagRequired("kind")
	reviewListCmd.Flags().BoolVar(&reviewListJSON, "json", false, "Output as JSON")
	reviewShowCmd.Flags().BoolVar(&reviewShowJSON, "json", false, "Output as JSON")
	reviewCmd.AddCommand(reviewRequestCmd, reviewListCmd, reviewShowCmd, reviewCancelCmd, reviewReopenCmd, reviewWatchCmd)
	rootCmd.AddCommand(reviewCmd)
}

func reviewTown() (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return townRoot, nil
}

func reviewClient(townRoot string) *review.Client { return &review.Client{TownRoot: townRoot} }

// reviewAlive reports whether an agent address has a live session.
func reviewAlive(t *tmux.Tmux) func(string) bool {
	return func(addr string) bool {
		for _, s := range mail.AddressToSessionIDs(addr) {
			if ok, err := t.HasSession(s); err == nil && ok {
				return true
			}
		}
		return false
	}
}

func runReviewRequest(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	if !review.ValidKind(reviewKind) {
		return fmt.Errorf("unknown kind %q (want plan, design, question or approval)", reviewKind)
	}
	if _, err := getBeadInfoFromTownRoot(townRoot, reviewBead); err != nil {
		return fmt.Errorf("bead %s: %w", reviewBead, err)
	}
	tips, err := reviewTips(townRoot, reviewBead, reviewKind)
	if err != nil {
		return err
	}
	id, err := review.NewID()
	if err != nil {
		return err
	}
	dir := review.AssetDir(townRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	copyPath, err := review.CopyPage(args[0], dir)
	if err != nil {
		return err
	}
	page, err := os.ReadFile(copyPath) //nolint:gosec // G304: the copy we just made
	if err != nil {
		return err
	}
	if err := os.WriteFile(copyPath, []byte(review.InjectPanel(string(page), reviewKind)), 0o644); err != nil { //nolint:gosec // G306: a page, not a secret
		return err
	}
	client := reviewClient(townRoot)
	link, err := client.Open(copyPath, false)
	if err != nil {
		return err
	}
	_, publicURL := review.TownSettings(townRoot)
	link = review.PublicLink(link, publicURL)

	src, _ := filepath.Abs(args[0])
	rec := &review.Record{
		ID: id, Bead: reviewBead, Kind: reviewKind, HTML: copyPath, Source: src,
		URL: link, Requester: detectSender(), State: review.StateOpen, Tips: tips,
	}
	if err := review.Create(townRoot, rec); err != nil {
		_ = client.End(copyPath)
		return err
	}
	ok = true
	comment := fmt.Sprintf("Review %s requested (%s): %s", id, reviewKind, link)
	if err := review.SetBeadState(review.BeadMirror{TownRoot: townRoot}, rec, comment); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not mark bead %s: %v\n", reviewBead, err)
	}
	fmt.Printf("Review %s (%s) requested for %s. Open: %s\n", id, reviewKind, reviewBead, link)
	return nil
}

// reviewTips records what an approval review is about, so the gate can tell
// when the code changed after the approval. For the epic or a task of a
// feature it is the feature branch tip of each rig that has one; otherwise it
// is the git HEAD of the working directory, when that is a repo. It returns
// nothing for other kinds, and nothing when there is nothing to record. A tip
// that exists but cannot be read is an error.
func reviewTips(townRoot, bead, kind string) ([]review.Tip, error) {
	if kind != review.KindApproval {
		return nil, nil
	}
	rec, err := projectfeature.Find(townRoot, bead)
	if err != nil {
		rec, _, err = projectfeature.ResolveForBead(townRoot, bead)
	}
	if err != nil && !errors.Is(err, projectfeature.ErrNotFound) {
		return nil, err
	}
	if rec != nil {
		names := make([]string, 0, len(rec.Rigs))
		for name, rs := range rec.Rigs {
			switch rs.State {
			case projectfeature.RigBranch, projectfeature.RigPROpen, projectfeature.RigHeld:
				names = append(names, name)
			}
		}
		sort.Strings(names)
		var tips []review.Tip
		for _, name := range names {
			repo, err := featureRigRepo(townRoot, name)
			if err != nil {
				return nil, fmt.Errorf("recording the code under review: %w", err)
			}
			tip, err := review.BranchTip(name, repo.GitDir, rec.Branch)
			if err != nil {
				return nil, fmt.Errorf("recording the code under review: %w", err)
			}
			tips = append(tips, tip)
		}
		return tips, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil
	}
	tip, err := review.HeadTip(cwd)
	if err != nil {
		return nil, fmt.Errorf("recording the code under review: %w", err)
	}
	if tip == nil {
		return nil, nil
	}
	return []review.Tip{*tip}, nil
}

func runReviewList(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	recs, errs := review.List(townRoot)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", e)
	}
	if reviewListJSON {
		if recs == nil {
			recs = []*review.Record{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(recs)
	}
	if len(recs) == 0 {
		fmt.Println("No reviews.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tBEAD\tKIND\tSTATE\tFEEDBACK\tAGE")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.ID, r.Bead, r.Kind, r.State, len(r.Feedback),
			time.Since(r.CreatedAt).Truncate(time.Minute))
	}
	return tw.Flush()
}

func runReviewShow(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	rec, err := review.Load(townRoot, args[0])
	if err != nil {
		return err
	}
	if reviewShowJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rec)
	}
	fmt.Printf("%s  %s review of %s\n", rec.ID, rec.Kind, rec.Bead)
	fmt.Printf("  State:     %s\n", rec.State)
	fmt.Printf("  Link:      %s\n", rec.URL)
	fmt.Printf("  Page:      %s\n", rec.HTML)
	fmt.Printf("  Asked by:  %s\n", rec.Requester)
	fmt.Printf("  Created:   %s\n", rec.CreatedAt.Format(time.RFC3339))
	if rec.Error != "" {
		fmt.Printf("  Problem:   %s\n", rec.Error)
	}
	if len(rec.Feedback) == 0 {
		fmt.Println("  Feedback:  (none)")
		return nil
	}
	fmt.Println("  Feedback:")
	for _, f := range rec.Feedback {
		who := f.Tag
		if f.Decision != "" {
			who = f.Decision
		}
		sel := ""
		if f.Selector != "" {
			sel = " [" + f.Selector + "]"
		}
		fmt.Printf("    %s %s%s: %s\n", f.At.Local().Format("15:04"), who, sel, strings.ReplaceAll(f.Text, "\n", "\n      "))
	}
	return nil
}

func runReviewCancel(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	rec, err := review.Load(townRoot, args[0])
	if err != nil {
		return err
	}
	if rec.State != review.StateOpen && rec.State != review.StateDisconnected {
		return fmt.Errorf("review %s is %s; only an open review can be cancelled", rec.ID, rec.State)
	}
	// Mark it ended first so the daemon's poll, which returns when the
	// session ends, does not report it as a surprise.
	rec, err = review.Update(townRoot, rec.ID, func(r *review.Record) error {
		r.State = review.StateEnded
		return nil
	})
	if err != nil {
		return err
	}
	if err := reviewClient(townRoot).End(rec.HTML); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not end the lavish session: %v\n", err)
	}
	if err := review.SetBeadState(review.BeadMirror{TownRoot: townRoot}, rec, fmt.Sprintf("Review %s cancelled.", rec.ID)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update bead %s: %v\n", rec.Bead, err)
	}
	fmt.Printf("Review %s cancelled.\n", rec.ID)
	return nil
}

func runReviewReopen(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	rec, err := review.Load(townRoot, args[0])
	if err != nil {
		return err
	}
	if rec.State != review.StateDisconnected && rec.State != review.StateEnded {
		return fmt.Errorf("review %s is %s; only a disconnected or ended review can be reopened", rec.ID, rec.State)
	}
	link, err := reviewClient(townRoot).Open(rec.HTML, true)
	if err != nil && !errors.Is(err, review.ErrUserEnded) {
		return err
	}
	_, publicURL := review.TownSettings(townRoot)
	link = review.PublicLink(link, publicURL)
	rec, err = review.Update(townRoot, rec.ID, func(r *review.Record) error {
		r.State, r.URL, r.Error = review.StateOpen, link, ""
		return nil
	})
	if err != nil {
		return err
	}
	if err := review.SetBeadState(review.BeadMirror{TownRoot: townRoot}, rec, fmt.Sprintf("Review %s reopened: %s", rec.ID, link)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update bead %s: %v\n", rec.Bead, err)
	}
	fmt.Printf("Review %s reopened. Open: %s\n", rec.ID, link)
	return nil
}

func runReviewWatch(cmd *cobra.Command, args []string) error {
	townRoot, err := reviewTown()
	if err != nil {
		return err
	}
	notifier := review.Notifier{TownRoot: townRoot, Alive: reviewAlive(tmux.NewTmux())}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "review: "+format+"\n", a...) }
	eng := review.NewEngine(townRoot, reviewClient(townRoot), review.BeadMirror{TownRoot: townRoot}, notifier.Notify, logf)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	fmt.Println("Watching reviews. Press Ctrl-C to stop.")
	for {
		eng.Tick()
		select {
		case <-sig:
			eng.Stop()
			return nil
		case <-tick.C:
		}
	}
}
