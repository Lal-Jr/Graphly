# Operating Graphly

How to run Graphly in production and know when it needs attention. Backups and restores are
covered in [backups.md](backups.md).

## Topology

One static binary serves the API, the WebSocket endpoint and the built web app. Run as many
instances as you like behind a load balancer, all against one Postgres:

- Realtime events go into the `events` table and are announced with `NOTIFY`. Every instance
  `LISTEN`s and fans them out to its own sockets. A client that reconnects to a different
  instance replays what it missed with `?since=`.
- Event ids commit in order within a project: `publish` holds a per-project advisory lock from
  allocating the id until commit. Clients skip any seq at or below the last one they applied, so
  out-of-order commits would otherwise lose events.
- The forecast job is single-flight across instances (a session advisory lock). The
  notification outbox is drained with `FOR UPDATE SKIP LOCKED`, so each digest goes out once.
- Migrations run at startup under an advisory lock, so instances can boot together.
- Rate limiters and the forecast cache are per instance. A limit of N per minute is therefore up
  to N per minute per instance.

`LISTEN` needs a session-level connection. Point `DATABASE_URL` at Postgres directly, or at a
pooler in session mode. PgBouncer in transaction mode will silently drop notifications.

`docker compose --profile multi up` runs two instances (`:8080` and `:8081`) on one database.
`scripts/multi-instance.sh` checks realtime fan-out, ordering, presence and replay across them.
`go test -run 'TestMultiInstance|TestListenerReconnect|TestPublishOrder' ./...` runs the same
checks in-process, plus single-flight jobs and exactly-once delivery.

## Health checks

| Path | Meaning | Use for |
|---|---|---|
| `GET /healthz` | The process is up. Never touches the database. | Liveness probes (restart on failure). |
| `GET /readyz` | The database answers within 2s and the realtime listener is connected. 503 with the failing check otherwise. | Readiness probes and load balancer health. |
| `GET /api/health` | Same as `/readyz`, kept for existing checks. | |

Don't use `/readyz` as a liveness probe. A database blip would otherwise restart every
instance at once.

## Logs

JSON on stdout, one object per line (`LOG_FORMAT=text` for local development, `LOG_LEVEL` to
change the level). Each request logs one line:

```json
{"time":"…","level":"INFO","msg":"request","request_id":"f41adf0d…","trace_id":"4bf92f35…","user_id":"…","workspace_id":"…","method":"GET","route":"/api/projects/{id}","path":"/api/projects/c0d2…","status":200,"duration_ms":7.59,"bytes":1170,"ip":"203.0.113.9"}
```

- `request_id` is the incoming `X-Request-ID` when it's well formed, otherwise generated. It's
  echoed in the response's `X-Request-ID` and attached to any error logged while handling the
  request.
- `trace_id` comes from a W3C `traceparent` header when a proxy or client sends one, so logs can
  be joined to traces from your edge.
- `user_id` and `workspace_id` are filled in once the request is authenticated and its workspace
  is known.
- Health checks and `/metrics` aren't logged. `LOG_REQUESTS=false` turns request lines off
  entirely (errors are still logged).

Tracing: there's no OpenTelemetry SDK in the binary. Adding it would pull in a large dependency
tree for spans that, today, would mostly be "one Postgres query". Instead, request logs carry the
`traceparent` trace id. If you need spans, wrap the handler with `otelhttp` in `Routes()` and
`pgx`'s tracer hook; the request context already flows through every query.

## Metrics

Prometheus text format at `/metrics`.

- **Main listener**: requires `Authorization: Bearer $METRICS_TOKEN` and returns 404 without
  it, or when no token is set.
- **Private listener**: set `METRICS_ADDR` (for example `:9090`) to serve it without auth on an
  address that isn't exposed publicly.

Each instance reports its own numbers; scrape every instance.

