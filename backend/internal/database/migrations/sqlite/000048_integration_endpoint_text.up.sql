-- SQLite uses dynamic typing: VARCHAR(N) is a soft hint, not a hard constraint.
-- Existing rows are not affected, and SQLite would not have rejected long values
-- anyway. This migration is a no-op kept for parity with the PostgreSQL track
-- (000048_integration_endpoint_text.up.sql), which performs an actual ALTER.
SELECT 1;
