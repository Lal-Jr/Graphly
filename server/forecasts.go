package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// The scheduling engine runs in the browser, so clients report the day's forecast. The latest
// report of the day wins; there's one row per project per day.
type snapshot struct {
	Day        string  `json:"day"`
	Finish     *string `json:"finish"`
	FinishP85  *string `json:"finishP85"`
	Remaining  float64 `json:"remaining"`
	OpenIssues int     `json:"openIssues"`
	Blocked    int     `json:"blocked"`
}

func (s *Server) putSnapshot(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, _, err := s.projectAccess(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	var body snapshot
	if err := readJSON(r, &body); err != nil {
		return err
	}
	for _, d := range []*string{body.Finish, body.FinishP85} {
		if d != nil {
			if _, err := time.Parse("2006-01-02", *d); err != nil {
				return badRequest("dates must be YYYY-MM-DD")
			}
		}
	}
	if body.Remaining < 0 || body.Remaining > 100000 || body.OpenIssues < 0 || body.Blocked < 0 {
		return badRequest("invalid snapshot")
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO forecast_snapshots (project_id, day, finish, finish_p85, remaining, open_issues, blocked)
		VALUES ($1, current_date, $2::date, $3::date, $4, $5, $6)
		ON CONFLICT (project_id, day) DO UPDATE SET finish = EXCLUDED.finish, finish_p85 = EXCLUDED.finish_p85,
			remaining = EXCLUDED.remaining, open_issues = EXCLUDED.open_issues, blocked = EXCLUDED.blocked, taken_at = now()`,
		id, body.Finish, body.FinishP85, body.Remaining, body.OpenIssues, body.Blocked)
	if err != nil {
		return err
	}
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, _, err := s.projectAccess(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	snaps, err := collect(func(row pgx.Row) (snapshot, error) {
		var sn snapshot
		return sn, row.Scan(&sn.Day, &sn.Finish, &sn.FinishP85, &sn.Remaining, &sn.OpenIssues, &sn.Blocked)
	})(s.db.Query(r.Context(), `
		SELECT to_char(day, 'YYYY-MM-DD'), to_char(finish, 'YYYY-MM-DD'), to_char(finish_p85, 'YYYY-MM-DD'), remaining, open_issues, blocked
		FROM forecast_snapshots WHERE project_id = $1 AND day > current_date - 60 ORDER BY day`, id))
	if err != nil {
		return err
	}
	return ok(w, snaps)
}
