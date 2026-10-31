package review

// A small reader for the part of the TOON format that `lavish-axi poll` prints
// on stdout. lavish-axi has no JSON mode: its only machine output is TOON
// (axi-sdk-js 0.1.13, indentation of 2 spaces). This reader covers what poll
// emits: nested objects, inline arrays, list arrays ("- item") and tabular
// arrays ("key[N]{a,b}:" followed by rows). Every scalar is kept as a string.
// A contract test pins the exact poll output against the real lavish-axi.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type toonLine struct {
	indent int
	text   string
}

var toonHeader = regexp.MustCompile(`^("(?:[^"\\]|\\.)*"|[^\[\]{}:"]+)(?:\[(\d+)[^\]]*\])?(?:\{([^}]*)\})?:(.*)$`)

// decodeTOON parses a document whose root is an object.
func decodeTOON(doc string) (map[string]any, error) {
	var lines []toonLine
	for _, raw := range strings.Split(doc, "\n") {
		raw = strings.TrimRight(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		trimmed := strings.TrimLeft(raw, " ")
		lines = append(lines, toonLine{indent: len(raw) - len(trimmed), text: trimmed})
	}
	p := &toonParser{lines: lines}
	obj, err := p.object(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, fmt.Errorf("unexpected content at line %d: %q", p.pos+1, p.lines[p.pos].text)
	}
	return obj, nil
}

type toonParser struct {
	lines []toonLine
	pos   int
}

// object reads consecutive key lines at exactly the given indent.
func (p *toonParser) object(indent int) (map[string]any, error) {
	obj := map[string]any{}
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("unexpected indent at %q", ln.text)
		}
		m := toonHeader.FindStringSubmatch(ln.text)
		if m == nil {
			return nil, fmt.Errorf("cannot read line %q", ln.text)
		}
		key, err := toonKey(m[1])
		if err != nil {
			return nil, err
		}
		p.pos++
		rest := strings.TrimSpace(m[4])
		switch {
		case m[2] != "" && m[3] != "": // tabular array
			n, _ := strconv.Atoi(m[2])
			cols, err := splitTOONRow(m[3])
			if err != nil {
				return nil, err
			}
			rows := make([]any, 0, n)
			for len(rows) < n && p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				vals, err := splitTOONRow(p.lines[p.pos].text)
				if err != nil {
					return nil, err
				}
				p.pos++
				row := map[string]any{}
				for i, c := range cols {
					c, _ = toonKey(c)
					if i < len(vals) {
						row[c] = vals[i]
					}
				}
				rows = append(rows, row)
			}
			obj[key] = rows
		case m[2] != "": // inline or list array
			if rest != "" {
				vals, err := splitTOONRow(rest)
				if err != nil {
					return nil, err
				}
				arr := make([]any, len(vals))
				for i, v := range vals {
					arr[i] = v
				}
				obj[key] = arr
				break
			}
			arr, err := p.list(indent + 2)
			if err != nil {
				return nil, err
			}
			obj[key] = arr
		case rest != "":
			v, err := toonScalar(rest)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		default: // nested object (possibly empty)
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				sub, err := p.object(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				obj[key] = sub
			} else {
				obj[key] = map[string]any{}
			}
		}
	}
	return obj, nil
}

// list reads "- item" lines at the given indent.
func (p *toonParser) list(indent int) ([]any, error) {
	var arr []any
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent != indent || !(ln.text == "-" || strings.HasPrefix(ln.text, "- ")) {
			break
		}
		content := strings.TrimSpace(strings.TrimPrefix(ln.text, "-"))
		if content == "" {
			p.pos++
			arr = append(arr, map[string]any{})
			continue
		}
		if toonHeader.MatchString(content) {
			// First field of an object sits on the hyphen line; the rest are
			// indented to align with it. Treat the hyphen line as a key line
			// two columns deeper.
			p.lines[p.pos] = toonLine{indent: indent + 2, text: content}
			obj, err := p.object(indent + 2)
			if err != nil {
				return nil, err
			}
			arr = append(arr, obj)
			continue
		}
		v, err := toonScalar(content)
		if err != nil {
			return nil, err
		}
		p.pos++
		arr = append(arr, v)
	}
	return arr, nil
}

func toonKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		return strconv.Unquote(s)
	}
	return s, nil
}

func toonScalar(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		u, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("bad quoted string %q: %w", s, err)
		}
		return u, nil
	}
	if s == "null" {
		return "", nil
	}
	return s, nil
}

// splitTOONRow splits comma separated scalars, honouring quotes.
func splitTOONRow(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote, escaped := false, false
	flush := func() error {
		v, err := toonScalar(cur.String())
		cur.Reset()
		if err != nil {
			return err
		}
		out = append(out, v)
		return nil
	}
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			cur.WriteRune(r)
		case inQuote && r == '\\':
			escaped = true
			cur.WriteRune(r)
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}
