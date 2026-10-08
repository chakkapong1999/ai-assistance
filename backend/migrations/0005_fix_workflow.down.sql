DROP TABLE IF EXISTS finding_events;
ALTER TABLE reviews
    DROP CONSTRAINT IF EXISTS reviews_close_note_needs_close,
    DROP COLUMN IF EXISTS close_note,
    DROP COLUMN IF EXISTS closed_by,
    DROP COLUMN IF EXISTS closed_at;
ALTER TABLE review_findings
    DROP COLUMN IF EXISTS status_at,
    DROP COLUMN IF EXISTS status_note,
    DROP COLUMN IF EXISTS status_by,
    DROP COLUMN IF EXISTS status;
