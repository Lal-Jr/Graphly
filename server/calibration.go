package main

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
)

// Every issue that finishes records its estimate next to how long it really took (working days
// from first entering an in-progress column to done). Once a project has enough of those, its
// forecast samples overruns from its own history instead of the generic defaults.

// TypeCalibration is the overrun range used for one issue type.
type TypeCalibration struct {
	// "type": from this type's own history; "pooled": from all of the project's history because
	// this type has too few samples; "default": the generic range.
	Source  string     `json:"source"`
	Samples int        `json:"samples"`
	Spread  [2]float64 `json:"spread"`
	// Median actual ÷ estimate, for display (1.3 = takes 30% longer than estimated).
	Median float64 `json:"median"`
}

// Calibration describes the overrun model a forecast used.
type Calibration struct {
	// "calibrated" once the project has Threshold completed, estimated issues; otherwise "default".
	Source    string                     `json:"source"`
	Samples   int                        `json:"samples"`
	Threshold int                        `json:"threshold"`
	Spreads   map[string][2]float64      `json:"spreads"`
	ByType    map[string]TypeCalibration `json:"byType"`
}

var calibratedTypes = []string{"bug", "story", "task"}

// Bounds keep one wild outlier (a "1 day" task that sat in review for a month) from making the
// whole forecast meaningless. The estimate stays the mode, so lo ≤ 1 ≤ hi.
const (
	minLow, maxHigh = 0.2, 6.0
	calibrationDays = 365
	maxSamples      = 500
)

func (s *Server) calibrate(ctx context.Context, q querier, projectID string) (*Calibration, error) {
	type sample struct {
		typ   string
		ratio float64
	}
	samples, err := collect(func(row pgx.Row) (sample, error) {
		var x sample
		var est, actual float64
		err := row.Scan(&x.typ, &est, &actual)
		x.ratio = actual / est
		return x, err
	})(q.Query(ctx, `SELECT type, estimate, actual_days FROM issue_actuals
		WHERE project_id = $1 AND completed_at > now() - make_interval(days => $2)
		ORDER BY completed_at DESC LIMIT $3`, projectID, calibrationDays, maxSamples))
	if err != nil {
		return nil, err
	}
	byType := map[string][]float64{}
	var all []float64
	for _, x := range samples {
		byType[x.typ] = append(byType[x.typ], x.ratio)
		all = append(all, x.ratio)
	}
	return buildCalibration(all, byType, s.cfg.CalibrationMinIssues, s.cfg.CalibrationMinPerType), nil
}

func buildCalibration(all []float64, byType map[string][]float64, threshold, perType int) *Calibration {
	c := &Calibration{Source: "default", Samples: len(all), Threshold: threshold, Spreads: map[string][2]float64{}, ByType: map[string]TypeCalibration{}}
	calibrated := threshold > 0 && len(all) >= threshold
	if calibrated {
		c.Source = "calibrated"
	}
	pooled := ratioSpread(all)
	for _, t := range calibratedTypes {
		tc := TypeCalibration{Source: "default", Samples: len(byType[t]), Spread: forecast.DefaultSpreads[t]}
		switch {
		case calibrated && len(byType[t]) >= max(1, perType):
			tc.Source, tc.Spread, tc.Median = "type", ratioSpread(byType[t]), round2(quantile(byType[t], 0.5))
		case calibrated:
			tc.Source, tc.Spread, tc.Median = "pooled", pooled, round2(quantile(all, 0.5))
		}
		c.ByType[t] = tc
		c.Spreads[t] = tc.Spread
	}
	return c
}

// ratioSpread is the 10th–90th percentile of actual÷estimate, clamped so the estimate stays the mode.
func ratioSpread(ratios []float64) [2]float64 {
	if len(ratios) == 0 {
		return forecast.DefaultSpreads["task"]
	}
	lo := math.Min(1, math.Max(minLow, quantile(ratios, 0.1)))
	hi := math.Max(1, math.Min(maxHigh, quantile(ratios, 0.9)))
	return [2]float64{round2(lo), round2(hi)}
}

// quantile is the nearest-rank quantile.
func quantile(xs []float64, p float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	k := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(len(s)-1, max(0, k))]
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// workingDaysSpent counts Monday–Friday calendar days from start to end inclusive (at least 1),
// in the deployment's planning timezone.
func workingDaysSpent(start, end time.Time, loc *time.Location) float64 {
	a, b := forecast.Day(start.In(loc)), forecast.Day(end.In(loc))
	n := 0
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n++
		}
	}
	return float64(max(1, n))
}

// syncLifecycle brings started_at / completed_at and the recorded actual in line with each issue's
// current status. Call it in the same transaction as any change that can move an issue between
// categories: a status edit, a PR moving it, a column deleted or recategorised, an import.
func (s *Server) syncLifecycle(ctx context.Context, tx pgx.Tx, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}
	type row struct {
		id, projectID, typ, category string
		estimate                     float64
		started, completed           *time.Time
	}
	rows, err := collect(func(r pgx.Row) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.projectID, &x.typ, &x.category, &x.estimate, &x.started, &x.completed)
	})(tx.Query(ctx, `
		UPDATE issues i SET
			started_at = coalesce(i.started_at, CASE WHEN st.category = 'in_progress' THEN now() END),
			completed_at = CASE WHEN st.category = 'done' THEN coalesce(i.completed_at, now()) END
		FROM statuses st WHERE st.id = i.status_id AND i.id = ANY($1)
		RETURNING i.id, i.project_id, i.type, st.category, i.estimate, i.started_at, i.completed_at`, issueIDs))
	if err != nil {
		return err
	}
	for _, x := range rows {
		if x.category == "done" && x.started != nil && x.completed != nil && x.estimate > 0 && x.typ != "epic" && !x.completed.Before(*x.started) {
			_, err = tx.Exec(ctx, `
				INSERT INTO issue_actuals (issue_id, project_id, type, estimate, actual_days, started_at, completed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (issue_id) DO UPDATE SET type = EXCLUDED.type, estimate = EXCLUDED.estimate,
					actual_days = EXCLUDED.actual_days, started_at = EXCLUDED.started_at, completed_at = EXCLUDED.completed_at`,
				x.id, x.projectID, x.typ, x.estimate, workingDaysSpent(*x.started, *x.completed, s.cfg.ForecastLocation), *x.started, *x.completed)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM issue_actuals WHERE issue_id = $1`, x.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
