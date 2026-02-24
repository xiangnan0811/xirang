import { createAlertsApi } from "./alerts-api";
import { createAuditApi } from "./audit-api";
import { createAuthApi } from "./auth-api";
import { createIntegrationsApi } from "./integrations-api";
import { createNodesApi } from "./nodes-api";
import { createPoliciesApi } from "./policies-api";
import { createSSHKeysApi } from "./ssh-keys-api";
import { createTasksApi } from "./tasks-api";
import { createUsersApi } from "./users-api";

export { ApiError } from "./core";

export const apiClient = {
  ...createAuthApi(),
  ...createNodesApi(),
  ...createPoliciesApi(),
  ...createTasksApi(),
  ...createSSHKeysApi(),
  ...createIntegrationsApi(),
  ...createAlertsApi(),
  ...createAuditApi(),
  ...createUsersApi()
};
