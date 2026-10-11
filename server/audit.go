package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// auditUser records an action on a user's own account (no workspace).
func (s *Server) auditUser(ctx context.Context, tx pgx.Tx, actorID, action, targetType, targetID string, data map[string]any) error {
	return s.audit(ctx, tx, "", actorID, action, targetType, targetID, data)
}

// audit records a sensitive action. The append-only log lands with roles (#22).
func (s *Server) audit(ctx context.Context, tx pgx.Tx, workspaceID, actorID, action, targetType, targetID string, data map[string]any) error {
	return nil
}
