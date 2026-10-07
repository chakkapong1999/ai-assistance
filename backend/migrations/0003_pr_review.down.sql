DROP INDEX IF EXISTS reviews_pr_idx;
DELETE FROM reviews WHERE pr_id IS NOT NULL;
ALTER TABLE reviews
    DROP CONSTRAINT IF EXISTS reviews_one_subject,
    DROP COLUMN IF EXISTS pr_head_hash,
    DROP COLUMN IF EXISTS pr_id,
    ALTER COLUMN commit_id SET NOT NULL;

DROP INDEX IF EXISTS pull_requests_open_idx;
DROP INDEX IF EXISTS pull_requests_author_idx;
DROP INDEX IF EXISTS pull_requests_repo_idx;
ALTER TABLE pull_requests
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS deletions,
    DROP COLUMN IF EXISTS additions,
    DROP COLUMN IF EXISTS files_changed,
    DROP COLUMN IF EXISTS review_skip_reason,
    DROP COLUMN IF EXISTS review_status,
    DROP COLUMN IF EXISTS bb_updated_on,
    DROP COLUMN IF EXISTS bb_created_on,
    DROP COLUMN IF EXISTS source_hash,
    DROP COLUMN IF EXISTS description;
