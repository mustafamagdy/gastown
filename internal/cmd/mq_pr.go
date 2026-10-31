package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/vcs"
)

// The "gt mq pr" verbs let the refinery open, check and merge a pull request
// without calling gh or az itself. They go through internal/vcs, so a rig on
// GitHub and a rig on Azure DevOps use the same commands. The provider comes
// from the rig's origin remote.

var (
	mqPRJSON        bool
	mqPRTitle       string
	mqPRBody        string
	mqPRDraft       bool
	mqPRWait        bool
	mqPRTimeout     time.Duration
	mqPRInterval    time.Duration
	mqPRMethod      string
	mqPRExpectHead  string
	mqPRProviderOpt string
)

var mqPRCmd = &cobra.Command{
	Use:   "pr",
	Short: "Open, check and merge a pull request (GitHub or Azure DevOps)",
	Long: `Pull request verbs for the refinery. They work the same on GitHub and on
Azure DevOps: the provider is detected from the rig's origin remote (or set
with --provider). Pass the PR id printed by 'gt mq pr create'.`,
	RunE: requireSubcommand,
}

var mqPRCreateCmd = &cobra.Command{
	Use:   "create <rig> <mr-id>",
	Short: "Open a PR for a merge request (or return the open one)",
	Long: `Open a PR from the merge request's source branch to its target branch.
If an open PR for that branch pair exists it is returned and nothing is
created, so the command is safe to repeat.`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPRCreate,
}

var mqPRChecksCmd = &cobra.Command{
	Use:   "checks <rig> <pr-id>",
	Short: "Show CI and policy check state of a PR",
	Long: `Print the check state of a PR: pending, passing, failing or none.
With --wait, poll until the checks are no longer pending (exit 1 on timeout).
A failing result is still a successful command; read the state from the output.`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPRChecks,
}

var mqPRReviewCmd = &cobra.Command{
	Use:   "review <rig> <pr-id>",
	Short: "Show the review state of a PR (approved, pending or rejected)",
	Args:  cobra.ExactArgs(2),
	RunE:  runMQPRReview,
}

var mqPRMergeCmd = &cobra.Command{
	Use:   "merge <rig> <pr-id>",
	Short: "Merge a PR and wait until it is merged",
	Long: `Merge a PR. With --expect-head the merge is refused if the PR head moved.
The command returns success only when the PR is merged.`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPRMerge,
}

func init() {
	mqPRCmd.PersistentFlags().BoolVar(&mqPRJSON, "json", false, "Output JSON")
	mqPRCmd.PersistentFlags().StringVar(&mqPRProviderOpt, "provider", "", "Provider: github or azure (default: detect from origin)")
	mqPRCreateCmd.Flags().StringVar(&mqPRTitle, "title", "", "PR title (default: the source issue id)")
	mqPRCreateCmd.Flags().StringVar(&mqPRBody, "body", "", "PR description")
	mqPRCreateCmd.Flags().BoolVar(&mqPRDraft, "draft", false, "Open the PR as a draft")
	mqPRChecksCmd.Flags().BoolVar(&mqPRWait, "wait", false, "Wait until the checks are no longer pending")
	mqPRChecksCmd.Flags().DurationVar(&mqPRTimeout, "timeout", 15*time.Minute, "Longest wait with --wait")
	mqPRChecksCmd.Flags().DurationVar(&mqPRInterval, "interval", 20*time.Second, "Poll interval with --wait")
	mqPRMergeCmd.Flags().StringVar(&mqPRMethod, "method", "merge", "Merge method: merge or squash")
	mqPRMergeCmd.Flags().StringVar(&mqPRExpectHead, "expect-head", "", "Refuse to merge unless the PR head is this commit")
	mqPRCmd.AddCommand(mqPRCreateCmd, mqPRChecksCmd, mqPRReviewCmd, mqPRMergeCmd)
	mqCmd.AddCommand(mqPRCmd)
}

// mqPRRepoDir is the git directory the provider runs in for a rig.
func mqPRRepoDir(rigPath string) (string, error) {
	for _, dir := range []string{filepath.Join(rigPath, ".repo.git"), filepath.Join(rigPath, "mayor", "rig")} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no repo found for the rig (neither .repo.git nor mayor/rig exists)")
}

// mqPRNewProvider picks the provider of a repo: by name when given, else from
// the origin remote. Tests replace it.
var mqPRNewProvider = func(repoDir, name string) (vcs.Provider, error) {
	opts := vcs.Options{RepoDir: repoDir}
	if name != "" {
		return vcs.New(name, opts)
	}
	out, err := exec.Command("git", "--git-dir="+gitDirOf(repoDir), "remote", "get-url", "origin").Output() //nolint:gosec // G204: fixed git args
	if err != nil {
		return nil, fmt.Errorf("reading origin remote: %w", err)
	}
	return vcs.ForRemote(strings.TrimSpace(string(out)), opts)
}

