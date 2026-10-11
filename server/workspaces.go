package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const inviteTTL = 7 * 24 * time.Hour

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// role returns the user's role in a workspace, or errNotFound if they aren't a member
// (not-found rather than forbidden, so workspace ids can't be probed).
func (s *Server) role(ctx context.Context, q querier, workspaceID, userID string) (string, error) {
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", errNotFound
	}
	return role, err
}

var workspaceRoles = map[string]bool{"admin": true, "member": true, "viewer": true}

func (s *Server) requireAdmin(ctx context.Context, workspaceID, userID string) error {
	role, err := s.role(ctx, s.db, workspaceID, userID)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errStatus(http.StatusForbidden, "only workspace admins can do that")
	}
	return nil
}

// accessChanged tells every open connection of a user in the workspace (or one project) to
// re-check their access, so removals and demotions take effect on live sockets at once.
func accessChanged(ctx context.Context, tx pgx.Tx, wsID, projectID, userID string) error {
	projects := []string{projectID}
	if projectID == "" {
		var err error
		if projects, err = collect(scanString)(tx.Query(ctx, `SELECT id FROM projects WHERE workspace_id = $1 ORDER BY id`, wsID)); err != nil {
			return err
		}
	}
	// Always in id order, so two transactions that each publish to several projects take the
	// per-project event locks in the same order and can't deadlock.
	for _, p := range projects {
		if err := publish(ctx, tx, p, "access.changed", map[string]any{"userId": userID}); err != nil {
			return err
		}
	}
	return nil
}

func createWorkspaceTx(ctx context.Context, tx pgx.Tx, name, ownerID string) (Workspace, error) {
	ws := Workspace{Name: name, Role: "admin"}
	if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name) VALUES ($1) RETURNING id`, name).Scan(&ws.ID); err != nil {
		return ws, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, ws.ID, ownerID)
	return ws, err
}

func cleanTitle(v string, max int, what string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || utf8.RuneCountInString(v) > max {
		return "", badRequest(what + " is required (max " + strconv.Itoa(max) + " characters)")
	}
	return v, nil
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request, u *User) error {
	var body struct{ Name string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	name, err := cleanTitle(body.Name, 80, "workspace name")
	if err != nil {
		return err
	}
	var ws Workspace
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		ws, err = createWorkspaceTx(r.Context(), tx, name, u.ID)
		return err
	})
	if err != nil {
		return err
	}
	return ok(w, ws)
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
		return err
	}
	var body struct{ Name string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	name, err := cleanTitle(body.Name, 80, "workspace name")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE workspaces SET name = $2 WHERE id = $1`, wsID, name); err != nil {
		return err
	}
	return ok(w, Workspace{ID: wsID, Name: name, Role: "admin"})
}

func (s *Server) members(ctx context.Context, q querier, workspaceID string) ([]Member, error) {
	return collect(func(row pgx.Row) (Member, error) {
		var m Member
		return m, row.Scan(&m.ID, &m.Email, &m.Name, &m.Color, &m.Role, &m.JoinedAt)
	})(q.Query(ctx, `
		SELECT u.id, u.email, u.name, u.color, m.role, m.joined_at
		FROM workspace_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 ORDER BY u.name`, workspaceID))
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if _, err := s.role(r.Context(), s.db, wsID, u.ID); err != nil {
		return err
	}
	ms, err := s.members(r.Context(), s.db, wsID)
	if err != nil {
		return err
	}
	return ok(w, ms)
}

