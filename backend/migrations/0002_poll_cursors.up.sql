-- Where the poller got to on each branch: the head commit it last synced.
-- A branch whose head still equals last_hash needs no API call for commits.
CREATE TABLE poll_cursors (
    repo_id    bigint NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    branch     text NOT NULL,
    last_hash  text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, branch)
);
