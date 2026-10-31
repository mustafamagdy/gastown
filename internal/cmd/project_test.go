package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/templates"
)

func testRigsConfig() *config.RigsConfig {
	return &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"api":  {GitURL: "https://example.com/api.git"},
			"web":  {GitURL: "https://example.com/web.git", Project: "xelta"},
			"solo": {GitURL: "https://example.com/solo.git", Project: "other"},
		},
	}
}

func TestAssignRigsToProject(t *testing.T) {
	t.Parallel()

	t.Run("adds free rigs and is idempotent for same project", func(t *testing.T) {
		t.Parallel()
		rc := testRigsConfig()
		if err := assignRigsToProject(rc, "xelta", []string{"api", "web"}, false); err != nil {
			t.Fatal(err)
		}
		if rc.Rigs["api"].Project != "xelta" || rc.Rigs["web"].Project != "xelta" {
			t.Errorf("rigs not assigned: %+v", rc.Rigs)
		}
	})

	t.Run("unknown rig errors without changes", func(t *testing.T) {
		t.Parallel()
		rc := testRigsConfig()
		err := assignRigsToProject(rc, "xelta", []string{"api", "nope"}, false)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("got %v, want not found", err)
		}
		if rc.Rigs["api"].Project != "" {
			t.Error("api was modified despite error")
		}
	})

	t.Run("rig in another project needs --move", func(t *testing.T) {
		t.Parallel()
		rc := testRigsConfig()
		err := assignRigsToProject(rc, "xelta", []string{"solo"}, false)
		if err == nil || !strings.Contains(err.Error(), "--move") {
			t.Fatalf("got %v, want --move hint", err)
		}
		if err := assignRigsToProject(rc, "xelta", []string{"solo"}, true); err != nil {
			t.Fatal(err)
		}
		if rc.Rigs["solo"].Project != "xelta" {
			t.Error("solo not moved")
		}
	})
}

