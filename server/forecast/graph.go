// Package forecast is the scheduling engine: dependency analysis, the critical path method over
// working days, and Monte Carlo confidence. It is a line-for-line port of web/src/lib/graph.ts,
// forecast.ts and montecarlo.ts — the browser keeps its copy for instant what-if edits, the server
// uses this one for history and alerts — and fixtures/forecast holds test vectors both must match
// exactly. Keep the two in step: same iteration orders, same PRNG, same arithmetic.
package forecast

import (
	"math"
	"math/bits"
	"sort"
)

// Node is the minimum the engine needs to know about a unit of work.
type Node struct {
	ID string `json:"id"`
	// Estimated effort in days.
	Estimate float64 `json:"estimate"`
	// "todo", "in_progress" or "done".
	State string `json:"state"`
	// Ids of nodes that must be done before this one can start.
	DependsOn []string `json:"dependsOn"`
}

type ScheduleEntry struct {
	EarliestStart  float64 `json:"earliestStart"`
	EarliestFinish float64 `json:"earliestFinish"`
	LatestStart    float64 `json:"latestStart"`
	LatestFinish   float64 `json:"latestFinish"`
	Slack          float64 `json:"slack"`
}

type Blocker struct {
	ID     string `json:"id"`
	Impact int    `json:"impact"`
}

type Analysis struct {
	// Unfinished direct dependencies of each unfinished task (only tasks that have some).
	BlockedBy map[string][]string
	// Unfinished tasks that directly depend on each task, and the order keys were first seen in.
	Dependents     map[string][]string
	dependentOrder []string
	// Number of unfinished tasks transitively waiting on each unfinished task.
	Impact map[string]int
	// Unfinished tasks sorted by how much work they hold up.
	TopBlockers []Blocker
	// Tasks being worked on while a dependency is still open.
	AtRisk []string
	// Unblocked tasks that haven't started yet.
	Ready []string
	// Task ids that are part of a dependency cycle.
	InCycle map[string]bool
	// Unfinished, acyclic tasks in topological order (the iteration order of the TS schedule map).
	Order        []string
	Schedule     map[string]ScheduleEntry
	CriticalPath []string
	// Remaining project duration (days) along the critical path.
	Remaining float64
}

const eps = 1e-9

func isOpen(n *Node) bool { return n.State != "done" }

// dur mirrors `Math.max(0, estimate || 0)`.
func dur(n *Node) float64 {
	if math.IsNaN(n.Estimate) || n.Estimate <= 0 {
		return 0
	}
	return n.Estimate
}

// WouldCreateCycle reports whether making taskID depend on depID would introduce a cycle.
func WouldCreateCycle(nodes []Node, taskID, depID string) bool {
	if taskID == depID {
		return true
	}
	byID := index(nodes)
	stack := []string{depID}
	seen := map[string]bool{}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == taskID {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if n := byID[id]; n != nil {
			stack = append(stack, n.DependsOn...)
		}
	}
	return false
}

func index(nodes []Node) map[string]*Node {
	m := make(map[string]*Node, len(nodes))
	for i := range nodes {
		m[nodes[i].ID] = &nodes[i]
	}
	return m
}

// components returns the strongly connected components of ids under next, in Tarjan's order:
// every component comes after all the components it reaches. Iterative, so a long chain can't
// exhaust the stack.
func components(ids []string, next func(string) []string) [][]string {
	idx := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	counter := 0

	type frame struct {
		id string
		i  int // next neighbour to look at
	}
	for _, root := range ids {
		if _, ok := idx[root]; ok {
			continue
		}
		call := []frame{{id: root}}
		idx[root], low[root] = counter, counter
		counter++
		stack = append(stack, root)
		onStack[root] = true
		for len(call) > 0 {
			f := &call[len(call)-1]
			deps := next(f.id)
			if f.i < len(deps) {
				dep := deps[f.i]
				f.i++
				if _, seen := idx[dep]; !seen {
					idx[dep], low[dep] = counter, counter
					counter++
					stack = append(stack, dep)
					onStack[dep] = true
					call = append(call, frame{id: dep})
				} else if onStack[dep] {
					low[f.id] = min(low[f.id], idx[dep])
				}
				continue
			}
			// All neighbours visited: close the component if this is its root, then return to the caller.
			id := f.id
			if low[id] == idx[id] {
				var component []string
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					component = append(component, w)
					if w == id {
						break
					}
				}
				out = append(out, component)
			}
			call = call[:len(call)-1]
			if len(call) > 0 {
				parent := call[len(call)-1].id
				low[parent] = min(low[parent], low[id])
			}
		}
	}
	return out
}

