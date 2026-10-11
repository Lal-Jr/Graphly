package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/Lal-Jr/Graphly/server/forecast"
)

func TestMentions(t *testing.T) {
	members := []Member{
		{User: User{ID: "1", Email: "ana.lopez@example.com", Name: "Ana López"}},
		{User: User{ID: "2", Email: "bo@example.com", Name: "Bo Chen"}},
		{User: User{ID: "3", Email: "bob@example.com", Name: "Bo Diaz"}}, // shares a first name: "@bo" means user 2's email
	}
	h := mentionHandles(members)
	got := mentionedIn("cc @ana.lopez and @BoChen, also @bo. Not me@example.com or @nobody; again @ana.lopez", h)
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("mentions: %v", got)
	}
	if got := mentionedIn("ping @bob!", h); len(got) != 1 || got[0] != "3" {
		t.Fatalf("email handle: %v", got)
	}
	if got := mentionedIn("@ana", h); len(got) != 1 || got[0] != "1" {
		t.Fatalf("unique first name: %v", got)
	}
}

type inbox struct {
	Items  []Notification
	Unread int
}

func TestNotifications(t *testing.T) {
	srv := newTestServer(t)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	bobUser, _ := signup(t, bob, "Bob Stone", "bob@example.com")
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)

	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "NT", "name": "Notify"}, &p)
	var snap projectSnapshot
	alice.must("GET", "/api/projects/"+p.ID, nil, &snap)
	doing, done := snap.Statuses[2].ID, snap.Statuses[4].ID

	var a, b Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "API"}, &a)
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "UI", "assigneeId": bobUser.ID}, &b)
	alice.must("POST", "/api/issues/"+b.ID+"/blockers", map[string]string{"blockerId": a.ID}, nil)

	// Finishing Bob's only blocker tells Bob he's unblocked; Alice hears nothing about her own action.
	alice.must("PATCH", "/api/issues/"+a.ID, map[string]any{"statusId": done}, nil)
	var box inbox
	bob.must("GET", "/api/notifications", nil, &box)
	if box.Unread != 1 || box.Items[0].Kind != kindUnblocked || *box.Items[0].IssueID != b.ID {
		t.Fatalf("bob after unblock: %+v", box)
	}
	alice.must("GET", "/api/notifications", nil, &box)
	if box.Unread != 0 {
		t.Fatalf("alice notified of her own change: %+v", box)
	}

	// A mention notifies and subscribes; Bob (assignee) also watches B, so a status change reaches him.
	var c Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Docs"}, &c)
	alice.must("POST", "/api/issues/"+c.ID+"/comments", map[string]any{"body": "@bob can you review?"}, nil)
	alice.must("PATCH", "/api/issues/"+c.ID, map[string]any{"statusId": doing}, nil)
	bob.must("GET", "/api/notifications", nil, &box)
	kinds := map[string]int{}
	for _, n := range box.Items {
		kinds[n.Kind]++
	}
	if box.Unread != 3 || kinds[kindMention] != 1 || kinds[kindStatus] != 1 {
		t.Fatalf("bob inbox: %v (unread %d)", kinds, box.Unread)
	}
	// Re-saving a description that already mentions him doesn't notify again.
	alice.must("PATCH", "/api/issues/"+c.ID, map[string]any{"description": "Owner: @bob"}, nil)
	alice.must("PATCH", "/api/issues/"+c.ID, map[string]any{"description": "Owner: @bob (draft 2)"}, nil)
	bob.must("GET", "/api/notifications", nil, &box)
	if box.Unread != 4 {
		t.Fatalf("description mentions: unread %d", box.Unread)
	}

	// Mark one read, then all.
	var res struct{ Unread int }
	bob.must("POST", "/api/notifications/read", map[string]any{"ids": []int64{box.Items[0].ID}}, &res)
	if res.Unread != 3 {
		t.Fatalf("after reading one: %d", res.Unread)
	}
	bob.must("POST", "/api/notifications/read", map[string]any{"all": true}, &res)
	if res.Unread != 0 {
		t.Fatalf("after reading all: %d", res.Unread)
	}
	// Notifications are private.
	if code := alice.do("POST", "/api/notifications/read", map[string]any{"ids": []int64{box.Items[0].ID}}, &res); code != 200 || res.Unread != 0 {
		t.Fatalf("alice reading bob's: %d", code)
	}

	// Turning a kind off everywhere stops it.
	bob.must("PUT", "/api/me/notification-settings", map[string]any{"prefs": map[string]any{"status": map[string]bool{"inApp": false, "email": false, "slack": false}}}, nil)
	alice.must("PATCH", "/api/issues/"+c.ID, map[string]any{"statusId": done}, nil)
	bob.must("GET", "/api/notifications/unread", nil, &res)
	if res.Unread != 0 {
		t.Fatalf("muted kind still delivered: %d", res.Unread)
	}
	// Unwatching works too, and Slack webhooks must be real Slack URLs.
	bob.must("DELETE", "/api/issues/"+b.ID+"/watch", nil, nil)
	var wst struct{ Watching bool }
	bob.must("GET", "/api/issues/"+b.ID+"/watch", nil, &wst)
	if wst.Watching {
		t.Fatal("still watching")
	}
	if code := bob.do("PUT", "/api/me/notification-settings", map[string]any{"slackWebhookUrl": "https://169.254.169.254/latest"}, nil); code != http.StatusBadRequest {
		t.Fatalf("non-Slack webhook accepted: %d", code)
	}
	var settings notificationSettings
	bob.must("PUT", "/api/me/notification-settings", map[string]any{"slackWebhookUrl": "https://hooks.slack.com/services/T0/B0/x"}, &settings)
	if !settings.SlackConnected || settings.Prefs[kindStatus].InApp {
		t.Fatalf("settings: %+v", settings)
	}
}

