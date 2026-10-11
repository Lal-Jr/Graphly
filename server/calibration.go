package main

import "context"

// Calibration describes the overrun model a forecast used.
type Calibration struct {
	Spreads map[string][2]float64 `json:"spreads"`
}

func (s *Server) calibrate(ctx context.Context, q querier, projectID string) (*Calibration, error) {
	return nil, nil
}
