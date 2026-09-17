BEGIN;

-- 000077 is a quiesced cutover. Scoped provider_delete rows without a valid
-- receipt are not representable by the durable claim protocol and fail closed.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM recovery_point_lifecycle_attempts AS attempt
        JOIN recovery_points AS point ON point.id = attempt.recovery_point_id
        WHERE attempt.operation IN ('retention_expire', 'explicit_purge')
          AND attempt.phase = 'provider_delete'
          AND NOT EXISTS (
              SELECT 1
              FROM recovery_point_lifecycle_tombstones AS tombstone
              WHERE tombstone.recovery_point_id = attempt.recovery_point_id
                AND tombstone.repository_id = point.repository_id
                AND tombstone.original_semantics = point.semantics
                AND tombstone.terminal_operation = attempt.operation
                AND tombstone.terminal_state = 'expired'
                AND tombstone.created_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00'
                AND tombstone.managed_history = TRUE
                AND tombstone.retired_at IS NULL
                AND tombstone.purged_at IS NOT NULL
                AND tombstone.purged_at = tombstone.created_at
                AND tombstone.deletion_receipt_digest IS NOT NULL
                AND tombstone.deletion_receipt_digest ~ '^[0-9a-f]{64}$'
                AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
                AND point.state IN ('expiring', 'purge_blocked', 'expired')
          )
    ) THEN
        RAISE EXCEPTION '000077 upgrade requires quiesced provider_delete attempts with valid receipts';
    END IF;
END
$$;

