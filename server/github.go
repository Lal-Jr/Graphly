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
	Provider  string    `json:"provider"`
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
	if _, err := s.projectRole(r.Context(), s.db, id, u, roleViewer); err != nil {
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
	wsID, err := s.projectRole(r.Context(), s.db, id, u, roleAdmin)
	if err != nil {
		return err
	}
	secret := randomToken(24)
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE projects SET github_secret = $2 WHERE id = $1`, id, secret); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, wsID, u.ID, "webhook.github.rotate", "project", id, nil)
	}); err != nil {
		return err
	}
	return ok(w, map[string]any{"enabled": true, "secret": secret, "path": "/api/hooks/github/" + id})
}

func (s *Server) disableGitHub(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wsID, err := s.projectRole(r.Context(), s.db, id, u, roleAdmin)
	if err != nil {
		return err
	}
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE projects SET github_secret = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, wsID, u.ID, "webhook.github.disable", "project", id, nil)
	}); err != nil {
		return err
	}
	return ok(w, map[string]bool{"enabled": false})
}

func (s *Server) listPRs(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	prs, err := collect(scanPR)(s.db.Query(r.Context(), `SELECT provider, repo, number, title, url, state, author, updated_at FROM issue_prs WHERE issue_id = $1 ORDER BY updated_at DESC`, id))
	if err != nil {
		return err
	}
	if prs == nil {
		prs = []PullRequest{}
	}
	return ok(w, prs)
}

func scanPR(row pgx.Row) (PullRequest, error) {
	var p PullRequest
	return p, row.Scan(&p.Provider, &p.Repo, &p.Number, &p.Title, &p.URL, &p.State, &p.Author, &p.UpdatedAt)
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
	case "push":
		return s.githubPush(w, r, projectID, key, body)
	default:
		return ok(w, map[string]string{"ignored": r.Header.Get("X-GitHub-Event")})
	}
	var ev prEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return badRequest("invalid payload")
	}
	pr := PullRequest{
		Provider: "github",
		Repo:     ev.Repository.FullName,
		Number:   ev.PullRequest.Number,
		Title:    ev.PullRequest.Title,
		URL:      ev.PullRequest.HTMLURL,
		Author:   ev.PullRequest.User.Login,
		State:    prState(ev),
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
	_ = tx.QueryRow(ctx, `SELECT state FROM issue_prs WHERE issue_id = $1 AND provider = $2 AND repo = $3 AND number = $4`,
		issueID, pr.Provider, pr.Repo, pr.Number).Scan(&prevState)
	if _, err := tx.Exec(ctx, `
		INSERT INTO issue_prs (issue_id, provider, repo, number, title, url, state, author, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (issue_id, provider, repo, number) DO UPDATE SET title = EXCLUDED.title, url = EXCLUDED.url, state = EXCLUDED.state,
			author = EXCLUDED.author, updated_at = now()`,
		issueID, pr.Provider, pr.Repo, pr.Number, pr.Title, pr.URL, pr.State, pr.Author); err != nil {
		return false, err
	}
	if err := publish(ctx, tx, projectID, "pr.update", map[string]any{"issueId": issueID, "pr": pr}); err != nil {
		return false, err
	}
	if prevState == nil || *prevState != pr.State {
		if err := logActivity(ctx, tx, projectID, &issueID, nil, "pr", map[string]any{
			"state": pr.State, "repo": pr.Repo, "number": pr.Number, "url": pr.URL, "title": pr.Title, "author": pr.Author, "provider": pr.Provider,
		}); err != nil {
			return false, err
		}
	}

	// Pick the column this PR event should move the issue to.
	var target string
	switch {
	case pr.State == "merged":
		target = targetDone
	case pr.State == "open" && action != "synchronize" && action != "edited" && action != "update":
		target = targetReview
	case pr.State == "draft":
		target = targetStarted
	default:
		return false, nil
	}
	return s.advance(ctx, tx, projectID, issueID, target, pr.Provider, map[string]any{"pr": pr.Number})
}
