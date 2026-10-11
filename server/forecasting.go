package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The server owns the forecast. The browser still runs the same engine for instant what-if edits,
// but the daily history, "now late" and "date moved" alerts come from here, so they don't depend on
// anyone having the project open (and can't be forged by a client).

const forecastRuns = 1000

// planIssue is what the engine needs to know about one issue.
type planIssue struct {
	ID         string
	Number     int
	Title      string
	Type       string
	Estimate   float64
	DueDate    *string
	Category   string
	AssigneeID *string
}

type plan struct {
	ProjectID string
	Key       string
	Issues    []planIssue
	Links     []Link
}

func loadPlan(ctx context.Context, q querier, projectID string) (*plan, error) {
	p := &plan{ProjectID: projectID}
	if err := q.QueryRow(ctx, `SELECT key FROM projects WHERE id = $1`, projectID).Scan(&p.Key); err != nil {
		return nil, err
	}
	var err error
	p.Issues, err = collect(func(row pgx.Row) (planIssue, error) {
		var i planIssue
		return i, row.Scan(&i.ID, &i.Number, &i.Title, &i.Type, &i.Estimate, &i.DueDate, &i.Category, &i.AssigneeID)
	})(q.Query(ctx, `
		SELECT i.id, i.number, i.title, i.type, i.estimate, to_char(i.due_date, 'YYYY-MM-DD'), st.category, i.assignee_id
		FROM issues i JOIN statuses st ON st.id = i.status_id WHERE i.project_id = $1 ORDER BY i.rank, i.number`, projectID))
	if err != nil {
		return nil, err
	}
	p.Links, err = collect(func(row pgx.Row) (Link, error) {
		var l Link
		return l, row.Scan(&l.BlockerID, &l.BlockedID)
	})(q.Query(ctx, `SELECT l.blocker_id, l.blocked_id FROM issue_links l JOIN issues i ON i.id = l.blocked_id WHERE i.project_id = $1
		ORDER BY l.created_at, l.blocker_id`, projectID))
	return p, err
}

// nodes builds the engine's input exactly as the browser does (web/src/store/useProject.tsx):
// epics are excluded, and only links between non-epic issues count.
func (p *plan) nodes() ([]forecast.Node, map[string]string, map[string]string) {
	work := map[string]bool{}
	for _, i := range p.Issues {
		if i.Type != "epic" {
			work[i.ID] = true
		}
	}
	blockers := map[string][]string{}
	for _, l := range p.Links {
		if work[l.BlockerID] && work[l.BlockedID] {
			blockers[l.BlockedID] = append(blockers[l.BlockedID], l.BlockerID)
		}
	}
	var nodes []forecast.Node
	due := map[string]string{}
	types := map[string]string{}
	for _, i := range p.Issues {
		if !work[i.ID] {
			continue
		}
		deps := blockers[i.ID]
		if deps == nil {
			deps = []string{}
		}
		nodes = append(nodes, forecast.Node{ID: i.ID, Estimate: i.Estimate, State: i.Category, DependsOn: deps})
		if i.DueDate != nil {
			due[i.ID] = *i.DueDate
		}
		types[i.ID] = i.Type
	}
	return nodes, due, types
}

type issueForecast struct {
	Start    string   `json:"start"`
	Finish   string   `json:"finish"`
	DaysLate int      `json:"daysLate"`
	Slack    float64  `json:"slack"`
	OnTime   *float64 `json:"onTime,omitempty"`
}

