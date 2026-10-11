package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Personal API tokens: "Authorization: Bearer gly_…". A token acts as its owner, limited to the
// routes registered with s.api (the documented surface in openapi.yaml) and, for read tokens, to
// GET. Each token has its own rate limit. Tokens can't manage tokens, passwords or sign-in methods.

const tokenPrefix = "gly_"

type apiToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func (s *Server) tokenUser(ctx context.Context, raw string) (*User, error) {
	if !strings.HasPrefix(raw, tokenPrefix) || len(raw) > 100 {
		return nil, errStatus(http.StatusUnauthorized, "invalid API token")
	}
	var u User
	var t apiToken
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.color, t.id, t.scope, t.last_used_at FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > now())`, hashToken(raw)).
		Scan(&u.ID, &u.Email, &u.Name, &u.Color, &t.ID, &t.Scope, &t.LastUsedAt)
	if err == pgx.ErrNoRows {
		return nil, errStatus(http.StatusUnauthorized, "invalid, expired or revoked API token")
	}
	if err != nil {
		return nil, err
	}
	// Record use at most once a minute, so busy scripts don't turn every read into a write.
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > time.Minute {
		if _, err := s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1`, t.ID); err != nil {
			return nil, err
		}
	}
	u.token = &t
	return &u, nil
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:]), true
	}
	return "", false
}

// apiGate applies token rules to a request on a public route.
func (s *Server) apiGate(w http.ResponseWriter, r *http.Request, u *User) error {
	t := u.token
	if t == nil {
		return nil
	}
	if t.Scope == "read" && r.Method != http.MethodGet && r.Method != http.MethodHead {
		return errStatus(http.StatusForbidden, "this token is read-only")
	}
	allowed, remaining, reset := s.tokenLimit.check(t.ID)
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(s.tokenLimit.limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(reset).Seconds()+0.999))))
		observeRateLimited("api_token")
		return errStatus(http.StatusTooManyRequests, "rate limit exceeded for this token")
	}
	return nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, u *User) error {
	ts, err := collect(func(row pgx.Row) (apiToken, error) {
		var t apiToken
		return t, row.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt)
	})(s.db.Query(r.Context(), `SELECT id, name, prefix, scope, created_at, expires_at, last_used_at FROM api_tokens
		WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, u.ID))
	if err != nil {
		return err
	}
	return ok(w, ts)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, u *User) error {
	var body struct {
		Name          string `json:"name"`
		Scope         string `json:"scope"`
		ExpiresInDays *int   `json:"expiresInDays"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		return badRequest("name the token (1–60 characters) so you can recognise it later")
	}
	if body.Scope == "" {
		body.Scope = "write"
	}
	if body.Scope != "read" && body.Scope != "write" {
		return badRequest("scope must be read or write")
	}
	var expires *time.Time
	if body.ExpiresInDays != nil {
		if *body.ExpiresInDays < 1 || *body.ExpiresInDays > 366 {
			return badRequest("expiry must be 1–366 days")
		}
		e := time.Now().AddDate(0, 0, *body.ExpiresInDays)
		expires = &e
	}
	raw := tokenPrefix + randomToken(30)
	var t apiToken
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL`, u.ID).Scan(&n); err != nil {
			return err
		}
		if n >= 50 {
			return badRequest("you have 50 active tokens; revoke some first")
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO api_tokens (user_id, name, prefix, token_hash, scope, expires_at) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, name, prefix, scope, created_at, expires_at, last_used_at`, u.ID, name, raw[:12], hashToken(raw), body.Scope, expires).
			Scan(&t.ID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt); err != nil {
			return err
		}
		return s.auditUser(r.Context(), tx, u.ID, "token.create", "api_token", t.ID, map[string]any{"name": name, "scope": body.Scope})
	})
	if err != nil {
		return err
	}
	// The only time the token itself is ever returned.
	return ok(w, map[string]any{"token": raw, "info": t})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if err := s.auditUser(r.Context(), tx, u.ID, "token.revoke", "api_token", id, nil); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}
