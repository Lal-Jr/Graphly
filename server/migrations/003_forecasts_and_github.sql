-- One forecast reading per project per day, so the standup can show how (and why) the finish date moved.
CREATE TABLE forecast_snapshots (
  project_id  uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
  day         date NOT NULL,
  finish      date,
  finish_p85  date,
  remaining   double precision NOT NULL,
  open_issues integer NOT NULL,
  blocked     integer NOT NULL,
  taken_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, day)
);

-- GitHub integration: a per-project webhook secret, and the pull requests that mention each issue.
ALTER TABLE projects ADD COLUMN github_secret text;

CREATE TABLE issue_prs (
  issue_id   uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  repo       text NOT NULL,
  number     integer NOT NULL,
  title      text NOT NULL,
  url        text NOT NULL,
  state      text NOT NULL CHECK (state IN ('open', 'draft', 'merged', 'closed')),
  author     text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issue_id, repo, number)
);
