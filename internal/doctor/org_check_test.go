package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func townWithSettings(t *testing.T, mutate func(*config.TownSettings)) string {
	t.Helper()
	root := t.TempDir()
	s := config.NewTownSettings()
	mutate(s)
	if err := config.SaveTownSettings(config.TownSettingsPath(root), s); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOrgEnvCheck(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "org.env")
	if err := os.WriteFile(envFile, []byte("GH_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		org    *config.OrgEnvConfig
		status CheckStatus
	}{
		{"unconfigured", nil, StatusOK},
		{"ok", &config.OrgEnvConfig{File: envFile}, StatusOK},
		{"missing file", &config.OrgEnvConfig{File: envFile + ".nope"}, StatusWarning},
		{"empty result", &config.OrgEnvConfig{File: envFile, Vars: []string{"OTHER"}}, StatusWarning},
		{"invalid", &config.OrgEnvConfig{}, StatusWarning},
	}
	for _, c := range cases {
		root := townWithSettings(t, func(s *config.TownSettings) { s.OrgEnv = c.org })
		r := NewOrgEnvCheck().Run(&CheckContext{TownRoot: root})
		if r.Status != c.status {
			t.Errorf("%s: status %v (%s), want %v", c.name, r.Status, r.Message, c.status)
		}
		for _, d := range append(r.Details, r.Message) {
			if strings.Contains(d, "x\n") {
				t.Errorf("%s: leaked value", c.name)
			}
		}
	}
}

// fakeTool writes an executable script into a fresh bin dir and returns the dir.
func fakeTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func TestAxiToolsCheck(t *testing.T) {
	fakeTools(t, map[string]string{
		"good-axi":   `[ "$1" = "--version" ] && echo "good-axi 1.2.3" || echo "home view"`,
		"old-axi":    `[ "$1" = "--version" ] && echo "old-axi 0.9.0" || echo "home view"`,
		"broken-axi": `[ "$1" = "--version" ] && { echo "broken-axi 1.0.0"; exit 0; }; echo "SubscriptionNotFound: stale" >&2; echo "second line" >&2; exit 1`,
	})
	run := func(tools ...string) *CheckResult {
		root := townWithSettings(t, func(s *config.TownSettings) { s.AxiTools = tools })
		return NewAxiToolsCheck().Run(&CheckContext{TownRoot: root})
	}

	if r := run(); r.Status != StatusOK || !strings.Contains(r.Message, "not configured") {
		t.Errorf("unconfigured: %v %s", r.Status, r.Message)
	}
	if r := run("good-axi", "good-axi@1.2.3"); r.Status != StatusOK {
		t.Errorf("pass: %v %s %v", r.Status, r.Message, r.Details)
	}
	r := run("old-axi@1.0.0")
	if r.Status != StatusError || !strings.Contains(strings.Join(r.Details, "\n"), "pinned 1.0.0") {
		t.Errorf("wrong version: %v %v", r.Status, r.Details)
	}
	r = run("broken-axi")
	if r.Status != StatusError || !strings.Contains(strings.Join(r.Details, "\n"), "SubscriptionNotFound") ||
		strings.Contains(strings.Join(r.Details, "\n"), "second line") {
		t.Errorf("home failure should report first error line only: %v %v", r.Status, r.Details)
	}
	r = run("missing-axi-tool")
	if r.Status != StatusError || !strings.Contains(strings.Join(r.Details, "\n"), "not found on PATH") {
		t.Errorf("missing: %v %v", r.Status, r.Details)
	}
}

func TestAxiToolsCheck_InjectedRunnerAndOrgEnv(t *testing.T) {
	bin := fakeTools(t, map[string]string{"env-axi": "exit 0"})
	envFile := filepath.Join(t.TempDir(), "org.env")
	if err := os.WriteFile(envFile, []byte("GH_TOKEN=orgtok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := townWithSettings(t, func(s *config.TownSettings) {
		s.AxiTools = []string{"env-axi"}
		s.OrgEnv = &config.OrgEnvConfig{File: envFile}
	})
	var gotDir, gotTok string
	c := NewAxiToolsCheck()
	c.run = func(_ context.Context, dir string, env []string, b string, args ...string) ([]byte, error) {
		gotDir, gotTok = dir, envValue(env, "GH_TOKEN")
		if b != filepath.Join(bin, "env-axi") || len(args) != 0 {
			return nil, errors.New("unexpected call")
		}
		return nil, nil
	}
	if r := c.Run(&CheckContext{TownRoot: root}); r.Status != StatusOK {
		t.Fatalf("%v %s %v", r.Status, r.Message, r.Details)
	}
	if gotDir != root || gotTok != "orgtok" {
		t.Errorf("runner dir=%q tok=%q", gotDir, gotTok)
	}
}

func TestVersionMatches(t *testing.T) {
	for out, want := range map[string]bool{
		"gh-axi 0.1.37": true, "v0.1.37": true, "0.1.37\n": true,
		"gh-axi 0.1.370": false, "0.1.3": false, "version=0.1.37": true,
	} {
		if got := versionMatches(out, map[bool]string{true: "0.1.37", false: "0.1.37"}[want]); got != want {
			t.Errorf("versionMatches(%q) = %v, want %v", out, got, want)
		}
	}
}
