-- Money (and time) spent on review attempts that did not end in a stored
-- review: the model answered with something unusable, a later chunk failed
-- after earlier ones were paid for, saving failed, the call timed out. Without
-- this the dashboard's cost only counted the attempt that finally worked.
CREATE TABLE review_attempts (
    id          bigserial PRIMARY KEY,
    commit_id   bigint REFERENCES commits(id) ON DELETE CASCADE,
    pr_id       bigint REFERENCES pull_requests(id) ON DELETE CASCADE,
    attempt     integer NOT NULL,
    error       text NOT NULL,
    duration_ms integer,
    -- NULL = the reviewer reported nothing (mock, or a killed call).
    tokens_in   integer,
    tokens_out  integer,
    cost_usd    numeric(10, 4),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT review_attempts_one_subject CHECK ((commit_id IS NULL) <> (pr_id IS NULL))
);
CREATE INDEX review_attempts_commit_idx ON review_attempts (commit_id) WHERE commit_id IS NOT NULL;
CREATE INDEX review_attempts_pr_idx ON review_attempts (pr_id) WHERE pr_id IS NOT NULL;
CREATE INDEX review_attempts_time_idx ON review_attempts (created_at);
