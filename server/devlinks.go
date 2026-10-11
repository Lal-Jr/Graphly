package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Shared by the GitHub and GitLab webhooks: linking commits to issues and moving issues forward.
//
// The forward-only rule: an integration only ever moves an issue to a later column (by position)
// and never moves anything out of done. A merged PR/MR moves it to done; an opened one to review;
// a draft, a new branch or a pushed commit that names the issue to the first in-progress column.

const (
	targetDone    = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'done' ORDER BY position LIMIT 1`
	targetReview  = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'in_progress' ORDER BY (name ILIKE '%review%') DESC, position DESC LIMIT 1`
	targetStarted = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'in_progress' ORDER BY position LIMIT 1`
)

type Commit struct {
	Provider    string     `json:"provider"`
	Repo        string     `json:"repo"`
	SHA         string     `json:"sha"`
	Message     string     `json:"message"`
	URL         string     `json:"url"`
	Author      string     `json:"author"`
	Branch      string     `json:"branch"`
	CommittedAt *time.Time `json:"committedAt"`
}

func providerLabel(p string) string {
	if p == "gitlab" {
		return "GitLab"
	}
	return "GitHub"
}

// advance moves an issue to the column `target` selects, if that's further along. It reports
// whether the issue moved.
func (s *Server) advance(ctx context.Context, tx pgx.Tx, projectID, issueID, target, provider string, extra map[string]any) (bool, error) {
	var targetID string
	var targetPos int
	if err := tx.QueryRow(ctx, target, projectID).Scan(&targetID, &targetPos); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	before, err := loadIssue(ctx, tx, issueID, false)
	if err != nil {
		return false, err
	}
	var curPos int
	var curCat string
	if err := tx.QueryRow(ctx, `SELECT position, category FROM statuses WHERE id = $1`, before.StatusID).Scan(&curPos, &curCat); err != nil {
		return false, err
	}
	if curCat == "done" || curPos >= targetPos {
		return false, nil
	}
	after, err := scanIssue(tx.QueryRow(ctx, `UPDATE issues AS i SET status_id = $2, version = version + 1, updated_at = now()
		WHERE id = $1 RETURNING `+issueCols, issueID, targetID))
	if err != nil {
		return false, err
	}
	if err := publish(ctx, tx, projectID, "issue.upsert", map[string]any{"issue": after}); err != nil {
		return false, err
	}
	data := map[string]any{"field": "status", "from": before.StatusID, "to": after.StatusID, "via": provider}
	for k, v := range extra {
		data[k] = v
	}
	if err := logActivity(ctx, tx, projectID, &issueID, nil, "updated", data); err != nil {
		return false, err
	}
	if err := s.syncLifecycle(ctx, tx, []string{issueID}); err != nil {
		return false, err
	}
	if err := s.noteStatusChange(ctx, tx, projectID, before, after, nil, providerLabel(provider)); err != nil {
		return false, err
	}
	return true, s.noteUnblocked(ctx, tx, projectID, before, after, nil)
}

type pushedCommit struct {
	ID        string
	Message   string
	URL       string
	Author    string
	Timestamp string
}

type push struct {
	Provider, Repo, Branch string
	Created                bool // a new branch
	Commits                []pushedCommit
}

// branchOf strips refs/heads/; tags and other refs return "".
func branchOf(ref string) string {
	b, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		return ""
	}
	return b
}

// applyPush links each commit to the issues its message or branch names, and moves those issues
// to in progress. A new branch that names an issue counts too, before any commit lands.
func (s *Server) applyPush(ctx context.Context, projectID, key string, p push) (map[string]any, error) {
	if p.Branch == "" {
		return map[string]any{"ignored": "not a branch"}, nil
	}
	branchIssues := issueNumbers(key, p.Branch)
	linked, moved := 0, 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		started := map[int]bool{}
		for i, c := range p.Commits {
			if i >= 100 {
				break
			}
			subject, _, _ := strings.Cut(c.Message, "\n")
			numbers := issueNumbers(key, c.Message)
			for _, n := range branchIssues {
				numbers = appendUnique(numbers, n)
			}
			var at *time.Time
			if t, err := time.Parse(time.RFC3339, c.Timestamp); err == nil {
				at = &t
			}
			for _, n := range numbers {
				var issueID string
				if err := tx.QueryRow(ctx, `SELECT id FROM issues WHERE project_id = $1 AND number = $2`, projectID, n).Scan(&issueID); err == pgx.ErrNoRows {
					continue
				} else if err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `INSERT INTO issue_commits (issue_id, provider, repo, sha, message, url, author, branch, committed_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING`,
					issueID, p.Provider, p.Repo, c.ID, clip(subject, 300), c.URL, clip(c.Author, 100), clip(p.Branch, 255), at)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue // already linked (a re-delivered webhook, or the same commit on another branch)
				}
				linked++
				if err := publish(ctx, tx, projectID, "commit.link", map[string]any{"issueId": issueID}); err != nil {
					return err
				}
				if err := logActivity(ctx, tx, projectID, &issueID, nil, "commit", map[string]any{
					"provider": p.Provider, "repo": p.Repo, "sha": c.ID, "message": clip(subject, 300), "url": c.URL, "author": c.Author, "branch": p.Branch,
				}); err != nil {
					return err
				}
				started[n] = true
			}
		}
		if p.Created {
			for _, n := range branchIssues {
				started[n] = true
			}
		}
		for n := range started {
			var issueID string
			if err := tx.QueryRow(ctx, `SELECT id FROM issues WHERE project_id = $1 AND number = $2 FOR UPDATE`, projectID, n).Scan(&issueID); err == pgx.ErrNoRows {
				continue
			} else if err != nil {
				return err
			}
			did, err := s.advance(ctx, tx, projectID, issueID, targetStarted, p.Provider, map[string]any{"branch": p.Branch})
			if err != nil {
				return err
			}
			if did {
				moved++
			}
		}
		return nil
	})
	return map[string]any{"linked": linked, "moved": moved}, err
}

func appendUnique(xs []int, x int) []int {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// githubPush handles GitHub's push event (branch pushes and new branches).
func (s *Server) githubPush(w http.ResponseWriter, r *http.Request, projectID, key string, body []byte) error {
	var ev struct {
		Ref        string `json:"ref"`
		Created    bool   `json:"created"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Commits []struct {
			ID        string `json:"id"`
			Message   string `json:"message"`
			URL       string `json:"url"`
			Timestamp string `json:"timestamp"`
			Author    struct {
				Name     string `json:"name"`
				Username string `json:"username"`
			} `json:"author"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return badRequest("invalid payload")
	}
	if ev.Deleted {
		return ok(w, map[string]string{"ignored": "branch deleted"})
	}
	p := push{Provider: "github", Repo: ev.Repository.FullName, Branch: branchOf(ev.Ref), Created: ev.Created}
	for _, c := range ev.Commits {
		if !strings.HasPrefix(c.URL, "https://") {
			continue
		}
		p.Commits = append(p.Commits, pushedCommit{ID: c.ID, Message: c.Message, URL: c.URL, Author: firstNonEmpty(c.Author.Username, c.Author.Name), Timestamp: c.Timestamp})
	}
	res, err := s.applyPush(r.Context(), projectID, key, p)
	if err != nil {
		return err
	}
	return ok(w, res)
}

func (s *Server) listCommits(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	commits, err := collect(func(row pgx.Row) (Commit, error) {
		var c Commit
		return c, row.Scan(&c.Provider, &c.Repo, &c.SHA, &c.Message, &c.URL, &c.Author, &c.Branch, &c.CommittedAt)
	})(s.db.Query(r.Context(), `SELECT provider, repo, sha, message, url, author, branch, committed_at FROM issue_commits
		WHERE issue_id = $1 ORDER BY coalesce(committed_at, linked_at) DESC LIMIT 50`, id))
	if err != nil {
		return err
	}
	if commits == nil {
		commits = []Commit{}
	}
	return ok(w, commits)
}

// ---- GitLab ----

func (s *Server) gitlabStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.projectRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	var enabled bool
	if err := s.db.QueryRow(r.Context(), `SELECT gitlab_secret IS NOT NULL FROM projects WHERE id = $1`, id).Scan(&enabled); err != nil {
		return err
	}
	until, err := hookPrevUntil(r.Context(), s.db, hookGitLab, id)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"enabled": enabled, "path": "/api/hooks/gitlab/" + id, "previousValidUntil": until})
}

// enableGitLab (re)generates the token GitLab sends in X-Gitlab-Token. It's shown once; after
// regenerating, the old one keeps working for WEBHOOK_SECRET_GRACE.
func (s *Server) enableGitLab(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wsID, err := s.projectRole(r.Context(), s.db, id, u, roleAdmin)
	if err != nil {
		return err
	}
	secret := randomToken(24)
	var until *time.Time
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if until, err = s.rotateHookSecret(r.Context(), tx, hookGitLab, id, secret); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, wsID, u.ID, "webhook.gitlab.rotate", "project", id, map[string]any{"previousValidUntil": until})
	}); err != nil {
		return err
	}
	return ok(w, map[string]any{"enabled": true, "secret": secret, "path": "/api/hooks/gitlab/" + id, "previousValidUntil": until})
}

func (s *Server) disableGitLab(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wsID, err := s.projectRole(r.Context(), s.db, id, u, roleAdmin)
	if err != nil {
		return err
	}
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := disableHook(r.Context(), tx, hookGitLab, id); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, wsID, u.ID, "webhook.gitlab.disable", "project", id, nil)
	}); err != nil {
		return err
	}
	return ok(w, map[string]bool{"enabled": false})
}

type gitlabMR struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	Attrs struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		URL          string `json:"url"`
		State        string `json:"state"`
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		Draft        bool   `json:"draft"`
		WIP          bool   `json:"work_in_progress"`
	} `json:"object_attributes"`
}

func gitlabState(ev gitlabMR) string {
	switch ev.Attrs.State {
	case "merged":
		return "merged"
	case "closed", "locked":
		return "closed"
	}
	if ev.Attrs.Draft || ev.Attrs.WIP {
		return "draft"
	}
	return "open"
}

// gitlabWebhook handles GitLab's Merge Request and Push hooks, authenticated by X-Gitlab-Token.
func (s *Server) gitlabWebhook(w http.ResponseWriter, r *http.Request) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.limit(w, s.webhookLimit, "webhook", projectID); err != nil {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		return badRequest("payload too large")
	}
	secret, prev, key, err := hookSecrets(r.Context(), s.db, hookGitLab, projectID)
	if err != nil {
		return err
	}
	if secret == nil {
		return errNotFound
	}
	got := r.Header.Get("X-Gitlab-Token")
	if !constantTimeEqual(*secret, got) && (prev == nil || !constantTimeEqual(*prev, got)) {
		return errStatus(http.StatusUnauthorized, "bad token")
	}
	switch r.Header.Get("X-Gitlab-Event") {
	case "Merge Request Hook":
	case "Push Hook":
		var ev struct {
			Ref     string `json:"ref"`
			Before  string `json:"before"`
			After   string `json:"after"`
			Project struct {
				PathWithNamespace string `json:"path_with_namespace"`
			} `json:"project"`
			Commits []struct {
				ID        string                `json:"id"`
				Message   string                `json:"message"`
				URL       string                `json:"url"`
				Timestamp string                `json:"timestamp"`
				Author    struct{ Name string } `json:"author"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return badRequest("invalid payload")
		}
		zero := strings.Repeat("0", 40)
		if ev.After == zero {
			return ok(w, map[string]string{"ignored": "branch deleted"})
		}
		p := push{Provider: "gitlab", Repo: ev.Project.PathWithNamespace, Branch: branchOf(ev.Ref), Created: ev.Before == zero}
		for _, c := range ev.Commits {
			if strings.HasPrefix(c.URL, "https://") {
				p.Commits = append(p.Commits, pushedCommit{ID: c.ID, Message: c.Message, URL: c.URL, Author: c.Author.Name, Timestamp: c.Timestamp})
			}
		}
		res, err := s.applyPush(r.Context(), projectID, key, p)
		if err != nil {
			return err
		}
		return ok(w, res)
	default:
		return ok(w, map[string]string{"ignored": r.Header.Get("X-Gitlab-Event")})
	}
	var ev gitlabMR
	if err := json.Unmarshal(body, &ev); err != nil || ev.ObjectKind != "merge_request" {
		return badRequest("not a merge request payload")
	}
	pr := PullRequest{
		Provider: "gitlab",
		Repo:     ev.Project.PathWithNamespace,
		Number:   ev.Attrs.IID,
		Title:    ev.Attrs.Title,
		URL:      ev.Attrs.URL,
		Author:   ev.User.Username,
		State:    gitlabState(ev),
	}
	if pr.Repo == "" || pr.Number == 0 || !strings.HasPrefix(pr.URL, "https://") {
		return badRequest("not a merge request payload")
	}
	// GitLab's actions: open, reopen, update, close, merge, approved, unapproved…
	action := map[string]string{"open": "opened", "reopen": "reopened", "update": "update"}[ev.Attrs.Action]
	if action == "" {
		action = ev.Attrs.Action
	}
	numbers := issueNumbers(key, ev.Attrs.Title+" "+ev.Attrs.SourceBranch+" "+ev.Attrs.Description)
	moved := 0
	for _, n := range numbers {
		if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
			did, err := s.applyPR(r.Context(), tx, projectID, n, action, pr)
			if did {
				moved++
			}
			return err
		}); err != nil {
			return err
		}
	}
	return ok(w, map[string]any{"linked": len(numbers), "moved": moved})
}
