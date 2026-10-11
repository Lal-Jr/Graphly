package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A small Prometheus registry: counters, gauges and histograms with labels, written in the text
// exposition format. It avoids pulling in client_golang for the dozen series Graphly exports.
// Every instance keeps its own numbers; Prometheus scrapes each one.

type metricKind string

const (
	counterKind   metricKind = "counter"
	gaugeKind     metricKind = "gauge"
	histogramKind metricKind = "histogram"
)

type series struct {
	labels  []string
	value   float64
	buckets []uint64 // histogram: cumulative counts are computed when written
	sum     float64
	count   uint64
}

type metric struct {
	name, help string
	kind       metricKind
	labelNames []string
	bounds     []float64 // histogram upper bounds

	mu     sync.Mutex
	series map[string]*series
}

type registry struct {
	mu      sync.Mutex
	metrics []*metric
	// Collected at scrape time (pool stats, connection counts).
	collectors []func()
}

var metrics = &registry{}

func (r *registry) add(m *metric) *metric {
	m.series = map[string]*series{}
	r.mu.Lock()
	r.metrics = append(r.metrics, m)
	r.mu.Unlock()
	return m
}

func newCounter(name, help string, labels ...string) *metric {
	return metrics.add(&metric{name: name, help: help, kind: counterKind, labelNames: labels})
}

func newGauge(name, help string, labels ...string) *metric {
	return metrics.add(&metric{name: name, help: help, kind: gaugeKind, labelNames: labels})
}

func newHistogram(name, help string, bounds []float64, labels ...string) *metric {
	return metrics.add(&metric{name: name, help: help, kind: histogramKind, labelNames: labels, bounds: bounds})
}

func (m *metric) get(labels []string) *series {
	if len(labels) != len(m.labelNames) {
		panic(fmt.Sprintf("metric %s: %d labels, want %d", m.name, len(labels), len(m.labelNames)))
	}
	key := strings.Join(labels, "\xff")
	s := m.series[key]
	if s == nil {
		s = &series{labels: append([]string(nil), labels...)}
		if m.kind == histogramKind {
			s.buckets = make([]uint64, len(m.bounds))
		}
		m.series[key] = s
	}
	return s
}

func (m *metric) Add(v float64, labels ...string) {
	m.mu.Lock()
	m.get(labels).value += v
	m.mu.Unlock()
}

func (m *metric) Inc(labels ...string) { m.Add(1, labels...) }

func (m *metric) Set(v float64, labels ...string) {
	m.mu.Lock()
	m.get(labels).value = v
	m.mu.Unlock()
}

func (m *metric) Observe(v float64, labels ...string) {
	m.mu.Lock()
	s := m.get(labels)
	for i, b := range m.bounds {
		if v <= b {
			s.buckets[i]++
			break
		}
	}
	s.sum += v
	s.count++
	m.mu.Unlock()
}

func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func labelString(names, values []string, extra ...string) string {
	var parts []string
	for i, n := range names {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, n, escapeLabel(values[i])))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, extra[i], escapeLabel(extra[i+1])))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (r *registry) write(w io.Writer) {
	r.mu.Lock()
	collectors := append([]func(){}, r.collectors...)
	ms := append([]*metric{}, r.metrics...)
	r.mu.Unlock()
	for _, c := range collectors {
		c()
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].name < ms[j].name })
	for _, m := range ms {
		m.mu.Lock()
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
		keys := make([]string, 0, len(m.series))
		for k := range m.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := m.series[k]
			if m.kind != histogramKind {
				fmt.Fprintf(w, "%s%s %s\n", m.name, labelString(m.labelNames, s.labels), formatFloat(s.value))
				continue
			}
			var cum uint64
			for i, b := range m.bounds {
				cum += s.buckets[i]
				fmt.Fprintf(w, "%s_bucket%s %d\n", m.name, labelString(m.labelNames, s.labels, "le", formatFloat(b)), cum)
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", m.name, labelString(m.labelNames, s.labels, "le", "+Inf"), s.count)
			fmt.Fprintf(w, "%s_sum%s %s\n", m.name, labelString(m.labelNames, s.labels), formatFloat(s.sum))
			fmt.Fprintf(w, "%s_count%s %d\n", m.name, labelString(m.labelNames, s.labels), s.count)
		}
		m.mu.Unlock()
	}
}

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

