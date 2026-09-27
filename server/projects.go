package main

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

var defaultStatuses = []struct{ name, category string }{
	{"Backlog", "todo"},
	{"To Do", "todo"},
	{"In Progress", "in_progress"},
	{"In Review", "in_progress"},
	{"Done", "done"},
}

// createProjectTx creates a project with the default workflow, optionally seeded with the sample plan.
func createProjectTx(ctx context.Context, tx pgx.Tx, wsID, key, name, description string, sample bool, assignees []string) (Project, error) {
	p, _, err := createProjectWithIDs(ctx, tx, wsID, key, name, description, sample, assignees)
	return p, err
}

// createProjectWithIDs is createProjectTx that also returns the sample issue ids and status ids.
func createProjectWithIDs(ctx context.Context, tx pgx.Tx, wsID, key, name, description string, sample bool, assignees []string) (Project, *seeded, error) {
	var p Project
	err := tx.QueryRow(ctx, `INSERT INTO projects (workspace_id, key, name, description) VALUES ($1, $2, $3, $4)
		RETURNING id, workspace_id, key, name, description, created_at`, wsID, key, name, description).
		Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Name, &p.Description, &p.CreatedAt)
	if isUnique(err) {
		return p, nil, errStatus(http.StatusConflict, "a project with key "+key+" already exists")
	}
	if err != nil {
		return p, nil, err
	}
	statusIDs := make([]string, len(defaultStatuses))
	for i, st := range defaultStatuses {
		if err := tx.QueryRow(ctx, `INSERT INTO statuses (project_id, name, category, position) VALUES ($1, $2, $3, $4) RETURNING id`,
			p.ID, st.name, st.category, i).Scan(&statusIDs[i]); err != nil {
			return p, nil, err
		}
	}
	if !sample {
		return p, nil, nil
	}
	ids, err := seedSample(ctx, tx, p.ID, statusIDs, assignees)
	return p, &seeded{issues: ids, statuses: statusIDs}, err
}

type seeded struct {
	issues   map[string]string // sample key → issue id
	statuses []string          // in defaultStatuses order
}

type projectSummary struct {
	Project
	OpenIssues int `json:"openIssues"`
	DoneIssues int `json:"doneIssues"`
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if _, err := s.role(r.Context(), s.db, wsID, u.ID); err != nil {
		return err
	}
	ps, err := collect(func(row pgx.Row) (projectSummary, error) {
		var p projectSummary
		return p, row.Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.OpenIssues, &p.DoneIssues)
	})(s.db.Query(r.Context(), `
		SELECT p.id, p.workspace_id, p.key, p.name, p.description, p.created_at,
			count(i.id) FILTER (WHERE st.category <> 'done'), count(i.id) FILTER (WHERE st.category = 'done')
		FROM projects p
		LEFT JOIN issues i ON i.project_id = p.id
		LEFT JOIN statuses st ON st.id = i.status_id
		WHERE p.workspace_id = $1 GROUP BY p.id ORDER BY p.name`, wsID))
	if err != nil {
		return err
	}
	return ok(w, ps)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if _, err := s.role(r.Context(), s.db, wsID, u.ID); err != nil {
		return err
	}
	var body struct {
		Key, Name, Description string
		Sample                 bool
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	key := strings.ToUpper(strings.TrimSpace(body.Key))
	if !projectKeyRe.MatchString(key) {
		return badRequest("key must be 2–10 letters or digits, starting with a letter")
	}
	name, err := cleanTitle(body.Name, 80, "project name")
	if err != nil {
		return err
	}
	var p Project
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		assignees := []string{u.ID}
		p, err = createProjectTx(r.Context(), tx, wsID, key, name, body.Description, body.Sample, assignees)
		return err
	})
	if err != nil {
		return err
	}
	return ok(w, p)
}

