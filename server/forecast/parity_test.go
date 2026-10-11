package forecast

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// The fixtures are shared with web/src/lib/parity.test.ts, which generates `expect` from the
// TypeScript engine. Matching them exactly means the server's history and alerts are computed from
// the same numbers the browser shows.

type fixture struct {
	Name     string                `json:"name"`
	Today    string                `json:"today"`
	Runs     int                   `json:"runs"`
	Seed     *uint32               `json:"seed"`
	Nodes    []Node                `json:"nodes"`
	DueDates map[string]string     `json:"dueDates"`
	Types    map[string]string     `json:"types"`
	Spreads  map[string][2]float64 `json:"spreads"`
	Expect   expectation           `json:"expect"`
}

type expectation struct {
	Analysis struct {
		Remaining    float64                  `json:"remaining"`
		Order        []string                 `json:"order"`
		CriticalPath []string                 `json:"criticalPath"`
		InCycle      []string                 `json:"inCycle"`
		Ready        []string                 `json:"ready"`
		AtRisk       []string                 `json:"atRisk"`
		BlockedBy    map[string][]string      `json:"blockedBy"`
		TopBlockers  []Blocker                `json:"topBlockers"`
		Schedule     map[string]ScheduleEntry `json:"schedule"`
	} `json:"analysis"`
	Forecast map[string]struct {
		Start    string `json:"start"`
		Finish   string `json:"finish"`
		DaysLate int    `json:"daysLate"`
	} `json:"forecast"`
	Confidence struct {
		Seed      uint32             `json:"seed"`
		P50       float64            `json:"p50"`
		P85       float64            `json:"p85"`
		P95       float64            `json:"p95"`
		Histogram []int              `json:"histogram"`
		OnTime    map[string]float64 `json:"onTime"`
		P85ByID   map[string]float64 `json:"p85ById"`
	} `json:"confidence"`
}

func loadFixtures(t *testing.T) map[string]fixture {
	t.Helper()
	paths, err := filepath.Glob("../../fixtures/forecast/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	out := map[string]fixture{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[filepath.Base(p)] = f
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %v\n want %v", what, got, want)
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestFixtureParity(t *testing.T) {
	for name, f := range loadFixtures(t) {
		t.Run(name, func(t *testing.T) {
			today, err := ParseDate(f.Today)
			if err != nil {
				t.Fatal(err)
			}
			for i := range f.Nodes {
				f.Nodes[i].DependsOn = orEmpty(f.Nodes[i].DependsOn)
			}
			a := Analyze(f.Nodes)
			x := f.Expect.Analysis
			eq(t, "remaining", a.Remaining, x.Remaining)
			eq(t, "order", orEmpty(a.Order), orEmpty(x.Order))
			eq(t, "critical path", a.CriticalPath, orEmpty(x.CriticalPath))
			var cyc []string
			for id := range a.InCycle {
				cyc = append(cyc, id)
			}
			sort.Strings(cyc)
			eq(t, "in cycle", orEmpty(cyc), orEmpty(x.InCycle))
			eq(t, "ready", a.Ready, orEmpty(x.Ready))
			eq(t, "at risk", a.AtRisk, orEmpty(x.AtRisk))
			eq(t, "blocked by", a.BlockedBy, x.BlockedBy)
			eq(t, "top blockers", a.TopBlockers, append([]Blocker{}, x.TopBlockers...))
			eq(t, "schedule", a.Schedule, x.Schedule)

			fc := Forecast(a, f.DueDates, today)
			if len(fc) != len(f.Expect.Forecast) {
				t.Errorf("forecast has %d issues, want %d", len(fc), len(f.Expect.Forecast))
			}
			for id, want := range f.Expect.Forecast {
				got := fc[id]
				eq(t, "forecast "+id, [3]any{FormatDate(got.Start), FormatDate(got.Finish), got.DaysLate}, [3]any{want.Start, want.Finish, want.DaysLate})
			}

			table := map[string][2]float64{}
			for k, v := range DefaultSpreads {
				table[k] = v
			}
			for k, v := range f.Spreads {
				table[k] = v
			}
			spread := SpreadFrom(func(id string) string { return f.Types[id] }, table)
			c := SimulateConfidence(f.Nodes, a, f.DueDates, spread, f.Runs, today, f.Seed)
			xc := f.Expect.Confidence
			eq(t, "seed", c.Seed, xc.Seed)
			eq(t, "percentiles", [3]float64{c.P50, c.P85, c.P95}, [3]float64{xc.P50, xc.P85, xc.P95})
			eq(t, "histogram", c.Histogram, append([]int{}, xc.Histogram...))
			eq(t, "on time", c.OnTime, orMap(xc.OnTime))
			eq(t, "p85 by id", c.P85ByID, orMap(xc.P85ByID))
		})
	}
}

func orMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}

func TestSeedIsReproducibleAndSensitive(t *testing.T) {
	nodes := []Node{{ID: "a", Estimate: 4, State: "todo"}, {ID: "b", Estimate: 4, State: "todo", DependsOn: []string{"a"}}}
	a := Analyze(nodes)
	today, _ := ParseDate("2025-10-06")
	spread := SpreadFrom(func(string) string { return "bug" }, DefaultSpreads)
	s1, s2 := uint32(1), uint32(2)
	x := SimulateConfidence(nodes, a, nil, spread, 200, today, &s1)
	y := SimulateConfidence(nodes, a, nil, spread, 200, today, &s1)
	z := SimulateConfidence(nodes, a, nil, spread, 200, today, &s2)
	if !reflect.DeepEqual(x, y) {
		t.Fatal("same seed gave different results")
	}
	if reflect.DeepEqual(x.Histogram, z.Histogram) {
		t.Fatal("different seeds gave identical histograms")
	}
}

func TestLongChainDoesNotOverflow(t *testing.T) {
	var nodes []Node
	for i := 0; i < 5000; i++ {
		n := Node{ID: string(rune('a'+i%26)) + itoa(i), Estimate: 1, State: "todo"}
		if i > 0 {
			n.DependsOn = []string{nodes[i-1].ID}
		}
		nodes = append(nodes, n)
	}
	a := Analyze(nodes)
	if a.Remaining != 5000 || len(a.CriticalPath) != 5000 {
		t.Fatalf("remaining %v, path %d", a.Remaining, len(a.CriticalPath))
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