| Metric | Type | What it tells you |
|---|---|---|
| `graphly_http_requests_total{method,route,code}` | counter | Traffic and error rate by route pattern. |
| `graphly_http_request_duration_seconds{method,route}` | histogram | Latency (WebSocket upgrades excluded). |
| `graphly_errors_total{kind}` | counter | `http_5xx`, `panic`, `forecast_job`, `delivery`. |
| `graphly_websocket_connections` | gauge | Open realtime connections on this instance. |
| `graphly_realtime_event_lag_seconds` | histogram | From the publishing transaction to the broadcast here. |
| `graphly_realtime_resyncs_total{reason}` | counter | Clients told to reload: `listener_reconnect` (lost `LISTEN`), `replay_pruned` (offline longer than event retention). |
| `graphly_realtime_replayed_events_total` | counter | Events replayed to reconnecting clients. |
| `graphly_notification_queue_depth{channel}` | gauge | Pending email/Slack deliveries. |
| `graphly_notification_queue_oldest_seconds` | gauge | How overdue the oldest due delivery is (outbox lag). |
| `graphly_notification_delivery_failures_total{channel}` | counter | Failed attempts, retries included. |
| `graphly_forecast_job_duration_seconds` | histogram | One pass of the forecast job. |
| `graphly_forecast_job_projects` | gauge | Projects recomputed in the last pass. |
| `graphly_forecast_job_last_success_timestamp_seconds` | gauge | When this instance last finished a pass. Only the instance holding the lock runs it. |
| `graphly_rate_limited_total{limiter}` | counter | Requests refused by each rate limiter. |
| `graphly_db_pool_connections{state}` | gauge | `acquired`, `idle`, `total`, `max`. |
| `graphly_db_pool_acquire_wait_seconds_total` | gauge | Cumulative wait for a connection. Its rate is pool pressure. |
| `graphly_goroutines`, `graphly_heap_bytes` | gauge | Process health. |

## Suggested alerts

Starting points. Tune the thresholds to your traffic.

```yaml
groups:
  - name: graphly
    rules:
      - alert: GraphlyNotReady
        expr: probe_success{job="graphly-readyz"} == 0     # from blackbox_exporter on /readyz
        for: 2m
        labels: { severity: page }
        annotations: { summary: "A Graphly instance can't reach Postgres or lost its realtime listener" }

      - alert: GraphlyHighErrorRate
        expr: |
          sum(rate(graphly_http_requests_total{code=~"5.."}[5m]))
            / sum(rate(graphly_http_requests_total[5m])) > 0.02
        for: 10m
        labels: { severity: page }

      - alert: GraphlySlowRequests
        expr: |
          histogram_quantile(0.95, sum by (le) (rate(graphly_http_request_duration_seconds_bucket{route!~".*/live"}[5m]))) > 1
        for: 15m
        labels: { severity: ticket }

      - alert: GraphlyPanics
        expr: increase(graphly_errors_total{kind="panic"}[15m]) > 0
        labels: { severity: ticket }

      - alert: GraphlyRealtimeLagging
        expr: histogram_quantile(0.95, sum by (le) (rate(graphly_realtime_event_lag_seconds_bucket[5m]))) > 2
        for: 10m
        labels: { severity: ticket }
        annotations: { summary: "Edits take more than 2s to reach other browsers" }

      - alert: GraphlyListenerFlapping
        expr: increase(graphly_realtime_resyncs_total{reason="listener_reconnect"}[30m]) > 3
        labels: { severity: ticket }
        annotations: { summary: "LISTEN connection keeps dropping (pooler in transaction mode? idle timeouts?)" }

      - alert: GraphlyNotificationsBackedUp
        expr: max(graphly_notification_queue_oldest_seconds) > 900
        for: 10m
        labels: { severity: ticket }
        annotations: { summary: "Email/Slack deliveries are more than 15 minutes overdue" }

      - alert: GraphlyDeliveriesFailing
        expr: sum by (channel) (increase(graphly_notification_delivery_failures_total[30m])) > 20
        labels: { severity: ticket }
        annotations: { summary: "SMTP or Slack is rejecting deliveries" }

      - alert: GraphlyForecastJobStale
        # The job runs on one instance at a time, so take the newest across instances.
        expr: time() - max(graphly_forecast_job_last_success_timestamp_seconds) > 3 * 900   # 3 × FORECAST_INTERVAL
        for: 5m
        labels: { severity: ticket }
        annotations: { summary: "Forecast history and late/date-moved alerts have stopped updating" }

      - alert: GraphlyDBPoolSaturated
        expr: graphly_db_pool_connections{state="acquired"} / graphly_db_pool_connections{state="max"} > 0.9
        for: 10m
        labels: { severity: ticket }
```

Also alert on Postgres itself: disk, replication lag, connections, and backup freshness (see
[backups.md](backups.md)).
