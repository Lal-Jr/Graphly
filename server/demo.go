package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Teammates who appear in every demo workspace so boards, filters and the bottleneck view have people in them.
// They're demo users with no usable password.
var demoTeammates = []struct{ name, color string }{
	{"Sam Okafor", "#1F845A"},
	{"Lena Park", "#E56910"},
}

// startDemo signs the visitor into a fresh guest account with a sample project, no sign-up needed.
// Guests, their teammates and workspaces are deleted after a week by pruneLoop.
func (s *Server) startDemo(w http.ResponseWriter, r *http.Request) error {
	if !s.demo.allow(clientIP(r, s.cfg.ClientIPHeader)) {
		return errStatus(http.StatusTooManyRequests, "too many demo workspaces from your network — try again in a few minutes")
	}
	ctx := r.Context()
	var guest User
	var projectID string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		guest, err = createDemoUser(ctx, tx, "Guest", "#0C66E4")
		if err != nil {
			return err
		}
		var wsID string
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name, is_demo) VALUES ('Demo workspace', true) RETURNING id`).Scan(&wsID); err != nil {
			return err
		}
		members := []string{guest.ID}
		if _, err := tx.Exec(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, wsID, guest.ID); err != nil {
			return err
		}
		for _, t := range demoTeammates {
			mate, err := createDemoUser(ctx, tx, t.name, t.color)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, wsID, mate.ID); err != nil {
				return err
			}
			members = append(members, mate.ID)
		}
		p, seed, err := createProjectWithIDs(ctx, tx, wsID, "LAUNCH", "Product launch", "A sample plan: explore the board, dependency graph, forecast and insights.", true, members)
		if err != nil {
			return err
		}
		projectID = p.ID
		if err := seedDemoHistory(ctx, tx, p.ID, seed, members, time.Now()); err != nil {
			return err
		}
		return s.startSession(ctx, w, tx, guest.ID)
	})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"user": guest, "projectId": projectID})
}

func createDemoUser(ctx context.Context, tx pgx.Tx, name, color string) (User, error) {
	u := User{Name: name, Color: color}
	// "!" is not a valid bcrypt hash, so these accounts can never be logged into with a password.
	err := tx.QueryRow(ctx, `INSERT INTO users (email, name, password_hash, color, is_demo) VALUES ($1, $2, '!', $3, true)
		RETURNING id, email`, "guest-"+randomToken(9)+"@demo.graphly", name, color).Scan(&u.ID, &u.Email)
	return u, err
}

// seedDemoHistory backdates a believable last day of work (a merged PR, work started, a re-estimate,
// new scope and a new dependency) plus a week of forecast readings, so the standup, history and
// trend views have something real to show on a brand-new demo.
func seedDemoHistory(ctx context.Context, tx pgx.Tx, projectID string, seed *seeded, members []string, now time.Time) error {
	guest, sam, lena := members[0], members[1], members[2]
	id := seed.issues
	st := seed.statuses // Backlog, To Do, In Progress, In Review, Done
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }

	type act struct {
		at    time.Time
		issue string
		actor any
		kind  string
		data  map[string]any
	}
	status := func(from, to int, extra ...any) map[string]any {
		m := map[string]any{"field": "status", "from": st[from], "to": st[to]}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i].(string)] = extra[i+1]
		}
		return m
	}
	pr := func(state, title string, n int) map[string]any {
		return map[string]any{"state": state, "repo": "acme/auth-service", "number": n, "url": "https://github.com/acme/auth-service/pull/" + itoa(n), "title": title, "author": "samokafor"}
	}
	acts := []act{
		// Earlier in the week.
		{ago(100), "spec", guest, "updated", status(3, 4)},
		{ago(99), "schema", sam, "unblocked", map[string]any{"by": id["spec"]}},
		{ago(96), "api", sam, "updated", status(1, 2)},
		// Since the last standup.
		{ago(19), "schema", nil, "pr", pr("merged", "Users and sessions schema (LAUNCH-4)", 38)},
		{ago(19), "schema", nil, "updated", status(3, 4, "via", "github", "pr", 38)},
		{ago(19), "api", nil, "unblocked", map[string]any{"by": id["schema"]}},
		{ago(18), "ui", lena, "updated", status(1, 2)},
		{ago(16), "audit", lena, "updated", map[string]any{"field": "estimate", "from": 1, "to": 3}},
		{ago(15), "bug2", sam, "created", map[string]any{}},
		{ago(12), "audit", lena, "blocker.add", map[string]any{"blocker": id["reset"]}},
		{ago(6), "api", nil, "pr", pr("open", "Sign-up and login endpoints (LAUNCH-5)", 42)},
		{ago(3), "bug1", nil, "pr", pr("open", "Set Secure on session cookie behind proxy (LAUNCH-11)", 44)},
		{ago(3), "bug1", nil, "updated", status(2, 3, "via", "github", "pr", 44)},
	}
	for _, a := range acts {
		raw, _ := json.Marshal(a.data)
		if _, err := tx.Exec(ctx, `INSERT INTO activity (project_id, issue_id, actor_id, kind, data, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			projectID, id[a.issue], a.actor, a.kind, raw, a.at); err != nil {
			return err
		}
	}

	prs := []struct {
		issue, state, title string
		n                   int
		at                  time.Time
	}{
		{"schema", "merged", "Users and sessions schema (LAUNCH-4)", 38, ago(19)},
		{"api", "open", "Sign-up and login endpoints (LAUNCH-5)", 42, ago(6)},
		{"bug1", "open", "Set Secure on session cookie behind proxy (LAUNCH-11)", 44, ago(3)},
	}
	for _, p := range prs {
		if _, err := tx.Exec(ctx, `INSERT INTO issue_prs (issue_id, repo, number, title, url, state, author, updated_at)
			VALUES ($1, 'acme/auth-service', $2, $3, $4, $5, 'samokafor', $6)`,
			id[p.issue], p.n, p.title, "https://github.com/acme/auth-service/pull/"+itoa(p.n), p.state, p.at); err != nil {
			return err
		}
	}

	comments := []struct {
		issue, author, body string
		at                  time.Time
	}{
		{"api", sam, "Login endpoint is up behind a flag. Rate limiting lands tomorrow.", ago(5)},
		{"api", lena, "OAuth is waiting on this — ping me when the session model is final.", ago(2)},
		{"audit", lena, "Bumped to 3d: security wants password reset covered too, so it now depends on LAUNCH-8.", ago(12)},
	}
	for _, c := range comments {
		if _, err := tx.Exec(ctx, `INSERT INTO comments (issue_id, author_id, body, created_at) VALUES ($1, $2, $3, $4)`, id[c.issue], c.author, c.body, c.at); err != nil {
			return err
		}
	}

	// Make the timestamps agree with the story, and leave two blockers visibly stale.
	for key, h := range map[string]float64{"spec": 100, "schema": 19, "ui": 18, "audit": 12, "bug2": 15, "api": 2, "bug1": 3, "oauth": 130, "docs": 110} {
		if _, err := tx.Exec(ctx, `UPDATE issues SET updated_at = $2 WHERE id = $1`, id[key], ago(h)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE issues SET created_at = $2 WHERE project_id = $1`, projectID, ago(170)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE issues SET created_at = $2 WHERE id = $1`, id["bug2"], ago(15)); err != nil {
		return err
	}

	// A week of daily forecast readings: the finish crept out, then slipped two days yesterday when the
	// security review was re-estimated. Today's reading comes from the first client to open the project.
	finish := addWorkdays(nextWorkday(now), 12) // the sample plan's critical path is 13 working days
	for day, slip := range map[int]int{7: 4, 6: 4, 5: 3, 4: 3, 3: 2, 2: 2, 1: 2} {
		f := addWorkdays(finish, -slip)
		taken := now.AddDate(0, 0, -day)
		if _, err := tx.Exec(ctx, `INSERT INTO forecast_snapshots (project_id, day, finish, finish_p85, remaining, open_issues, blocked, taken_at)
			VALUES ($1, $2::date, $3::date, $4::date, $5, $6, $7, $8)`,
			projectID, taken.Format("2006-01-02"), f.Format("2006-01-02"), addWorkdays(f, 3).Format("2006-01-02"),
			workdaysBetween(taken, f)+1, 16, 9, taken); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func isWeekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

func nextWorkday(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	for isWeekend(t) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// addWorkdays moves n working days from t (backwards when n is negative).
func addWorkdays(t time.Time, n int) time.Time {
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	for i := 0; i < n; {
		t = t.AddDate(0, 0, step)
		if !isWeekend(t) {
			i++
		}
	}
	return t
}

func workdaysBetween(a, b time.Time) int {
	n := 0
	for x := a; x.Before(b); x = x.AddDate(0, 0, 1) {
		if !isWeekend(x.AddDate(0, 0, 1)) {
			n++
		}
	}
	return n
}
