package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	issueTypes = map[string]bool{"story": true, "task": true, "bug": true, "epic": true}
	priorities = map[string]bool{"lowest": true, "low": true, "medium": true, "high": true, "highest": true}
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func loadIssue(ctx context.Context, q querier, id string, lock bool) (Issue, error) {
	sql := `SELECT ` + issueCols + ` FROM issues i WHERE i.id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanIssue(q.QueryRow(ctx, sql, id))
}

// logActivity records an entry in the issue/project history. actorID is a user id, or nil for automation (e.g. GitHub).
func logActivity(ctx context.Context, tx pgx.Tx, projectID string, issueID *string, actorID any, kind string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	a, err := scanActivity(tx.QueryRow(ctx, `INSERT INTO activity (project_id, issue_id, actor_id, kind, data) VALUES ($1, $2, $3, $4, $5)
		RETURNING `+activityCols, projectID, issueID, actorID, kind, raw))
	if err != nil {
		return err
	}
	return publish(ctx, tx, projectID, "activity.add", map[string]any{"activity": a})
}

// issueFields validates the editable fields present in a create/update body.
type issueFields struct {
	raw map[string]json.RawMessage
}

func (f issueFields) has(k string) bool { _, ok := f.raw[k]; return ok }

func (f issueFields) str(k string, dst *string) error {
	if !f.has(k) {
		return nil
	}
	return json.Unmarshal(f.raw[k], dst)
}

// apply validates each present field and writes it into is. It returns the names of fields that changed.
func (s *Server) applyFields(ctx context.Context, tx pgx.Tx, f issueFields, is *Issue, workspaceID string) ([]string, error) {
	var changed []string
	mark := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}
	decode := func(k string, dst any) error {
		if err := json.Unmarshal(f.raw[k], dst); err != nil {
			return badRequest("invalid " + k)
		}
		return nil
	}
	if f.has("title") {
		var v string
		if err := decode("title", &v); err != nil {
			return nil, err
		}
		v, err := cleanTitle(v, 255, "title")
		if err != nil {
			return nil, err
		}
		mark("title", v != is.Title)
		is.Title = v
	}
	if f.has("description") {
		var v string
		if err := decode("description", &v); err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(v) > 50000 {
			return nil, badRequest("description is too long")
		}
		mark("description", v != is.Description)
		is.Description = v
	}
	if f.has("type") {
		var v string
		if err := decode("type", &v); err != nil || !issueTypes[v] {
			return nil, badRequest("type must be story, task, bug or epic")
		}
		mark("type", v != is.Type)
		is.Type = v
	}
	if f.has("priority") {
		var v string
		if err := decode("priority", &v); err != nil || !priorities[v] {
			return nil, badRequest("invalid priority")
		}
		mark("priority", v != is.Priority)
		is.Priority = v
	}
	if f.has("statusId") {
		var v string
		if err := decode("statusId", &v); err != nil || !validID(v) {
			return nil, badRequest("invalid status")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM statuses WHERE id = $1 AND project_id = $2)`, v, is.ProjectID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, badRequest("unknown status")
		}
		mark("status", v != is.StatusID)
		is.StatusID = v
	}
	if f.has("assigneeId") {
		var v *string
		if err := decode("assigneeId", &v); err != nil {
			return nil, err
		}
		if v != nil {
			if !validID(*v) {
				return nil, badRequest("invalid assignee")
			}
			if _, err := s.role(ctx, tx, workspaceID, *v); err != nil {
				return nil, badRequest("assignee must be a member of this workspace")
			}
		}
		mark("assignee", !eqPtr(v, is.AssigneeID))
		is.AssigneeID = v
	}
	if f.has("estimate") {
		var v float64
		if err := decode("estimate", &v); err != nil || v < 0 || v > 1000 || math.IsNaN(v) {
			return nil, badRequest("estimate must be between 0 and 1000 days")
		}
		mark("estimate", v != is.Estimate)
		is.Estimate = v
	}
	if f.has("dueDate") {
		var v *string
		if err := decode("dueDate", &v); err != nil {
			return nil, err
		}
		if v != nil {
			if _, err := time.Parse("2006-01-02", *v); err != nil || !dateRe.MatchString(*v) {
				return nil, badRequest("due date must be YYYY-MM-DD")
			}
		}
		mark("dueDate", !eqPtr(v, is.DueDate))
		is.DueDate = v
	}
	if f.has("labels") {
		var v []string
		if err := decode("labels", &v); err != nil {
			return nil, err
		}
		clean := []string{}
		for _, l := range v {
			l = strings.ToLower(strings.Join(strings.Fields(l), "-"))
			if l != "" && len(l) <= 40 && !slices.Contains(clean, l) {
				clean = append(clean, l)
			}
		}
		if len(clean) > 20 {
			return nil, badRequest("at most 20 labels")
		}
		slices.Sort(clean)
		mark("labels", !slices.Equal(clean, is.Labels))
		is.Labels = clean
	}
	if f.has("epicId") {
		var v *string
		if err := decode("epicId", &v); err != nil {
			return nil, err
		}
		if v != nil {
			if !validID(*v) || *v == is.ID {
				return nil, badRequest("invalid epic")
			}
			var t string
			err := tx.QueryRow(ctx, `SELECT type FROM issues WHERE id = $1 AND project_id = $2`, *v, is.ProjectID).Scan(&t)
			if err != nil || t != "epic" {
				return nil, badRequest("parent must be an epic in this project")
			}
		}
		mark("epic", !eqPtr(v, is.EpicID))
		is.EpicID = v
	}
	if f.has("rank") {
		var v float64
		if err := decode("rank", &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, badRequest("invalid rank")
		}
		is.Rank = v // reordering isn't worth an activity entry
	}
	if is.Type == "epic" && is.EpicID != nil {
		return nil, badRequest("an epic can't belong to another epic")
	}
	return changed, nil
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		return err
	}
	f := issueFields{raw}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		wsID, _, err := s.projectAccess(ctx, tx, projectID, u.ID)
		if err != nil {
			return err
		}
		is := Issue{ProjectID: projectID, Type: "task", Priority: "medium", Estimate: 1, ReporterID: &u.ID, Labels: []string{}}
		// Defaults: first status, bottom of the backlog.
		if err := tx.QueryRow(ctx, `SELECT id FROM statuses WHERE project_id = $1 ORDER BY position LIMIT 1`, projectID).Scan(&is.StatusID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(rank), 0) + 1024 FROM issues WHERE project_id = $1`, projectID).Scan(&is.Rank); err != nil {
			return err
		}
		if !f.has("title") {
			return badRequest("title is required")
		}
		if _, err := s.applyFields(ctx, tx, f, &is, wsID); err != nil {
			return err
		}
		// Locking the project row serializes issue numbering.
		if err := tx.QueryRow(ctx, `UPDATE projects SET issue_seq = issue_seq + 1 WHERE id = $1 RETURNING issue_seq`, projectID).Scan(&is.Number); err != nil {
			return err
		}
		is, err = scanIssue(tx.QueryRow(ctx, `
			INSERT INTO issues AS i (project_id, number, type, title, description, status_id, priority, assignee_id, reporter_id,
				estimate, due_date, labels, epic_id, rank)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14) RETURNING `+issueCols,
			is.ProjectID, is.Number, is.Type, is.Title, is.Description, is.StatusID, is.Priority, is.AssigneeID, is.ReporterID,
			is.Estimate, is.DueDate, is.Labels, is.EpicID, is.Rank))
		if err != nil {
			return err
		}
		if err := publish(ctx, tx, projectID, "issue.upsert", map[string]any{"issue": is}); err != nil {
			return err
		}
		if err := logActivity(ctx, tx, projectID, &is.ID, u.ID, "created", map[string]any{}); err != nil {
			return err
		}
		return ok(w, is)
	})
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		return err
	}
	// `base` holds the values the client started editing from. If someone else has changed one
	// of those fields since, the write is refused rather than silently clobbering their edit.
	var base map[string]string
	if b, ok := raw["base"]; ok {
		if err := json.Unmarshal(b, &base); err != nil {
			return badRequest("invalid base")
		}
		delete(raw, "base")
	}
	f := issueFields{raw}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.issueProject(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		wsID, _, err := s.projectAccess(ctx, tx, projectID, u.ID)
		if err != nil {
			return err
		}
		is, err := loadIssue(ctx, tx, id, true)
		if err != nil {
			return err
		}
		before := is
		for field, want := range base {
			var cur string
			switch field {
			case "title":
				cur = is.Title
			case "description":
				cur = is.Description
			default:
				continue
			}
			if cur != want {
				return &apiError{Status: http.StatusConflict, Msg: "someone else changed the " + field + " while you were editing", Data: map[string]any{"issue": is}}
			}
		}
		changed, err := s.applyFields(ctx, tx, f, &is, wsID)
		if err != nil {
			return err
		}
		if len(changed) == 0 && is.Rank == before.Rank {
			return ok(w, is)
		}
		is, err = scanIssue(tx.QueryRow(ctx, `
			UPDATE issues AS i SET type = $2, title = $3, description = $4, status_id = $5, priority = $6, assignee_id = $7,
				estimate = $8, due_date = $9::date, labels = $10, epic_id = $11, rank = $12, version = version + 1, updated_at = now()
			WHERE id = $1 RETURNING `+issueCols,
			id, is.Type, is.Title, is.Description, is.StatusID, is.Priority, is.AssigneeID, is.Estimate, is.DueDate, is.Labels, is.EpicID, is.Rank))
		if err != nil {
			return err
		}
		if err := publish(ctx, tx, projectID, "issue.upsert", map[string]any{"issue": is}); err != nil {
			return err
		}
		if before.Type == "epic" && is.Type != "epic" {
			if err := detachChildren(ctx, tx, projectID, id); err != nil {
				return err
			}
		}
		for _, field := range changed {
			data := map[string]any{"field": field}
			switch field {
			case "status":
				data["from"], data["to"] = before.StatusID, is.StatusID
			case "assignee":
				data["from"], data["to"] = before.AssigneeID, is.AssigneeID
			case "priority":
				data["from"], data["to"] = before.Priority, is.Priority
			case "estimate":
				data["from"], data["to"] = before.Estimate, is.Estimate
			case "dueDate":
				data["from"], data["to"] = before.DueDate, is.DueDate
			case "type":
				data["from"], data["to"] = before.Type, is.Type
			case "title":
				data["from"], data["to"] = before.Title, is.Title
			}
			if err := logActivity(ctx, tx, projectID, &is.ID, u.ID, "updated", data); err != nil {
				return err
			}
		}
		if slices.Contains(changed, "status") {
			if err := s.noteUnblocked(ctx, tx, projectID, before, is, u.ID); err != nil {
				return err
			}
		}
		return ok(w, is)
	})
}

// noteUnblocked records an activity entry on each issue this one was the last open blocker of,
// so the people waiting on it can see exactly when (and by what) they were freed up.
func (s *Server) noteUnblocked(ctx context.Context, tx pgx.Tx, projectID string, before, after Issue, actorID any) error {
	var wasDone, isDone bool
	if err := tx.QueryRow(ctx, `SELECT (SELECT category = 'done' FROM statuses WHERE id = $1), (SELECT category = 'done' FROM statuses WHERE id = $2)`,
		before.StatusID, after.StatusID).Scan(&wasDone, &isDone); err != nil {
		return err
	}
	if wasDone || !isDone {
		return nil
	}
	freed, err := collect(scanString)(tx.Query(ctx, `
		SELECT l.blocked_id FROM issue_links l
		JOIN issues d ON d.id = l.blocked_id JOIN statuses ds ON ds.id = d.status_id
		WHERE l.blocker_id = $1 AND ds.category <> 'done'
		AND NOT EXISTS (
			SELECT 1 FROM issue_links o JOIN issues b ON b.id = o.blocker_id JOIN statuses bs ON bs.id = b.status_id
			WHERE o.blocked_id = l.blocked_id AND bs.category <> 'done')`, after.ID))
	if err != nil {
		return err
	}
	for _, d := range freed {
		if err := logActivity(ctx, tx, projectID, &d, actorID, "unblocked", map[string]any{"by": after.ID}); err != nil {
			return err
		}
	}
	return nil
}

// detachChildren clears the epic of every issue under epicID and tells clients.
func detachChildren(ctx context.Context, tx pgx.Tx, projectID, epicID string) error {
	children, err := collect(scanIssue)(tx.Query(ctx, `UPDATE issues AS i SET epic_id = NULL, version = version + 1, updated_at = now()
		WHERE epic_id = $1 RETURNING `+issueCols, epicID))
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := publish(ctx, tx, projectID, "issue.upsert", map[string]any{"issue": c}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.issueProject(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		is, err := loadIssue(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := detachChildren(ctx, tx, projectID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM issues WHERE id = $1`, id); err != nil {
			return err
		}
		if err := publish(ctx, tx, projectID, "issue.delete", map[string]any{"id": id}); err != nil {
			return err
		}
		if err := logActivity(ctx, tx, projectID, nil, u.ID, "deleted", map[string]any{"number": is.Number, "title": is.Title}); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) addBlocker(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		BlockerID string `json:"blockerId"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if !validID(body.BlockerID) {
		return badRequest("invalid blocker")
	}
	if body.BlockerID == id {
		return badRequest("an issue can't block itself")
	}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.issueProject(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		// Serialize link changes per project so two concurrent edits can't together form a cycle.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
			return err
		}
		var sameProject bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issues WHERE id = $1 AND project_id = $2)`, body.BlockerID, projectID).Scan(&sameProject); err != nil {
			return err
		}
		if !sameProject {
			return badRequest("blocker must be in the same project")
		}
		// Adding blocker → id closes a loop if the blocker already (transitively) waits on id.
		var cycle bool
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE upstream(id) AS (
				SELECT $1::uuid
				UNION
				SELECT l.blocker_id FROM issue_links l JOIN upstream u ON l.blocked_id = u.id
			)
			SELECT EXISTS (SELECT 1 FROM upstream WHERE id = $2)`, body.BlockerID, id).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return errStatus(http.StatusConflict, "that would create a dependency cycle")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO issue_links (blocker_id, blocked_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, body.BlockerID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err := publish(ctx, tx, projectID, "link.add", map[string]any{"link": Link{body.BlockerID, id}}); err != nil {
				return err
			}
			if err := logActivity(ctx, tx, projectID, &id, u.ID, "blocker.add", map[string]any{"blocker": body.BlockerID}); err != nil {
				return err
			}
		}
		return ok(w, Link{body.BlockerID, id})
	})
}

func (s *Server) removeBlocker(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	blocker, err := pathID(r, "blocker")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.issueProject(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM issue_links WHERE blocker_id = $1 AND blocked_id = $2`, blocker, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err := publish(ctx, tx, projectID, "link.remove", map[string]any{"link": Link{blocker, id}}); err != nil {
				return err
			}
			if err := logActivity(ctx, tx, projectID, &id, u.ID, "blocker.remove", map[string]any{"blocker": blocker}); err != nil {
				return err
			}
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func scanComment(row pgx.Row) (Comment, error) {
	var c Comment
	return c, row.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Body, &c.CreatedAt)
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueProject(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	cs, err := collect(scanComment)(s.db.Query(r.Context(), `SELECT id, issue_id, author_id, body, created_at FROM comments WHERE issue_id = $1 ORDER BY created_at`, id))
	if err != nil {
		return err
	}
	return ok(w, cs)
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct{ Body string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	text := strings.TrimSpace(body.Body)
	if text == "" || utf8.RuneCountInString(text) > 20000 {
		return badRequest("comment must be 1–20000 characters")
	}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.issueProject(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		c, err := scanComment(tx.QueryRow(ctx, `INSERT INTO comments (issue_id, author_id, body) VALUES ($1, $2, $3)
			RETURNING id, issue_id, author_id, body, created_at`, id, u.ID, text))
		if err != nil {
			return err
		}
		if err := publish(ctx, tx, projectID, "comment.add", map[string]any{"comment": c}); err != nil {
			return err
		}
		return ok(w, c)
	})
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var issueID string
		var author *string
		if err := tx.QueryRow(ctx, `SELECT issue_id, author_id FROM comments WHERE id = $1`, id).Scan(&issueID, &author); err != nil {
			return err
		}
		projectID, err := s.issueProject(ctx, tx, issueID, u.ID)
		if err != nil {
			return err
		}
		if author == nil || *author != u.ID {
			return errStatus(http.StatusForbidden, "you can only delete your own comments")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id); err != nil {
			return err
		}
		if err := publish(ctx, tx, projectID, "comment.delete", map[string]any{"id": id, "issueId": issueID}); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) issueActivity(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueProject(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	acts, err := collect(scanActivity)(s.db.Query(r.Context(), `SELECT `+activityCols+` FROM activity WHERE issue_id = $1 ORDER BY id DESC LIMIT 200`, id))
	if err != nil {
		return err
	}
	return ok(w, acts)
}
