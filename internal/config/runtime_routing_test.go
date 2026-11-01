package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectRuntimeRouting(t *testing.T) {
	load := func(rr string) (*RuntimeRoutingPolicy, error) {
		var p ProjectConfig
		doc := `{"type":"project","version":1,"name":"shop","prefix":"shop"`
		if rr != "" {
			doc += `,"runtime_routing":` + rr
		}
		doc += `}`
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatal(err)
		}
		return ProjectRuntimeRouting(&p)
	}

	pol, err := load(`{"default":"auto","hard":"claude","avoid_below_pct":20,"candidates":["claude","codex"],"providers":{"claude-sonnet":"claude"}}`)
	if err != nil || pol == nil {
		t.Fatalf("valid policy: %v %v", pol, err)
	}
	if pol.Provider("claude-sonnet") != "claude" || pol.Provider("codex") != "codex" {
		t.Errorf("provider mapping wrong: %+v", pol)
	}
	if pol, err := load(""); pol != nil || err != nil {
		t.Errorf("no policy: got %v %v", pol, err)
	}
	if pol, err := load(`{"default":"codex"}`); err != nil || pol.Default != "codex" {
		t.Errorf("fixed default: %v %v", pol, err)
	}

	bad := map[string]string{
		`{"default":"auto","candidates":["a"],"typo":1}`: "unknown field",
		`{"default":"auto"}`:                             "candidates",
		`{"default":"auto","candidates":["a"],"avoid_below_pct":101}`: "0-100",
		`{"default":"auto","candidates":["a"],"avoid_below_pct":-1}`:  "0-100",
		`{"candidates":["a"]}`:                                        "default is required",
		`[1]`:                                                         "runtime_routing",
	}
	for in, want := range bad {
		if _, err := load(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestProjectRuntimeRoutingSurvivesRoundTrip(t *testing.T) {
	var p ProjectConfig
	in := `{"type":"project","version":1,"name":"shop","prefix":"shop","runtime_routing":{"default":"codex"}}`
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(&p)
	if err != nil || !strings.Contains(string(out), `"runtime_routing":{"default":"codex"}`) {
		t.Errorf("round trip lost the policy: %s %v", out, err)
	}
}
