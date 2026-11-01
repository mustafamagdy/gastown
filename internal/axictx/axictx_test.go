package axictx

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTool writes an executable shell script named tool into dir.
func fakeTool(t *testing.T, dir, tool, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fakes")
	}
	if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func opts(town, bin string, tools ...string) Options {
	return Options{
		TownRoot: town,
		OrgVars:  map[string]string{"AZURE_CONFIG_DIR": "/org/az"},
		Tools:    tools,
		Env:      []string{"PATH=" + bin + ":/usr/bin:/bin", "ORG_VAR=acme"},
		Timeout:  2 * time.Second,
	}
}

func TestCollect_Success(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `echo "issues: 3 open"`)
	res := Collect(context.Background(), opts(town, bin, "gh-axi@0.1.37"))
	if len(res) != 1 || res[0].Err != nil || res[0].Text != "issues: 3 open" || res[0].Tool != "gh-axi" {
		t.Fatalf("unexpected result: %+v", res)
	}
	st, err := os.Stat(filepath.Join(CacheDir(town), "gh-axi.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v, want 0600", st.Mode().Perm())
	}
	ds, _ := os.Stat(CacheDir(town))
	if ds.Mode().Perm() != 0o700 {
		t.Errorf("cache dir mode = %v, want 0700", ds.Mode().Perm())
	}
}

func TestCollect_NoAxiToolsRunsNothing(t *testing.T) {
	town := t.TempDir()
	o := opts(town, t.TempDir())
	o.Run = func(context.Context, string, []string, string, ...string) ([]byte, error) {
		t.Fatal("runner must not be called")
		return nil, nil
	}
	if res := Collect(context.Background(), o); len(res) != 0 {
		t.Fatalf("got %+v", res)
	}
	// Tools without a session view are skipped and not run.
	o.Tools = []string{"lavish-axi", "quota-axi", "unknown-tool"}
	if res := Collect(context.Background(), o); len(res) != 0 {
		t.Fatalf("got %+v", res)
	}
	if _, err := os.Stat(CacheDir(town)); err == nil {
		t.Error("cache dir must not be created when nothing runs")
	}
}

func TestCollect_NonZeroExit(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "az-axi", `echo "SubscriptionNotFound secret-ish"; exit 1`)
	res := Collect(context.Background(), opts(town, bin, "az-axi"))
	if len(res) != 1 || res[0].Err == nil || res[0].Text != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if strings.Contains(res[0].Err.Error(), "secret") {
		t.Error("error must not carry tool output")
	}
	if _, err := os.Stat(filepath.Join(CacheDir(town), "az-axi.txt")); err == nil {
		t.Error("a failure must not be cached")
	}
}

func TestCollect_MissingTool(t *testing.T) {
	res := Collect(context.Background(), opts(t.TempDir(), t.TempDir(), "gh-axi"))
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCollect_Timeout(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `sleep 30`)
	o := opts(town, bin, "gh-axi")
	o.Timeout = 200 * time.Millisecond
	start := time.Now()
	res := Collect(context.Background(), o)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	if len(res) != 1 || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "timed out") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCollect_CacheHit(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	counter := filepath.Join(t.TempDir(), "count")
	fakeTool(t, bin, "gh-axi", `echo x >> `+counter+`; echo "fresh"`)
	for i := 0; i < 3; i++ {
		res := Collect(context.Background(), opts(town, bin, "gh-axi"))
		if res[0].Text != "fresh" || res[0].Err != nil {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	data, _ := os.ReadFile(counter)
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Fatalf("tool ran %d times, want 1", n)
	}
}

func TestCollect_Expiry(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `echo "new"`)
	cache := filepath.Join(CacheDir(town), "gh-axi.txt")
	if err := os.MkdirAll(CacheDir(town), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	res := Collect(context.Background(), opts(town, bin, "gh-axi"))
	if res[0].Text != "new" {
		t.Fatalf("stale cache not refreshed: %+v", res)
	}
	data, _ := os.ReadFile(cache)
	if string(data) != "new" {
		t.Errorf("cache = %q, want new", data)
	}
}

func TestCollect_StaleCacheNotUsedWhenRefreshFails(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `exit 2`)
	cache := filepath.Join(CacheDir(town), "gh-axi.txt")
	if err := os.MkdirAll(CacheDir(town), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("old data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	res := Collect(context.Background(), opts(town, bin, "gh-axi"))
	if res[0].Err == nil || res[0].Text != "" {
		t.Fatalf("stale data must not be used: %+v", res)
	}
}

func TestCollect_Truncation(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `i=0; while [ $i -lt 200 ]; do echo "line number $i of the view"; i=$((i+1)); done`)
	res := Collect(context.Background(), opts(town, bin, "gh-axi"))
	text := res[0].Text
	if !strings.HasSuffix(text, "\n(truncated)") {
		t.Fatalf("missing note: %q", text[len(text)-30:])
	}
	if len(text) > DefaultMaxBytes+len("\n(truncated)") {
		t.Errorf("len = %d", len(text))
	}
	body := strings.TrimSuffix(text, "\n(truncated)")
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "line number ") || !strings.HasSuffix(l, " of the view") {
			t.Fatalf("cut mid-line: %q", l)
		}
	}
}

