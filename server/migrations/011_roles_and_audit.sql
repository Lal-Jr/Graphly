-- Roles. Workspace: admin (everything), member (edits projects), viewer (reads only).
-- A project can override a person's role there (admin, editor or viewer), e.g. to let a viewer
-- edit one project or to make someone read-only in a sensitive one. Workspace admins are always
-- project admins.
ALTER TABLE workspace_members DROP CONSTRAINT workspace_members_role_check;
ALTER TABLE workspace_members ADD CONSTRAINT workspace_members_role_check CHECK (role IN ('admin', 'member', 'viewer'));
ALTER TABLE invites ADD COLUMN role text NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member', 'viewer'));

CREATE TABLE project_members (
  project_id uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('admin', 'editor', 'viewer')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_idx ON project_members (user_id);

-- Append-only record of sensitive actions. No foreign keys, so deleting a user or workspace
-- never rewrites history; names are copied in at the time.
CREATE TABLE audit_log (
  id           bigserial PRIMARY KEY,
  workspace_id uuid,
  actor_id     uuid,
  actor_name   text,
  action       text NOT NULL,
  target_type  text NOT NULL,
  target_id    text,
  data         jsonb NOT NULL DEFAULT '{}',
  ip           text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_workspace_idx ON audit_log (workspace_id, id DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, id DESC) WHERE workspace_id IS NULL;

CREATE FUNCTION audit_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only';
END $$;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
