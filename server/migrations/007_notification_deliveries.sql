-- Outbox for email and Slack. A worker claims due rows with FOR UPDATE SKIP LOCKED (so several
-- instances never send the same row), groups each person's pending rows per channel into one
-- digest, and retries failures with exponential backoff until it gives up ('failed').
CREATE TABLE notification_deliveries (
  id              bigserial PRIMARY KEY,
  notification_id bigint NOT NULL REFERENCES notifications ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  channel         text NOT NULL CHECK (channel IN ('email', 'slack')),
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  attempts        integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz,
  UNIQUE (notification_id, channel)
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (user_id, channel, next_attempt_at) WHERE status = 'pending';
