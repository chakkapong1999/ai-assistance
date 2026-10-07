-- Fix workflow: after the AI review, the author marks each finding as fixed,
-- and a senior, lead or admin looks again and closes the whole review.
--
-- Findings start 'open'. The author moves one to 'fixed'. A reviewer can send
-- it back to 'open' (still broken) or set it to 'dismissed' (not a real
-- problem). A review can be closed once no finding is open.
ALTER TABLE review_findings
    ADD COLUMN status         text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'fixed', 'dismissed')),
    ADD COLUMN status_by      bigint REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN status_note    text,
    ADD COLUMN status_at      timestamptz;

ALTER TABLE reviews
    ADD COLUMN closed_at  timestamptz,
    ADD COLUMN closed_by  bigint REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN close_note text,
    -- closed_by may become NULL when a user is deleted, so the pair is not constrained.
    ADD CONSTRAINT reviews_close_note_needs_close CHECK (close_note IS NULL OR closed_at IS NOT NULL);

-- Every change, so "fixed, then sent back, then fixed again" stays readable.
CREATE TABLE finding_events (
    id         bigserial PRIMARY KEY,
    finding_id bigint NOT NULL REFERENCES review_findings(id) ON DELETE CASCADE,
    actor_id   bigint REFERENCES users(id) ON DELETE SET NULL,
    action     text NOT NULL CHECK (action IN ('fixed', 'reopened', 'dismissed')),
    note       text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX finding_events_finding_idx ON finding_events (finding_id, id);
