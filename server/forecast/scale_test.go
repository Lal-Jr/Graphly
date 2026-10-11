package forecast

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var benchToday = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// The server recomputes every project with open work on each forecast job tick, so a 5,000-issue
// project has to stay cheap. The budget is generous; it catches an accidental quadratic.
func TestScale5k(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	p := Synthetic(5000, 1)
	links := 0
	for _, n := range p.Nodes {
		links += len(n.DependsOn)
	}
	t0 := time.Now()
	a := Analyze(p.Nodes)
	t1 := time.Now()
	Forecast(a, p.DueDates, benchToday)
	t2 := time.Now()
	seed := uint32(1)
	c := SimulateConfidence(p.Nodes, a, p.DueDates, DefaultSpread(func(id string) string { return p.Types[id] }), 1000, benchToday, &seed)
	t3 := time.Now()
	t.Logf("5000 issues, %d links: analyze %v, forecast %v, 1000-run simulation %v", links, t1.Sub(t0).Round(time.Millisecond), t2.Sub(t1).Round(time.Millisecond), t3.Sub(t2).Round(time.Millisecond))
	if len(a.InCycle) == 0 || a.Remaining <= 0 || c.P85 < c.P50 {
		t.Fatalf("unexpected plan: cycles %d, remaining %v", len(a.InCycle), a.Remaining)
	}
	if total := t3.Sub(t0); total > 10*time.Second {
		t.Fatalf("too slow: %v", total)
	}
}

func BenchmarkAnalyze5k(b *testing.B) {
	p := Synthetic(5000, 1)
	for b.Loop() {
		Analyze(p.Nodes)
	}
}

func BenchmarkForecast5k(b *testing.B) {
	p := Synthetic(5000, 1)
	a := Analyze(p.Nodes)
	for b.Loop() {
		Forecast(a, p.DueDates, benchToday)
	}
}

func BenchmarkSimulate5k(b *testing.B) {
	p := Synthetic(5000, 1)
	a := Analyze(p.Nodes)
	seed := uint32(1)
	spread := DefaultSpread(func(id string) string { return p.Types[id] })
	for b.Loop() {
		SimulateConfidence(p.Nodes, a, p.DueDates, spread, 1000, benchToday, &seed)
	}
}

// TestScaleParity checks the Go engine against the summary the TypeScript engine wrote for the
// same 5,000-issue plan (web/src/lib/scale.test.ts).
func TestScaleParity(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	raw, err := os.ReadFile("../../fixtures/forecast-scale/synthetic-5k.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Links        int       `json:"links"`
		Remaining    float64   `json:"remaining"`
		OrderHash    uint32    `json:"orderHash"`
		CriticalPath []string  `json:"criticalPath"`
		InCycle      []string  `json:"inCycle"`
		TopBlockers  []Blocker `json:"topBlockers"`
		ImpactSum    int       `json:"impactSum"`
		Ready        int       `json:"ready"`
		AtRisk       int       `json:"atRisk"`
		FinishHash   uint32    `json:"finishHash"`
		Confidence   struct {
			P50, P85, P95 float64
			Histogram     []int  `json:"histogram"`
			OnTimeHash    uint32 `json:"onTimeHash"`
		} `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	p := Synthetic(5000, 1)
	a := Analyze(p.Nodes)
	fc := Forecast(a, p.DueDates, benchToday)
	seed := uint32(7)
	c := SimulateConfidence(p.Nodes, a, p.DueDates, DefaultSpread(func(id string) string { return p.Types[id] }), 200, benchToday, &seed)

	links, impactSum := 0, 0
	for _, n := range p.Nodes {
		links += len(n.DependsOn)
	}
	for _, v := range a.Impact {
		impactSum += v
	}
	var inCycle, finishes, onTime []string
	for id := range a.InCycle {
		inCycle = append(inCycle, id)
	}
	sort.Strings(inCycle)
	for _, id := range a.Order {
		f := fc[id]
		finishes = append(finishes, FormatDate(f.Finish)+strconv.Itoa(f.DaysLate))
		if v, ok := c.OnTime[id]; ok {
			onTime = append(onTime, id+jsNumber(v))
		}
	}
	check := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", what, got, want)
		}
	}
	check("links", links, want.Links)
	check("remaining", a.Remaining, want.Remaining)
	check("order", Hash(strings.Join(a.Order, ",")), want.OrderHash)
	check("critical path", a.CriticalPath, want.CriticalPath)
	check("cycles", inCycle, want.InCycle)
	check("top blockers", a.TopBlockers[:25], want.TopBlockers)
	check("impact", impactSum, want.ImpactSum)
	check("ready", len(a.Ready), want.Ready)
	check("at risk", len(a.AtRisk), want.AtRisk)
	check("finishes", Hash(strings.Join(finishes, ",")), want.FinishHash)
	check("p50/p85/p95", []float64{c.P50, c.P85, c.P95}, []float64{want.Confidence.P50, want.Confidence.P85, want.Confidence.P95})
	check("histogram", c.Histogram, want.Confidence.Histogram)
	check("on time", Hash(strings.Join(onTime, ",")), want.Confidence.OnTimeHash)
}