func TestForecastNotifications(t *testing.T) {
	srv, app := newTestApp(t, nil)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	bobUser, _ := signup(t, bob, "Bob", "bob@example.com")
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "LT", "name": "Late"}, &p)

	// Bob owns two issues due in 6 working days, both waiting on a 2-day blocker. On time for now.
	due := forecast.FormatDate(forecast.AddWorkdays(app.today(), 5))
	var blocker, x, y Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Blocker", "estimate": 2}, &blocker)
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "X", "estimate": 1, "dueDate": due, "assigneeId": bobUser.ID}, &x)
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Y", "estimate": 1, "dueDate": due, "assigneeId": bobUser.ID}, &y)
	alice.must("POST", "/api/issues/"+x.ID+"/blockers", map[string]string{"blockerId": blocker.ID}, nil)
	alice.must("POST", "/api/issues/"+y.ID+"/blockers", map[string]string{"blockerId": blocker.ID}, nil)
	ctx := context.Background()
	if err := runForecastJobNow(ctx, app); err != nil {
		t.Fatal(err)
	}
	var box inbox
	bob.must("GET", "/api/notifications", nil, &box)
	if box.Unread != 0 {
		t.Fatalf("first reading should be a silent baseline: %+v", box.Items)
	}

	// The blocker slips by 8 days: both of Bob's issues are now late and the date moves.
	alice.must("PATCH", "/api/issues/"+blocker.ID, map[string]any{"estimate": 10}, nil)
	for range 2 { // a second run changes nothing: no duplicates
		if err := runForecastJobNow(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	bob.must("GET", "/api/notifications", nil, &box)
	kinds := map[string]int{}
	for _, n := range box.Items {
		kinds[n.Kind]++
	}
	if kinds[kindLate] != 1 || kinds[kindDateMoved] != 1 || len(box.Items) != 2 {
		t.Fatalf("bob: %v", kinds)
	}
	for _, n := range box.Items {
		if n.Kind == kindLate && (n.IssueID != nil || !contains(string(n.Data), x.ID) || !contains(string(n.Data), y.ID)) {
			t.Fatalf("one late notice should cover both issues: %s", n.Data)
		}
	}
	// Alice is a workspace admin, so she hears about the date too (but owns nothing late).
	alice.must("GET", "/api/notifications", nil, &box)
	if len(box.Items) != 1 || box.Items[0].Kind != kindDateMoved {
		t.Fatalf("alice: %+v", box.Items)
	}

	// A one-day wobble back is under the threshold: no new alert.
	alice.must("PATCH", "/api/issues/"+blocker.ID, map[string]any{"estimate": 9}, nil)
	if err := runForecastJobNow(ctx, app); err != nil {
		t.Fatal(err)
	}
	alice.must("GET", "/api/notifications", nil, &box)
	if len(box.Items) != 1 {
		t.Fatalf("small move notified: %d", len(box.Items))
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
