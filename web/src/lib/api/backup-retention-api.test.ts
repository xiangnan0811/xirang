import { readFileSync } from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import {
  createBackupRetentionApi,
  mapBackupRetentionHoldRecord,
  mapBackupRetentionImpact,
  mapBackupRetentionPolicy,
  mapBackupRetentionPurgeImpact,
  mapBackupRetentionPurgePlan,
  mapBackupRetentionPurgeResult,
} from "./backup-retention-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

const policyId = "a".repeat(32);
const scopeId = "b".repeat(32);
const recoveryPointId = "c".repeat(32);
const holdId = "d".repeat(32);
const repositoryId = "e".repeat(32);
const planId = "f".repeat(32);
const digest = "9".repeat(64);

function rawPolicy() {
  return {
    id: policyId,
    scope_kind: "repository",
    scope_id: scopeId,
    revision: 2,
    rules: {
      version: 1,
      age: { keep_days: 30 },
      count: { keep_latest: 7 },
      calendar: [{ unit: "week", keep: 4 }],
    },
    rule_digest: digest,
    status: "active",
    created_by: 7,
    updated_by: 7,
    created_at: "2026-08-17T08:00:00+08:00",
    updated_at: "2026-08-17T09:00:00+08:00",
    reason: "PRIVATE RETENTION REASON",
  };
}

function rawImpact() {
  return {
    policy_id: policyId,
    policy_revision: 2,
    impact_revision: 11,
    evaluated_at: "2026-08-17T01:00:00Z",
    selected_count: 1,
    hold_count: 0,
    lease_count: 0,
    worm_count: 0,
    points: [
      {
        recovery_point_id: recoveryPointId,
        point_revision: 3,
        capability_revision: 5,
      },
    ],
    provider_locator: "/PRIVATE/IMPACT/PATH",
  };
}

function rawHold() {
  return {
    id: holdId,
    recovery_point_id: recoveryPointId,
    hold_type: "legal",
    state: "active",
    created_by: 7,
    created_at: "2026-08-17T00:00:00Z",
    updated_at: "2026-08-17T00:00:00Z",
    reason: "PRIVATE HOLD REASON",
  };
}

function rawPurgePlan() {
  return {
    id: planId,
    repository_id: repositoryId,
    revision: 1,
    impact_revision: 11,
    expires_at: "2026-08-17T02:00:00Z",
    hold_count: 0,
    lease_count: 0,
    worm_count: 0,
    status: "ready",
    item_count: 1,
    items: [
      {
        recovery_point_id: recoveryPointId,
        point_revision: 3,
        capability_revision: 5,
      },
    ],
    reason: "PRIVATE PURGE REASON",
  };
}

function expectNoPrivateLeak(value: unknown) {
  const serialized = JSON.stringify(value);
  for (const forbidden of [
    "PRIVATE",
    "provider_locator",
    "reason",
    "keep_days",
    "keep_latest",
    "scope_kind",
    "rule_digest",
  ]) {
    expect(serialized).not.toContain(forbidden);
  }
}

