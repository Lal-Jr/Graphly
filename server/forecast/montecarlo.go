package forecast

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Single-point estimates are optimistic by construction, so a date computed from them is a coin
// flip at best. SimulateConfidence runs the plan many times, sampling each task's duration from a
// skewed (triangular) distribution around its estimate, and reports what fraction of runs finish
// by a given day.

// Spread gives the low and high multipliers on a task's estimate.
type Spread func(id string) [2]float64

// DefaultSpreads are the overrun ranges used until a project has enough history to calibrate its
// own. Work tends to overrun, bugs most of all.
var DefaultSpreads = map[string][2]float64{
	"bug":   {0.6, 2.5},
	"story": {0.8, 1.8},
	"task":  {0.8, 1.6},
}

// DefaultSpread looks each task's type up in DefaultSpreads (anything unknown is a task).
func DefaultSpread(typeOf func(id string) string) Spread { return SpreadFrom(typeOf, DefaultSpreads) }

type Confidence struct {
	// Remaining working days by percentile of simulated runs.
	P50, P85, P95 float64
	// Probability (0–1) each dated open task finishes on or before its due date.
	OnTime map[string]float64
	// 85th-percentile finish (working days from today) per task.
	P85ByID map[string]float64
	// How many runs finished on each working day from today (index = days, rounded up).
	Histogram []int
	Runs      int
	Seed      uint32
}

// Mulberry32 is the small seedable PRNG the browser uses, bit for bit.
func Mulberry32(seed uint32) func() float64 {
	return func() float64 {
		seed += 0x6d2b79f5
		t := (seed ^ (seed >> 15)) * (1 | seed)
		t = (t + (t^(t>>7))*(61|t)) ^ t
		return float64(t^(t>>14)) / 4294967296
	}
}

// Hash is FNV-1a over UTF-16 code units, matching the TS hash().
func Hash(s string) uint32 {
	h := uint32(2166136261)
	for _, c := range utf16.Encode([]rune(s)) {
		h = (h ^ uint32(c)) * 16777619
	}
	return h
}

// jsNumber formats a float the way JavaScript's Number#toString does for the values estimates take.
func jsNumber(f float64) string {
	if a := math.Abs(f); a == 0 || (a >= 1e-6 && a < 1e21) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 1e-07 → 1e-7, 1e+21 stays
	mant, exp, _ := strings.Cut(s, "e")
	sign := exp[:1]
	exp = strings.TrimLeft(exp[1:], "0")
	return mant + "e" + sign + exp
}

// PlanSeed is the default seed: a hash of the schedule order and estimates, so the same plan
// always yields the same numbers on every machine.
func PlanSeed(order []string, estimates []float64) uint32 {
	est := make([]string, len(estimates))
	for i, e := range estimates {
		est[i] = jsNumber(e)
	}
	return Hash(strings.Join(order, ",") + strings.Join(est, ","))
}

// triangular samples a triangular distribution on [lo, hi] with the given mode. The float64()
// conversions stop the compiler fusing multiply-adds, which would change the last bit vs. JS.
func triangular(rand func() float64, lo, mode, hi float64) float64 {
	if hi <= lo {
		return mode
	}
	u := rand()
	c := (mode - lo) / (hi - lo)
	if u < c {
		return lo + math.Sqrt(float64(float64(u*(hi-lo))*(mode-lo)))
	}
	return hi - math.Sqrt(float64(float64((1-u)*(hi-lo))*(hi-mode)))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(math.Floor(p*float64(len(sorted)))))]
}

// SimulateConfidence runs the plan `runs` times. Pass seed nil to use PlanSeed.
func SimulateConfidence(nodes []Node, a *Analysis, dueDates map[string]string, spread Spread, runs int, today time.Time, seed *uint32) *Confidence {
	byID := index(nodes)
	order := a.Order
	pos := make(map[string]int, len(order))
	for k, id := range order {
		pos[id] = k
	}
	preds := make([][]int, len(order))
	est := make([]float64, len(order))
	lo := make([]float64, len(order))
	hi := make([]float64, len(order))
	for k, id := range order {
		n := byID[id]
		for _, d := range n.DependsOn {
			if p, ok := pos[d]; ok {
				preds[k] = append(preds[k], p)
			}
		}
		est[k] = dur(n)
		s := spread(id)
		lo[k], hi[k] = s[0], s[1]
	}
	origin := NextWorkday(today)
	dueCol := make([]int, len(order))
	for k, id := range order {
		dueCol[k] = -1
		if due, ok := dueDates[id]; ok && due != "" {
			if d, err := ParseDate(due); err == nil {
				dueCol[k] = WorkdaysBetween(origin, d)
			}
		}
	}

	c := &Confidence{OnTime: map[string]float64{}, P85ByID: map[string]float64{}, Histogram: []int{}, Runs: runs}
	if seed != nil {
		c.Seed = *seed
	} else {
		c.Seed = PlanSeed(order, est)
	}
	rand := Mulberry32(c.Seed)
	finishes := make([]float64, len(order))
	totals := make([]float64, 0, runs)
	perTask := make([][]float64, len(order))
	for k := range perTask {
		perTask[k] = make([]float64, 0, runs)
	}
	hits := make([]int, len(order))

	for r := 0; r < runs; r++ {
		total := 0.0
		for k := range order {
			start := 0.0
			for _, p := range preds[k] {
				if finishes[p] > start {
					start = finishes[p]
				}
			}
			d := 0.0
			if est[k] != 0 {
				d = triangular(rand, float64(est[k]*lo[k]), est[k], float64(est[k]*hi[k]))
			}
			f := start + d
			finishes[k] = f
			perTask[k] = append(perTask[k], f)
			if f > total {
				total = f
			}
			// Finishing within working day dueCol means ceil(f) - 1 <= dueCol.
			if dueCol[k] >= 0 && int(math.Ceil(f))-1 <= dueCol[k] {
				hits[k]++
			}
		}
		totals = append(totals, total)
	}

	sort.Float64s(totals)
	for k, id := range order {
		if dueCol[k] >= 0 {
			c.OnTime[id] = float64(hits[k]) / float64(runs)
		}
		sort.Float64s(perTask[k])
		c.P85ByID[id] = percentile(perTask[k], 0.85)
	}
	for _, t := range totals {
		day := int(math.Ceil(t))
		for len(c.Histogram) <= day {
			c.Histogram = append(c.Histogram, 0)
		}
		c.Histogram[day]++
	}
	c.P50, c.P85, c.P95 = percentile(totals, 0.5), percentile(totals, 0.85), percentile(totals, 0.95)
	return c
}

// SpreadFrom looks each task's type up in a table; unknown types use the "task" entry.
func SpreadFrom(typeOf func(id string) string, table map[string][2]float64) Spread {
	return func(id string) [2]float64 {
		if s, ok := table[typeOf(id)]; ok {
			return s
		}
		if s, ok := table["task"]; ok {
			return s
		}
		return DefaultSpreads["task"]
	}
}
