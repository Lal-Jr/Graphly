-- Imports from Jira and Linear. import_mappings remembers which Graphly row each external
-- project, issue and comment became, so running the same import again updates instead of duplicating.
CREATE TABLE import_mappings (
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  source       text NOT NULL CHECK (source IN ('jira', 'linear')),
  kind         text NOT NULL CHECK (kind IN ('project', 'issue', 'comment')),
  external_id  text NOT NULL,
  target_id    uuid NOT NULL,
  imported_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, source, kind, external_id)
);

CREATE TABLE imports (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
  user_id      uuid REFERENCES users ON DELETE SET NULL,
  source       text NOT NULL,
  format       text NOT NULL,
  report       jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX imports_workspace_idx ON imports (workspace_id, created_at DESC);
