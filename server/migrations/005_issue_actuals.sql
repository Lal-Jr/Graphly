-- When work actually started and finished, so forecasts can learn how a team's estimates overrun.
-- started_at is the first move into an in-progress column; completed_at the move into done (cleared
-- if the issue is reopened). Issues that skip straight to done have no start, so no actual.
ALTER TABLE issues ADD COLUMN started_at timestamptz;
ALTER TABLE issues ADD COLUMN completed_at timestamptz;

-- One row per finished issue that had an estimate: its estimate and how many working days it took.
CREATE TABLE issue_actuals (
  issue_id     uuid PRIMARY KEY REFERENCES issues ON DELETE CASCADE,
  project_id   uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  type         text NOT NULL,
  estimate     double precision NOT NULL CHECK (estimate > 0),
  actual_days  double precision NOT NULL CHECK (actual_days > 0),
  started_at   timestamptz NOT NULL,
  completed_at timestamptz NOT NULL
);
CREATE INDEX issue_actuals_project_idx ON issue_actuals (project_id, completed_at DESC);

-- Backfill from the status history recorded so far.
UPDATE issues i SET started_at = h.at FROM (
  SELECT a.issue_id, min(a.created_at) AS at
  FROM activity a JOIN statuses st ON st.id::text = a.data->>'to'
  WHERE a.kind = 'updated' AND a.data->>'field' = 'status' AND st.category = 'in_progress' AND a.issue_id IS NOT NULL
  GROUP BY a.issue_id
) h WHERE h.issue_id = i.id;

UPDATE issues i SET completed_at = h.at FROM (
  SELECT a.issue_id, max(a.created_at) AS at
  FROM activity a JOIN statuses st ON st.id::text = a.data->>'to'
  WHERE a.kind = 'updated' AND a.data->>'field' = 'status' AND st.category = 'done' AND a.issue_id IS NOT NULL
  GROUP BY a.issue_id
) h, statuses cur
WHERE h.issue_id = i.id AND cur.id = i.status_id AND cur.category = 'done';

-- Working days are Monday to Friday, counted inclusively and in UTC for the backfill.
INSERT INTO issue_actuals (issue_id, project_id, type, estimate, actual_days, started_at, completed_at)
SELECT i.id, i.project_id, i.type, i.estimate,
  greatest(1, (SELECT count(*) FROM generate_series((i.started_at AT TIME ZONE 'UTC')::date, (i.completed_at AT TIME ZONE 'UTC')::date, interval '1 day') d
               WHERE extract(isodow FROM d) < 6)),
  i.started_at, i.completed_at
FROM issues i
WHERE i.started_at IS NOT NULL AND i.completed_at IS NOT NULL AND i.completed_at >= i.started_at
  AND i.estimate > 0 AND i.type <> 'epic';
