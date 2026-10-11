package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	eventsChannel   = "graphly_events"
	presenceChannel = "graphly_presence"
	presenceTTL     = 45 * time.Second
	heartbeat       = 15 * time.Second
)

// publish records a realtime event inside the caller's transaction. NOTIFY is delivered
// only on commit, so clients never see changes that were rolled back.
//
// Clients skip any seq at or below the last one they applied, so within a project event ids must
// become visible in increasing order. A sequence alone doesn't promise that: two transactions can
// take ids 9 and 10 and commit 10 first, and the client would then drop 9. The project-scoped
// advisory lock, taken before the id is allocated and held until commit, serializes publishers per
// project, so the committed events of a project are always a gap-free prefix of its ids.
func publish(ctx context.Context, tx pgx.Tx, projectID, typ string, data map[string]any) error {
	data["type"] = typ
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO events (project_id, payload)
		SELECT $1::uuid, $2 FROM (SELECT pg_advisory_xact_lock(hashtextextended('graphly_events:' || $1::text, 0))) AS l
		RETURNING id`, projectID, payload).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, eventsChannel, strconv.FormatInt(id, 10))
	return err
}

type PresenceUser struct {
	UserID  string  `json:"userId"`
	Name    string  `json:"name"`
	Color   string  `json:"color"`
	Viewing *string `json:"viewing"`
	// Editing is the field the user is typing in, e.g. "description".
	Editing *string `json:"editing"`
}

type presenceMsg struct {
	Instance  string       `json:"instance"`
	Conn      int64        `json:"conn"`
	ProjectID string       `json:"projectId"`
	User      PresenceUser `json:"user"`
	Left      bool         `json:"left"`
}

type presenceEntry struct {
	user PresenceUser
	seen time.Time
}

type client struct {
	id        int64
	projectID string
	send      chan []byte
	user      PresenceUser
	// Signalled when this user's access to the project may have changed.
	recheck chan struct{}
}

// Hub fans events out to this instance's WebSocket clients. Every instance LISTENs on the same
// Postgres channels, so a change made through any instance reaches clients on all of them.
type Hub struct {
	db       *pgxpool.Pool
	instance string

	listening atomic.Bool

	mu       sync.Mutex
	nextID   int64
	clients  map[string]map[int64]*client        // project → conn → client
	presence map[string]map[string]presenceEntry // project → instance/conn → entry
}

func NewHub(db *pgxpool.Pool) *Hub {
	return &Hub{
		db:       db,
		instance: randomToken(6),
		clients:  map[string]map[int64]*client{},
		presence: map[string]map[string]presenceEntry{},
	}
}

func (h *Hub) Run(ctx context.Context) {
	go h.heartbeatLoop(ctx)
	for ctx.Err() == nil {
		if err := h.listen(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("hub listener disconnected, retrying", "err", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := h.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	for _, ch := range []string{eventsChannel, presenceChannel} {
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			return err
		}
	}
	h.listening.Store(true)
	defer h.listening.Store(false)
	// Anything published while we were disconnected is recovered by clients via ?since= on reconnect.
	if h.connections() > 0 {
		realtimeResyncs.Inc("listener_reconnect")
	}
	h.resyncAll()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case eventsChannel:
			id, err := strconv.ParseInt(n.Payload, 10, 64)
			if err != nil {
				continue
			}
			var projectID string
			var payload json.RawMessage
			var age float64
			if err := h.db.QueryRow(ctx, `SELECT project_id, payload, extract(epoch FROM clock_timestamp() - created_at) FROM events WHERE id = $1`, id).Scan(&projectID, &payload, &age); err != nil {
				slog.Warn("load event", "id", id, "err", err)
				continue
			}
			var head struct {
				Type   string `json:"type"`
				UserID string `json:"userId"`
			}
			if json.Unmarshal(payload, &head) == nil && head.Type == "access.changed" {
				h.recheck(projectID, head.UserID)
			}
			h.broadcast(projectID, envelope("event", map[string]any{"seq": id, "event": payload}))
			realtimeLag.Observe(max(age, 0))
		case presenceChannel:
			var m presenceMsg
			if json.Unmarshal([]byte(n.Payload), &m) == nil {
				h.applyPresence(m)
			}
		}
	}
}

func envelope(typ string, data map[string]any) []byte {
	data["type"] = typ
	b, _ := json.Marshal(data)
	return b
}

func (h *Hub) broadcast(projectID string, msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients[projectID] {
		select {
		case c.send <- msg:
		default:
			// Slow consumer: drop it; the client reconnects and replays from its last seq.
			close(c.send)
			delete(h.clients[projectID], c.id)
		}
	}
}

// resyncAll tells every client to reload after the listener reconnects, since notifications may have been missed.
func (h *Hub) resyncAll() {
	h.mu.Lock()
	projects := make([]string, 0, len(h.clients))
	for p := range h.clients {
		projects = append(projects, p)
	}
	h.mu.Unlock()
	for _, p := range projects {
		h.broadcast(p, envelope("resync", map[string]any{}))
	}
}

// Listening reports whether this instance is receiving notifications (used by /readyz).
func (h *Hub) Listening() bool { return h.listening.Load() }

// connections counts this instance's open WebSocket clients.
func (h *Hub) connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, cs := range h.clients {
		n += len(cs)
	}
	return n
}

func (h *Hub) register(projectID string, user PresenceUser) *client {
	h.mu.Lock()
	h.nextID++
	c := &client{id: h.nextID, projectID: projectID, send: make(chan []byte, 256), user: user, recheck: make(chan struct{}, 1)}
	if h.clients[projectID] == nil {
		h.clients[projectID] = map[int64]*client{}
	}
	h.clients[projectID][c.id] = c
	h.mu.Unlock()
	h.announce(c, false)
	return c
}

// recheck asks a user's connections to a project to verify their access again.
func (h *Hub) recheck(projectID, userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients[projectID] {
		if c.user.UserID == userID {
			select {
			case c.recheck <- struct{}{}:
			default:
			}
		}
	}
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	if cs := h.clients[c.projectID]; cs != nil {
		if _, ok := cs[c.id]; ok {
			delete(cs, c.id)
			close(c.send)
		}
		if len(cs) == 0 {
			delete(h.clients, c.projectID)
		}
	}
	h.mu.Unlock()
	h.announce(c, true)
}

func (h *Hub) announce(c *client, left bool) {
	h.mu.Lock()
	user := c.user
	h.mu.Unlock()
	b, _ := json.Marshal(presenceMsg{Instance: h.instance, Conn: c.id, ProjectID: c.projectID, User: user, Left: left})
	if _, err := h.db.Exec(context.Background(), `SELECT pg_notify($1, $2)`, presenceChannel, string(b)); err != nil {
		slog.Warn("announce presence", "err", err)
	}
}

func (h *Hub) applyPresence(m presenceMsg) {
	key := m.Instance + "/" + strconv.FormatInt(m.Conn, 10)
	h.mu.Lock()
	ps := h.presence[m.ProjectID]
	if ps == nil {
		ps = map[string]presenceEntry{}
		h.presence[m.ProjectID] = ps
	}
	prev, existed := ps[key]
	var changed bool
	if m.Left {
		delete(ps, key)
		changed = existed
	} else {
		ps[key] = presenceEntry{user: m.User, seen: time.Now()}
		changed = !existed || !samePresence(prev.user, m.User)
	}
	h.mu.Unlock()
	if changed {
		h.sendPresence(m.ProjectID)
	}
}

func samePresence(a, b PresenceUser) bool {
	return a.UserID == b.UserID && a.Name == b.Name && a.Color == b.Color && eqPtr(a.Viewing, b.Viewing) && eqPtr(a.Editing, b.Editing)
}

// setActivity updates what a connection is looking at; guarded because heartbeats read it concurrently.
func (h *Hub) setActivity(c *client, viewing, editing *string) {
	h.mu.Lock()
	c.user.Viewing, c.user.Editing = viewing, editing
	h.mu.Unlock()
	h.announce(c, false)
}

func (h *Hub) presenceList(projectID string) []PresenceUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []PresenceUser{}
	for _, e := range h.presence[projectID] {
		out = append(out, e.user)
	}
	return out
}

func (h *Hub) sendPresence(projectID string) {
	h.broadcast(projectID, envelope("presence", map[string]any{"users": h.presenceList(projectID)}))
}

func (h *Hub) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.mu.Lock()
		var mine []*client
		for _, cs := range h.clients {
			for _, c := range cs {
				mine = append(mine, c)
			}
		}
		// Expire entries from instances that stopped heartbeating (crashed or partitioned).
		var stale []string
		for p, ps := range h.presence {
			for k, e := range ps {
				if time.Since(e.seen) > presenceTTL {
					delete(ps, k)
					stale = append(stale, p)
				}
			}
		}
		h.mu.Unlock()
		for _, c := range mine {
			h.announce(c, false)
		}
		for _, p := range stale {
			h.sendPresence(p)
		}
	}
}

// live upgrades to a WebSocket streaming the project's events. ?since=<seq> replays anything
// the client missed (e.g. between loading the snapshot and connecting, or while offline).
func (s *Server) live(w http.ResponseWriter, r *http.Request, u *User) error {
	projectID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, role, err := effectiveRole(r.Context(), s.db, projectID, u.ID)
	if err != nil {
		return err
	}
	var level atomic.Int32
	level.Store(int32(role))
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)

	opts := &websocket.AcceptOptions{OriginPatterns: []string{r.Host}}
	for _, o := range s.cfg.AllowedOrigins {
		opts.OriginPatterns = append(opts.OriginPatterns, hostOf(o))
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return nil // Accept already wrote the response
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := s.hub.register(projectID, PresenceUser{UserID: u.ID, Name: u.Name, Color: u.Color})
	defer s.hub.unregister(c)

	// Register first, then replay, so nothing falls in between. Clients ignore seqs they've seen.
	if since > 0 {
		var oldest, newest int64
		_ = s.db.QueryRow(ctx, `SELECT coalesce(min(id), 0), coalesce(max(id), 0) FROM events`).Scan(&oldest, &newest)
		switch {
		case oldest > since+1:
			realtimeResyncs.Inc("replay_pruned")
			_ = conn.Write(ctx, websocket.MessageText, envelope("resync", map[string]any{}))
		case since > newest:
			// The client has seen events this database never had: it was restored from a backup
			// (the events sequence went back) or the client is pointed at another deployment.
			// Its local state can't be trusted, and new events would arrive with seqs it skips.
			realtimeResyncs.Inc("client_ahead")
			_ = conn.Write(ctx, websocket.MessageText, envelope("resync", map[string]any{}))
		default:
			rows, err := s.db.Query(ctx, `SELECT id, payload FROM events WHERE project_id = $1 AND id > $2 ORDER BY id`, projectID, since)
			if err == nil {
				for rows.Next() {
					var id int64
					var payload json.RawMessage
					if rows.Scan(&id, &payload) == nil {
						realtimeReplayed.Inc()
						_ = conn.Write(ctx, websocket.MessageText, envelope("event", map[string]any{"seq": id, "event": payload}))
					}
				}
				rows.Close()
			}
		}
	}
	_ = conn.Write(ctx, websocket.MessageText, envelope("presence", map[string]any{"users": s.hub.presenceList(projectID)}))

	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type    string  `json:"type"`
				Viewing *string `json:"viewing"`
				Editing *string `json:"editing"`
			}
			if json.Unmarshal(data, &msg) != nil || msg.Type != "presence" {
				continue
			}
			if msg.Viewing != nil && !validID(*msg.Viewing) {
				continue
			}
			if msg.Editing != nil && len(*msg.Editing) > 32 {
				continue
			}
			// Every message is checked against the current role: revoked access stops here, and
			// only editors can claim to be editing a field.
			if level.Load() < int32(roleViewer) {
				return
			}
			if level.Load() < int32(roleEditor) {
				msg.Editing = nil
			}
			s.hub.setActivity(c, msg.Viewing, msg.Editing)
		}
	}()

	// Access is re-verified when it changes (membership or role edits publish access.changed) and
	// on every ping, which also catches expired or signed-out sessions.
	stillAllowed := func() bool {
		cu, err := s.currentUser(r)
		if err != nil {
			return errors.Is(err, context.Canceled) // a DB hiccup during shutdown isn't a revocation
		}
		_, role, err := effectiveRole(ctx, s.db, projectID, cu.ID)
		if err != nil {
			level.Store(0)
			return false
		}
		level.Store(int32(role))
		return true
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.recheck:
			if !stillAllowed() {
				conn.Close(websocket.StatusPolicyViolation, "access revoked")
				return nil
			}
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return nil
		case msg, ok := <-c.send:
			if !ok {
				conn.Close(websocket.StatusTryAgainLater, "too slow")
				return nil
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				return nil
			}
		case <-ping.C:
			if !stillAllowed() {
				conn.Close(websocket.StatusPolicyViolation, "access revoked")
				return nil
			}
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return nil
			}
		}
	}
}

func hostOf(origin string) string {
	for i := 0; i+2 < len(origin); i++ {
		if origin[i:i+3] == "://" {
			return origin[i+3:]
		}
	}
	return origin
}
