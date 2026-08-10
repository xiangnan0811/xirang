-- The guard is deliberately the first schema-affecting operation.  All
-- following work is enclosed in this transaction, so a used aggregate leaves
-- the complete 000069 schema and version intact.
BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_recovery_plans)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_plan_items)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_preflights)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_grants)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_jobs)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_job_items)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_attempts)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_checkpoints)
       OR EXISTS (
            SELECT 1 FROM backup_asset_recovery_evidence
            WHERE NOT (
                kind = 'scheduler_state'
                AND ((id = '0000000000000000000000000000006a' AND scheduler_scope = 'claim')
                    OR (id = '0000000000000000000000000000006b' AND scheduler_scope = 'takeover'))
            )
       )
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_result_sets)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_results)
       OR EXISTS (SELECT 1 FROM backup_asset_recovery_node_leases)
       OR EXISTS (SELECT 1 FROM backup_asset_delivery_grants WHERE resource_kind = 'recovery_result')
       OR EXISTS (
            SELECT 1
            FROM backup_asset_delivery_requests AS request_row
            JOIN backup_asset_delivery_grants AS grant_row ON grant_row.id = request_row.grant_id
            WHERE grant_row.resource_kind = 'recovery_result'
       )
       -- Usage and content-session leases are shared aggregate/fence state.
       -- The pre-000069 schema cannot safely attribute them to an arm, so
       -- conservative refusal is required for a reversible downgrade.
       OR EXISTS (SELECT 1 FROM backup_asset_delivery_usage)
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'content_session')
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'recovery_job' AND status = 'active') THEN
        RAISE EXCEPTION '000069 down blocked: recovery, recovery content, or recovery lease state exists';
    END IF;
END
$$;

DROP TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_immutable ON task_runs;
DROP TRIGGER trg_backup_asset_recovery_task_runs_node_snapshot_insert ON task_runs;
DROP FUNCTION backup_asset_recovery_task_run_node_snapshot_guard();
DROP INDEX idx_task_runs_node_snapshot_status;
ALTER TABLE task_runs
    DROP CONSTRAINT task_runs_node_id_snapshot_positive,
    DROP COLUMN node_id_snapshot;

DROP TRIGGER trg_backup_asset_recovery_content_binding_immutable ON backup_asset_delivery_grants;
DROP TRIGGER trg_backup_asset_recovery_content_authorization_update ON backup_asset_delivery_grants;
DROP TRIGGER trg_backup_asset_recovery_content_authorization_insert ON backup_asset_delivery_grants;
DROP FUNCTION backup_asset_recovery_content_binding_guard();
DROP FUNCTION backup_asset_recovery_content_authorization_guard();
DROP INDEX idx_backup_asset_delivery_grants_recovery_result_state;
ALTER TABLE backup_asset_delivery_grants
    DROP CONSTRAINT backup_asset_delivery_grants_recovery_result_fk,
    DROP CONSTRAINT backup_asset_delivery_grants_recovery_job_fk,
    DROP CONSTRAINT backup_asset_delivery_grants_resource_check,
    DROP CONSTRAINT backup_asset_delivery_grants_security_product_check;