CREATE TABLE recovery_point_lifecycle_effect_claims (
    id VARCHAR(32) NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_point_lifecycle_attempts(id) ON DELETE RESTRICT,
    executor_id VARCHAR(32) NOT NULL CHECK (executor_id ~ '^[0-9a-f]{32}$'),
    execution_id VARCHAR(32) NOT NULL CHECK (execution_id ~ '^[0-9a-f]{32}$'),
    transition_revision BIGINT NOT NULL CHECK (transition_revision > 0),
    lease_id VARCHAR(32) NOT NULL CHECK (lease_id ~ '^[0-9a-f]{32}$'),
    lease_attempt_id VARCHAR(32) NOT NULL CHECK (lease_attempt_id ~ '^[0-9a-f]{32}$'),
    lease_fence_token_hash VARCHAR(64) NOT NULL CHECK (lease_fence_token_hash ~ '^[0-9a-f]{64}$'),
    target_identity_digest VARCHAR(64) NOT NULL CHECK (target_identity_digest ~ '^[0-9a-f]{64}$'),
    state VARCHAR(32) NOT NULL CHECK (state IN ('in_flight', 'uncertain', 'proven')),
    deadline_at TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX idx_recovery_point_lifecycle_effect_claims_attempt
    ON recovery_point_lifecycle_effect_claims(attempt_id);
CREATE INDEX idx_recovery_point_lifecycle_effect_claims_state_deadline
    ON recovery_point_lifecycle_effect_claims(state, deadline_at);

CREATE OR REPLACE FUNCTION recovery_point_lifecycle_effect_claim_transition_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state = 'proven' THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim is proven and immutable';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.target_identity_digest IS DISTINCT FROM OLD.target_identity_digest
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim identity is immutable';
    END IF;
    IF OLD.state = 'in_flight' AND NEW.state NOT IN ('in_flight', 'uncertain', 'proven') THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim state transition is invalid';
    END IF;
    IF OLD.state = 'uncertain' AND NEW.state NOT IN ('uncertain', 'in_flight') THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim takeover transition is invalid';
    END IF;
    IF OLD.state = 'uncertain' AND NEW.state = 'uncertain' THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim uncertainty is historical';
    END IF;
    IF OLD.state = 'in_flight' AND NEW.state = OLD.state
       AND (NEW.executor_id IS DISTINCT FROM OLD.executor_id
         OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
         OR NEW.transition_revision IS DISTINCT FROM OLD.transition_revision
         OR NEW.lease_id IS DISTINCT FROM OLD.lease_id
         OR NEW.lease_attempt_id IS DISTINCT FROM OLD.lease_attempt_id
         OR NEW.lease_fence_token_hash IS DISTINCT FROM OLD.lease_fence_token_hash) THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim renewal rebinding is invalid';
    END IF;
    IF OLD.state = 'in_flight' AND NEW.state IN ('uncertain', 'proven')
       AND (NEW.executor_id IS DISTINCT FROM OLD.executor_id
         OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
         OR NEW.transition_revision IS DISTINCT FROM OLD.transition_revision
         OR NEW.lease_id IS DISTINCT FROM OLD.lease_id
         OR NEW.lease_attempt_id IS DISTINCT FROM OLD.lease_attempt_id
         OR NEW.lease_fence_token_hash IS DISTINCT FROM OLD.lease_fence_token_hash) THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim binding changed before takeover';
    END IF;
    IF OLD.state = 'uncertain' AND NEW.state = 'in_flight'
       AND NEW.execution_id IS NOT DISTINCT FROM OLD.execution_id THEN
        RAISE EXCEPTION 'recovery point lifecycle effect claim takeover must rotate execution_id';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claims_transition
BEFORE UPDATE ON recovery_point_lifecycle_effect_claims
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_effect_claim_transition_guard();

CREATE OR REPLACE FUNCTION recovery_point_lifecycle_effect_claim_delete_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'recovery point lifecycle effect claim is permanent';
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claims_no_delete
BEFORE DELETE ON recovery_point_lifecycle_effect_claims
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_effect_claim_delete_guard();

CREATE TABLE recovery_point_lifecycle_audit_slots (
    id VARCHAR(32) NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    attempt_id VARCHAR(32) NOT NULL CHECK (attempt_id ~ '^[0-9a-f]{32}$')
        REFERENCES recovery_point_lifecycle_attempts(id) ON DELETE RESTRICT,
    status VARCHAR(32) NOT NULL CHECK (status IN ('deleted', 'already_absent', 'blocked', 'identity_conflict')),
    emitted_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX idx_recovery_point_lifecycle_audit_slots_attempt_status
    ON recovery_point_lifecycle_audit_slots(attempt_id, status);
CREATE UNIQUE INDEX idx_recovery_point_lifecycle_audit_slots_terminal
    ON recovery_point_lifecycle_audit_slots(attempt_id)
    WHERE status IN ('deleted', 'already_absent');

CREATE OR REPLACE FUNCTION recovery_point_lifecycle_audit_slot_transition_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- The parent attempt is the canonical serialization boundary. Lock it
    -- before inspecting slots so terminal and observational inserts cannot
    -- both pass the EXISTS check in concurrent transactions.
    PERFORM 1
    FROM recovery_point_lifecycle_attempts
    WHERE id = NEW.attempt_id
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'recovery point lifecycle audit slot attempt is missing';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM recovery_point_lifecycle_audit_slots
        WHERE attempt_id = NEW.attempt_id
          AND status IN ('deleted', 'already_absent')
    ) THEN
        RAISE EXCEPTION 'recovery point lifecycle audit slot follows a terminal status';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_transition
BEFORE INSERT ON recovery_point_lifecycle_audit_slots
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_audit_slot_transition_guard();

CREATE OR REPLACE FUNCTION recovery_point_lifecycle_audit_slot_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'recovery point lifecycle audit slot is immutable';
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_immutable_update
BEFORE UPDATE ON recovery_point_lifecycle_audit_slots
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_audit_slot_immutable_guard();
CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_immutable_delete
BEFORE DELETE ON recovery_point_lifecycle_audit_slots
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_audit_slot_immutable_guard();

-- Keep this admission trigger stacked with the v70-v76 guards. The body down
-- migration repeats the checks so bypassing metadata admission is still safe.
CREATE OR REPLACE FUNCTION recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 77 AND (
        EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims)
        OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots)
    ) THEN
        RAISE EXCEPTION '000077 downgrade blocked: lifecycle effect claim or audit slot exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission();

