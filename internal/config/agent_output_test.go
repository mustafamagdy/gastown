package config

import "testing"

func TestAgentEnvAgentOutput(t *testing.T) {
	townRoot := t.TempDir()
	// No settings file: no GT_OUTPUT.
	if env := AgentEnv(AgentEnvConfig{Role: "mayor", TownRoot: townRoot}); env["GT_OUTPUT"] != "" {
		t.Errorf("GT_OUTPUT = %q without settings", env["GT_OUTPUT"])
	}
	s := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), s); err != nil {
		t.Fatal(err)
	}
	if env := AgentEnv(AgentEnvConfig{Role: "mayor", TownRoot: townRoot}); env["GT_OUTPUT"] != "" {
		t.Errorf("GT_OUTPUT = %q with agent_output unset", env["GT_OUTPUT"])
	}
	s.AgentOutput = "axi"
	if err := SaveTownSettings(TownSettingsPath(townRoot), s); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"mayor", "witness", "polecat", "crew"} {
		env := AgentEnv(AgentEnvConfig{Role: role, Rig: "r", AgentName: "a", TownRoot: townRoot})
		if env["GT_OUTPUT"] != "axi" {
			t.Errorf("%s: GT_OUTPUT = %q, want axi", role, env["GT_OUTPUT"])
		}
	}
	// Any other value leaves the default.
	s.AgentOutput = "text"
	if err := SaveTownSettings(TownSettingsPath(townRoot), s); err != nil {
		t.Fatal(err)
	}
	if env := AgentEnv(AgentEnvConfig{Role: "mayor", TownRoot: townRoot}); env["GT_OUTPUT"] != "" {
		t.Errorf("GT_OUTPUT = %q for agent_output text", env["GT_OUTPUT"])
	}
}
