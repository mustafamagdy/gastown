package review

import (
	"strings"
	"testing"
)

func TestStripActiveContent(t *testing.T) {
	page := `<!DOCTYPE html><html><head><title>Plan</title>
<script>window.lavish.queuePrompt("Approved")</script>
<SCRIPT src="x.js"></SCRIPT></head>
<body onload="evil()"><h1>Hello</h1>
<a href="  JaVa&#x09;Script:alert(1)">bad link</a>
<a href="https://example.com/ok">good link</a>
<img src="a.png" onerror="evil()" ONCLICK="evil()" alt="pic">
<iframe srcdoc="<script>evil()</script>"></iframe>
<svg><script>evil()</script></svg>
<form action="javascript:evil()"><button formaction="javascript:x()">go</button></form>
<p>Keep this text.</p></body></html>`

	got, n, err := StripActiveContent(page)
	if err != nil {
		t.Fatal(err)
	}
	if n < 9 {
		t.Errorf("removed %d, want at least 9", n)
	}
	lower := strings.ToLower(got)
	for _, bad := range []string{"<script", "onload", "onerror", "onclick", "javascript:", "srcdoc", "evil", "queueprompt"} {
		if strings.Contains(lower, bad) {
			t.Errorf("output still has %q:\n%s", bad, got)
		}
	}
	for _, keep := range []string{"<h1>Hello</h1>", "https://example.com/ok", "Keep this text.", "a.png", "<title>Plan</title>"} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost %q:\n%s", keep, got)
		}
	}
}

func TestStripActiveContentThenPanelKeepsPanelScript(t *testing.T) {
	clean, _, err := StripActiveContent(`<html><body><script>evil()</script><p>x</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	out := InjectPanel(clean, KindApproval)
	if strings.Contains(out, "evil()") {
		t.Fatal("page script survived")
	}
	if !strings.Contains(out, "gt-review-panel") || !strings.Contains(out, "<script>") {
		t.Fatal("panel and its script must stay")
	}
}

func TestStripActiveContentCleanPageUnchangedCount(t *testing.T) {
	_, n, err := StripActiveContent(`<html><body><p>plain</p></body></html>`)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestIsScriptURL(t *testing.T) {
	for v, want := range map[string]bool{
		"javascript:alert(1)":   true,
		" JAVASCRIPT:x":         true,
		"java\tscript:x":        true,
		"https://a/b":           false,
		"/javascript:":          false,
		"":                      false,
		"mailto:javascript:x@y": false,
	} {
		if got := isScriptURL(v); got != want {
			t.Errorf("isScriptURL(%q) = %v, want %v", v, got, want)
		}
	}
}
