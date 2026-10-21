-- The whole diff a review was made from (secrets masked), so the dashboard can
-- show every change even when there are no findings. Null for reviews made
-- before this migration.
ALTER TABLE reviews ADD COLUMN diff text;
