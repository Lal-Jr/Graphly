# Graphly

Graphly is a Jira-style issue tracker that treats your plan as a **dependency graph**. Every "blocks / is blocked by" link feeds a scheduling engine, so the board always knows what's blocked, what's holding everyone up, and — from estimates and dependencies alone — when things will actually finish.

## What it does

**The Jira basics:** workspaces and projects with issue keys (`APL-42`), stories / tasks / bugs / epics, a configurable workflow, a Kanban board with swimlanes (by assignee or epic) and drag-and-drop, a sortable list view, an issue view with comments and full history, labels, priorities, due dates, filters, a ⌘K command palette, invite links with admin and member roles, and light / dark / system themes.

**Things Jira doesn't do:**

| | |
|---|---|
| **Forecast dates** | Every open issue gets a start and finish date computed by the critical path method over working days. Epics roll up to the finish of their last child. |
| **Forecast timeline** | An auto-scheduled Gantt chart: bars, slack and due-date markers are derived from dependencies, not dragged by hand. |
| **What-if simulation** | "If APL-9 slips 3 days…" shows the new project finish, every issue that shifts, and which due dates newly get missed. |
| **Critical path** | The chain that decides your ship date is highlighted across the board, graph and timeline. Every issue shows its slack. |
| **Blocker intelligence** | Blockers are ranked by how many issues they transitively hold up. "At risk" flags work started while its blockers are still open, and "stale blockers" flags blocking issues nobody has touched in days. |
| **Late before it's late** | Issues whose *forecast* lands after their due date are flagged now, not on the day. |
| **People & bottlenecks** | Load per person, split into critical-path and other work, plus a suggested next task for each person: unblocked, critical first, then biggest impact. |
| **Unblock notifications** | Closing the last blocker writes "unblocked by APL-5" into the waiting issue's history. |
| **Dependency graph** | A live DAG you can edit by dragging between nodes. Cycles are rejected both in the UI and, transactionally, on the server. |
| **Safe concurrent edits** | Live presence shows who's viewing and who's typing. Title and description saves use compare-and-swap, so a simultaneous edit shows a conflict instead of silently overwriting. |

## Architecture

```
web/      React + TypeScript (Vite). Graph engine and forecasting run client-side.
server/   Go (net/http, pgx, coder/websocket). Postgres is the source of truth.
```

- **Realtime:** every write records an event in an outbox table inside the same transaction, then `pg_notify`s its id. Every server instance `LISTEN`s, so the architecture supports more than one instance (only a single instance has been run so far). Clients resume from their last sequence number after a disconnect, and the server asks them to reload if the gap is older than the 24-hour retention window.
- **Integrity:** adding a dependency locks the project row and checks reachability with a recursive CTE, so two people linking A→B and B→A at the same moment can't create a cycle. `TestConcurrentOppositeLinks` covers this case.
- **Auth:** bcrypt passwords and random 256-bit session tokens, stored hashed, in an `HttpOnly`, `SameSite=Lax` cookie. State-changing requests require JSON and a same-origin `Origin` header. Login is rate-limited per IP. Access is enforced per workspace on every route and WebSocket.

## Development

Prerequisites: Go 1.26+, Node 22+, Postgres 15+.

```sh
createdb graphly

# API on :8080 (runs migrations on boot)
cd server && go run .

# Frontend on :5173, proxying /api to the Go server
cd web && npm install && npm run dev
```

If port 8080 is taken, run the server with `ADDR=:8090` and the frontend with `GRAPHLY_API=http://localhost:8090 npm run dev`. See `.env.example` for all settings. When you create a project, tick **Include a sample plan** to get an 18-issue launch with a due date that's already at risk.

### Tests

```sh
createdb graphly_test
cd server && go test -race ./...   # API, auth, realtime, concurrency (needs Postgres; wipes graphly_test)
cd web && npm test                 # graph engine + forecasting
```

## Deploying

The `Dockerfile` builds a single distroless image: the Go binary serves the API, WebSockets and the built SPA.

```sh
docker compose up --build     # Postgres + Graphly on http://localhost:8080
```

For a real deployment, point `DATABASE_URL` at managed Postgres, serve over HTTPS, and set `SECURE_COOKIES=true`. Any container host works (Fly.io, Render, Railway, ECS, and so on).
