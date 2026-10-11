package main

import (
	"context"
	"net/http"
)

// Project roles, weakest first. Every project route names the least role it needs.
type projectRoleLevel int

const (
	roleViewer projectRoleLevel = iota + 1
	roleEditor
	roleAdmin
)

// projectRole checks the user can act on a project with at least role `need`, and returns the
// project's workspace. Non-members get errNotFound so project ids can't be probed.
func (s *Server) projectRole(ctx context.Context, q querier, projectID string, u *User, need projectRoleLevel) (string, error) {
	wsID, role, err := s.projectAccess(ctx, q, projectID, u.ID)
	if err != nil {
		return "", err
	}
	have := roleEditor
	if role == "admin" {
		have = roleAdmin
	}
	if have < need {
		return "", errStatus(http.StatusForbidden, "you don't have permission to do that in this project")
	}
	return wsID, nil
}

// issueRole is projectRole for the project an issue belongs to; it returns the project id.
func (s *Server) issueRole(ctx context.Context, q querier, issueID string, u *User, need projectRoleLevel) (string, error) {
	projectID, err := s.issueProject(ctx, q, issueID, u.ID)
	if err != nil {
		return "", err
	}
	if _, err := s.projectRole(ctx, q, projectID, u, need); err != nil {
		return "", err
	}
	return projectID, nil
}
