-- The server computes forecasts now (see forecasting.go). project_forecasts keeps the latest one per
-- project so the background job can tell what changed since its last run.
CREATE TABLE project_forecasts (
  project_id  uuid PRIMARY KEY REFERENCES projects ON DELETE CASCADE,
  computed_at timestamptz NOT NULL DEFAULT now(),
  day         date NOT NULL,
  finish      date,
  finish_p85  date,
  data        jsonb NOT NULL DEFAULT '{}'
);

-- Readings are no longer reported by browsers; record which ones came from the server.
ALTER TABLE forecast_snapshots ADD COLUMN source text NOT NULL DEFAULT 'client' CHECK (source IN ('client', 'server', 'seed'));
