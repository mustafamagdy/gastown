// Package vcs is the pull request layer: a small provider interface to open,
// inspect and land a pull request on GitHub or Azure DevOps. It shells out to
// the stable machine interfaces of the platform CLIs (`gh ... --json`,
// `az ... --output json`) with exec args, never through a shell, and never
// logs or embeds credentials.
//
// It deliberately does not import internal/refinery; the refinery's
// PRProvider is a separate, merge-queue shaped abstraction.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PR states.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// Check states.
const (
	ChecksPending = "pending"
	ChecksPassing = "passing"
	ChecksFailing = "failing"
	ChecksNone    = "none"
)

// Review states.
const (
	ReviewApproved = "approved"
	ReviewPending  = "pending"
	ReviewRejected = "rejected"
)

// Merge methods.
const (
	MethodSquash = "squash"
	MethodMerge  = "merge"
)

// Provider names accepted by New.
const (
	ProviderGitHub    = "github"
	ProviderAzure     = "azure"
	ProviderBitbucket = "bitbucket"
)

// ErrUnsupported is returned for providers that have no PR layer implementation.
var ErrUnsupported = errors.New("unsupported vcs provider")

// ErrNotMerged is returned by Merge when the platform accepted the request but
// the PR is not merged (for example queued in a merge queue).
var ErrNotMerged = errors.New("pull request not merged")

// ErrHeadMoved is returned by Merge when the PR head no longer matches
// MergeOptions.ExpectHead.
var ErrHeadMoved = errors.New("pull request head moved")

// PR is a pull request, normalized across providers.
type PR struct {
	ID          string // provider id: GitHub number, Azure pullRequestId (as string)
	Number      int
	URL         string // human web URL
	Head        string // source branch, without refs/heads/
	Base        string // target branch, without refs/heads/; callers verify this
	HeadSHA     string
	State       string // open | merged | closed
	MergeCommit string // set once merged, when the provider reports it
	Draft       bool
}

// CreatePRRequest describes a PR to open.
type CreatePRRequest struct {
	RepoDir      string // working directory of the repo; overrides Options.RepoDir
	Head         string
	Base         string
	Title        string
	Body         string
	Draft        bool
	AutoComplete bool   // ask the platform to merge when policies pass
	MergeMethod  string // method for AutoComplete: squash | merge (default merge)
}

// MergeOptions controls Merge.
type MergeOptions struct {
	Method     string // squash | merge (default merge)
	ExpectHead string // refuse to merge unless the PR head is this SHA
}

// CheckSummary summarizes CI / policy checks.
type CheckSummary struct {
	State   string   // pending | passing | failing | none
	Failing []string // names of failing checks
}

// Provider is the PR layer for one VCS platform.
type Provider interface {
	// CreatePR opens a PR head->base. It is idempotent: when an open PR for
	// head->base exists it is returned unchanged.
	CreatePR(ctx context.Context, req CreatePRRequest) (*PR, error)
	// FindPR returns the open PR for head->base, or (nil, nil) when none.
	FindPR(ctx context.Context, head, base string) (*PR, error)
	// GetPR fetches a PR by provider id (or URL for GitHub).
	GetPR(ctx context.Context, id string) (*PR, error)
	// Checks summarizes CI / policy checks. State is none when there are none.
	Checks(ctx context.Context, pr *PR) (CheckSummary, error)
	// ReviewState reports approved, pending or rejected.
	ReviewState(ctx context.Context, pr *PR) (string, error)
	// Merge lands the PR and returns only once it is merged, or an error.
	// pr.MergeCommit is filled in when the provider reports it.
	Merge(ctx context.Context, pr *PR, opts MergeOptions) error
}

// Options configures a provider.
type Options struct {
	// RepoDir is the git working directory commands run in. Required for
	// GitHub (gh infers the repo from it) and used to resolve the remote for
	// Azure DevOps.
	RepoDir string
	// RemoteURL overrides the remote used by Azure DevOps. When empty it is
	// read from `git remote get-url origin` in RepoDir.
	RemoteURL string
	// MergePollInterval and MergeTimeout bound Azure's async merge wait.
	// Defaults: 3s and 5m.
	MergePollInterval time.Duration
	MergeTimeout      time.Duration
}

// New returns the provider for a name: "github" or "azure". Bitbucket and
// anything else returns an error wrapping ErrUnsupported.
func New(name string, opts Options) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", ProviderGitHub:
		return newGitHub(opts), nil
	case ProviderAzure, "azure-devops", "ado":
		return newAzure(opts), nil
	case ProviderBitbucket:
		return nil, fmt.Errorf("%w: bitbucket has no PR layer yet (supported: github, azure)", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: %q (supported: github, azure)", ErrUnsupported, name)
	}
}

// ForRemote detects the provider from a git remote URL and returns it.
func ForRemote(remoteURL string, opts Options) (Provider, error) {
	kind, err := DetectProvider(remoteURL)
	if err != nil {
		return nil, err
	}
	if opts.RemoteURL == "" {
		opts.RemoteURL = remoteURL
	}
	return New(kind, opts)
}

// DetectProvider names the provider for a remote URL by host.
func DetectProvider(remoteURL string) (string, error) {
	host := remoteHost(remoteURL)
	switch {
	case host == "":
		return "", fmt.Errorf("cannot determine host of remote %q", RedactURL(remoteURL))
	case host == "dev.azure.com", host == "ssh.dev.azure.com",
		host == "vs-ssh.visualstudio.com", strings.HasSuffix(host, ".visualstudio.com"):
		return ProviderAzure, nil
	case host == "bitbucket.org", strings.Contains(host, "bitbucket"):
		return ProviderBitbucket, nil
	case host == "github.com", strings.Contains(host, "github"):
		return ProviderGitHub, nil
	}
	return "", fmt.Errorf("%w: cannot detect provider for host %q", ErrUnsupported, host)
}

// CmdError is a failed CLI invocation. Args are not included: they may carry
// PR bodies. Stderr is truncated.
type CmdError struct {
	Bin    string
	Sub    string // first words of the command, e.g. "pr create"
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	msg := fmt.Sprintf("%s %s: %v", e.Bin, e.Sub, e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *CmdError) Unwrap() error { return e.Err }

// run executes bin with args (no shell) in dir. On failure the stdout is still
// returned so callers can interpret CLIs that exit non-zero with valid output.
func run(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: bin is a fixed CLI name, args are exec args
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		s := strings.TrimSpace(stderr.String())
		if len(s) > 500 {
			s = s[:500] + "..."
		}
		return stdout.Bytes(), &CmdError{Bin: bin, Sub: subcommand(args), Stderr: s, Err: err}
	}
	return stdout.Bytes(), nil
}

// subcommand returns the leading non-flag words of args.
func subcommand(args []string) string {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(words) >= 3 {
			break
		}
		words = append(words, a)
	}
	return strings.Join(words, " ")
}

func normalizeMethod(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", MethodMerge:
		return MethodMerge, nil
	case MethodSquash:
		return MethodSquash, nil
	}
	return "", fmt.Errorf("unsupported merge method %q (want squash or merge)", m)
}

func firstDir(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
