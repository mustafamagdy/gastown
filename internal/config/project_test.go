package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// writeTestRigs writes mayor/rigs.json with the given rig -> project map.
func writeTestRigs(t *testing.T, townRoot string, rigs map[string]string) {
	t.Helper()
	rc := &RigsConfig{Version: CurrentRigsVersion, Rigs: map[string]RigEntry{}}
	for name, project := range rigs {
		rc.Rigs[name] = RigEntry{GitURL: "https://example.com/" + name + ".git", Project: project}
	}
	if err := SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rc); err != nil {
		t.Fatal(err)
	}
}

func TestProjectConfigRoundTrip(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	in := &ProjectConfig{Name: "xelta", Prefix: "xe", Docs: "~/work/mbg/xelta/docs", Memory: "mbg/xelta", Description: "Xelta platform"}
	if err := SaveProjectConfig(townRoot, in); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "projects", "xelta", "project.json")); err != nil {
		t.Fatalf("project.json not at expected path: %v", err)
	}

	out, err := LoadProjectConfig(townRoot, "xelta")
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch: got %+v, want %+v", out, in)
	}
	if out.Type != "project" || out.Version != CurrentProjectVersion {
		t.Errorf("defaults not set: %+v", out)
	}
}

func TestLoadProjectConfig_NotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadProjectConfig(t.TempDir(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestProjectValidation(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "a-b", "a.b", "a b", "a/b", "hq", "HQ"} {
		if err := ValidateProjectName(name); err == nil {
			t.Errorf("ValidateProjectName(%q) = nil, want error", name)
		}
	}
	if err := ValidateProjectName("my_proj1"); err != nil {
		t.Errorf("ValidateProjectName(my_proj1) = %v", err)
	}
	for _, prefix := range []string{"", "1x", "x y", "x;rm"} {
		if err := SaveProjectConfig(t.TempDir(), &ProjectConfig{Name: "p", Prefix: prefix}); err == nil {
			t.Errorf("SaveProjectConfig with prefix %q = nil, want error", prefix)
		}
	}
}

func TestListProjects(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	got, err := ListProjects(townRoot)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty town: got %v, %v", got, err)
	}

	for _, n := range []string{"zeta", "alpha", "mid"} {
		if err := SaveProjectConfig(townRoot, &ProjectConfig{Name: n, Prefix: n[:2]}); err != nil {
			t.Fatal(err)
		}
	}
	// A directory without project.json is not a project.
	if err := os.MkdirAll(filepath.Join(ProjectsDir(townRoot), "stray"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err = ListProjects(townRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestProjectForRig(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	// No rigs.json: not an error, no project.
	if got, err := ProjectForRig(townRoot, "api"); err != nil || got != "" {
		t.Errorf("no rigs.json: got %q, %v", got, err)
	}

	writeTestRigs(t, townRoot, map[string]string{"api": "xelta", "web": "xelta", "solo": ""})

	for rig, want := range map[string]string{"api": "xelta", "web": "xelta", "solo": "", "missing": ""} {
		got, err := ProjectForRig(townRoot, rig)
		if err != nil || got != want {
			t.Errorf("ProjectForRig(%q) = %q, %v; want %q", rig, got, err, want)
		}
	}

	rc, err := LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := RigsInProject(rc, "xelta"); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("RigsInProject = %v", got)
	}
}

func TestLoadRoleDirective_ProjectTierOrdering(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	writeTestRigs(t, townRoot, map[string]string{"api": "xelta", "other": ""})

	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(townRoot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("directives/polecat.md", "town")
	write("projects/xelta/directives/polecat.md", "project")
	write("api/directives/polecat.md", "rig")
	write("other/directives/polecat.md", "other rig")

	if got := LoadRoleDirective("polecat", townRoot, "api"); got != "town\nproject\nrig" {
		t.Errorf("api: got %q, want town, project, rig in order", got)
	}
	// Rig outside the project does not get project directives.
	if got := LoadRoleDirective("polecat", townRoot, "other"); got != "town\nother rig" {
		t.Errorf("other: got %q", got)
	}
}

func TestAgentEnv_Project(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	writeTestRigs(t, townRoot, map[string]string{"api": "xelta", "solo": ""})

	for _, role := range []string{constants.RoleWitness, constants.RoleRefinery, constants.RolePolecat, constants.RoleCrew} {
		env := AgentEnv(AgentEnvConfig{Role: role, Rig: "api", AgentName: "a", TownRoot: townRoot})
		if env["GT_PROJECT"] != "xelta" {
			t.Errorf("%s: GT_PROJECT = %q, want xelta", role, env["GT_PROJECT"])
		}
		env = AgentEnv(AgentEnvConfig{Role: role, Rig: "solo", AgentName: "a", TownRoot: townRoot})
		if _, ok := env["GT_PROJECT"]; ok {
			t.Errorf("%s: GT_PROJECT set for rig without project", role)
		}
	}

	// Town-level roles never get GT_PROJECT.
	env := AgentEnv(AgentEnvConfig{Role: constants.RoleMayor, TownRoot: townRoot})
	if _, ok := env["GT_PROJECT"]; ok {
		t.Error("mayor: GT_PROJECT should not be set")
	}

	// Explicit Project wins over lookup.
	env = AgentEnv(AgentEnvConfig{Role: constants.RolePolecat, Rig: "solo", AgentName: "a", TownRoot: townRoot, Project: "forced"})
	if env["GT_PROJECT"] != "forced" {
		t.Errorf("explicit project: got %q", env["GT_PROJECT"])
	}
}
