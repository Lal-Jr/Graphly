package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// GitHub integration: point a repository webhook (pull_request events) at
// /api/hooks/github/{projectId}. Any PR whose title, branch or description mentions an issue key
// (e.g. "APL-12") is linked to that issue and moves it along the workflow:
//
//	opened / ready for review → the review column    merged → done    draft → in progress
//
// Issues only ever move forward, so a late webhook can't drag finished work backwards.

type PullRequest struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	Author    string    `json:"author"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Server) githubStatus(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, _, err := s.projectAccess(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	var enabled bool
	if err := s.db.QueryRow(r.Context(), `SELECT github_secret IS NOT NULL FROM projects WHERE id = $1`, id).Scan(&enabled); err != nil {
		return err
	}
	return ok(w, map[string]any{"enabled": enabled, "path": "/api/hooks/github/" + id})
}

// enableGitHub (re)generates the webhook secret. It's shown once; regenerating invalidates the old one.
func (s *Server) enableGitHub(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, role, err := s.projectAccess(r.Context(), s.db, id, u.ID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errStatus(http.StatusForbidden, "only workspace admins can manage integrations")
	}
	secret := randomToken(24)
	if _, err := s.db.Exec(r.Context(), `UPDATE projects SET github_secret = $2 WHERE id = $1`, id, secret); err != nil {
		return err
	}
	return ok(w, map[string]any{"enabled": true, "secret": secret, "path": "/api/hooks/github/" + id})
}

func (s *Server) disableGitHub(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, role, err := s.projectAccess(r.Context(), s.db, id, u.ID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errStatus(http.StatusForbidden, "only workspace admins can manage integrations")
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE projects SET github_secret = NULL WHERE id = $1`, id); err != nil {
		return err
	}
	return ok(w, map[string]bool{"enabled": false})
}

func (s *Server) listPRs(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueProject(r.Context(), s.db, id, u.ID); err != nil {
		return err
	}
	prs, err := collect(scanPR)(s.db.Query(r.Context(), `SELECT repo, number, title, url, state, author, updated_at FROM issue_prs WHERE issue_id = $1 ORDER BY updated_at DESC`, id))
	if err != nil {
		return err
	}
	return ok(w, prs)
}

func scanPR(row pgx.Row) (PullRequest, error) {
	var p PullRequest
	return p, row.Scan(&p.Repo, &p.Number, &p.Title, &p.URL, &p.State, &p.Author, &p.UpdatedAt)
}

type prEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		Merged  bool   `json:"merged"`
		State   string `json:"state"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		return badRequest("payload too large")
	}
	var secret *string
	var key string
	if err := s.db.QueryRow(r.Context(), `SELECT github_secret, key FROM projects WHERE id = $1`, projectID).Scan(&secret, &key); err != nil {
		return err
	}
	if secret == nil {
		return errNotFound
	}
	if !validSignature(*secret, body, r.Header.Get("X-Hub-Signature-256")) {
		return errStatus(http.StatusUnauthorized, "bad signature")
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		return ok(w, map[string]string{"ok": "pong"})
	case "pull_request":
	default:
		return ok(w, map[string]string{"ignored": r.Header.Get("X-GitHub-Event")})
	}
	var ev prEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return badRequest("invalid payload")
	}
	pr := PullRequest{
		Repo:   ev.Repository.FullName,
		Number: ev.PullRequest.Number,
		Title:  ev.PullRequest.Title,
		URL:    ev.PullRequest.HTMLURL,
		Author: ev.PullRequest.User.Login,
		State:  prState(ev),
	}
	if pr.Repo == "" || pr.Number == 0 || !strings.HasPrefix(pr.URL, "https://") {
		return badRequest("not a pull request payload")
	}
	numbers := issueNumbers(key, ev.PullRequest.Title+" "+ev.PullRequest.Head.Ref+" "+ev.PullRequest.Body)
	moved := 0
	for _, n := range numbers {
		err := s.inTx(r.Context(), func(tx pgx.Tx) error {
			did, err := s.applyPR(r.Context(), tx, projectID, n, ev.Action, pr)
			if did {
				moved++
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return ok(w, map[string]any{"linked": len(numbers), "moved": moved})
}

func validSignature(secret string, body []byte, header string) bool {
	sig, found := strings.CutPrefix(header, "sha256=")
	if !found {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func prState(ev prEvent) string {
	switch {
	case ev.PullRequest.Merged:
		return "merged"
	case ev.PullRequest.State == "closed":
		return "closed"
	case ev.PullRequest.Draft:
		return "draft"
	default:
		return "open"
	}
}

// issueNumbers finds "KEY-123" mentions (case-insensitive), deduplicated and capped.
func issueNumbers(key, text string) []int {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(key) + `-(\d{1,7})\b`)
	seen := map[int]bool{}
	var out []int
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		if n > 0 && !seen[n] && len(out) < 20 {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// applyPR links a PR to issue #number and advances its status. It reports whether the status changed.
func (s *Server) applyPR(ctx context.Context, tx pgx.Tx, projectID string, number int, action string, pr PullRequest) (bool, error) {
	var issueID string
	err := tx.QueryRow(ctx, `SELECT id FROM issues WHERE project_id = $1 AND number = $2 FOR UPDATE`, projectID, number).Scan(&issueID)
	if err == pgx.ErrNoRows {
		return false, nil // mentions of issues that don't exist are ignored
	}
	if err != nil {
		return false, err
	}
	pr.UpdatedAt = time.Now()
	var prevState *string
	_ = tx.QueryRow(ctx, `SELECT state FROM issue_prs WHERE issue_id = $1 AND repo = $2 AND number = $3`, issueID, pr.Repo, pr.Number).Scan(&prevState)
	if _, err := tx.Exec(ctx, `
		INSERT INTO issue_prs (issue_id, repo, number, title, url, state, author, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (issue_id, repo, number) DO UPDATE SET title = EXCLUDED.title, url = EXCLUDED.url, state = EXCLUDED.state,
			author = EXCLUDED.author, updated_at = now()`,
		issueID, pr.Repo, pr.Number, pr.Title, pr.URL, pr.State, pr.Author); err != nil {
		return false, err
	}
	if err := publish(ctx, tx, projectID, "pr.update", map[string]any{"issueId": issueID, "pr": pr}); err != nil {
		return false, err
	}
	if prevState == nil || *prevState != pr.State {
		if err := logActivity(ctx, tx, projectID, &issueID, nil, "pr", map[string]any{
			"state": pr.State, "repo": pr.Repo, "number": pr.Number, "url": pr.URL, "title": pr.Title, "author": pr.Author,
		}); err != nil {
			return false, err
		}
	}

	// Pick the column this PR event should move the issue to.
	var target string
	switch {
	case pr.State == "merged":
		target = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'done' ORDER BY position LIMIT 1`
	case pr.State == "open" && action != "synchronize" && action != "edited":
		target = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'in_progress'
			ORDER BY (name ILIKE '%review%') DESC, position DESC LIMIT 1`
	case pr.State == "draft":
		target = `SELECT id, position FROM statuses WHERE project_id = $1 AND category = 'in_progress' ORDER BY position LIMIT 1`
	default:
		return false, nil
	}
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
	if err := logActivity(ctx, tx, projectID, &issueID, nil, "updated", map[string]any{
		"field": "status", "from": before.StatusID, "to": after.StatusID, "via": "github", "pr": pr.Number,
	}); err != nil {
		return false, err
	}
	return true, s.noteUnblocked(ctx, tx, projectID, before, after, nil)
}
