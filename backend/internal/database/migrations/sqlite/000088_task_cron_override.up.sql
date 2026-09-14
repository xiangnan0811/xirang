-- 000088 records whether a task's cron is task-owned or inherited from its
-- policy. New policy-created tasks remain inherited (0). For historical
-- policy tasks, only a persisted cron that differs from the policy's effective
-- schedule is strong enough evidence to preserve it as an override. Equal
-- values are intentionally treated as inherited because an explicit write
-- cannot be proven from the pre-000088 schema.
ALTER TABLE tasks ADD COLUMN cron_override INTEGER NOT NULL DEFAULT 0;

UPDATE tasks
SET cron_override = 1
WHERE source = 'policy'
  AND policy_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM policies AS policy
      WHERE policy.id = tasks.policy_id
        AND TRIM(COALESCE(tasks.cron_spec, '')) <> TRIM(
            CASE WHEN COALESCE(policy.enabled, 0) <> 0
                 THEN COALESCE(policy.cron_spec, '')
                 ELSE '' END
        )
  );

-- A downgrade would erase schedule provenance once an explicit override has
-- been observed. Empty override sets remain reversible for migration tooling;
-- populated sets fail closed and require an audited data-preservation action.
DROP TRIGGER IF EXISTS trg_task_cron_override_downgrade_admission;
CREATE TRIGGER trg_task_cron_override_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 88
 AND EXISTS (SELECT 1 FROM tasks WHERE cron_override <> 0)
BEGIN
    SELECT RAISE(ABORT, '000088 downgrade blocked: task cron override provenance exists');
END;
