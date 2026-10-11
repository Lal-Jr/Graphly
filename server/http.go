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
	// Recent forecasts, keyed by project version.
	forecasts *forecastCache
	// Called after the background job records a forecast, with the one it replaced (may be nil).
	onForecast func(ctx context.Context, prev, cur *ProjectForecast) error
	// Sends email; nil when SMTP isn't configured.
	mailer mailer
	// For outbound calls (Slack, OAuth providers).
	httpClient *http.Client
	// Per-token request budget.
	tokenLimit *rateLimiter
	// Per user (writes, imports), per project (webhooks) and per IP (unauthenticated endpoints).
	writeLimit, importLimit, webhookLimit, publicLimit *rateLimiter
	// "METHOD /path" of every route an API token may call; must match openapi.yaml.
	publicRoutes []string
	// Configured sign-in providers by id (google, github).
	providers map[string]*oauthProvider
}

func NewServer(db *pgxpool.Pool, hub *Hub, cfg Config) (s *Server) {
	defer func() { s.registerRuntimeCollectors() }()
	if cfg.ForecastLocation == nil {
		cfg.ForecastLocation = time.UTC
	}
	if cfg.CalibrationMinIssues == 0 {
		cfg.CalibrationMinIssues = 20
	}
	if cfg.CalibrationMinPerType == 0 {
		cfg.CalibrationMinPerType = 5
	}
	if cfg.ShipDateMoveThreshold == 0 {
		cfg.ShipDateMoveThreshold = 2
	}
	if cfg.SlackWebhookHosts == nil {
		cfg.SlackWebhookHosts = []string{"hooks.slack.com"}
	}
	if cfg.DeliveryPollInterval == 0 {
		cfg.DeliveryPollInterval = 5 * time.Second
	}
	if cfg.APITokenRateLimit == 0 {
		cfg.APITokenRateLimit = 600
	}
	if cfg.DeliveryMaxAttempts == 0 {
		cfg.DeliveryMaxAttempts = 8
	}
	for _, d := range []struct {
		v   *int
		def int
	}{{&cfg.WriteRateLimit, 300}, {&cfg.ImportRateLimit, 20}, {&cfg.WebhookRateLimit, 600}, {&cfg.PublicRateLimit, 120}, {&cfg.MaxIssuesPerProject, 20000}} {
		if *d.v == 0 {
			*d.v = d.def
		}
	}
	s = &Server{db: db, hub: hub, cfg: cfg, login: newRateLimiter(10, time.Minute), demo: newRateLimiter(5, 10*time.Minute),
		forecasts: newForecastCache(256), mailer: newSMTPMailer(cfg), httpClient: outboundClient(), providers: oauthProviders(cfg),
		tokenLimit: newRateLimiter(cfg.APITokenRateLimit, time.Minute), writeLimit: newRateLimiter(cfg.WriteRateLimit, time.Minute),
		importLimit: newRateLimiter(cfg.ImportRateLimit, time.Hour), webhookLimit: newRateLimiter(cfg.WebhookRateLimit, time.Minute),
		publicLimit: newRateLimiter(cfg.PublicRateLimit, time.Minute)}
	s.onForecast = s.forecastNotifications
	return s
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
				slog.Error("request failed", append(logAttrs(r.Context()), "method", r.Method, "path", r.URL.Path, "err", err)...)
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
		r = r.WithContext(withAuditIP(r.Context(), clientIP(r, s.cfg.ClientIPHeader)))
		s.observe(w, r, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.guard(w, r, next) }))
	})
}

