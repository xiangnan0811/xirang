BEGIN;

-- Keep independent body guards even when metadata admission was bypassed.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims) THEN
        RAISE EXCEPTION '000077 downgrade blocked: lifecycle effect claim exists';
    END IF;
    IF EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots) THEN
        RAISE EXCEPTION '000077 downgrade blocked: lifecycle audit slot exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission();

DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_immutable_delete ON recovery_point_lifecycle_audit_slots;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_immutable_update ON recovery_point_lifecycle_audit_slots;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_transition ON recovery_point_lifecycle_audit_slots;
DROP FUNCTION IF EXISTS recovery_point_lifecycle_audit_slot_immutable_guard();
DROP FUNCTION IF EXISTS recovery_point_lifecycle_audit_slot_transition_guard();
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_audit_slots_terminal;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_audit_slots_attempt_status;
DROP TABLE IF EXISTS recovery_point_lifecycle_audit_slots;

DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claims_no_delete ON recovery_point_lifecycle_effect_claims;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claims_transition ON recovery_point_lifecycle_effect_claims;
DROP FUNCTION IF EXISTS recovery_point_lifecycle_effect_claim_delete_guard();
DROP FUNCTION IF EXISTS recovery_point_lifecycle_effect_claim_transition_guard();
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_effect_claims_state_deadline;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_effect_claims_attempt;
DROP TABLE IF EXISTS recovery_point_lifecycle_effect_claims;

COMMIT;
