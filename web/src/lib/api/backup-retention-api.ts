import type {
  BackupRetentionHoldRecord,
  BackupRetentionImpact,
  BackupRetentionImpactPoint,
  BackupRetentionPolicy,
  BackupRetentionPurgeImpact,
  BackupRetentionPolicyPage,
  BackupRetentionPurgePlan,
  BackupRetentionPurgePlanItem,
  BackupRetentionPurgeResult,
  CatalogProjection,
  PurgePlanStatus,
  RecoveryPointHoldRecordState,
  RecoveryPointHoldType,
  RetentionCalendarUnit,
  RetentionPolicyRules,
  RetentionPolicyScopeKind,
  RetentionPolicyStatus,
} from "@/types/domain";
import { request } from "./core";
import { finiteInteger } from "./lifecycle-integers";
import { normalizeCatalogTime } from "./recovery-points-api";

type RawObject = Record<string, unknown>;

function isRawObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function blocked<T>(): CatalogProjection<T> {
  return {
    status: "blocked",
    reason: { code: "unknown_internal_state", params: {} },
  };
}

function opaqueId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : null;
}

function scopeKind(value: unknown): RetentionPolicyScopeKind | null {
  return value === "repository" || value === "task_link" ? value : null;
}

function policyStatus(value: unknown): RetentionPolicyStatus | null {
  return value === "active" || value === "deleted" ? value : null;
}

function calendarUnit(value: unknown): RetentionCalendarUnit | null {
  return value === "day" || value === "week" || value === "month" || value === "year" ? value : null;
}

function holdType(value: unknown): RecoveryPointHoldType | null {
  return value === "operational" || value === "legal" ? value : null;
}

function holdRecordState(value: unknown): RecoveryPointHoldRecordState | null {
  return value === "active" || value === "released" ? value : null;
}

function purgePlanStatus(value: unknown): PurgePlanStatus | null {
  switch (value) {
    case "ready":
    case "bound":
    case "executing":
    case "consumed":
    case "invalidated":
      return value;
    default:
      return null;
  }
}

function present(value: unknown): boolean {
  return value !== undefined && value !== null && value !== "";
}

function uniqueOpaqueIds(items: Array<{ recoveryPointId: string }>): boolean {
  const seen = new Set<string>();
  for (const item of items) {
    if (seen.has(item.recoveryPointId)) {
      return false;
    }
    seen.add(item.recoveryPointId);
  }
  return true;
}

function validStepUpProof(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 8192 && !/[\r\n\0\s]/.test(value);
}

function mapRules(value: unknown): RetentionPolicyRules | null {
  if (!isRawObject(value) || value.version !== 1) {
    return null;
  }
  const rules: RetentionPolicyRules = { version: 1 };
  if (value.age !== undefined) {
    if (!isRawObject(value.age)) {
      return null;
    }
    const keepDays = finiteInteger(value.age.keep_days, 1);
    if (keepDays === null || keepDays > 36500) {
      return null;
    }
    rules.age = { keepDays };
  }
  if (value.count !== undefined) {
    if (!isRawObject(value.count)) {
      return null;
    }
    const keepLatest = finiteInteger(value.count.keep_latest, 1);
    if (keepLatest === null || keepLatest > 1_000_000) {
      return null;
    }
    rules.count = { keepLatest };
  }
  if (value.calendar !== undefined) {
    if (!Array.isArray(value.calendar) || value.calendar.length > 4) {
      return null;
    }
    const calendar: Array<{ unit: RetentionCalendarUnit; keep: number }> = [];
    const seen = new Set<RetentionCalendarUnit>();
    for (const item of value.calendar) {
      if (!isRawObject(item)) {
        return null;
      }
      const unit = calendarUnit(item.unit);
      const keep = finiteInteger(item.keep, 1);
      if (unit === null || keep === null || keep > 10000 || seen.has(unit)) {
        return null;
      }
      seen.add(unit);
      calendar.push({ unit, keep });
    }
    rules.calendar = calendar;
  }
  if (rules.age === undefined && rules.count === undefined && (rules.calendar === undefined || rules.calendar.length === 0)) {
    return null;
  }
  return rules;
}

