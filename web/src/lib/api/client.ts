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
import { createBackupRepositoriesApi } from "./backup-repositories-api";
import { createRecoveryPointsApi } from "./recovery-points-api";
import { createBackupAssetsApi } from "./backup-assets-api";

export { ApiError } from "./core";

type BackupAssetSearchApi = ReturnType<
  typeof import("./backup-asset-search-api").createBackupAssetSearchApi
>;
type BackupAssetOverlaysApi = ReturnType<
  typeof import("./backup-asset-overlays-api").createBackupAssetOverlaysApi
>;
type BackupContentApi = ReturnType<typeof import("./backup-content-api").createBackupContentApi>;

let backupAssetSearchApiPromise: Promise<BackupAssetSearchApi> | undefined;
let backupAssetOverlaysApiPromise: Promise<BackupAssetOverlaysApi> | undefined;
let backupContentApiPromise: Promise<BackupContentApi> | undefined;

function loadBackupAssetSearchApi(): Promise<BackupAssetSearchApi> {
  backupAssetSearchApiPromise ??= import("./backup-asset-search-api").then((module) =>
    module.createBackupAssetSearchApi()
  );
  return backupAssetSearchApiPromise;
}

function loadBackupAssetOverlaysApi(): Promise<BackupAssetOverlaysApi> {
  backupAssetOverlaysApiPromise ??= import("./backup-asset-overlays-api").then((module) =>
    module.createBackupAssetOverlaysApi()
  );
  return backupAssetOverlaysApiPromise;
}

function loadBackupContentApi(): Promise<BackupContentApi> {
  backupContentApiPromise ??= import("./backup-content-api").then((module) =>
    module.createBackupContentApi()
  );
  return backupContentApiPromise;
}

const lazyBackupAssetSearchApi: BackupAssetSearchApi = {
  async search(...args) {
    return (await loadBackupAssetSearchApi()).search(...args);
  },
};

const lazyBackupAssetOverlaysApi: BackupAssetOverlaysApi = {
  async listSavedSearches(...args) {
    return (await loadBackupAssetOverlaysApi()).listSavedSearches(...args);
  },
  async createSavedSearch(...args) {
    return (await loadBackupAssetOverlaysApi()).createSavedSearch(...args);
  },
  async getSavedSearch(...args) {
    return (await loadBackupAssetOverlaysApi()).getSavedSearch(...args);
  },
  async updateSavedSearch(...args) {
    return (await loadBackupAssetOverlaysApi()).updateSavedSearch(...args);
  },
  async deleteSavedSearch(...args) {
    return (await loadBackupAssetOverlaysApi()).deleteSavedSearch(...args);
  },
  async listFavorites(...args) {
    return (await loadBackupAssetOverlaysApi()).listFavorites(...args);
  },
  async addFavorite(...args) {
    return (await loadBackupAssetOverlaysApi()).addFavorite(...args);
  },
  async removeFavorite(...args) {
    return (await loadBackupAssetOverlaysApi()).removeFavorite(...args);
  },
  async listTags(...args) {
    return (await loadBackupAssetOverlaysApi()).listTags(...args);
  },
  async createTag(...args) {
    return (await loadBackupAssetOverlaysApi()).createTag(...args);
  },
  async updateTag(...args) {
    return (await loadBackupAssetOverlaysApi()).updateTag(...args);
  },
  async deleteTag(...args) {
    return (await loadBackupAssetOverlaysApi()).deleteTag(...args);
  },
  async assignTag(...args) {
    return (await loadBackupAssetOverlaysApi()).assignTag(...args);
  },
  async unassignTag(...args) {
    return (await loadBackupAssetOverlaysApi()).unassignTag(...args);
  },
  async listRecent(...args) {
    return (await loadBackupAssetOverlaysApi()).listRecent(...args);
  },
  async clearRecent(...args) {
    return (await loadBackupAssetOverlaysApi()).clearRecent(...args);
  },
};

const lazyBackupContentApi: BackupContentApi = {
  async issueTicket(...args) {
    return (await loadBackupContentApi()).issueTicket(...args);
  },
};

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
  ...createBackupRepositoriesApi(),
  ...createRecoveryPointsApi(),
  ...createBackupAssetsApi(),
  ...lazyBackupAssetSearchApi,
  ...lazyBackupAssetOverlaysApi,
  ...lazyBackupContentApi,
};
