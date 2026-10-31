package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mqPRFakeCLI installs a fake gh or az on PATH, in the same way as the tests
// in internal/vcs: for each call it derives a key from the leading words
// ("pr create" for gh, "repos pr create" for az), logs the args, and answers
// from files in the returned dir (<key>.json, <key>.exit).
func mqPRFakeCLI(t *testing.T, name string) (dir string, calls func() string) {
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
[ -f "$FAKE_DIR/$key.json" ] && cat "$FAKE_DIR/$key.json"
[ -f "$FAKE_DIR/$key.exit" ] && exit $(cat "$FAKE_DIR/$key.exit")
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	depth := "1"
	if name == "az" {
		depth = "2"
	}
	t.Setenv("FAKE_DEPTH", depth)
	return dir, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		return string(b)
	}
}

func mqPRPut(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mqPRRepo makes a git repo whose origin is remote.
func mqPRRepo(t *testing.T, remote string) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

const (
	mqPRGHOpen = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","headRefName":"polecat/x","baseRefName":"main","headRefOid":"abc123"}`
	mqPRGHDone = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"MERGED","headRefName":"polecat/x","baseRefName":"main","headRefOid":"abc123","mergeCommit":{"oid":"deadbeef"}}`
	mqPRAZOpen = `{"pullRequestId":42,"status":"active","mergeStatus":"succeeded","isDraft":false,
 "sourceRefName":"refs/heads/polecat/x","targetRefName":"refs/heads/main",
 "lastMergeSourceCommit":{"commitId":"abc123"},
 "repository":{"name":"r","webUrl":"https://dev.azure.com/org/My%20Proj/_git/r"}}`
	mqPRAZDone = `{"pullRequestId":42,"status":"completed","mergeStatus":"succeeded",
 "sourceRefName":"refs/heads/polecat/x","targetRefName":"refs/heads/main",
 "lastMergeSourceCommit":{"commitId":"abc123"},"lastMergeCommit":{"commitId":"feedface"},
 "repository":{"webUrl":"https://dev.azure.com/org/My%20Proj/_git/r"}}`
)

// The same verbs work on GitHub and on Azure DevOps: only the remote differs.
func TestMQPRVerbsOnBothProviders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell fake CLIs")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cases := []struct {
		name, cli, remote string
		setup             func(t *testing.T, dir string)
		wantID            string
		wantMerge         string
		wantCalls         []string
	}{
		{
			name: "github", cli: "gh", remote: "https://github.com/o/r.git", wantID: "7", wantMerge: "deadbeef",
			setup: func(t *testing.T, dir string) {
				mqPRPut(t, dir, "pr_list.json", `[]`)
				mqPRPut(t, dir, "pr_create.json", "https://github.com/o/r/pull/7\n")
				mqPRPut(t, dir, "pr_view.json", mqPRGHDone)
				mqPRPut(t, dir, "pr_checks.json", `[{"name":"ci","bucket":"pass"}]`)
			},
			wantCalls: []string{"pr create", "--head polecat/x", "--base main", "pr checks 7", "pr merge 7"},
		},
		{
			name: "azure", cli: "az", remote: "https://org@dev.azure.com/org/My%20Proj/_git/r", wantID: "42", wantMerge: "feedface",
			setup: func(t *testing.T, dir string) {
				mqPRPut(t, dir, "repos_pr_list.json", `[]`)
				mqPRPut(t, dir, "repos_pr_create.json", mqPRAZOpen)
				mqPRPut(t, dir, "repos_pr_show.json", mqPRAZDone)
				mqPRPut(t, dir, "repos_pr_update.json", mqPRAZOpen)
				mqPRPut(t, dir, "repos_pr_policy.json", `[{"status":"approved","configuration":{"isBlocking":true,"isEnabled":true,"type":{"displayName":"Build"},"settings":{"displayName":"ci"}}}]`)
			},
			wantCalls: []string{"repos pr create", "--source-branch polecat/x", "--target-branch main", "repos pr policy list", "repos pr update"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, calls := mqPRFakeCLI(t, c.cli)
			c.setup(t, dir)
			prov, err := mqPRNewProvider(mqPRRepo(t, c.remote), "")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()

			pr, err := mqPRCreate(ctx, prov, "polecat/x", "main", "Title (gt-1)", "body", false)
			if err != nil || pr.ID != c.wantID {
				t.Fatalf("create: %+v, %v", pr, err)
			}
			if _, err := mqPRCreate(ctx, prov, "", "main", "t", "", false); err == nil {
				t.Fatal("create without a source branch must fail")
			}
			res, err := mqPRChecks(ctx, prov, pr.ID, true, time.Second, 10*time.Millisecond)
			if err != nil || res.State != "passing" {
				t.Fatalf("checks: %+v, %v", res, err)
			}
			merged, err := mqPRMerge(ctx, prov, pr.ID, "merge", "")
			if err != nil || merged.State != "merged" || merged.MergeCommit != c.wantMerge {
				t.Fatalf("merge: %+v, %v", merged, err)
			}
			got := calls()
			for _, want := range c.wantCalls {
				if !strings.Contains(got, want) {
					t.Errorf("calls lack %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestMQPRChecksFailingAndTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell fake CLIs")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir, _ := mqPRFakeCLI(t, "gh")
	mqPRPut(t, dir, "pr_view.json", mqPRGHOpen)
	mqPRPut(t, dir, "pr_checks.json", `[{"name":"unit","bucket":"fail"}]`)
	mqPRPut(t, dir, "pr_checks.exit", "1")
	prov, err := mqPRNewProvider(mqPRRepo(t, "https://github.com/o/r.git"), "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := mqPRChecks(context.Background(), prov, "7", false, time.Second, time.Millisecond)
	if err != nil || res.State != "failing" || len(res.Failing) != 1 || res.Failing[0] != "unit" {
		t.Fatalf("failing: %+v, %v", res, err)
	}

	mqPRPut(t, dir, "pr_checks.json", `[{"name":"unit","bucket":"pending"}]`)
	mqPRPut(t, dir, "pr_checks.exit", "8")
	if _, err := mqPRChecks(context.Background(), prov, "7", true, 30*time.Millisecond, 10*time.Millisecond); err == nil {
		t.Fatal("pending checks must time out with an error")
	}
	res, err = mqPRChecks(context.Background(), prov, "7", false, time.Second, time.Millisecond)
	if err != nil || res.State != "pending" {
		t.Fatalf("no wait: %+v, %v", res, err)
	}
}