ALTER TABLE backup_asset_delivery_grants
    ADD CONSTRAINT backup_asset_delivery_grants_resource_check CHECK (
        resource_kind = 'backup_asset'
        AND recovery_point_id IS NOT NULL
        AND catalog_generation_id IS NOT NULL
        AND entry_id IS NOT NULL
        AND recovery_job_id IS NULL
        AND recovery_result_id IS NULL
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_security_product_check CHECK (
        (action = 'download' AND renderer = 'attachment' AND profile = 'original_v1'
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.download' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification = 'non_secret'
            AND step_up_action IS NULL AND step_up_proof_id IS NULL AND step_up_expires_at IS NULL)
        OR (action = 'preview' AND renderer <> 'attachment' AND classification IN ('secret', 'unknown')
            AND step_up_action IS NOT NULL AND step_up_action = 'asset.secret_reveal' AND step_up_proof_id IS NOT NULL
            AND step_up_proof_id ~ '^[0-9a-f]{32}$'
            AND step_up_expires_at IS NOT NULL AND absolute_expires_at <= step_up_expires_at)
    );

DROP TRIGGER trg_backup_asset_recovery_results_classification_immutable ON backup_asset_recovery_results;
DROP TRIGGER trg_backup_asset_recovery_results_publish ON backup_asset_recovery_results;
DROP TRIGGER trg_backup_asset_recovery_result_sets_terminal_delete ON backup_asset_recovery_result_sets;
DROP TRIGGER trg_backup_asset_recovery_result_sets_state_transition ON backup_asset_recovery_result_sets;
DROP TRIGGER trg_backup_asset_recovery_result_sets_deadline_integrity ON backup_asset_recovery_result_sets;
DROP TRIGGER trg_backup_asset_recovery_result_sets_publish ON backup_asset_recovery_result_sets;
DROP TRIGGER trg_backup_asset_recovery_attempts_terminal_job_barrier ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_attempts_publication_integrity ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_jobs_state_transition ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_transition ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_jobs_workspace_cleanup_insert ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_jobs_publication_integrity ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_job_items_projection ON backup_asset_recovery_job_items;
DROP TRIGGER trg_backup_asset_recovery_job_items_binding_immutable ON backup_asset_recovery_job_items;
DROP TRIGGER trg_backup_asset_recovery_job_items_insert_binding ON backup_asset_recovery_job_items;
DROP TRIGGER trg_backup_asset_recovery_checkpoints_consumed_replay ON backup_asset_recovery_checkpoints;
DROP TRIGGER trg_backup_asset_recovery_checkpoints_consumed_delete ON backup_asset_recovery_checkpoints;
DROP TRIGGER trg_backup_asset_recovery_checkpoints_immutable ON backup_asset_recovery_checkpoints;
DROP TRIGGER trg_backup_asset_recovery_checkpoints_authority_insert ON backup_asset_recovery_checkpoints;
DROP TRIGGER trg_backup_asset_recovery_jobs_binding_immutable ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_jobs_authority_insert ON backup_asset_recovery_jobs;
DROP TRIGGER trg_backup_asset_recovery_preflights_immutable ON backup_asset_recovery_preflights;
DROP TRIGGER trg_backup_asset_recovery_plans_binding_frozen ON backup_asset_recovery_plans;
DROP TRIGGER trg_backup_asset_recovery_grants_delete_binding_insert ON backup_asset_recovery_grants;
DROP TRIGGER trg_backup_asset_recovery_grants_terminal_replay ON backup_asset_recovery_grants;
DROP TRIGGER trg_backup_asset_recovery_grants_terminal_delete ON backup_asset_recovery_grants;
DROP TRIGGER trg_backup_asset_recovery_grants_terminal ON backup_asset_recovery_grants;
DROP TRIGGER trg_backup_asset_recovery_attempts_terminal_delete ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_attempts_terminal_replay ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_attempts_integrity ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_attempts_mutation_arm_monotonic ON backup_asset_recovery_attempts;
DROP TRIGGER trg_backup_asset_recovery_evidence_receipt_insert ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_receipt_delete ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_receipt_update ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_scheduler_delete ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_scheduler_update ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_latch_delete ON backup_asset_recovery_evidence;
DROP TRIGGER trg_backup_asset_recovery_evidence_latch_update ON backup_asset_recovery_evidence;
DROP FUNCTION backup_asset_recovery_result_classification_guard();
DROP FUNCTION backup_asset_recovery_result_publish_guard();
DROP FUNCTION backup_asset_recovery_result_set_terminal_delete_guard();
DROP FUNCTION backup_asset_recovery_result_set_state_transition_guard();
DROP FUNCTION backup_asset_recovery_result_set_deadline_integrity_guard();
DROP FUNCTION backup_asset_recovery_result_set_publish_guard();
DROP FUNCTION backup_asset_recovery_attempt_terminal_job_barrier_guard();
DROP FUNCTION backup_asset_recovery_attempt_publication_integrity_guard();
DROP FUNCTION backup_asset_recovery_job_state_transition_guard();
DROP FUNCTION backup_asset_recovery_job_workspace_cleanup_transition_guard();
DROP FUNCTION backup_asset_recovery_job_workspace_cleanup_insert_guard();
DROP FUNCTION backup_asset_recovery_job_publication_integrity_guard();
DROP FUNCTION backup_asset_recovery_job_item_projection_guard();
DROP FUNCTION backup_asset_recovery_job_item_insert_binding_guard();
DROP FUNCTION backup_asset_recovery_checkpoint_consumed_replay_guard();
DROP FUNCTION backup_asset_recovery_checkpoint_authority_insert_guard();
DROP FUNCTION backup_asset_recovery_job_authority_insert_guard();
DROP FUNCTION backup_asset_recovery_plan_binding_guard();
DROP FUNCTION backup_asset_recovery_frozen_product_guard();
DROP FUNCTION backup_asset_recovery_grant_delete_binding_guard();
DROP FUNCTION backup_asset_recovery_grant_terminal_guard();
DROP FUNCTION backup_asset_recovery_attempt_terminal_delete_guard();
DROP FUNCTION backup_asset_recovery_attempt_integrity_guard();
DROP FUNCTION backup_asset_recovery_attempt_mutation_arm_monotonic();
DROP FUNCTION backup_asset_recovery_receipt_insert_guard();
DROP FUNCTION backup_asset_recovery_receipt_immutable();
DROP FUNCTION backup_asset_recovery_scheduler_state_guard();
DROP FUNCTION backup_asset_recovery_latch_immutable();
DROP TRIGGER trg_backup_asset_recovery_downgrade_admission ON schema_migrations;
DROP FUNCTION backup_asset_recovery_downgrade_admission();
DROP INDEX idx_recovery_point_leases_recovery_job_owner;
ALTER TABLE backup_asset_recovery_jobs
    DROP CONSTRAINT backup_asset_recovery_jobs_workspace_cleanup_node_lease_fk;

DROP TABLE backup_asset_recovery_evidence;
DROP TABLE backup_asset_recovery_grants;
DROP TABLE backup_asset_recovery_results;
DROP TABLE backup_asset_recovery_result_sets;
DROP TABLE backup_asset_recovery_node_leases;
DROP TABLE backup_asset_recovery_checkpoints;
DROP TABLE backup_asset_recovery_attempts;
DROP TABLE backup_asset_recovery_job_items;
DROP TABLE backup_asset_recovery_jobs;
DROP TABLE backup_asset_recovery_preflights;
DROP TABLE backup_asset_recovery_plan_items;
DROP TABLE backup_asset_recovery_plans;
DROP INDEX idx_recovery_points_repository_id_id;

COMMIT;