type projectSnapshot struct {
	Project  Project  `json:"project"`
	Statuses []Status `json:"statuses"`
	Issues   []Issue  `json:"issues"`
	Links    []Link   `json:"links"`
	Members  []Member `json:"members"`
	// Realtime cursor: subscribe to /live?since=<this> to receive everything after the snapshot.
	Seq int64 `json:"seq"`
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	// Repeatable read gives a consistent snapshot across the queries below.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	wsID, _, err := s.projectAccess(ctx, tx, id, u.ID)
	if err != nil {
		return err
	}
	var snap projectSnapshot
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM events`).Scan(&snap.Seq); err != nil {
		return err
	}
	p := &snap.Project
	if err := tx.QueryRow(ctx, `SELECT id, workspace_id, key, name, description, created_at FROM projects WHERE id = $1`, id).
		Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Name, &p.Description, &p.CreatedAt); err != nil {
		return err
	}
	if snap.Statuses, err = s.statuses(ctx, tx, id); err != nil {
		return err
	}
	if snap.Issues, err = collect(scanIssue)(tx.Query(ctx, `SELECT `+issueCols+` FROM issues i WHERE i.project_id = $1 ORDER BY i.rank`, id)); err != nil {
		return err
	}
	if snap.Links, err = collect(func(row pgx.Row) (Link, error) {
		var l Link
		return l, row.Scan(&l.BlockerID, &l.BlockedID)
	})(tx.Query(ctx, `
		SELECT l.blocker_id, l.blocked_id FROM issue_links l JOIN issues i ON i.id = l.blocked_id WHERE i.project_id = $1`, id)); err != nil {
		return err
	}
	if snap.Members, err = s.members(ctx, tx, wsID); err != nil {
		return err
	}
	return ok(w, snap)
}

func (s *Server) statuses(ctx context.Context, q querier, projectID string) ([]Status, error) {
	return collect(func(row pgx.Row) (Status, error) {
		var st Status
		return st, row.Scan(&st.ID, &st.ProjectID, &st.Name, &st.Category, &st.Position)
	})(q.Query(ctx, `SELECT id, project_id, name, category, position FROM statuses WHERE project_id = $1 ORDER BY position`, projectID))
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct{ Name, Description *string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, _, err := s.projectAccess(r.Context(), tx, id, u.ID); err != nil {
			return err
		}
		var p Project
		if err := tx.QueryRow(r.Context(), `SELECT id, workspace_id, key, name, description, created_at FROM projects WHERE id = $1 FOR UPDATE`, id).
			Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Name, &p.Description, &p.CreatedAt); err != nil {
			return err
		}
		if body.Name != nil {
			if p.Name, err = cleanTitle(*body.Name, 80, "project name"); err != nil {
				return err
			}
		}
		if body.Description != nil {
			p.Description = *body.Description
		}
		if _, err := tx.Exec(r.Context(), `UPDATE projects SET name = $2, description = $3 WHERE id = $1`, id, p.Name, p.Description); err != nil {
			return err
		}
		if err := publish(r.Context(), tx, id, "project.update", map[string]any{"project": p}); err != nil {
			return err
		}
		return ok(w, p)
	})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wsID, role, err := s.projectAccess(r.Context(), s.db, id, u.ID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errStatus(http.StatusForbidden, "only workspace admins can delete projects")
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM projects WHERE id = $1 AND workspace_id = $2`, id, wsID); err != nil {
			return err
		}
		if err := publish(r.Context(), tx, id, "project.delete", map[string]any{"id": id}); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

var categories = map[string]bool{"todo": true, "in_progress": true, "done": true}

func (s *Server) createStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct{ Name, Category string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	name, err := cleanTitle(body.Name, 40, "status name")
	if err != nil {
		return err
	}
	if !categories[body.Category] {
		return badRequest("category must be todo, in_progress or done")
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, _, err := s.projectAccess(r.Context(), tx, projectID, u.ID); err != nil {
			return err
		}
		st := Status{ProjectID: projectID, Name: name, Category: body.Category}
		// Slot it in after the last column of the same category, keeping the workflow in category order.
		if err := tx.QueryRow(r.Context(), `
			SELECT coalesce(max(position) FILTER (WHERE category = $2), max(position), -1) + 1 FROM statuses WHERE project_id = $1`,
			projectID, body.Category).Scan(&st.Position); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE statuses SET position = position + 1 WHERE project_id = $1 AND position >= $2`, projectID, st.Position); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO statuses (project_id, name, category, position) VALUES ($1, $2, $3, $4) RETURNING id`,
			projectID, st.Name, st.Category, st.Position).Scan(&st.ID); err != nil {
			return err
		}
		if err := s.publishStatuses(r.Context(), tx, projectID); err != nil {
			return err
		}
		return ok(w, st)
	})
}

func (s *Server) publishStatuses(ctx context.Context, tx pgx.Tx, projectID string) error {
	sts, err := s.statuses(ctx, tx, projectID)
	if err != nil {
		return err
	}
	return publish(ctx, tx, projectID, "statuses.update", map[string]any{"statuses": sts})
}

