package main

import "testing"

func TestEstimateQuality(t *testing.T) {
	mk := func(open, unestimated, doneUnestimated int) *plan {
		p := &plan{}
		for i := 0; i < open; i++ {
			est := 1.0
			if i < unestimated {
				est = 0
			}
			p.Issues = append(p.Issues, planIssue{Type: "task", Estimate: est, Category: "todo"})
		}
		for i := 0; i < doneUnestimated; i++ {
			p.Issues = append(p.Issues, planIssue{Type: "task", Category: "done"})
		}
		p.Issues = append(p.Issues, planIssue{Type: "epic", Category: "todo"}) // epics never need estimates
		return p
	}
	for _, c := range []struct {
		open, unest int
		want        string
	}{{20, 1, "high"}, {20, 2, "medium"}, {20, 5, "medium"}, {20, 6, "low"}, {0, 0, "high"}} {
		q := estimateQuality(mk(c.open, c.unest, 3))
		if q.Confidence != c.want || q.OpenIssues != c.open || q.Unestimated != c.unest {
			t.Errorf("%d/%d: %+v, want %s", c.unest, c.open, q, c.want)
		}
	}
}
