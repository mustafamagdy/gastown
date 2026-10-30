package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	in := strings.Join([]string{
		"# comment",
		"",
		"PLAIN=value",
		"export EXPORTED=yes",
		`DQ="double quoted"`,
		`SQ='single quoted'`,
		"SPACED = padded ",
		"EMPTY=",
		"EQ=a=b=c",
		"INLINE=val # trailing",
		`QUOTEDHASH="a # b"`,
		"NOINTERP=$HOME/x",
		"bad line",
		"=novalue",
		"CRLF=win\r",
		`MISMATCH="open`,
	}, "\n")
	want := map[string]string{
		"PLAIN": "value", "EXPORTED": "yes", "DQ": "double quoted",
		"SQ": "single quoted", "SPACED": "padded", "EMPTY": "", "EQ": "a=b=c",
		"INLINE": "val", "QUOTEDHASH": "a # b", "NOINTERP": "$HOME/x",
		"CRLF": "win", "MISMATCH": `"open`,
	}
	if got := ParseDotenv(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestIsOrgEnvProtected(t *testing.T) {
	for _, n := range []string{"GT_ROLE", "BD_ACTOR", "BEADS_DIR", "TMUX", "TMUX_PANE", "HERDR_ENV",
		"CLAUDE_CONFIG_DIR", "HOME", "USER", "PWD", "SHLVL", "_", "OLDPWD"} {
		if !IsOrgEnvProtected(n) {
			t.Errorf("%s should be protected", n)
		}
	}
	for _, n := range []string{"GH_TOKEN", "AZURE_DEVOPS_EXT_PAT", "PATH", "GITHUB_USER"} {
		if IsOrgEnvProtected(n) {
			t.Errorf("%s should not be protected", n)
		}
	}
}

func writeDotenv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "org.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveOrgEnvFile_ProtectedAndUnchanged(t *testing.T) {
	p := writeDotenv(t, "GH_TOKEN=tok\nGT_ROLE=evil\nBD_ACTOR=evil\nHOME=/evil\nTMUX=x\nSAME=1\nCLAUDE_CONFIG_DIR=/evil\n")
	env, err := ResolveOrgEnvConfig(&OrgEnvConfig{File: p}, []string{"SAME=1", "HOME=/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := env.Names(), []string{"GH_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	if !strings.HasPrefix(env.Source, "file:") {
		t.Errorf("source = %q", env.Source)
	}
}

func TestResolveOrgEnv_AllowList(t *testing.T) {
	p := writeDotenv(t, "A=1\nB=2\nSAME=3\nGT_X=4\n")
	// With an allow-list, a var is taken even if equal to gt's own; protected still blocked.
	env, err := ResolveOrgEnvConfig(&OrgEnvConfig{File: p, Vars: []string{"A", "SAME", "GT_X"}}, []string{"SAME=3"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := env.Names(), []string{"A", "SAME"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

func TestMergePath(t *testing.T) {
	sep := string(os.PathListSeparator)
	cases := []struct{ base, org, want string }{
		{"/a" + sep + "/b", "/x" + sep + "/a", "/x" + sep + "/a" + sep + "/b"},
		{"/a", "/a", "/a"},
		{"/a", "/y" + sep + "/x", "/y" + sep + "/x" + sep + "/a"},
		{"/a" + sep + "/b", "/c", "/c" + sep + "/a" + sep + "/b"},
		{"", "/x", "/x"},
	}
	for _, c := range cases {
		if got := mergePath(c.base, c.org); got != c.want {
			t.Errorf("mergePath(%q,%q) = %q, want %q", c.base, c.org, got, c.want)
		}
	}
}

func TestResolveOrgEnv_PathMergeNeverDrops(t *testing.T) {
	p := writeDotenv(t, "PATH=/org/bin:/usr/bin\n")
	env, err := ResolveOrgEnvConfig(&OrgEnvConfig{File: p}, []string{"PATH=/usr/bin:/local/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := env.Vars["PATH"], "/org/bin:/usr/bin:/local/bin"; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	// Org PATH adds nothing new: PATH is not overridden at all.
	env, _ = ResolveOrgEnvConfig(&OrgEnvConfig{File: p}, []string{"PATH=/org/bin:/usr/bin"})
	if _, ok := env.Vars["PATH"]; ok {
		t.Error("PATH should be absent when org adds no entries")
	}
}

func TestResolveOrgEnv_Validate(t *testing.T) {
	for _, c := range []*OrgEnvConfig{{}, {Direnv: "/a", File: "/b"}} {
		if _, err := ResolveOrgEnvConfig(c, nil); err == nil {
			t.Errorf("%+v should be invalid", c)
		}
	}
	if _, err := ResolveOrgEnvConfig(&OrgEnvConfig{File: "/nonexistent/x.env"}, nil); err == nil {
		t.Error("missing file should error")
	}
}

// fakeDirenv puts a fake `direnv` script on PATH that emits NUL-separated env
// for "direnv exec <dir> env -0".
func fakeDirenv(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "direnv"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestResolveOrgEnvDirenv(t *testing.T) {
	fakeDirenv(t, `[ "$1" = exec ] && [ "$3" = env ] && [ "$4" = "-0" ] || exit 9
printf 'GH_TOKEN=secret\0GT_ROLE=evil\0SAME=1\0AZURE_DEVOPS_EXT_PAT=pat\0PATH=/org/bin:/usr/bin\0'`)
	dir := t.TempDir()
	env, err := ResolveOrgEnvConfig(&OrgEnvConfig{Direnv: dir}, []string{"SAME=1", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AZURE_DEVOPS_EXT_PAT", "GH_TOKEN", "PATH"}
	if got := env.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	if env.Vars["PATH"] != "/org/bin:/usr/bin" {
		t.Errorf("PATH = %q", env.Vars["PATH"])
	}

	env, err = ResolveOrgEnvConfig(&OrgEnvConfig{Direnv: dir, Vars: []string{"GH_TOKEN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Names(); !reflect.DeepEqual(got, []string{"GH_TOKEN"}) {
		t.Errorf("allow-list names = %v", got)
	}
}

func TestResolveOrgEnvDirenv_Failure(t *testing.T) {
	fakeDirenv(t, `echo "direnv: error .envrc is blocked" >&2; exit 1`)
	_, err := ResolveOrgEnvConfig(&OrgEnvConfig{Direnv: t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("err = %v", err)
	}
}

func TestOrgEnvEnviron(t *testing.T) {
	o := &OrgEnv{Vars: map[string]string{"A": "new", "B": "2"}}
	got := o.Environ([]string{"A=old", "C=3"})
	want := []string{"C=3", "A=new", "B=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestTownSettingsOrgEnvRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", "config.json")
	s := NewTownSettings()
	s.OrgEnv = &OrgEnvConfig{Direnv: "~/work/mbg", Vars: []string{"GH_TOKEN"}}
	s.AxiTools = []string{"gh-axi@0.1.37", "lavish-axi"}
	if err := SaveTownSettings(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreateTownSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.OrgEnv, s.OrgEnv) || !reflect.DeepEqual(got.AxiTools, s.AxiTools) {
		t.Errorf("round trip: %+v %v", got.OrgEnv, got.AxiTools)
	}
	if n, v := ParseAxiTool("gh-axi@0.1.37"); n != "gh-axi" || v != "0.1.37" {
		t.Errorf("ParseAxiTool = %q %q", n, v)
	}
	if n, v := ParseAxiTool("lavish-axi"); n != "lavish-axi" || v != "" {
		t.Errorf("ParseAxiTool = %q %q", n, v)
	}
}

func TestAgentEnvIncludesOrgEnv(t *testing.T) {
	ResetOrgEnvCache()
	t.Cleanup(ResetOrgEnvCache)
	townRoot := t.TempDir()
	envFile := writeDotenv(t, "GH_TOKEN=org-token\nGT_ROLE=evil\nBD_ACTOR=evil\n")
	s := NewTownSettings()
	s.OrgEnv = &OrgEnvConfig{File: envFile}
	if err := SaveTownSettings(TownSettingsPath(townRoot), s); err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{"mayor", "polecat"} {
		env := AgentEnv(AgentEnvConfig{Role: role, Rig: "r", AgentName: "a", TownRoot: townRoot})
		if env["GH_TOKEN"] != "org-token" {
			t.Errorf("%s: GH_TOKEN = %q", role, env["GH_TOKEN"])
		}
		if env["GT_ROLE"] == "evil" || env["BD_ACTOR"] == "evil" {
			t.Errorf("%s: org env overrode identity: %q %q", role, env["GT_ROLE"], env["BD_ACTOR"])
		}
	}

	// A town without org_env is unaffected.
	other := t.TempDir()
	if env := AgentEnv(AgentEnvConfig{Role: "mayor", TownRoot: other}); env["GH_TOKEN"] != "" {
		t.Error("GH_TOKEN leaked into a town without org_env")
	}
}

func TestOrgEnvForTown_FailureIsNonFatalAndCached(t *testing.T) {
	ResetOrgEnvCache()
	t.Cleanup(ResetOrgEnvCache)
	townRoot := t.TempDir()
	s := NewTownSettings()
	s.OrgEnv = &OrgEnvConfig{File: filepath.Join(townRoot, "missing.env")}
	if err := SaveTownSettings(TownSettingsPath(townRoot), s); err != nil {
		t.Fatal(err)
	}
	if OrgEnvForTown(townRoot) != nil {
		t.Error("expected nil on failure")
	}
	// Create the file now: the cached failure still holds for this process.
	if err := os.WriteFile(filepath.Join(townRoot, "missing.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if OrgEnvForTown(townRoot) != nil {
		t.Error("result should be cached for the process")
	}
	if env := AgentEnv(AgentEnvConfig{Role: "mayor", TownRoot: townRoot}); env["A"] != "" {
		t.Error("AgentEnv should work without org env")
	}
}
