package herdrmux

import "strings"

// RenderFormat expands a tmux format string. lookup returns the value of a
// variable (unknown variables should return ""). Supported forms:
//
//	#{name}  #{==:a,b}  #{!=:a,b}  #{&&:a,b}  #{||:a,b}  #{?cond,a,b}  ##
func RenderFormat(f string, lookup func(name string) string) string {
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '#' || i+1 >= len(f) {
			b.WriteByte(c)
			continue
		}
		switch f[i+1] {
		case '#':
			b.WriteByte('#')
			i++
		case '{':
			end := matchBrace(f, i+1)
			if end < 0 {
				b.WriteString(f[i:])
				return b.String()
			}
			b.WriteString(evalExpr(f[i+2:end], lookup))
			i = end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// matchBrace returns the index of the "}" closing the "{" at open, honoring
// nested #{...}; -1 if unbalanced.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits s on commas that are not inside nested #{...}.
func splitTop(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func truthy(s string) bool { return s != "" && s != "0" }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func evalExpr(expr string, lookup func(string) string) string {
	switch {
	case strings.HasPrefix(expr, "?"):
		parts := splitTop(expr[1:])
		if len(parts) < 2 {
			return ""
		}
		cond := parts[0]
		var cv string
		if strings.Contains(cond, "#{") {
			cv = RenderFormat(cond, lookup)
		} else if v := lookup(cond); v != "" {
			cv = v
		} else {
			cv = cond
		}
		if truthy(cv) {
			return RenderFormat(parts[1], lookup)
		}
		if len(parts) >= 3 {
			// "#{?a,b,c,d}" chains are rare; rejoin the tail as the else branch.
			return RenderFormat(strings.Join(parts[2:], ","), lookup)
		}
		return ""
	case strings.HasPrefix(expr, "==:"), strings.HasPrefix(expr, "!=:"),
		strings.HasPrefix(expr, "&&:"), strings.HasPrefix(expr, "||:"):
		parts := splitTop(expr[3:])
		if len(parts) < 2 {
			return "0"
		}
		a := RenderFormat(parts[0], lookup)
		bv := RenderFormat(strings.Join(parts[1:], ","), lookup)
		switch expr[:2] {
		case "==":
			return boolStr(a == bv)
		case "!=":
			return boolStr(a != bv)
		case "&&":
			return boolStr(truthy(a) && truthy(bv))
		default:
			return boolStr(truthy(a) || truthy(bv))
		}
	}
	return lookup(expr)
}
