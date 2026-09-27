CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text NOT NULL,
  name          text NOT NULL,
  password_hash text NOT NULL,
  color         text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE sessions (
  token_hash bytea PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE workspaces (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  user_id      uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  role         text NOT NULL CHECK (role IN ('admin', 'member')),
  joined_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_idx ON workspace_members (user_id);

CREATE TABLE invites (
  token        text PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  created_by   uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL
);

CREATE TABLE projects (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  key          text NOT NULL CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
  name         text NOT NULL,
  description  text NOT NULL DEFAULT '',
  issue_seq    integer NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, key)
);

CREATE TABLE statuses (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  name       text NOT NULL,
  category   text NOT NULL CHECK (category IN ('todo', 'in_progress', 'done')),
  position   integer NOT NULL
);
CREATE INDEX statuses_project_idx ON statuses (project_id);

CREATE TABLE issues (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  number      integer NOT NULL,
  type        text NOT NULL CHECK (type IN ('story', 'task', 'bug', 'epic')),
  title       text NOT NULL,
  description text NOT NULL DEFAULT '',
  status_id   uuid NOT NULL REFERENCES statuses,
  priority    text NOT NULL CHECK (priority IN ('lowest', 'low', 'medium', 'high', 'highest')),
  assignee_id uuid REFERENCES users ON DELETE SET NULL,
  reporter_id uuid REFERENCES users ON DELETE SET NULL,
  estimate    double precision NOT NULL DEFAULT 1 CHECK (estimate >= 0),
  due_date    date,
  labels      text[] NOT NULL DEFAULT '{}',
  epic_id     uuid REFERENCES issues ON DELETE SET NULL,
  rank        double precision NOT NULL,
  version     integer NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, number)
);
CREATE INDEX issues_project_idx ON issues (project_id);

-- `blocked_id` cannot start until `blocker_id` is done.
CREATE TABLE issue_links (
  blocker_id uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  blocked_id uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (blocker_id, blocked_id),
  CHECK (blocker_id <> blocked_id)
);
CREATE INDEX issue_links_blocked_idx ON issue_links (blocked_id);

CREATE TABLE comments (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  issue_id   uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  author_id  uuid REFERENCES users ON DELETE SET NULL,
  body       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_issue_idx ON comments (issue_id, created_at);

CREATE TABLE activity (
  id         bigserial PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  issue_id   uuid REFERENCES issues ON DELETE CASCADE,
  actor_id   uuid REFERENCES users ON DELETE SET NULL,
  kind       text NOT NULL,
  data       jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX activity_issue_idx ON activity (issue_id, id);
CREATE INDEX activity_project_idx ON activity (project_id, id);

-- Outbox of realtime events. Rows are announced with NOTIFY so every server
-- instance can fan them out, and let reconnecting clients replay what they missed.
CREATE TABLE events (
  id         bigserial PRIMARY KEY,
  project_id uuid NOT NULL,
  payload    jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_project_idx ON events (project_id, id);
