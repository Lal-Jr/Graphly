package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func joinAs(t *testing.T, admin, c *apiClient, wsID, role string) {
	t.Helper()
	var inv struct{ Token, Role string }
	admin.must("POST", "/api/workspaces/"+wsID+"/invites", map[string]any{"role": role}, &inv)
	if inv.Role != role {
		t.Fatalf("invite role %q", inv.Role)
	}
	c.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
}

func TestRoles(t *testing.T) {
	srv, app := newTestApp(t, nil)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	bobUser, _ := signup(t, bob, "Bob", "bob@example.com")
	vic := newClient(t, srv)
	vicUser, _ := signup(t, vic, "Vic", "vic@example.com")
	joinAs(t, alice, bob, ws.ID, "member")
	joinAs(t, alice, vic, ws.ID, "viewer")

	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "RL", "name": "Roles", "sample": true}, &p)
	var snap projectSnapshot
	vic.must("GET", "/api/projects/"+p.ID, nil, &snap)
	if snap.Role != "viewer" || len(snap.Issues) == 0 {
		t.Fatalf("viewer snapshot: role %q, %d issues", snap.Role, len(snap.Issues))
	}
	issue := snap.Issues[1]

	// Viewers read, watch and see forecasts, but can't change anything.
	expect := func(c *apiClient, method, path string, body any, want int) {
		t.Helper()
		if code := c.do(method, path, body, nil); code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, code, want)
		}
	}
	expect(vic, "GET", "/api/projects/"+p.ID+"/forecast", nil, 200)
	expect(vic, "GET", "/api/issues/"+issue.ID+"/comments", nil, 200)
	expect(vic, "PUT", "/api/issues/"+issue.ID+"/watch", map[string]any{}, 200)
	expect(vic, "POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "nope"}, 403)
	expect(vic, "PATCH", "/api/issues/"+issue.ID, map[string]any{"title": "nope"}, 403)
	expect(vic, "DELETE", "/api/issues/"+issue.ID, nil, 403)
	expect(vic, "POST", "/api/issues/"+issue.ID+"/comments", map[string]any{"body": "hi"}, 403)
	expect(vic, "POST", "/api/issues/"+issue.ID+"/blockers", map[string]any{"blockerId": snap.Issues[2].ID}, 403)
	expect(vic, "POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "VV", "name": "V"}, 403)

	// Members edit issues but not the project's settings, workflow or integrations.
	expect(bob, "PATCH", "/api/issues/"+issue.ID, map[string]any{"title": "Edited by Bob"}, 200)
	expect(bob, "PATCH", "/api/projects/"+p.ID, map[string]any{"name": "Mine now"}, 403)
	expect(bob, "POST", "/api/projects/"+p.ID+"/statuses", map[string]any{"name": "QA", "category": "in_progress"}, 403)
	expect(bob, "PATCH", "/api/statuses/"+snap.Statuses[0].ID, map[string]any{"name": "Icebox"}, 403)
	expect(bob, "POST", "/api/projects/"+p.ID+"/github", map[string]any{}, 403)
	expect(bob, "DELETE", "/api/projects/"+p.ID, nil, 403)
	expect(bob, "GET", "/api/workspaces/"+ws.ID+"/audit", nil, 403)
	expect(bob, "PUT", "/api/projects/"+p.ID+"/members/"+vicUser.ID, map[string]any{"role": "editor"}, 403)
	// Viewers can't be assigned work.
	expect(alice, "PATCH", "/api/issues/"+issue.ID, map[string]any{"assigneeId": vicUser.ID}, 400)

	// Project overrides: Vic edits this project; Bob is read-only in it.
	alice.must("PUT", "/api/projects/"+p.ID+"/members/"+vicUser.ID, map[string]any{"role": "editor"}, nil)
	alice.must("PUT", "/api/projects/"+p.ID+"/members/"+bobUser.ID, map[string]any{"role": "viewer"}, nil)
	expect(vic, "POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Vic's first"}, 200)
	expect(bob, "PATCH", "/api/issues/"+issue.ID, map[string]any{"title": "again"}, 403)
	var pm []projectMember
	bob.must("GET", "/api/projects/"+p.ID+"/members", nil, &pm)
	roles := map[string]string{}
	for _, m := range pm {
		roles[m.UserID] = m.Role
	}
	if roles[vicUser.ID] != "editor" || roles[bobUser.ID] != "viewer" {
		t.Fatalf("project roles: %v", roles)
	}
	// Making Bob a project admin lets him manage the workflow there.
	alice.must("PUT", "/api/projects/"+p.ID+"/members/"+bobUser.ID, map[string]any{"role": "admin"}, nil)
	expect(bob, "POST", "/api/projects/"+p.ID+"/statuses", map[string]any{"name": "QA", "category": "in_progress"}, 200)
	alice.must("PUT", "/api/projects/"+p.ID+"/members/"+bobUser.ID, map[string]any{"role": nil}, nil)
	expect(bob, "POST", "/api/projects/"+p.ID+"/statuses", map[string]any{"name": "QA2", "category": "in_progress"}, 403)

	// Workspace roles accept only known values.
	alice.must("PATCH", "/api/workspaces/"+ws.ID+"/members/"+vicUser.ID, map[string]any{"role": "member"}, nil)
	expect(alice, "PATCH", "/api/workspaces/"+ws.ID+"/members/"+vicUser.ID, map[string]any{"role": "owner"}, 400)

	// The audit log saw all of it, and can't be rewritten.
	var bt struct{ Token string }
	bob.must("POST", "/api/me/tokens", map[string]any{"name": "bob's script"}, &bt)
	alice.must("DELETE", "/api/issues/"+snap.Issues[3].ID, nil, nil)
	var log []auditEntry
	alice.must("GET", "/api/workspaces/"+ws.ID+"/audit", nil, &log)
	seen := map[string]int{}
	for _, e := range log {
		seen[e.Action]++
	}
	for _, a := range []string{"invite.create", "member.join", "project.member.role", "member.role", "token.create", "issue.delete"} {
		if seen[a] == 0 {
			t.Errorf("audit log has no %s: %v", a, seen)
		}
	}
	if seen["invite.create"] != 2 || seen["project.member.role"] != 4 {
		t.Errorf("audit counts: %v", seen)
	}
	ctx := context.Background()
	if _, err := app.db.Exec(ctx, `UPDATE audit_log SET action = 'nothing'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit_log update allowed: %v", err)
	}
	if _, err := app.db.Exec(ctx, `DELETE FROM audit_log`); err == nil {
		t.Fatal("audit_log delete allowed")
	}
}

// Removing someone closes their open WebSocket instead of letting it keep streaming.
func TestRemovedMemberIsDisconnected(t *testing.T) {
	srv, _ := newTestApp(t, nil)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	bobUser, _ := signup(t, bob, "Bob", "bob@example.com")
	joinAs(t, alice, bob, ws.ID, "member")
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "WS", "name": "Socket"}, &p)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/projects/" + p.ID + "/live"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: bob.http})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil { // initial presence
		t.Fatal(err)
	}
	start := time.Now()
	alice.must("DELETE", "/api/workspaces/"+ws.ID+"/members/"+bobUser.ID, nil, nil)
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue // the access.changed event itself, or presence
		}
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("closed with %v", err)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Fatalf("took %v to disconnect", time.Since(start))
		}
		return
	}
}
