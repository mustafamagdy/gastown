package review

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// StripActiveContent removes what could run script from an HTML page: script
// elements, inline on* event handlers, javascript: URLs and srcdoc frames. It
// returns the cleaned page and how many things it removed. The page is parsed
// with a real HTML parser, so odd spelling and nesting cannot hide a script.
// If the page cannot be parsed or written back, it returns an error and the
// caller must not use the page.
//
// It is used for approval reviews, where a script in the agent's page must not
// be able to submit the decision. Call it before InjectPanel, whose own script
// is added afterwards.
func StripActiveContent(page string) (string, int, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return "", 0, fmt.Errorf("parsing page: %w", err)
	}
	removed := stripNode(doc)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return "", 0, fmt.Errorf("writing page: %w", err)
	}
	return buf.String(), removed, nil
}

func stripNode(n *html.Node) int {
	removed := 0
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && strings.EqualFold(c.Data, "script") {
			n.RemoveChild(c)
			removed++
		} else {
			if c.Type == html.ElementNode {
				removed += stripAttrs(c)
			}
			removed += stripNode(c)
		}
		c = next
	}
	return removed
}

func stripAttrs(n *html.Node) int {
	kept := n.Attr[:0]
	removed := 0
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if strings.HasPrefix(key, "on") || key == "srcdoc" || isScriptURL(a.Val) {
			removed++
			continue
		}
		kept = append(kept, a)
	}
	n.Attr = kept
	return removed
}

// isScriptURL reports whether v is a javascript: URL. Browsers ignore white
// space and control characters inside the scheme, so they are dropped first.
func isScriptURL(v string) bool {
	var b strings.Builder
	for _, r := range v {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		if b.Len() >= len("javascript:") {
			break
		}
	}
	return b.String() == "javascript:"
}
