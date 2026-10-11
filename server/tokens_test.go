package main

import (
	"bufio"
	"bytes"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// bearerClient calls the API with a token and no cookies, like a script would.
type bearerClient struct {
	t     *testing.T
	base  string
	token string
}

func (c bearerClient) do(method, path string, body any, out any) (int, http.Header) {
	c.t.Helper()
	var hdr http.Header
	a := &apiClient{t: c.t, base: c.base, http: &http.Client{Transport: headerTransport{"Authorization", "Bearer " + c.token, &hdr}}}
	return a.do(method, path, body, out), hdr
}

type headerTransport struct {
	key, value string
	got        *http.Header
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(h.key, h.value)
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && h.got != nil {
		*h.got = res.Header
	}
	return res, err
}

func TestAPITokens(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.APITokenRateLimit = 30 })
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "API", "name": "Api", "sample": true}, &p)

	var created struct {
		Token string
		Info  apiToken
	}
	alice.must("POST", "/api/me/tokens", map[string]any{"name": "CI script", "scope": "write"}, &created)
	if !strings.HasPrefix(created.Token, "gly_") || created.Info.Prefix != created.Token[:12] {
		t.Fatalf("created: %+v", created)
	}
	var list []apiToken
	alice.must("GET", "/api/me/tokens", nil, &list)
	if len(list) != 1 || list[0].Name != "CI script" || list[0].LastUsedAt != nil {
		t.Fatalf("list: %+v", list)
	}
	var stored []byte
	app.db.QueryRow(t.Context(), `SELECT token_hash FROM api_tokens`).Scan(&stored)
	if bytes.Contains(stored, []byte(created.Token)) || len(stored) != 32 {
		t.Fatal("token not stored hashed")
	}

	bot := bearerClient{t, srv.URL, created.Token}
	var page issuePage
	if code, hdr := bot.do("GET", "/api/projects/"+p.ID+"/issues?category=todo&limit=5", nil, &page); code != 200 || hdr.Get("X-RateLimit-Limit") != "30" {
		t.Fatalf("list issues: %d %v", code, hdr)
	}
	if len(page.Issues) != 5 || page.Next == nil {
		t.Fatalf("page: %d issues, next %v", len(page.Issues), page.Next)
	}
	var next issuePage
	bot.do("GET", "/api/projects/"+p.ID+"/issues?category=todo&limit=5&after="+itoa(*page.Next), nil, &next)
	if len(next.Issues) == 0 || next.Issues[0].Number <= page.Issues[4].Number {
		t.Fatalf("second page: %+v", next.Issues)
	}
	var is Issue
	if code, _ := bot.do("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "From CI"}, &is); code != 200 {
		t.Fatalf("create with write token: %d", code)
	}
	var byNum Issue
	bot.do("GET", "/api/projects/"+p.ID+"/issues/"+itoa(is.Number), nil, &byNum)
	if byNum.ID != is.ID {
		t.Fatal("lookup by number")
	}
	var links []Link
	var fc ProjectForecast
	if code, _ := bot.do("GET", "/api/projects/"+p.ID+"/links", nil, &links); code != 200 || len(links) == 0 {
		t.Fatalf("links: %d %d", code, len(links))
	}
	if code, _ := bot.do("GET", "/api/projects/"+p.ID+"/forecast", nil, &fc); code != 200 || fc.Finish == nil {
		t.Fatalf("forecast: %d", code)
	}
	alice.must("GET", "/api/me/tokens", nil, &list)
	if list[0].LastUsedAt == nil {
		t.Fatal("last used not recorded")
	}

	// Session-only endpoints refuse tokens, so a leaked token can't mint more or take over the account.
	for _, path := range []string{"/api/me/tokens", "/api/me/identities", "/api/notifications"} {
		if code, _ := bot.do("GET", path, nil, nil); code != 403 {
			t.Fatalf("token on %s: %d", path, code)
		}
	}
	if code, _ := bot.do("PUT", "/api/me/password", map[string]string{"password": "takeover!!"}, nil); code != 403 {
		t.Fatalf("token changed password: %d", code)
	}

	// Read tokens can't write.
	var ro struct{ Token string }
	alice.must("POST", "/api/me/tokens", map[string]any{"name": "dashboard", "scope": "read", "expiresInDays": 30}, &ro)
	reader := bearerClient{t, srv.URL, ro.Token}
	if code, _ := reader.do("PATCH", "/api/issues/"+is.ID, map[string]any{"title": "x"}, nil); code != 403 {
		t.Fatalf("read token wrote: %d", code)
	}
	if code, _ := reader.do("GET", "/api/issues/"+is.ID, nil, nil); code != 200 {
		t.Fatalf("read token read: %d", code)
	}

	// Per-token rate limit, independent between tokens.
	limited := 0
	var retry string
	for i := 0; i < 40; i++ {
		code, hdr := bot.do("GET", "/api/me", nil, nil)
		if code == 429 {
			limited++
			retry = hdr.Get("Retry-After")
		}
	}
	if limited == 0 || retry == "" {
		t.Fatalf("no 429 after 40 requests (limit 30): %d", limited)
	}
	if code, _ := reader.do("GET", "/api/me", nil, nil); code != 200 {
		t.Fatalf("other token throttled too: %d", code)
	}

	// Revoked and bogus tokens are rejected.
	alice.must("DELETE", "/api/me/tokens/"+created.Info.ID, nil, nil)
	if code, _ := bot.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
	if code, _ := (bearerClient{t, srv.URL, "gly_nope"}).do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("bogus token: %d", code)
	}
	// Other people can't see or revoke your tokens.
	bob := newClient(t, srv)
	signup(t, bob, "Bob", "bob@example.com")
	alice.must("GET", "/api/me/tokens", nil, &list)
	if code := bob.do("DELETE", "/api/me/tokens/"+list[0].ID, nil, nil); code != 404 {
		t.Fatalf("bob revoked alice's token: %d", code)
	}
}

// The spec documents exactly the routes tokens can call.
func TestOpenAPICoversPublicRoutes(t *testing.T) {
	app := &Server{cfg: Config{}}
	app.Routes()
	var documented []string
	path := ""
	pathRe := regexp.MustCompile(`^  (/api/\S+):\s*$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):`)
	sc := bufio.NewScanner(bytes.NewReader(openAPIYAML))
	for sc.Scan() {
		if m := pathRe.FindStringSubmatch(sc.Text()); m != nil {
			path = m[1]
		} else if m := opRe.FindStringSubmatch(sc.Text()); m != nil && path != "" {
			documented = append(documented, strings.ToUpper(m[1])+" "+path)
		} else if strings.HasPrefix(sc.Text(), "components:") {
			path = ""
		}
	}
	public := slices.Clone(app.publicRoutes)
	slices.Sort(public)
	slices.Sort(documented)
	if !slices.Equal(public, documented) {
		for _, r := range public {
			if !slices.Contains(documented, r) {
				t.Errorf("undocumented public route: %s", r)
			}
		}
		for _, r := range documented {
			if !slices.Contains(public, r) {
				t.Errorf("documented route tokens can't call: %s", r)
			}
		}
	}
	if len(public) < 15 {
		t.Fatalf("only %d public routes", len(public))
	}
}
