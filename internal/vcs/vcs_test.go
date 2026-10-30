package vcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI installs a fake executable `name` on PATH. For each call it derives
// a key from the leading non-flag words ("repos pr create" -> repos_pr_create),
// logs the args, and replies from files in the returned dir:
//
//	<key>.json or <key>.<n>.json (n = 1-based call count)  stdout
//	<key>.err                                              stderr
//	<key>.exit                                             exit code
func fakeCLI(t *testing.T, name string) (dir string, calls func() []string) {
	t.Helper()
	dir = t.TempDir()
	bin := t.TempDir()
	script := `#!/bin/sh
key=""
for a in "$@"; do
  case "$a" in -*) break;; esac
  key="${key:+${key}_}$a"
  n=$(echo "$key" | tr -cd '_' | wc -c)
  [ "$n" -ge "$FAKE_DEPTH" ] && break
done
printf '%s\n' "$*" | tr '\n' ' ' >> "$FAKE_DIR/calls.log"; echo >> "$FAKE_DIR/calls.log"
cf="$FAKE_DIR/$key.count"
c=$(cat "$cf" 2>/dev/null || echo 0); c=$((c+1)); echo $c > "$cf"
[ -f "$FAKE_DIR/$key.err" ] && cat "$FAKE_DIR/$key.err" >&2
if [ -f "$FAKE_DIR/$key.$c.json" ]; then cat "$FAKE_DIR/$key.$c.json"
elif [ -f "$FAKE_DIR/$key.json" ]; then cat "$FAKE_DIR/$key.json"; fi
[ -f "$FAKE_DIR/$key.exit" ] && exit $(cat "$FAKE_DIR/$key.exit")
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	depth := "1" // gh: "pr view"; az: "repos pr create"
	if name == "az" {
		depth = "2"
	}
	t.Setenv("FAKE_DEPTH", depth)
	return dir, func() []string {
		b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		s := strings.TrimSpace(string(b))
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
}

func put(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func anyCall(calls []string, parts ...string) bool {
outer:
	for _, c := range calls {
		for _, p := range parts {
			if !strings.Contains(c, p) {
				continue outer
			}
		}
		return true
	}
	return false
}

// ---- GitHub ----

const ghOpen = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","headRefName":"feat/x","baseRefName":"main","headRefOid":"abc123","isDraft":false,"mergeCommit":null}`

func TestGitHubCreateAndIdempotent(t *testing.T) {
	dir, calls := fakeCLI(t, "gh")
	put(t, dir, "pr_list.json", `[]`)
	put(t, dir, "pr_create.json", "Creating pull request\nhttps://github.com/o/r/pull/7\n")
	put(t, dir, "pr_view.json", ghOpen)
	p, _ := New("github", Options{RepoDir: t.TempDir()})
	pr, err := p.CreatePR(context.Background(), CreatePRRequest{Head: "feat/x", Base: "main", Title: "T", Body: "B", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.ID != "7" || pr.Base != "main" || pr.Head != "feat/x" || pr.State != StateOpen || pr.HeadSHA != "abc123" {
		t.Fatalf("pr = %+v", pr)
	}
	if !anyCall(calls(), "pr create", "--draft", "--head feat/x", "--base main") {
		t.Fatalf("calls = %v", calls())
	}

	// Idempotent: existing open PR is returned, no create call.
	dir2, calls2 := fakeCLI(t, "gh")
	put(t, dir2, "pr_list.json", `[`+ghOpen+`]`)
	p2, _ := New("github", Options{RepoDir: t.TempDir()})
	pr2, err := p2.CreatePR(context.Background(), CreatePRRequest{Head: "feat/x", Base: "main", Title: "T"})
	if err != nil || pr2.ID != "7" {
		t.Fatalf("pr=%+v err=%v", pr2, err)
	}
	if anyCall(calls2(), "pr create") {
		t.Fatal("create must not run when PR exists")
	}
}

func TestGitHubFindNoneAndWrongBase(t *testing.T) {
	dir, _ := fakeCLI(t, "gh")
	put(t, dir, "pr_list.json", `[`+strings.Replace(ghOpen, `"baseRefName":"main"`, `"baseRefName":"develop"`, 1)+`]`)
	p, _ := New("github", Options{RepoDir: t.TempDir()})
	pr, err := p.FindPR(context.Background(), "feat/x", "main")
	if err != nil || pr != nil {
		t.Fatalf("want nil,nil got %+v %v", pr, err)
	}
}

func TestGitHubChecksMapping(t *testing.T) {
	cases := []struct {
		name, json, stderr string
		exit               string
		want               string
		failing            int
	}{
		{"passing", `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"skipping"}]`, "", "0", ChecksPassing, 0},
		{"pending", `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"pending"}]`, "", "8", ChecksPending, 0},
		{"failing", `[{"name":"a","bucket":"fail"},{"name":"b","bucket":"pending"}]`, "", "1", ChecksFailing, 1},
		{"cancel", `[{"name":"a","bucket":"cancel"}]`, "", "1", ChecksFailing, 1},
		{"empty", `[]`, "", "0", ChecksNone, 0},
		{"none reported", ``, "no checks reported on the 'x' branch", "1", ChecksNone, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := fakeCLI(t, "gh")
			put(t, dir, "pr_checks.json", c.json)
			put(t, dir, "pr_checks.exit", c.exit)
			if c.stderr != "" {
				put(t, dir, "pr_checks.err", c.stderr)
			}
			p, _ := New("github", Options{RepoDir: t.TempDir()})
			got, err := p.Checks(context.Background(), &PR{ID: "7"})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.want || len(got.Failing) != c.failing {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestGitHubReviewMapping(t *testing.T) {
	for dec, want := range map[string]string{
		"APPROVED": ReviewApproved, "CHANGES_REQUESTED": ReviewRejected,
		"REVIEW_REQUIRED": ReviewPending, "": ReviewPending,
	} {
		dir, _ := fakeCLI(t, "gh")
		put(t, dir, "pr_view.json", `{"reviewDecision":"`+dec+`"}`)
		p, _ := New("github", Options{RepoDir: t.TempDir()})
		got, err := p.ReviewState(context.Background(), &PR{ID: "7"})
		if err != nil || got != want {
			t.Fatalf("%q: got %q err %v want %q", dec, got, err, want)
		}
	}
}

func TestGitHubMerge(t *testing.T) {
	dir, calls := fakeCLI(t, "gh")
	put(t, dir, "pr_view.json", `{"number":7,"url":"u","state":"MERGED","headRefName":"feat/x","baseRefName":"main","headRefOid":"abc","mergeCommit":{"oid":"deadbeef"}}`)
	p, _ := New("github", Options{RepoDir: t.TempDir()})
	pr := &PR{ID: "7"}
	if err := p.Merge(context.Background(), pr, MergeOptions{Method: "squash", ExpectHead: "abc"}); err != nil {
		t.Fatal(err)
	}
	if pr.MergeCommit != "deadbeef" || pr.State != StateMerged {
		t.Fatalf("pr = %+v", pr)
	}
	if !anyCall(calls(), "pr merge 7", "--squash", "--match-head-commit abc") {
		t.Fatalf("calls = %v", calls())
	}

	// Still open after merge request -> ErrNotMerged.
	dir2, _ := fakeCLI(t, "gh")
	put(t, dir2, "pr_view.json", ghOpen)
	p2, _ := New("github", Options{RepoDir: t.TempDir()})
	if err := p2.Merge(context.Background(), &PR{ID: "7"}, MergeOptions{}); !errors.Is(err, ErrNotMerged) {
		t.Fatalf("err = %v", err)
	}

	// Head moved.
	dir3, _ := fakeCLI(t, "gh")
	put(t, dir3, "pr_merge.exit", "1")
	put(t, dir3, "pr_merge.err", "Head branch was modified. Review and try the merge again.")
	p3, _ := New("github", Options{RepoDir: t.TempDir()})
	if err := p3.Merge(context.Background(), &PR{ID: "7"}, MergeOptions{ExpectHead: "old"}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err = %v", err)
	}
}

// ---- Azure DevOps ----

const azOpen = `{"pullRequestId":42,"status":"active","mergeStatus":"succeeded","isDraft":false,
 "sourceRefName":"refs/heads/feat/x","targetRefName":"refs/heads/main",
 "lastMergeSourceCommit":{"commitId":"abc123"},
 "repository":{"name":"r","webUrl":"https://dev.azure.com/org/My%20Proj/_git/r"}}`

const azDone = `{"pullRequestId":42,"status":"completed","mergeStatus":"succeeded",
 "sourceRefName":"refs/heads/feat/x","targetRefName":"refs/heads/main",
 "lastMergeSourceCommit":{"commitId":"abc123"},"lastMergeCommit":{"commitId":"feedface"},
 "repository":{"webUrl":"https://dev.azure.com/org/My%20Proj/_git/r"}}`

func azProvider(t *testing.T) Provider {
	t.Helper()
	p, err := New("azure", Options{
		RepoDir:           t.TempDir(),
		RemoteURL:         "https://org@dev.azure.com/org/My%20Proj/_git/r",
		MergePollInterval: 5 * time.Millisecond,
		MergeTimeout:      300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAzureCreateAndIdempotent(t *testing.T) {
	dir, calls := fakeCLI(t, "az")
	put(t, dir, "repos_pr_list.json", `[]`)
	put(t, dir, "repos_pr_create.json", azOpen)
	p := azProvider(t)
	pr, err := p.CreatePR(context.Background(), CreatePRRequest{
		Head: "feat/x", Base: "main", Title: "T", Body: "line1\nline2", AutoComplete: true, MergeMethod: "squash"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.ID != "42" || pr.Head != "feat/x" || pr.Base != "main" || pr.HeadSHA != "abc123" ||
		pr.URL != "https://dev.azure.com/org/My%20Proj/_git/r/pullrequest/42" {
		t.Fatalf("pr = %+v", pr)
	}
	if !anyCall(calls(), "repos pr create", "--source-branch feat/x", "--target-branch main",
		"--auto-complete true", "--squash true", "--organization https://dev.azure.com/org",
		"--project My Proj", "--repository r", "--output json") {
		t.Fatalf("calls = %q", calls())
	}

	dir2, calls2 := fakeCLI(t, "az")
	put(t, dir2, "repos_pr_list.json", `[`+azOpen+`]`)
	pr2, err := azProvider(t).CreatePR(context.Background(), CreatePRRequest{Head: "feat/x", Base: "main", Title: "T"})
	if err != nil || pr2.ID != "42" {
		t.Fatalf("pr=%+v err=%v", pr2, err)
	}
	if anyCall(calls2(), "pr create") {
		t.Fatal("create must not run when PR exists")
	}
}

func TestAzureFindNone(t *testing.T) {
	dir, _ := fakeCLI(t, "az")
	put(t, dir, "repos_pr_list.json", `[]`)
	pr, err := azProvider(t).FindPR(context.Background(), "feat/x", "main")
	if pr != nil || err != nil {
		t.Fatalf("got %+v %v", pr, err)
	}
}

func TestAzureChecksMapping(t *testing.T) {
	pol := func(status, name string, blocking bool) string {
		b := "false"
		if blocking {
			b = "true"
		}
		return `{"status":"` + status + `","configuration":{"isBlocking":` + b + `,"isEnabled":true,"type":{"displayName":"Build"},"settings":{"displayName":"` + name + `"}}}`
	}
	review := `{"status":"rejected","configuration":{"isBlocking":true,"type":{"displayName":"Minimum number of reviewers"}}}`
	cases := []struct {
		name, json, want string
		failing          []string
	}{
		{"none", `[]`, ChecksNone, nil},
		{"only review policy ignored", `[` + review + `]`, ChecksNone, nil},
		{"passing", `[` + pol("approved", "ci", true) + `,` + pol("notApplicable", "x", true) + `]`, ChecksPassing, nil},
		{"pending", `[` + pol("approved", "ci", true) + `,` + pol("running", "slow", true) + `]`, ChecksPending, nil},
		{"queued", `[` + pol("queued", "slow", true) + `]`, ChecksPending, nil},
		{"failing", `[` + pol("rejected", "ci", true) + `,` + pol("running", "slow", true) + `]`, ChecksFailing, []string{"ci"}},
		{"non-blocking failure ignored", `[` + pol("rejected", "lint", false) + `,` + pol("approved", "ci", true) + `]`, ChecksPassing, nil},
		{"broken", `[` + pol("broken", "ci", true) + `]`, ChecksFailing, []string{"ci"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, calls := fakeCLI(t, "az")
			put(t, dir, "repos_pr_policy.json", c.json)
			got, err := azProvider(t).Checks(context.Background(), &PR{ID: "42"})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.want || strings.Join(got.Failing, ",") != strings.Join(c.failing, ",") {
				t.Fatalf("got %+v", got)
			}
			if !anyCall(calls(), "repos pr policy list", "--id 42") {
				t.Fatalf("calls = %q", calls())
			}
		})
	}
}

func TestAzureReviewMapping(t *testing.T) {
	cases := []struct{ name, json, want string }{
		{"approved", `[{"vote":10,"isRequired":true},{"vote":0,"isRequired":false}]`, ReviewApproved},
		{"approved with suggestions", `[{"vote":5},{"vote":10}]`, ReviewApproved},
		{"waiting is pending", `[{"vote":-5}]`, ReviewPending},
		{"no votes", `[{"vote":0}]`, ReviewPending},
		{"rejected", `[{"vote":10},{"vote":-10}]`, ReviewRejected},
		{"required missing", `[{"vote":10},{"vote":0,"isRequired":true}]`, ReviewPending},
		{"required group ignored", `[{"vote":10},{"vote":0,"isRequired":true,"isContainer":true}]`, ReviewApproved},
		{"empty", `[]`, ReviewPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := fakeCLI(t, "az")
			put(t, dir, "repos_pr_reviewer.json", c.json)
			got, err := azProvider(t).ReviewState(context.Background(), &PR{ID: "42"})
			if err != nil || got != c.want {
				t.Fatalf("got %q err %v want %q", got, err, c.want)
			}
		})
	}
}

func TestAzureMergePollsUntilCompleted(t *testing.T) {
	dir, calls := fakeCLI(t, "az")
	put(t, dir, "repos_pr_update.json", azOpen)
	put(t, dir, "repos_pr_show.1.json", azOpen) // ExpectHead check
	put(t, dir, "repos_pr_show.2.json", azOpen) // still active
	put(t, dir, "repos_pr_show.3.json", azOpen)
	put(t, dir, "repos_pr_show.json", azDone)
	pr := &PR{ID: "42"}
	err := azProvider(t).Merge(context.Background(), pr, MergeOptions{Method: "squash", ExpectHead: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != StateMerged || pr.MergeCommit != "feedface" {
		t.Fatalf("pr = %+v", pr)
	}
	if !anyCall(calls(), "repos pr update", "--id 42", "--status completed", "--squash true") {
		t.Fatalf("calls = %q", calls())
	}
}

func TestAzureMergeTimeoutAndFailures(t *testing.T) {
	dir, _ := fakeCLI(t, "az")
	put(t, dir, "repos_pr_update.json", azOpen)
	put(t, dir, "repos_pr_show.json", azOpen)
	err := azProvider(t).Merge(context.Background(), &PR{ID: "42"}, MergeOptions{})
	if !errors.Is(err, ErrNotMerged) || !strings.Contains(err.Error(), "not completed within") {
		t.Fatalf("err = %v", err)
	}

	dir2, _ := fakeCLI(t, "az")
	put(t, dir2, "repos_pr_update.json", azOpen)
	put(t, dir2, "repos_pr_show.json", strings.Replace(azOpen, `"succeeded"`, `"conflicts"`, 1))
	if err := azProvider(t).Merge(context.Background(), &PR{ID: "42"}, MergeOptions{}); !errors.Is(err, ErrNotMerged) {
		t.Fatalf("conflicts: err = %v", err)
	}

	// Head moved: update must not run.
	dir3, calls3 := fakeCLI(t, "az")
	put(t, dir3, "repos_pr_show.json", azOpen)
	if err := azProvider(t).Merge(context.Background(), &PR{ID: "42"}, MergeOptions{ExpectHead: "other"}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err = %v", err)
	}
	if anyCall(calls3(), "pr update") {
		t.Fatal("update ran despite moved head")
	}

	// Context cancel stops polling.
	dir4, _ := fakeCLI(t, "az")
	put(t, dir4, "repos_pr_update.json", azOpen)
	put(t, dir4, "repos_pr_show.json", azOpen)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	p, _ := New("azure", Options{RepoDir: t.TempDir(), RemoteURL: "https://dev.azure.com/o/p/_git/r",
		MergePollInterval: 5 * time.Millisecond, MergeTimeout: time.Minute})
	if err := p.Merge(ctx, &PR{ID: "42"}, MergeOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestAzureMergeMethodMerge(t *testing.T) {
	dir, calls := fakeCLI(t, "az")
	put(t, dir, "repos_pr_update.json", azOpen)
	put(t, dir, "repos_pr_show.json", azDone)
	if err := azProvider(t).Merge(context.Background(), &PR{ID: "42"}, MergeOptions{Method: "merge"}); err != nil {
		t.Fatal(err)
	}
	if !anyCall(calls(), "--squash false") {
		t.Fatalf("calls = %q", calls())
	}
}

// ---- parsing, detection, factory ----

func TestParseAzureRemote(t *testing.T) {
	cases := []struct {
		in                 string
		org, project, repo string
		wantErr            bool
	}{
		{"https://dev.azure.com/org/proj/_git/repo", "https://dev.azure.com/org", "proj", "repo", false},
		{"https://org@dev.azure.com/org/proj/_git/repo", "https://dev.azure.com/org", "proj", "repo", false},
		{"https://tok:secret@dev.azure.com/org/My%20Proj/_git/re%20po", "https://dev.azure.com/org", "My Proj", "re po", false},
		{"https://dev.azure.com/org/proj/_git/repo.git", "https://dev.azure.com/org", "proj", "repo", false},
		{"git@ssh.dev.azure.com:v3/org/proj/repo", "https://dev.azure.com/org", "proj", "repo", false},
		{"ssh://git@ssh.dev.azure.com/v3/org/proj/repo", "https://dev.azure.com/org", "proj", "repo", false},
		{"https://org.visualstudio.com/proj/_git/repo", "https://org.visualstudio.com", "proj", "repo", false},
		{"https://org.visualstudio.com/DefaultCollection/proj/_git/repo", "https://org.visualstudio.com", "proj", "repo", false},
		{"org@vs-ssh.visualstudio.com:v3/org/proj/repo", "https://org.visualstudio.com", "proj", "repo", false},
		{"https://dev.azure.com/org/_git/repo", "https://dev.azure.com/org", "repo", "repo", false},
		{"https://github.com/o/r", "", "", "", true},
		{"https://dev.azure.com/org", "", "", "", true},
		{"", "", "", "", true},
	}
	for _, c := range cases {
		got, err := ParseAzureRemote(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.OrgURL != c.org || got.Project != c.project || got.Repo != c.repo {
			t.Errorf("%q: got %+v", c.in, got)
		}
	}
}

func TestParseErrorDoesNotLeakCredentials(t *testing.T) {
	_, err := ParseAzureRemote("https://user:s3cret@dev.azure.com/org")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeRef(t *testing.T) {
	for in, want := range map[string]string{"refs/heads/feat/x": "feat/x", "main": "main", "refs/heads/a": "a"} {
		if got := normalizeRef(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestDetectAndFactory(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r.git":          ProviderGitHub,
		"git@github.com:o/r.git":              ProviderGitHub,
		"https://dev.azure.com/o/p/_git/r":    ProviderAzure,
		"git@ssh.dev.azure.com:v3/o/p/r":      ProviderAzure,
		"https://o.visualstudio.com/p/_git/r": ProviderAzure,
		"https://bitbucket.org/o/r.git":       ProviderBitbucket,
		"git@github.mycorp.com:o/r.git":       ProviderGitHub,
	}
	for in, want := range cases {
		got, err := DetectProvider(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := DetectProvider("https://gitlab.com/o/r"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("gitlab: %v", err)
	}
	if _, err := ForRemote("https://bitbucket.org/o/r", Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("bitbucket: %v", err)
	}
	if _, err := New("bitbucket", Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("bitbucket New: %v", err)
	}
	if _, err := New("nope", Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("nope: %v", err)
	}
	if p, err := ForRemote("https://dev.azure.com/o/p/_git/r", Options{}); err != nil {
		t.Errorf("azure: %v", err)
	} else if _, ok := p.(*azureProvider); !ok {
		t.Errorf("want azureProvider, got %T", p)
	}
}

func TestUpdatePRBody(t *testing.T) {
	_, calls := fakeCLI(t, "gh")
	p, _ := New("github", Options{RepoDir: t.TempDir()})
	if err := p.UpdatePRBody(context.Background(), &PR{ID: "7"}, "siblings: x"); err != nil {
		t.Fatal(err)
	}
	if !anyCall(calls(), "pr edit 7", "--body siblings: x") {
		t.Fatalf("calls = %v", calls())
	}

	dir2, _ := fakeCLI(t, "az")
	put(t, dir2, "repos_pr_update.exit", "1")
	put(t, dir2, "repos_pr_update.err", "boom")
	if err := azProvider(t).UpdatePRBody(context.Background(), &PR{ID: "42"}, "d"); err == nil {
		t.Fatal("expected az failure to surface")
	}
	_, calls3 := fakeCLI(t, "az")
	if err := azProvider(t).UpdatePRBody(context.Background(), &PR{ID: "42"}, "linked"); err != nil {
		t.Fatal(err)
	}
	if !anyCall(calls3(), "repos pr update", "--id 42", "--description linked", "--organization https://dev.azure.com/org") {
		t.Fatalf("calls = %v", calls3())
	}
}
