// Package axiout writes gt output in the compact AXI style that agent tools
// such as gh-axi and az-axi use: "key: value" lines, TOON-style arrays
// ("name[N]{a,b}:" followed by one indented row per item), a row cap with a
// count line, and a "help[N]:" block of next-step commands. It never writes
// colour or other ANSI sequences.
//
// The format is for agents to read, not for programs to parse; commands keep
// --json for that. Output is opt-in per command (--axi) or per environment
// (GT_OUTPUT=axi), and --json always wins.
package axiout

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// EnvVar selects the output style for every covered command.
const EnvVar = "GT_OUTPUT"

// Mode is the value of EnvVar that selects AXI output.
const Mode = "axi"

// DefaultRowCap is how many rows a table shows before it is cut off.
const DefaultRowCap = 20

// MaxText is the longest free text value (a title, a reason) before it is cut.
const MaxText = 80

// Wanted reports whether a command should write AXI output: the --axi flag or
// GT_OUTPUT=axi asks for it, and --json (jsonFlag) always wins.
func Wanted(axiFlag, jsonFlag bool) bool {
	if jsonFlag {
		return false
	}
	return axiFlag || strings.EqualFold(strings.TrimSpace(os.Getenv(EnvVar)), Mode)
}

// Writer builds AXI output in memory.
type Writer struct {
	b strings.Builder
}

// New returns an empty writer.
func New() *Writer { return &Writer{} }

// String returns the text written so far.
func (w *Writer) String() string { return w.b.String() }

// KV writes a "key: value" line. Strings are quoted only when needed; ints and
// bools are written bare.
func (w *Writer) KV(key string, value any) {
	switch v := value.(type) {
	case string:
		fmt.Fprintf(&w.b, "%s: %s\n", key, Quote(v))
	case int:
		fmt.Fprintf(&w.b, "%s: %d\n", key, v)
	case bool:
		fmt.Fprintf(&w.b, "%s: %t\n", key, v)
	default:
		fmt.Fprintf(&w.b, "%s: %s\n", key, Quote(fmt.Sprint(v)))
	}
}

// Table writes rows (cells are strings, ints or bools) as "name[N]{a,b}:" plus one indented row per item. N is
// the number of rows written. More than limit rows are cut and followed by a
// "showing: N of M" line (limit <= 0 means DefaultRowCap). An empty table is
// the single line "name: 0", so absence is stated and never silent.
func (w *Writer) Table(name string, cols []string, rows [][]any, limit int) {
	if len(rows) == 0 {
		fmt.Fprintf(&w.b, "%s: 0\n", name)
		return
	}
	if limit <= 0 {
		limit = DefaultRowCap
	}
	total := len(rows)
	if total > limit {
		rows = rows[:limit]
	}
	fmt.Fprintf(&w.b, "%s[%d]{%s}:\n", name, len(rows), strings.Join(cols, ","))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cell(c)
		}
		fmt.Fprintf(&w.b, "  %s\n", strings.Join(cells, ","))
	}
	if total > len(rows) {
		fmt.Fprintf(&w.b, "showing: %d of %d\n", len(rows), total)
	}
}

// List writes a flat list as "name[N]:" plus one indented item per line, or
// "name: 0" when empty.
func (w *Writer) List(name string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(&w.b, "%s: 0\n", name)
		return
	}
	fmt.Fprintf(&w.b, "%s[%d]:\n", name, len(items))
	for _, it := range items {
		fmt.Fprintf(&w.b, "  %s\n", Quote(it))
	}
}

// Help writes the "help[N]:" block of next-step commands. It writes nothing
// when there are none.
func (w *Writer) Help(cmds ...string) {
	var keep []string
	for _, c := range cmds {
		if strings.TrimSpace(c) != "" {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		return
	}
	fmt.Fprintf(&w.b, "help[%d]:\n", len(keep))
	for _, c := range keep {
		fmt.Fprintf(&w.b, "  %s\n", c)
	}
}

// cell formats one table value: ints and bools bare, anything else as text.
func cell(v any) string {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return Quote(x)
	}
	return Quote(fmt.Sprint(v))
}

// Truncate cuts s to at most max runes, ending in "..." when it was cut.
// Whitespace runs, newlines included, become single spaces first so a value
// stays on one line.
func Truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max < 4 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}

// Quote returns s as it should appear in a value position. Like the TOON
// format the AXI tools use, a string is quoted (with backslash escapes) only
// when bare text would be ambiguous: empty, padded with spaces, holding a
// comma, colon, quote, backslash, bracket, brace or control character,
// starting with "-", or reading as a number, true, false or null.
func Quote(s string) string {
	if !needsQuote(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func needsQuote(s string) bool {
	if s == "" || s != strings.TrimSpace(s) || strings.HasPrefix(s, "-") {
		return true
	}
	switch s {
	case "true", "false", "null":
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return true
		case strings.ContainsRune(`,:"\[]{}`, r):
			return true
		}
	}
	return false
}
