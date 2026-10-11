package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestApp(t, nil)
	_, h, _ := get(t, srv.URL+"/api/auth/providers")
	for k, want := range map[string]string{
		"Content-Security-Policy": "script-src 'self'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"Permissions-Policy":      "camera=()",
	} {
		if !strings.Contains(h.Get(k), want) {
			t.Errorf("%s = %q, want it to contain %q", k, h.Get(k), want)
		}
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent without HTTPS configured")
	}
	secure, _ := newTestApp(t, func(c *Config) { c.HSTSMaxAge = 365 * 24 * 3600 * 1e9 })
	if _, h, _ := get(t, secure.URL+"/healthz"); h.Get("Strict-Transport-Security") != "max-age=31536000; includeSubDomains" {
		t.Errorf("HSTS: %q", h.Get("Strict-Transport-Security"))
	}
}

func TestRateLimits(t *testing.T) {
	srv, _ := newTestApp(t, func(c *Config) {
		c.WriteRateLimit, c.ImportRateLimit, c.WebhookRateLimit, c.PublicRateLimit = 8, 1, 2, 4
	})
	c := newClient(t, srv) // signup is public: 1 of 4
	_, ws := signup(t, c, "Lim", "limits@example.com")

	// Writes per user per minute; reads aren't counted.
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "RL", "name": "Limits"}, &p) // 1
	for i := 0; i < 7; i++ {
		c.must("GET", "/api/projects/"+p.ID, nil, nil)
		c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "x"}, nil) // 2..8
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/projects/"+p.ID+"/issues", strings.NewReader(`{"title":"one too many"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("9th write: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	c.must("GET", "/api/projects/"+p.ID, nil, nil)

	// Unauthenticated endpoints per IP: 3 more after the signup.
	for i := 0; i < 3; i++ {
		if code, _, _ := get(t, srv.URL+"/api/auth/providers"); code != 200 {
			t.Fatalf("providers %d: %d", i, code)
		}
	}
	if code, _, _ := get(t, srv.URL+"/api/auth/providers"); code != http.StatusTooManyRequests {
		t.Fatalf("public limit: %d", code)
	}
}

func TestImportAndWebhookLimits(t *testing.T) {
	srv, _ := newTestApp(t, func(c *Config) { c.ImportRateLimit, c.WebhookRateLimit = 1, 2 })
	c := newClient(t, srv)
	_, ws := signup(t, c, "Imp", "imp-limit@example.com")
	csv := "Issue key,Summary,Issue Type,Status\nAB-1,First,Task,To Do\n"
	body := map[string]any{"source": "jira", "content": csv, "dryRun": true}
	c.must("POST", "/api/workspaces/"+ws.ID+"/import", body, nil)
	if code := c.do("POST", "/api/workspaces/"+ws.ID+"/import", body, nil); code != http.StatusTooManyRequests {
		t.Fatalf("second import within the hour: %d", code)
	}

	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "WH", "name": "Hooks"}, &p)
	var gh struct{ Secret, Path string }
	c.must("POST", "/api/projects/"+p.ID+"/github", map[string]any{}, &gh)
	for i := 0; i < 2; i++ {
		if code := signedPost(t, srv.URL+gh.Path, gh.Secret, "ping", map[string]any{}); code != 200 {
			t.Fatalf("ping %d: %d", i, code)
		}
	}
	// Limited before the signature is even checked, so floods don't reach the database.
	if code := signedPost(t, srv.URL+gh.Path, "wrong", "ping", map[string]any{}); code != http.StatusTooManyRequests {
		t.Fatalf("webhook flood: %d", code)
	}
}

