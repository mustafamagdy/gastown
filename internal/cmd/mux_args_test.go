package cmd

import (
	"reflect"
	"testing"
)

func TestMuxResolveSocketArgs(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	town := func() string { return "gt-abc" }
	none := func() string { return "" }
	tests := []struct {
		name string
		args []string
		env  map[string]string
		find func() string
		want []string
	}{
		{"plain shell", []string{"kill-session", "-t", "x"}, nil, town, []string{"-L", "gt-abc", "kill-session", "-t", "x"}},
		{"explicit -L", []string{"-L", "foo", "ls"}, nil, town, []string{"-L", "foo", "ls"}},
		{"explicit -S", []string{"-S", "/tmp/s", "ls"}, nil, town, []string{"-S", "/tmp/s", "ls"}},
		{"inside TMUX", []string{"ls"}, map[string]string{"TMUX": "/herdr/x,1,0"}, town, []string{"ls"}},
		{"not in town", []string{"ls"}, nil, none, []string{"ls"}},
		{"env override", []string{"ls"}, map[string]string{"GT_TOWN_SOCKET": "envsock"}, none, []string{"-L", "envsock", "ls"}},
		{"-L after command is not global", []string{"new-session", "-L", "x"}, nil, town, []string{"-L", "gt-abc", "new-session", "-L", "x"}},
		{"leading -u kept", []string{"-u", "ls"}, nil, town, []string{"-L", "gt-abc", "-u", "ls"}},
		{"version", []string{"-V"}, nil, town, []string{"-L", "gt-abc", "-V"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := muxResolveSocketArgs(append([]string(nil), tt.args...), env(tt.env), tt.find)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}