var (
	httpRequests       = newCounter("graphly_http_requests_total", "HTTP requests by route pattern and status code.", "method", "route", "code")
	httpDuration       = newHistogram("graphly_http_request_duration_seconds", "HTTP request latency by route pattern (WebSocket upgrades excluded).", latencyBuckets, "method", "route")
	httpErrors         = newCounter("graphly_errors_total", "Server-side errors: 5xx responses, panics, failed background work.", "kind")
	wsConnections      = newGauge("graphly_websocket_connections", "Open realtime WebSocket connections on this instance.")
	realtimeLag        = newHistogram("graphly_realtime_event_lag_seconds", "Delay from the publishing transaction to the broadcast on this instance (events outbox lag).", []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30})
	realtimeResyncs    = newCounter("graphly_realtime_resyncs_total", "Times clients were told to reload: the LISTEN connection was lost, or a replay reached past pruned events.", "reason")
	realtimeReplayed   = newCounter("graphly_realtime_replayed_events_total", "Events replayed to reconnecting clients from the events outbox.")
	deliveryPending    = newGauge("graphly_notification_queue_depth", "Pending email and Slack deliveries in the outbox.", "channel")
	deliveryOldest     = newGauge("graphly_notification_queue_oldest_seconds", "Age of the oldest pending delivery that is due (outbox lag).")
	deliveryFailures   = newCounter("graphly_notification_delivery_failures_total", "Failed delivery attempts (each retry counts).", "channel")
	forecastDuration   = newHistogram("graphly_forecast_job_duration_seconds", "Duration of one pass of the forecast job.", []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300})
	forecastProjects   = newGauge("graphly_forecast_job_projects", "Projects recomputed in the last forecast job pass.")
	forecastLastRun    = newGauge("graphly_forecast_job_last_success_timestamp_seconds", "Unix time the forecast job last completed on this instance.")
	rateLimited        = newCounter("graphly_rate_limited_total", "Requests rejected by a rate limiter.", "limiter")
	dbPoolConns        = newGauge("graphly_db_pool_connections", "Database pool connections by state.", "state")
	dbPoolWaitDuration = newGauge("graphly_db_pool_acquire_wait_seconds_total", "Cumulative time spent waiting for a database connection.")
	goroutines         = newGauge("graphly_goroutines", "Goroutines in this process.")
	heapBytes          = newGauge("graphly_heap_bytes", "Bytes of allocated heap objects.")
)

// registerRuntimeCollectors adds gauges read at scrape time.
func (s *Server) registerRuntimeCollectors() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.collectors = []func(){func() {
		st := s.db.Stat()
		dbPoolConns.Set(float64(st.AcquiredConns()), "acquired")
		dbPoolConns.Set(float64(st.IdleConns()), "idle")
		dbPoolConns.Set(float64(st.TotalConns()), "total")
		dbPoolConns.Set(float64(st.MaxConns()), "max")
		dbPoolWaitDuration.Set(st.AcquireDuration().Seconds())
		goroutines.Set(float64(runtime.NumGoroutine()))
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		heapBytes.Set(float64(mem.HeapAlloc))
		if s.hub != nil {
			wsConnections.Set(float64(s.hub.connections()))
		}
	}}
}

// metricsHandler serves /metrics. On the public listener it needs METRICS_TOKEN as a bearer token;
// METRICS_ADDR serves it on a private listener without one.
func (s *Server) metricsHandler(requireToken bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireToken {
			got, _ := bearer(r)
			if s.cfg.MetricsToken == "" || !constantTimeEqual(got, s.cfg.MetricsToken) {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.write(w)
	}
}

func observeForecastJob(d time.Duration, projects int) {
	forecastDuration.Observe(d.Seconds())
	forecastProjects.Set(float64(projects))
	forecastLastRun.Set(float64(time.Now().Unix()))
}

func observeDeliveryQueue(ctx context.Context, s *Server) {
	rows, err := s.db.Query(ctx, `SELECT channel, count(*), coalesce(extract(epoch FROM now() - min(next_attempt_at) FILTER (WHERE next_attempt_at <= now())), 0)
		FROM notification_deliveries WHERE status = 'pending' GROUP BY channel`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("delivery queue metrics", "err", err)
		}
		return
	}
	defer rows.Close()
	seen := map[string]bool{}
	oldest := 0.0
	for rows.Next() {
		var ch string
		var n int64
		var age float64
		if rows.Scan(&ch, &n, &age) == nil {
			deliveryPending.Set(float64(n), ch)
			seen[ch] = true
			oldest = math.Max(oldest, age)
		}
	}
	for _, ch := range []string{"email", "slack"} {
		if !seen[ch] {
			deliveryPending.Set(0, ch)
		}
	}
	deliveryOldest.Set(oldest)
}

func observeDeliveryFailure(channel string) { deliveryFailures.Inc(channel) }

func observeRateLimited(limiter string) { rateLimited.Inc(limiter) }
