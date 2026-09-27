package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests run against a real Postgres. Point TEST_DATABASE_URL at a disposable database —
// its public schema is wiped at the start of the run.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://localhost:5432/graphly_test?sslmode=disable"
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, url)
	if err == nil {
		err = pool.Ping(ctx)
	}
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	t.Cleanup(pool.Close)
	// Cleanups run last-in-first-out: stop the hub (releasing its LISTEN connection) before closing the pool.
	t.Cleanup(cancel)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(pool)
	go hub.Run(ctx)
	srv := httptest.NewServer(NewServer(pool, hub, Config{}).Routes())
	t.Cleanup(srv.Close)
	return srv
}

type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, srv *httptest.Server) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{t: t, base: srv.URL, http: &http.Client{Jar: jar}}
}

func (c *apiClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return res.StatusCode
}

func (c *apiClient) must(method, path string, body any, out any) {
	c.t.Helper()
	if code := c.do(method, path, body, out); code >= 300 {
		c.t.Fatalf("%s %s: status %d", method, path, code)
	}
}

func signup(t *testing.T, c *apiClient, name, email string) (User, Workspace) {
	t.Helper()
	var u User
	c.must("POST", "/api/auth/signup", map[string]string{"name": name, "email": email, "password": "correct horse"}, &u)
	var me struct{ Workspaces []Workspace }
	c.must("GET", "/api/me", nil, &me)
	return u, me.Workspaces[0]
}

func TestEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice Smith", "alice@example.com")

	if code := alice.do("POST", "/api/auth/signup", map[string]string{"name": "A", "email": "ALICE@example.com", "password": "correct horse"}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate email (case-insensitive): got %d", code)
	}

	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "web", "name": "Website", "sample": true}, &p)
	if p.Key != "WEB" {
		t.Fatalf("key not upper-cased: %q", p.Key)
	}
	var snap projectSnapshot
	alice.must("GET", "/api/projects/"+p.ID, nil, &snap)
	if len(snap.Issues) != len(sampleIssues) || len(snap.Statuses) != 5 || len(snap.Links) == 0 {
		t.Fatalf("sample snapshot: %d issues, %d statuses, %d links", len(snap.Issues), len(snap.Statuses), len(snap.Links))
	}

	// Create two issues and link them.
	var a, b Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Design", "estimate": 2}, &a)
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Build", "type": "story", "labels": []string{"Front End", "front end"}}, &b)
	if b.Number != a.Number+1 || len(b.Labels) != 1 || b.Labels[0] != "front-end" {
		t.Fatalf("numbering/labels: %d→%d %v", a.Number, b.Number, b.Labels)
	}
	alice.must("POST", "/api/issues/"+b.ID+"/blockers", map[string]string{"blockerId": a.ID}, nil)
	if code := alice.do("POST", "/api/issues/"+a.ID+"/blockers", map[string]string{"blockerId": b.ID}, nil); code != http.StatusConflict {
		t.Fatalf("cycle not rejected: %d", code)
	}

	// Compare-and-swap on description.
	alice.must("PATCH", "/api/issues/"+a.ID, map[string]any{"description": "v1", "base": map[string]string{"description": ""}}, nil)
	if code := alice.do("PATCH", "/api/issues/"+a.ID, map[string]any{"description": "v2", "base": map[string]string{"description": ""}}, nil); code != http.StatusConflict {
		t.Fatalf("stale description write not rejected: %d", code)
	}

	// Completing the only blocker records an "unblocked" entry on the dependent.
	done := snap.Statuses[4].ID
	alice.must("PATCH", "/api/issues/"+a.ID, map[string]any{"statusId": done}, nil)
	var acts []Activity
	alice.must("GET", "/api/issues/"+b.ID+"/activity", nil, &acts)
	if len(acts) == 0 || acts[0].Kind != "unblocked" {
		t.Fatalf("expected unblocked activity, got %+v", acts)
	}

	// Bob can't see Alice's project until he accepts an invite.
	bob := newClient(t, srv)
	bobUser, _ := signup(t, bob, "Bob", "bob@example.com")
	if code := bob.do("GET", "/api/projects/"+p.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("outsider access: %d", code)
	}
	if code := bob.do("PATCH", "/api/issues/"+a.ID, map[string]any{"title": "pwned"}, nil); code != http.StatusNotFound {
		t.Fatalf("outsider write: %d", code)
	}
	if code := alice.do("PATCH", "/api/issues/"+a.ID, map[string]any{"assigneeId": bobUser.ID}, nil); code != http.StatusBadRequest {
		t.Fatalf("assigning a non-member: %d", code)
	}
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	bob.must("GET", "/api/projects/"+p.ID, nil, nil)
	if code := bob.do("DELETE", "/api/projects/"+p.ID, nil, nil); code != http.StatusForbidden {
		t.Fatalf("member deleting project: %d", code)
	}

	// Realtime: Bob's socket sees Alice's edit.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/projects/" + p.ID + "/live"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: bob.http})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	alice.must("PATCH", "/api/issues/"+b.ID, map[string]any{"title": "Build it"}, nil)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for event: %v", err)
		}
		var msg struct {
			Type  string
			Event struct {
				Type  string
				Issue Issue
			}
		}
		_ = json.Unmarshal(data, &msg)
		if msg.Type == "event" && msg.Event.Type == "issue.upsert" && msg.Event.Issue.Title == "Build it" {
			break
		}
	}

	// Cross-origin writes are refused.
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, _ := alice.http.Do(req)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", res.StatusCode)
	}

	alice.must("POST", "/api/auth/logout", map[string]any{}, nil)
	if code := alice.do("GET", "/api/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
}