func (s *Server) statusProject(ctx context.Context, q querier, statusID, userID string) (string, error) {
	var projectID string
	if err := q.QueryRow(ctx, `SELECT project_id FROM statuses WHERE id = $1`, statusID).Scan(&projectID); err != nil {
		return "", err
	}
	_, _, err := s.projectAccess(ctx, q, projectID, userID)
	return projectID, err
}

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		Name     *string
		Category *string
		Position *int
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		projectID, err := s.statusProject(r.Context(), tx, id, u.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
			return err
		}
		if body.Name != nil {
			name, err := cleanTitle(*body.Name, 40, "status name")
			if err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE statuses SET name = $2 WHERE id = $1`, id, name); err != nil {
				return err
			}
		}
		if body.Category != nil {
			if !categories[*body.Category] {
				return badRequest("category must be todo, in_progress or done")
			}
			if _, err := tx.Exec(r.Context(), `UPDATE statuses SET category = $2 WHERE id = $1`, id, *body.Category); err != nil {
				return err
			}
		}
		if body.Position != nil {
			// Re-number the whole workflow with this status moved to the requested index.
			ids, err := collect(scanString)(tx.Query(r.Context(), `SELECT id FROM statuses WHERE project_id = $1 AND id <> $2 ORDER BY position`, projectID, id))
			if err != nil {
				return err
			}
			at := min(max(*body.Position, 0), len(ids))
			ids = append(ids[:at], append([]string{id}, ids[at:]...)...)
			for i, sid := range ids {
				if _, err := tx.Exec(r.Context(), `UPDATE statuses SET position = $2 WHERE id = $1`, sid, i); err != nil {
					return err
				}
			}
		}
		if err := s.publishStatuses(r.Context(), tx, projectID); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) deleteStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		MoveTo string `json:"moveTo"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &body); err != nil {
			return err
		}
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		projectID, err := s.statusProject(r.Context(), tx, id, u.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT 1 FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
			return err
		}
		var total, inUse int
		if err := tx.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM statuses WHERE project_id = $1), (SELECT count(*) FROM issues WHERE status_id = $2)`,
			projectID, id).Scan(&total, &inUse); err != nil {
			return err
		}
		if total <= 1 {
			return badRequest("a project needs at least one status")
		}
		if inUse > 0 {
			if body.MoveTo == "" || body.MoveTo == id || !validID(body.MoveTo) {
				return badRequest("choose a status to move this column's issues to")
			}
			moved, err := collect(scanIssue)(tx.Query(r.Context(), `UPDATE issues i SET status_id = $2, version = version + 1, updated_at = now()
				FROM statuses st WHERE st.id = $2 AND st.project_id = $3 AND i.status_id = $1 RETURNING `+issueCols, id, body.MoveTo, projectID))
			if err != nil {
				return err
			}
			if len(moved) != inUse {
				return badRequest("unknown target status")
			}
			for _, is := range moved {
				if err := publish(r.Context(), tx, projectID, "issue.upsert", map[string]any{"issue": is}); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM statuses WHERE id = $1`, id); err != nil {
			return err
		}
		if err := s.publishStatuses(r.Context(), tx, projectID); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) projectActivity(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, _, err := s.projectAccess(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	// ?since=<RFC3339> returns everything after that moment (capped), for the standup digest.
	since := time.Now().Add(-30 * 24 * time.Hour)
	limit := 100
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return badRequest("since must be an RFC 3339 timestamp")
		}
		since, limit = t, 1000
	}
	acts, err := collect(scanActivity)(s.db.Query(r.Context(), `SELECT `+activityCols+` FROM activity
		WHERE project_id = $1 AND created_at > $2 ORDER BY id DESC LIMIT $3`, id, since, limit))
	if err != nil {
		return err
	}
	return ok(w, acts)
}

// myWork lists open issues assigned to the user across the workspace, with each issue's project key.
func (s *Server) myWork(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if _, err := s.role(r.Context(), s.db, wsID, u.ID); err != nil {
		return err
	}
	type item struct {
		Issue
		ProjectKey   string `json:"projectKey"`
		StatusName   string `json:"statusName"`
		Category     string `json:"category"`
		OpenBlockers int    `json:"openBlockers"`
		// Open issues that can't start until this one is done — people waiting on you.
		Waiting int `json:"waiting"`
	}
	items, err := collect(func(row pgx.Row) (item, error) {
		var it item
		err := row.Scan(&it.ID, &it.ProjectID, &it.Number, &it.Type, &it.Title, &it.Description, &it.StatusID, &it.Priority,
			&it.AssigneeID, &it.ReporterID, &it.Estimate, &it.DueDate, &it.Labels, &it.EpicID,
			&it.Rank, &it.Version, &it.CreatedAt, &it.UpdatedAt, &it.ProjectKey, &it.StatusName, &it.Category, &it.OpenBlockers, &it.Waiting)
		return it, err
	})(s.db.Query(r.Context(), `
		SELECT `+issueCols+`, p.key, st.name, st.category,
			(SELECT count(*) FROM issue_links l JOIN issues b ON b.id = l.blocker_id JOIN statuses bs ON bs.id = b.status_id
			 WHERE l.blocked_id = i.id AND bs.category <> 'done'),
			(SELECT count(*) FROM issue_links l JOIN issues d ON d.id = l.blocked_id JOIN statuses ds ON ds.id = d.status_id
			 WHERE l.blocker_id = i.id AND ds.category <> 'done')
		FROM issues i JOIN projects p ON p.id = i.project_id JOIN statuses st ON st.id = i.status_id
		WHERE p.workspace_id = $1 AND i.assignee_id = $2 AND st.category <> 'done'
		ORDER BY i.due_date NULLS LAST, i.updated_at DESC LIMIT 200`, wsID, u.ID))
	if err != nil {
		return err
	}
	return ok(w, items)
}
