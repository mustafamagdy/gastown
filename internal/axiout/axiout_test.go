package axiout

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"two words":   "two words",
		"":            `""`,
		" pad":        `" pad"`,
		"a,b":         `"a,b"`,
		"k: v":        `"k: v"`,
		"-x":          `"-x"`,
		"123":         `"123"`,
		"1.5":         `"1.5"`,
		"true":        `"true"`,
		"null":        `"null"`,
		`say "hi"`:    `"say \"hi\""`,
		"line\nbreak": `"line\nbreak"`,
		"a[0]":        `"a[0]"`,
		"gt-abc12":    "gt-abc12",
		"rig/Toast":   "rig/Toast",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestWriterExact(t *testing.T) {
	w := New()
	w.KV("town", "demo")
	w.KV("rigs", 2)
	w.KV("ok", true)
	w.Table("polecats", []string{"name", "state"}, [][]any{{"rig/a", "working"}, {"rig/b", "idle, stale"}}, 0)
	w.List("blocking", []string{"one", "two"})
	w.Help("gt status", "", "gt hook")
	want := "town: demo\nrigs: 2\nok: true\n" +
		"polecats[2]{name,state}:\n  rig/a,working\n  rig/b,\"idle, stale\"\n" +
		"blocking[2]:\n  one\n  two\n" +
		"help[2]:\n  gt status\n  gt hook\n"
	if got := w.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTableEmptyIsDefinite(t *testing.T) {
	w := New()
	w.Table("polecats", []string{"name"}, nil, 0)
	w.List("blocking", nil)
	w.Help()
	if got, want := w.String(), "polecats: 0\nblocking: 0\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTableRowCap(t *testing.T) {
	var rows [][]any
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		rows = append(rows, []any{n})
	}
	w := New()
	w.Table("items", []string{"id"}, rows, 3)
	want := "items[3]{id}:\n  a\n  b\n  c\nshowing: 3 of 5\n"
	if got := w.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// At the cap exactly: no count line.
	w = New()
	w.Table("items", []string{"id"}, rows[:3], 3)
	if got, want := w.String(), "items[3]{id}:\n  a\n  b\n  c\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTableTypedCells(t *testing.T) {
	w := New()
	w.Table("t", []string{"a", "n", "ok", "s"}, [][]any{{"x", 3, true, "7"}}, 0)
	if got, want := w.String(), "t[1]{a,n,ok,s}:\n  x,3,true,\"7\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("abcdefghijkl", 8); got != "abcde..." {
		t.Errorf("got %q", got)
	}
	if got := Truncate("a\n  b\tc", 20); got != "a b c" {
		t.Errorf("got %q", got)
	}
}

func TestNoANSI(t *testing.T) {
	w := New()
	w.KV("k", "v")
	w.Table("t", []string{"a"}, [][]any{{"x"}}, 0)
	w.Help("gt x")
	for _, r := range w.String() {
		if r == 0x1b {
			t.Fatal("ANSI escape in output")
		}
	}
}

func TestWanted(t *testing.T) {
	t.Setenv(EnvVar, "")
	if Wanted(false, false) {
		t.Error("default must not be axi")
	}
	if !Wanted(true, false) {
		t.Error("flag should select axi")
	}
	if Wanted(true, true) {
		t.Error("--json must win over --axi")
	}
	t.Setenv(EnvVar, "axi")
	if !Wanted(false, false) {
		t.Error("env should select axi")
	}
	if Wanted(false, true) {
		t.Error("--json must win over env")
	}
	t.Setenv(EnvVar, "text")
	if Wanted(false, false) {
		t.Error("other env values are ignored")
	}
}
