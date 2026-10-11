-- People following an issue. Reporters, assignees, commenters and anyone mentioned are added
-- automatically; anyone can watch or unwatch by hand.
CREATE TABLE issue_watchers (
  issue_id   uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issue_id, user_id)
);
CREATE INDEX issue_watchers_user_idx ON issue_watchers (user_id);
INSERT INTO issue_watchers (issue_id, user_id) SELECT id, reporter_id FROM issues WHERE reporter_id IS NOT NULL ON CONFLICT DO NOTHING;
INSERT INTO issue_watchers (issue_id, user_id) SELECT id, assignee_id FROM issues WHERE assignee_id IS NOT NULL ON CONFLICT DO NOTHING;
INSERT INTO issue_watchers (issue_id, user_id) SELECT DISTINCT issue_id, author_id FROM comments WHERE author_id IS NOT NULL ON CONFLICT DO NOTHING;

-- One row per thing a person should hear about. dedupe_key stops the same event (a retried job, a
-- redelivered webhook) from notifying twice. inbox is false when the person turned in-app off for
-- this kind but still wants it by email or Slack.
CREATE TABLE notifications (
  id           bigserial PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  project_id   uuid REFERENCES projects ON DELETE CASCADE,
  issue_id     uuid REFERENCES issues ON DELETE CASCADE,
  kind         text NOT NULL CHECK (kind IN ('unblocked', 'late', 'mention', 'status', 'date_moved')),
  actor_id     uuid REFERENCES users ON DELETE SET NULL,
  data         jsonb NOT NULL DEFAULT '{}',
  dedupe_key   text,
  inbox        boolean NOT NULL DEFAULT true,
  created_at   timestamptz NOT NULL DEFAULT now(),
  read_at      timestamptz
);
CREATE UNIQUE INDEX notifications_dedupe_key ON notifications (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX notifications_user_idx ON notifications (user_id, id DESC) WHERE inbox;
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE inbox AND read_at IS NULL;

-- Per-user channel preferences: {"<kind>": {"inApp": bool, "email": bool, "slack": bool}}; missing
-- entries use the defaults in notifications.go.
CREATE TABLE notification_settings (
  user_id           uuid PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  prefs             jsonb NOT NULL DEFAULT '{}',
  slack_webhook_url text,
  updated_at        timestamptz NOT NULL DEFAULT now()
);

-- The ship date people were last told about, so a slow creep still alerts once it adds up.
ALTER TABLE project_forecasts ADD COLUMN notified_finish date;
