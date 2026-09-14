BEGIN;

-- 000088 records whether a task's cron is task-owned or inherited from its
-- policy. New policy-created tasks remain inherited (false). For historical
-- policy tasks, only a persisted cron that differs from the policy's effective
-- schedule is strong enough evidence to preserve it as an override. Equal
-- values are intentionally treated as inherited because an explicit write
-- cannot be proven from the pre-000088 schema.
ALTER TABLE tasks
    ADD COLUMN cron_override BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE tasks AS task
SET cron_override = TRUE
FROM policies AS policy
WHERE task.source = 'policy'
  AND task.policy_id = policy.id
  AND BTRIM(COALESCE(task.cron_spec, '')) <> BTRIM(
      CASE WHEN policy.enabled THEN COALESCE(policy.cron_spec, '') ELSE '' END
  );

-- A downgrade would erase schedule provenance once an explicit override has
-- been observed. Empty override sets remain reversible for migration tooling;
-- populated sets fail closed and require an audited data-preservation action.
CREATE OR REPLACE FUNCTION task_cron_override_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 88
       AND EXISTS (SELECT 1 FROM tasks WHERE cron_override) THEN
        RAISE EXCEPTION '000088 downgrade blocked: task cron override provenance exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_cron_override_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_task_cron_override_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION task_cron_override_downgrade_admission();

COMMIT;
