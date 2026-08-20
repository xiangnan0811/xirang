import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import type { AuthContextValue } from "@/context/auth-context.shared";
import { STEP_UP_ACTIONS } from "@/lib/step-up-storage";
import type {
  BackupRecoveryPoint,
  BackupRepository,
  BackupRetentionHoldRecord,
  BackupRetentionImpact,
  BackupRetentionPolicy,
  BackupRetentionPurgeImpact,
  BackupRetentionPolicyPage,
  BackupRetentionPurgePlan,
  BackupRetentionPurgeResult,
  CatalogProjection,
  RetentionCalendarUnit,
  RetentionPolicyRules,
} from "@/types/domain";

export type RetentionLifecycleApi = {
  listRetentionPolicies?(
    token: string,
    options?: { limit?: number; cursor?: string; signal?: AbortSignal },
  ): Promise<BackupRetentionPolicyPage>;
  createRetentionPolicy?(
    token: string,
    input: { scopeKind: "repository" | "task_link"; scopeId: string; rules: RetentionPolicyRules },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionPolicy>>;
  updateRetentionPolicy?(
    token: string,
    policyId: string,
    input: { expectedRevision: number; rules: RetentionPolicyRules },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionPolicy>>;
  deleteRetentionPolicy?(
    token: string,
    policyId: string,
    expectedRevision: number,
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionPolicy>>;
  previewRetentionPolicyImpact?(
    token: string,
    policyId: string,
    expectedRevision: number,
    signal?: AbortSignal,
    options?: { cursor?: string; limit?: number; evaluatedAt?: string },
  ): Promise<CatalogProjection<BackupRetentionImpact>>;
  previewRepositoryPurge?(
    token: string,
    repositoryId: string,
    input: { recoveryPointIds: string[] },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionPurgeImpact>>;
  createRepositoryPurgePlan?(
    token: string,
    repositoryId: string,
    input: { expectedImpactRevision: number; items: BackupRetentionImpact["points"] },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionPurgePlan>>;
  executeRepositoryPurge?(
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
  ): Promise<CatalogProjection<BackupRetentionPurgeResult>>;
  listRecoveryPointHolds?(
    token: string,
    recoveryPointId: string,
    signal?: AbortSignal,
  ): Promise<{ items: Array<CatalogProjection<BackupRetentionHoldRecord>> }>;
  createRecoveryPointHold?(
    token: string,
    recoveryPointId: string,
    input: { holdType: "legal" | "operational"; reason: string; expiresAt?: string },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionHoldRecord>>;
  releaseRecoveryPointHold?(
    token: string,
    recoveryPointId: string,
    holdId: string,
    input: { reason: string; stepUpProof: string },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRetentionHoldRecord>>;
};

export interface RetentionPolicyPanelProps {
  repositories: Array<CatalogProjection<BackupRepository>>;
  recoveryPoints: Array<CatalogProjection<BackupRecoveryPoint>>;
  selectedRepositoryId?: string;
  selectedRecoveryPointId?: string;
  runtime?: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof">;
  onRefresh?: () => void;
  api?: RetentionLifecycleApi;
}

export function RetentionPolicyPanel({
  repositories,
  recoveryPoints,
  selectedRepositoryId,
  selectedRecoveryPointId,
  runtime,
  onRefresh,
  api,
}: RetentionPolicyPanelProps) {
  const { t } = useTranslation();
  const isAdmin = runtime?.role === "admin" && Boolean(runtime.token);
  const [policies, setPolicies] = useState<Array<CatalogProjection<BackupRetentionPolicy>>>([]);
  const [policyCursor, setPolicyCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [impact, setImpact] = useState<BackupRetentionPurgeImpact | null>(null);
  const [policyImpact, setPolicyImpact] = useState<BackupRetentionImpact | null>(null);
  const [calendarDrafts, setCalendarDrafts] = useState<Record<string, Array<{ unit: string; keep: string }>>>({});
  const [selectedPurgePointIds, setSelectedPurgePointIds] = useState<string[]>(
    selectedRecoveryPointId ? [selectedRecoveryPointId] : [],
  );
  const [holds, setHolds] = useState<BackupRetentionHoldRecord[]>([]);
  const [holdOpen, setHoldOpen] = useState(false);
  const [confirmCount, setConfirmCount] = useState("");
  const [purgeReason, setPurgeReason] = useState("");
  const [holdReason, setHoldReason] = useState("");
  const [holdExpiresAt, setHoldExpiresAt] = useState("");
  const [releaseReasonByHold, setReleaseReasonByHold] = useState<Record<string, string>>({});
  const [createKeepDays, setCreateKeepDays] = useState("30");
  const [keepDaysDraft, setKeepDaysDraft] = useState<Record<string, string>>({});
  const [pendingDelete, setPendingDelete] = useState<BackupRetentionPolicy | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState("");
  const deleteTriggerRef = useRef<HTMLButtonElement | null>(null);
  const [notice, setNotice] = useState<"conflict" | "blocked" | "success" | "partial" | "claimed" | null>(null);
  const [busy, setBusy] = useState(false);
  const [holdPointId, setHoldPointId] = useState(selectedRecoveryPointId ?? "");
  const [holdTypeDraft, setHoldTypeDraft] = useState<"legal" | "operational">("legal");
  const [createKeepLatest, setCreateKeepLatest] = useState("");
  const [keepLatestDraft, setKeepLatestDraft] = useState<Record<string, string>>({});
  const [createCalendarUnit, setCreateCalendarUnit] = useState("");
  const [createCalendarKeep, setCreateCalendarKeep] = useState("");
  const [createScope, setCreateScope] = useState<"repository" | "task_link">("repository");
  const [createTaskLinkId, setCreateTaskLinkId] = useState("");
  const previewTriggerRef = useRef<HTMLButtonElement | null>(null);
  const holdTriggerRef = useRef<HTMLButtonElement | null>(null);

  const scopedRepository = resolveSelected(repositories, selectedRepositoryId);
  const scopedPoints = recoveryPoints.flatMap((item) => (
    item.status === "available" && (!scopedRepository || item.value.repositoryId === scopedRepository.id)
      ? [item.value]
      : []
  ));
  const eligiblePurgePoints = scopedPoints.filter(explicitPurgeEligible);
  const eligibleHoldPoints = scopedPoints.filter(holdEligibleRecoveryPoint);
  const selectedRecoveryPoint = eligibleHoldPoints.find((point) => point.id === holdPointId)
    ?? eligibleHoldPoints.find((point) => point.id === selectedRecoveryPointId);
  const activeTaskLinkIds = scopedRepository?.lineages.flatMap((lineage) => (
    lineage.source === "task_link" && lineage.active && lineage.taskRepositoryLinkId
      ? [lineage.taskRepositoryLinkId]
      : []
  )) ?? [];
  const availablePolicies = policies.flatMap((item) => {
    if (item.status !== "available" || item.value.status !== "active") {
      return [];
    }
    const policy = item.value;
    if (!scopedRepository) {
      return [];
    }
    if (policy.scopeKind === "repository" && policy.scopeId === scopedRepository.id) {
      return [policy];
    }
    if (policy.scopeKind === "task_link" && activeTaskLinkIds.includes(policy.scopeId)) {
      return [policy];
    }
    return [];
  });

  const resolveApi = useCallback(async (): Promise<RetentionLifecycleApi> => {
    if (api) return api;
    return (await import("@/lib/api/client")).apiClient;
  }, [api]);

  useEffect(() => {
    if (!isAdmin || !runtime?.token) {
      return;
    }
    const token = runtime.token;
    const controller = new AbortController();
    setLoading(true);
    void (async () => {
      try {
        const client = await resolveApi();
        if (!client.listRetentionPolicies) {
          if (!controller.signal.aborted) {
            setNotice("blocked");
          }
          return;
        }
        const page = await client.listRetentionPolicies(token, { signal: controller.signal });
        if (!controller.signal.aborted) {
          setPolicies(page.items);
          setPolicyCursor(page.nextCursor);
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setNotice(statusOf(error) === 409 ? "conflict" : "blocked");
        }
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      }
    })();
    return () => controller.abort();
  }, [api, isAdmin, resolveApi, runtime?.token]);

  useEffect(() => {
    if (selectedRecoveryPointId) {
      setHoldPointId(selectedRecoveryPointId);
    }
  }, [selectedRecoveryPointId]);

  useEffect(() => {
    if (holdPointId) {
      return;
    }
    if (eligibleHoldPoints.length === 1) {
      setHoldPointId(eligibleHoldPoints[0].id);
    }
  }, [holdPointId, eligibleHoldPoints]);

  const activeTaskLinkKey = activeTaskLinkIds.join(",");

  useEffect(() => {
    const ids = activeTaskLinkKey === "" ? [] : activeTaskLinkKey.split(",");
    setCreateTaskLinkId((current) => (current && ids.includes(current) ? current : (ids[0] ?? "")));
  }, [activeTaskLinkKey]);

  useEffect(() => {
    if (!isAdmin || !runtime?.token || !holdPointId) {
      setHolds([]);
      return;
    }
    const token = runtime.token;
    const controller = new AbortController();
    void (async () => {
      try {
        const client = await resolveApi();
        if (!client.listRecoveryPointHolds) {
          return;
        }
        const page = await client.listRecoveryPointHolds(token, holdPointId, controller.signal);
        if (controller.signal.aborted) {
          return;
        }
        setHolds(page.items.flatMap((item) => (item.status === "available" ? [item.value] : [])));
      } catch (error) {
        if (!controller.signal.aborted) {
          setNotice(statusOf(error) === 409 ? "conflict" : "blocked");
        }
      }
    })();
    return () => controller.abort();
  }, [api, holdPointId, isAdmin, resolveApi, runtime?.token]);

  if (!isAdmin || !runtime?.token) {
    return null;
  }

  const token = runtime.token;

  const run = async (action: (client: RetentionLifecycleApi) => Promise<void>) => {
    setBusy(true);
    setNotice(null);
    try {
      await action(await resolveApi());
    } catch (error) {
      setNotice(statusOf(error) === 409 ? "conflict" : "blocked");
    } finally {
      setBusy(false);
    }
  };

  const closePurgeDialog = () => {
    setImpact(null);
    setConfirmCount("");
    setPurgeReason("");
    queueMicrotask(() => previewTriggerRef.current?.focus());
  };

  const closeHoldDialog = () => {
    setHoldOpen(false);
    queueMicrotask(() => holdTriggerRef.current?.focus());
  };

  const closeDeleteDialog = () => {
    setPendingDelete(null);
    setDeleteConfirm("");
    queueMicrotask(() => deleteTriggerRef.current?.focus());
  };

  return (
    <section
      aria-label={t("backupAssets.lifecycle.policiesTitle")}
      className="space-y-4 border-b border-border px-3 py-4"
    >
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold">{t("backupAssets.lifecycle.policiesTitle")}</h2>
        {availablePolicies[0] ? <Badge tone="neutral">{availablePolicies[0].revision}</Badge> : null}
      </div>

      {notice === "conflict" ? <InlineAlert tone="critical">{t("backupAssets.lifecycle.conflict")}</InlineAlert> : null}
      {notice === "blocked" ? <InlineAlert tone="warning">{t("backupAssets.lifecycle.blocked")}</InlineAlert> : null}
      {notice === "success" ? <InlineAlert tone="success">{t("backupAssets.lifecycle.success")}</InlineAlert> : null}
      {notice === "claimed" ? <InlineAlert tone="info">{t("backupAssets.lifecycle.purgeClaimed")}</InlineAlert> : null}
      {notice === "partial" ? <InlineAlert tone="warning">{t("backupAssets.lifecycle.purgePartial")}</InlineAlert> : null}

      {loading ? <LoadingState title={t("backupAssets.lifecycle.loading")} rows={3} /> : null}
      {!loading && availablePolicies.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("backupAssets.lifecycle.policiesEmpty")}</p>
      ) : null}

      <div className="flex flex-wrap items-end gap-2">
        <label className="space-y-1 text-sm">
          <span>{t("backupAssets.lifecycle.keepDaysInput")}</span>
          <Input
            aria-label={t("backupAssets.lifecycle.keepDaysInput")}
            inputMode="numeric"
            value={createKeepDays}
            onChange={(event) => setCreateKeepDays(event.target.value)}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span>{t("backupAssets.lifecycle.keepLatestInput")}</span>
          <Input
            aria-label={t("backupAssets.lifecycle.keepLatestInput")}
            inputMode="numeric"
            value={createKeepLatest}
            onChange={(event) => setCreateKeepLatest(event.target.value)}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span>{t("backupAssets.lifecycle.calendarUnit")}</span>
          <select
            aria-label={t("backupAssets.lifecycle.calendarUnit")}
            className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
            value={createCalendarUnit}
            onChange={(event) => setCreateCalendarUnit(event.target.value)}
          >
            <option value="" />
            {calendarUnitOptions().map((unit) => (
              <option key={unit} value={unit}>{t(`backupAssets.lifecycle.calendarUnit${capitalizeUnit(unit)}`)}</option>
            ))}
          </select>
        </label>
        <label className="space-y-1 text-sm">
          <span>{t("backupAssets.lifecycle.calendarKeep")}</span>
          <Input
            aria-label={t("backupAssets.lifecycle.calendarKeep")}
            inputMode="numeric"
            value={createCalendarKeep}
            onChange={(event) => setCreateCalendarKeep(event.target.value)}
          />
        </label>
        {activeTaskLinkIds.length > 0 ? (
          <label className="space-y-1 text-sm">
            <span>{t("backupAssets.lifecycle.policyScope")}</span>
            <select
              aria-label={t("backupAssets.lifecycle.policyScope")}
              className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
              value={createScope}
              onChange={(event) => setCreateScope(event.target.value === "task_link" ? "task_link" : "repository")}
            >
              <option value="repository">{t("backupAssets.lifecycle.repositoryScope")}</option>
              <option value="task_link">{t("backupAssets.lifecycle.taskLinkScope")}</option>
            </select>
          </label>
        ) : null}
        {createScope === "task_link" && activeTaskLinkIds.length > 1 ? (
          <label className="space-y-1 text-sm">
            <span>{t("backupAssets.lifecycle.taskLinkScope")}</span>
            <select
              aria-label={t("backupAssets.lifecycle.taskLinkScope")}
              className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
              value={createTaskLinkId}
              onChange={(event) => setCreateTaskLinkId(event.target.value)}
            >
              {activeTaskLinkIds.map((linkId) => (
                <option key={linkId} value={linkId}>{linkId.slice(0, 8)}</option>
              ))}
            </select>
          </label>
        ) : null}
        <Button
          type="button"
          size="sm"
          disabled={busy || !scopedRepository || !canCreatePolicy(createKeepDays, createKeepLatest, createCalendarUnit, createCalendarKeep)}
          onClick={() => {
            if (!scopedRepository) return;
            void run(async (client) => {
              if (!client.createRetentionPolicy) {
                setNotice("blocked");
                return;
              }
              const scopeKind = createScope === "task_link" && activeTaskLinkIds.length > 0 ? "task_link" : "repository";
              const scopeId = scopeKind === "task_link"
                ? (createTaskLinkId || activeTaskLinkIds[0])
                : scopedRepository.id;
              const result = await client.createRetentionPolicy(
                token,
                {
                  scopeKind,
                  scopeId,
                  rules: buildPolicyRules({
                    keepDays: createKeepDays,
                    keepLatest: createKeepLatest,
                    calendarUnit: createCalendarUnit,
                    calendarKeep: createCalendarKeep,
                  }),
                },
                new AbortController().signal,
              );
              if (result.status === "blocked") {
                setNotice("blocked");
                return;
              }
              setPolicies((current) => [result, ...current]);
              setNotice("success");
              onRefresh?.();
            });
          }}
        >
          {t("backupAssets.lifecycle.createPolicy")}
        </Button>
      </div>

      {policyCursor ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => {
            void run(async (client) => {
              if (!client.listRetentionPolicies || !policyCursor) {
                return;
              }
              const page = await client.listRetentionPolicies(token, {
                cursor: policyCursor,
                signal: new AbortController().signal,
              });
              setPolicies((current) => [...current, ...page.items]);
              setPolicyCursor(page.nextCursor);
            });
          }}
        >
          {t("backupAssets.actions.loadMore")}
        </Button>
      ) : null}

      {availablePolicies.map((policy) => {
        const draft = keepDaysDraft[policy.id] ?? String(policy.rules.age?.keepDays ?? "");
        const latestDraft = keepLatestDraft[policy.id] ?? String(policy.rules.count?.keepLatest ?? "");
        const calendarRules = calendarDrafts[policy.id] ?? (
          policy.rules.calendar && policy.rules.calendar.length > 0
            ? policy.rules.calendar.map((rule) => ({ unit: rule.unit, keep: String(rule.keep) }))
            : [{ unit: "", keep: "" }]
        );
        return (
          <div key={policy.id} className="space-y-3 rounded-md border border-border p-3">
            <p>
              {t("backupAssets.lifecycle.policyScope")}: {policy.scopeKind === "task_link"
                ? t("backupAssets.lifecycle.taskLinkScope")
                : t("backupAssets.lifecycle.repositoryScope")} {policy.scopeId.slice(0, 8)}
            </p>
            {policy.rules.age ? (
              <p>{t("backupAssets.lifecycle.keepDays", { days: policy.rules.age.keepDays })}</p>
            ) : null}
            <label className="block space-y-1 text-sm">
              <span>{t("backupAssets.lifecycle.keepDaysInput")}</span>
              <Input
                aria-label={`${t("backupAssets.lifecycle.keepDaysInput")} ${policy.id}`}
                inputMode="numeric"
                value={draft}
                onChange={(event) => setKeepDaysDraft((current) => ({ ...current, [policy.id]: event.target.value }))}
              />
            </label>
            <label className="block space-y-1 text-sm">
              <span>{t("backupAssets.lifecycle.keepLatestInput")}</span>
              <Input
                aria-label={`${t("backupAssets.lifecycle.keepLatestInput")} ${policy.id}`}
                inputMode="numeric"
                value={latestDraft}
                onChange={(event) => setKeepLatestDraft((current) => ({ ...current, [policy.id]: event.target.value }))}
              />
            </label>
            {calendarRules.map((rule, index) => (
              <div key={`${policy.id}-calendar-${index}`} className="space-y-2">
                <label className="block space-y-1 text-sm">
                  <span>{t("backupAssets.lifecycle.calendarUnit")}</span>
                  <select
                    aria-label={`${t("backupAssets.lifecycle.calendarUnit")} ${policy.id} ${index}`}
                    className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                    value={rule.unit}
                    onChange={(event) => {
                      const unit = event.target.value;
                      setCalendarDrafts((current) => {
                        const next = [...(current[policy.id] ?? calendarRules)];
                        next[index] = { ...next[index], unit };
                        return { ...current, [policy.id]: next };
                      });
                    }}
                  >
                    <option value="" />
                    {calendarUnitOptions().map((unit) => (
                      <option key={unit} value={unit}>{t(`backupAssets.lifecycle.calendarUnit${capitalizeUnit(unit)}`)}</option>
                    ))}
                  </select>
                </label>
                <label className="block space-y-1 text-sm">
                  <span>{t("backupAssets.lifecycle.calendarKeep")}</span>
                  <Input
                    aria-label={`${t("backupAssets.lifecycle.calendarKeep")} ${policy.id} ${index}`}
                    inputMode="numeric"
                    value={rule.keep}
                    onChange={(event) => {
                      const keep = event.target.value;
                      setCalendarDrafts((current) => {
                        const next = [...(current[policy.id] ?? calendarRules)];
                        next[index] = { ...next[index], keep };
                        return { ...current, [policy.id]: next };
                      });
                    }}
                  />
                </label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={`${t("backupAssets.lifecycle.removeCalendarRule")} ${policy.id} ${index}`}
                  onClick={() => {
                    setCalendarDrafts((current) => {
                      const next = [...(current[policy.id] ?? calendarRules)];
                      next.splice(index, 1);
                      return { ...current, [policy.id]: next.length > 0 ? next : [{ unit: "", keep: "" }] };
                    });
                  }}
                >
                  {t("backupAssets.lifecycle.removeCalendarRule")}
                </Button>
              </div>
            ))}
            {calendarRules.length < 4 ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-label={`${t("backupAssets.lifecycle.addCalendarRule")} ${policy.id}`}
                onClick={() => {
                  setCalendarDrafts((current) => {
                    const next = [...(current[policy.id] ?? calendarRules)];
                    const used = new Set(next.map((item) => item.unit));
                    const unit = calendarUnitOptions().find((option) => !used.has(option)) ?? "";
                    next.push({ unit, keep: "" });
                    return { ...current, [policy.id]: next };
                  });
                }}
              >
                {t("backupAssets.lifecycle.addCalendarRule")}
              </Button>
            ) : null}
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                    disabled={busy || !canUpdatePolicy(
                      draft,
                      latestDraft,
                      calendarRules,
                      policy.rules,
                      keepDaysDraft[policy.id] !== undefined,
                      keepLatestDraft[policy.id] !== undefined,
                      calendarDrafts[policy.id] !== undefined,
                    )}
                aria-label={`${t("backupAssets.lifecycle.updatePolicy")} ${policy.id}`}
                onClick={() => {
                  void run(async (client) => {
                    if (!client.updateRetentionPolicy) {
                      setNotice("blocked");
                      return;
                    }
                    const result = await client.updateRetentionPolicy(
                      token,
                      policy.id,
                      {
                        expectedRevision: policy.revision,
                        rules: buildPolicyRules({
                            keepDays: draft,
                            keepLatest: latestDraft,
                            calendarUnit: calendarRules[0]?.unit ?? "",
                            calendarKeep: calendarRules[0]?.keep ?? "",
                            calendarRules,
                            fallback: policy.rules,
                            keepDaysTouched: keepDaysDraft[policy.id] !== undefined,
                            keepLatestTouched: keepLatestDraft[policy.id] !== undefined,
                            calendarTouched: calendarDrafts[policy.id] !== undefined,
                          }),
                      },
                      new AbortController().signal,
                    );
                    if (result.status === "blocked") {
                      setNotice("blocked");
                      return;
                    }
                    setPolicies((current) => current.map((item) => (item.status === "available" && item.value.id === policy.id ? result : item)));
                    setNotice("success");
                    onRefresh?.();
                  });
                }}
              >
                {t("backupAssets.lifecycle.updatePolicy")}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                aria-label={`${t("backupAssets.lifecycle.deletePolicy")} ${policy.id}`}
                onClick={(event) => {
                  deleteTriggerRef.current = event.currentTarget;
                  setPendingDelete(policy);
                  setDeleteConfirm("");
                }}
              >
                {t("backupAssets.lifecycle.deletePolicy")}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={busy}
                aria-label={`${t("backupAssets.lifecycle.previewImpact")} ${policy.id}`}
                onClick={(event) => {
                  previewTriggerRef.current = event.currentTarget;
                  void run(async (client) => {
                    if (!client.previewRetentionPolicyImpact) {
                      setNotice("blocked");
                      return;
                    }
                    const result = await client.previewRetentionPolicyImpact(
                      token,
                      policy.id,
                      policy.revision,
                      new AbortController().signal,
                    );
                    if (result.status === "blocked") {
                      setNotice("blocked");
                      return;
                    }
                    setPolicyImpact(result.value);
                    setNotice("success");
                  });
                }}
              >
                {t("backupAssets.lifecycle.previewImpact")}
              </Button>
            </div>
          </div>
        );
      })}

      {policyImpact ? (
        <div className="space-y-2 rounded-md border border-border p-3">
          <p>{t("backupAssets.lifecycle.selectedCount", { value: policyImpact.selectedCount })}</p>
          <p>{t("backupAssets.lifecycle.holdCount", { value: policyImpact.holdCount })}</p>
          <p>{t("backupAssets.lifecycle.leaseCount", { value: policyImpact.leaseCount })}</p>
          <p>{t("backupAssets.lifecycle.wormCount", { value: policyImpact.wormCount })}</p>
          <ul aria-label={t("backupAssets.lifecycle.selectedPointIds")} className="space-y-1 text-xs text-muted-foreground">
            {policyImpact.points.map((point) => (
              <li key={point.recoveryPointId}>{point.recoveryPointId}</li>
            ))}
          </ul>
          {policyImpact.nextCursor ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => {
                const cursor = policyImpact.nextCursor;
                const policy = availablePolicies.find((item) => item.id === policyImpact.policyId);
                if (!cursor || !policy) {
                  return;
                }
                void run(async (client) => {
                  if (!client.previewRetentionPolicyImpact) {
                    setNotice("blocked");
                    return;
                  }
                  const result = await client.previewRetentionPolicyImpact(
                    token,
                    policy.id,
                    policy.revision,
                    new AbortController().signal,
                    { cursor, evaluatedAt: policyImpact.evaluatedAt },
                  );
                  if (result.status === "blocked") {
                    setNotice("blocked");
                    return;
                  }
                  setPolicyImpact({
                    ...result.value,
                    points: [...policyImpact.points, ...result.value.points],
                    selectedCount: policyImpact.selectedCount + result.value.selectedCount,
                    holdCount: policyImpact.holdCount + result.value.holdCount,
                    leaseCount: policyImpact.leaseCount + result.value.leaseCount,
                    wormCount: policyImpact.wormCount + result.value.wormCount,
                  });
                });
              }}
            >
              {t("backupAssets.actions.loadMore")}
            </Button>
          ) : null}
        </div>
      ) : null}

      {scopedRepository ? (
        <div className="space-y-2">
          <fieldset className="space-y-2">
            <legend className="text-sm">{t("backupAssets.lifecycle.eligiblePoints")}</legend>
            {eligiblePurgePoints.map((point) => (
              <label key={point.id} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={selectedPurgePointIds.includes(point.id)}
                  aria-label={t("backupAssets.lifecycle.selectPurgePoint", { id: point.id })}
                  onChange={(event) => {
                    setSelectedPurgePointIds((current) => (
                      event.target.checked
                        ? [...current.filter((id) => id !== point.id), point.id]
                        : current.filter((id) => id !== point.id)
                    ));
                  }}
                />
                <span>{point.id}</span>
              </label>
            ))}
          </fieldset>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy || selectedPurgePointIds.length === 0}
            aria-label={t("backupAssets.lifecycle.previewPurge")}
            onClick={(event) => {
              previewTriggerRef.current = event.currentTarget;
              void run(async (client) => {
                if (!client.previewRepositoryPurge) {
                  setNotice("blocked");
                  return;
                }
                const result = await client.previewRepositoryPurge(
                  token,
                  scopedRepository.id,
                  { recoveryPointIds: selectedPurgePointIds },
                  new AbortController().signal,
                );
                if (result.status === "blocked") {
                  setNotice("blocked");
                  return;
                }
                setImpact(result.value);
                setConfirmCount("");
                setPurgeReason("");
              });
            }}
          >
            {t("backupAssets.lifecycle.previewPurge")}
          </Button>
        </div>
      ) : null}

      <div className="space-y-2">
        <label className="block space-y-1 text-sm">
          <span>{t("backupAssets.lifecycle.holdPoint")}</span>
          <select
            aria-label={t("backupAssets.lifecycle.holdPoint")}
            className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
            value={holdPointId}
            onChange={(event) => setHoldPointId(event.target.value)}
          >
            <option value="">{t("backupAssets.states.selectRecoveryPoint")}</option>
            {eligibleHoldPoints.map((point) => (
              <option key={point.id} value={point.id}>{point.id}</option>
            ))}
          </select>
        </label>
        <Button
          type="button"
          variant="outline"
          size="sm"
          ref={holdTriggerRef}
          disabled={!selectedRecoveryPoint}
          onClick={() => setHoldOpen(true)}
        >
          {t("backupAssets.lifecycle.createHold")}
        </Button>
        {holds.length > 0 ? (
          <ul className="space-y-2" aria-label={t("backupAssets.lifecycle.holdActive")}>
            {holds.map((hold) => (
              <li
                key={hold.id}
                className="space-y-2 rounded-md border border-border p-3 text-sm"
                aria-label={t("backupAssets.lifecycle.holdRow", { type: hold.holdType, id: hold.id })}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone={hold.holdType === "legal" ? "warning" : "info"}>
                    {hold.holdType === "legal"
                      ? t("backupAssets.lifecycle.holdTypeLegal")
                      : t("backupAssets.lifecycle.holdTypeOperational")}
                  </Badge>
                  {hold.state === "active" ? <span>{t("backupAssets.lifecycle.holdActive")}</span> : null}
                </div>
                {hold.state === "active" ? (
                  <div className="flex flex-wrap items-end gap-2">
                    <label className="space-y-1 text-sm">
                      <span>{t("backupAssets.lifecycle.releaseReason")}</span>
                      <Input
                        aria-label={`${t("backupAssets.lifecycle.releaseReason")} ${hold.id}`}
                        value={releaseReasonByHold[hold.id] ?? ""}
                        onChange={(event) => setReleaseReasonByHold((current) => ({
                          ...current,
                          [hold.id]: event.target.value,
                        }))}
                      />
                    </label>
                    <Button
                      type="button"
                      size="sm"
                      disabled={busy || (releaseReasonByHold[hold.id] ?? "").trim() === "" || !selectedRecoveryPoint}
                      aria-label={`${t("backupAssets.lifecycle.releaseHold")} ${hold.id}`}
                      onClick={() => {
                        if (!selectedRecoveryPoint) return;
                        const reason = (releaseReasonByHold[hold.id] ?? "").trim();
                        if (reason === "") return;
                        void run(async (client) => {
                          if (!client.releaseRecoveryPointHold) {
                            setNotice("blocked");
                            return;
                          }
                          const proof = await runtime.ensureStepUpProof(STEP_UP_ACTIONS.retentionHoldRelease, {
                            persist: false,
                            reuseCached: false,
                          });
                          const result = await client.releaseRecoveryPointHold(
                            token,
                            selectedRecoveryPoint.id,
                            hold.id,
                            { reason, stepUpProof: proof },
                            new AbortController().signal,
                          );
                          if (result.status === "blocked") {
                            setNotice("blocked");
                            return;
                          }
                          setHolds((current) => current.map((item) => (item.id === hold.id ? result.value : item)));
                          setNotice("success");
                          onRefresh?.();
                        });
                      }}
                    >
                      {t("backupAssets.lifecycle.releaseHold")}
                    </Button>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </div>

      <Dialog
        open={impact !== null}
        onOpenChange={(open) => {
          if (!open) closePurgeDialog();
        }}
      >
        <DialogContent
          size="md"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            previewTriggerRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("backupAssets.lifecycle.purge")}</DialogTitle>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody className="space-y-3">
            {impact ? (
              <>
                <p>{t("backupAssets.lifecycle.selectedCount", { value: impact.selectedCount })}</p>
                <p>{t("backupAssets.lifecycle.holdCount", { value: impact.holdCount })}</p>
                <p>{t("backupAssets.lifecycle.leaseCount", { value: impact.leaseCount })}</p>
                <p>{t("backupAssets.lifecycle.wormCount", { value: impact.wormCount })}</p>
                <ul aria-label={t("backupAssets.lifecycle.selectedPointIds")} className="space-y-1 text-xs text-muted-foreground">
                  {impact.points.map((point) => (
                    <li key={point.recoveryPointId}>{point.recoveryPointId}</li>
                  ))}
                </ul>
                <label className="block space-y-1 text-sm">
                  <span>{t("backupAssets.lifecycle.typeSelectedCount")}</span>
                  <Input
                    aria-label={t("backupAssets.lifecycle.typeSelectedCount")}
                    inputMode="numeric"
                    value={confirmCount}
                    onChange={(event) => setConfirmCount(event.target.value)}
                  />
                </label>
                <label className="block space-y-1 text-sm">
                  <span>{t("backupAssets.lifecycle.purgeReason")}</span>
                  <Input
                    aria-label={t("backupAssets.lifecycle.purgeReason")}
                    value={purgeReason}
                    onChange={(event) => setPurgeReason(event.target.value)}
                  />
                </label>
              </>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={closePurgeDialog}>
              {t("backupAssets.lifecycle.cancel")}
            </Button>
            <Button
              type="button"
              disabled={busy || !impact || confirmCount !== String(impact.selectedCount) || purgeReason.trim() === ""}
              onClick={() => {
                if (!impact || confirmCount !== String(impact.selectedCount) || purgeReason.trim() === "") {
                  return;
                }
                if (!scopedRepository) {
                  setNotice("blocked");
                  return;
                }
                const repositoryId = scopedRepository.id;
                void run(async (client) => {
                  if (!client.createRepositoryPurgePlan || !client.executeRepositoryPurge) {
                    setNotice("blocked");
                    return;
                  }
                  const proof = await runtime.ensureStepUpProof(STEP_UP_ACTIONS.repositoryPurge, {
                    persist: false,
                    reuseCached: false,
                  });
                  const plan = await client.createRepositoryPurgePlan(
                    token,
                    repositoryId,
                    { expectedImpactRevision: impact.impactRevision, items: impact.points },
                    new AbortController().signal,
                  );
                  if (plan.status === "blocked") {
                    setNotice("blocked");
                    return;
                  }
                  const result = await client.executeRepositoryPurge(
                    token,
                    repositoryId,
                    {
                      planId: plan.value.id,
                      expectedRevision: plan.value.revision,
                      expectedImpactRevision: plan.value.impactRevision,
                      reason: purgeReason,
                      stepUpProof: proof,
                    },
                    new AbortController().signal,
                  );
                  if (result.status === "blocked") {
                    setNotice("blocked");
                    return;
                  }
                  if (result.value.blocked > 0) {
                    setNotice("partial");
                  } else {
                    setNotice("claimed");
                  }
                  onRefresh?.();
                  closePurgeDialog();
                });
              }}
            >
              {t("backupAssets.lifecycle.executePurge")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) closeDeleteDialog();
        }}
      >
        <DialogContent
          size="sm"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            deleteTriggerRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("backupAssets.lifecycle.deletePolicy")}</DialogTitle>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody className="space-y-3">
            {pendingDelete?.rules.age ? (
              <p>{t("backupAssets.lifecycle.keepDays", { days: pendingDelete.rules.age.keepDays })}</p>
            ) : null}
            <p>{pendingDelete?.id}</p>
            <label className="block space-y-1 text-sm">
              <span>{t("backupAssets.lifecycle.typePolicyId")}</span>
              <Input
                aria-label={t("backupAssets.lifecycle.typePolicyId")}
                value={deleteConfirm}
                onChange={(event) => setDeleteConfirm(event.target.value)}
              />
            </label>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={closeDeleteDialog}>
              {t("backupAssets.lifecycle.cancel")}
            </Button>
            <Button
              type="button"
              disabled={busy || !pendingDelete || deleteConfirm !== pendingDelete.id}
              onClick={() => {
                if (!pendingDelete || deleteConfirm !== pendingDelete.id) {
                  return;
                }
                void run(async (client) => {
                  if (!client.deleteRetentionPolicy) {
                    setNotice("blocked");
                    return;
                  }
                  const result = await client.deleteRetentionPolicy(
                    token,
                    pendingDelete.id,
                    pendingDelete.revision,
                    new AbortController().signal,
                  );
                  if (result.status === "blocked") {
                    setNotice("blocked");
                    return;
                  }
                  setPolicies((current) => current.filter((item) => !(item.status === "available" && item.value.id === pendingDelete.id)));
                  setNotice("success");
                  onRefresh?.();
                  closeDeleteDialog();
                });
              }}
            >
              {t("backupAssets.lifecycle.confirmDeletePolicy")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={holdOpen}
        onOpenChange={(open) => {
          if (!open) closeHoldDialog();
        }}
      >
        <DialogContent
          size="sm"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            holdTriggerRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("backupAssets.lifecycle.createHold")}</DialogTitle>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody className="space-y-3">
            {selectedRecoveryPoint ? (
              <p>
                {t("backupAssets.lifecycle.selectedRecoveryPoint")}
                {": "}
                <span>{selectedRecoveryPoint.id}</span>
              </p>
            ) : null}
            <label className="block space-y-1 text-sm">
              <span>{t("backupAssets.lifecycle.holdType")}</span>
              <select
                aria-label={t("backupAssets.lifecycle.holdType")}
                className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                value={holdTypeDraft}
                onChange={(event) => setHoldTypeDraft(event.target.value === "operational" ? "operational" : "legal")}
              >
                <option value="legal">{t("backupAssets.lifecycle.holdTypeLegal")}</option>
                <option value="operational">{t("backupAssets.lifecycle.holdTypeOperational")}</option>
              </select>
            </label>
            <label className="block space-y-1 text-sm">
              <span>{t("backupAssets.lifecycle.holdReason")}</span>
              <Input
                aria-label={t("backupAssets.lifecycle.holdReason")}
                value={holdReason}
                onChange={(event) => setHoldReason(event.target.value)}
              />
            </label>
            {holdTypeDraft === "operational" ? (
              <label className="block space-y-1 text-sm">
                <span>{t("backupAssets.lifecycle.holdExpiresAt")}</span>
                <Input
                  type="datetime-local"
                  aria-label={t("backupAssets.lifecycle.holdExpiresAt")}
                  value={holdExpiresAt}
                  onChange={(event) => setHoldExpiresAt(event.target.value)}
                />
              </label>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={closeHoldDialog}>
              {t("backupAssets.lifecycle.cancel")}
            </Button>
            <Button
              type="button"
              disabled={
                busy
                || holdReason.trim() === ""
                || !selectedRecoveryPoint
                || (holdTypeDraft === "operational" && holdExpiresAtISO(holdExpiresAt) === undefined)
              }
              onClick={() => {
                if (!selectedRecoveryPoint) return;
                const expiresAt = holdTypeDraft === "operational" ? holdExpiresAtISO(holdExpiresAt) : undefined;
                if (holdTypeDraft === "operational" && expiresAt === undefined) {
                  return;
                }
                void run(async (client) => {
                  if (!client.createRecoveryPointHold) {
                    setNotice("blocked");
                    return;
                  }
                  const result = await client.createRecoveryPointHold(
                    token,
                    selectedRecoveryPoint.id,
                    {
                      holdType: holdTypeDraft,
                      reason: holdReason,
                      ...(expiresAt ? { expiresAt } : {}),
                    },
                    new AbortController().signal,
                  );
                  if (result.status === "blocked") {
                    setNotice("blocked");
                    return;
                  }
                  setHolds((current) => [result.value, ...current.filter((item) => item.id !== result.value.id)]);
                  setNotice("success");
                  onRefresh?.();
                  closeHoldDialog();
                });
              }}
            >
              {t("backupAssets.lifecycle.confirmHold")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function explicitPurgeEligible(point: BackupRecoveryPoint): boolean {
  switch (point.semantics) {
    case "native_snapshot":
    case "xirang_manifest":
    case "imported_baseline":
      return point.state === "committed" || point.state === "degraded";
    case "mutable_head":
      return point.state === "observed" || point.state === "retired";
    default:
      return false;
  }
}

function resolveSelected<T extends { id: string }>(
  items: Array<CatalogProjection<T>>,
  selectedId?: string,
): T | null {
  if (selectedId) {
    const match = items.find((item) => item.status === "available" && item.value.id === selectedId);
    return match?.status === "available" ? match.value : null;
  }
  const available = items.filter((item) => item.status === "available");
  if (available.length !== 1 || available[0]?.status !== "available") {
    return null;
  }
  return available[0].value;
}

function validKeepDays(value: string): boolean {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 1 && parsed <= 36500;
}

function validKeepLatest(value: string): boolean {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 1 && parsed <= 1_000_000;
}

function validCalendarKeep(value: string): boolean {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 1 && parsed <= 10_000;
}

function validCalendarUnit(value: string): value is RetentionCalendarUnit {
  return value === "day" || value === "week" || value === "month" || value === "year";
}

function calendarUnitOptions(): RetentionCalendarUnit[] {
  return ["day", "week", "month", "year"];
}

function capitalizeUnit(unit: RetentionCalendarUnit): string {
  return unit.charAt(0).toUpperCase() + unit.slice(1);
}

function canCreatePolicy(
  keepDays: string,
  keepLatest: string,
  calendarUnit: string,
  calendarKeep: string,
): boolean {
  const rules = buildPolicyRules({ keepDays, keepLatest, calendarUnit, calendarKeep });
  return Boolean(rules.age || rules.count || (rules.calendar && rules.calendar.length > 0));
}

function canUpdatePolicy(
  keepDays: string,
  keepLatest: string,
  calendarRules: Array<{ unit: string; keep: string }>,
  fallback?: RetentionPolicyRules,
  keepDaysTouched = false,
  keepLatestTouched = false,
  calendarTouched = false,
): boolean {
  const rules = buildPolicyRules({
    keepDays,
    keepLatest,
    calendarUnit: calendarRules[0]?.unit ?? "",
    calendarKeep: calendarRules[0]?.keep ?? "",
    calendarRules,
    fallback,
    keepDaysTouched,
    keepLatestTouched,
    calendarTouched,
  });
  return Boolean(rules.age || rules.count || (rules.calendar && rules.calendar.length > 0));
}

function holdEligibleRecoveryPoint(point: BackupRecoveryPoint): boolean {
  return point.semantics !== "mutable_head" &&
    (point.semantics === "native_snapshot" || point.semantics === "xirang_manifest" || point.semantics === "imported_baseline") &&
    (point.state === "committed" || point.state === "degraded" || point.state === "purge_blocked");
}

function buildPolicyRules(input: {
  keepDays: string;
  keepLatest: string;
  calendarUnit: string;
  calendarKeep: string;
  calendarRules?: Array<{ unit: string; keep: string }>;
  fallback?: RetentionPolicyRules;
  keepDaysTouched?: boolean;
  keepLatestTouched?: boolean;
  calendarTouched?: boolean;
}): RetentionPolicyRules {
  const rules: RetentionPolicyRules = { version: 1 };
  if (validKeepDays(input.keepDays)) {
    rules.age = { keepDays: Number(input.keepDays) };
  } else if (!input.keepDaysTouched && input.fallback?.age) {
    rules.age = input.fallback.age;
  }
  if (validKeepLatest(input.keepLatest)) {
    rules.count = { keepLatest: Number(input.keepLatest) };
  } else if (!input.keepLatestTouched && input.fallback?.count) {
    rules.count = input.fallback.count;
  }
  if (input.calendarRules) {
    const calendar = input.calendarRules.flatMap((rule) => (
      validCalendarUnit(rule.unit) && validCalendarKeep(rule.keep)
        ? [{ unit: rule.unit, keep: Number(rule.keep) }]
        : []
    ));
    if (calendar.length > 0) {
      rules.calendar = calendar;
    } else if (!input.calendarTouched && input.fallback?.calendar) {
      rules.calendar = input.fallback.calendar;
    }
  } else if (validCalendarUnit(input.calendarUnit) && validCalendarKeep(input.calendarKeep)) {
    rules.calendar = [{ unit: input.calendarUnit, keep: Number(input.calendarKeep) }];
  } else if (!input.calendarTouched && input.fallback?.calendar) {
    rules.calendar = input.fallback.calendar;
  }
  return rules;
}

function holdExpiresAtISO(value: string): string | undefined {
  const trimmed = value.trim();
  if (trimmed === "") {
    return undefined;
  }
  const parsed = new Date(trimmed);
  if (Number.isNaN(parsed.getTime()) || parsed.getTime() <= Date.now()) {
    return undefined;
  }
  return parsed.toISOString();
}

function statusOf(error: unknown): number | null {
  if (error !== null && typeof error === "object" && "status" in error && typeof error.status === "number") {
    return error.status;
  }
  return null;
}
