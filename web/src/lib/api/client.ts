import { createAlertsApi } from "./alerts-api";
import { createAuthApi } from "./auth-api";
import { createCredentialAccessGrantsApi } from "./credential-access-grants-api";
import { createBatchApi } from "./batch-api";
import { createIntegrationsApi } from "./integrations-api";
import { createNodesApi } from "./nodes-api";
import { createOverviewApi } from "./overview-api";
import { createPoliciesApi } from "./policies-api";
import { createSSHKeysApi } from "./ssh-keys-api";
import { createTaskRunsApi } from "./task-runs-api";
import { createTasksApi } from "./tasks-api";
import { createTOTPApi } from "./totp-api";
import { createUsersApi } from "./users-api";
import { createConfigApi } from "./config-api";
import { createSystemApi } from "./system-api";
import { createDockerApi } from "./docker-api";
import { createStorageGuideApi } from "./storage-guide-api";
import { createSettingsApi } from "./settings-api";
import { createSilencesApi } from "./silences";
import { createAnomalyApi } from "./anomaly";
import { createEscalationApi } from "./escalation";
import { createAlertDeliveriesApi } from "./alert-deliveries";
import { createAppCredentialsApi } from "./app-credentials";

type AuditApi = ReturnType<typeof import("./audit-api").createAuditApi>;
type CredentialAuditApi = ReturnType<typeof import("./credential-audit-api").createCredentialAuditApi>;
type SLOApi = ReturnType<typeof import("./slo").createSLOApi>;

export { ApiError } from "./core";

type BackupAssetsApi = ReturnType<typeof import("./backup-assets-api").createBackupAssetsApi>;
type BackupAssetSearchApi = ReturnType<
  typeof import("./backup-asset-search-api").createBackupAssetSearchApi
>;
type BackupAssetOverlaysApi = ReturnType<
  typeof import("./backup-asset-overlays-api").createBackupAssetOverlaysApi
>;
type BackupContentApi = ReturnType<typeof import("./backup-content-api").createBackupContentApi>;
type BackupRetentionApi = ReturnType<
  typeof import("./backup-retention-api").createBackupRetentionApi
>;
type BackupRepositoriesApi = ReturnType<
  typeof import("./backup-repositories-api").createBackupRepositoriesApi
>;
type BackupFileSourcesApi = ReturnType<
  typeof import("./backup-file-sources-api").createBackupFileSourcesApi
>;
type RecoveryPointsApi = ReturnType<
  typeof import("./recovery-points-api").createRecoveryPointsApi
>;

let backupAssetsApiPromise: Promise<BackupAssetsApi> | undefined;
let backupAssetSearchApiPromise: Promise<BackupAssetSearchApi> | undefined;
let backupAssetOverlaysApiPromise: Promise<BackupAssetOverlaysApi> | undefined;
let backupContentApiPromise: Promise<BackupContentApi> | undefined;
let backupRetentionApiPromise: Promise<BackupRetentionApi> | undefined;
let backupRepositoriesApiPromise: Promise<BackupRepositoriesApi> | undefined;
let backupFileSourcesApiPromise: Promise<BackupFileSourcesApi> | undefined;
let recoveryPointsApiPromise: Promise<RecoveryPointsApi> | undefined;
let auditApiPromise: Promise<AuditApi> | undefined;
let credentialAuditApiPromise: Promise<CredentialAuditApi> | undefined;
let sloApiPromise: Promise<SLOApi> | undefined;

function loadBackupAssetsApi(): Promise<BackupAssetsApi> {
  backupAssetsApiPromise ??= import("./backup-assets-api").then((module) =>
    module.createBackupAssetsApi()
  );
  return backupAssetsApiPromise;
}

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

function loadBackupRetentionApi(): Promise<BackupRetentionApi> {
  backupRetentionApiPromise ??= import("./backup-retention-api").then((module) =>
    module.createBackupRetentionApi()
  );
  return backupRetentionApiPromise;
}

function loadBackupRepositoriesApi(): Promise<BackupRepositoriesApi> {
  backupRepositoriesApiPromise ??= import("./backup-repositories-api").then((module) =>
    module.createBackupRepositoriesApi()
  );
  return backupRepositoriesApiPromise;
}

