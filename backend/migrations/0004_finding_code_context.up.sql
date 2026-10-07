-- The diff hunk a finding is about, so the dashboard can show the code next
-- to the comment. Null for reviews made before this migration.
ALTER TABLE review_findings ADD COLUMN code_context text;
