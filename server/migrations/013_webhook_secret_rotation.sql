-- Rotating a webhook secret keeps the previous one valid for a grace period, so deliveries signed
-- with the old secret (in flight, or before the new one is pasted into GitHub/GitLab) still land.
ALTER TABLE projects
  ADD COLUMN github_secret_prev       text,
  ADD COLUMN github_secret_prev_until timestamptz,
  ADD COLUMN gitlab_secret_prev       text,
  ADD COLUMN gitlab_secret_prev_until timestamptz;
