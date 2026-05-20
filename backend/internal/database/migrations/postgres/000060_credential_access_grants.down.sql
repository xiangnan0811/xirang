BEGIN;

DROP INDEX IF EXISTS idx_credential_access_grants_operation;
DROP INDEX IF EXISTS idx_credential_access_grants_revoked_by_user_id;
DROP INDEX IF EXISTS idx_credential_access_grants_approver_user_id;
DROP INDEX IF EXISTS idx_credential_access_grants_expires_at;
DROP INDEX IF EXISTS idx_credential_access_grants_requested_at;
DROP INDEX IF EXISTS idx_credential_access_grants_status;
DROP INDEX IF EXISTS idx_credential_access_grants_policy_id;
DROP INDEX IF EXISTS idx_credential_access_grants_task_id;
DROP INDEX IF EXISTS idx_credential_access_grants_node_id;
DROP INDEX IF EXISTS idx_credential_access_grants_purpose;
DROP INDEX IF EXISTS idx_credential_access_grants_action;
DROP INDEX IF EXISTS idx_credential_access_grants_requester_user_id;

DROP TABLE IF EXISTS credential_access_grants;

COMMIT;
