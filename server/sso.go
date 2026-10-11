package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sign in with Google or GitHub (OAuth 2.0 authorization code flow with PKCE and a state cookie).
//
// On callback, in order:
//  1. A known identity (provider + subject) signs in to its user.
//  2. Otherwise, a verified provider email that matches an existing account links to it. If that
//     account's email was never verified, its password and sessions are cleared first: whoever set
//     that password never proved they own the address, so they must not keep a way in.
//  3. Otherwise a new account (and workspace) is created.
//
// A signed-in user can also link a provider from their profile, and unlink it as long as some
// other way to sign in remains. SAML is not supported (see the README).

const (
	oauthCookie = "graphly_oauth"
	oauthTTL    = 10 * time.Minute
)

type oauthProvider struct {
	Name, Label                string
	ClientID, ClientSecret     string
	AuthURL, TokenURL, UserURL string
	EmailsURL                  string // GitHub only
	Scopes                     []string
}

type providerUser struct {
	Subject, Email, Name string
	EmailVerified        bool
}

func oauthProviders(cfg Config) map[string]*oauthProvider {
	out := map[string]*oauthProvider{}
	if cfg.GoogleClientID != "" {
		out["google"] = &oauthProvider{Name: "google", Label: "Google", ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret,
			AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
			UserURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "email", "profile"}}
	}
	if cfg.GitHubClientID != "" {
		out["github"] = &oauthProvider{Name: "github", Label: "GitHub", ClientID: cfg.GitHubClientID, ClientSecret: cfg.GitHubClientSecret,
			AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token",
			UserURL: "https://api.github.com/user", EmailsURL: "https://api.github.com/user/emails", Scopes: []string{"read:user", "user:email"}}
	}
	return out
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) error {
	type p struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	out := []p{}
	for _, id := range []string{"google", "github"} {
		if pr := s.providers[id]; pr != nil {
			out = append(out, p{id, pr.Label})
		}
	}
	return ok(w, out)
}

func (s *Server) redirectURI(provider string) string {
	return s.cfg.PublicURL + "/api/auth/oauth/" + provider + "/callback"
}

// safeNext keeps post-login redirects on this site.
func safeNext(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") || strings.ContainsAny(v, "\r\n") {
		return "/"
	}
	return v
}

