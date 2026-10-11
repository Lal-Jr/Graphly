-- Sign-in with Google and GitHub. An account can have a password, any number of linked
-- identities, or both. SSO-only accounts have no password (password_hash IS NULL).
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
-- Set once a provider has vouched for the address. Linking by email only ever happens on a
-- verified provider email; see linkIdentity.
ALTER TABLE users ADD COLUMN email_verified boolean NOT NULL DEFAULT false;

CREATE TABLE user_identities (
  provider   text NOT NULL CHECK (provider IN ('google', 'github')),
  subject    text NOT NULL,
  user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  email      text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_login timestamptz,
  PRIMARY KEY (provider, subject),
  UNIQUE (user_id, provider)
);
