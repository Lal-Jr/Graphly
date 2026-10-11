package main

import (
	_ "embed"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read endpoints that round out the public API (openapi.yaml). The app itself loads whole projects
// through GET /api/projects/{id}; scripts usually want a filtered page of issues instead.

//go:embed openapi.yaml
var openAPIYAML []byte

func (s *Server) openAPISpec(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, err := w.Write(openAPIYAML)
	return err
}

type issuePage struct {
	Issues []Issue `json:"issues"`
	// Pass as ?after= to get the next page; null on the last page.
	Next *int `json:"next"`
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.projectRole(r.Context(), s.db, projectID, u, roleViewer); err != nil {
		return err
	}
	q := r.URL.Query()
	where := []string{"i.project_id = $1"}
	args := []any{projectID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if v := q.Get("category"); v != "" {
		if v != "todo" && v != "in_progress" && v != "done" {
			return badRequest("category must be todo, in_progress or done")
		}
		where = append(where, "st.category = "+arg(v))
	}
	if v := q.Get("type"); v != "" {
		if !issueTypes[v] {
			return badRequest("type must be story, task, bug or epic")
		}
		where = append(where, "i.type = "+arg(v))
	}
	switch v := q.Get("assignee"); {
	case v == "":
	case v == "none":
		where = append(where, "i.assignee_id IS NULL")
	case v == "me":
		where = append(where, "i.assignee_id = "+arg(u.ID))
	case validID(v):
		where = append(where, "i.assignee_id = "+arg(v))
	default:
		return badRequest("assignee must be a user id, me or none")
	}
	if v := q.Get("label"); v != "" {
		where = append(where, arg(strings.ToLower(v))+" = ANY(i.labels)")
	}
	if v := q.Get("updatedSince"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return badRequest("updatedSince must be an RFC 3339 timestamp")
		}
		where = append(where, "i.updated_at >= "+arg(t))
	}
	if v := q.Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return badRequest("after must be an issue number")
		}
		where = append(where, "i.number > "+arg(n))
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 500 {
			return badRequest("limit must be 1–500")
		}
	}
	issues, err := collect(scanIssue)(s.db.Query(r.Context(), `SELECT `+issueCols+` FROM issues i JOIN statuses st ON st.id = i.status_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY i.number LIMIT `+arg(limit+1), args...))
	if err != nil {
		return err
	}
	page := issuePage{Issues: issues}
	if len(issues) > limit {
		page.Issues = issues[:limit]
		n := issues[limit-1].Number
		page.Next = &n
	}
	if page.Issues == nil {
		page.Issues = []Issue{}
	}
	return ok(w, page)
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	is, err := loadIssue(r.Context(), s.db, id, false)
	if err != nil {
		return err
	}
	return ok(w, is)
}

// getIssueByNumber looks an issue up by its number in the project (42 for ENG-42).
func (s *Server) getIssueByNumber(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		return errNotFound
	}
	if _, err := s.projectRole(r.Context(), s.db, projectID, u, roleViewer); err != nil {
		return err
	}
	is, err := scanIssue(s.db.QueryRow(r.Context(), `SELECT `+issueCols+` FROM issues i WHERE i.project_id = $1 AND i.number = $2`, projectID, n))
	if err != nil {
		return err
	}
	return ok(w, is)
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.projectRole(r.Context(), s.db, projectID, u, roleViewer); err != nil {
		return err
	}
	links, err := collect(func(row pgx.Row) (Link, error) {
		var l Link
		return l, row.Scan(&l.BlockerID, &l.BlockedID)
	})(s.db.Query(r.Context(), `SELECT l.blocker_id, l.blocked_id FROM issue_links l JOIN issues i ON i.id = l.blocked_id
		WHERE i.project_id = $1 ORDER BY l.created_at, l.blocker_id`, projectID))
	if err != nil {
		return err
	}
	if links == nil {
		links = []Link{}
	}
	return ok(w, links)
}
