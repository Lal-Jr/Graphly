package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// audit records a sensitive action. The append-only log lands with roles (#22).
func (s *Server) audit(ctx context.Context, tx pgx.Tx, workspaceID, actorID, action, targetType, targetID string, data map[string]any) error {
	return nil
}