type oauthState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
	Link     bool   `json:"l"`
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) error {
	p := s.providers[r.PathValue("provider")]
	if p == nil {
		return errNotFound
	}
	if s.cfg.PublicURL == "" {
		return errStatus(http.StatusServiceUnavailable, "PUBLIC_URL must be set to use single sign-on")
	}
	st := oauthState{State: randomToken(24), Verifier: randomToken(48), Next: safeNext(r.URL.Query().Get("next")), Link: r.URL.Query().Get("link") == "1"}
	if st.Link {
		if _, err := s.currentUser(r); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/api/auth/oauth/",
		MaxAge: int(oauthTTL.Seconds()), HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	q := url.Values{
		"client_id": {p.ClientID}, "redirect_uri": {s.redirectURI(p.Name)}, "response_type": {"code"},
		"scope": {strings.Join(p.Scopes, " ")}, "state": {st.State},
		"code_challenge": {pkceChallenge(st.Verifier)}, "code_challenge_method": {"S256"},
	}
	if p.Name == "google" {
		q.Set("prompt", "select_account")
	}
	http.Redirect(w, r, p.AuthURL+"?"+q.Encode(), http.StatusFound)
	return nil
}

// oauthCallback never returns JSON errors: it's a browser navigation, so failures go back to the
// login page with a message.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) error {
	fail := func(msg string, err error) error {
		if err != nil {
			slog.Warn("oauth", "provider", r.PathValue("provider"), "err", err)
		}
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
		return nil
	}
	p := s.providers[r.PathValue("provider")]
	if p == nil {
		return errNotFound
	}
	c, err := r.Cookie(oauthCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/api/auth/oauth/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	if err != nil {
		return fail("Sign-in expired. Please try again.", nil)
	}
	var st oauthState
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.State == "" {
		return fail("Sign-in expired. Please try again.", nil)
	}
	if subtle.ConstantTimeCompare([]byte(st.State), []byte(r.URL.Query().Get("state"))) != 1 {
		return fail("Sign-in couldn't be verified. Please try again.", nil)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return fail(p.Label+" sign-in was cancelled.", nil)
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return fail("Sign-in failed. Please try again.", nil)
	}
	ctx := r.Context()
	token, err := s.exchangeCode(ctx, p, code, st.Verifier)
	if err != nil {
		return fail("Couldn't reach "+p.Label+". Please try again.", err)
	}
	pu, err := s.fetchProviderUser(ctx, p, token)
	if err != nil {
		return fail("Couldn't read your "+p.Label+" profile.", err)
	}
	if pu.Subject == "" || pu.Email == "" {
		return fail("Your "+p.Label+" account has no email address we can use.", nil)
	}

	var current *User
	if st.Link {
		if current, err = s.currentUser(r); err != nil {
			return fail("Log in first, then connect "+p.Label+".", nil)
		}
	}
	var userID string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		userID, err = s.linkIdentity(ctx, tx, p, pu, current)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_identities SET last_login = now(), email = $3 WHERE provider = $1 AND subject = $2`, p.Name, pu.Subject, pu.Email); err != nil {
			return err
		}
		if current != nil {
			return nil
		}
		return s.startSession(ctx, w, tx, userID)
	})
	var ae *apiError
	if errors.As(err, &ae) {
		return fail(ae.Msg, nil)
	}
	if err != nil {
		return fail("Sign-in failed. Please try again.", err)
	}
	next := st.Next
	if current != nil {
		next = "/profile?connected=" + p.Name + "#sign-in"
	}
	http.Redirect(w, r, next, http.StatusFound)
	return nil
}

func (s *Server) linkIdentity(ctx context.Context, tx pgx.Tx, p *oauthProvider, pu providerUser, current *User) (string, error) {
	var owner string
	err := tx.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider = $1 AND subject = $2`, p.Name, pu.Subject).Scan(&owner)
	switch {
	case err == nil && current != nil && owner != current.ID:
		return "", errStatus(http.StatusConflict, "That "+p.Label+" account is already connected to another Graphly account.")
	case err == nil:
		return owner, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	attach := func(userID string) error {
		_, err := tx.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`, p.Name, pu.Subject, userID, pu.Email)
		if isUnique(err) {
			return errStatus(http.StatusConflict, "This account already has a different "+p.Label+" account connected.")
		}
		return err
	}
	if current != nil {
		return current.ID, attach(current.ID)
	}
	if !pu.EmailVerified {
		return "", errStatus(http.StatusForbidden, "Verify your email with "+p.Label+" first, then try again.")
	}
	var userID string
	var verified, demo bool
	err = tx.QueryRow(ctx, `SELECT id, email_verified, is_demo FROM users WHERE lower(email) = lower($1) FOR UPDATE`, pu.Email).Scan(&userID, &verified, &demo)
	switch {
	case err == nil && demo:
		return "", errStatus(http.StatusConflict, "That email belongs to a demo account.")
	case err == nil:
		if !verified {
			// The password was set by someone who never proved they own this address.
			if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = NULL WHERE id = $1`, userID); err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
				return "", err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET email_verified = true WHERE id = $1`, userID); err != nil {
			return "", err
		}
		return userID, attach(userID)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	name := strings.TrimSpace(pu.Name)
	if name == "" {
		name = strings.Split(pu.Email, "@")[0]
	}
	name, _ = cleanName(clip(name, 80))
	color := userColors[time.Now().UnixNano()%int64(len(userColors))]
	if err := tx.QueryRow(ctx, `INSERT INTO users (email, name, password_hash, color, email_verified) VALUES ($1, $2, NULL, $3, true) RETURNING id`,
		pu.Email, name, color).Scan(&userID); err != nil {
		return "", err
	}
	if _, err := createWorkspaceTx(ctx, tx, strings.Fields(name)[0]+"'s workspace", userID); err != nil {
		return "", err
	}
	return userID, attach(userID)
}

func (s *Server) exchangeCode(ctx context.Context, p *oauthProvider, code, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.redirectURI(p.Name)},
		"client_id": {p.ClientID}, "client_secret": {p.ClientSecret}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := s.getJSON(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token exchange: %s", out.Error)
	}
	return out.AccessToken, nil
}

func (s *Server) getJSON(req *http.Request, v any) error {
	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", req.Method, req.URL.Host, res.Status)
	}
	return json.Unmarshal(body, v)
}

func (s *Server) fetchProviderUser(ctx context.Context, p *oauthProvider, token string) (providerUser, error) {
	get := func(u string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return s.getJSON(req, v)
	}
	var pu providerUser
	switch p.Name {
	case "google":
		var g struct {
			Sub, Email, Name string
			EmailVerified    any `json:"email_verified"`
		}
		if err := get(p.UserURL, &g); err != nil {
			return pu, err
		}
		verified := g.EmailVerified == true || g.EmailVerified == "true"
		return providerUser{Subject: g.Sub, Email: g.Email, Name: g.Name, EmailVerified: verified}, nil
	case "github":
		var gh struct {
			ID          int64 `json:"id"`
			Login, Name string
		}
		if err := get(p.UserURL, &gh); err != nil {
			return pu, err
		}
		var emails []struct {
			Email             string
			Primary, Verified bool
		}
		if err := get(p.EmailsURL, &emails); err != nil {
			return pu, err
		}
		pu = providerUser{Subject: strconv.FormatInt(gh.ID, 10), Name: firstNonEmpty(gh.Name, gh.Login)}
		for _, e := range emails {
			if e.Primary {
				pu.Email, pu.EmailVerified = e.Email, e.Verified
			}
		}
		if gh.ID == 0 {
			pu.Subject = ""
		}
		return pu, nil
	}
	return pu, errors.New("unknown provider")
}

type identity struct {
	Provider  string     `json:"provider"`
	Email     string     `json:"email"`
	CreatedAt time.Time  `json:"createdAt"`
	LastLogin *time.Time `json:"lastLogin"`
}

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request, u *User) error {
	ids, err := collect(func(row pgx.Row) (identity, error) {
		var i identity
		return i, row.Scan(&i.Provider, &i.Email, &i.CreatedAt, &i.LastLogin)
	})(s.db.Query(r.Context(), `SELECT provider, email, created_at, last_login FROM user_identities WHERE user_id = $1 ORDER BY provider`, u.ID))
	if err != nil {
		return err
	}
	var hasPassword bool
	if err := s.db.QueryRow(r.Context(), `SELECT password_hash IS NOT NULL FROM users WHERE id = $1`, u.ID).Scan(&hasPassword); err != nil {
		return err
	}
	return ok(w, map[string]any{"identities": ids, "hasPassword": hasPassword})
}

func (s *Server) unlinkIdentity(w http.ResponseWriter, r *http.Request, u *User) error {
	provider := r.PathValue("provider")
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		var others int
		if err := tx.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM user_identities WHERE user_id = $1 AND provider <> $2)
			+ (SELECT count(*) FROM users WHERE id = $1 AND password_hash IS NOT NULL) FROM users WHERE id = $1 FOR UPDATE`, u.ID, provider).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return badRequest("set a password or connect another sign-in method first")
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM user_identities WHERE user_id = $1 AND provider = $2`, u.ID, provider)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return ok(w, map[string]bool{"ok": true})
	})
}
