-- 000090 is an irreversible data-retirement migration. Do not synthesize
-- empty history or restore deleted columns/tables; recover with the paired
-- pre-upgrade database backup and the old binary instead.
CREATE TEMP TABLE backup_focus_retirement_000090_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO backup_focus_retirement_000090_down_guard(valid) VALUES (0);
DROP TABLE backup_focus_retirement_000090_down_guard;