describe("backup retention API boundary", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps policy, impact, hold, and purge shapes to camelCase without private fields", () => {
    const policy = mapBackupRetentionPolicy(rawPolicy());
    const impact = mapBackupRetentionImpact(rawImpact());
    const hold = mapBackupRetentionHoldRecord(rawHold());
    const purgeImpact = mapBackupRetentionPurgeImpact({
      repository_id: repositoryId,
      impact_revision: 11,
      selected_count: 1,
      hold_count: 0,
      lease_count: 0,
      worm_count: 0,
      points: rawImpact().points,
    });
    const plan = mapBackupRetentionPurgePlan(rawPurgePlan());
    const result = mapBackupRetentionPurgeResult({
      plan_id: planId,
      claimed: 1,
      blocked: 0,
    });

    expect(policy).toMatchObject({
      status: "available",
      value: {
        id: policyId,
        scopeKind: "repository",
        scopeId,
        revision: 2,
        rules: {
          version: 1,
          age: { keepDays: 30 },
          count: { keepLatest: 7 },
          calendar: [{ unit: "week", keep: 4 }],
        },
        ruleDigest: digest,
        status: "active",
        createdAt: "2026-08-17T00:00:00.000Z",
      },
    });
    expect(impact).toMatchObject({
      status: "available",
      value: {
        policyId,
        policyRevision: 2,
        impactRevision: 11,
        selectedCount: 1,
        points: [{ recoveryPointId, pointRevision: 3, capabilityRevision: 5 }],
      },
    });
    expect(purgeImpact).toMatchObject({
      status: "available",
      value: {
        repositoryId,
        impactRevision: 11,
        selectedCount: 1,
        points: [{ recoveryPointId, pointRevision: 3, capabilityRevision: 5 }],
      },
    });
    expect(hold).toMatchObject({
      status: "available",
      value: {
        id: holdId,
        holdType: "legal",
        state: "active",
        expiresAt: null,
        releasedBy: null,
        releasedAt: null,
      },
    });
    expect(plan.status).toBe("available");
    expect(result).toEqual({
      status: "available",
      value: { planId, claimed: 1, blocked: 0 },
    });
    expectNoPrivateLeak([policy, impact, hold, plan, result]);
  });

  it("projects unknown closed enums as one blocked object without echoing the raw value", () => {
    const mapped = mapBackupRetentionPolicy({
      ...rawPolicy(),
      scope_kind: "future_private_scope",
    });
    expect(mapped).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(JSON.stringify(mapped)).not.toContain("future_private_scope");
    expect(JSON.stringify(mapBackupRetentionImpact({
      ...rawImpact(),
      points: [{ ...rawImpact().points[0], recovery_point_id: "not-opaque" }],
    }))).not.toContain("not-opaque");
  });

  it("fails closed on incomplete policy, impact, hold, and purge products", () => {
    const blocked = {
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    };
    expect(mapBackupRetentionPolicy({
      ...rawPolicy(),
      rules: { version: 2, age: { keep_days: 30 } },
    })).toEqual(blocked);
    expect(JSON.stringify(mapBackupRetentionPolicy({
      ...rawPolicy(),
      rules: { version: 2, age: { keep_days: 30 } },
    }))).not.toContain("\"version\":2");
    expect(mapBackupRetentionPolicy({
      ...rawPolicy(),
      rules: { version: 1 },
    })).toEqual(blocked);
    expect(mapBackupRetentionImpact({
      ...rawImpact(),
      selected_count: 99,
    })).toEqual(blocked);
    expect(mapBackupRetentionHoldRecord({
      ...rawHold(),
      expires_at: "2026-09-01T00:00:00Z",
    })).toEqual(blocked);
    expect(mapBackupRetentionHoldRecord({
      ...rawHold(),
      hold_type: "operational",
    })).toEqual(blocked);
    expect(mapBackupRetentionHoldRecord({
      ...rawHold(),
      state: "released",
    })).toEqual(blocked);
    expect(mapBackupRetentionPurgePlan({
      ...rawPurgePlan(),
      item_count: 99,
    })).toEqual(blocked);
  });

  it("sends exact policy, hold, and purge request shapes and forwards proofs", async () => {
    const signal = new AbortController().signal;
    requestMock
      .mockResolvedValueOnce({ items: [rawPolicy()], next_cursor: "next-policy" })
      .mockResolvedValueOnce(rawPolicy())
      .mockResolvedValueOnce(rawPolicy())
      .mockResolvedValueOnce(rawPolicy())
      .mockResolvedValueOnce(rawImpact())
      .mockResolvedValueOnce({
        repository_id: repositoryId,
        impact_revision: 11,
        selected_count: 1,
        hold_count: 0,
        lease_count: 0,
        worm_count: 0,
        points: rawImpact().points,
      })
      .mockResolvedValueOnce(rawHold())
      .mockResolvedValueOnce({ ...rawHold(), state: "released", released_by: 7, released_at: "2026-08-17T03:00:00Z" })
      .mockResolvedValueOnce(rawPurgePlan())
      .mockResolvedValueOnce({ plan_id: planId, claimed: 1, blocked: 0 })
      .mockResolvedValueOnce({ items: [rawHold()] });

    const api = createBackupRetentionApi();
    const page = await api.listRetentionPolicies("token", { limit: 25, cursor: "cursor-value", signal });
    await api.createRetentionPolicy("token", {
      scopeKind: "repository",
      scopeId,
      rules: { version: 1, age: { keepDays: 30 } },
    }, signal);
    await api.updateRetentionPolicy("token", policyId, {
      expectedRevision: 2,
      rules: { version: 1, count: { keepLatest: 3 } },
    }, signal);
    await api.deleteRetentionPolicy("token", policyId, 2, signal);
    await api.previewRetentionPolicyImpact("token", policyId, 2, signal);
    await api.previewRepositoryPurge("token", repositoryId, { recoveryPointIds: [recoveryPointId] }, signal);
    await api.createRecoveryPointHold("token", recoveryPointId, {
      holdType: "legal",
      reason: "legal-hold-for-case",
    }, signal);
    await api.releaseRecoveryPointHold("token", recoveryPointId, holdId, {
      reason: "case-closed",
      stepUpProof: "hold-proof",
    }, signal);
    await api.createRepositoryPurgePlan("token", repositoryId, {
      expectedImpactRevision: 11,
      items: [{ recoveryPointId, pointRevision: 3, capabilityRevision: 5 }],
    }, signal);
    await api.executeRepositoryPurge("token", repositoryId, {
      planId,
      expectedRevision: 1,
      expectedImpactRevision: 11,
      reason: "approved-purge",
      stepUpProof: "purge-proof",
    }, signal);
    const holds = await api.listRecoveryPointHolds("token", recoveryPointId, signal);
    expect(holds.items[0]?.status).toBe("available");
    if (holds.items[0]?.status === "available") {
      expect(holds.items[0].value.id).toBe(holdId);
      expect(holds.items[0].value).not.toHaveProperty("reason");
    }

    expect(page.nextCursor).toBe("next-policy");
    expect(page.items[0]?.status).toBe("available");
    expect(requestMock).toHaveBeenNthCalledWith(
      1,
      "/backup-retention-policies?limit=25&cursor=cursor-value",
      { token: "token", signal },
    );
    expect(requestMock).toHaveBeenCalledWith(
      "/backup-retention-policies",
      expect.objectContaining({
        method: "POST",
        token: "token",
        body: {
          scope_kind: "repository",
          scope_id: scopeId,
          rules: { version: 1, age: { keep_days: 30 } },
        },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-retention-policies/${policyId}`,
      expect.objectContaining({
        method: "PATCH",
        body: {
          expected_revision: 2,
          rules: { version: 1, count: { keep_latest: 3 } },
        },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/recovery-points/${recoveryPointId}/holds/${holdId}/release`,
      expect.objectContaining({
        method: "POST",
        stepUpProof: "hold-proof",
        body: { reason: "case-closed" },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-retention-policies/${policyId}`,
      expect.objectContaining({
        method: "DELETE",
        body: { expected_revision: 2 },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-retention-policies/${policyId}/impact`,
      expect.objectContaining({
        method: "POST",
        body: { expected_revision: 2 },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-repositories/${repositoryId}/purge-preview`,
      expect.objectContaining({
        method: "POST",
        body: { recovery_point_ids: [recoveryPointId] },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/recovery-points/${recoveryPointId}/holds`,
      expect.objectContaining({
        method: "POST",
        body: { hold_type: "legal", reason: "legal-hold-for-case" },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-repositories/${repositoryId}/purge-plans`,
      expect.objectContaining({
        method: "POST",
        body: {
          expected_impact_revision: 11,
          items: [{
            recovery_point_id: recoveryPointId,
            point_revision: 3,
            capability_revision: 5,
          }],
        },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/backup-repositories/${repositoryId}/purges`,
      expect.objectContaining({
        method: "POST",
        stepUpProof: "purge-proof",
        body: {
          plan_id: planId,
          expected_revision: 1,
          expected_impact_revision: 11,
          reason: "approved-purge",
        },
      }),
    );
    expect(requestMock).toHaveBeenCalledWith(
      `/recovery-points/${recoveryPointId}/holds`,
      { token: "token", signal },
    );
  });

  it("does not POST illegal hold create or blank step-up proofs", async () => {
    const api = createBackupRetentionApi();
    expect(await api.createRecoveryPointHold("token", recoveryPointId, {
      holdType: "legal",
      reason: "legal-hold-for-case",
      expiresAt: "2026-09-01T00:00:00.000Z",
    })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(await api.releaseRecoveryPointHold("token", recoveryPointId, holdId, {
      reason: "case-closed",
      stepUpProof: " ",
    })).toEqual({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    expect(requestMock).not.toHaveBeenCalled();
  });

  it("uses request() only and never imports fetch", () => {
    const source = readFileSync(path.resolve(process.cwd(), "src/lib/api/backup-retention-api.ts"), "utf8");
    expect(source).not.toMatch(/\bfetch\s*\(/);
  });
});
