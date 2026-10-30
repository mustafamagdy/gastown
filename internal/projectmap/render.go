package projectmap

import (
	"fmt"
	"strings"
)

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func endpointList(eps []Endpoint) string {
	var s []string
	for _, e := range eps {
		s = append(s, e.String())
	}
	return strings.Join(s, ", ")
}

func cell(s string) string { return orDash(strings.ReplaceAll(s, "|", "/")) }

// RenderMarkdown renders the whole map as a table plus the edges between services.
func RenderMarkdown(m *Map) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Service map: %s\n\n", m.Project)
	fmt.Fprintf(&b, "Generated %s. Do not edit; put corrections in services/<rig>.toml.\n\n", m.GeneratedAt.Format("2006-01-02 15:04 MST"))
	b.WriteString("| Rig | Service | Owns | Serves | Calls | Publishes | Consumes | Database |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, s := range m.Services {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			s.Rig, cell(s.Name), cell(s.Owns), cell(endpointList(s.Serves)),
			cell(strings.Join(s.Calls, ", ")), cell(strings.Join(s.Publishes, ", ")),
			cell(strings.Join(s.Consumes, ", ")), cell(s.Database))
	}
	b.WriteString("\n## Edges\n\n")
	n := 0
	r := newResolver(m.Services)
	for _, s := range m.Services {
		for _, c := range s.Calls {
			target := c
			if rig, ok := r.rig(c); ok {
				target = rig
			}
			fmt.Fprintf(&b, "- %s calls %s\n", s.Rig, target)
			n++
		}
	}
	for _, s := range m.Services {
		for _, e := range s.Publishes {
			for _, o := range m.Services {
				if o.Rig != s.Rig && toSet(o.Consumes)[e] {
					fmt.Fprintf(&b, "- %s publishes %s, consumed by %s\n", s.Rig, e, o.Rig)
					n++
				}
			}
		}
	}
	if n == 0 {
		b.WriteString("(none)\n")
	}
	return b.String()
}

// RenderNeighbourhood renders one rig's entry in full plus its neighbours,
// compactly, for agent context. At most maxNeighbours neighbours are shown;
// moreHint is appended to the overflow line.
func RenderNeighbourhood(m *Map, rig string, maxNeighbours int, moreHint string) string {
	self := m.Find(rig)
	if self == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- %s (rig %s): owns %s\n", self.Name, self.Rig, orDash(self.Owns))
	writeField := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&b, "  - %s: %s\n", label, v)
		}
	}
	writeField("serves", endpointList(self.Serves))
	writeField("calls", strings.Join(self.Calls, ", "))
	writeField("publishes", strings.Join(self.Publishes, ", "))
	writeField("consumes", strings.Join(self.Consumes, ", "))
	writeField("database", self.Database)

	rels := Relations(m, rig)
	if len(rels) == 0 {
		b.WriteString("Neighbours: none known.\n")
		return b.String()
	}
	b.WriteString("Neighbours:\n")
	for i, rel := range rels {
		if i >= maxNeighbours {
			fmt.Fprintf(&b, "- and %d more (%s)\n", len(rels)-maxNeighbours, moreHint)
			break
		}
		o := m.Find(rel.Rig)
		var parts []string
		if rel.Calls {
			parts = append(parts, "you call it")
		}
		if rel.CalledBy {
			parts = append(parts, "it calls you")
		}
		if len(rel.Consumers) > 0 {
			parts = append(parts, "consumes your "+strings.Join(rel.Consumers, ", "))
		}
		if len(rel.Producers) > 0 {
			parts = append(parts, "publishes "+strings.Join(rel.Producers, ", ")+" that you consume")
		}
		line := fmt.Sprintf("- %s (rig %s): %s", o.Name, o.Rig, strings.Join(parts, "; "))
		if o.Owns != "" {
			line += "; owns " + o.Owns
		}
		if rel.Calls && len(o.Serves) > 0 {
			eps := o.Serves
			more := ""
			if len(eps) > 6 {
				more = fmt.Sprintf(", +%d more", len(eps)-6)
				eps = eps[:6]
			}
			line += "; serves " + endpointList(eps) + more
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