type lateIssue struct {
	ID         string  `json:"id"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	AssigneeID *string `json:"assigneeId"`
	DueDate    string  `json:"dueDate"`
	Finish     string  `json:"finish"`
	DaysLate   int     `json:"daysLate"`
}

// ProjectForecast is the server's view of when a project ships and how sure we can be.
type ProjectForecast struct {
	ProjectID  string    `json:"projectId"`
	ComputedAt time.Time `json:"computedAt"`
	// The planning day ("today" in the configured timezone) the dates count from.
	Today     string  `json:"today"`
	Runs      int     `json:"runs"`
	Seed      uint32  `json:"seed"`
	Remaining float64 `json:"remaining"`
	// Finish if every estimate holds, and at the 50/85/95th percentile of simulated runs.
	Finish       *string                  `json:"finish"`
	FinishP50    *string                  `json:"finishP50"`
	FinishP85    *string                  `json:"finishP85"`
	FinishP95    *string                  `json:"finishP95"`
	P50          float64                  `json:"p50"`
	P85          float64                  `json:"p85"`
	P95          float64                  `json:"p95"`
	Histogram    []int                    `json:"histogram"`
	CriticalPath []string                 `json:"criticalPath"`
	OpenIssues   int                      `json:"openIssues"`
	Blocked      int                      `json:"blocked"`
	Late         []lateIssue              `json:"late"`
	Spreads      map[string][2]float64    `json:"spreads"`
	Calibration  *Calibration             `json:"calibration"`
	Quality      *EstimateQuality         `json:"quality"`
	Issues       map[string]issueForecast `json:"issues,omitempty"`
}

func dateStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := forecast.FormatDate(*t)
	return &s
}

// computeForecast runs the engine over a plan. cal may be nil (default overruns).
func computeForecast(p *plan, today time.Time, cal *Calibration) *ProjectForecast {
	nodes, due, types := p.nodes()
	a := forecast.Analyze(nodes)
	fc := forecast.Forecast(a, due, today)
	spreads := forecast.DefaultSpreads
	if cal != nil && cal.Spreads != nil {
		spreads = cal.Spreads
	}
	conf := forecast.SimulateConfidence(nodes, a, due, forecast.SpreadFrom(func(id string) string { return types[id] }, spreads), forecastRuns, today, nil)

	out := &ProjectForecast{
		ProjectID:    p.ProjectID,
		ComputedAt:   time.Now(),
		Today:        forecast.FormatDate(today),
		Runs:         conf.Runs,
		Seed:         conf.Seed,
		Remaining:    a.Remaining,
		Finish:       dateStr(forecast.DateAt(today, a.Remaining)),
		FinishP50:    dateStr(forecast.DateAt(today, conf.P50)),
		FinishP85:    dateStr(forecast.DateAt(today, conf.P85)),
		FinishP95:    dateStr(forecast.DateAt(today, conf.P95)),
		P50:          conf.P50,
		P85:          conf.P85,
		P95:          conf.P95,
		Histogram:    conf.Histogram,
		CriticalPath: a.CriticalPath,
		Blocked:      len(a.BlockedBy),
		Late:         []lateIssue{},
		Spreads:      spreads,
		Calibration:  cal,
		Issues:       make(map[string]issueForecast, len(fc)),
	}
	byID := map[string]planIssue{}
	for _, i := range p.Issues {
		byID[i.ID] = i
	}
	for _, n := range nodes {
		if n.State != "done" {
			out.OpenIssues++
		}
	}
	out.Quality = estimateQuality(p)
	for _, id := range a.Order {
		f := fc[id]
		e := issueForecast{Start: forecast.FormatDate(f.Start), Finish: forecast.FormatDate(f.Finish), DaysLate: f.DaysLate, Slack: a.Schedule[id].Slack}
		if v, ok := conf.OnTime[id]; ok {
			e.OnTime = &v
		}
		out.Issues[id] = e
		if f.DaysLate > 0 {
			i := byID[id]
			out.Late = append(out.Late, lateIssue{ID: id, Number: i.Number, Title: i.Title, AssigneeID: i.AssigneeID, DueDate: *i.DueDate, Finish: e.Finish, DaysLate: f.DaysLate})
		}
	}
	return out
}

// today is the planning day in the deployment's timezone.
func (s *Server) today() time.Time {
	return forecast.Day(time.Now().In(s.cfg.ForecastLocation))
}

// forecastFor computes a project's forecast, reusing a cached one while nothing in the project has
// changed (every write to a project publishes an event, so its latest event id is a cheap version).
func (s *Server) forecastFor(ctx context.Context, projectID string) (*ProjectForecast, error) {
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM events WHERE project_id = $1`, projectID).Scan(&version); err != nil {
		return nil, err
	}
	today := s.today()
	key := projectID + "/" + strconv.FormatInt(version, 10) + "/" + forecast.FormatDate(today)
	if f := s.forecasts.get(key); f != nil {
		return f, nil
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, err := loadPlan(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	cal, err := s.calibrate(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	f := computeForecast(p, today, cal)
	s.forecasts.put(key, f)
	return f, nil
}

// getForecast returns the project's current forecast. ?issues=false leaves out the per-issue map.
func (s *Server) getForecast(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.projectRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	f, err := s.forecastFor(r.Context(), id)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("issues") == "false" {
		c := *f
		c.Issues = nil
		return ok(w, c)
	}
	return ok(w, f)
}

// forecastCache is a small LRU-ish map; entries are keyed by project version so they never go stale.
type forecastCache struct {
	mu   sync.Mutex
	max  int
	m    map[string]*ProjectForecast
	keys []string
}

func newForecastCache(max int) *forecastCache {
	return &forecastCache{max: max, m: map[string]*ProjectForecast{}}
}

func (c *forecastCache) get(k string) *ProjectForecast {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k]
}

func (c *forecastCache) put(k string, f *ProjectForecast) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.m[k] = f
	for len(c.keys) > c.max {
		delete(c.m, c.keys[0])
		c.keys = c.keys[1:]
	}
}

// ---- history ----

type snapshot struct {
	Day        string  `json:"day"`
	Finish     *string `json:"finish"`
	FinishP85  *string `json:"finishP85"`
	Remaining  float64 `json:"remaining"`
	OpenIssues int     `json:"openIssues"`
	Blocked    int     `json:"blocked"`
}

