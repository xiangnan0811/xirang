-- Revert endpoint back to VARCHAR(1024). May fail if any row exceeds 1024 bytes;
-- that is the intentional protection — running this down migration in production
-- requires first identifying & shortening any over-long rows.
ALTER TABLE integrations ALTER COLUMN endpoint TYPE VARCHAR(1024);