// lastAdminGuard stops a change that would leave a workspace without an admin.
func lastAdminGuard(ctx context.Context, tx pgx.Tx, wsID, userID string) error {
	var others int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'admin' AND user_id <> $2`,
		wsID, userID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return badRequest("a workspace needs at least one admin")
	}
	return nil
}

func (s *Server) updateMember(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	target, err := pathID(r, "user")
	if err != nil {
		return err
	}
	if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
		return err
	}
	var body struct{ Role string }
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if !workspaceRoles[body.Role] {
		return badRequest("role must be admin, member or viewer")
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if body.Role != "admin" {
			if err := lastAdminGuard(r.Context(), tx, wsID, target); err != nil {
				return err
			}
		}
		var from string
		if err := tx.QueryRow(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2 FOR UPDATE`, wsID, target).Scan(&from); err != nil {
			return err
		}
		if from == body.Role {
			return ok(w, map[string]bool{"ok": true})
		}
		if _, err := tx.Exec(r.Context(), `UPDATE workspace_members SET role = $3 WHERE workspace_id = $1 AND user_id = $2`, wsID, target, body.Role); err != nil {
			return err
		}
		if err := s.audit(r.Context(), tx, wsID, u.ID, "member.role", "user", target, map[string]any{"from": from, "to": body.Role}); err != nil {
			return err
		}
		if err := accessChanged(r.Context(), tx, wsID, "", target); err != nil {
			return err
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	target, err := pathID(r, "user")
	if err != nil {
		return err
	}
	// Anyone may leave; only admins may remove others.
	if target != u.ID {
		if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
			return err
		}
	} else if _, err := s.role(r.Context(), s.db, wsID, u.ID); err != nil {
		return err
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if err := lastAdminGuard(r.Context(), tx, wsID, target); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, wsID, target)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM project_members WHERE user_id = $2 AND project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`, wsID, target); err != nil {
			return err
		}
		action := "member.remove"
		if target == u.ID {
			action = "member.leave"
		}
		if err := s.audit(r.Context(), tx, wsID, u.ID, action, "user", target, nil); err != nil {
			return err
		}
		if err := accessChanged(r.Context(), tx, wsID, "", target); err != nil {
			return err
		}
		// Unassign their open work in this workspace so it doesn't silently stall.
		freed, err := collect(scanIssue)(tx.Query(r.Context(), `UPDATE issues AS i SET assignee_id = NULL, version = version + 1, updated_at = now()
			WHERE assignee_id = $2 AND project_id IN (SELECT id FROM projects WHERE workspace_id = $1) RETURNING `+issueCols, wsID, target))
		if err != nil {
			return err
		}
		for _, is := range freed {
			if err := publish(r.Context(), tx, is.ProjectID, "issue.upsert", map[string]any{"issue": is}); err != nil {
				return err
			}
		}
		return ok(w, map[string]bool{"ok": true})
	})
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
		return err
	}
	var body struct{ Role string }
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			return err
		}
	}
	if body.Role == "" {
		body.Role = "member"
	}
	if !workspaceRoles[body.Role] {
		return badRequest("role must be admin, member or viewer")
	}
	token := randomToken(18)
	expires := time.Now().Add(inviteTTL)
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO invites (token, workspace_id, created_by, expires_at, role) VALUES ($1, $2, $3, $4, $5)`,
			token, wsID, u.ID, expires, body.Role); err != nil {
			return err
		}
		// The token is a credential: log only a fingerprint.
		return s.audit(r.Context(), tx, wsID, u.ID, "invite.create", "invite", token[:6], map[string]any{"role": body.Role, "expiresAt": expires})
	})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"token": token, "expiresAt": expires, "role": body.Role})
}

func (s *Server) previewInvite(w http.ResponseWriter, r *http.Request) error {
	var wsName, inviter string
	err := s.db.QueryRow(r.Context(), `
		SELECT w.name, u.name FROM invites i JOIN workspaces w ON w.id = i.workspace_id JOIN users u ON u.id = i.created_by
		WHERE i.token = $1 AND i.expires_at > now()`, r.PathValue("token")).Scan(&wsName, &inviter)
	if err != nil {
		return err
	}
	return ok(w, map[string]string{"workspace": wsName, "invitedBy": inviter})
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request, u *User) error {
	var wsID, role string
	err := s.db.QueryRow(r.Context(), `SELECT workspace_id, role FROM invites WHERE token = $1 AND expires_at > now()`, r.PathValue("token")).Scan(&wsID, &role)
	if err != nil {
		return err
	}
	joined := false
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, wsID, u.ID, role)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		joined = true
		return s.audit(r.Context(), tx, wsID, u.ID, "member.join", "user", u.ID, map[string]any{"role": role, "invite": r.PathValue("token")[:6]})
	}); err != nil {
		return err
	}
	if !joined {
		return ok(w, map[string]string{"workspaceId": wsID})
	}
	// Let open boards in the workspace learn about the new assignable person.
	projects, err := collect(scanString)(s.db.Query(r.Context(), `SELECT id FROM projects WHERE workspace_id = $1`, wsID))
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
			return publish(r.Context(), tx, p, "member.join", map[string]any{"member": Member{User: *u, Role: role, JoinedAt: time.Now()}})
		}); err != nil {
			return err
		}
	}
	return ok(w, map[string]string{"workspaceId": wsID})
}

func scanString(row pgx.Row) (string, error) {
	var s string
	return s, row.Scan(&s)
}
