-- Personal API tokens. Only a SHA-256 of the token is stored; the token is shown once at creation.
CREATE TABLE api_tokens (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  name         text NOT NULL,
  prefix       text NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  scope        text NOT NULL CHECK (scope IN ('read', 'write')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz,
  last_used_at timestamptz,
  revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id, created_at DESC);
