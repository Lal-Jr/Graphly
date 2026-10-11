-- Development links from GitHub and GitLab: merge requests sit beside pull requests, and pushed
-- commits link to the issues their branch name or message mentions.
ALTER TABLE issue_prs ADD COLUMN provider text NOT NULL DEFAULT 'github' CHECK (provider IN ('github', 'gitlab'));
ALTER TABLE issue_prs DROP CONSTRAINT issue_prs_pkey;
ALTER TABLE issue_prs ADD PRIMARY KEY (issue_id, provider, repo, number);

ALTER TABLE projects ADD COLUMN gitlab_secret text;

CREATE TABLE issue_commits (
  issue_id     uuid NOT NULL REFERENCES issues ON DELETE CASCADE,
  provider     text NOT NULL CHECK (provider IN ('github', 'gitlab')),
  repo         text NOT NULL,
  sha          text NOT NULL,
  message      text NOT NULL,
  url          text NOT NULL,
  author       text NOT NULL DEFAULT '',
  branch       text NOT NULL DEFAULT '',
  committed_at timestamptz,
  linked_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issue_id, provider, repo, sha)
);
