package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

// Several replicas share one Postgres. Everything that crosses instances goes through it: realtime
// events (events table + NOTIFY), presence (NOTIFY), replay after a reconnect, and the background
// jobs (advisory locks, SKIP LOCKED). These tests run two instances in one process, each with its
// own pool, LISTEN connection and HTTP server. scripts/multi-instance.sh runs the same checks
// against real processes behind docker compose --profile multi.

type liveMsg struct {
	Type  string          `json:"type"`
	Seq   int64           `json:"seq"`
	Event json.RawMessage `json:"event"`
	Users []PresenceUser  `json:"users"`
}

type liveConn struct {
	t    *testing.T
	conn *websocket.Conn
	msgs chan liveMsg
	// Shown if next times out.
	progress func() string
}

func dialLive(t *testing.T, ctx context.Context, c *apiClient, base, projectID string, since int64) *liveConn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + "/api/projects/" + projectID + "/live"
	if since > 0 {
		url += fmt.Sprintf("?since=%d", since)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1 << 20)
	l := &liveConn{t: t, conn: conn, msgs: make(chan liveMsg, 1024)}
	go func() {
		defer close(l.msgs)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m liveMsg
			if json.Unmarshal(data, &m) == nil {
				l.msgs <- m
			}
		}
	}()
	t.Cleanup(func() { conn.CloseNow() })
	return l
}

// next returns the next message accepted by keep, failing after a few seconds.
func (l *liveConn) next(keep func(liveMsg) bool) liveMsg {
	l.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-l.msgs:
			if !ok {
				l.t.Fatal("live connection closed")
			}
			if keep(m) {
				return m
			}
		case <-timeout:
			if l.progress != nil {
				l.t.Fatalf("timed out waiting for a live message (%s)", l.progress())
			}
			l.t.Fatal("timed out waiting for a live message")
		}
	}
}

func eventType(m liveMsg) string {
	var e struct{ Type string }
	_ = json.Unmarshal(m.Event, &e)
	return e.Type
}

func on(c *apiClient, srvURL string) *apiClient {
	return &apiClient{t: c.t, base: srvURL, http: c.http} // sessions live in Postgres, so a cookie works on every instance
}

func TestMultiInstanceRealtime(t *testing.T) {
	a, _ := newTestApp(t, nil)
	b, _ := newPeer(t, nil)
	checkRealtimeAcross(t, a.URL, b.URL)
}

// TestMultiInstanceProcesses runs the same checks against separately running servers, e.g.
//
//	docker compose --profile multi up -d
//	MULTI_INSTANCE_URLS=http://localhost:8080,http://localhost:8081 go test -run TestMultiInstanceProcesses .
//
// (scripts/multi-instance.sh does exactly that.)
func TestMultiInstanceProcesses(t *testing.T) {
	urls := strings.Split(os.Getenv("MULTI_INSTANCE_URLS"), ",")
	if len(urls) < 2 {
		t.Skip("set MULTI_INSTANCE_URLS to two or more running instances")
	}
	for i := 1; i < len(urls); i++ {
		checkRealtimeAcross(t, urls[0], urls[i])
	}
}