function validHoldCreateInput(input: {
  holdType: RecoveryPointHoldType;
  reason: string;
  expiresAt?: string;
}): boolean {
  if (!validHoldReason(input.reason)) {
    return false;
  }
  if (input.holdType === "legal") {
    return input.expiresAt === undefined;
  }
  return input.holdType === "operational" && input.expiresAt !== undefined && normalizeCatalogTime(input.expiresAt) !== null;
}

function validHoldReason(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.length > 0 && trimmed.length <= 4096 && !trimmed.includes("\0") && trimmed === value;
}

function wireRules(rules: RetentionPolicyRules): Record<string, unknown> {
  return {
    version: rules.version,
    ...(rules.age ? { age: { keep_days: rules.age.keepDays } } : {}),
    ...(rules.count ? { count: { keep_latest: rules.count.keepLatest } } : {}),
    ...(rules.calendar
      ? { calendar: rules.calendar.map((item) => ({ unit: item.unit, keep: item.keep })) }
      : {}),
  };
}

export function mapBackupRetentionPolicy(value: unknown): CatalogProjection<BackupRetentionPolicy> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const kind = scopeKind(value.scope_kind);
  const scopeId = opaqueId(value.scope_id);
  const revision = finiteInteger(value.revision, 1);
  const rules = mapRules(value.rules);
  const status = policyStatus(value.status);
  const createdBy = finiteInteger(value.created_by, 1);
  const updatedBy = finiteInteger(value.updated_by, 1);
  const createdAt = normalizeCatalogTime(value.created_at);
  const updatedAt = normalizeCatalogTime(value.updated_at);
  const ruleDigest = typeof value.rule_digest === "string" && /^[0-9a-f]{64}$/.test(value.rule_digest)
    ? value.rule_digest
    : null;
  if (
    id === null ||
    kind === null ||
    scopeId === null ||
    revision === null ||
    rules === null ||
    status === null ||
    createdBy === null ||
    updatedBy === null ||
    createdAt === null ||
    updatedAt === null ||
    ruleDigest === null
  ) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      scopeKind: kind,
      scopeId,
      revision,
      rules,
      ruleDigest,
      status,
      createdBy,
      updatedBy,
      createdAt,
      updatedAt,
    },
  };
}

function mapImpactPoint(value: unknown): BackupRetentionImpactPoint | null {
  if (!isRawObject(value)) {
    return null;
  }
  const recoveryPointId = opaqueId(value.recovery_point_id);
  const pointRevision = finiteInteger(value.point_revision, 1);
  const capabilityRevision = finiteInteger(value.capability_revision, 1);
  if (recoveryPointId === null || pointRevision === null || capabilityRevision === null) {
    return null;
  }
  return { recoveryPointId, pointRevision, capabilityRevision };
}

export function mapBackupRetentionImpact(value: unknown): CatalogProjection<BackupRetentionImpact> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const policyId = opaqueId(value.policy_id);
  const policyRevision = finiteInteger(value.policy_revision, 1);
  const impactRevision = finiteInteger(value.impact_revision, 1);
  const evaluatedAt = normalizeCatalogTime(value.evaluated_at);
  const selectedCount = finiteInteger(value.selected_count);
  const holdCount = finiteInteger(value.hold_count);
  const leaseCount = finiteInteger(value.lease_count);
  const wormCount = finiteInteger(value.worm_count);
  if (
    policyId === null ||
    policyRevision === null ||
    impactRevision === null ||
    evaluatedAt === null ||
    selectedCount === null ||
    holdCount === null ||
    leaseCount === null ||
    wormCount === null ||
    !Array.isArray(value.points)
  ) {
    return blocked();
  }
  const points: BackupRetentionImpactPoint[] = [];
  for (const item of value.points) {
    const mapped = mapImpactPoint(item);
    if (mapped === null) {
      return blocked();
    }
    points.push(mapped);
  }
  if (selectedCount !== points.length || !uniqueOpaqueIds(points)) {
    return blocked();
  }
  const nextCursor = typeof value.next_cursor === "string" && value.next_cursor.trim() !== ""
    ? value.next_cursor
    : null;
  return {
    status: "available",
    value: {
      policyId,
      policyRevision,
      impactRevision,
      evaluatedAt,
      selectedCount,
      holdCount,
      leaseCount,
      wormCount,
      points,
      nextCursor,
    },
  };
}

