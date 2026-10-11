package main

import (
	"context"

	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A 5,000-issue project end to end: the snapshot the browser loads, the forecast endpoint and the
// background job. Budgets are generous; they catch a regression to quadratic work.
func TestLargeProject(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	srv, app := newTestApp(t, nil)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Big", "big@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "BIG", "name": "Big"}, &p)
	if err := app.inTx(context.Background(), func(tx pgx.Tx) error { return seedSynthetic(context.Background(), tx, p.ID, 5000, 1) }); err != nil {
		t.Fatal(err)
	}

	timed := func(what string, budget time.Duration, fn func()) {
		t.Helper()
		start := time.Now()
		fn()
		took := time.Since(start)
		t.Logf("%s: %v", what, took.Round(time.Millisecond))
		if took > budget {
			t.Errorf("%s took %v (budget %v)", what, took, budget)
		}
	}
	var snap projectSnapshot
	timed("project snapshot", 5*time.Second, func() { c.must("GET", "/api/projects/"+p.ID, nil, &snap) })
	if len(snap.Issues) != 5000 || len(snap.Links) < 17000 {
		t.Fatalf("snapshot: %d issues, %d links", len(snap.Issues), len(snap.Links))
	}
	var fc struct {
		Remaining float64
		Runs      int
	}
	timed("forecast endpoint (cold)", 10*time.Second, func() { c.must("GET", "/api/projects/"+p.ID+"/forecast?issues=false", nil, &fc) })
	timed("forecast endpoint (cached)", time.Second, func() { c.must("GET", "/api/projects/"+p.ID+"/forecast?issues=false", nil, &fc) })
	if fc.Remaining <= 0 || fc.Runs == 0 {
		t.Fatalf("forecast: %+v", fc)
	}
	timed("forecast job", 15*time.Second, func() {
		if err := app.runForecastJob(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