func checkRealtimeAcross(t *testing.T, aURL, bURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := func() *apiClient {
		jar, _ := cookiejar.New(nil)
		return &apiClient{t: t, base: aURL, http: &http.Client{Jar: jar}}
	}
	run := time.Now().UnixNano()
	alice := client()
	_, ws := signup(t, alice, "Alice", fmt.Sprintf("multi-alice-%d@example.com", run))
	bob := client()
	signup(t, bob, "Bob", fmt.Sprintf("multi-bob-%d@example.com", run))
	var inv struct{ Token string }
	alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
	bob.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "MI", "name": "Multi"}, &p)
	a := struct{ URL string }{aURL}
	b := struct{ URL string }{bURL}

	// Alice watches through A, Bob through B.
	la := dialLive(t, ctx, alice, a.URL, p.ID, 0)
	lb := dialLive(t, ctx, bob, b.URL, p.ID, 0)

	// Presence crosses instances.
	la.next(func(m liveMsg) bool {
		if m.Type != "presence" {
			return false
		}
		for _, u := range m.Users {
			if u.Name == "Bob" {
				return true
			}
		}
		return false
	})

	// A write through B reaches the socket on A, and the other way round.
	var is Issue
	on(bob, b.URL).must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "From B"}, &is)
	la.next(func(m liveMsg) bool { return m.Type == "event" && eventType(m) == "issue.upsert" })
	alice.must("PATCH", "/api/issues/"+is.ID, map[string]any{"title": "From A"}, nil)
	lb.next(func(m liveMsg) bool { return m.Type == "event" && strings.Contains(string(m.Event), "From A") })

	// Concurrent writers on both instances: every socket sees every event, in strictly increasing
	// seq order. The client drops anything at or below the last seq it applied, so an event that
	// committed out of order would be lost.
	const writers, each = 8, 6
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			c := alice
			base := a.URL
			if w%2 == 1 {
				c, base = bob, b.URL
			}
			for i := 0; i < each; i++ {
				on(c, base).must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": fmt.Sprintf("w%d-%d", w, i)}, nil)
			}
		}(w)
	}
	wg.Wait()
	lastSeen := map[string]int64{}
	for name, l := range map[string]*liveConn{"A": la, "B": lb} {
		var last int64
		titles := map[string]bool{}
		l.progress = func() string {
			return fmt.Sprintf("instance %s: %d of %d issues, last seq %d", name, len(titles), writers*each, last)
		}
		for len(titles) < writers*each {
			m := l.next(func(m liveMsg) bool {
				if m.Type == "resync" {
					t.Fatalf("instance %s: unexpected resync after %d of %d", name, len(titles), writers*each)
				}
				return m.Type == "event"
			})
			if m.Seq <= last {
				t.Fatalf("instance %s: seq %d arrived after %d", name, m.Seq, last)
			}
			last = m.Seq
			var e struct {
				Type  string
				Issue Issue
			}
			_ = json.Unmarshal(m.Event, &e)
			if e.Type == "issue.upsert" && strings.HasPrefix(e.Issue.Title, "w") {
				titles[e.Issue.Title] = true
			}
		}
		lastSeen[name] = last
	}

	// Replay: Bob drops off B, Alice keeps writing through A, Bob reconnects to A with ?since=
	// and receives exactly what he missed.
	last := lastSeen["B"]
	lb.conn.CloseNow()
	for i := 0; i < 3; i++ {
		alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": fmt.Sprintf("missed-%d", i)}, nil)
	}
	back := dialLive(t, ctx, bob, a.URL, p.ID, last)
	for i := 0; i < 3; i++ {
		m := back.next(func(m liveMsg) bool { return m.Type == "event" && eventType(m) == "issue.upsert" })
		if m.Seq <= last || !strings.Contains(string(m.Event), fmt.Sprintf("missed-%d", i)) {
			t.Fatalf("replay %d: seq %d, %s", i, m.Seq, m.Event)
		}
		last = m.Seq
	}

	// A snapshot's seq is where this project's stream continues: activity in another project
	// doesn't move it.
	var sb projectSnapshot
	on(bob, b.URL).must("GET", "/api/projects/"+p.ID, nil, &sb)
	if sb.Seq < last {
		t.Fatalf("snapshot seq %d is behind the last event %d", sb.Seq, last)
	}
	var other Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "OT", "name": "Other"}, &other)
	alice.must("POST", "/api/projects/"+other.ID+"/issues", map[string]any{"title": "elsewhere"}, nil)
	var again projectSnapshot
	alice.must("GET", "/api/projects/"+p.ID, nil, &again)
	if again.Seq != sb.Seq {
		t.Fatalf("another project's events moved the snapshot seq from %d to %d", sb.Seq, again.Seq)
	}
}

// When an instance loses its LISTEN connection it may have missed notifications, so it tells its
// clients to resync, reconnects, and carries on.
func TestListenerReconnect(t *testing.T) {
	a, app := newTestApp(t, nil)
	b, _ := newPeer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	alice := newClient(t, a)
	_, ws := signup(t, alice, "Alice", "listen@example.com")
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "LR", "name": "Listen"}, &p)
	la := dialLive(t, ctx, alice, a.URL, p.ID, 0)
	la.next(func(m liveMsg) bool { return m.Type == "presence" })

	// Kill every LISTEN backend, as a database failover or a proxy timeout would.
	if _, err := app.db.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname = current_database() AND query LIKE 'LISTEN %' AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	la.next(func(m liveMsg) bool { return m.Type == "resync" })
	// After reconnecting, writes through the other instance arrive again.
	deadline := time.Now().Add(10 * time.Second)
	for {
		on(alice, b.URL).must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "after"}, nil)
		select {
		case m := <-la.msgs:
			if m.Type == "event" {
				return
			}
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("no events after the listener reconnected")
		}
	}
}