export function mapBackupRetentionHoldRecord(value: unknown): CatalogProjection<BackupRetentionHoldRecord> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const recoveryPointId = opaqueId(value.recovery_point_id);
  const type = holdType(value.hold_type);
  const state = holdRecordState(value.state);
  const createdBy = finiteInteger(value.created_by, 1);
  const createdAt = normalizeCatalogTime(value.created_at);
  const updatedAt = normalizeCatalogTime(value.updated_at);
  const expiresAt = present(value.expires_at) ? normalizeCatalogTime(value.expires_at) : null;
  const releasedBy = present(value.released_by) ? finiteInteger(value.released_by, 1) : null;
  const releasedAt = present(value.released_at) ? normalizeCatalogTime(value.released_at) : null;
  if (
    id === null ||
    recoveryPointId === null ||
    type === null ||
    state === null ||
    createdBy === null ||
    createdAt === null ||
    updatedAt === null ||
    (type === "legal" && present(value.expires_at)) ||
    (type === "operational" && expiresAt === null) ||
    (state === "active" && (present(value.released_by) || present(value.released_at))) ||
    (state === "released" && (releasedBy === null || releasedAt === null))
  ) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      recoveryPointId,
      holdType: type,
      state,
      createdBy,
      expiresAt,
      releasedBy,
      releasedAt,
      createdAt,
      updatedAt,
    },
  };
}

export function mapBackupRetentionPurgeImpact(value: unknown): CatalogProjection<BackupRetentionPurgeImpact> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const repositoryId = opaqueId(value.repository_id);
  const impactRevision = finiteInteger(value.impact_revision, 1);
  const selectedCount = finiteInteger(value.selected_count);
  const holdCount = finiteInteger(value.hold_count);
  const leaseCount = finiteInteger(value.lease_count);
  const wormCount = finiteInteger(value.worm_count);
  if (
    repositoryId === null ||
    impactRevision === null ||
    selectedCount === null ||
    holdCount === null ||
    leaseCount === null ||
    wormCount === null ||
    !Array.isArray(value.points)
  ) {
    return blocked();
  }
  const points: BackupRetentionImpactPoint[] = [];
  for (const item of value.points) {
    const mapped = mapImpactPoint(item);
    if (mapped === null) {
      return blocked();
    }
    points.push(mapped);
  }
  if (selectedCount !== points.length || !uniqueOpaqueIds(points)) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      repositoryId,
      impactRevision,
      selectedCount,
      holdCount,
      leaseCount,
      wormCount,
      points,
    },
  };
}

export function mapBackupRetentionPurgePlan(value: unknown): CatalogProjection<BackupRetentionPurgePlan> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const repositoryId = opaqueId(value.repository_id);
  const revision = finiteInteger(value.revision, 1);
  const impactRevision = finiteInteger(value.impact_revision, 1);
  const expiresAt = normalizeCatalogTime(value.expires_at);
  const holdCount = finiteInteger(value.hold_count);
  const leaseCount = finiteInteger(value.lease_count);
  const wormCount = finiteInteger(value.worm_count);
  const status = purgePlanStatus(value.status);
  const itemCount = finiteInteger(value.item_count);
  if (
    id === null ||
    repositoryId === null ||
    revision === null ||
    impactRevision === null ||
    expiresAt === null ||
    holdCount === null ||
    leaseCount === null ||
    wormCount === null ||
    status === null ||
    itemCount === null ||
    !Array.isArray(value.items)
  ) {
    return blocked();
  }
  const items: BackupRetentionPurgePlanItem[] = [];
  for (const item of value.items) {
    const mapped = mapImpactPoint(item);
    if (mapped === null) {
      return blocked();
    }
    items.push(mapped);
  }
  if (itemCount !== items.length || !uniqueOpaqueIds(items)) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      repositoryId,
      revision,
      impactRevision,
      expiresAt,
      holdCount,
      leaseCount,
      wormCount,
      status,
      itemCount,
      items,
    },
  };
}