// guard applies the same-origin check and baseline response headers.
func (s *Server) guard(w http.ResponseWriter, r *http.Request, next http.Handler) {
	{
		webhook := strings.HasPrefix(r.URL.Path, "/api/hooks/")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !webhook && !s.sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		s.securityHeaders(w.Header())
		next.ServeHTTP(w, r)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn handler) { mux.HandleFunc(pattern, s.wrap(fn)) }
	// authed routes need a signed-in session; api routes also accept API tokens and form the
	// public, documented surface (openapi.yaml).
	authed := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *User) error) {
		mux.HandleFunc(pattern, s.wrap(func(w http.ResponseWriter, r *http.Request) error {
			u, err := s.currentUser(r)
			if err != nil {
				return err
			}
			if u.token != nil {
				return errStatus(http.StatusForbidden, "API tokens can't use this endpoint")
			}
			if err := s.limitWrites(w, r, u); err != nil {
				return err
			}
			return fn(w, r, u)
		}))
	}
	s.publicRoutes = nil
	api := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *User) error) {
		s.publicRoutes = append(s.publicRoutes, pattern)
		mux.HandleFunc(pattern, s.wrap(func(w http.ResponseWriter, r *http.Request) error {
			u, err := s.currentUser(r)
			if err != nil {
				return err
			}
			if err := s.apiGate(w, r, u); err != nil {
				return err
			}
			if err := s.limitWrites(w, r, u); err != nil {
				return err
			}
			return fn(w, r, u)
		}))
	}

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /api/health", s.readyz) // older name, kept for existing health checks
	mux.HandleFunc("GET /metrics", s.metricsHandler(true))

	h("POST /api/auth/signup", s.public(s.signup))
	h("POST /api/auth/login", s.loginHandler)
	h("POST /api/auth/logout", s.logout)
	h("POST /api/auth/demo", s.startDemo)
	h("GET /api/auth/providers", s.public(s.listProviders))
	h("GET /api/auth/oauth/{provider}/start", s.public(s.oauthStart))
	h("GET /api/auth/oauth/{provider}/callback", s.public(s.oauthCallback))
	authed("GET /api/me/identities", s.listIdentities)
	authed("DELETE /api/me/identities/{provider}", s.unlinkIdentity)
	authed("PUT /api/me/password", s.setPassword)
	api("GET /api/me", s.me)
	authed("GET /api/me/tokens", s.listTokens)
	authed("POST /api/me/tokens", s.createToken)
	authed("DELETE /api/me/tokens/{id}", s.revokeToken)
	h("GET /api/openapi.yaml", s.openAPISpec)
	authed("PATCH /api/me", s.updateMe)

	authed("POST /api/workspaces", s.createWorkspace)
	authed("PATCH /api/workspaces/{ws}", s.updateWorkspace)
	api("GET /api/workspaces/{ws}/members", s.listMembers)
	authed("PATCH /api/workspaces/{ws}/members/{user}", s.updateMember)
	authed("DELETE /api/workspaces/{ws}/members/{user}", s.removeMember)
	authed("POST /api/workspaces/{ws}/invites", s.createInvite)
	api("POST /api/workspaces/{ws}/import", s.importHandler)
	h("GET /api/invites/{token}", s.public(s.previewInvite))
	authed("POST /api/invites/{token}/accept", s.acceptInvite)

	api("GET /api/workspaces/{ws}/projects", s.listProjects)
	api("POST /api/workspaces/{ws}/projects", s.createProject)
	authed("GET /api/workspaces/{ws}/my-work", s.myWork)
	api("GET /api/projects/{id}", s.getProject)
	api("PATCH /api/projects/{id}", s.updateProject)
	authed("DELETE /api/projects/{id}", s.deleteProject)
	authed("GET /api/projects/{id}/live", s.live)
	api("GET /api/projects/{id}/activity", s.projectActivity)

	api("GET /api/projects/{id}/members", s.listProjectMembers)
	authed("PUT /api/projects/{id}/members/{user}", s.setProjectMember)
	authed("GET /api/workspaces/{ws}/audit", s.listAudit)
	authed("POST /api/projects/{id}/statuses", s.createStatus)
	authed("PATCH /api/statuses/{id}", s.updateStatus)
	authed("DELETE /api/statuses/{id}", s.deleteStatus)

	api("GET /api/projects/{id}/issues", s.listIssues)
	api("GET /api/projects/{id}/issues/{number}", s.getIssueByNumber)
	api("GET /api/projects/{id}/links", s.listLinks)
	api("POST /api/projects/{id}/issues", s.createIssue)
	api("GET /api/issues/{id}", s.getIssue)
	api("PATCH /api/issues/{id}", s.updateIssue)
	api("DELETE /api/issues/{id}", s.deleteIssue)
	api("POST /api/issues/{id}/blockers", s.addBlocker)
	api("DELETE /api/issues/{id}/blockers/{blocker}", s.removeBlocker)
	api("GET /api/issues/{id}/comments", s.listComments)
	api("POST /api/issues/{id}/comments", s.addComment)
	authed("DELETE /api/comments/{id}", s.deleteComment)
	authed("GET /api/issues/{id}/activity", s.issueActivity)
	authed("GET /api/issues/{id}/prs", s.listPRs)
	authed("GET /api/issues/{id}/commits", s.listCommits)
	authed("GET /api/issues/{id}/watch", s.getWatch)
	authed("PUT /api/issues/{id}/watch", s.setWatch)
	authed("DELETE /api/issues/{id}/watch", s.setWatch)

	authed("GET /api/notifications", s.listNotifications)
	authed("GET /api/notifications/unread", s.unreadCount)
	authed("POST /api/notifications/read", s.markRead)
	authed("GET /api/me/notification-settings", s.getNotificationSettings)
	authed("PUT /api/me/notification-settings", s.updateNotificationSettings)

	api("GET /api/projects/{id}/forecast", s.getForecast)
	authed("PUT /api/projects/{id}/snapshot", s.putSnapshot)
	api("GET /api/projects/{id}/snapshots", s.listSnapshots)
	authed("GET /api/projects/{id}/github", s.githubStatus)
	authed("POST /api/projects/{id}/github", s.enableGitHub)
	authed("DELETE /api/projects/{id}/github", s.disableGitHub)
	// Signed by GitHub, not by a session; same-origin checks don't apply (see middleware).
	h("POST /api/hooks/github/{id}", s.githubWebhook)
	authed("GET /api/projects/{id}/gitlab", s.gitlabStatus)
	authed("POST /api/projects/{id}/gitlab", s.enableGitLab)
	authed("DELETE /api/projects/{id}/gitlab", s.disableGitLab)
	// Authenticated by X-Gitlab-Token, not by a session.
	h("POST /api/hooks/gitlab/{id}", s.gitlabWebhook)

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