func TestCapText(t *testing.T) {
	if got := capText("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := capText(strings.Repeat("é", 20), 9); got != strings.Repeat("é", 4)+"\n(truncated)" {
		t.Errorf("got %q", got)
	}
}

func TestSanitize(t *testing.T) {
	got := sanitize("\x1b[31mred\x1b[0m\x00 ok\r\nnext\ttab\n")
	if got != "[31mred[0m ok\nnext\ttab" {
		t.Errorf("got %q", got)
	}
}

func TestCollect_OrgEnvApplied(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "az-axi", `echo "org=$ORG_VAR cwd=$(pwd)"`)
	res := Collect(context.Background(), opts(town, bin, "az-axi"))
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	real, _ := filepath.EvalSymlinks(town)
	if !strings.HasPrefix(res[0].Text, "org=acme cwd=") || !strings.HasSuffix(res[0].Text, filepath.Base(real)) {
		t.Errorf("got %q", res[0].Text)
	}
}

func TestCollect_OrderFollowsList(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `sleep 0.3; echo gh`)
	fakeTool(t, bin, "az-axi", `echo az`)
	res := Collect(context.Background(), opts(town, bin, "gh-axi", "lavish-axi", "az-axi"))
	if len(res) != 2 || res[0].Tool != "gh-axi" || res[1].Tool != "az-axi" {
		t.Fatalf("got %+v", res)
	}
}

func TestCollect_Parallel(t *testing.T) {
	bin, town := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `sleep 0.6; echo gh`)
	fakeTool(t, bin, "az-axi", `sleep 0.6; echo az`)
	start := time.Now()
	Collect(context.Background(), opts(town, bin, "gh-axi", "az-axi"))
	if d := time.Since(start); d > 1100*time.Millisecond {
		t.Errorf("took %v, tools did not run in parallel", d)
	}
}

func TestAzureViewNeedsOrgAccount(t *testing.T) {
	town, bin := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "az-axi", `echo "subscription: another org"`)
	o := opts(town, bin, "az-axi")
	o.OrgVars = nil
	if res := Collect(context.Background(), o); res != nil {
		t.Fatalf("az-axi ran with no org account: %+v", res)
	}
	if _, err := os.Stat(CacheDir(town)); err == nil {
		t.Fatal("cache written for a skipped tool")
	}
}

func TestPerRepoViewRunsInWorkDir(t *testing.T) {
	town, bin := t.TempDir(), t.TempDir()
	repoA, repoB := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "gh-axi", `pwd`)
	for _, repo := range []string{repoA, repoB} {
		o := opts(town, bin, "gh-axi")
		o.WorkDir = repo
		res := Collect(context.Background(), o)
		if len(res) != 1 || !strings.Contains(res[0].Text, filepath.Base(repo)) {
			t.Fatalf("repo %s: got %+v", repo, res)
		}
	}
}