// Background jobs run on every instance; the database decides who does each piece of work.
func TestMultiInstanceJobs(t *testing.T) {
	a, appA := newTestApp(t, nil)
	_, appB := newPeer(t, nil)
	ctx := context.Background()
	mailA, mailB := &fakeMailer{}, &fakeMailer{}
	appA.mailer, appB.mailer = mailA, mailB

	alice := newClient(t, a)
	_, ws := signup(t, alice, "Alice", "jobs-alice@example.com")
	var p Project
	alice.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "JB", "name": "Jobs"}, &p)
	var is Issue
	alice.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Mention target"}, &is)
	const users = 6
	for i := 0; i < users; i++ {
		c := newClient(t, a)
		signup(t, c, fmt.Sprintf("User%d", i), fmt.Sprintf("jobs-%d@example.com", i))
		var inv struct{ Token string }
		alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
		c.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
		alice.must("POST", "/api/issues/"+is.ID+"/comments", map[string]any{"body": fmt.Sprintf("@user%d look", i)}, nil)
	}

	// Both instances drain the delivery outbox at once: each digest goes out exactly once.
	var wg sync.WaitGroup
	for _, app := range []*Server{appA, appB, appA, appB} {
		wg.Add(1)
		go func(app *Server) {
			defer wg.Done()
			if _, err := app.runDeliveries(ctx); err != nil {
				t.Error(err)
			}
		}(app)
	}
	wg.Wait()
	sent := map[string]int{}
	for _, m := range append(mailA.sent, mailB.sent...) {
		sent[m.to]++
	}
	if len(sent) != users {
		t.Fatalf("expected %d recipients, got %d: %v", users, len(sent), sent)
	}
	for to, n := range sent {
		if n != 1 {
			t.Fatalf("%s got %d emails", to, n)
		}
	}

	// The forecast job is single-flight across instances, and its history row is written once.
	errs := make(chan error, 4)
	for _, app := range []*Server{appA, appB, appA, appB} {
		go func(app *Server) { errs <- app.runForecastJob(ctx) }(app)
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	appA.db.QueryRow(ctx, `SELECT count(*) FROM forecast_snapshots WHERE project_id = $1`, p.ID).Scan(&rows)
	if rows != 1 {
		t.Fatalf("forecast history rows: %d", rows)
	}
}

// Deterministic version of the ordering guarantee: while one transaction holds an unpublished
// event for a project, a second publisher to the same project waits, so its higher id can't
// become visible first. A publisher to another project doesn't wait.
func TestPublishOrderPerProject(t *testing.T) {
	srv, app := newTestApp(t, nil)
	ctx := context.Background()
	c := newClient(t, srv)
	_, ws := signup(t, c, "Ann", "order@example.com")
	var p, q Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "OR", "name": "Order"}, &p)
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "OQ", "name": "Other"}, &q)

	first, err := app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	if err := publish(ctx, first, p.ID, "test", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}

	// Another project is independent.
	if err := app.inTx(ctx, func(tx pgx.Tx) error { return publish(ctx, tx, q.ID, "test", map[string]any{}) }); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- app.inTx(ctx, func(tx pgx.Tx) error { return publish(ctx, tx, p.ID, "test", map[string]any{"n": 2}) })
	}()
	select {
	case err := <-done:
		t.Fatalf("second publisher committed while the first was open (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var ns []int
	rows, _ := app.db.Query(ctx, `SELECT (payload->>'n')::int FROM events WHERE project_id = $1 AND payload->>'type' = 'test' ORDER BY id`, p.ID)
	for rows.Next() {
		var n int
		rows.Scan(&n)
		ns = append(ns, n)
	}
	if fmt.Sprint(ns) != "[1 2]" {
		t.Fatalf("events in id order: %v", ns)
	}
}
