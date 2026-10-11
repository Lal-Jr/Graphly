package forecast

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// SyntheticPlan is a deterministic project for scale tests, identical to web/src/lib/synthetic.ts
// for the same n and seed: issues each depending on up to seven earlier ones, a fifth done, a
// tenth with due dates, and a cycle planted every thousand issues.
type SyntheticPlan struct {
	Nodes    []Node
	DueDates map[string]string
	Types    map[string]string
}

func Synthetic(n int, seed uint32) SyntheticPlan {
	rand := Mulberry32(seed)
	pick := func(k int) int { return int(math.Floor(rand() * float64(k))) }
	estimates := []float64{0, 0.5, 1, 2, 3, 5}
	types := []string{"task", "story", "bug"}
	p := SyntheticPlan{Nodes: make([]Node, 0, n), DueDates: map[string]string{}, Types: map[string]string{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", i)
		estimate := estimates[pick(len(estimates))]
		r := rand()
		state := "todo"
		if r < 0.2 {
			state = "done"
		} else if r < 0.35 {
			state = "in_progress"
		}
		dependsOn := []string{}
		links := 0
		if i > 0 {
			links = pick(8)
		}
		for k := 0; k < links; k++ {
			var j int
			if rand() < 0.7 {
				j = max(0, i-1-pick(40))
			} else {
				j = pick(i)
			}
			dep := fmt.Sprintf("s%d", j)
			if !slices.Contains(dependsOn, dep) {
				dependsOn = append(dependsOn, dep)
			}
		}
		p.Nodes = append(p.Nodes, Node{ID: id, Estimate: estimate, State: state, DependsOn: dependsOn})
		p.Types[id] = types[pick(len(types))]
		if rand() < 0.1 {
			p.DueDates[id] = FormatDate(time.Date(2026, 1, 5+pick(400), 0, 0, 0, 0, time.UTC))
		}
	}
	for i := 999; i < n; i += 1000 {
		if len(p.Nodes[i].DependsOn) > 0 {
			j, _ := strconv.Atoi(p.Nodes[i].DependsOn[0][1:])
			p.Nodes[j].DependsOn = append(p.Nodes[j].DependsOn, p.Nodes[i].ID)
		}
	}
	return p
}
