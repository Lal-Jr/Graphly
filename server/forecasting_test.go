package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
)

func TestServerForecast(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.ForecastInterval = time.Hour })
	c := newClient(t, srv)
	_, ws := signup(t, c, "Fran", "fran@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "FC", "name": "Forecast"}, &p)

	// a(2) → b(3), with b due before it can possibly finish; c(1) runs in parallel.
	today := app.today()
	due := forecast.FormatDate(forecast.AddWorkdays(today, 2))
	var a, b, cc Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "A", "estimate": 2}, &a)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "B", "estimate": 3, "dueDate": due}, &b)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "C", "estimate": 1}, &cc)
	c.must("POST", "/api/issues/"+b.ID+"/blockers", map[string]string{"blockerId": a.ID}, nil)

	var f ProjectForecast
	c.must("GET", "/api/projects/"+p.ID+"/forecast", nil, &f)
	if f.Remaining != 5 || len(f.CriticalPath) != 2 || f.CriticalPath[0] != a.ID || f.CriticalPath[1] != b.ID {
		t.Fatalf("forecast: remaining %v, critical path %v", f.Remaining, f.CriticalPath)
	}
	if want := forecast.FormatDate(forecast.AddWorkdays(today, 4)); f.Finish == nil || *f.Finish != want {
		t.Fatalf("finish %v, want %s", f.Finish, want)
	}
	if len(f.Late) != 1 || f.Late[0].ID != b.ID || f.Late[0].DaysLate != 2 || f.OpenIssues != 3 || f.Blocked != 1 {
		t.Fatalf("late %+v, open %d, blocked %d", f.Late, f.OpenIssues, f.Blocked)
	}
	if f.P85 < f.Remaining || f.Issues[b.ID].OnTime == nil || *f.Issues[b.ID].OnTime != 0 {
		t.Fatalf("confidence: p85 %v, b on time %v", f.P85, f.Issues[b.ID].OnTime)
	}
	// Same plan, same numbers.
	var again ProjectForecast
	c.must("GET", "/api/projects/"+p.ID+"/forecast?issues=false", nil, &again)
	if again.Seed != f.Seed || again.P85 != f.P85 || again.Issues != nil {
		t.Fatalf("not reproducible: %v/%v vs %v/%v", again.Seed, again.P85, f.Seed, f.P85)
	}

	// Clients can no longer write history.
	if code := c.do("PUT", "/api/projects/"+p.ID+"/snapshot", map[string]any{"finish": "2030-01-02", "remaining": 5}, nil); code != http.StatusGone {
		t.Fatalf("client snapshot: %d", code)
	}

	// The job writes the day's reading; concurrent runs on several instances don't collide.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runForecastJobNow(context.Background(), app); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var snaps []snapshot
	c.must("GET", "/api/projects/"+p.ID+"/snapshots", nil, &snaps)
	if len(snaps) != 1 || snaps[0].Day != forecast.FormatDate(today) || *snaps[0].Finish != *f.Finish || snaps[0].OpenIssues != 3 {
		t.Fatalf("snapshots: %+v", snaps)
	}
	var source string
	if err := app.db.QueryRow(context.Background(), `SELECT source FROM forecast_snapshots WHERE project_id = $1`, p.ID).Scan(&source); err != nil || source != "server" {
		t.Fatalf("source %q, %v", source, err)
	}

	// Editing the plan changes the forecast (the cache is keyed on the project's version).
	c.must("PATCH", "/api/issues/"+a.ID, map[string]any{"estimate": 4}, nil)
	c.must("GET", "/api/projects/"+p.ID+"/forecast", nil, &f)
	if f.Remaining != 7 {
		t.Fatalf("after re-estimate: %v", f.Remaining)
	}

	// Outsiders can't read it.
	other := newClient(t, srv)
	signup(t, other, "Out", "out@example.com")
	if code := other.do("GET", "/api/projects/"+p.ID+"/forecast", nil, nil); code != http.StatusNotFound {
		t.Fatalf("outsider forecast: %d", code)
	}
}

// A project nobody opens still gets a reading every day.
func TestSnapshotsWithoutClients(t *testing.T) {
	srv, app := newTestApp(t, nil)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Gil", "gil@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "QUIET", "name": "Quiet", "sample": true}, &p)
	if err := runForecastJobNow(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := app.db.QueryRow(context.Background(), `SELECT count(*) FROM forecast_snapshots WHERE project_id = $1 AND source = 'server'`, p.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("server readings: %d, %v", n, err)
	}
}