function loadBackupFileSourcesApi(): Promise<BackupFileSourcesApi> {
  backupFileSourcesApiPromise ??= import("./backup-file-sources-api").then((module) => module.createBackupFileSourcesApi());
  return backupFileSourcesApiPromise;
}

function loadRecoveryPointsApi(): Promise<RecoveryPointsApi> {
  recoveryPointsApiPromise ??= import("./recovery-points-api").then((module) =>
    module.createRecoveryPointsApi()
  );
  return recoveryPointsApiPromise;
}

function loadAuditApi(): Promise<AuditApi> {
  auditApiPromise ??= import("./audit-api").then((module) => module.createAuditApi());
  return auditApiPromise;
}

function loadCredentialAuditApi(): Promise<CredentialAuditApi> {
  credentialAuditApiPromise ??= import("./credential-audit-api").then((module) =>
    module.createCredentialAuditApi()
  );
  return credentialAuditApiPromise;
}

function loadSLOApi(): Promise<SLOApi> {
  sloApiPromise ??= import("./slo").then((module) => module.createSLOApi());
  return sloApiPromise;
}

const lazyAuditApi: AuditApi = {
  async getAuditLogs(...args) {
    return (await loadAuditApi()).getAuditLogs(...args);
  },
  async exportAuditLogsCSV(...args) {
    return (await loadAuditApi()).exportAuditLogsCSV(...args);
  },
};

const lazyCredentialAuditApi: CredentialAuditApi = {
  async getCredentialAuditEvents(...args) {
    return (await loadCredentialAuditApi()).getCredentialAuditEvents(...args);
  },
  async exportCredentialAuditEventsCSV(...args) {
    return (await loadCredentialAuditApi()).exportCredentialAuditEventsCSV(...args);
  },
};

const lazySLOApi: SLOApi = {
  async listSLOs(...args) {
    return (await loadSLOApi()).listSLOs(...args);
  },
  async createSLO(...args) {
    return (await loadSLOApi()).createSLO(...args);
  },
  async updateSLO(...args) {
    return (await loadSLOApi()).updateSLO(...args);
  },
  async deleteSLO(...args) {
    return (await loadSLOApi()).deleteSLO(...args);
  },
  async getSLOCompliance(...args) {
    return (await loadSLOApi()).getSLOCompliance(...args);
  },
  async getSLOSummary(...args) {
    return (await loadSLOApi()).getSLOSummary(...args);
  },
};

const lazyBackupAssetsApi: BackupAssetsApi = {
  async listBackupAssets(...args) {
    return (await loadBackupAssetsApi()).listBackupAssets(...args);
  },
  async listAssetVersions(...args) {
    return (await loadBackupAssetsApi()).listAssetVersions(...args);
  },
  async getBackupAsset(...args) {
    return (await loadBackupAssetsApi()).getBackupAsset(...args);
  },
  async diffRecoveryPoints(...args) {
    return (await loadBackupAssetsApi()).diffRecoveryPoints(...args);
  },
};

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

const lazyBackupFileSourcesApi: BackupFileSourcesApi = {
  async resolveBackupFileSourceRecoveryPoint(...args) {
    return (await loadBackupFileSourcesApi()).resolveBackupFileSourceRecoveryPoint(...args);
  },
  async listBackupFileSourceNodes(...args) {
    return (await loadBackupFileSourcesApi()).listBackupFileSourceNodes(...args);
  },
  async listBackupFileSourceSets(...args) {
    return (await loadBackupFileSourcesApi()).listBackupFileSourceSets(...args);
  },
  async listBackupFileSourceVersions(...args) {
    return (await loadBackupFileSourcesApi()).listBackupFileSourceVersions(...args);
  },
};

const lazyRecoveryPointsApi: RecoveryPointsApi = {
  async listRecoveryPoints(...args) {
    return (await loadRecoveryPointsApi()).listRecoveryPoints(...args);
  },
  async getRecoveryPoint(...args) {
    return (await loadRecoveryPointsApi()).getRecoveryPoint(...args);
  },
  async getRecoveryPointCatalogStatus(...args) {
    return (await loadRecoveryPointsApi()).getRecoveryPointCatalogStatus(...args);
  },
  async getRecoveryPointEvidence(...args) {
    return (await loadRecoveryPointsApi()).getRecoveryPointEvidence(...args);
  },
  async preparePreviewSource(...args) {
    return (await loadRecoveryPointsApi()).preparePreviewSource(...args);
  },
};

