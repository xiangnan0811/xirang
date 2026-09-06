-- 000077 adds durable provider-delete claims and retention-proof settled-audit slots.
-- The lifecycle cutover is quiesced: an in-flight provider_delete attempt without
-- a valid receipt must be reconciled before this migration is allowed to land.
CREATE TEMP TABLE lifecycle_effect_claim_audit_slot_000077_cutover_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO lifecycle_effect_claim_audit_slot_000077_cutover_guard(valid)
SELECT CASE WHEN EXISTS (
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
            AND julianday(tombstone.created_at) <> julianday('0001-01-01 00:00:00')
            AND tombstone.managed_history = 1
            AND tombstone.retired_at IS NULL
            AND tombstone.purged_at IS NOT NULL
            AND tombstone.purged_at = tombstone.created_at
            AND tombstone.deletion_receipt_digest IS NOT NULL
            AND length(tombstone.deletion_receipt_digest) = 64
            AND tombstone.deletion_receipt_digest NOT GLOB '*[^0-9a-f]*'
            AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
            AND point.state IN ('expiring', 'purge_blocked', 'expired')
      )
) THEN 0 ELSE 1 END;
DROP TABLE lifecycle_effect_claim_audit_slot_000077_cutover_guard;

CREATE TABLE recovery_point_lifecycle_effect_claims (
    id TEXT NOT NULL PRIMARY KEY
        CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    attempt_id TEXT NOT NULL
        CHECK (length(attempt_id) = 32 AND attempt_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES recovery_point_lifecycle_attempts(id) ON DELETE RESTRICT,
    executor_id TEXT NOT NULL
        CHECK (length(executor_id) = 32 AND executor_id NOT GLOB '*[^0-9a-f]*'),
    execution_id TEXT NOT NULL
        CHECK (length(execution_id) = 32 AND execution_id NOT GLOB '*[^0-9a-f]*'),
    transition_revision INTEGER NOT NULL CHECK (transition_revision > 0),
    lease_id TEXT NOT NULL
        CHECK (length(lease_id) = 32 AND lease_id NOT GLOB '*[^0-9a-f]*'),
    lease_attempt_id TEXT NOT NULL
        CHECK (length(lease_attempt_id) = 32 AND lease_attempt_id NOT GLOB '*[^0-9a-f]*'),
    lease_fence_token_hash TEXT NOT NULL
        CHECK (length(lease_fence_token_hash) = 64 AND lease_fence_token_hash NOT GLOB '*[^0-9a-f]*'),
    target_identity_digest TEXT NOT NULL
        CHECK (length(target_identity_digest) = 64 AND target_identity_digest NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL CHECK (state IN ('in_flight', 'uncertain', 'proven')),
    deadline_at DATETIME NOT NULL,
    heartbeat_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE UNIQUE INDEX idx_recovery_point_lifecycle_effect_claims_attempt
    ON recovery_point_lifecycle_effect_claims(attempt_id);
CREATE INDEX idx_recovery_point_lifecycle_effect_claims_state_deadline
    ON recovery_point_lifecycle_effect_claims(state, deadline_at);

-- A claim is an append-only acquisition history. In-flight renewals may touch
-- only the heartbeat/deadline/update clocks; uncertainty is historical and
-- takeover is the sole rebinding transition.
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claims_transition
BEFORE UPDATE ON recovery_point_lifecycle_effect_claims
WHEN OLD.state = 'proven'
    OR NEW.id IS NOT OLD.id
    OR NEW.attempt_id IS NOT OLD.attempt_id
    OR NEW.target_identity_digest IS NOT OLD.target_identity_digest
    OR NEW.created_at IS NOT OLD.created_at
    OR (OLD.state = 'in_flight' AND NEW.state NOT IN ('in_flight', 'uncertain', 'proven'))
    OR (OLD.state = 'uncertain' AND NEW.state NOT IN ('uncertain', 'in_flight'))
    OR (OLD.state = 'uncertain' AND NEW.state = 'uncertain')
    OR (OLD.state = 'in_flight' AND NEW.state = OLD.state
        AND (NEW.executor_id IS NOT OLD.executor_id
            OR NEW.execution_id IS NOT OLD.execution_id
            OR NEW.transition_revision IS NOT OLD.transition_revision
            OR NEW.lease_id IS NOT OLD.lease_id
            OR NEW.lease_attempt_id IS NOT OLD.lease_attempt_id
            OR NEW.lease_fence_token_hash IS NOT OLD.lease_fence_token_hash))
    OR (OLD.state = 'in_flight' AND NEW.state IN ('uncertain', 'proven')
        AND (NEW.executor_id IS NOT OLD.executor_id
            OR NEW.execution_id IS NOT OLD.execution_id
            OR NEW.transition_revision IS NOT OLD.transition_revision
            OR NEW.lease_id IS NOT OLD.lease_id
            OR NEW.lease_attempt_id IS NOT OLD.lease_attempt_id
            OR NEW.lease_fence_token_hash IS NOT OLD.lease_fence_token_hash))
    OR (OLD.state = 'uncertain' AND NEW.state = 'in_flight'
        AND NEW.execution_id IS OLD.execution_id)
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle effect claim transition is immutable or invalid');
END;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claims_no_delete
BEFORE DELETE ON recovery_point_lifecycle_effect_claims
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle effect claim is permanent');
END;

CREATE TABLE recovery_point_lifecycle_audit_slots (
    id TEXT NOT NULL PRIMARY KEY
        CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    attempt_id TEXT NOT NULL
        CHECK (length(attempt_id) = 32 AND attempt_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES recovery_point_lifecycle_attempts(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('deleted', 'already_absent', 'blocked', 'identity_conflict')),
    emitted_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE UNIQUE INDEX idx_recovery_point_lifecycle_audit_slots_attempt_status
    ON recovery_point_lifecycle_audit_slots(attempt_id, status);
CREATE UNIQUE INDEX idx_recovery_point_lifecycle_audit_slots_terminal
    ON recovery_point_lifecycle_audit_slots(attempt_id)
    WHERE status IN ('deleted', 'already_absent');

CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_transition
BEFORE INSERT ON recovery_point_lifecycle_audit_slots
WHEN EXISTS (
    SELECT 1
    FROM recovery_point_lifecycle_audit_slots
    WHERE attempt_id = NEW.attempt_id
      AND status IN ('deleted', 'already_absent')
)
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle audit slot follows a terminal status');
END;

CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_immutable_update
BEFORE UPDATE ON recovery_point_lifecycle_audit_slots
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle audit slot is immutable');
END;

CREATE TRIGGER trg_recovery_point_lifecycle_audit_slots_immutable_delete
BEFORE DELETE ON recovery_point_lifecycle_audit_slots
BEGIN
    SELECT RAISE(ABORT, 'recovery point lifecycle audit slot is permanent');
END;

-- A v77 downgrade is admitted only while both append-only tables are empty.
-- This trigger is intentionally stacked with all v70-v76 admission triggers.
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 77
 AND (
    EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots)
 )
BEGIN
    SELECT RAISE(ABORT, '000077 downgrade blocked: lifecycle effect claim or audit slot exists');
END;

-- Backfill only the exact settled-deletion producer contract. Temporary tables
-- make malformed signatures, ambiguity and illegal histories abort the cutover.
CREATE TEMP TABLE lifecycle_effect_audit_slot_000077_candidates (
    attempt_id TEXT NOT NULL PRIMARY KEY,
    recovery_point_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    phase TEXT NOT NULL,
    blocked_reason TEXT NOT NULL,
    terminal_status TEXT NOT NULL DEFAULT '',
    terminal_at DATETIME,
    tombstone_found INTEGER NOT NULL CHECK (tombstone_found IN (0, 1)),
    tombstone_valid INTEGER NOT NULL CHECK (tombstone_valid IN (0, 1))
);
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
                  AND julianday(tombstone.created_at) <> julianday('0001-01-01 00:00:00')
                  AND tombstone.managed_history = 1
                  AND tombstone.retired_at IS NULL
                  AND tombstone.purged_at IS NOT NULL
                  AND tombstone.purged_at = tombstone.created_at
                  AND tombstone.deletion_receipt_digest IS NOT NULL
                  AND length(tombstone.deletion_receipt_digest) = 64
                  AND tombstone.deletion_receipt_digest NOT GLOB '*[^0-9a-f]*'
                  AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
                  AND point.state IN ('expiring', 'purge_blocked', 'expired')
             THEN CASE tombstone.result_code
                      WHEN 'provider_deleted' THEN 'deleted'
                      WHEN 'provider_already_absent' THEN 'already_absent'
                      ELSE ''
                  END
             ELSE '' END,
       tombstone.created_at,
       CASE WHEN tombstone.recovery_point_id IS NOT NULL THEN 1 ELSE 0 END,
       CASE WHEN tombstone.recovery_point_id IS NOT NULL
                  AND tombstone.repository_id = point.repository_id
                  AND tombstone.original_semantics = point.semantics
                  AND tombstone.terminal_operation = attempt.operation
                  AND tombstone.terminal_state = 'expired'
                  AND julianday(tombstone.created_at) <> julianday('0001-01-01 00:00:00')
                  AND tombstone.managed_history = 1
                  AND tombstone.retired_at IS NULL
                  AND tombstone.purged_at IS NOT NULL
                  AND tombstone.purged_at = tombstone.created_at
                  AND tombstone.deletion_receipt_digest IS NOT NULL
                  AND length(tombstone.deletion_receipt_digest) = 64
                  AND tombstone.deletion_receipt_digest NOT GLOB '*[^0-9a-f]*'
                  AND tombstone.result_code IN ('provider_deleted', 'provider_already_absent')
                  AND point.state IN ('expiring', 'purge_blocked', 'expired')
             THEN 1 ELSE 0 END
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
    attempt_id TEXT NOT NULL,
    status TEXT NOT NULL,
    event_at DATETIME NOT NULL,
    segment_no INTEGER NOT NULL,
    segment_sequence INTEGER NOT NULL,
    exact INTEGER NOT NULL CHECK (exact IN (0, 1))
);
INSERT INTO lifecycle_effect_audit_slot_000077_events(
    attempt_id, status, event_at, segment_no, segment_sequence, exact
)
SELECT candidate.attempt_id,
       CASE WHEN json_type(event.fields_json, '$.status') = 'text'
            THEN json_extract(event.fields_json, '$.status') ELSE '' END,
       event.created_at,
       event.segment_no,
       event.segment_sequence,
       CASE WHEN event.action = 'repository_purge'
             AND event.outcome IN ('blocked', 'success')
             AND event.repository_id = candidate.repository_id
             AND event.recovery_point_id = candidate.recovery_point_id
             AND event.item_count = 1
             AND json_type(event.fields_json) = 'object'
             AND (SELECT COUNT(*) FROM json_each(event.fields_json)) = 4
             AND json_type(event.fields_json, '$.stage') = 'text'
             AND json_extract(event.fields_json, '$.stage') = 'settled'
             AND json_type(event.fields_json, '$.status') = 'text'
             AND json_extract(event.fields_json, '$.status') IN (
                 'blocked', 'identity_conflict', 'deleted', 'already_absent')
             AND json_type(event.fields_json, '$.item_count') = 'integer'
             AND json_extract(event.fields_json, '$.item_count') = 1
             AND json_type(event.fields_json, '$.source') = 'text'
             AND json_extract(event.fields_json, '$.source') = candidate.attempt_id
             AND (
                 (json_extract(event.fields_json, '$.status') IN ('blocked', 'identity_conflict')
                      AND event.outcome = 'blocked')
                 OR (candidate.tombstone_valid = 1
                     AND json_extract(event.fields_json, '$.status') = candidate.terminal_status
                     AND event.outcome = 'success')
             )
            THEN 1 ELSE 0 END
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
JOIN backup_asset_audit_events AS event
  ON event.action = 'repository_purge'
 AND (
      event.recovery_point_id = candidate.recovery_point_id
      OR json_extract(event.fields_json, '$.source') = candidate.attempt_id
  );

CREATE TEMP TABLE lifecycle_effect_audit_slot_000077_matches (
    attempt_id TEXT NOT NULL,
    status TEXT NOT NULL,
    terminal INTEGER NOT NULL CHECK (terminal IN (0, 1)),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    exact_count INTEGER NOT NULL,
    first_event_at DATETIME,
    last_event_at DATETIME,
    first_terminal_segment_no INTEGER,
    first_terminal_segment_sequence INTEGER,
    last_observation_segment_no INTEGER,
    last_observation_segment_sequence INTEGER,
    PRIMARY KEY (attempt_id, status)
);
INSERT INTO lifecycle_effect_audit_slot_000077_matches(
    attempt_id, status, terminal, required, exact_count, first_event_at, last_event_at,
    first_terminal_segment_no, first_terminal_segment_sequence,
    last_observation_segment_no, last_observation_segment_sequence
)
SELECT candidate.attempt_id,
       event.status,
       CASE WHEN event.status IN ('deleted', 'already_absent') THEN 1 ELSE 0 END,
       CASE WHEN candidate.tombstone_valid = 0
                  AND candidate.phase = 'blocked'
                  AND event.status = CASE
                      WHEN candidate.blocked_reason = 'provider_identity_conflict'
                      THEN 'identity_conflict' ELSE 'blocked' END
            THEN 1 ELSE 0 END,
       COUNT(*),
       MIN(event.event_at),
       MAX(event.event_at),
       CASE WHEN event.status IN ('deleted', 'already_absent') THEN (
           SELECT ordered.segment_no
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact = 1
           ORDER BY ordered.segment_no, ordered.segment_sequence
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('deleted', 'already_absent') THEN (
           SELECT ordered.segment_sequence
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact = 1
           ORDER BY ordered.segment_no, ordered.segment_sequence
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('blocked', 'identity_conflict') THEN (
           SELECT ordered.segment_no
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact = 1
           ORDER BY ordered.segment_no DESC, ordered.segment_sequence DESC
           LIMIT 1
       ) END,
       CASE WHEN event.status IN ('blocked', 'identity_conflict') THEN (
           SELECT ordered.segment_sequence
           FROM lifecycle_effect_audit_slot_000077_events AS ordered
           WHERE ordered.attempt_id = candidate.attempt_id
             AND ordered.status = event.status
             AND ordered.exact = 1
           ORDER BY ordered.segment_no DESC, ordered.segment_sequence DESC
           LIMIT 1
       ) END
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
JOIN lifecycle_effect_audit_slot_000077_events AS event
  ON event.attempt_id = candidate.attempt_id
 AND event.exact = 1
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
       0, 1, 0, NULL, NULL, NULL, NULL, NULL, NULL
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
WHERE candidate.tombstone_valid = 0
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
SELECT candidate.attempt_id, candidate.terminal_status, 1, 0, 0, NULL, NULL,
       NULL, NULL, NULL, NULL
FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
WHERE candidate.tombstone_valid = 1
  AND candidate.phase IN ('tombstoning', 'complete')
  AND NOT EXISTS (
      SELECT 1
      FROM lifecycle_effect_audit_slot_000077_matches AS match
      WHERE match.attempt_id = candidate.attempt_id
        AND match.status = candidate.terminal_status
  );

CREATE TEMP TABLE lifecycle_effect_claim_audit_slot_000077_backfill_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO lifecycle_effect_claim_audit_slot_000077_backfill_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE EXISTS (
        SELECT 1
        FROM lifecycle_effect_audit_slot_000077_events AS event
        WHERE event.attempt_id = candidate.attempt_id
          AND event.exact = 0
    )
      AND NOT EXISTS (
        SELECT 1
        FROM lifecycle_effect_audit_slot_000077_events AS event
        WHERE event.attempt_id = candidate.attempt_id
          AND event.exact = 1
    )
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE candidate.tombstone_found = 1
      AND candidate.tombstone_valid = 0
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_matches
    WHERE required = 1 AND exact_count = 0
) OR EXISTS (
    SELECT 1
    FROM lifecycle_effect_audit_slot_000077_candidates AS candidate
    WHERE candidate.tombstone_valid = 1
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
    WHERE terminal_match.terminal = 1
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
) THEN 0 ELSE 1 END;
DROP TABLE lifecycle_effect_claim_audit_slot_000077_backfill_guard;
INSERT INTO recovery_point_lifecycle_audit_slots(id, attempt_id, status, emitted_at, created_at)
SELECT lower(hex(randomblob(16))),
       match.attempt_id,
       match.status,
       COALESCE(match.first_event_at, candidate.terminal_at),
       COALESCE(match.first_event_at, candidate.terminal_at)
FROM lifecycle_effect_audit_slot_000077_matches AS match
JOIN lifecycle_effect_audit_slot_000077_candidates AS candidate
  ON candidate.attempt_id = match.attempt_id
WHERE match.exact_count > 0
   OR (match.terminal = 1 AND candidate.phase IN ('tombstoning', 'complete'))
ORDER BY match.attempt_id, match.terminal, match.status;

DROP TABLE lifecycle_effect_audit_slot_000077_matches;
DROP TABLE lifecycle_effect_audit_slot_000077_events;
DROP TABLE lifecycle_effect_audit_slot_000077_candidates;
DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission;
CREATE TRIGGER trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 77
 AND (
    EXISTS (SELECT 1 FROM recovery_point_lifecycle_effect_claims)
    OR EXISTS (SELECT 1 FROM recovery_point_lifecycle_audit_slots)
 )
BEGIN
    SELECT RAISE(ABORT, '000077 downgrade blocked: lifecycle effect claim or audit slot exists');
END;