func TestWebhookSecretRotation(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.WebhookSecretGrace = 3600 * 1e9 })
	c := newClient(t, srv)
	_, ws := signup(t, c, "Rot", "rotate@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "RO", "name": "Rotate"}, &p)
	type hook struct {
		Secret, Path       string
		PreviousValidUntil *string
	}
	var first, second hook
	c.must("POST", "/api/projects/"+p.ID+"/github", map[string]any{}, &first)
	if first.PreviousValidUntil != nil {
		t.Fatal("first secret has no predecessor")
	}
	c.must("POST", "/api/projects/"+p.ID+"/github", map[string]any{}, &second)
	if second.PreviousValidUntil == nil {
		t.Fatal("rotation should report the grace period")
	}
	url := srv.URL + first.Path
	for name, secret := range map[string]string{"new": second.Secret, "old (in grace)": first.Secret} {
		if code := signedPost(t, url, secret, "ping", map[string]any{}); code != 200 {
			t.Fatalf("%s secret: %d", name, code)
		}
	}
	var status hook
	c.must("GET", "/api/projects/"+p.ID+"/github", nil, &status)
	if status.PreviousValidUntil == nil {
		t.Fatal("status should show the grace period")
	}
	// Once the grace period is over the old secret stops working.
	app.db.Exec(context.Background(), `UPDATE projects SET github_secret_prev_until = now() - interval '1 second' WHERE id = $1`, p.ID)
	if code := signedPost(t, url, first.Secret, "ping", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("expired old secret: %d", code)
	}
	if code := signedPost(t, url, second.Secret, "ping", map[string]any{}); code != 200 {
		t.Fatalf("current secret: %d", code)
	}

	// GitLab tokens rotate the same way.
	var gl1, gl2 hook
	c.must("POST", "/api/projects/"+p.ID+"/gitlab", map[string]any{}, &gl1)
	c.must("POST", "/api/projects/"+p.ID+"/gitlab", map[string]any{}, &gl2)
	for _, tok := range []string{gl1.Secret, gl2.Secret} {
		if code, _ := gitlabPost(t, srv.URL+gl1.Path, tok, "Note Hook", map[string]any{}); code != 200 {
			t.Fatalf("gitlab token during grace: %d", code)
		}
	}
	// Disabling drops both, grace or not.
	c.must("DELETE", "/api/projects/"+p.ID+"/gitlab", nil, nil)
	if code, _ := gitlabPost(t, srv.URL+gl1.Path, gl1.Secret, "Note Hook", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("after disable: %d", code)
	}

	// With no grace configured, rotation cuts the old secret off at once.
	srv0, _ := newTestApp(t, nil)
	c0 := newClient(t, srv0)
	_, ws0 := signup(t, c0, "Rot", "rotate0@example.com")
	var p0 Project
	c0.must("POST", "/api/workspaces/"+ws0.ID+"/projects", map[string]any{"key": "RZ", "name": "Rotate"}, &p0)
	var a, b hook
	c0.must("POST", "/api/projects/"+p0.ID+"/github", map[string]any{}, &a)
	c0.must("POST", "/api/projects/"+p0.ID+"/github", map[string]any{}, &b)
	if code := signedPost(t, srv0.URL+a.Path, a.Secret, "ping", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("old secret without grace: %d", code)
	}
}

func TestInputCaps(t *testing.T) {
	srv, _ := newTestApp(t, func(c *Config) { c.MaxIssuesPerProject = 3 })
	c := newClient(t, srv)
	_, ws := signup(t, c, "Cap", "caps@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "CP", "name": "Caps"}, &p)
	var is Issue
	for i := 0; i < 3; i++ {
		c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "ok"}, &is)
	}
	if code := c.do("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "over"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("issue cap: %d", code)
	}
	for _, d := range []string{"0001-01-01", "9999-12-31"} {
		if code := c.do("PATCH", "/api/issues/"+is.ID, map[string]any{"dueDate": d}, nil); code != http.StatusBadRequest {
			t.Fatalf("due date %s: %d", d, code)
		}
	}
	// Bodies over 1 MB are refused, not buffered.
	big := bytes.Repeat([]byte("a"), 2<<20)
	payload, _ := json.Marshal(map[string]string{"description": string(big)})
	req, _ := http.NewRequest("PATCH", srv.URL+"/api/issues/"+is.ID, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode < 400 || res.StatusCode >= 500 {
		t.Fatalf("2 MB body: %d", res.StatusCode)
	}
	// A workflow has at most 30 statuses.
	var snap projectSnapshot
	c.must("GET", "/api/projects/"+p.ID, nil, &snap)
	for i := len(snap.Statuses); i < maxStatuses; i++ {
		c.must("POST", "/api/projects/"+p.ID+"/statuses", map[string]any{"name": "Step " + string(rune('A'+i)), "category": "todo"}, nil)
	}
	if code := c.do("POST", "/api/projects/"+p.ID+"/statuses", map[string]any{"name": "One more", "category": "todo"}, nil); code != http.StatusBadRequest {
		t.Fatalf("status cap: %d", code)
	}
}
