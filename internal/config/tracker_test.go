package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectTracker(t *testing.T) {
	load := func(tr string) (*TrackerConfig, error) {
		var p ProjectConfig
		doc := `{"type":"project","version":1,"name":"shop","prefix":"shop"`
		if tr != "" {
			doc += `,"tracker":` + tr
		}
		doc += `}`
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatal(err)
		}
		return ProjectTracker(&p)
	}

	az, err := load(`{"kind":"azure","org":"geeksclub","project":"gt-pr-test","ready_tag":"gt-ready","active_state":"Active","done_state":"Resolved"}`)
	if err != nil || az == nil || az.OrgURL() != "https://dev.azure.com/geeksclub" {
		t.Fatalf("azure: %+v %v", az, err)
	}
	gh, err := load(`{"kind":"github","repo":"owner/name"}`)
	if err != nil || gh.ReadyLabel != "gt-ready" {
		t.Fatalf("github default label: %+v %v", gh, err)
	}
	if tr, err := load(""); tr != nil || err != nil {
		t.Errorf("no tracker: %v %v", tr, err)
	}

	bad := map[string]string{
		`{"kind":"azure","org":"o","project":"p","active_state":"A","done_state":"R","typo":1}`:     "unknown field",
		`{"kind":"azure","org":"o","project":"p","done_state":"R"}`:                                 "active_state is required",
		`{"kind":"azure","org":"o","project":"p","active_state":"A"}`:                               "done_state is required",
		`{"kind":"azure","project":"p","active_state":"A","done_state":"R"}`:                        "org is required",
		`{"kind":"azure","org":"o","project":"p","active_state":"A","done_state":"A"}`:              "must differ",
		`{"kind":"azure","org":"o","project":"p","active_state":"A","done_state":"R","repo":"a/b"}`: "belong to kind",
		`{"kind":"github","repo":"nope"}`:                                                           "owner/name",
		`{"kind":"github","repo":"a/b","done_state":"x"}`:                                           "belong to kind",
		`{"kind":"jira"}`: "unknown kind",
		`{}`:              "kind is required",
		`[1]`:             "tracker",
	}
	for in, want := range bad {
		if _, err := load(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}