// FindCycles returns the ids of tasks in a dependency cycle.
func FindCycles(nodes []Node) map[string]bool {
	byID := index(nodes)
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	deps := func(id string) []string {
		var out []string
		for _, d := range byID[id].DependsOn {
			if byID[d] != nil {
				out = append(out, d)
			}
		}
		return out
	}
	result := map[string]bool{}
	for _, c := range components(ids, deps) {
		selfLoop := false
		for _, d := range byID[c[0]].DependsOn {
			selfLoop = selfLoop || d == c[0]
		}
		if len(c) > 1 || selfLoop {
			for _, id := range c {
				result[id] = true
			}
		}
	}
	return result
}

// transitiveImpact counts, for every task reachable from keys, how many tasks transitively wait on
// it (itself excluded). One DFS per task is quadratic, so cycles are collapsed into components and,
// walking them so each comes after those downstream of it, bitsets of what each reaches are ORed.
func transitiveImpact(keys []string, dependents map[string][]string) map[string]int {
	next := func(id string) []string { return dependents[id] }
	comps := components(keys, next)
	compOf := map[string]int{}
	bit := map[string]int{}
	for k, c := range comps {
		for _, id := range c {
			compOf[id] = k
			bit[id] = len(bit)
		}
	}
	words := (len(bit) + 63) / 64
	// reach[k]: the tasks strictly downstream of component k (its own members excluded).
	reach := make([][]uint64, len(comps))
	impact := make(map[string]int, len(bit))
	for k, c := range comps {
		r := make([]uint64, words)
		for _, id := range c {
			for _, w := range next(id) {
				d := compOf[w]
				if d == k {
					continue
				}
				for i, v := range reach[d] {
					r[i] |= v
				}
				for _, m := range comps[d] {
					b := bit[m]
					r[b/64] |= 1 << (b % 64)
				}
			}
		}
		reach[k] = r
		n := 0
		for _, v := range r {
			n += bits.OnesCount64(v)
		}
		for _, id := range c {
			impact[id] = n + len(c) - 1
		}
	}
	return impact
}

