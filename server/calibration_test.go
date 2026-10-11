package main

import (
	"context"
	"testing"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
)

type pgxTx = pgx.Tx

func TestBuildCalibration(t *testing.T) {
	// Too little history: generic ranges, labelled as such.
	c := buildCalibration([]float64{1, 2}, map[string][]float64{"bug": {1, 2}}, 20, 5)
	if c.Source != "default" || c.Spreads["bug"] != forecast.DefaultSpreads["bug"] || c.ByType["bug"].Source != "default" {
		t.Fatalf("below threshold: %+v", c)
	}

	// Enough history: bugs (10 samples) use their own range; stories (2) fall back to the pooled one.
	bugs := []float64{0.5, 1, 1, 1.5, 2, 2, 2.5, 3, 4, 40} // one absurd outlier
	stories := []float64{1, 1.2}
	all := append(append([]float64{}, bugs...), stories...)
	c = buildCalibration(all, map[string][]float64{"bug": bugs, "story": stories}, 10, 5)
	if c.Source != "calibrated" || c.Samples != 12 {
		t.Fatalf("calibrated: %+v", c)
	}
	if b := c.ByType["bug"]; b.Source != "type" || b.Spread != [2]float64{0.5, 4} || b.Median != 2 {
		t.Fatalf("bug calibration: %+v", b)
	}
	if st := c.ByType["story"]; st.Source != "pooled" || st.Spread != ratioSpread(all) {
		t.Fatalf("story calibration: %+v", st)
	}
	// Clamped so the estimate stays the most likely outcome, and outliers can't explode the range.
	if sp := ratioSpread([]float64{2, 3, 50, 60, 70}); sp[0] != 1 || sp[1] != maxHigh {
		t.Fatalf("clamped spread: %v", sp)
	}
	if sp := ratioSpread([]float64{0.01, 0.02, 0.5}); sp[0] != minLow || sp[1] != 1 {
		t.Fatalf("clamped low: %v", sp)
	}
}

func TestWorkingDaysSpent(t *testing.T) {
	fri := time.Date(2025, 10, 10, 15, 0, 0, 0, time.UTC)
	mon := time.Date(2025, 10, 13, 9, 0, 0, 0, time.UTC)
	if d := workingDaysSpent(fri, mon, time.UTC); d != 2 {
		t.Fatalf("fri→mon: %v", d)
	}
	if d := workingDaysSpent(mon, mon.Add(time.Hour), time.UTC); d != 1 {
		t.Fatalf("same day: %v", d)
	}
}

func TestActualsAndCalibratedForecast(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.CalibrationMinIssues, c.CalibrationMinPerType = 4, 2 })
	c := newClient(t, srv)
	_, ws := signup(t, c, "Hal", "hal@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "CAL", "name": "Calibrate"}, &p)
	var snap projectSnapshot
	c.must("GET", "/api/projects/"+p.ID, nil, &snap)
	todo, doing, done := snap.Statuses[1].ID, snap.Statuses[2].ID, snap.Statuses[4].ID
	ctx := context.Background()

	var f ProjectForecast
	c.must("GET", "/api/projects/"+p.ID+"/forecast", nil, &f)
	if f.Calibration == nil || f.Calibration.Source != "default" || f.Spreads["bug"] != forecast.DefaultSpreads["bug"] {
		t.Fatalf("new project should use default overruns: %+v", f.Calibration)
	}

	// Four 1-day bugs that each took three working days (started Monday, done Wednesday).
	mon := forecast.Day(time.Now().AddDate(0, 0, -21))
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	for range 4 {
		var is Issue
		c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Bug", "type": "bug", "estimate": 1, "statusId": todo}, &is)
		c.must("PATCH", "/api/issues/"+is.ID, map[string]any{"statusId": doing}, nil)
		if _, err := app.db.Exec(ctx, `UPDATE issues SET started_at = $2 WHERE id = $1`, is.ID, mon.Add(10*time.Hour)); err != nil {
			t.Fatal(err)
		}
		c.must("PATCH", "/api/issues/"+is.ID, map[string]any{"statusId": done}, nil)
		if _, err := app.db.Exec(ctx, `UPDATE issues SET completed_at = $2 WHERE id = $1`, is.ID, mon.AddDate(0, 0, 2).Add(15*time.Hour)); err != nil {
			t.Fatal(err)
		}
		// Re-sync with the backdated times, as if it had really happened over those days.
		if err := app.inTx(ctx, func(tx pgxTx) error { return app.syncLifecycle(ctx, tx, []string{is.ID}) }); err != nil {
			t.Fatal(err)
		}
	}
	var actual float64
	var n int
	if err := app.db.QueryRow(ctx, `SELECT count(*), max(actual_days) FROM issue_actuals WHERE project_id = $1`, p.ID).Scan(&n, &actual); err != nil || n != 4 || actual != 3 {
		t.Fatalf("actuals: %d rows, %v days, %v", n, actual, err)
	}

	// An issue moved straight to done has no start, so it records no actual.
	var skip Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Quick", "statusId": todo}, &skip)
	c.must("PATCH", "/api/issues/"+skip.ID, map[string]any{"statusId": done}, nil)
	if err := app.db.QueryRow(ctx, `SELECT count(*) FROM issue_actuals WHERE issue_id = $1`, skip.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("straight-to-done recorded an actual: %d", n)
	}

	var open Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Open bug", "type": "bug", "estimate": 2}, &open)
	c.must("GET", "/api/projects/"+p.ID+"/forecast", nil, &f)
	if f.Calibration.Source != "calibrated" || f.Calibration.Samples != 4 || f.Calibration.ByType["bug"].Source != "type" {
		t.Fatalf("calibration: %+v", f.Calibration)
	}
	// Every bug took exactly 3× its estimate: the range collapses to [1, 3]... clamped so lo ≤ 1.
	if f.Spreads["bug"] != [2]float64{1, 3} || f.Calibration.ByType["task"].Source != "pooled" {
		t.Fatalf("bug spread %v, task %+v", f.Spreads["bug"], f.Calibration.ByType["task"])
	}
	if f.P50 <= 2 {
		t.Fatalf("calibrated forecast should expect the bug to overrun: p50 %v", f.P50)
	}

	// Reopening a finished issue withdraws its actual.
	var one string
	if err := app.db.QueryRow(ctx, `SELECT issue_id FROM issue_actuals WHERE project_id = $1 LIMIT 1`, p.ID).Scan(&one); err != nil {
		t.Fatal(err)
	}
	c.must("PATCH", "/api/issues/"+one, map[string]any{"statusId": doing}, nil)
	if err := app.db.QueryRow(ctx, `SELECT count(*) FROM issue_actuals WHERE project_id = $1`, p.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("after reopen: %d actuals", n)
	}
}
