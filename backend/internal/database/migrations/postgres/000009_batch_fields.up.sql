ALTER TABLE tasks ADD COLUMN batch_id VARCHAR(64);
CREATE INDEX IF NOT EXISTS idx_tasks_batch_id ON tasks(batch_id);