-- Backfill only the exact settledDeletionCandidate/event producer contract.
-- Temporary relational staging makes malformed signatures, ambiguity and
-- illegal histories abort the entire cutover.
CREATE TEMP TABLE lifecycle_effect_audit_slot_000077_candidates (
    attempt_id VARCHAR(32) PRIMARY KEY,
    recovery_point_id VARCHAR(32) NOT NULL,
    repository_id VARCHAR(32) NOT NULL,
    operation VARCHAR(24) NOT NULL,
    phase VARCHAR(24) NOT NULL,
    blocked_reason VARCHAR(48) NOT NULL,
    terminal_status VARCHAR(32) NOT NULL DEFAULT '',
    terminal_at TIMESTAMPTZ,
    tombstone_found BOOLEAN NOT NULL,
    tombstone_valid BOOLEAN NOT NULL
) ON COMMIT DROP;
INSERT INTO lifecycle_effect_audit_slot_000077_candidates(
    attempt_id, recovery_point_id, repository_id, operation, phase,
    blocked_reason, terminal_status, terminal_at, tombstone_found, tombstone_valid
)
SELECT attempt.id,
       attempt.recovery_point_id,
       point.repository_id,
       attempt.operation,
       attempt.phase,
       attempt.blocked_reason,
       CASE WHEN tombstone.recovery_point_id IS NOT NULL
                  AND tombstone.repository_id = point.repository_id
                  AND tombstone.original_semantics = point.semantics
                  AND tombstone.terminal_state = 'expired'
                  AND tombstone.created_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00'
                  AND tombstone.managed_history = TRUE
                  AND tombstone.retired_at IS NULL
                  AND tombstone.purged_at IS NOT NULL
                  AND tombstone.purged_at = tombstone.created_at
                  AND tombstone.deletion_receipt_digest IS NOT NULL
                  AND tombstone.deletion_receipt_digest ~ '^[0-9a-f]{64}$'
                  AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
                  AND point.state IN ('expiring', 'purge_blocked', 'expired')
             THEN CASE tombstone.result_code
                      WHEN 'provider_deleted' THEN 'deleted'
                      WHEN 'provider_already_absent' THEN 'already_absent'
                      ELSE ''
                  END
             ELSE '' END,
       tombstone.created_at,
       (tombstone.recovery_point_id IS NOT NULL),
       CASE WHEN tombstone.recovery_point_id IS NOT NULL
                  AND tombstone.repository_id = point.repository_id
                  AND tombstone.original_semantics = point.semantics
                  AND tombstone.terminal_operation = attempt.operation
                  AND tombstone.terminal_state = 'expired'
                  AND tombstone.created_at <> TIMESTAMPTZ '0001-01-01 00:00:00+00'
                  AND tombstone.managed_history = TRUE
                  AND tombstone.retired_at IS NULL
                  AND tombstone.purged_at IS NOT NULL
                  AND tombstone.purged_at = tombstone.created_at
                  AND tombstone.deletion_receipt_digest IS NOT NULL
                  AND tombstone.deletion_receipt_digest ~ '^[0-9a-f]{64}$'
                  AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
                  AND point.state IN ('expiring', 'purge_blocked', 'expired')
             THEN TRUE ELSE FALSE END
FROM recovery_point_lifecycle_attempts AS attempt
JOIN recovery_points AS point ON point.id = attempt.recovery_point_id
LEFT JOIN recovery_point_lifecycle_tombstones AS tombstone
  ON tombstone.recovery_point_id = attempt.recovery_point_id
 AND tombstone.terminal_operation = attempt.operation
WHERE attempt.operation IN ('retention_expire', 'explicit_purge')
  AND (
      tombstone.recovery_point_id IS NOT NULL
      OR (attempt.phase = 'blocked' AND attempt.blocked_reason IN (
          'active_hold', 'provider_worm', 'provider_unavailable',
          'provider_identity_conflict', 'provider_native_version_referenced',
          'provider_delete_unproven', 'deletion_unavailable'))
  );

CREATE TEMP TABLE lifecycle_effect_audit_slot_000077_events (
    attempt_id VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    event_at TIMESTAMPTZ NOT NULL,
    segment_no BIGINT NOT NULL,
    segment_sequence BIGINT NOT NULL,
    exact BOOLEAN NOT NULL
) ON COMMIT DROP;
INSERT INTO lifecycle_effect_audit_slot_000077_events(
    attempt_id, status, event_at, segment_no, segment_sequence, exact
)
SELECT candidate.attempt_id,
       CASE WHEN jsonb_typeof(event.fields_json::jsonb -> 'status') = 'string'
            THEN event.fields_json::jsonb ->> 'status' ELSE '' END,
       event.created_at,
       event.segment_no,
       event.segment_sequence,
       CASE WHEN event.action = 'repository_purge'
             AND event.outcome IN ('blocked', 'success')
             AND event.repository_id = candidate.repository_id
             AND event.recovery_point_id = candidate.recovery_point_id
             AND event.item_count = 1
             AND jsonb_typeof(event.fields_json::jsonb) = 'object'
             AND (SELECT COUNT(*) FROM jsonb_object_keys(event.fields_json::jsonb)) = 4
             AND (event.fields_json::jsonb ?& ARRAY['stage', 'status', 'item_count', 'source'])
             AND jsonb_typeof(event.fields_json::jsonb -> 'stage') = 'string'
             AND event.fields_json::jsonb ->> 'stage' = 'settled'
             AND jsonb_typeof(event.fields_json::jsonb -> 'status') = 'string'
             AND event.fields_json::jsonb ->> 'status' IN (
                 'blocked', 'identity_conflict', 'deleted', 'already_absent')
             AND jsonb_typeof(event.fields_json::jsonb -> 'item_count') = 'number'
             AND event.fields_json::jsonb ->> 'item_count' = '1'
             AND jsonb_typeof(event.fields_json::jsonb -> 'source') = 'string'
             AND event.fields_json::jsonb ->> 'source' = candidate.attempt_id
             AND (
                 (event.fields_json::jsonb ->> 'status' IN ('blocked', 'identity_conflict')
                      AND event.outcome = 'blocked')
                 OR (candidate.tombstone_valid
                     AND event.fields_json::jsonb ->> 'status' = candidate.terminal_status
                     AND event.outcome = 'success')
             )
            THEN TRUE ELSE FALSE END
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
JOIN backup_asset_audit_events AS event
  ON event.action = 'repository_purge'
 AND (
      event.recovery_point_id = candidate.recovery_point_id
      OR event.fields_json::jsonb ->> 'source' = candidate.attempt_id
  );

