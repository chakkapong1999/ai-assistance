-- Who changed what, and when. One row per change made through the API: a
-- finding marked fixed, sent back or dismissed, a review closed, review
-- switched on or off for a repository, a re-review asked for.
--
-- actor_id has no foreign key on purpose: deleting a user must not touch the
-- log, and actor_name keeps the name as it was then. actor_id is NULL for a
-- token that is not linked to a user (the role is still recorded).
CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor_id     bigint,
    actor_name   text,
    actor_role   text NOT NULL,
    action       text NOT NULL,
    subject_type text NOT NULL,
    subject_id   bigint NOT NULL,
    detail       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC, id DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, id DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_log_subject_idx ON audit_log (subject_type, subject_id, id DESC);

-- Entries are never edited or removed by the application. (TRUNCATE is still
-- possible for whoever owns the database, e.g. a retention job.)
CREATE FUNCTION audit_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END $$;

CREATE TRIGGER audit_log_no_change BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();