export function mapBackupRetentionPurgeResult(value: unknown): CatalogProjection<BackupRetentionPurgeResult> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const planId = opaqueId(value.plan_id);
  const claimed = finiteInteger(value.claimed);
  const blockedCount = finiteInteger(value.blocked);
  if (planId === null || claimed === null || blockedCount === null) {
    return blocked();
  }
  return {
    status: "available",
    value: { planId, claimed, blocked: blockedCount },
  };
}

function appendQuery(path: string, query: URLSearchParams): string {
  const encoded = query.toString();
  return encoded === "" ? path : `${path}?${encoded}`;
}

export function createBackupRetentionApi() {
  return {
    async listRetentionPolicies(
      token: string,
      options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
    ): Promise<BackupRetentionPolicyPage> {
      const query = new URLSearchParams();
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      if (options.cursor) query.set("cursor", options.cursor);
      const raw = await request<unknown>(appendQuery("/backup-retention-policies", query), {
        token,
        signal: options.signal,
      });
      const page = isRawObject(raw) ? raw : {};
      return {
        items: Array.isArray(page.items) ? page.items.map(mapBackupRetentionPolicy) : [],
        nextCursor: typeof page.next_cursor === "string" && page.next_cursor !== "" ? page.next_cursor : null,
      };
    },

    async createRetentionPolicy(
      token: string,
      input: { scopeKind: RetentionPolicyScopeKind; scopeId: string; rules: RetentionPolicyRules },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPolicy>> {
      const raw = await request<unknown>("/backup-retention-policies", {
        method: "POST",
        token,
        signal,
        body: {
          scope_kind: input.scopeKind,
          scope_id: input.scopeId,
          rules: wireRules(input.rules),
        },
      });
      return mapBackupRetentionPolicy(raw);
    },

    async updateRetentionPolicy(
      token: string,
      policyId: string,
      input: { expectedRevision: number; rules: RetentionPolicyRules },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPolicy>> {
      const raw = await request<unknown>(`/backup-retention-policies/${encodeURIComponent(policyId)}`, {
        method: "PATCH",
        token,
        signal,
        body: {
          expected_revision: input.expectedRevision,
          rules: wireRules(input.rules),
        },
      });
      return mapBackupRetentionPolicy(raw);
    },

    async deleteRetentionPolicy(
      token: string,
      policyId: string,
      expectedRevision: number,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPolicy>> {
      const raw = await request<unknown>(`/backup-retention-policies/${encodeURIComponent(policyId)}`, {
        method: "DELETE",
        token,
        signal,
        body: { expected_revision: expectedRevision },
      });
      return mapBackupRetentionPolicy(raw);
    },

    async previewRetentionPolicyImpact(
      token: string,
      policyId: string,
      expectedRevision: number,
      signal?: AbortSignal,
      options?: { cursor?: string; limit?: number; evaluatedAt?: string },
    ): Promise<CatalogProjection<BackupRetentionImpact>> {
      const raw = await request<unknown>(`/backup-retention-policies/${encodeURIComponent(policyId)}/impact`, {
        method: "POST",
        token,
        signal,
        body: {
          expected_revision: expectedRevision,
          ...(options?.limit ? { limit: options.limit } : {}),
          ...(options?.cursor ? { cursor: options.cursor } : {}),
          ...(options?.evaluatedAt ? { evaluated_at: options.evaluatedAt } : {}),
        },
      });
      return mapBackupRetentionImpact(raw);
    },

    async listRecoveryPointHolds(
      token: string,
      recoveryPointId: string,
      signal?: AbortSignal,
    ): Promise<{ items: Array<CatalogProjection<BackupRetentionHoldRecord>> }> {
      const raw = await request<unknown>(`/recovery-points/${encodeURIComponent(recoveryPointId)}/holds`, {
        token,
        signal,
      });
      const page = isRawObject(raw) ? raw : {};
      return {
        items: Array.isArray(page.items) ? page.items.map(mapBackupRetentionHoldRecord) : [],
      };
    },

    async createRecoveryPointHold(
      token: string,
      recoveryPointId: string,
      input: { holdType: RecoveryPointHoldType; reason: string; expiresAt?: string },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionHoldRecord>> {
      if (!validHoldCreateInput(input)) {
        return blocked();
      }
      const raw = await request<unknown>(`/recovery-points/${encodeURIComponent(recoveryPointId)}/holds`, {
        method: "POST",
        token,
        signal,
        body: {
          hold_type: input.holdType,
          reason: input.reason,
          ...(input.expiresAt ? { expires_at: input.expiresAt } : {}),
        },
      });
      return mapBackupRetentionHoldRecord(raw);
    },

    async releaseRecoveryPointHold(
      token: string,
      recoveryPointId: string,
      holdId: string,
      input: { reason: string; stepUpProof: string },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionHoldRecord>> {
      if (!validHoldReason(input.reason) || !validStepUpProof(input.stepUpProof)) {
        return blocked();
      }
      const raw = await request<unknown>(
        `/recovery-points/${encodeURIComponent(recoveryPointId)}/holds/${encodeURIComponent(holdId)}/release`,
        {
          method: "POST",
          token,
          signal,
          stepUpProof: input.stepUpProof,
          body: { reason: input.reason },
        },
      );
      return mapBackupRetentionHoldRecord(raw);
    },

    async previewRepositoryPurge(
      token: string,
      repositoryId: string,
      input: { recoveryPointIds: string[] },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPurgeImpact>> {
      if (!Array.isArray(input.recoveryPointIds) || input.recoveryPointIds.length === 0 || !input.recoveryPointIds.every((id) => opaqueId(id))) {
        return blocked();
      }
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/purge-preview`, {
        method: "POST",
        token,
        signal,
        body: { recovery_point_ids: input.recoveryPointIds },
      });
      return mapBackupRetentionPurgeImpact(raw);
    },

    async createRepositoryPurgePlan(
      token: string,
      repositoryId: string,
      input: { expectedImpactRevision: number; items: BackupRetentionPurgePlanItem[] },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPurgePlan>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/purge-plans`, {
        method: "POST",
        token,
        signal,
        body: {
          expected_impact_revision: input.expectedImpactRevision,
          items: input.items.map((item) => ({
            recovery_point_id: item.recoveryPointId,
            point_revision: item.pointRevision,
            capability_revision: item.capabilityRevision,
          })),
        },
      });
      return mapBackupRetentionPurgePlan(raw);
    },

    async executeRepositoryPurge(
      token: string,
      repositoryId: string,
      input: {
        planId: string;
        expectedRevision: number;
        expectedImpactRevision: number;
        reason: string;
        stepUpProof: string;
      },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRetentionPurgeResult>> {
      if (!validHoldReason(input.reason) || !validStepUpProof(input.stepUpProof)) {
        return blocked();
      }
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/purges`, {
        method: "POST",
        token,
        signal,
        stepUpProof: input.stepUpProof,
        body: {
          plan_id: input.planId,
          expected_revision: input.expectedRevision,
          expected_impact_revision: input.expectedImpactRevision,
          reason: input.reason,
        },
      });
      return mapBackupRetentionPurgeResult(raw);
    },
  };
}
