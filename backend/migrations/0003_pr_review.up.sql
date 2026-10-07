-- Pull request reviews: one review of a PR's whole diff (all its commits as
-- one change), on top of the per-commit reviews.
--
-- pull_requests existed since 0001 but nothing wrote to it. The new columns
-- track the PR's current state in Bitbucket and where its review stands.
ALTER TABLE pull_requests
    ADD COLUMN description        text NOT NULL DEFAULT '',
    -- Head commit of the source branch as Bitbucket reports it (a short hash).
    -- A change of this value is what makes a PR need a new review.
    ADD COLUMN source_hash        text,
    ADD COLUMN bb_created_on      timestamptz,
    ADD COLUMN bb_updated_on      timestamptz,
    ADD COLUMN review_status      text NOT NULL DEFAULT 'pending'
        CHECK (review_status IN ('pending', 'running', 'done', 'skipped', 'failed')),
    ADD COLUMN review_skip_reason text,
    ADD COLUMN files_changed      integer,
    ADD COLUMN additions          integer,
    ADD COLUMN deletions          integer,
    ADD COLUMN updated_at         timestamptz NOT NULL DEFAULT now();

CREATE INDEX pull_requests_repo_idx   ON pull_requests (repo_id, bb_updated_on DESC);
CREATE INDEX pull_requests_author_idx ON pull_requests (author_user_id);
CREATE INDEX pull_requests_open_idx   ON pull_requests (repo_id) WHERE state = 'OPEN';

-- A review belongs to a commit or to a pull request, never both. Findings and
-- suggestions hang off the review, so they work for both without change.
ALTER TABLE reviews
    ALTER COLUMN commit_id DROP NOT NULL,
    ADD COLUMN pr_id        bigint REFERENCES pull_requests(id) ON DELETE CASCADE,
    -- The source head the reviewed diff was taken at.
    ADD COLUMN pr_head_hash text,
    ADD CONSTRAINT reviews_one_subject CHECK ((commit_id IS NULL) <> (pr_id IS NULL));

CREATE INDEX reviews_pr_idx ON reviews (pr_id, created_at DESC) WHERE pr_id IS NOT NULL;
