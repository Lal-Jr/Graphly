package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type sampleIssue struct {
	key, typ, title, desc string
	status                int // index into defaultStatuses
	priority              string
	estimate              float64
	dueInDays             int // 0 = no due date
	labels                []string
	epic                  string
	blockedBy             []string
	who                   int // index into the assignee pool (wraps), so a solo project assigns everything to its creator
}

// A realistic launch plan with a deliberate squeeze: the critical path runs past the beta due date.
var sampleIssues = []sampleIssue{
	{key: "auth", typ: "epic", title: "Accounts & authentication", status: 2, priority: "high", estimate: 0, labels: []string{"q3"}},
	{key: "onboard", typ: "epic", title: "First-run onboarding", status: 1, priority: "medium", estimate: 0, labels: []string{"q3"}},

	{key: "spec", who: 0, typ: "task", title: "Write auth technical spec", status: 4, priority: "high", estimate: 2, epic: "auth", labels: []string{"docs"},
		desc: "Session model, password policy, OAuth scopes and the threat model."},
	{key: "schema", who: 1, typ: "task", title: "Design users & sessions schema", status: 4, priority: "high", estimate: 2, epic: "auth", labels: []string{"backend"}, blockedBy: []string{"spec"}},
	{key: "api", who: 1, typ: "story", title: "Build sign-up and login API", status: 2, priority: "highest", estimate: 4, epic: "auth", labels: []string{"backend"}, blockedBy: []string{"schema"},
		desc: "As a new user I can create an account and sign in with email and password.\n\n- bcrypt hashing\n- rate-limited login\n- httpOnly session cookie"},
	{key: "oauth", who: 2, typ: "story", title: "Sign in with Google and GitHub", status: 1, priority: "high", estimate: 3, epic: "auth", labels: []string{"backend"}, blockedBy: []string{"api"}},
	{key: "ui", who: 0, typ: "story", title: "Login and sign-up screens", status: 2, priority: "medium", estimate: 3, epic: "auth", labels: []string{"frontend"}, blockedBy: []string{"spec"}},
	{key: "reset", who: 1, typ: "story", title: "Password reset by email", status: 0, priority: "medium", estimate: 2, epic: "auth", labels: []string{"backend"}, blockedBy: []string{"api"}},
	{key: "e2e", who: 2, typ: "task", title: "End-to-end auth test suite", status: 0, priority: "medium", estimate: 2, epic: "auth", labels: []string{"qa"}, blockedBy: []string{"oauth", "ui"}},
	{key: "audit", who: 0, typ: "task", title: "Security review of auth flows", status: 0, priority: "high", estimate: 3, epic: "auth", labels: []string{"security"}, blockedBy: []string{"e2e", "reset"}},
	{key: "bug1", who: 1, typ: "bug", title: "Session cookie missing Secure flag behind proxy", status: 3, priority: "high", estimate: 0.5, epic: "auth", labels: []string{"security", "backend"}, blockedBy: []string{"api"}},

	{key: "tour", who: 2, typ: "story", title: "Guided product tour", status: 1, priority: "low", estimate: 3, epic: "onboard", labels: []string{"frontend"}, blockedBy: []string{"ui"}},
	{key: "emails", who: 0, typ: "story", title: "Welcome email sequence", status: 0, priority: "low", estimate: 2, epic: "onboard", labels: []string{"growth"}, blockedBy: []string{"api"}},
	{key: "metrics", who: 2, typ: "task", title: "Activation funnel dashboard", status: 1, priority: "medium", estimate: 2, epic: "onboard", labels: []string{"data"}},

	{key: "docs", who: 1, typ: "task", title: "Developer docs for auth SDK", status: 0, priority: "low", estimate: 1, labels: []string{"docs"}, blockedBy: []string{"api"}},
	{key: "beta", who: 0, typ: "task", title: "Launch private beta", status: 0, priority: "highest", estimate: 1, dueInDays: 12, labels: []string{"launch"}, blockedBy: []string{"audit", "docs", "tour"},
		desc: "Invite the first 200 waitlist users."},
	{key: "ci", who: 1, typ: "task", title: "Cut CI time below 5 minutes", status: 3, priority: "low", estimate: 1, labels: []string{"infra"}},
	{key: "bug2", who: 2, typ: "bug", title: "Board flickers when dragging between columns", status: 1, priority: "medium", estimate: 1, labels: []string{"frontend"}},
}

// seedSample fills a project with the sample plan. assignees must be workspace members; the first is also the reporter.
func seedSample(ctx context.Context, tx pgx.Tx, projectID string, statusIDs []string, assignees []string) (map[string]string, error) {
	reporter := assignees[0]
	ids := map[string]string{}
	today := time.Now().UTC()
	for i, s := range sampleIssues {
		var due *string
		if s.dueInDays > 0 {
			d := today.AddDate(0, 0, s.dueInDays).Format("2006-01-02")
			due = &d
		}
		var epic *string
		if s.epic != "" {
			e := ids[s.epic]
			epic = &e
		}
		assignee := &assignees[s.who%len(assignees)]
		if s.typ == "epic" {
			assignee = nil
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO issues (project_id, number, type, title, description, status_id, priority, assignee_id, reporter_id,
				estimate, due_date, labels, epic_id, rank)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14) RETURNING id`,
			projectID, i+1, s.typ, s.title, s.desc, statusIDs[s.status], s.priority, assignee, reporter,
			s.estimate, due, s.labels, epic, float64((i+1)*1024)).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids[s.key] = id
	}
	for _, s := range sampleIssues {
		for _, b := range s.blockedBy {
			if _, err := tx.Exec(ctx, `INSERT INTO issue_links (blocker_id, blocked_id) VALUES ($1, $2)`, ids[b], ids[s.key]); err != nil {
				return nil, err
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE projects SET issue_seq = $2 WHERE id = $1`, projectID, len(sampleIssues))
	return ids, err
}
