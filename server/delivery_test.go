package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type sentMail struct{ to, subject, body string }

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
	fail int // fail this many sends first
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail > 0 {
		m.fail--
		return errors.New("421 try again later")
	}
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

// slackStub answers webhook posts without the network.
type slackStub struct {
	mu     sync.Mutex
	posts  []string
	status int
}

func (s *slackStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	s.posts = append(s.posts, r.URL.Host+" "+string(b))
	code := s.status
	if code == 0 {
		code = 200
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
}

func TestDeliveryDigestsAndRetries(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.PublicURL = "https://graphly.test"; c.DeliveryMaxAttempts = 3 })
	mail := &fakeMailer{}
	app.mailer = mail
	slack := &slackStub{}
	app.httpClient = &http.Client{Transport: slack}
	ctx := context.Background()

	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	signup(t, bob, "Bob", "bob@example.com")
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "DL", "name": "Deliver"}, &p)

	// Webhook validation: only the allowed host, over https.
	if code := bob.do("PUT", "/api/me/notification-settings", map[string]any{"slackWebhookUrl": "https://evil.example/hook"}, nil); code != 400 {
		t.Fatalf("foreign webhook accepted: %d", code)
	}
	bob.must("PUT", "/api/me/notification-settings", map[string]any{"slackWebhookUrl": "https://hooks.slack.com/services/T/B/x"}, nil)

	// 40 mentions in a burst become one email and one Slack message, not 40 of each.
	var is Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Launch"}, &is)
	for i := 0; i < 40; i++ {
		alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob ping"}, nil)
	}
	// Nothing goes out inside the requests.
	if len(mail.sent) != 0 || len(slack.posts) != 0 {
		t.Fatal("delivered inline")
	}
	n, err := app.runDeliveries(ctx)
	if err != nil || n != 2 {
		t.Fatalf("runDeliveries: %d, %v", n, err)
	}
	if len(mail.sent) != 1 || mail.sent[0].to != "bob@example.com" || mail.sent[0].subject != "Graphly: 40 updates" {
		t.Fatalf("email: %+v", mail.sent)
	}
	if !strings.Contains(mail.sent[0].body, "Alice mentioned you on DL-1") || !strings.Contains(mail.sent[0].body, "https://graphly.test/p/"+p.ID+"/board?issue=DL-1") {
		t.Fatalf("email body:\n%s", mail.sent[0].body)
	}
	if len(slack.posts) != 1 || !strings.HasPrefix(slack.posts[0], "hooks.slack.com ") {
		t.Fatalf("slack: %v", slack.posts)
	}
	if n, _ := app.runDeliveries(ctx); n != 0 {
		t.Fatal("resent a delivered digest")
	}

	// A failed send is retried with backoff, then succeeds.
	mail.fail = 1
	alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob again"}, nil)
	app.runDeliveries(ctx)
	var attempts int
	var next time.Time
	var lastErr *string
	if err := app.db.QueryRow(ctx, `SELECT attempts, next_attempt_at, last_error FROM notification_deliveries
		WHERE channel = 'email' AND status = 'pending'`).Scan(&attempts, &next, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || time.Until(next) < 20*time.Second || lastErr == nil {
		t.Fatalf("after failure: attempts %d, next in %v, err %v", attempts, time.Until(next), lastErr)
	}
	app.db.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() WHERE status = 'pending'`)
	app.runDeliveries(ctx)
	if len(mail.sent) != 2 || !strings.HasPrefix(mail.sent[1].subject, "Graphly: Alice mentioned you on DL-1") {
		t.Fatalf("retry: %+v", mail.sent)
	}

	// Repeated failures end in 'failed' after DeliveryMaxAttempts.
	mail.fail = 10
	alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob third"}, nil)
	for i := 0; i < 4; i++ {
		app.db.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() WHERE status = 'pending'`)
		app.runDeliveries(ctx)
	}
	var failed int
	app.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE channel = 'email' AND status = 'failed' AND attempts = 3`).Scan(&failed)
	if failed != 1 {
		t.Fatalf("expected one dead email delivery, got %d", failed)
	}

	// A webhook Slack has revoked is skipped, not retried forever.
	slack.status = 404
	alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob fourth"}, nil)
	app.db.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() WHERE status = 'pending'`)
	app.runDeliveries(ctx)
	var skipped int
	app.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE channel = 'slack' AND status = 'skipped'`).Scan(&skipped)
	if skipped != 1 {
		t.Fatalf("revoked webhook not skipped: %d", skipped)
	}

	// Turning a channel off stops queuing for it.
	bob.must("PUT", "/api/me/notification-settings", map[string]any{"prefs": map[string]any{"mention": map[string]bool{"inApp": true, "email": false, "slack": false}}}, nil)
	before := 0
	app.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries`).Scan(&before)
	alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob quiet"}, nil)
	after := 0
	app.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries`).Scan(&after)
	if after != before {
		t.Fatalf("queued %d deliveries with channels off", after-before)
	}
}

func TestDigestWindowHoldsBursts(t *testing.T) {
	srv, app := newTestApp(t, func(c *Config) { c.DigestWindow = time.Hour })
	mail := &fakeMailer{}
	app.mailer = mail
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice", "alice@example.com")
	bob := newClient(t, srv)
	signup(t, bob, "Bob", "bob@example.com")
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "DW", "name": "Window"}, &p)
	var is Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "x"}, &is)
	alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": "@bob hi"}, nil)
	if n, _ := app.runDeliveries(context.Background()); n != 0 {
		t.Fatal("sent before the digest window closed")
	}
	app.db.Exec(context.Background(), `UPDATE notification_deliveries SET created_at = now() - interval '2 hours'`)
	if n, _ := app.runDeliveries(context.Background()); n != 1 {
		t.Fatal("not sent after the window")
	}
}

func TestNoticeTextAndHeaders(t *testing.T) {
	got := noticeText(kindDateMoved, map[string]any{"projectName": "Apollo", "to": "2025-11-14", "days": float64(3)})
	if got != "Apollo now ships Nov 14 (+3 working days)" {
		t.Fatal(got)
	}
	if h := mimeHeader("Bonjour\r\nBcc: x@y"); strings.ContainsAny(h, "\r\n") {
		t.Fatalf("header injection: %q", h)
	}
	if h := mimeHeader("Café"); !strings.HasPrefix(h, "=?utf-8?q?") {
		t.Fatal(h)
	}
}
