package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// limit counts a hit against l for key and refuses with 429 (and Retry-After) past the limit.
func (s *Server) limit(w http.ResponseWriter, l *rateLimiter, name, key string) error {
	ok, _, reset := l.check(key)
	if ok {
		return nil
	}
	observeRateLimited(name)
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(reset).Seconds()+0.999))))
	return errStatus(http.StatusTooManyRequests, "too many requests, try again shortly")
}

// limitWrites applies the per-user write limit to state-changing requests.
func (s *Server) limitWrites(w http.ResponseWriter, r *http.Request, u *User) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	return s.limit(w, s.writeLimit, "write", u.ID)
}

// public wraps an unauthenticated endpoint with the per-IP limit.
func (s *Server) public(fn handler) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if err := s.limit(w, s.publicLimit, "public", clientIP(r, s.cfg.ClientIPHeader)); err != nil {
			return err
		}
		return fn(w, r)
	}
}

// ---- webhook secrets ----

type hookProvider string

const (
	hookGitHub hookProvider = "github"
	hookGitLab hookProvider = "gitlab"
)

// hookSecrets returns a project's current webhook secret and, while its grace period lasts, the
// previous one. current is nil when the integration is off.
func hookSecrets(ctx context.Context, q querier, provider hookProvider, projectID string) (current, previous *string, key string, err error) {
	err = q.QueryRow(ctx, fmt.Sprintf(`SELECT %[1]s_secret, CASE WHEN %[1]s_secret_prev_until > now() THEN %[1]s_secret_prev END, key
		FROM projects WHERE id = $1`, provider), projectID).Scan(&current, &previous, &key)
	return
}

// rotateHookSecret installs a new secret. The old one stays valid for WEBHOOK_SECRET_GRACE so
// deliveries keep landing while the new secret is pasted into GitHub or GitLab.
func (s *Server) rotateHookSecret(ctx context.Context, tx pgx.Tx, provider hookProvider, projectID, secret string) (*time.Time, error) {
	var until *time.Time
	err := tx.QueryRow(ctx, fmt.Sprintf(`UPDATE projects SET
			%[1]s_secret_prev = CASE WHEN $3 > 0 THEN %[1]s_secret END,
			%[1]s_secret_prev_until = CASE WHEN $3 > 0 AND %[1]s_secret IS NOT NULL THEN now() + make_interval(secs => $3) END,
			%[1]s_secret = $2
		WHERE id = $1 RETURNING %[1]s_secret_prev_until`, provider), projectID, secret, s.cfg.WebhookSecretGrace.Seconds()).Scan(&until)
	return until, err
}

func disableHook(ctx context.Context, tx pgx.Tx, provider hookProvider, projectID string) error {
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE projects SET %[1]s_secret = NULL, %[1]s_secret_prev = NULL, %[1]s_secret_prev_until = NULL
		WHERE id = $1`, provider), projectID)
	return err
}

func hookPrevUntil(ctx context.Context, q querier, provider hookProvider, projectID string) (*time.Time, error) {
	var until *time.Time
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT CASE WHEN %[1]s_secret_prev_until > now() THEN %[1]s_secret_prev_until END FROM projects WHERE id = $1`, provider), projectID).Scan(&until)
	return until, err
}

// ---- response headers ----

// contentSecurityPolicy allows only same-origin scripts, styles, fonts, workers and connections
// (WebSockets included), no framing, and no plugins. The built app has no inline scripts.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"

func (s *Server) securityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	if s.cfg.HSTSMaxAge > 0 {
		h.Set("Strict-Transport-Security", "max-age="+strconv.Itoa(int(s.cfg.HSTSMaxAge.Seconds()))+"; includeSubDomains")
	}
}
