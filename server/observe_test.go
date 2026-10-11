package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) lines() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func get(t *testing.T, url string, header ...string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

func TestObservability(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(newLogger("json", "info", logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv, _ := newTestApp(t, func(c *Config) { c.LogRequests = true; c.MetricsToken = "scrape-me" })
	c := newClient(t, srv)
	u, ws := signup(t, c, "Olly", "olly@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "OB", "name": "Observed"}, &p)
	c.must("GET", "/api/projects/"+p.ID, nil, nil)

	// Liveness never needs the database; readiness checks it and the realtime listener.
	if code, _, _ := get(t, srv.URL+"/healthz"); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
	code, _, body := get(t, srv.URL+"/readyz")
	if code != 200 || !strings.Contains(body, `"ready":true`) {
		t.Fatalf("readyz: %d %s", code, body)
	}

	// Request ids: a sane one from the proxy is kept, anything else is replaced.
	_, h, _ := get(t, srv.URL+"/healthz", "X-Request-ID", "edge-abc.123")
	if h.Get("X-Request-ID") != "edge-abc.123" {
		t.Fatalf("request id not kept: %q", h.Get("X-Request-ID"))
	}
	_, h, _ = get(t, srv.URL+"/healthz", "X-Request-ID", "bad id with spaces and <html>")
	if id := h.Get("X-Request-ID"); id == "" || strings.Contains(id, " ") {
		t.Fatalf("request id not replaced: %q", id)
	}

	// One structured line per request, with who and which workspace.
	var found map[string]any
	for _, l := range logs.lines() {
		if l["msg"] == "request" && l["route"] == "/api/projects/{id}" && l["method"] == "GET" {
			found = l
		}
	}
	if found == nil || found["user_id"] != u.ID || found["workspace_id"] != ws.ID || found["request_id"] == "" || found["status"] != float64(200) {
		t.Fatalf("request log line: %v", found)
	}
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	get(t, srv.URL+"/api/me", "traceparent", tp)
	var traced bool
	for _, l := range logs.lines() {
		traced = traced || (l["path"] == "/api/me" && l["trace_id"] == "4bf92f3577b34da6a3ce929d0e0e4736")
	}
	if !traced {
		t.Fatal("trace id from traceparent not logged")
	}

	// Metrics: hidden without the token, Prometheus text with it.
	if code, _, _ := get(t, srv.URL+"/metrics"); code != http.StatusNotFound {
		t.Fatalf("metrics without token: %d", code)
	}
	code, h, body = get(t, srv.URL+"/metrics", "Authorization", "Bearer scrape-me")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics: %d %s", code, h.Get("Content-Type"))
	}
	for _, want := range []string{
		`graphly_http_requests_total{method="GET",route="/api/projects/{id}",code="200"}`,
		`graphly_http_request_duration_seconds_bucket{method="GET",route="/api/projects/{id}",le="+Inf"}`,
		`# TYPE graphly_websocket_connections gauge`,
		`graphly_db_pool_connections{state="total"}`,
		`# TYPE graphly_forecast_job_duration_seconds histogram`,
		`graphly_notification_queue_depth`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}

// An instance whose realtime listener isn't running isn't ready, even with the database up.
func TestReadyzNeedsListener(t *testing.T) {
	_, app := newTestApp(t, nil)
	stalled := NewServer(app.db, NewHub(app.db), Config{}) // hub never started
	rec := httptest.NewRecorder()
	stalled.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil).WithContext(context.Background()))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not listening") {
		t.Fatalf("readyz with a stopped listener: %d %s", rec.Code, rec.Body)
	}
}

func TestMetricsExposition(t *testing.T) {
	h := &metric{name: "t_seconds", help: "test", kind: histogramKind, bounds: []float64{1, 5}, labelNames: []string{"k"}, series: map[string]*series{}}
	r := &registry{metrics: []*metric{h}}
	h.Observe(0.5, `a"b`)
	h.Observe(3, `a"b`)
	h.Observe(9, `a"b`)
	var b bytes.Buffer
	r.write(&b)
	want := `# HELP t_seconds test
# TYPE t_seconds histogram
t_seconds_bucket{k="a\"b",le="1"} 1
t_seconds_bucket{k="a\"b",le="5"} 2
t_seconds_bucket{k="a\"b",le="+Inf"} 3
t_seconds_sum{k="a\"b"} 12.5
t_seconds_count{k="a\"b"} 3
`
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