// Two people linking the same pair in opposite directions at the same moment must not both
// succeed — that would merge into a cycle, which the old CRDT version could only detect after the fact.
func TestConcurrentOppositeLinks(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Ana", "ana@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "RACE", "name": "Race"}, &p)
	for round := 0; round < 10; round++ {
		var a, b Issue
		c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "A"}, &a)
		c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "B"}, &b)
		codes := make(chan int, 2)
		go func() {
			codes <- c.do("POST", "/api/issues/"+b.ID+"/blockers", map[string]string{"blockerId": a.ID}, nil)
		}()
		go func() {
			codes <- c.do("POST", "/api/issues/"+a.ID+"/blockers", map[string]string{"blockerId": b.ID}, nil)
		}()
		x, y := <-codes, <-codes
		if !((x == 200 && y == 409) || (x == 409 && y == 200)) {
			t.Fatalf("round %d: expected exactly one link to win, got %d and %d", round, x, y)
		}
	}
}

func TestWorkflowAndMembership(t *testing.T) {
	srv := newTestServer(t)
	admin := newClient(t, srv)
	_, ws := signup(t, admin, "Admin", "admin@example.com")
	var p Project
	admin.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "WF", "name": "Workflow"}, &p)
	var snap projectSnapshot
	admin.must("GET", "/api/projects/"+p.ID, nil, &snap)
	backlog, todo := snap.Statuses[0].ID, snap.Statuses[1].ID

	var is Issue
	admin.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Parked", "statusId": backlog}, &is)

	// A column with issues can only be deleted by moving them somewhere.
	if code := admin.do("DELETE", "/api/statuses/"+backlog, nil, nil); code != http.StatusBadRequest {
		t.Fatalf("deleting non-empty status without target: %d", code)
	}
	admin.must("DELETE", "/api/statuses/"+backlog, map[string]string{"moveTo": todo}, nil)
	admin.must("GET", "/api/projects/"+p.ID, nil, &snap)
	if len(snap.Statuses) != 4 || snap.Issues[0].StatusID != todo {
		t.Fatalf("after delete: %d statuses, issue in %s", len(snap.Statuses), snap.Issues[0].StatusID)
	}

	// Reordering renumbers positions densely.
	admin.must("PATCH", "/api/statuses/"+snap.Statuses[3].ID, map[string]any{"position": 0}, nil)
	admin.must("GET", "/api/projects/"+p.ID, nil, &snap)
	for i, st := range snap.Statuses {
		if st.Position != i {
			t.Fatalf("positions not dense: %+v", snap.Statuses)
		}
	}

	// Removing a member unassigns their work; the last admin can't be demoted.
	member := newClient(t, srv)
	mu, _ := signup(t, member, "Member", "member@example.com")
	var inv struct{ Token string }
	admin.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	member.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	admin.must("PATCH", "/api/issues/"+is.ID, map[string]any{"assigneeId": mu.ID}, nil)
	var me User
	admin.must("GET", "/api/me", nil, &struct{ User *User }{&me})
	if code := admin.do("PATCH", "/api/workspaces/"+ws.ID+"/members/"+me.ID, map[string]string{"role": "member"}, nil); code != http.StatusBadRequest {
		t.Fatalf("demoting last admin: %d", code)
	}
	if code := member.do("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, nil); code != http.StatusForbidden {
		t.Fatalf("member creating invite: %d", code)
	}
	admin.must("DELETE", "/api/workspaces/"+ws.ID+"/members/"+mu.ID, nil, nil)
	admin.must("GET", "/api/projects/"+p.ID, nil, &snap)
	if snap.Issues[0].AssigneeID != nil {
		t.Fatal("removed member's issue still assigned")
	}
	if code := member.do("GET", "/api/projects/"+p.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("removed member still has access: %d", code)
	}
}

