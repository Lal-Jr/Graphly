package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Permissions.
//
// Workspace roles: admin, member, viewer. Project roles: admin, editor, viewer. A person's role in
// a project is, in order: admin if they're a workspace admin; their project override if one is
// set; otherwise editor for workspace members and viewer for workspace viewers.
//
//	viewer  read everything in the project, watch issues, see forecasts
//	editor  + create, edit and delete issues, links and comments
//	admin   + project settings, workflow, members' project roles, integrations, deleting the project
//
// Workspace admins also manage members, invites, imports and see the audit log. Every route checks
// the role it needs (projectRole/issueRole/workspaceRole); the WebSocket re-checks on every
// message and whenever someone's access changes.

type projectRoleLevel int

const (
	roleViewer projectRoleLevel = iota + 1
	roleEditor
	roleAdmin
)

var roleNames = map[projectRoleLevel]string{roleViewer: "viewer", roleEditor: "editor", roleAdmin: "admin"}

func (l projectRoleLevel) String() string { return roleNames[l] }

var projectRoleByName = map[string]projectRoleLevel{"viewer": roleViewer, "editor": roleEditor, "admin": roleAdmin}

func errNeed(need projectRoleLevel) error {
	switch need {
	case roleAdmin:
		return errStatus(http.StatusForbidden, "only project admins can do that")
	case roleEditor:
		return errStatus(http.StatusForbidden, "you have view-only access to this project")
	}
	return errForbidden
}

// effectiveRole returns the project's workspace and the user's role in it. Non-members get
// errNotFound so project ids can't be probed.
func effectiveRole(ctx context.Context, q querier, projectID, userID string) (string, projectRoleLevel, error) {
	var wsID, wsRole string
	var override *string
	err := q.QueryRow(ctx, `
		SELECT p.workspace_id, m.role, pm.role FROM projects p
		JOIN workspace_members m ON m.workspace_id = p.workspace_id AND m.user_id = $2
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $2
		WHERE p.id = $1`, projectID, userID).Scan(&wsID, &wsRole, &override)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, errNotFound
	}
	if err != nil {
		return "", 0, err
	}
	return wsID, combineRoles(wsRole, override), nil
}

func combineRoles(wsRole string, override *string) projectRoleLevel {
	switch {
	case wsRole == "admin":
		return roleAdmin
	case override != nil:
		return projectRoleByName[*override]
	case wsRole == "member":
		return roleEditor
	}
	return roleViewer
}

// projectRole checks the user has at least `need` in a project and returns its workspace.
func (s *Server) projectRole(ctx context.Context, q querier, projectID string, u *User, need projectRoleLevel) (string, error) {
	wsID, have, err := effectiveRole(ctx, q, projectID, u.ID)
	if err != nil {
		return "", err
	}
	if have < need {
		return "", errNeed(need)
	}
	return wsID, nil
}

// issueRole is projectRole for the project an issue belongs to; it returns the project id.
func (s *Server) issueRole(ctx context.Context, q querier, issueID string, u *User, need projectRoleLevel) (string, error) {
	var projectID string
	if err := q.QueryRow(ctx, `SELECT project_id FROM issues WHERE id = $1`, issueID).Scan(&projectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errNotFound
		}
		return "", err
	}
	if _, err := s.projectRole(ctx, q, projectID, u, need); err != nil {
		return "", err
	}
	return projectID, nil
}

// workspaceRole checks membership and, when write is set, that the user isn't a viewer.
func (s *Server) workspaceRole(ctx context.Context, q querier, wsID string, u *User, write bool) (string, error) {
	role, err := s.role(ctx, q, wsID, u.ID)
	if err != nil {
		return "", err
	}
	if write && role == "viewer" {
		return "", errStatus(http.StatusForbidden, "viewers can't do that")
	}
	return role, nil
}

type projectMember struct {
	UserID        string  `json:"userId"`
	WorkspaceRole string  `json:"workspaceRole"`
	Override      *string `json:"override"`
	Role          string  `json:"role"` // effective
}

// listProjectMembers returns everyone's effective role in a project.
func (s *Server) listProjectMembers(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	wsID, err := s.projectRole(r.Context(), s.db, id, u, roleViewer)
	if err != nil {
		return err
	}
	ms, err := collect(func(row pgx.Row) (projectMember, error) {
		var m projectMember
		err := row.Scan(&m.UserID, &m.WorkspaceRole, &m.Override)
		m.Role = combineRoles(m.WorkspaceRole, m.Override).String()
		return m, err
	})(s.db.Query(r.Context(), `SELECT m.user_id, m.role, pm.role FROM workspace_members m
		LEFT JOIN project_members pm ON pm.project_id = $2 AND pm.user_id = m.user_id
		WHERE m.workspace_id = $1 ORDER BY m.joined_at`, wsID, id))
	if err != nil {
		return err
	}
	return ok(w, ms)
}

// setProjectMember sets (or with role null, clears) someone's role override in a project.
func (s *Server) setProjectMember(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	target, err := pathID(r, "user")
	if err != nil {
		return err
	}
	var body struct {
		Role *string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if body.Role != nil && projectRoleByName[*body.Role] == 0 {
		return badRequest("role must be admin, editor, viewer or null")
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		wsID, err := s.projectRole(r.Context(), tx, id, u, roleAdmin)
		if err != nil {
			return err
		}
		wsRole, err := s.role(r.Context(), tx, wsID, target)
		if err != nil {
			return badRequest("that person isn't in this workspace")
		}
		if wsRole == "admin" {
			return badRequest("workspace admins are always project admins")
		}
		var before *string
		_ = tx.QueryRow(r.Context(), `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, id, target).Scan(&before)
		if body.Role == nil {
			_, err = tx.Exec(r.Context(), `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, id, target)
		} else {
			_, err = tx.Exec(r.Context(), `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
				ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now()`, id, target, *body.Role)
		}
		if err != nil {
			return err
		}
		if err := s.audit(r.Context(), tx, wsID, u.ID, "project.member.role", "user", target, map[string]any{"project": id, "from": before, "to": body.Role}); err != nil {
			return err
		}
		if err := accessChanged(r.Context(), tx, wsID, id, target); err != nil {
			return err
		}
		return ok(w, projectMember{UserID: target, WorkspaceRole: wsRole, Override: body.Role, Role: combineRoles(wsRole, body.Role).String()})
	})
}
