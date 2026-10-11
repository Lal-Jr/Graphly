package main

import "math"

// An open issue with no estimate counts as zero days, so it silently pulls the forecast in. The
// more of the open plan that's unestimated, the less the ship date means. The same definition
// lives in web/src/lib/quality.ts.

// EstimateQuality summarises how much of the open plan is actually estimated.
type EstimateQuality struct {
	OpenIssues  int `json:"openIssues"`
	Unestimated int `json:"unestimated"`
	// Share of open issues with no estimate (0–1).
	Share float64 `json:"share"`
	// "high" under 10% unestimated, "medium" under 30%, "low" from there.
	Confidence string `json:"confidence"`
}

const (
	qualityHigh   = 0.10
	qualityMedium = 0.30
)

func estimateQuality(p *plan) *EstimateQuality {
	q := &EstimateQuality{}
	for _, i := range p.Issues {
		if i.Type == "epic" || i.Category == "done" {
			continue
		}
		q.OpenIssues++
		if !(i.Estimate > 0) || math.IsNaN(i.Estimate) {
			q.Unestimated++
		}
	}
	if q.OpenIssues > 0 {
		q.Share = float64(q.Unestimated) / float64(q.OpenIssues)
	}
	switch {
	case q.Share < qualityHigh:
		q.Confidence = "high"
	case q.Share < qualityMedium:
		q.Confidence = "medium"
	default:
		q.Confidence = "low"
	}
	return q
}
