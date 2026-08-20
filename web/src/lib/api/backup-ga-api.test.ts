import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { request } from "./core";
import {
  createBackupGaApi,
  mapBackupGaInventory,
  mapBackupGaReadiness,
} from "./backup-ga-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);
const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
const repositoryId = "a".repeat(32);

function rawReadiness(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    class: "existing",
    status: "ready",
    inventory_complete: true,
    inventory_digest: digest,
    acknowledged_digest: "",
    export_root_valid: true,
    key_domains_ready: true,
    worker_optional: true,
    counts: {
      candidates: 2,
      conflicts: 1,
      unsupported: 1,
      capability_gaps: 0,
    },
    conflicts: [
      {
        kind: "command_unsupported",
        task_ids: [9],
        repository_id: repositoryId,
        stable_reason_code: "backup_assets.ga.command_unsupported",
      },
    ],
    identity_key: "/PRIVATE/REPOSITORY/PATH",
    snapshot_path: "/var/backups/SnapshotFileIndex",
    grant_secret: "PRIVATE GRANT",
    ...overrides,
  };
}

describe("backup GA API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps readiness snake_case to camelCase and drops locator-shaped fields", () => {
    expect(mapBackupGaReadiness(rawReadiness())).toEqual({
      schemaVersion: 1,
      class: "existing",
      status: "ready",
      inventoryComplete: true,
      inventoryDigest: digest,
      acknowledgedDigest: "",
      exportRootValid: true,
      keyDomainsReady: true,
      workerOptional: true,
      counts: {
        candidates: 2,
        conflicts: 1,
        unsupported: 1,
        capabilityGaps: 0,
      },
      conflicts: [
        {
          kind: "command_unsupported",
          taskIds: [9],
          repositoryId,
          stableReasonCode: "backup_assets.ga.command_unsupported",
        },
      ],
    });
  });

  it("maps inventory counts and opaque conflict IDs only", () => {
    const mapped = mapBackupGaInventory(rawReadiness({ class: "fresh", status: "ready" }));
    expect(mapped.class).toBe("fresh");
    expect(mapped.inventoryDigest).toBe(digest);
    expect(mapped.counts.conflicts).toBe(1);
    expect(mapped).not.toHaveProperty("identityKey");
    expect(mapped).not.toHaveProperty("snapshotPath");
    expect(mapped).not.toHaveProperty("candidates");
  });

  it("calls the admin-only GA routes through the shared request wrapper", async () => {
    requestMock.mockResolvedValue(rawReadiness());
    const api = createBackupGaApi();
    await api.getReadiness("admin-token");
    await api.runInventory("admin-token");
    await api.acknowledge("admin-token", digest);
    expect(requestMock.mock.calls.map((call) => [call[0], call[1]?.method])).toEqual([
      ["/settings/backup-assets/ga/readiness", undefined],
      ["/settings/backup-assets/ga/inventory", "POST"],
      ["/settings/backup-assets/ga/acknowledge", "POST"],
    ]);
    expect(requestMock.mock.calls[2]?.[1]?.body).toEqual({ digest });
  });

  it("keeps backup_assets out of System settings CATEGORY_ORDER", () => {
    const source = readFileSync(
      path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../pages/settings-page.system.tsx"),
      "utf8",
    );
    expect(source).toContain(
      'const CATEGORY_ORDER = ["security", "node_monitor", "retention", "storage", "alert", "anomaly"]',
    );
    expect(source).not.toMatch(/CATEGORY_ORDER[\s\S]*backup_assets/);
  });
});