// gitDirOf returns the .git directory of a work tree, or dir itself for a
// bare repo.
func gitDirOf(dir string) string {
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && st.IsDir() {
		return filepath.Join(dir, ".git")
	}
	return dir
}

func mqPRProviderForRig(rigName string) (vcs.Provider, error) {
	_, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return nil, err
	}
	repoDir, err := mqPRRepoDir(r.Path)
	if err != nil {
		return nil, err
	}
	return mqPRNewProvider(repoDir, mqPRProviderOpt)
}

type mqPRInfo struct {
	ID      string `json:"id"`
	Number  int    `json:"number,omitempty"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha,omitempty"`
}

func mqPRInfoOf(pr *vcs.PR) mqPRInfo {
	return mqPRInfo{ID: pr.ID, Number: pr.Number, URL: pr.URL, State: pr.State, Head: pr.Head, Base: pr.Base, HeadSHA: pr.HeadSHA}
}

func printMQPR(v any, text string) error {
	if mqPRJSON {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Println(text)
	return nil
}

func runMQPRCreate(cmd *cobra.Command, args []string) error {
	mgr, _, _, err := getRefineryManager(args[0])
	if err != nil {
		return err
	}
	mr, err := mgr.FindMR(args[1])
	if err != nil {
		return err
	}
	prov, err := mqPRProviderForRig(args[0])
	if err != nil {
		return err
	}
	title := mqPRTitle
	if title == "" {
		title = mr.IssueID
	}
	pr, err := mqPRCreate(cmd.Context(), prov, mr.Branch, mr.TargetBranch, title, mqPRBody, mqPRDraft)
	if err != nil {
		return err
	}
	return printMQPR(mqPRInfoOf(pr), fmt.Sprintf("%s %s %s", pr.ID, pr.State, pr.URL))
}

func mqPRCreate(ctx context.Context, prov vcs.Provider, head, base, title, body string, draft bool) (*vcs.PR, error) {
	if head == "" || base == "" {
		return nil, fmt.Errorf("merge request has no source or target branch")
	}
	return prov.CreatePR(ctx, vcs.CreatePRRequest{Head: head, Base: base, Title: title, Body: body, Draft: draft})
}

type mqPRChecksResult struct {
	State   string   `json:"state"`
	Failing []string `json:"failing,omitempty"`
}

func runMQPRChecks(cmd *cobra.Command, args []string) error {
	prov, err := mqPRProviderForRig(args[0])
	if err != nil {
		return err
	}
	res, err := mqPRChecks(cmd.Context(), prov, args[1], mqPRWait, mqPRTimeout, mqPRInterval)
	if err != nil {
		return err
	}
	text := res.State
	if len(res.Failing) > 0 {
		text += ": " + strings.Join(res.Failing, ", ")
	}
	return printMQPR(res, text)
}

func mqPRChecks(ctx context.Context, prov vcs.Provider, id string, wait bool, timeout, interval time.Duration) (mqPRChecksResult, error) {
	pr, err := prov.GetPR(ctx, id)
	if err != nil {
		return mqPRChecksResult{}, err
	}
	deadline := time.Now().Add(timeout)
	for {
		sum, err := prov.Checks(ctx, pr)
		if err != nil {
			return mqPRChecksResult{}, err
		}
		if sum.State != vcs.ChecksPending || !wait {
			return mqPRChecksResult{State: sum.State, Failing: sum.Failing}, nil
		}
		if time.Now().Add(interval).After(deadline) {
			return mqPRChecksResult{}, fmt.Errorf("checks still pending after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return mqPRChecksResult{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func runMQPRReview(cmd *cobra.Command, args []string) error {
	prov, err := mqPRProviderForRig(args[0])
	if err != nil {
		return err
	}
	pr, err := prov.GetPR(cmd.Context(), args[1])
	if err != nil {
		return err
	}
	state, err := prov.ReviewState(cmd.Context(), pr)
	if err != nil {
		return err
	}
	return printMQPR(map[string]string{"state": state}, state)
}

func runMQPRMerge(cmd *cobra.Command, args []string) error {
	prov, err := mqPRProviderForRig(args[0])
	if err != nil {
		return err
	}
	pr, err := mqPRMerge(cmd.Context(), prov, args[1], mqPRMethod, mqPRExpectHead)
	if err != nil {
		return err
	}
	return printMQPR(map[string]string{"id": pr.ID, "state": pr.State, "merge_commit": pr.MergeCommit}, fmt.Sprintf("%s %s %s", pr.ID, pr.State, pr.MergeCommit))
}

func mqPRMerge(ctx context.Context, prov vcs.Provider, id, method, expectHead string) (*vcs.PR, error) {
	pr, err := prov.GetPR(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := prov.Merge(ctx, pr, vcs.MergeOptions{Method: method, ExpectHead: expectHead}); err != nil {
		return nil, err
	}
	pr.State = vcs.StateMerged
	return pr, nil
}
