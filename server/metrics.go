package main

import (
	"context"
	"time"
)

func observeForecastJob(d time.Duration, projects int) {}

func observeDeliveryQueue(ctx context.Context, s *Server) {}

func observeDeliveryFailure(channel string) {}
