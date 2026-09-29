-- 000090 is an irreversible data-retirement migration. Do not synthesize
-- empty history or restore deleted columns/tables; recover with the paired
-- pre-upgrade database backup and the old binary instead.
BEGIN;
DO $$
BEGIN
    RAISE EXCEPTION '000090 downgrade blocked: backup-focus retirement is irreversible; restore the pre-upgrade database backup';
END
$$;
COMMIT;