const lazyBackupRepositoriesApi: BackupRepositoriesApi = {
  async listBackupRepositories(...args) {
    return (await loadBackupRepositoriesApi()).listBackupRepositories(...args);
  },
  async getBackupRepository(...args) {
    return (await loadBackupRepositoriesApi()).getBackupRepository(...args);
  },
  async connectBackupRepository(...args) {
    return (await loadBackupRepositoriesApi()).connectBackupRepository(...args);
  },
  async reconcileBackupRepository(...args) {
    return (await loadBackupRepositoriesApi()).reconcileBackupRepository(...args);
  },
  async disconnectBackupRepository(...args) {
    return (await loadBackupRepositoriesApi()).disconnectBackupRepository(...args);
  },
  async scanBackupRepositoryImports(...args) {
    return (await loadBackupRepositoriesApi()).scanBackupRepositoryImports(...args);
  },
  async listBackupRepositoryImportCandidates(...args) {
    return (await loadBackupRepositoriesApi()).listBackupRepositoryImportCandidates(...args);
  },
  async reviewBackupRepositoryImportCandidate(...args) {
    return (await loadBackupRepositoriesApi()).reviewBackupRepositoryImportCandidate(...args);
  },
  async rebuildBackupRepositoryImports(...args) {
    return (await loadBackupRepositoriesApi()).rebuildBackupRepositoryImports(...args);
  },
};

const lazyBackupRetentionApi: BackupRetentionApi = {
  async listRetentionPolicies(...args) {
    return (await loadBackupRetentionApi()).listRetentionPolicies(...args);
  },
  async createRetentionPolicy(...args) {
    return (await loadBackupRetentionApi()).createRetentionPolicy(...args);
  },
  async updateRetentionPolicy(...args) {
    return (await loadBackupRetentionApi()).updateRetentionPolicy(...args);
  },
  async deleteRetentionPolicy(...args) {
    return (await loadBackupRetentionApi()).deleteRetentionPolicy(...args);
  },
  async previewRetentionPolicyImpact(...args) {
    return (await loadBackupRetentionApi()).previewRetentionPolicyImpact(...args);
  },
  async listRecoveryPointHolds(...args) {
    return (await loadBackupRetentionApi()).listRecoveryPointHolds(...args);
  },
  async createRecoveryPointHold(...args) {
    return (await loadBackupRetentionApi()).createRecoveryPointHold(...args);
  },
  async releaseRecoveryPointHold(...args) {
    return (await loadBackupRetentionApi()).releaseRecoveryPointHold(...args);
  },
  async previewRepositoryPurge(...args) {
    return (await loadBackupRetentionApi()).previewRepositoryPurge(...args);
  },
  async createRepositoryPurgePlan(...args) {
    return (await loadBackupRetentionApi()).createRepositoryPurgePlan(...args);
  },
  async executeRepositoryPurge(...args) {
    return (await loadBackupRetentionApi()).executeRepositoryPurge(...args);
  },
};

export const apiClient = {
  ...createAuthApi(),
  ...createNodesApi(),
  ...createOverviewApi(),
  ...createPoliciesApi(),
  ...createTasksApi(),
  ...createTaskRunsApi(),
  ...createBatchApi(),
  ...createSSHKeysApi(),
  ...createIntegrationsApi(),
  ...createAlertsApi(),
  ...lazyAuditApi,
  ...lazyCredentialAuditApi,
  ...createCredentialAccessGrantsApi(),
  ...createUsersApi(),
  ...createTOTPApi(),
  ...createConfigApi(),
  ...createSystemApi(),
  ...createDockerApi(),
  ...createStorageGuideApi(),
  ...createSettingsApi(),
  ...createSilencesApi(),
  ...lazySLOApi,
  ...createAnomalyApi(),
  ...createEscalationApi(),
  ...createAlertDeliveriesApi(),
  ...createAppCredentialsApi(),
  ...lazyRecoveryPointsApi,
  ...lazyBackupAssetsApi,
  ...lazyBackupAssetSearchApi,
  ...lazyBackupAssetOverlaysApi,
  ...lazyBackupContentApi,
  ...lazyBackupFileSourcesApi,
  ...lazyBackupRepositoriesApi,
  ...lazyBackupRetentionApi,
};
