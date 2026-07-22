import { useCallback, useEffect, useRef, useState } from "react";
import { Power, RefreshCw, Save, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { Switch } from "@/components/ui/switch";
import type { AuthContextValue, AuthRole } from "@/context/auth-context.shared";
import type {
  BackupProcessingAdminControl,
  BackupProcessingBackfillPolicy,
  BackupProcessingCoverageSummary,
  BackupProcessingUpdaterCandidate,
  BackupProcessingUpdaterStatus,
} from "@/types/domain";

export type BackupProcessingAdminClient = {
  getAdminControl(token: string, signal?: AbortSignal): Promise<BackupProcessingAdminControl>;
  getCoverage(token: string, signal?: AbortSignal): Promise<BackupProcessingCoverageSummary>;
  getUpdaterStatus(token: string, signal?: AbortSignal): Promise<BackupProcessingUpdaterStatus>;
  listOfflineCandidates(token: string, signal?: AbortSignal): Promise<BackupProcessingUpdaterCandidate[]>;
  scanOfflineCandidates(token: string, signal?: AbortSignal): Promise<void>;
  activateOfflineCandidate(
    token: string,
    candidateId: string,
    expectedActiveFingerprint: string | null,
    signal?: AbortSignal
  ): Promise<void>;
  updateBackfillPolicy(
    token: string,
    policy: BackupProcessingBackfillPolicy,
    signal?: AbortSignal
  ): Promise<BackupProcessingBackfillPolicy>;
};

export interface ProcessingCoveragePanelProps {
  token: string | null;
  role: AuthRole | null;
  ensureStepUpProof?: AuthContextValue["ensureStepUpProof"];
  loadApi?: () => Promise<BackupProcessingAdminClient>;
}

type PanelData = {
  control: BackupProcessingAdminControl;
  coverage: BackupProcessingCoverageSummary;
  updater: BackupProcessingUpdaterStatus;
  candidates: BackupProcessingUpdaterCandidate[];
};

type PanelResource =
  | { status: "loading"; data: null }
  | { status: "ready"; data: PanelData }
  | { status: "error"; data: null };

type PanelScope = {
  generation: number;
  token: string;
  controller: AbortController;
  client: BackupProcessingAdminClient | null;
};

let defaultAdminApiPromise: Promise<BackupProcessingAdminClient> | null = null;

function loadDefaultAdminApi(): Promise<BackupProcessingAdminClient> {
  defaultAdminApiPromise ??= import("@/lib/api/backup-asset-processing-api").then((module) =>
    module.createBackupAssetProcessingApi()
  );
  return defaultAdminApiPromise;
}

export function ProcessingCoveragePanel({
  token,
  role,
  loadApi = loadDefaultAdminApi,
}: ProcessingCoveragePanelProps) {
  const { t } = useTranslation();
  const [resource, setResource] = useState<PanelResource>({ status: "loading", data: null });
  const [draft, setDraft] = useState<BackupProcessingBackfillPolicy | null>(null);
  const [pending, setPending] = useState<"policy" | "scan" | "activation" | null>(null);
  const [mutationError, setMutationError] = useState(false);
  const generationRef = useRef(0);
  const scopeRef = useRef<PanelScope | null>(null);
  const loadApiRef = useRef(loadApi);
  loadApiRef.current = loadApi;

  const isCurrentScope = useCallback((scope: PanelScope) =>
    scopeRef.current === scope
      && scopeRef.current.generation === scope.generation
      && !scope.controller.signal.aborted, []);

  useEffect(() => {
    const previousScope = scopeRef.current;
    scopeRef.current = null;
    previousScope?.controller.abort();
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    setPending(null);
    setDraft(null);
    setMutationError(false);
    if (!token || role !== "admin") return undefined;
    const controller = new AbortController();
    const scope: PanelScope = { generation, token, controller, client: null };
    scopeRef.current = scope;
    setResource({ status: "loading", data: null });
    void (async () => {
      try {
        const client = await loadApiRef.current();
        if (!isCurrentScope(scope)) return;
        scope.client = client;
        const [control, coverage, updater, candidates] = await Promise.all([
          client.getAdminControl(token, controller.signal),
          client.getCoverage(token, controller.signal),
          client.getUpdaterStatus(token, controller.signal),
          client.listOfflineCandidates(token, controller.signal),
        ]);
        if (!isCurrentScope(scope)) return;
        setDraft(control.backfillPolicy);
        setResource({ status: "ready", data: { control, coverage, updater, candidates } });
      } catch {
        if (isCurrentScope(scope)) setResource({ status: "error", data: null });
      }
    })();
    return () => {
      controller.abort();
      if (scopeRef.current === scope) scopeRef.current = null;
    };
  }, [isCurrentScope, role, token]);

  if (!token || role !== "admin") return null;
  if (scopeRef.current === null || scopeRef.current.token !== token || resource.status === "loading") {
    return <LoadingState title={t("backupAssets.adminProcessing.loading")} rows={7} />;
  }
  if (resource.status === "error") {
    return <InlineAlert tone="critical">{t("backupAssets.adminProcessing.error")}</InlineAlert>;
  }

  const { control, coverage, updater, candidates } = resource.data;

  const runMutation = async (
    kind: NonNullable<typeof pending>,
    action: (
      client: BackupProcessingAdminClient,
      activeSignal: AbortSignal,
      activeScope: PanelScope
    ) => Promise<void>
  ) => {
    const scope = scopeRef.current;
    const client = scope?.client;
    if (!scope || !client || !isCurrentScope(scope) || pending !== null) return;
    setPending(kind);
    setMutationError(false);
    try {
      await action(client, scope.controller.signal, scope);
    } catch {
      if (isCurrentScope(scope)) setMutationError(true);
    } finally {
      if (isCurrentScope(scope)) setPending(null);
    }
  };

  const scan = () => runMutation("scan", async (client, activeSignal, activeScope) => {
    await client.scanOfflineCandidates(token, activeSignal);
    const next = await client.listOfflineCandidates(token, activeSignal);
    if (!isCurrentScope(activeScope)) return;
    setResource({ status: "ready", data: { ...resource.data, candidates: next } });
  });

  const activate = (candidate: BackupProcessingUpdaterCandidate) => runMutation(
    "activation",
    async (client, activeSignal, activeScope) => {
      await client.activateOfflineCandidate(
        token,
        candidate.candidateId,
        updater.active?.bundleFingerprint ?? null,
        activeSignal
      );
      const [nextUpdater, nextCandidates] = await Promise.all([
        client.getUpdaterStatus(token, activeSignal),
        client.listOfflineCandidates(token, activeSignal),
      ]);
      if (!isCurrentScope(activeScope)) return;
      setResource({
        status: "ready",
        data: { ...resource.data, updater: nextUpdater, candidates: nextCandidates },
      });
    }
  );

  const savePolicy = () => {
    if (!draft || !validPolicy(draft)) return;
    void runMutation("policy", async (client, activeSignal, activeScope) => {
      const next = await client.updateBackfillPolicy(token, draft, activeSignal);
      if (!isCurrentScope(activeScope)) return;
      setDraft(next);
      setResource({
        status: "ready",
        data: {
          ...resource.data,
          control: { ...control, backfillPolicy: next },
        },
      });
    });
  };

  return (
    <section aria-labelledby="backup-processing-admin-title" className="min-w-0 divide-y divide-border">
      <header className="flex min-h-12 flex-wrap items-center justify-between gap-2 px-3 py-2">
        <div className="min-w-0">
          <h2 id="backup-processing-admin-title" className="text-sm font-semibold">
            {t("backupAssets.adminProcessing.title")}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t("backupAssets.adminProcessing.summary", {
              completed: coverage.completed,
              eligible: coverage.eligible,
            })}
          </p>
        </div>
        <Badge tone={control.configured ? "success" : "warning"}>
          {t(control.configured
            ? "backupAssets.adminProcessing.configured"
            : "backupAssets.adminProcessing.coreOnly")}
        </Badge>
      </header>

      <div className="grid grid-cols-2 gap-px bg-border sm:grid-cols-4">
        <CoverageMetric label={t("backupAssets.adminProcessing.metrics.queued")} value={coverage.queued} />
        <CoverageMetric label={t("backupAssets.adminProcessing.metrics.partial")} value={coverage.partial} />
        <CoverageMetric label={t("backupAssets.adminProcessing.metrics.failed")} value={coverage.failed} />
        <CoverageMetric label={t("backupAssets.adminProcessing.metrics.stale")} value={coverage.stale} />
      </div>

      <div className="px-3 py-3">
        <div className="mb-2 flex items-center justify-between gap-3">
          <h3 className="text-xs font-semibold uppercase text-muted-foreground">
            {t("backupAssets.adminProcessing.coverageTitle")}
          </h3>
          <span className="text-xs tabular-nums text-muted-foreground">
            {t(`backupAssets.adminProcessing.backlog.${coverage.backlogAgeBucket}`)}
          </span>
        </div>
        <div className="overflow-x-auto border-y border-border">
          <table className="w-full min-w-[34rem] text-left text-xs">
            <thead className="bg-muted/40 text-muted-foreground">
              <tr>
                <th className="px-2 py-1.5 font-medium">{t("backupAssets.adminProcessing.capability")}</th>
                <th className="px-2 py-1.5 font-medium">{t("backupAssets.adminProcessing.profile")}</th>
                <th className="px-2 py-1.5 text-right font-medium">{t("backupAssets.adminProcessing.metrics.completed")}</th>
                <th className="px-2 py-1.5 text-right font-medium">{t("backupAssets.adminProcessing.metrics.queued")}</th>
                <th className="px-2 py-1.5 text-right font-medium">{t("backupAssets.adminProcessing.metrics.failed")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {coverage.byCapability.map((bucket) => (
                <tr key={`${bucket.capability}:${bucket.profile}`}>
                  <td className="px-2 py-1.5 font-mono">{bucket.capability}</td>
                  <td className="px-2 py-1.5 font-mono text-muted-foreground">{bucket.profile}</td>
                  <td className="px-2 py-1.5 text-right tabular-nums">{bucket.completed}</td>
                  <td className="px-2 py-1.5 text-right tabular-nums">{bucket.queued}</td>
                  <td className="px-2 py-1.5 text-right tabular-nums">{bucket.failed}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {draft ? (
        <div className="px-3 py-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h3 className="text-sm font-medium">{t("backupAssets.adminProcessing.policyTitle")}</h3>
              <p className="text-xs text-muted-foreground">{t("backupAssets.adminProcessing.policyDescription")}</p>
            </div>
            <Switch
              checked={draft.paused}
              label={t("backupAssets.adminProcessing.pause")}
              disabled={pending !== null}
              onCheckedChange={(paused) => setDraft({ ...draft, paused })}
            />
          </div>
          <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            <PolicyNumberInput id="processing-batch" label={t("backupAssets.adminProcessing.fields.batchSize")} value={draft.batchSize} min={1} max={10_000} onChange={(batchSize) => setDraft({ ...draft, batchSize })} />
            <PolicyNumberInput id="processing-jobs" label={t("backupAssets.adminProcessing.fields.jobsPerHour")} value={draft.jobsPerHour} min={1} max={100_000} onChange={(jobsPerHour) => setDraft({ ...draft, jobsPerHour })} />
            <PolicyNumberInput id="processing-bytes" label={t("backupAssets.adminProcessing.fields.bytesPerHour")} value={draft.bytesPerHour} min={65_536} max={1_099_511_627_776} onChange={(bytesPerHour) => setDraft({ ...draft, bytesPerHour })} />
            <PolicyNumberInput id="processing-provider" label={t("backupAssets.adminProcessing.fields.providerConcurrency")} value={draft.providerConcurrency} min={1} max={32} onChange={(providerConcurrency) => setDraft({ ...draft, providerConcurrency })} />
            <PolicyNumberInput id="processing-capability" label={t("backupAssets.adminProcessing.fields.capabilityConcurrency")} value={draft.capabilityConcurrency} min={1} max={32} onChange={(capabilityConcurrency) => setDraft({ ...draft, capabilityConcurrency })} />
          </div>
          <div className="mt-3 flex justify-end">
            <Button type="button" size="sm" loading={pending === "policy"} disabled={!validPolicy(draft)} onClick={savePolicy}>
              <Save className="size-4" aria-hidden />
              {t("backupAssets.adminProcessing.save")}
            </Button>
          </div>
        </div>
      ) : null}

      <div className="px-3 py-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <h3 className="text-sm font-medium">{t("backupAssets.adminProcessing.updaterTitle")}</h3>
            <div className="mt-1 flex flex-wrap gap-1.5">
              <Badge tone={updater.enabled ? "success" : "neutral"}>
                {t(updater.enabled
                  ? "backupAssets.adminProcessing.updaterEnabled"
                  : "backupAssets.adminProcessing.updaterDisabled")}
              </Badge>
              <Badge tone="neutral">
                {t(updater.onlineEnabled
                  ? "backupAssets.adminProcessing.online"
                  : "backupAssets.adminProcessing.offlineOnly")}
              </Badge>
            </div>
          </div>
          <Button type="button" variant="outline" size="sm" loading={pending === "scan"} onClick={() => void scan()}>
            <RefreshCw className="size-4" aria-hidden />
            {t("backupAssets.adminProcessing.scan")}
          </Button>
        </div>
        {updater.active ? (
          <p className="mt-2 text-xs text-muted-foreground">
            {t("backupAssets.adminProcessing.active", { version: updater.active.version })}
          </p>
        ) : null}

        <div className="mt-3 divide-y divide-border border-y border-border">
          {candidates.length === 0 ? (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t("backupAssets.adminProcessing.noCandidates")}</p>
          ) : candidates.map((candidate) => (
            <article key={candidate.candidateId} className="flex min-w-0 flex-wrap items-start justify-between gap-3 px-2 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{candidate.version}</span>
                  <Badge tone={candidate.state === "verified" ? "success" : "neutral"}>
                    {t(`backupAssets.adminProcessing.candidateState.${candidate.state}`)}
                  </Badge>
                  <code className="text-[11px] text-muted-foreground" title={candidate.bundleFingerprint}>
                    {shortFingerprint(candidate.bundleFingerprint)}
                  </code>
                </div>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  {candidate.capabilityChanges.map((change) => (
                    <span key={`${candidate.candidateId}:${change.capability}`}>{change.capability}</span>
                  ))}
                </div>
              </div>
              {candidate.state === "verified" ? (
                <Button type="button" variant="outline" size="sm" loading={pending === "activation"} onClick={() => void activate(candidate)}>
                  <Power className="size-4" aria-hidden />
                  {t("backupAssets.adminProcessing.activate", { version: candidate.version })}
                </Button>
              ) : (
                <ShieldCheck className="mt-1 size-4 text-muted-foreground" aria-hidden />
              )}
            </article>
          ))}
        </div>
      </div>

      {mutationError ? (
        <div className="px-3 py-2">
          <InlineAlert tone="critical">{t("backupAssets.adminProcessing.mutationError")}</InlineAlert>
        </div>
      ) : null}
    </section>
  );
}

function CoverageMetric({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-0 bg-card px-3 py-2">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className="mt-0.5 text-lg font-semibold tabular-nums">{value}</p>
    </div>
  );
}

function PolicyNumberInput({
  id,
  label,
  value,
  min,
  max,
  onChange,
}: {
  id: string;
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (value: number) => void;
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="mb-1 block text-xs text-muted-foreground">{label}</label>
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={min}
        max={max}
        step={1}
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
      />
    </div>
  );
}

function validPolicy(policy: BackupProcessingBackfillPolicy): boolean {
  const values: Array<[number, number, number]> = [
    [policy.batchSize, 1, 10_000],
    [policy.jobsPerHour, 1, 100_000],
    [policy.bytesPerHour, 65_536, 1_099_511_627_776],
    [policy.providerConcurrency, 1, 32],
    [policy.capabilityConcurrency, 1, 32],
  ];
  return /^[0-9a-f]{64}$/.test(policy.revision) && values.every(([value, min, max]) =>
    Number.isSafeInteger(value) && value >= min && value <= max
  );
}

function shortFingerprint(value: string): string {
  return `${value.slice(0, 8)}...${value.slice(-8)}`;
}
