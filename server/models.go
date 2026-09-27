package main

import (
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type Member struct {
	User
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

type Project struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Status struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Position  int    `json:"position"`
}

type Issue struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Number      int       `json:"number"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	StatusID    string    `json:"statusId"`
	Priority    string    `json:"priority"`
	AssigneeID  *string   `json:"assigneeId"`
	ReporterID  *string   `json:"reporterId"`
	Estimate    float64   `json:"estimate"`
	DueDate     *string   `json:"dueDate"`
	Labels      []string  `json:"labels"`
	EpicID      *string   `json:"epicId"`
	Rank        float64   `json:"rank"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Link struct {
	BlockerID string `json:"blockerId"`
	BlockedID string `json:"blockedId"`
}

type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issueId"`
	AuthorID  *string   `json:"authorId"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type Activity struct {
	ID        int64           `json:"id"`
	IssueID   *string         `json:"issueId"`
	ActorID   *string         `json:"actorId"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
}

const issueCols = `i.id, i.project_id, i.number, i.type, i.title, i.description, i.status_id, i.priority,
	i.assignee_id, i.reporter_id, i.estimate, to_char(i.due_date, 'YYYY-MM-DD'), i.labels, i.epic_id,
	i.rank, i.version, i.created_at, i.updated_at`

func scanIssue(row pgx.Row) (Issue, error) {
	var i Issue
	err := row.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Type, &i.Title, &i.Description, &i.StatusID, &i.Priority,
		&i.AssigneeID, &i.ReporterID, &i.Estimate, &i.DueDate, &i.Labels, &i.EpicID,
		&i.Rank, &i.Version, &i.CreatedAt, &i.UpdatedAt)
	if i.Labels == nil {
		i.Labels = []string{}
	}
	return i, err
}

const activityCols = `id, issue_id, actor_id, kind, data, created_at`

func scanActivity(row pgx.Row) (Activity, error) {
	var a Activity
	err := row.Scan(&a.ID, &a.IssueID, &a.ActorID, &a.Kind, &a.Data, &a.CreatedAt)
	return a, err
}

// collect returns a function that scans every row with fn, always yielding a non-nil slice so it
// encodes as []. It's curried so a Query call can be passed straight in: collect(fn)(q.Query(...)).
func collect[T any](fn func(pgx.Row) (T, error)) func(pgx.Rows, error) ([]T, error) {
	return func(rows pgx.Rows, err error) ([]T, error) {
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []T{}
		for rows.Next() {
			v, err := fn(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
}
