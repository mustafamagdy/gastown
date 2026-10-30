package projectfeature

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/projectmap"
)

// RigDeps returns, from the service map, which rigs each rig depends on:
// rig A depends on B when A calls B, or A consumes an event B publishes.
// Only rigs in the given set are considered.
func RigDeps(m *projectmap.Map, rigs []string) map[string][]string {
	set := map[string]bool{}
	for _, r := range rigs {
		set[r] = true
	}
	deps := map[string][]string{}
	for _, a := range rigs {
		for _, rel := range projectmap.Relations(m, a) {
			if !set[rel.Rig] {
				continue
			}
			if rel.Calls || len(rel.Producers) > 0 {
				deps[a] = append(deps[a], rel.Rig)
			}
		}
		sort.Strings(deps[a])
	}
	return deps
}

// breakCycles removes every dependency between rigs of the same strongly
// connected component and returns the cycles found (each as a sorted rig
// list).
func breakCycles(deps map[string][]string, rigs []string) (map[string][]string, [][]string) {
	index := map[string]int{}
	low := map[string]int{}
	on := map[string]bool{}
	comp := map[string]int{}
	var stack []string
	var sccs [][]string
	next := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range deps[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if on[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] == index[v] {
			var c []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp[w] = len(sccs)
				c = append(c, w)
				if w == v {
					break
				}
			}
			sort.Strings(c)
			sccs = append(sccs, c)
		}
	}
	for _, r := range rigs {
		if _, seen := index[r]; !seen {
			visit(r)
		}
	}
	var cycles [][]string
	for _, c := range sccs {
		if len(c) > 1 {
			cycles = append(cycles, c)
		}
	}
	out := map[string][]string{}
	for a, list := range deps {
		for _, b := range list {
			if comp[a] != comp[b] {
				out[a] = append(out[a], b)
			}
		}
	}
	return out, cycles
}

// ApplyOrder recomputes the derived 'after' edges of every task from the
// service map and keeps the explicit ones. A task in rig A waits for all tasks
// of every rig A depends on. Dependencies between rigs that depend on each
// other in a cycle are left out, and an edge that would close a cycle with
// explicit task edges is skipped; both are reported in warnings.
func ApplyOrder(rec *Record, m *projectmap.Map) (warnings []string) {
	var rigs []string
	seen := map[string]bool{}
	for _, t := range rec.Tasks {
		if !seen[t.Rig] {
			seen[t.Rig] = true
			rigs = append(rigs, t.Rig)
		}
	}
	sort.Strings(rigs)
	for _, r := range rigs {
		if m.Find(r) == nil {
			warnings = append(warnings, fmt.Sprintf("rig %s is not in the service map; no order derived for it", r))
		}
	}
	deps, cycles := breakCycles(RigDeps(m, rigs), rigs)
	for _, c := range cycles {
		warnings = append(warnings, fmt.Sprintf("rigs depend on each other (%s); no order derived between them", strings.Join(c, ", ")))
	}

	byRig := map[string][]string{}
	for _, t := range rec.Tasks {
		byRig[t.Rig] = append(byRig[t.Rig], t.ID)
	}
	// Start from the explicit edges only.
	for i := range rec.Tasks {
		rec.Tasks[i].After = append([]string(nil), rec.Tasks[i].Manual...)
	}
	for i := range rec.Tasks {
		t := &rec.Tasks[i]
		for _, depRig := range deps[t.Rig] {
			for _, pre := range byRig[depRig] {
				if contains(t.After, pre) {
					continue
				}
				if reaches(rec, pre, t.ID) {
					warnings = append(warnings, fmt.Sprintf("task %s after %s would make a cycle with explicit edges; skipped", t.ID, pre))
					continue
				}
				t.After = append(t.After, pre)
			}
		}
		sort.Strings(t.After)
	}
	return warnings
}

// reaches reports whether 'from' already waits (transitively) for 'to'.
func reaches(rec *Record, from, to string) bool {
	visited := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		if id == to {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		t := rec.Task(id)
		if t == nil {
			return false
		}
		for _, a := range t.After {
			if walk(a) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// AddTask appends a task with explicit 'after' edges. Every edge must name an
// existing task.
func (r *Record) AddTask(t Task) error {
	if _, ok := r.Rigs[t.Rig]; !ok {
		return fmt.Errorf("rig %q is not part of feature %s", t.Rig, r.ID)
	}
	if r.Task(t.ID) != nil {
		return fmt.Errorf("task %s already in feature %s", t.ID, r.ID)
	}
	for _, a := range t.After {
		if r.Task(a) == nil {
			return fmt.Errorf("--after %s: no such task in feature %s", a, r.ID)
		}
	}
	t.Manual = append([]string(nil), t.After...)
	r.Tasks = append(r.Tasks, t)
	return nil
}
