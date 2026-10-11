package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Request-scoped facts for logs and metrics. The middleware puts a *requestInfo in the context;
// handlers fill in who and where as they learn it (currentUser, the role checks).
type requestInfo struct {
	id, traceID         string
	userID, workspaceID string
}

type requestInfoKey struct{}

func reqInfo(ctx context.Context) *requestInfo {
	ri, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return ri
}

func noteUser(ctx context.Context, userID string) {
	if ri := reqInfo(ctx); ri != nil {
		ri.userID = userID
	}
}

func noteWorkspace(ctx context.Context, wsID string) {
	if ri := reqInfo(ctx); ri != nil && wsID != "" {
		ri.workspaceID = wsID
	}
}

// logAttrs are the request's identifiers, for any log line written while handling it.
func logAttrs(ctx context.Context) []any {
	ri := reqInfo(ctx)
	if ri == nil {
		return nil
	}
	attrs := []any{"request_id", ri.id}
	if ri.traceID != "" {
		attrs = append(attrs, "trace_id", ri.traceID)
	}
	if ri.userID != "" {
		attrs = append(attrs, "user_id", ri.userID)
	}
	if ri.workspaceID != "" {
		attrs = append(attrs, "workspace_id", ri.workspaceID)
	}
	return attrs
}

var (
	requestIDRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	traceparentRe = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)
)

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// requestIdentity keeps a well-formed X-Request-ID from the proxy (so logs line up across hops)
// and the W3C trace id from traceparent, if any.
func requestIdentity(r *http.Request) *requestInfo {
	ri := &requestInfo{id: r.Header.Get("X-Request-ID")}
	if !requestIDRe.MatchString(ri.id) {
		ri.id = newRequestID()
	}
	if m := traceparentRe.FindStringSubmatch(r.Header.Get("traceparent")); m != nil && m[1] != strings.Repeat("0", 32) {
		ri.traceID = m[1]
	}
	return ri
}

// statusWriter records the status and size of a response. Unwrap lets http.ResponseController
// and the WebSocket upgrade reach the underlying writer.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// routeLabel is the mux pattern without the method ("/api/issues/{id}"), so metrics stay
// low-cardinality; static files and unmatched paths are grouped.
func routeLabel(r *http.Request) string {
	p := r.Pattern
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = rest
	}
	switch {
	case p == "":
		return "unmatched"
	case p == "/":
		return "static"
	}
	return p
}

var quietPaths = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true, "/api/health": true}

// observe logs one line per request and records its metrics.
func (s *Server) observe(w http.ResponseWriter, r *http.Request, next http.Handler) {
	start := time.Now()
	ri := requestIdentity(r)
	w.Header().Set("X-Request-ID", ri.id)
	sw := &statusWriter{ResponseWriter: w}
	r = r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, ri))
	defer func() {
		if rec := recover(); rec != nil {
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			httpErrors.Inc("panic")
			slog.Error("panic", append(logAttrs(r.Context()), "method", r.Method, "path", r.URL.Path, "panic", rec)...)
			if sw.status == 0 {
				writeJSON(sw, http.StatusInternalServerError, map[string]string{"error": "something went wrong"})
			}
		}
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		took := time.Since(start)
		route := routeLabel(r)
		httpRequests.Inc(r.Method, route, strconv.Itoa(status))
		if status != http.StatusSwitchingProtocols {
			httpDuration.Observe(took.Seconds(), r.Method, route)
		}
		if status >= 500 {
			httpErrors.Inc("http_5xx")
		}
		if quietPaths[r.URL.Path] || !s.cfg.LogRequests {
			return
		}
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		slog.Log(r.Context(), level, "request", append(logAttrs(r.Context()),
			"method", r.Method, "route", route, "path", r.URL.Path, "status", status,
			"duration_ms", float64(took.Microseconds())/1000, "bytes", sw.bytes, "ip", auditIP(r.Context()))...)
	}()
	next.ServeHTTP(sw, r)
}

// newLogger builds the process logger: JSON by default (one object per line, ready for any log
// pipeline), or text with LOG_FORMAT=text for local development.
func newLogger(format, level string, out io.Writer) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}

// healthz: the process is up and serving. It never touches the database, so a slow database
// doesn't get every instance restarted at once.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz: this instance can do useful work: the database answers and the realtime listener is
// connected. Load balancers should route only to ready instances.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"database": "ok", "realtime": "ok"}
	ready := true
	var one int
	if err := s.db.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		checks["database"] = "unavailable"
		ready = false
	}
	if s.hub != nil && !s.hub.Listening() {
		checks["realtime"] = "not listening"
		ready = false
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ready": ready, "checks": checks})
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