func TestDemo(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	var res struct {
		User      User
		ProjectID string
	}
	c.must("POST", "/api/auth/demo", map[string]any{}, &res)
	var snap projectSnapshot
	c.must("GET", "/api/projects/"+res.ProjectID, nil, &snap)
	if len(snap.Members) != 1+len(demoTeammates) || len(snap.Issues) != len(sampleIssues) {
		t.Fatalf("demo workspace: %d members, %d issues", len(snap.Members), len(snap.Issues))
	}
	assignees := map[string]bool{}
	for _, i := range snap.Issues {
		if i.AssigneeID != nil {
			assignees[*i.AssigneeID] = true
		}
	}
	if len(assignees) != 3 {
		t.Fatalf("sample work should be spread across the team, got %d assignees", len(assignees))
	}
	// Demo accounts can't be logged into with a password.
	other := newClient(t, srv)
	if code := other.do("POST", "/api/auth/login", map[string]string{"email": res.User.Email, "password": "!"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("demo login: %d", code)
	}
}

func signedPost(t *testing.T, url, secret, event string, payload any) int {
	t.Helper()
	body, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Origin", "https://github.com") // webhooks are exempt from the same-origin check
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestGitHubWebhook(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Dev", "dev@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "GH", "name": "Hooks"}, &p)
	var blocker, waiting Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "API"}, &blocker)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "UI"}, &waiting)
	c.must("POST", "/api/issues/"+waiting.ID+"/blockers", map[string]string{"blockerId": blocker.ID}, nil)

	var gh struct{ Secret, Path string }
	c.must("POST", "/api/projects/"+p.ID+"/github", map[string]any{}, &gh)
	hook := srv.URL + gh.Path
	pr := func(action string, merged bool) map[string]any {
		return map[string]any{
			"action": action,
			"pull_request": map[string]any{
				"number": 7, "title": "Add login endpoint", "body": "Closes gh-1", "html_url": "https://github.com/acme/app/pull/7",
				"merged": merged, "state": map[bool]string{true: "closed", false: "open"}[merged],
				"head": map[string]string{"ref": "feature/GH-1-login"}, "user": map[string]string{"login": "octocat"},
			},
			"repository": map[string]string{"full_name": "acme/app"},
		}
	}

	if code := signedPost(t, hook, "wrong-secret", "pull_request", pr("opened", false)); code != http.StatusUnauthorized {
		t.Fatalf("bad signature accepted: %d", code)
	}
	if code := signedPost(t, hook, gh.Secret, "ping", map[string]any{}); code != 200 {
		t.Fatalf("ping: %d", code)
	}
	status := func() Status {
		var snap projectSnapshot
		c.must("GET", "/api/projects/"+p.ID, nil, &snap)
		for _, i := range snap.Issues {
			if i.ID == blocker.ID {
				for _, st := range snap.Statuses {
					if st.ID == i.StatusID {
						return st
					}
				}
			}
		}
		t.Fatal("issue not found")
		return Status{}
	}

	if code := signedPost(t, hook, gh.Secret, "pull_request", pr("opened", false)); code != 200 {
		t.Fatalf("opened: %d", code)
	}
	if st := status(); st.Name != "In Review" {
		t.Fatalf("opened PR should move to In Review, got %s", st.Name)
	}
	var prs []PullRequest
	c.must("GET", "/api/issues/"+blocker.ID+"/prs", nil, &prs)
	if len(prs) != 1 || prs[0].State != "open" || prs[0].Author != "octocat" {
		t.Fatalf("linked PRs: %+v", prs)
	}

	signedPost(t, hook, gh.Secret, "pull_request", pr("closed", true))
	if st := status(); st.Category != "done" {
		t.Fatalf("merged PR should move to done, got %s", st.Name)
	}
	var acts []Activity
	c.must("GET", "/api/issues/"+waiting.ID+"/activity", nil, &acts)
	if len(acts) == 0 || acts[0].Kind != "unblocked" {
		t.Fatalf("merge should unblock the dependent, got %+v", acts)
	}

	// A late "opened" redelivery must not drag a done issue backwards.
	signedPost(t, hook, gh.Secret, "pull_request", pr("reopened", false))
	if st := status(); st.Category != "done" {
		t.Fatalf("issue moved backwards to %s", st.Name)
	}

	// Snapshots: one per day, latest wins.
	c.must("PUT", "/api/projects/"+p.ID+"/snapshot", map[string]any{"finish": "2030-01-02", "remaining": 5, "openIssues": 3, "blocked": 1}, nil)
	c.must("PUT", "/api/projects/"+p.ID+"/snapshot", map[string]any{"finish": "2030-01-03", "remaining": 6, "openIssues": 3, "blocked": 1}, nil)
	var snaps []snapshot
	c.must("GET", "/api/projects/"+p.ID+"/snapshots", nil, &snaps)
	if len(snaps) != 1 || *snaps[0].Finish != "2030-01-03" {
		t.Fatalf("snapshots: %+v", snaps)
	}
}

func TestIssueNumbers(t *testing.T) {
	got := issueNumbers("APL", "APL-12: fix apl-3 (see APL-12, xAPL-9, APL-0)")
	if len(got) != 2 || got[0] != 12 || got[1] != 3 {
		t.Fatalf("got %v", got)
	}
}
