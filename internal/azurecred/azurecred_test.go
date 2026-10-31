package azurecred

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useTempConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestSaveLoadDelete(t *testing.T) {
	useTempConfig(t)
	if err := Save("GeeksClub", " secret\n"); err != nil {
		t.Fatal(err)
	}
	got, err := Load("geeksclub")
	if err != nil || got != "secret" {
		t.Fatalf("Load = %q, %v", got, err)
	}

	dir, _ := Dir()
	info, err := os.Stat(filepath.Join(dir, "geeksclub.pat"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", info.Mode().Perm())
	}

	entries, err := List()
	if err != nil || len(entries) != 1 || entries[0].Org != "geeksclub" {
		t.Fatalf("List = %v, %v", entries, err)
	}

	if existed, err := Delete("geeksclub"); err != nil || !existed {
		t.Fatalf("Delete = %v, %v", existed, err)
	}
	if got, err := Load("geeksclub"); err != nil || got != "" {
		t.Fatalf("Load after delete = %q, %v", got, err)
	}
}

func TestRejectsBadInput(t *testing.T) {
	useTempConfig(t)
	for _, org := range []string{"", "../evil", "a/b", ".hidden", "-x"} {
		if err := Save(org, "t"); err == nil {
			t.Errorf("Save(%q) succeeded", org)
		}
	}
	if err := Save("org", "  "); err == nil {
		t.Error("empty token accepted")
	}
	if err := Save("org", "a\nb"); err == nil {
		t.Error("multi-line token accepted")
	}
}

func TestAnswerGit(t *testing.T) {
	useTempConfig(t)
	if err := Save("orga", "token-a"); err != nil {
		t.Fatal(err)
	}
	if err := Save("orgb", "token-b"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, request, want string
	}{
		{"path", "protocol=https\nhost=dev.azure.com\npath=orga/proj/_git/repo\n", "token-a"},
		{"other org in path", "protocol=https\nhost=dev.azure.com\nusername=orga\npath=orgb/proj/_git/repo\n", "token-b"},
		{"username only", "protocol=https\nhost=dev.azure.com\nusername=orgb\n", "token-b"},
		{"visualstudio host", "protocol=https\nhost=OrgA.visualstudio.com\npath=proj/_git/repo\n", "token-a"},
		{"unknown org", "protocol=https\nhost=dev.azure.com\npath=client/proj/_git/repo\n", ""},
		{"other host", "protocol=https\nhost=github.com\npath=orga/repo\n", ""},
		{"lookalike host", "protocol=https\nhost=dev.azure.com.evil.test\npath=orga/repo\n", ""},
		{"plain http", "protocol=http\nhost=dev.azure.com\npath=orga/proj/_git/repo\n", ""},
		{"path traversal", "protocol=https\nhost=dev.azure.com\npath=../orga/x\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := AnswerGit(strings.NewReader(tt.request), &out); err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if out.Len() != 0 {
					t.Fatalf("answered %q, want nothing", out.String())
				}
				return
			}
			if want := "username=pat\npassword=" + tt.want + "\n"; out.String() != want {
				t.Fatalf("answered %q, want %q", out.String(), want)
			}
		})
	}
}
