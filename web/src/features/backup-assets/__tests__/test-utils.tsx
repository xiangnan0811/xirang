import type { ReactElement, ReactNode } from "react";
import { render as rtlRender, type RenderResult } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import type { BackupRecoveryPoint, BackupRepository } from "@/types/domain";
import type { BackupAssetResultRow } from "../backup-assets-state";

export function backupTestAuth(overrides: Partial<AuthContextValue> = {}): AuthContextValue {
  return {
    token: "token",
    username: "tester",
    role: "admin",
    userId: 1,
    totpEnabled: true,
    isAuthenticated: true,
    authTransitioning: false,
    beginTOTPActivation: () => 1,
    abortTOTPActivation: () => undefined,
    completeTOTPActivation: () => true,
    login: () => undefined,
    logout: () => undefined,
    setTotpEnabled: () => undefined,
    ensureStepUpProof: async () => "proof",
    clearStepUpProof: () => undefined,
    ...overrides,
  };
}

export function renderBackupSurface(
  ui: ReactElement,
  auth?: Partial<AuthContextValue>,
): RenderResult {
  const host = (node: ReactNode) => (
    <MemoryRouter initialEntries={["/app/backups/data"]}>
      <AuthContext.Provider value={backupTestAuth(auth)}>{node}</AuthContext.Provider>
    </MemoryRouter>
  );
  const view = rtlRender(host(ui));
  return {
    ...view,
    rerender: (next: ReactNode) => view.rerender(<>{host(next)}</>),
  };
}

export const repository: BackupRepository = {
  id: "a".repeat(32),
  providerKind: "restic",
  displayName: "Synthetic Primary Repository With A Deliberately Long Name",
  description: "",
  versionMode: "native_snapshot",
  status: "online",
  capabilityRevision: 1,
  capabilities: {
    list: true,
    searchPath: true,
    openSequential: true,
    openRange: true,
    download: true,
    restore: true,
    diff: true,
    nativeHistory: true,
    reason: null,
  },
  immutabilityLevel: "backend_versioned",
  lastSeenAt: "2026-07-19T00:00:00Z",
  lastReconciledAt: "2026-07-19T00:00:00Z",
  createdAt: "2026-07-18T00:00:00Z",
  updatedAt: "2026-07-19T00:00:00Z",
  accessActive: true,
  lineages: [],
  catalog: {
    recoveryPointCount: 2,
    completeCatalogCount: 2,
    coverage: "complete",
    contentAvailability: { available: true, reason: null },
    permissions: { list: true, preview: true, download: true },
  },
};

export const recoveryPoint: BackupRecoveryPoint = {
  id: "b".repeat(32),
  repositoryId: repository.id,
  lineage: { producingTaskId: 7, producingTaskRunId: 21 },
  semantics: "native_snapshot",
  state: "committed",
  physicalAvailability: "online",
  holdState: "none",
  immutabilityLevel: "backend_versioned",
  manifestDigest: "sha256:synthetic",
  entryCount: 128,
  logicalBytes: 4096,
  capturedAt: "2026-07-19T00:00:00Z",
  committedAt: "2026-07-19T00:05:00Z",
  observedAt: "2026-07-19T00:05:00Z",
  capabilityRevision: 1,
  capabilities: repository.capabilities,
  createdAt: "2026-07-19T00:00:00Z",
  updatedAt: "2026-07-19T00:05:00Z",
  producingTaskName: "Synthetic nightly backup",
  producingNodeId: 3,
  producingNodeName: "synthetic-node",
  catalog: {
    status: "available",
    value: {
      generation: null,
      latestBuild: null,
      coverage: {
        status: "complete",
        indexedEntries: 128,
        expectedEntries: 128,
        manifestDigest: "sha256:synthetic",
        observedAt: "2026-07-19T00:05:00Z",
      },
      staleness: { status: "fresh", observedAt: "2026-07-19T00:05:00Z", reason: null },
      contentAvailability: { available: true, reason: null },
      permissions: { list: true, preview: true, download: true },
    },
  },
};

export function buildAssetRows(count: number): BackupAssetResultRow[] {
  const recoveryPointId = "b".repeat(32);
  return Array.from({ length: count }, (_, index) => {
    const entryId = index.toString(16).padStart(64, "0");
    const ref = { recoveryPointId, entryId };
    return {
      ref,
      source: "browse",
      hitFields: [],
      snippet: null,
      asset: {
        ref,
        parentRef: null,
        name: `synthetic-entry-${index.toString().padStart(4, "0")}.yaml`,
        entryType: index % 7 === 0 ? "directory" : "file",
        size: index * 128,
        modifiedAt: "2026-07-19T00:00:00Z",
        mode: "0640",
        owner: "operator",
        mimeType: "text/yaml",
        fingerprintStrength: "strong",
        breadcrumb: [],
      },
    };
  });
}
