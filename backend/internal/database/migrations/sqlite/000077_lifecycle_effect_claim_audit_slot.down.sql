-- Admission is normally checked when golang-migrate writes the target dirty
-- version. Keep an independent body guard for audited/manual execution paths.
DROP TABLE IF EXISTS lifecycle_effect_claims_000077_down_guard;
CREATE TEMP TABLE lifecycle_effect_claims_000077_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO lifecycle_effect_claims_000077_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM recovery_point_lifecycle_effect_claims
) THEN 0 ELSE 1 END;
DROP TABLE lifecycle_effect_claims_000077_down_guard;

DROP TABLE IF EXISTS lifecycle_effect_audit_slots_000077_down_guard;
CREATE TEMP TABLE lifecycle_effect_audit_slots_000077_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO lifecycle_effect_audit_slots_000077_down_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM recovery_point_lifecycle_audit_slots
) THEN 0 ELSE 1 END;
DROP TABLE lifecycle_effect_audit_slots_000077_down_guard;

DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_immutable_delete;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_immutable_update;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_audit_slots_transition;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_audit_slots_terminal;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_audit_slots_attempt_status;
DROP TABLE IF EXISTS recovery_point_lifecycle_audit_slots;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claims_no_delete;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claims_transition;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_effect_claims_state_deadline;
DROP INDEX IF EXISTS idx_recovery_point_lifecycle_effect_claims_attempt;
DROP TABLE IF EXISTS recovery_point_lifecycle_effect_claims;
