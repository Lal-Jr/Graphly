package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "graphly_session"
	sessionTTL    = 30 * 24 * time.Hour
)

var userColors = []string{"#0C66E4", "#1F845A", "#E56910", "#AE2E24", "#6E5DC6", "#206A83", "#943D73", "#5B7F24"}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, q querier, userID string) error {
	token := randomToken(32)
	if _, err := q.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) currentUser(r *http.Request) (*User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, errUnauthorized
	}
	var u User
	err = s.db.QueryRow(r.Context(), `
		SELECT u.id, u.email, u.name, u.color FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(c.Value)).Scan(&u.ID, &u.Email, &u.Name, &u.Color)
	if err == pgx.ErrNoRows {
		return nil, errUnauthorized
	}
	return &u, err
}

type credentials struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return "", badRequest("name must be 1–80 characters")
	}
	return name, nil
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) error {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		return err
	}
	name, err := cleanName(c.Name)
	if err != nil {
		return err
	}
	email := strings.TrimSpace(c.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return badRequest("enter a valid email address")
	}
	if len(c.Password) < 8 || len(c.Password) > 72 {
		return badRequest("password must be 8–72 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), 12)
	if err != nil {
		return err
	}
	var u User
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		color := userColors[time.Now().UnixNano()%int64(len(userColors))]
		err := tx.QueryRow(r.Context(), `INSERT INTO users (email, name, password_hash, color) VALUES ($1, $2, $3, $4)
			RETURNING id, email, name, color`, email, name, string(hash), color).Scan(&u.ID, &u.Email, &u.Name, &u.Color)
		if err != nil {
			if isUnique(err) {
				return errStatus(http.StatusConflict, "an account with that email already exists")
			}
			return err
		}
		if _, err := createWorkspaceTx(r.Context(), tx, strings.Fields(name)[0]+"'s workspace", u.ID); err != nil {
			return err
		}
		return s.startSession(r.Context(), w, tx, u.ID)
	})
	if err != nil {
		return err
	}
	return ok(w, u)
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) error {
	if !s.login.allow(clientIP(r, s.cfg.ClientIPHeader)) {
		return errStatus(http.StatusTooManyRequests, "too many attempts — try again in a minute")
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		return err
	}
	var u User
	var hash string
	err := s.db.QueryRow(r.Context(), `SELECT id, email, name, color, password_hash FROM users WHERE lower(email) = lower($1)`,
		strings.TrimSpace(c.Email)).Scan(&u.ID, &u.Email, &u.Name, &u.Color, &hash)
	if err == pgx.ErrNoRows {
		// Spend the same time as a real check so response timing doesn't reveal which emails exist.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password))
		return errStatus(http.StatusUnauthorized, "incorrect email or password")
	}
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		return errStatus(http.StatusUnauthorized, "incorrect email or password")
	}
	if err := s.startSession(r.Context(), w, s.db, u.ID); err != nil {
		return err
	}
	return ok(w, u)
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("graphly-timing-equalizer"), 12)

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, hashToken(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *User) error {
	ws, err := collect(func(row pgx.Row) (Workspace, error) {
		var w Workspace
		return w, row.Scan(&w.ID, &w.Name, &w.Role)
	})(s.db.Query(r.Context(), `
		SELECT w.id, w.name, m.role FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = $1 ORDER BY m.joined_at`, u.ID))
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"user": u, "workspaces": ws})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, u *User) error {
	var body struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if body.Name != nil {
		name, err := cleanName(*body.Name)
		if err != nil {
			return err
		}
		u.Name = name
	}
	if body.Color != nil {
		valid := false
		for _, c := range userColors {
			valid = valid || c == *body.Color
		}
		if !valid {
			return badRequest("unknown color")
		}
		u.Color = *body.Color
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET name = $2, color = $3 WHERE id = $1`, u.ID, u.Name, u.Color); err != nil {
		return err
	}
	return ok(w, u)
}

// rateLimiter is a fixed-window per-key counter, good enough to blunt password guessing on one instance.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newRateLimiter(limit int, per time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: per, hits: map[string]*window{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.limit
}

// clientIP is the caller's address for rate limiting. Behind a proxy that sets a trusted header
// (Fly.io's Fly-Client-IP, say) every request would otherwise appear to come from the proxy.
func clientIP(r *http.Request, trustedHeader string) string {
	if trustedHeader != "" {
		if v := strings.TrimSpace(r.Header.Get(trustedHeader)); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
