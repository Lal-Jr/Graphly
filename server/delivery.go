package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type mailer interface{}

func (s *Server) enqueueDeliveries(ctx context.Context, tx pgx.Tx, notificationID int64, userID string, p channelPrefs) error {
	return nil
}
