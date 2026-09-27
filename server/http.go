package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	db    *pgxpool.Pool
	hub   *Hub
	cfg   Config
	login *rateLimiter
	demo  *rateLimiter
}

func NewServer(db *pgxpool.Pool, hub *Hub, cfg Config) *Server {
	return &Server{db: db, hub: hub, cfg: cfg, login: newRateLimiter(10, time.Minute), demo: newRateLimiter(5, 10*time.Minute)}
}

// apiError is an error with an HTTP status that is safe to show to clients.
type apiError struct {
	Status int
	Msg    string
	Data   any
}

func (e *apiError) Error() string { return e.Msg }

func errStatus(status int, msg string) error { return &apiError{Status: status, Msg: msg} }

var (
	errNotFound     = errStatus(http.StatusNotFound, "not found")
	errUnauthorized = errStatus(http.StatusUnauthorized, "sign in required")
	errForbidden    = errStatus(http.StatusForbidden, "you don't have access to that")
)

func badRequest(msg string) error { return errStatus(http.StatusBadRequest, msg) }

type handler func(w http.ResponseWriter, r *http.Request) error

func (s *Server) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		var ae *apiError
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &ae):
		case errors.Is(err, pgx.ErrNoRows):
			ae = errNotFound.(*apiError)
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			ae = &apiError{Status: http.StatusConflict, Msg: "that already exists"}
		case errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22P02" || pgErr.Code == "23503"):
			ae = &apiError{Status: http.StatusBadRequest, Msg: "invalid value"}
		default:
			if r.Context().Err() == nil {
				slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			}
			ae = &apiError{Status: http.StatusInternalServerError, Msg: "something went wrong"}
		}
		body := map[string]any{"error": ae.Msg}
		if ae.Data != nil {
			body["data"] = ae.Data
		}
		writeJSON(w, ae.Status, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusOK, v)
	return nil
}

func readJSON(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errStatus(http.StatusUnsupportedMediaType, "expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body")
	}
	return nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// pathID returns a validated UUID path parameter, so malformed ids 404 instead of hitting the database.
func pathID(r *http.Request, name string) (string, error) {
	id := strings.ToLower(r.PathValue(name))
	if !uuidRe.MatchString(id) {
		return "", errNotFound
	}
	return id, nil
}

func validID(id string) bool { return uuidRe.MatchString(id) }

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Server) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

// sameOrigin rejects cross-site state-changing requests. Cookies are SameSite=Lax and
// mutations require a JSON body; this is the belt to those braces.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	for _, o := range s.cfg.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "path", r.URL.Path, "panic", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "something went wrong"})
			}
		}()
		webhook := strings.HasPrefix(r.URL.Path, "/api/hooks/")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !webhook && !s.sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn handler) { mux.HandleFunc(pattern, s.wrap(fn)) }
	authed := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *User) error) {
		mux.HandleFunc(pattern, s.wrap(func(w http.ResponseWriter, r *http.Request) error {
			u, err := s.currentUser(r)
			if err != nil {
				return err
			}
			return fn(w, r, u)
		}))
	}

	h("GET /api/health", func(w http.ResponseWriter, r *http.Request) error {
		if err := s.db.Ping(r.Context()); err != nil {
			return errStatus(http.StatusServiceUnavailable, "database unavailable")
		}
		return ok(w, map[string]bool{"ok": true})
	})

	h("POST /api/auth/signup", s.signup)
	h("POST /api/auth/login", s.loginHandler)
	h("POST /api/auth/logout", s.logout)
	h("POST /api/auth/demo", s.startDemo)
	authed("GET /api/me", s.me)
	authed("PATCH /api/me", s.updateMe)

	authed("POST /api/workspaces", s.createWorkspace)
	authed("PATCH /api/workspaces/{ws}", s.updateWorkspace)
	authed("GET /api/workspaces/{ws}/members", s.listMembers)
	authed("PATCH /api/workspaces/{ws}/members/{user}", s.updateMember)
	authed("DELETE /api/workspaces/{ws}/members/{user}", s.removeMember)
	authed("POST /api/workspaces/{ws}/invites", s.createInvite)
	h("GET /api/invites/{token}", s.previewInvite)
	authed("POST /api/invites/{token}/accept", s.acceptInvite)

	authed("GET /api/workspaces/{ws}/projects", s.listProjects)
	authed("POST /api/workspaces/{ws}/projects", s.createProject)
	authed("GET /api/workspaces/{ws}/my-work", s.myWork)
	authed("GET /api/projects/{id}", s.getProject)
	authed("PATCH /api/projects/{id}", s.updateProject)
	authed("DELETE /api/projects/{id}", s.deleteProject)
	authed("GET /api/projects/{id}/live", s.live)
	authed("GET /api/projects/{id}/activity", s.projectActivity)

	authed("POST /api/projects/{id}/statuses", s.createStatus)
	authed("PATCH /api/statuses/{id}", s.updateStatus)
	authed("DELETE /api/statuses/{id}", s.deleteStatus)

	authed("POST /api/projects/{id}/issues", s.createIssue)
	authed("PATCH /api/issues/{id}", s.updateIssue)
	authed("DELETE /api/issues/{id}", s.deleteIssue)
	authed("POST /api/issues/{id}/blockers", s.addBlocker)
	authed("DELETE /api/issues/{id}/blockers/{blocker}", s.removeBlocker)
	authed("GET /api/issues/{id}/comments", s.listComments)
	authed("POST /api/issues/{id}/comments", s.addComment)
	authed("DELETE /api/comments/{id}", s.deleteComment)
	authed("GET /api/issues/{id}/activity", s.issueActivity)
	authed("GET /api/issues/{id}/prs", s.listPRs)

	authed("PUT /api/projects/{id}/snapshot", s.putSnapshot)
	authed("GET /api/projects/{id}/snapshots", s.listSnapshots)
	authed("GET /api/projects/{id}/github", s.githubStatus)
	authed("POST /api/projects/{id}/github", s.enableGitHub)
	authed("DELETE /api/projects/{id}/github", s.disableGitHub)
	// Signed by GitHub, not by a session; same-origin checks don't apply (see middleware).
	h("POST /api/hooks/github/{id}", s.githubWebhook)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	if s.cfg.StaticDir != "" {
		mux.Handle("/", spaHandler(s.cfg.StaticDir))
	}
	return s.middleware(mux)
}

// spaHandler serves built frontend assets, falling back to index.html for client routes.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