func TestUnassignRigsFromProject(t *testing.T) {
	t.Parallel()

	rc := testRigsConfig()
	if err := unassignRigsFromProject(rc, "xelta", []string{"api"}); err == nil {
		t.Error("removing a rig that is not in the project should fail")
	}
	if err := unassignRigsFromProject(rc, "xelta", []string{"web", "nope"}); err == nil {
		t.Error("unknown rig should fail")
	}
	if rc.Rigs["web"].Project != "xelta" {
		t.Error("web was modified despite error")
	}
	if err := unassignRigsFromProject(rc, "xelta", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if rc.Rigs["web"].Project != "" {
		t.Error("web not removed")
	}
}

func TestCreateProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell fake bd")
	}
	// Cannot use t.Parallel() due to t.Setenv
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	townRoot := t.TempDir()
	if err := beads.AppendRoute(townRoot, beads.Route{Prefix: "gt-", Path: "gastown"}); err != nil {
		t.Fatal(err)
	}
	rc := testRigsConfig()
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{Name: "xelta", Prefix: "xe-", Docs: "~/docs", Memory: "mbg/xelta", Description: "Xelta platform"}
	if err := createProject(townRoot, cfg, true); err != nil {
		t.Fatalf("createProject: %v", err)
	}

	got, err := config.LoadProjectConfig(townRoot, "xelta")
	if err != nil {
		t.Fatal(err)
	}
	if got.Prefix != "xe" {
		t.Errorf("prefix = %q, want xe (trailing hyphen trimmed)", got.Prefix)
	}
	routes, err := beads.LoadRoutes(filepath.Join(townRoot, ".beads"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range routes {
		if r.Prefix == "xe-" && r.Path == "projects/xelta" {
			found = true
		}
	}
	if !found {
		t.Errorf("route xe- -> projects/xelta missing: %+v", routes)
	}
	// bd warns on every command when .beads is group or world readable.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(townRoot, "projects", "xelta", ".beads"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf(".beads mode = %o, want 700", perm)
		}
	}
	ctxData, err := os.ReadFile(config.ProjectContextPath(townRoot, "xelta"))
	if err != nil {
		t.Fatalf("CONTEXT.md missing: %v", err)
	}
	for _, want := range []string{"# Project: xelta", "Xelta platform", "~/docs", "mbg/xelta", "xe-"} {
		if !strings.Contains(string(ctxData), want) {
			t.Errorf("CONTEXT.md missing %q:\n%s", want, ctxData)
		}
	}

	// Duplicate name, colliding prefix, rig-name clash and bad names all fail.
	cases := []struct {
		name string
		cfg  *config.ProjectConfig
		want string
	}{
		{"duplicate", &config.ProjectConfig{Name: "xelta", Prefix: "zz"}, "already exists"},
		{"prefix of project", &config.ProjectConfig{Name: "other2", Prefix: "xe"}, "already used"},
		{"prefix of rig", &config.ProjectConfig{Name: "other3", Prefix: "gt"}, "already used"},
		{"rig name", &config.ProjectConfig{Name: "api", Prefix: "ap"}, "rig named"},
		{"bad name", &config.ProjectConfig{Name: "a-b", Prefix: "ab"}, "invalid characters"},
		{"bad prefix", &config.ProjectConfig{Name: "okname", Prefix: "1x"}, "invalid project prefix"},
	}
	for _, tc := range cases {
		err := createProject(townRoot, tc.cfg, true)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestWriteProjectContextIfMissing_KeepsExisting(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	cfg := &config.ProjectConfig{Name: "xelta", Prefix: "xe"}
	if err := os.MkdirAll(config.ProjectDir(townRoot, "xelta"), 0755); err != nil {
		t.Fatal(err)
	}
	path := config.ProjectContextPath(townRoot, "xelta")
	if err := os.WriteFile(path, []byte("hand written"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectContextIfMissing(townRoot, cfg); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "hand written" {
		t.Errorf("existing CONTEXT.md overwritten: %q", data)
	}
}

func TestOutputProjectContextFile(t *testing.T) {
	// Not parallel: captures os.Stdout.
	townRoot := t.TempDir()
	rc := testRigsConfig()
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(townRoot, &config.ProjectConfig{Name: "xelta", Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectContextPath(townRoot, "xelta"), []byte("shared project notes"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		outputProjectContextFile(RoleContext{Role: RolePolecat, TownRoot: townRoot, Rig: "web"})
	})
	if !strings.Contains(out, "## Project Context: xelta") || !strings.Contains(out, "shared project notes") {
		t.Errorf("project context missing: %q", out)
	}

	// Rig outside the project: nothing injected.
	out = captureStdout(t, func() {
		outputProjectContextFile(RoleContext{Role: RolePolecat, TownRoot: townRoot, Rig: "api"})
	})
	if out != "" {
		t.Errorf("expected no output for rig without project, got %q", out)
	}
}

func TestRoleProjectContextAndTemplates(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rc := testRigsConfig()
	rc.Rigs["api"] = config.RigEntry{GitURL: "https://example.com/api.git", Project: "xelta"}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectConfig(townRoot, &config.ProjectConfig{Name: "xelta", Prefix: "xe"}); err != nil {
		t.Fatal(err)
	}

	project, siblings, prefix := roleProjectContext(RoleContext{TownRoot: townRoot, Rig: "api"})
	if project != "xelta" || prefix != "xe" || len(siblings) != 1 || siblings[0] != "web" {
		t.Fatalf("got %q %v %q", project, siblings, prefix)
	}

	tmpl, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"polecat", "crew", "witness", "refinery"} {
		withProject, err := tmpl.RenderRole(role, templates.RoleData{Role: role, RigName: "api", Project: project, ProjectRigs: siblings, ProjectPrefix: prefix})
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		for _, want := range []string{"part of project **xelta**", "repos: web", "projects/xelta/CONTEXT.md", "`xe-`"} {
			if !strings.Contains(withProject, want) {
				t.Errorf("%s: missing %q", role, want)
			}
		}
		without, err := tmpl.RenderRole(role, templates.RoleData{Role: role, RigName: "api"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(without, "part of project") {
			t.Errorf("%s: project section rendered without a project", role)
		}
	}
}

func TestSnapshotTownBeadsRestores(t *testing.T) {
	town := t.TempDir()
	path := filepath.Join(town, ".beads", "metadata.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"dolt_database":"hq"}`), 0600); err != nil {
		t.Fatal(err)
	}
	restore, err := snapshotTownBeads(town)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"dolt_database":"shop"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"dolt_database":"hq"}` {
		t.Fatalf("metadata not restored: %s", got)
	}
}
