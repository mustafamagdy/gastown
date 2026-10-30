package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func writeBrainFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderProjectBrainMemory(t *testing.T) {
	brain := t.TempDir()
	t.Setenv("AGENT_BRAIN_DIR", brain)
	dir := filepath.Join(brain, "memory", "mbg", "xelta")
	cfg := &config.ProjectConfig{Name: "shop", Memory: "mbg/xelta"}

	if got := renderProjectBrain(cfg); got != "" {
		t.Fatalf("missing folder should print nothing, got %q", got)
	}
	if got := renderProjectBrain(&config.ProjectConfig{Name: "shop"}); got != "" {
		t.Fatalf("empty memory should print nothing, got %q", got)
	}

	var idx strings.Builder
	idx.WriteString("# Memory\n\n")
	for i := 0; i < 45; i++ {
		idx.WriteString("- [Fact " + string(rune('A'+i%26)) + "](f.md): hook\n")
	}
	writeBrainFile(t, filepath.Join(dir, "MEMORY.md"), idx.String())
	got := renderProjectBrain(cfg)
	if !strings.Contains(got, "## Project memory (mbg/xelta)") ||
		!strings.Contains(got, "- and 5 more in "+dir) ||
		!strings.Contains(got, "Read the linked files from "+dir) ||
		!strings.Contains(got, "never secrets") {
		t.Errorf("memory section:\n%s", got)
	}
	if n := strings.Count(got, "\n"); n > 50 {
		t.Errorf("section has %d lines, want <= 50", n)
	}
	if strings.Contains(got, "# Memory") {
		t.Errorf("heading line of the index should be dropped")
	}

	// Folder without an index still tells the agent where to save.
	writeBrainFile(t, filepath.Join(dir, "MEMORY.md"), "")
	if got := renderProjectBrain(cfg); !strings.Contains(got, "no entries yet") {
		t.Errorf("empty index: %q", got)
	}
}

func TestRenderProjectBrainDocs(t *testing.T) {
	t.Setenv("AGENT_BRAIN_DIR", t.TempDir())
	docs := t.TempDir()
	for i := 0; i < 17; i++ {
		writeBrainFile(t, filepath.Join(docs, "doc"+string(rune('a'+i))+".md"), "x")
	}
	writeBrainFile(t, filepath.Join(docs, ".hidden"), "x")
	writeBrainFile(t, filepath.Join(docs, "sub", "nested.md"), "x")
	got := renderProjectBrain(&config.ProjectConfig{Name: "shop", Docs: docs})
	if !strings.Contains(got, "Project docs: "+docs) || !strings.Contains(got, "doca.md") ||
		strings.Contains(got, "docq.md") || !strings.Contains(got, "and 2 more") ||
		strings.Contains(got, "nested") || strings.Contains(got, ".hidden") {
		t.Errorf("docs listing:\n%s", got)
	}
	if got := renderProjectBrain(&config.ProjectConfig{Name: "shop", Docs: filepath.Join(docs, "nope")}); got != "" {
		t.Errorf("missing docs dir: %q", got)
	}
}

func TestPrimeProjectBrainSection(t *testing.T) {
	// Not parallel: captures os.Stdout.
	town := mapTestTown(t)
	brain := t.TempDir()
	t.Setenv("AGENT_BRAIN_DIR", brain)
	writeBrainFile(t, filepath.Join(brain, "memory", "mbg", "xelta", "MEMORY.md"), "- [A](a.md): hook\n")
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh", Memory: "mbg/xelta"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { outputProjectBrainContext(RoleContext{TownRoot: town, Rig: "orders_svc"}) })
	if !strings.Contains(out, "- [A](a.md): hook") {
		t.Errorf("prime output: %q", out)
	}
	if out := captureStdout(t, func() { outputProjectBrainContext(RoleContext{TownRoot: town, Rig: "other"}) }); out != "" {
		t.Errorf("non-project rig: %q", out)
	}
}

func TestRenderNeighbourCheckouts(t *testing.T) {
	town := t.TempDir()
	pdir := filepath.Join(town, "orders_svc", "polecats", "toast")
	if got := renderNeighbourCheckouts(town, "orders_svc", "toast"); got != "" {
		t.Fatalf("no checkouts: %q", got)
	}
	if got := renderNeighbourCheckouts(town, "orders_svc", ""); got != "" {
		t.Fatalf("not a polecat: %q", got)
	}
	// A real checkout dir is needed for the commit lookup.
	nb := filepath.Join(pdir, "neighbours", "billing_svc")
	if err := os.MkdirAll(nb, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, nb, "init", "-q", "-b", "main")
	runGit(t, nb, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c")
	got := renderNeighbourCheckouts(town, "orders_svc", "toast")
	if !strings.Contains(got, "Read-only code: ../neighbours/billing_svc (") || !strings.Contains(got, "not for edits") {
		t.Errorf("neighbour lines: %q", got)
	}
}

func TestBrainRules(t *testing.T) {
	town := mapTestTown(t)
	if _, err := renderBrainRules(town, "shop"); err == nil || !strings.Contains(err.Error(), "memory key") {
		t.Fatalf("expected memory error, got %v", err)
	}
	if err := config.SaveProjectConfig(town, &config.ProjectConfig{Name: "shop", Prefix: "sh", Memory: "mbg/xelta"}); err != nil {
		t.Fatal(err)
	}
	got, err := renderBrainRules(town, "shop")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", got)
	}
	want := `{ path = "` + town + `/billing_svc", org = "mbg", project = "xelta" },`
	if lines[0] != want {
		t.Errorf("line 0 = %q, want %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[2], "# ") || !strings.Contains(lines[2], "more specific") {
		t.Errorf("trailing comment: %q", lines[2])
	}
	if _, err := renderBrainRules(town, "nope"); err == nil {
		t.Error("expected error for unknown project")
	}
}

func TestBrainRulesHomeTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	town := mapTestTown(t)
	// mapTestTown is outside HOME; build a town under it instead.
	under := filepath.Join(home, "gt")
	if err := os.Rename(town, under); err != nil {
		t.Skipf("cannot move town: %v", err)
	}
	if err := config.SaveProjectConfig(under, &config.ProjectConfig{Name: "shop", Prefix: "sh", Memory: "mbg/xelta"}); err != nil {
		t.Fatal(err)
	}
	got, err := renderBrainRules(under, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `{ path = "~/gt/billing_svc"`) {
		t.Errorf("tilde: %q", got)
	}
}
