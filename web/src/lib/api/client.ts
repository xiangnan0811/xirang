import { createAlertsApi } from "./alerts-api";
import { createAuditApi } from "./audit-api";
import { createAuthApi } from "./auth-api";
import { createCredentialAuditApi } from "./credential-audit-api";
import { createCredentialAccessGrantsApi } from "./credential-access-grants-api";
import { createBatchApi } from "./batch-api";
import { createIntegrationsApi } from "./integrations-api";
import { createNodeMetricsApi } from "./node-metrics-api";
import { createNodesApi } from "./nodes-api";
import { createOverviewApi } from "./overview-api";
import { createPoliciesApi } from "./policies-api";
import { createSSHKeysApi } from "./ssh-keys-api";
import { createTaskRunsApi } from "./task-runs-api";
import { createTasksApi } from "./tasks-api";
import { createTOTPApi } from "./totp-api";
import { createUsersApi } from "./users-api";
import { createSnapshotsApi } from "./snapshots-api";
import { createConfigApi } from "./config-api";
import { createSystemApi } from "./system-api";
import { createDockerApi } from "./docker-api";
import { createStorageGuideApi } from "./storage-guide-api";
import { createSettingsApi } from "./settings-api";
import { createSnapshotDiffApi } from "./snapshot-diff-api";
import { createDashboardsApi } from "./dashboards";
import { createSilencesApi } from "./silences";
import { createSLOApi } from "./slo";
import { createAnomalyApi } from "./anomaly";
import { createEscalationApi } from "./escalation";
import { createNodeLogsApi } from "./node-logs";
import { createAlertDeliveriesApi } from "./alert-deliveries";
import { createAppCredentialsApi } from "./app-credentials";
import { createAutomationRulesApi } from "./automation-rules";
import { createServiceMonitorsApi } from "./service-monitors";

export { ApiError } from "./core";

export const apiClient = {
  ...createAuthApi(),
  ...createNodesApi(),
  ...createNodeMetricsApi(),
  ...createOverviewApi(),
  ...createPoliciesApi(),
  ...createTasksApi(),
  ...createTaskRunsApi(),
  ...createBatchApi(),
  ...createSSHKeysApi(),
  ...createIntegrationsApi(),
  ...createAlertsApi(),
  ...createAuditApi(),
  ...createCredentialAuditApi(),
  ...createCredentialAccessGrantsApi(),
  ...createUsersApi(),
  ...createTOTPApi(),
  ...createSnapshotsApi(),
  ...createConfigApi(),
  ...createSystemApi(),
  ...createDockerApi(),
  ...createStorageGuideApi(),
  ...createSettingsApi(),
  ...createSnapshotDiffApi(),
  ...createDashboardsApi(),
  ...createSilencesApi(),
  ...createSLOApi(),
  ...createAnomalyApi(),
  ...createEscalationApi(),
  ...createNodeLogsApi(),
  ...createAlertDeliveriesApi(),
  ...createAppCredentialsApi(),
  ...createAutomationRulesApi(),
  ...createServiceMonitorsApi(),
};