// putSnapshot used to accept the browser's reading of the day. History is computed by the server
// now, so client reports are refused rather than trusted.
func (s *Server) putSnapshot(w http.ResponseWriter, r *http.Request, u *User) error {
	return errStatus(http.StatusGone, "forecast history is recorded by the server; clients no longer report it")
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.projectRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	// A brand-new project (or one the job hasn't reached yet) gets today's reading on first view.
	var have bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM forecast_snapshots WHERE project_id = $1 AND day = $2)`,
		id, s.today()).Scan(&have); err != nil {
		return err
	}
	if !have {
		if _, err := s.recordForecast(r.Context(), id); err != nil {
			return err
		}
	}
	snaps, err := collect(func(row pgx.Row) (snapshot, error) {
		var sn snapshot
		return sn, row.Scan(&sn.Day, &sn.Finish, &sn.FinishP85, &sn.Remaining, &sn.OpenIssues, &sn.Blocked)
	})(s.db.Query(r.Context(), `
		SELECT to_char(day, 'YYYY-MM-DD'), to_char(finish, 'YYYY-MM-DD'), to_char(finish_p85, 'YYYY-MM-DD'), remaining, open_issues, blocked
		FROM forecast_snapshots WHERE project_id = $1 AND day > $2::date - 60 ORDER BY day`, id, s.today()))
	if err != nil {
		return err
	}
	return ok(w, snaps)
}

// recordForecast computes a project's forecast, writes the day's history row and the latest
// forecast, and returns the forecast it replaced (nil the first time) so callers can diff them.
func (s *Server) recordForecast(ctx context.Context, projectID string) (prev *ProjectForecast, err error) {
	f, err := s.forecastFor(ctx, projectID)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT data FROM project_forecasts WHERE project_id = $1 FOR UPDATE`, projectID).Scan(&raw)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if raw != nil {
			prev = &ProjectForecast{}
			if json.Unmarshal(raw, prev) != nil {
				prev = nil
			}
		}
		slim := *f
		slim.Issues = nil
		data, err := json.Marshal(slim)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_forecasts (project_id, computed_at, day, finish, finish_p85, data) VALUES ($1, now(), $2, $3::date, $4::date, $5)
			ON CONFLICT (project_id) DO UPDATE SET computed_at = now(), day = EXCLUDED.day, finish = EXCLUDED.finish,
				finish_p85 = EXCLUDED.finish_p85, data = EXCLUDED.data`,
			projectID, f.Today, f.Finish, f.FinishP85, data); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO forecast_snapshots (project_id, day, finish, finish_p85, remaining, open_issues, blocked, source)
			VALUES ($1, $2::date, $3::date, $4::date, $5, $6, $7, 'server')
			ON CONFLICT (project_id, day) DO UPDATE SET finish = EXCLUDED.finish, finish_p85 = EXCLUDED.finish_p85,
				remaining = EXCLUDED.remaining, open_issues = EXCLUDED.open_issues, blocked = EXCLUDED.blocked, taken_at = now(), source = 'server'`,
			projectID, f.Today, f.Finish, f.FinishP85, f.Remaining, f.OpenIssues, f.Blocked)
		return err
	})
	if err != nil {
		return nil, err
	}
	if s.onForecast != nil {
		if err := s.onForecast(ctx, prev, f); err != nil {
			slog.Warn("forecast follow-up", "project", projectID, "err", err)
		}
	}
	return prev, nil
}

// forecastLoop recomputes every project with open work on an interval. A session advisory lock
// makes one instance do each run when several are deployed, and projects computed within the last
// half interval are skipped, so instances that alternate don't double up.
func (s *Server) forecastLoop(ctx context.Context) {
	if s.cfg.ForecastInterval <= 0 {
		return
	}
	t := time.NewTicker(s.cfg.ForecastInterval)
	defer t.Stop()
	for {
		if err := s.runForecastJob(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("forecast job", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

const forecastJobLock = 7249002

func (s *Server) runForecastJob(ctx context.Context) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, forecastJobLock).Scan(&got); err != nil || !got {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, forecastJobLock)

	started := time.Now()
	ids, err := collect(scanString)(s.db.Query(ctx, `
		SELECT p.id FROM projects p
		LEFT JOIN project_forecasts f ON f.project_id = p.id
		WHERE (f.computed_at IS NULL OR f.computed_at < now() - $1::interval OR f.day <> $2::date)
		AND (EXISTS (SELECT 1 FROM issues i JOIN statuses st ON st.id = i.status_id WHERE i.project_id = p.id AND st.category <> 'done')
			OR f.project_id IS NOT NULL)
		ORDER BY f.computed_at NULLS FIRST`, (s.cfg.ForecastInterval / 2).String(), s.today()))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := s.recordForecast(ctx, id); err != nil && ctx.Err() == nil {
			slog.Warn("forecast project", "project", id, "err", err)
		}
	}
	observeForecastJob(time.Since(started), len(ids))
	return nil
}

// runForecastJobNow is for tests: run once, bypassing the interval.
func runForecastJobNow(ctx context.Context, s *Server) error { return s.runForecastJob(ctx) }

var _ = (*pgxpool.Pool)(nil)
