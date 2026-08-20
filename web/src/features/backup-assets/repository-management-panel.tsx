import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowRight, Database } from "lucide-react";
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
import type { AuthContextValue } from "@/context/auth-context.shared";
import type {
  BackupImportCandidate,
  BackupImportDiscoveryResult,
  BackupImportCandidatePage,
  BackupRebuildResult,
  BackupRepository,
  BackupRepositoryMutationResult,
  CatalogProjection,
  ImportCandidateKind,
} from "@/types/domain";

import {
  backupAssetsImmutabilityKey,
  backupAssetsProviderKey,
  backupAssetsVersionModeKey,
  presentBackupAssetsCode,
} from "./backup-assets-presenters";

export type RepositoryLifecycleApi = {
  connectBackupRepository?(
    token: string,
    input: { taskId: number; repositoryId?: string; displayName?: string; description?: string; replaceAccess?: boolean },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRepositoryMutationResult>>;
  reconcileBackupRepository?(
    token: string,
    repositoryId: string,
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRepositoryMutationResult>>;
  disconnectBackupRepository?(
    token: string,
    repositoryId: string,
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupRepositoryMutationResult>>;
  listBackupRepositoryImportCandidates?(
    token: string,
    repositoryId: string,
    options?: { limit?: number; cursor?: string; signal?: AbortSignal },
  ): Promise<BackupImportCandidatePage>;
  scanBackupRepositoryImports?(
    token: string,
    repositoryId: string,
    options?: { limit?: number; cursor?: string; signal?: AbortSignal },
  ): Promise<CatalogProjection<BackupImportDiscoveryResult>>;
  reviewBackupRepositoryImportCandidate?(
    token: string,
    repositoryId: string,
    candidateId: string,
    input: { decision: "accepted" | "rejected"; acceptAs?: ImportCandidateKind },
    signal?: AbortSignal,
  ): Promise<CatalogProjection<BackupImportCandidate>>;
  rebuildBackupRepositoryImports?(
    token: string,
    repositoryId: string,
    options?: { limit?: number; cursor?: string; signal?: AbortSignal },
  ): Promise<CatalogProjection<BackupRebuildResult>>;
};

type BackupAssetsViewport = "desktop" | "intermediate" | "mobile";
type DialogKind = "reconnect" | "reconcile" | "disconnect" | "rebuild";
type ActiveDialog = { kind: DialogKind; repositoryId: string } | null;

export interface RepositoryManagementPanelProps {
  repositories: Array<CatalogProjection<BackupRepository>>;
  selectedRepositoryId?: string;
  viewport: BackupAssetsViewport;
  onBrowse: (repositoryId: string) => void;
  runtime?: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof">;
  onRefresh?: () => void;
  api?: RepositoryLifecycleApi;
}

export function RepositoryManagementPanel({
  repositories,
  selectedRepositoryId,
  viewport,
  onBrowse,
  runtime,
  onRefresh,
  api,
}: RepositoryManagementPanelProps) {
  const { t } = useTranslation();
  const isAdmin = runtime?.role === "admin" && Boolean(runtime.token);
  const availableCount = repositories.filter((repository) => repository.status === "available").length;
  const [dialog, setDialog] = useState<ActiveDialog>(null);
  const [taskId, setTaskId] = useState("");
  const [disconnectConfirm, setDisconnectConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<"success" | "blocked" | "conflict" | null>(null);
  const [candidatesByRepository, setCandidatesByRepository] = useState<Record<string, Array<CatalogProjection<BackupImportCandidate>>>>({});
  const [candidateCursorByRepository, setCandidateCursorByRepository] = useState<Record<string, string | null>>({});
  const [scanCursorByRepository, setScanCursorByRepository] = useState<Record<string, string | null>>({});
  const [rebuildCursorByRepository, setRebuildCursorByRepository] = useState<Record<string, string | null>>({});
  const candidatesRef = useRef(candidatesByRepository);
  candidatesRef.current = candidatesByRepository;
  const scanCursorRef = useRef(scanCursorByRepository);
  scanCursorRef.current = scanCursorByRepository;
  const rebuildCursorRef = useRef(rebuildCursorByRepository);
  rebuildCursorRef.current = rebuildCursorByRepository;
  const [acceptAsDraft, setAcceptAsDraft] = useState<Record<string, ImportCandidateKind | "">>({});
  const [rebuildSummary, setRebuildSummary] = useState<{ accepted: number; partial: number; failed: number } | null>(null);
  const rebuildSummaryRef = useRef(rebuildSummary);
  rebuildSummaryRef.current = rebuildSummary;
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  const closeDialog = () => {
    setDialog(null);
    setTaskId("");
    setDisconnectConfirm("");
    queueMicrotask(() => triggerRef.current?.focus());
  };

  const resolveApi = useCallback(async (): Promise<RepositoryLifecycleApi> => {
    if (api) return api;
    return (await import("@/lib/api/client")).apiClient;
  }, [api]);

  useEffect(() => {
    if (!isAdmin || !runtime?.token) {
      return;
    }
    const token = runtime.token;
    const controller = new AbortController();
    void (async () => {
      const client = await resolveApi();
      if (!client.listBackupRepositoryImportCandidates) {
        return;
      }
      const next: Record<string, Array<CatalogProjection<BackupImportCandidate>>> = { ...candidatesRef.current };
      const nextCursors: Record<string, string | null> = {};
      for (const projection of repositories) {
        if (projection.status !== "available") {
          continue;
        }
        try {
          const queued = await collectImportCandidateQueue(
            client,
            token,
            projection.value.id,
            controller.signal,
          );
          next[projection.value.id] = queued.items;
          nextCursors[projection.value.id] = queued.nextCursor;
        } catch {
          if (!next[projection.value.id] && candidatesRef.current[projection.value.id]) {
            next[projection.value.id] = candidatesRef.current[projection.value.id];
          }
        }
      }
      if (!controller.signal.aborted) {
        setCandidatesByRepository(next);
        setCandidateCursorByRepository((current) => ({ ...current, ...nextCursors }));
      }
    })();
    return () => controller.abort();
  }, [api, isAdmin, repositories, resolveApi, runtime?.token]);

  const runMutation = async (action: (client: RepositoryLifecycleApi) => Promise<CatalogProjection<unknown> | void>) => {
    if (!isAdmin || !runtime?.token) return;
    setBusy(true);
    setNotice(null);
    try {
      const result = await action(await resolveApi());
      if (!result || result.status === "blocked") {
        setNotice("blocked");
        return;
      }
      if (isRebuildSummary(result.value)) {
        setRebuildSummary({
          accepted: result.value.accepted,
          partial: result.value.partial,
          failed: result.value.failed,
        });
      }
      setNotice("success");
      onRefresh?.();
      closeDialog();
    } catch (error) {
      setNotice(statusOf(error) === 409 ? "conflict" : "blocked");
    } finally {
      setBusy(false);
    }
  };

  const runScanImports = (repositoryId: string, resume: boolean) => {
    void runMutation(async (client) => {
      if (!client.scanBackupRepositoryImports || !runtime?.token) return undefined;
      const startCursor = resume ? scanCursorRef.current[repositoryId] ?? undefined : undefined;
      const scanned = await collectScanImportPages(
        client,
        runtime.token,
        repositoryId,
        new AbortController().signal,
        startCursor,
      );
      if (scanned.last?.status === "available") {
        setScanCursorByRepository((current) => ({ ...current, [repositoryId]: scanned.nextCursor }));
        const queued = client.listBackupRepositoryImportCandidates
          ? await collectImportCandidateQueue(
            client,
            runtime.token,
            repositoryId,
            new AbortController().signal,
          )
          : {
            items: resume
              ? [...(candidatesRef.current[repositoryId] ?? []), ...scanned.candidates]
              : scanned.candidates,
            nextCursor: scanned.nextCursor,
          };
        setCandidatesByRepository((current) => ({ ...current, [repositoryId]: queued.items }));
        setCandidateCursorByRepository((current) => ({ ...current, [repositoryId]: queued.nextCursor }));
      }
      return scanned.last;
    });
  };

  const runRebuildImports = (repositoryId: string, resume: boolean) => {
    void runMutation(async (client) => {
      if (!client.rebuildBackupRepositoryImports || !runtime?.token) return undefined;
      const startCursor = resume ? rebuildCursorRef.current[repositoryId] ?? undefined : undefined;
      const rebuilt = await collectRebuildPages(
        client,
        runtime.token,
        repositoryId,
        new AbortController().signal,
        startCursor,
      );
      if (rebuilt.last?.status !== "available") {
        return rebuilt.last;
      }
      setRebuildCursorByRepository((current) => ({ ...current, [repositoryId]: rebuilt.nextCursor }));
      const base = resume && rebuildSummaryRef.current
        ? rebuildSummaryRef.current
        : { accepted: 0, partial: 0, failed: 0 };
      return {
        status: "available" as const,
        value: {
          ...rebuilt.last.value,
          accepted: base.accepted + rebuilt.accepted,
          partial: base.partial + rebuilt.partial,
          failed: base.failed + rebuilt.failed,
          nextCursor: rebuilt.nextCursor,
        },
      };
    });
  };

  return (
    <section
      data-testid="backup-assets-workspace"
      data-viewport={viewport}
      aria-label={t("backupAssets.repositories.title")}
      className="min-h-[36rem] overflow-hidden border-y border-border"
    >
      <div className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-3 py-2">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold">{t("backupAssets.repositories.title")}</h2>
          <p className="text-xs text-muted-foreground">
            {t("backupAssets.repositories.summary", { count: availableCount })}
          </p>
        </div>
        <Badge tone="neutral">{availableCount}</Badge>
      </div>

      {notice === "success" ? <InlineAlert tone="success">{t("backupAssets.lifecycle.success")}</InlineAlert> : null}
      {notice === "blocked" ? <InlineAlert tone="warning">{t("backupAssets.lifecycle.blocked")}</InlineAlert> : null}
      {notice === "conflict" ? <InlineAlert tone="critical">{t("backupAssets.lifecycle.conflict")}</InlineAlert> : null}
      {rebuildSummary ? (
        <p>
          {t("backupAssets.lifecycle.rebuildAccepted", { value: rebuildSummary.accepted })}
          {" · "}
          {t("backupAssets.lifecycle.rebuildPartial", { value: rebuildSummary.partial })}
          {" · "}
          {t("backupAssets.lifecycle.rebuildFailed", { value: rebuildSummary.failed })}
        </p>
      ) : null}

      {repositories.length === 0 ? (
        <div className="flex min-h-48 flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
          <Database className="size-5" aria-hidden />
          <p>{t("backupAssets.states.noRepositories")}</p>
        </div>
      ) : (
        <div className="max-h-[calc(100dvh-15rem)] overflow-y-auto">
          {repositories.map((projection, index) => {
            if (projection.status === "blocked") {
              const reason = presentBackupAssetsCode("capability", projection.reason.code);
              return (
                <div key={`blocked-repository-${index}`} className="border-b border-border p-3">
                  <InlineAlert tone="warning">{t(reason.translationKey)}</InlineAlert>
                </div>
              );
            }

            const repository = projection.value;
            const canBrowse =
              repository.accessActive && repository.capabilities.list && repository.catalog.permissions.list;
            const selectedRow = repository.id === selectedRepositoryId;
            return (
              <article
                key={repository.id}
                data-selected={selectedRow ? "true" : "false"}
                className={
                  selectedRow
                    ? "border-b border-l-2 border-b-border border-l-primary bg-accent/20 px-3 py-4"
                    : "border-b border-border px-3 py-4"
                }
              >
                <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0 space-y-2">
                    <div className="flex min-w-0 items-center gap-2">
                      <Database className="size-4 shrink-0 text-primary" aria-hidden />
                      <h3 className="min-w-0 break-words text-sm font-medium" title={repository.displayName}>
                        {repository.displayName}
                      </h3>
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      <Badge tone="neutral">{t(backupAssetsProviderKey(repository.providerKind))}</Badge>
                      <Badge tone={repository.status === "online" ? "success" : "warning"}>
                        {t(`backupAssets.codes.repositoryStatus.${repository.status}`)}
                      </Badge>
                      <Badge tone="info">{t(backupAssetsVersionModeKey(repository.versionMode))}</Badge>
                      <Badge tone="neutral">{t(backupAssetsImmutabilityKey(repository.immutabilityLevel))}</Badge>
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {canBrowse ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-label={t("backupAssets.repositories.browse", { name: repository.displayName })}
                        onClick={() => onBrowse(repository.id)}
                      >
                        {t("backupAssets.repositories.browseShort")}
                        <ArrowRight className="size-4" aria-hidden />
                      </Button>
                    ) : null}
                    {isAdmin ? (
                      <>
                        <Button type="button" variant="outline" size="sm" aria-label={`${t("backupAssets.lifecycle.reconnect")} ${repository.displayName}`} onClick={() => { triggerRef.current = document.activeElement as HTMLButtonElement; setDialog({ kind: "reconnect", repositoryId: repository.id }); }}>
                          {t("backupAssets.lifecycle.reconnect")}
                        </Button>
                        <Button type="button" variant="outline" size="sm" aria-label={`${t("backupAssets.lifecycle.reconcile")} ${repository.displayName}`} onClick={() => { triggerRef.current = document.activeElement as HTMLButtonElement; setDialog({ kind: "reconcile", repositoryId: repository.id }); }}>
                          {t("backupAssets.lifecycle.reconcile")}
                        </Button>
                        <Button type="button" variant="outline" size="sm" aria-label={`${t("backupAssets.lifecycle.disconnect")} ${repository.displayName}`} onClick={() => { triggerRef.current = document.activeElement as HTMLButtonElement; setDialog({ kind: "disconnect", repositoryId: repository.id }); }}>
                          {t("backupAssets.lifecycle.disconnect")}
                        </Button>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={busy}
                          aria-label={`${t("backupAssets.lifecycle.scanImports")} ${repository.displayName}`}
                          onClick={() => runScanImports(repository.id, false)}
                        >
                          {t("backupAssets.lifecycle.scanImports")}
                        </Button>
                        {scanCursorByRepository[repository.id] ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={busy}
                            aria-label={`${t("backupAssets.lifecycle.continueScan")} ${repository.displayName}`}
                            onClick={() => runScanImports(repository.id, true)}
                          >
                            {t("backupAssets.lifecycle.continueScan")}
                          </Button>
                        ) : null}
                        <Button type="button" variant="outline" size="sm" aria-label={`${t("backupAssets.lifecycle.rebuild")} ${repository.displayName}`} onClick={() => { triggerRef.current = document.activeElement as HTMLButtonElement; setDialog({ kind: "rebuild", repositoryId: repository.id }); }}>
                          {t("backupAssets.lifecycle.rebuild")}
                        </Button>
                        {rebuildCursorByRepository[repository.id] ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={busy}
                            aria-label={`${t("backupAssets.lifecycle.continueRebuild")} ${repository.displayName}`}
                            onClick={() => runRebuildImports(repository.id, true)}
                          >
                            {t("backupAssets.lifecycle.continueRebuild")}
                          </Button>
                        ) : null}
                      </>
                    ) : null}
                  </div>
                </div>

                <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(12rem,0.8fr)_minmax(18rem,1.1fr)_minmax(12rem,0.8fr)]">
                  <RepositoryFactGroup
                    title={t("backupAssets.repositories.catalogFacts")}
                    facts={[
                      [t("backupAssets.context.catalog"), t(`backupAssets.codes.coverage.${repository.catalog.coverage}`)],
                      [t("backupAssets.repositories.recoveryPointCount"), String(repository.catalog.recoveryPointCount)],
                      [t("backupAssets.repositories.completeCatalogCount"), String(repository.catalog.completeCatalogCount)],
                      [
                        t("backupAssets.context.content"),
                        t(repository.catalog.contentAvailability.available
                          ? "backupAssets.repositories.available"
                          : "backupAssets.repositories.unavailable"),
                      ],
                    ]}
                  />
                  <RepositoryFactGroup
                    title={t("backupAssets.repositories.capabilities")}
                    facts={repositoryCapabilityFacts(repository, t)}
                  />
                  <RepositoryFactGroup
                    title={t("backupAssets.repositories.permissions")}
                    facts={[
                      [t("backupAssets.repositories.permissionList"), availabilityText(repository.catalog.permissions.list, t)],
                      [t("backupAssets.repositories.permissionPreview"), availabilityText(repository.catalog.permissions.preview, t)],
                      [t("backupAssets.repositories.permissionDownload"), availabilityText(repository.catalog.permissions.download, t)],
                    ]}
                  />
                </div>

                {isAdmin && (candidatesByRepository[repository.id] ?? []).length > 0 ? (
                  <ul className="mt-4 space-y-2" aria-label={t("backupAssets.lifecycle.scanImports")}>
                    {(candidatesByRepository[repository.id] ?? []).map((candidate, candidateIndex) => {
                      if (candidate.status !== "available") {
                        return <li key={`blocked-candidate-${candidateIndex}`}>{t("backupAssets.lifecycle.blocked")}</li>;
                      }
                      return (
                        <li key={candidate.value.id} className="flex flex-wrap items-center gap-2 text-sm">
                          <span>{candidateKindLabel(candidate.value.kind, t)}</span>
                          <Badge tone="neutral">{candidateStateLabel(candidate.value.state, t)}</Badge>
                          {candidate.value.quarantined ? (
                            <Badge
                              tone="warning"
                              aria-label={`${t("backupAssets.lifecycle.candidateQuarantined")} ${candidate.value.id}`}
                            >
                              {t("backupAssets.lifecycle.candidateQuarantined")}
                            </Badge>
                          ) : null}
                          {candidate.value.state === "pending" && !candidate.value.quarantined ? (
                            <>
                              {candidate.value.kind === "mutable_head" ? (
                                <label className="space-y-1 text-sm">
                                  <span>{t("backupAssets.lifecycle.acceptAsDisposition")}</span>
                                  <select
                                    aria-label={`${t("backupAssets.lifecycle.acceptAsDisposition")} ${candidate.value.id}`}
                                    className="flex h-10 rounded-md border border-input bg-background px-3 py-2 text-sm"
                                    value={acceptAsDraft[candidate.value.id] ?? ""}
                                    onChange={(event) => {
                                      const next = event.target.value;
                                      setAcceptAsDraft((current) => ({
                                        ...current,
                                        [candidate.value.id]: next === "imported_baseline" || next === "mutable_head"
                                          ? next
                                          : "",
                                      }));
                                    }}
                                  >
                                    <option value="" />
                                    <option value="imported_baseline">{t("backupAssets.lifecycle.importedBaseline")}</option>
                                    <option value="mutable_head">{t("backupAssets.lifecycle.mutableHead")}</option>
                                  </select>
                                </label>
                              ) : null}
                            <Button
                              type="button"
                              size="sm"
                              disabled={busy || (candidate.value.kind === "mutable_head" && !validMutableDisposition(acceptAsDraft[candidate.value.id]))}
                              aria-label={`${t("backupAssets.lifecycle.acceptCandidate")} ${candidate.value.id}`}
                              onClick={() => {
                                const acceptAs = candidate.value.kind === "mutable_head"
                                  ? acceptAsDraft[candidate.value.id]
                                  : candidate.value.kind;
                                if (candidate.value.kind === "mutable_head" && !validMutableDisposition(acceptAs)) {
                                  return;
                                }
                                void runMutation(async (client) => {
                                  const result = await client.reviewBackupRepositoryImportCandidate?.(
                                    runtime.token!,
                                    repository.id,
                                    candidate.value.id,
                                    {
                                      decision: "accepted",
                                      acceptAs: acceptAs === "imported_baseline" || acceptAs === "mutable_head" || acceptAs === "native_snapshot" || acceptAs === "xirang_manifest"
                                        ? acceptAs
                                        : candidate.value.kind,
                                    },
                                    new AbortController().signal,
                                  );
                                  if (result?.status === "available") {
                                    setCandidatesByRepository((current) => ({
                                      ...current,
                                      [repository.id]: (current[repository.id] ?? []).map((item) => (
                                        item.status === "available" && item.value.id === candidate.value.id
                                          ? { status: "available" as const, value: result.value }
                                          : item
                                      )),
                                    }));
                                  }
                                  return result;
                                });
                              }}
                            >
                              {t("backupAssets.lifecycle.acceptCandidate")}
                            </Button>
                            </>
                          ) : null}
                          {candidate.value.state === "pending" ? (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              disabled={busy}
                              aria-label={`${t("backupAssets.lifecycle.rejectCandidate")} ${candidate.value.id}`}
                              onClick={() => {
                                void runMutation(async (client) => {
                                  const result = await client.reviewBackupRepositoryImportCandidate?.(
                                    runtime.token!,
                                    repository.id,
                                    candidate.value.id,
                                    { decision: "rejected" },
                                    new AbortController().signal,
                                  );
                                  if (result?.status === "available") {
                                    setCandidatesByRepository((current) => ({
                                      ...current,
                                      [repository.id]: (current[repository.id] ?? []).map((item) => (
                                        item.status === "available" && item.value.id === candidate.value.id
                                          ? { status: "available" as const, value: result.value }
                                          : item
                                      )),
                                    }));
                                  }
                                  return result;
                                });
                              }}
                            >
                              {t("backupAssets.lifecycle.rejectCandidate")}
                            </Button>
                          ) : null}
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
                {isAdmin && candidateCursorByRepository[repository.id] ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="mt-2"
                    disabled={busy}
                    aria-label={`${t("backupAssets.actions.loadMore")} ${repository.displayName}`}
                    onClick={() => {
                      const cursor = candidateCursorByRepository[repository.id];
                      if (!cursor) {
                        return;
                      }
                      void runMutation(async (client) => {
                        const queued = await collectImportCandidateQueue(
                          client,
                          runtime.token!,
                          repository.id,
                          new AbortController().signal,
                          cursor,
                        );
                        setCandidatesByRepository((current) => ({
                          ...current,
                          [repository.id]: [...(current[repository.id] ?? []), ...queued.items],
                        }));
                        setCandidateCursorByRepository((current) => ({ ...current, [repository.id]: queued.nextCursor }));
                        return { status: "available", value: queued };
                      });
                    }}
                  >
                    {t("backupAssets.actions.loadMore")}
                  </Button>
                ) : null}
              </article>
            );
          })}
        </div>
      )}

      <Dialog open={dialog !== null} onOpenChange={(open) => { if (!open) closeDialog(); }}>
        <DialogContent
          size="sm"
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            triggerRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {dialog?.kind === "reconnect" ? t("backupAssets.lifecycle.reconnect") : null}
              {dialog?.kind === "reconcile" ? t("backupAssets.lifecycle.reconcile") : null}
              {dialog?.kind === "disconnect" ? t("backupAssets.lifecycle.disconnect") : null}
              {dialog?.kind === "rebuild" ? t("backupAssets.lifecycle.rebuild") : null}
            </DialogTitle>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody className="space-y-3">
            {dialog?.kind === "reconnect" ? (
              <label className="block space-y-1 text-sm">
                <span>{t("backupAssets.lifecycle.taskId")}</span>
                <Input
                  aria-label={t("backupAssets.lifecycle.taskId")}
                  inputMode="numeric"
                  value={taskId}
                  onChange={(event) => setTaskId(event.target.value)}
                />
              </label>
            ) : null}
            {dialog?.kind === "disconnect" ? (
              <label className="block space-y-1 text-sm">
                <span>{t("backupAssets.lifecycle.typeRepositoryName")}</span>
                <p>{dialogRepositoryName(repositories, dialog.repositoryId)}</p>
                <Input
                  aria-label={t("backupAssets.lifecycle.typeRepositoryName")}
                  value={disconnectConfirm}
                  onChange={(event) => setDisconnectConfirm(event.target.value)}
                />
              </label>
            ) : null}
            {dialog && dialog.kind !== "reconnect" && dialog.kind !== "disconnect" ? (
              <p>{dialogRepositoryName(repositories, dialog.repositoryId)}</p>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={closeDialog}>{t("backupAssets.lifecycle.cancel")}</Button>
            {dialog?.kind === "reconnect" ? (
              <Button
                type="button"
                disabled={busy || !/^[1-9][0-9]*$/.test(taskId) || !dialog.repositoryId}
                onClick={() => {
                  void runMutation(async (client) => {
                    if (!client.connectBackupRepository) return undefined;
                    return client.connectBackupRepository(
                      runtime!.token!,
                      { taskId: Number(taskId), repositoryId: dialog.repositoryId },
                      new AbortController().signal,
                    );
                  });
                }}
              >
                {t("backupAssets.lifecycle.confirmReconnect")}
              </Button>
            ) : null}
            {dialog?.kind === "reconcile" ? (
              <Button
                type="button"
                disabled={busy || !dialog.repositoryId}
                onClick={() => {
                  void runMutation(async (client) => {
                    if (!client.reconcileBackupRepository) return undefined;
                    return client.reconcileBackupRepository(
                      runtime!.token!,
                      dialog.repositoryId,
                      new AbortController().signal,
                    );
                  });
                }}
              >
                {t("backupAssets.lifecycle.confirmReconcile")}
              </Button>
            ) : null}
            {dialog?.kind === "disconnect" ? (
              <Button
                type="button"
                disabled={busy || !dialog.repositoryId || disconnectConfirm !== dialogRepositoryName(repositories, dialog.repositoryId)}
                onClick={() => {
                  void runMutation(async (client) => {
                    if (!client.disconnectBackupRepository) return undefined;
                    return client.disconnectBackupRepository(
                      runtime!.token!,
                      dialog.repositoryId,
                      new AbortController().signal,
                    );
                  });
                }}
              >
                {t("backupAssets.lifecycle.confirmDisconnect")}
              </Button>
            ) : null}
            {dialog?.kind === "rebuild" ? (
              <Button
                type="button"
                disabled={busy || !dialog.repositoryId}
                onClick={() => runRebuildImports(dialog.repositoryId, false)}
              >
                {t("backupAssets.lifecycle.confirmRebuild")}
              </Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function validMutableDisposition(value: ImportCandidateKind | "" | undefined): value is "imported_baseline" | "mutable_head" {
  return value === "imported_baseline" || value === "mutable_head";
}

const maxImportCandidatePagesPerTick = 8;

async function collectScanImportPages(
  client: RepositoryLifecycleApi,
  token: string,
  repositoryId: string,
  signal: AbortSignal,
  startCursor?: string,
): Promise<{
  last: CatalogProjection<BackupImportDiscoveryResult> | undefined;
  candidates: Array<CatalogProjection<BackupImportCandidate>>;
  nextCursor: string | null;
}> {
  if (!client.scanBackupRepositoryImports) {
    return { last: undefined, candidates: [], nextCursor: startCursor ?? null };
  }
  const candidates: Array<CatalogProjection<BackupImportCandidate>> = [];
  let cursor = startCursor;
  let pages = 0;
  let last: CatalogProjection<BackupImportDiscoveryResult> | undefined;
  let nextCursor: string | null = startCursor ?? null;
  while (!signal.aborted && pages < maxImportCandidatePagesPerTick) {
    pages += 1;
    last = await client.scanBackupRepositoryImports(token, repositoryId, { cursor, signal });
    if (last.status !== "available") {
      return { last, candidates, nextCursor };
    }
    candidates.push(...last.value.candidates);
    if (!last.value.nextCursor) {
      return { last, candidates, nextCursor: null };
    }
    cursor = last.value.nextCursor;
    nextCursor = last.value.nextCursor;
  }
  return { last, candidates, nextCursor };
}

async function collectRebuildPages(
  client: RepositoryLifecycleApi,
  token: string,
  repositoryId: string,
  signal: AbortSignal,
  startCursor?: string,
): Promise<{
  last: CatalogProjection<BackupRebuildResult> | undefined;
  accepted: number;
  partial: number;
  failed: number;
  nextCursor: string | null;
}> {
  if (!client.rebuildBackupRepositoryImports) {
    return { last: undefined, accepted: 0, partial: 0, failed: 0, nextCursor: startCursor ?? null };
  }
  let cursor = startCursor;
  let pages = 0;
  let last: CatalogProjection<BackupRebuildResult> | undefined;
  let accepted = 0;
  let partial = 0;
  let failed = 0;
  let nextCursor: string | null = startCursor ?? null;
  while (!signal.aborted && pages < maxImportCandidatePagesPerTick) {
    pages += 1;
    last = await client.rebuildBackupRepositoryImports(token, repositoryId, { cursor, signal });
    if (last.status !== "available") {
      return { last, accepted, partial, failed, nextCursor };
    }
    accepted += last.value.accepted;
    partial += last.value.partial;
    failed += last.value.failed;
    if (!last.value.nextCursor) {
      return { last, accepted, partial, failed, nextCursor: null };
    }
    cursor = last.value.nextCursor;
    nextCursor = last.value.nextCursor;
  }
  return { last, accepted, partial, failed, nextCursor };
}

async function collectImportCandidateQueue(
  client: RepositoryLifecycleApi,
  token: string,
  repositoryId: string,
  signal: AbortSignal,
  startCursor?: string,
): Promise<{ items: Array<CatalogProjection<BackupImportCandidate>>; nextCursor: string | null }> {
  if (!client.listBackupRepositoryImportCandidates) {
    return { items: [], nextCursor: null };
  }
  const collected: Array<CatalogProjection<BackupImportCandidate>> = [];
  let cursor: string | undefined = startCursor;
  let pages = 0;
  let nextCursor: string | null = null;
  while (!signal.aborted && pages < maxImportCandidatePagesPerTick) {
    pages += 1;
    const page = await client.listBackupRepositoryImportCandidates(token, repositoryId, { cursor, signal });
    collected.push(...page.items.filter(isQueuedImportCandidate));
    if (!page.nextCursor) {
      nextCursor = null;
      break;
    }
    cursor = page.nextCursor;
    nextCursor = page.nextCursor;
  }
  return { items: collected, nextCursor };
}

function isQueuedImportCandidate(item: CatalogProjection<BackupImportCandidate>): boolean {
  return item.status !== "available" || item.value.state === "pending" || item.value.quarantined;
}

function candidateStateLabel(state: BackupImportCandidate["state"], t: (key: string) => string): string {
  switch (state) {
    case "accepted":
      return t("backupAssets.lifecycle.candidateAccepted");
    case "rejected":
      return t("backupAssets.lifecycle.candidateRejected");
    case "pending":
      return t("backupAssets.lifecycle.candidatePending");
  }
}

function isRebuildSummary(value: unknown): value is BackupRebuildResult {
  return value !== null && typeof value === "object" && "accepted" in value && "partial" in value && "failed" in value;
}

function candidateKindLabel(kind: ImportCandidateKind, t: (key: string) => string): string {
  switch (kind) {
    case "imported_baseline":
      return t("backupAssets.lifecycle.importedBaseline");
    case "native_snapshot":
      return t("backupAssets.lifecycle.nativeSnapshot");
    case "xirang_manifest":
      return t("backupAssets.lifecycle.xirangManifest");
    case "mutable_head":
      return t("backupAssets.lifecycle.mutableHead");
  }
}

function dialogRepositoryName(
  repositories: Array<CatalogProjection<BackupRepository>>,
  repositoryId: string | undefined,
): string {
  const match = repositories.find((item) => item.status === "available" && item.value.id === repositoryId);
  return match?.status === "available" ? match.value.displayName : "";
}

function statusOf(error: unknown): number | null {
  if (error !== null && typeof error === "object" && "status" in error && typeof error.status === "number") {
    return error.status;
  }
  return null;
}

function RepositoryFactGroup({ title, facts }: { title: string; facts: Array<readonly [string, string]> }) {
  return (
    <div className="min-w-0">
      <h4 className="mb-1.5 text-xs font-medium text-muted-foreground">{title}</h4>
      <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-xs">
        {facts.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="min-w-0 truncate" title={label}>{label}</dt>
            <dd className="text-right text-muted-foreground">{value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function repositoryCapabilityFacts(
  repository: BackupRepository,
  t: (key: string) => string,
): Array<readonly [string, string]> {
  return [
    [t("backupAssets.repositories.capabilityList"), availabilityText(repository.capabilities.list, t)],
    [t("backupAssets.repositories.capabilitySearch"), availabilityText(repository.capabilities.searchPath, t)],
    [t("backupAssets.repositories.capabilitySequential"), availabilityText(repository.capabilities.openSequential, t)],
    [t("backupAssets.repositories.capabilityRange"), availabilityText(repository.capabilities.openRange, t)],
    [t("backupAssets.repositories.capabilityDownload"), availabilityText(repository.capabilities.download, t)],
    [t("backupAssets.repositories.capabilityRestore"), availabilityText(repository.capabilities.restore, t)],
    [t("backupAssets.repositories.capabilityDiff"), availabilityText(repository.capabilities.diff, t)],
    [t("backupAssets.repositories.capabilityHistory"), availabilityText(repository.capabilities.nativeHistory, t)],
  ];
}

function availabilityText(available: boolean, t: (key: string) => string): string {
  return t(available ? "backupAssets.repositories.available" : "backupAssets.repositories.unavailable");
}