// Analyze computes blockers, impact and the critical-path schedule.
func Analyze(nodes []Node) *Analysis {
	byID := index(nodes)
	inCycle := FindCycles(nodes)
	var open []*Node
	for i := range nodes {
		if isOpen(&nodes[i]) {
			open = append(open, &nodes[i])
		}
	}
	openDeps := func(n *Node) []string {
		var out []string
		for _, d := range n.DependsOn {
			if dn := byID[d]; dn != nil && isOpen(dn) {
				out = append(out, d)
			}
		}
		return out
	}

	a := &Analysis{
		BlockedBy:  map[string][]string{},
		Dependents: map[string][]string{},
		Impact:     map[string]int{},
		InCycle:    inCycle,
		Schedule:   map[string]ScheduleEntry{},
		AtRisk:     []string{},
		Ready:      []string{},
	}
	for _, t := range open {
		deps := openDeps(t)
		if len(deps) > 0 {
			a.BlockedBy[t.ID] = deps
		}
		for _, d := range deps {
			if _, ok := a.Dependents[d]; !ok {
				a.dependentOrder = append(a.dependentOrder, d)
			}
			a.Dependents[d] = append(a.Dependents[d], t.ID)
		}
	}

	impact := transitiveImpact(a.dependentOrder, a.Dependents)
	for _, id := range a.dependentOrder {
		a.Impact[id] = impact[id]
		a.TopBlockers = append(a.TopBlockers, Blocker{id, impact[id]})
	}
	sort.SliceStable(a.TopBlockers, func(i, j int) bool { return a.TopBlockers[i].Impact > a.TopBlockers[j].Impact })

	for _, t := range open {
		_, blocked := a.BlockedBy[t.ID]
		if t.State == "in_progress" && blocked {
			a.AtRisk = append(a.AtRisk, t.ID)
		}
		if t.State == "todo" && !blocked {
			a.Ready = append(a.Ready, t.ID)
		}
	}

	// Critical path method over the open, acyclic subgraph.
	var sched []*Node
	inSched := map[string]bool{}
	for _, t := range open {
		if !inCycle[t.ID] {
			sched = append(sched, t)
			inSched[t.ID] = true
		}
	}
	preds := make(map[string][]string, len(sched))
	succs := make(map[string][]string, len(sched))
	for _, t := range sched {
		var ps []string
		for _, d := range openDeps(t) {
			if inSched[d] {
				ps = append(ps, d)
			}
		}
		preds[t.ID] = ps
		succs[t.ID] = []string{}
	}
	for _, t := range sched {
		for _, p := range preds[t.ID] {
			succs[p] = append(succs[p], t.ID)
		}
	}

	// Kahn topological order (a FIFO, like the TS queue.shift()).
	indeg := make(map[string]int, len(sched))
	var queue []string
	for _, t := range sched {
		indeg[t.ID] = len(preds[t.ID])
		if indeg[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}
	order := make([]string, 0, len(sched))
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		order = append(order, id)
		for _, s := range succs[id] {
			indeg[s]--
			if indeg[s] == 0 {
				queue = append(queue, s)
			}
		}
	}

	es := make(map[string]float64, len(order))
	ef := make(map[string]float64, len(order))
	for _, id := range order {
		start := 0.0
		for _, p := range preds[id] {
			start = math.Max(start, ef[p])
		}
		es[id] = start
		ef[id] = float64(start + dur(byID[id]))
	}
	remaining := 0.0
	for _, id := range order {
		remaining = math.Max(remaining, ef[id])
	}

	lf := make(map[string]float64, len(order))
	ls := make(map[string]float64, len(order))
	for k := len(order) - 1; k >= 0; k-- {
		id := order[k]
		finish := remaining
		for _, s := range succs[id] {
			finish = math.Min(finish, ls[s])
		}
		lf[id] = finish
		ls[id] = float64(finish - dur(byID[id]))
	}
	for _, id := range order {
		a.Schedule[id] = ScheduleEntry{
			EarliestStart:  es[id],
			EarliestFinish: ef[id],
			LatestStart:    ls[id],
			LatestFinish:   lf[id],
			Slack:          math.Max(0, ls[id]-es[id]),
		}
	}
	a.Order = order
	a.Remaining = remaining

	// Walk back from the latest-finishing task through zero-slack predecessors, preferring the
	// longest task on ties (a stable sort by duration, descending, then the first).
	longest := func(ids []string) string {
		best := ""
		for _, id := range ids {
			if best == "" || dur(byID[id]) > dur(byID[best]) {
				best = id
			}
		}
		return best
	}
	if len(order) > 0 && remaining > 0 {
		var ends []string
		for _, id := range order {
			if math.Abs(ef[id]-remaining) < eps {
				ends = append(ends, id)
			}
		}
		cur := longest(ends)
		var path []string
		for cur != "" {
			path = append(path, cur)
			start := es[cur]
			var cands []string
			for _, p := range preds[cur] {
				if math.Abs(ef[p]-start) < eps && a.Schedule[p].Slack < eps {
					cands = append(cands, p)
				}
			}
			cur = longest(cands)
		}
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		a.CriticalPath = path
	}
	if a.CriticalPath == nil {
		a.CriticalPath = []string{}
	}
	if a.TopBlockers == nil {
		a.TopBlockers = []Blocker{}
	}
	return a
}

// CriticalSet is the critical path as a set.
func (a *Analysis) CriticalSet() map[string]bool {
	m := make(map[string]bool, len(a.CriticalPath))
	for _, id := range a.CriticalPath {
		m[id] = true
	}
	return m
}
