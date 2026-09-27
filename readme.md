# Graphly

A graph-driven Kanban board for software teams. Tasks live on a board, but every task is also a node in a dependency graph — so Graphly can tell you what's blocked, what's holding everyone up, and which chain of work decides your ship date.

## Features

- **Kanban board** — drag cards between Backlog / To Do / In Progress / Review / Done. Hover a card to light up everything upstream (amber) and downstream (indigo) of it.
- **Dependency graph** — auto-laid-out DAG (dagre). Drag from a node's right handle to another node to add a dependency; select an edge and press Backspace to remove it. Cycles are rejected.
- **Blocker detection** — tasks with open dependencies are flagged, "Top blockers" ranks open tasks by how many downstream tasks they transitively hold up, and work started on a still-blocked task is flagged **at risk**.
- **Critical path** — critical path method (CPM) over open tasks using estimates: remaining project duration, per-task earliest start/finish and slack, and the critical chain highlighted on both views.
- **Real-time collaboration** — the board is a [Yjs](https://yjs.dev) CRDT synced peer-to-peer over WebRTC (and instantly between tabs), with live presence showing who's viewing which task. Changes persist locally in IndexedDB. Concurrent edits that merge into a cycle are detected and surfaced.
- **Undo / redo** — `⌘Z` / `⇧⌘Z`. `g` toggles board/graph, `i` toggles insights.

## Getting started

```sh
npm install
npm run dev     # http://localhost:5173
npm test        # graph engine unit tests
npm run build
```

Each board is a room identified by the URL hash (`#room=…`). Click **Share** to copy the link — anyone who opens it joins the same board.

### Signaling

WebRTC peers find each other through y-webrtc's public signaling servers by default. For anything beyond a demo, run your own (`npx y-webrtc-signaling`, port 4444) and point Graphly at it:

```sh
VITE_SIGNALING=wss://signal.example.com npm run build
```

## Structure

```
src/lib/graph.ts        cycle detection, blockers, CPM (pure, tested)
src/store/doc.ts        Yjs document, providers, presence, mutations
src/store/context.tsx   React bindings (useSyncExternalStore)
src/components/         BoardView, GraphView, TaskEditor, Insights
```