CREATE TEMP TABLE lifecycle_effect_audit_slot_000077_matches (
    attempt_id VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    terminal BOOLEAN NOT NULL,
    required BOOLEAN NOT NULL,
    exact_count BIGINT NOT NULL,
    first_event_at TIMESTAMPTZ,
    last_event_at TIMESTAMPTZ,
    first_terminal_segment_no BIGINT,
    first_terminal_segment_sequence BIGINT,
    last_observation_segment_no BIGINT,
    last_observation_segment_sequence BIGINT,
    PRIMARY KEY (attempt_id, status)
) ON COMMIT DROP;
INSERT INTO lifecycle_effect_audit_slot_000077_matches(
    attempt_id, status, terminal, required, exact_count, first_event_at, last_event_at,
    first_terminal_segment_no, first_terminal_segment_sequence,
    last_observation_segment_no, last_observation_segment_sequence
)
SELECT candidate.attempt_id,
       event.status,
       event.status IN ('deleted', 'already_absent'),
       (NOT candidate.tombstone_valid
            AND candidate.phase = 'blocked'
            AND event.status = CASE
                WHEN candidate.blocked_reason = 'provider_identity_conflict'
                THEN 'identity_conflict' ELSE 'blocked' END),
       COUNT(*),
       MIN(event.event_at),
       MAX(event.event_at),
       CASE WHEN event.status IN ('deleted', 'already_absent') THEN (
           SELECT ordered.segment_no
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact
           ORDER BY ordered.segment_no, ordered.segment_sequence
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('deleted', 'already_absent') THEN (
           SELECT ordered.segment_sequence
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact
           ORDER BY ordered.segment_no, ordered.segment_sequence
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('blocked', 'identity_conflict') THEN (
           SELECT ordered.segment_no
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact
           ORDER BY ordered.segment_no DESC, ordered.segment_sequence DESC
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('blocked', 'identity_conflict') THEN (
           SELECT ordered.segment_sequence
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact
           ORDER BY ordered.segment_no DESC, ordered.segment_sequence DESC
           LIMIT 1
       ) END
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
JOIN lifecycle_effect_audit_slot_000077_events AS event
  ON event.attempt_id = candidate.attempt_id
 AND event.exact
GROUP BY candidate.attempt_id, event.status, candidate.tombstone_valid,
         candidate.phase, candidate.blocked_reason;

-- A blocked candidate must have a current-truth observation. Historical
-- observations are independent and may contain either status in either order.
INSERT INTO lifecycle_effect_audit_slot_000077_matches(
    attempt_id, status, terminal, required, exact_count, first_event_at, last_event_at,
    first_terminal_segment_no, first_terminal_segment_sequence,
    last_observation_segment_no, last_observation_segment_sequence
)
SELECT candidate.attempt_id,
       CASE WHEN candidate.blocked_reason = 'provider_identity_conflict'
            THEN 'identity_conflict' ELSE 'blocked' END,
       FALSE, TRUE, 0, NULL, NULL, NULL, NULL, NULL, NULL
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
WHERE NOT candidate.tombstone_valid
  AND candidate.phase = 'blocked'
  AND NOT EXISTS (
      SELECT 1
      FROM lifecycle_effect_audit_slot_000077_matches AS match
      WHERE match.attempt_id = candidate.attempt_id
        AND match.status = CASE WHEN candidate.blocked_reason = 'provider_identity_conflict'
                                THEN 'identity_conflict' ELSE 'blocked' END
  );

-- A valid receipt can infer its terminal event only from the two terminal
-- lifecycle phases. Other phases need an exact retained terminal event.
INSERT INTO lifecycle_effect_audit_slot_000077_matches(
    attempt_id, status, terminal, required, exact_count, first_event_at, last_event_at,
    first_terminal_segment_no, first_terminal_segment_sequence,
    last_observation_segment_no, last_observation_segment_sequence
)
SELECT candidate.attempt_id, candidate.terminal_status, TRUE, FALSE, 0, NULL, NULL,
       NULL, NULL, NULL, NULL
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
WHERE candidate.tombstone_valid
  AND candidate.phase IN ('tombstoning', 'complete')
  AND NOT EXISTS (
      SELECT 1
      FROM lifecycle_effect_audit_slot_000077_matches AS match
      WHERE match.attempt_id = candidate.attempt_id
        AND match.status = candidate.terminal_status
  );

CREATE TEMP TABLE lifecycle_effect_claim_audit_slot_000077_backfill_guard (
    valid BOOLEAN NOT NULL CHECK (valid)
) ON COMMIT DROP;
INSERT INTO lifecycle_effect_claim_audit_slot_000077_backfill_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE EXISTS (
        SELECT 1
        FROM lifecycle_effect_audit_slot_000077_events AS event
        WHERE event.attempt_id = candidate.attempt_id
          AND NOT event.exact
    )
      AND NOT EXISTS (
        SELECT 1
        FROM lifecycle_effect_audit_slot_000077_events AS event
        WHERE event.attempt_id = candidate.attempt_id
          AND event.exact
    )
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE candidate.tombstone_found AND NOT candidate.tombstone_valid
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_matches
    WHERE required AND exact_count = 0
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE candidate.tombstone_valid
      AND NOT EXISTS (
          SELECT 1
          FROM lifecycle_effect_audit_slot_000077_matches AS match
          WHERE match.attempt_id = candidate.attempt_id
            AND match.status = candidate.terminal_status
            AND (match.exact_count > 0 OR
                 (match.exact_count = 0 AND candidate.phase IN ('tombstoning', 'complete')))
      )
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_matches AS terminal_match
    JOIN lifecycle_effect_audit_slot_000077_candidates AS candidate
      ON candidate.attempt_id = terminal_match.attempt_id
    JOIN lifecycle_effect_audit_slot_000077_matches AS observation_match
      ON observation_match.attempt_id = terminal_match.attempt_id
     AND observation_match.status IN ('blocked', 'identity_conflict')
     AND observation_match.exact_count > 0
    WHERE terminal_match.terminal
      AND (
          COALESCE(terminal_match.first_event_at, candidate.terminal_at)
              < observation_match.last_event_at
          OR (
              COALESCE(terminal_match.first_event_at, candidate.terminal_at)
                  = observation_match.last_event_at
              AND (
                  terminal_match.first_terminal_segment_no IS NULL
                  OR observation_match.last_observation_segment_no IS NULL
                  OR terminal_match.first_terminal_segment_no < observation_match.last_observation_segment_no
                  OR (
                      terminal_match.first_terminal_segment_no = observation_match.last_observation_segment_no
                      AND terminal_match.first_terminal_segment_sequence
                          <= observation_match.last_observation_segment_sequence
                  )
              )
          )
      )
) THEN FALSE ELSE TRUE END;
-- The CHECK above turns every unsafe candidate into an atomic migration error.
DROP TABLE lifecycle_effect_claim_audit_slot_000077_backfill_guard;

INSERT INTO recovery_point_lifecycle_audit_slots(id, attempt_id, status, emitted_at, created_at)
SELECT replace(gen_random_uuid()::text, '-', ''),
       match.attempt_id,
       match.status,
       COALESCE(match.first_event_at, candidate.terminal_at),
       COALESCE(match.first_event_at, candidate.terminal_at)
FROM lifecycle_effect_audit_slot_000077_matches AS match
JOIN lifecycle_effect_audit_slot_000077_candidates AS candidate
  ON candidate.attempt_id = match.attempt_id
WHERE match.exact_count > 0
   OR (match.terminal AND candidate.phase IN ('tombstoning', 'complete'))
ORDER BY match.attempt_id, match.terminal, match.status;

DROP TABLE lifecycle_effect_audit_slot_000077_matches;
DROP TABLE lifecycle_effect_audit_slot_000077_events;
DROP TABLE lifecycle_effect_audit_slot_000077_candidates;

-- The admission trigger is created before backfill for failure safety and
-- recreated here to make the final definition explicit after all cutover work.
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission();

COMMIT;
