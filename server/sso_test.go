package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeIdP plays Google (OIDC userinfo) and GitHub (user + emails) closely enough to drive the
// whole redirect dance: /authorize consents at once and bounces back with a code.
type fakeIdP struct {
	mu         sync.Mutex
	challenges map[string]string // code → PKCE challenge
	user       map[string]any    // what the userinfo endpoint returns
	emails     []map[string]any  // GitHub /user/emails
}

func (f *fakeIdP) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := randomToken(8)
		f.mu.Lock()
		f.challenges[code] = q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		want := f.challenges[r.Form.Get("code")]
		delete(f.challenges, r.Form.Get("code"))
		f.mu.Unlock()
		if want == "" || pkceChallenge(r.Form.Get("code_verifier")) != want || r.Form.Get("client_secret") != "secret" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.user)
	})
	mux.HandleFunc("/emails", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.emails)
	})
	return mux
}

// browse follows redirects like a browser and returns where it ended up.
func (c *apiClient) browse(path string) *url.URL {
	c.t.Helper()
	res, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	res.Body.Close()
	return res.Request.URL
}

func TestSSO(t *testing.T) {
	srv, app := newTestApp(t, nil)
	idp := &fakeIdP{challenges: map[string]string{}}
	idpSrv := httptest.NewServer(idp.handler())
	t.Cleanup(idpSrv.Close)
	app.cfg.PublicURL = srv.URL
	app.providers = map[string]*oauthProvider{
		"google": {Name: "google", Label: "Google", ClientID: "id", ClientSecret: "secret", AuthURL: idpSrv.URL + "/authorize", TokenURL: idpSrv.URL + "/token", UserURL: idpSrv.URL + "/user"},
		"github": {Name: "github", Label: "GitHub", ClientID: "id", ClientSecret: "secret", AuthURL: idpSrv.URL + "/authorize", TokenURL: idpSrv.URL + "/token", UserURL: idpSrv.URL + "/user", EmailsURL: idpSrv.URL + "/emails"},
	}
	ctx := context.Background()

	var providers []map[string]string
	newClient(t, srv).must("GET", "/api/auth/providers", nil, &providers)
	if len(providers) != 2 || providers[0]["id"] != "google" {
		t.Fatalf("providers: %v", providers)
	}

	// A new Google user gets an account and lands where they were going.
	idp.user = map[string]any{"sub": "g-1", "email": "dana@example.com", "email_verified": true, "name": "Dana Scully"}
	dana := newClient(t, srv)
	if u := dana.browse("/api/auth/oauth/google/start?next=/p/x/board"); u.Path != "/p/x/board" {
		t.Fatalf("landed on %v", u)
	}
	var me struct {
		User       User
		Workspaces []Workspace
	}
	dana.must("GET", "/api/me", nil, &me)
	if me.User.Email != "dana@example.com" || me.User.Name != "Dana Scully" || len(me.Workspaces) != 1 {
		t.Fatalf("me: %+v", me)
	}
	// Signing in again finds the same account.
	again := newClient(t, srv)
	again.browse("/api/auth/oauth/google/start")
	var me2 struct{ User User }
	again.must("GET", "/api/me", nil, &me2)
	if me2.User.ID != me.User.ID {
		t.Fatal("second sign-in made a new account")
	}
	// No password yet, so the identity can't be removed; setting one makes it removable.
	if code := dana.do("DELETE", "/api/me/identities/google", nil, nil); code != 400 {
		t.Fatalf("unlinked the only sign-in method: %d", code)
	}
	dana.must("PUT", "/api/me/password", map[string]string{"password": "a long password"}, nil)
	dana.must("DELETE", "/api/me/identities/google", nil, nil)

	// A pre-registered password account on the same address is linked, and since that password
	// was never proven to belong to the address owner, it stops working and its sessions end.
	squatter := newClient(t, srv)
	signup(t, squatter, "Squatter", "erin@example.com")
	idp.user = map[string]any{"id": 77, "login": "erin", "name": ""}
	idp.emails = []map[string]any{{"email": "old@example.com", "primary": false, "verified": true}, {"email": "erin@example.com", "primary": true, "verified": true}}
	erin := newClient(t, srv)
	erin.browse("/api/auth/oauth/github/start")
	var meE struct{ User User }
	erin.must("GET", "/api/me", nil, &meE)
	if meE.User.Email != "erin@example.com" {
		t.Fatalf("erin: %+v", meE)
	}
	if code := squatter.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("squatter's session survived: %d", code)
	}
	if code := newClient(t, srv).do("POST", "/api/auth/login", map[string]string{"email": "erin@example.com", "password": "password123"}, nil); code != 401 {
		t.Fatalf("squatter's password still works: %d", code)
	}
	var verified bool
	app.db.QueryRow(ctx, `SELECT email_verified FROM users WHERE email = 'erin@example.com'`).Scan(&verified)
	if !verified {
		t.Fatal("email not marked verified")
	}

	// An unverified provider email never creates or links anything.
	idp.user = map[string]any{"id": 78, "login": "frank"}
	idp.emails = []map[string]any{{"email": "frank@example.com", "primary": true, "verified": false}}
	if u := newClient(t, srv).browse("/api/auth/oauth/github/start"); u.Path != "/login" || !strings.Contains(u.Query().Get("error"), "Verify your email") {
		t.Fatalf("unverified email: %v", u)
	}

	// Linking from the profile attaches to the signed-in user, even with a different email.
	gina := newClient(t, srv)
	ginaUser, _ := signup(t, gina, "Gina", "gina@example.com")
	idp.user = map[string]any{"sub": "g-9", "email": "gina.personal@example.com", "email_verified": true}
	if u := gina.browse("/api/auth/oauth/google/start?link=1"); u.Path != "/profile" {
		t.Fatalf("link landed on %v", u)
	}
	var ids struct {
		Identities  []identity
		HasPassword bool
	}
	gina.must("GET", "/api/me/identities", nil, &ids)
	if len(ids.Identities) != 1 || ids.Identities[0].Email != "gina.personal@example.com" || !ids.HasPassword {
		t.Fatalf("identities: %+v", ids)
	}
	// …and that Google account now signs in as Gina; it can't be linked to anyone else.
	other := newClient(t, srv)
	other.browse("/api/auth/oauth/google/start")
	var meG struct{ User User }
	other.must("GET", "/api/me", nil, &meG)
	if meG.User.ID != ginaUser.ID {
		t.Fatal("linked identity signed in as someone else")
	}
	hal := newClient(t, srv)
	signup(t, hal, "Hal", "hal@example.com")
	if u := hal.browse("/api/auth/oauth/google/start?link=1"); !strings.Contains(u.Query().Get("error"), "already connected") {
		t.Fatalf("stole a linked identity: %v", u)
	}

	// A forged or replayed state is rejected.
	mallory := newClient(t, srv)
	if u := mallory.browse("/api/auth/oauth/google/callback?code=x&state=y"); !strings.Contains(u.Query().Get("error"), "expired") {
		t.Fatalf("callback without cookie: %v", u)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/p/1/board?x=1": "/p/1/board?x=1", "//evil.com": "/", "https://evil.com": "/", "/\\evil.com": "/", "": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q", in, got)
		}
	}
}
