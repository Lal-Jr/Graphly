package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The audit log records membership and role changes, invites, API tokens, sign-in links,
// webhook changes, imports and deletions. Rows can't be changed or removed (a trigger refuses),
// and workspace admins can read their workspace's log in Settings.

type auditCtxKey struct{}

// withAuditIP lets audit() record the caller's address without threading the request through.
func withAuditIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, auditCtxKey{}, ip)
}

func auditIP(ctx context.Context) string {
	ip, _ := ctx.Value(auditCtxKey{}).(string)
	return ip
}

// audit records an action. workspaceID may be empty for account-level actions (tokens, sign-in).
func (s *Server) audit(ctx context.Context, tx pgx.Tx, workspaceID, actorID, action, targetType, targetID string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	ip := auditIP(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_id, actor_name, action, target_type, target_id, data, ip)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, (SELECT name FROM users WHERE id = NULLIF($2, '')::uuid), $3, $4, NULLIF($5, ''), $6, NULLIF($7, ''))`,
		workspaceID, actorID, action, targetType, targetID, raw, ip)
	return err
}

// auditUser records an action on a user's own account (no workspace).
func (s *Server) auditUser(ctx context.Context, tx pgx.Tx, actorID, action, targetType, targetID string, data map[string]any) error {
	return s.audit(ctx, tx, "", actorID, action, targetType, targetID, data)
}

type auditEntry struct {
	ID         int64           `json:"id"`
	ActorID    *string         `json:"actorId"`
	ActorName  *string         `json:"actorName"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   *string         `json:"targetId"`
	Data       json.RawMessage `json:"data"`
	IP         *string         `json:"ip"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// listAudit returns the workspace's log, plus account-level actions (like creating API tokens) by
// its current members, newest first. ?before=<id> pages back.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
		return err
	}
	before := int64(1 << 62)
	if v := r.URL.Query().Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil {
			return badRequest("before must be an entry id")
		}
	}
	entries, err := collect(func(row pgx.Row) (auditEntry, error) {
		var e auditEntry
		return e, row.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.Action, &e.TargetType, &e.TargetID, &e.Data, &e.IP, &e.CreatedAt)
	})(s.db.Query(r.Context(), `
		SELECT id, actor_id, actor_name, action, target_type, target_id, data, ip, created_at FROM audit_log
		WHERE id < $2 AND (workspace_id = $1 OR (workspace_id IS NULL AND actor_id IN (SELECT user_id FROM workspace_members WHERE workspace_id = $1)))
		ORDER BY id DESC LIMIT 100`, wsID, before))
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []auditEntry{}
	}
	return ok(w, entries)
}
