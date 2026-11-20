import { useEffect, useId, useMemo, useRef, useState } from "react";
import { ChevronRight, GitCompareArrows } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type {
  BackupFileSourceComparisonItem,
  BackupFileSourceComparisonSide,
  BackupFileSourceVersion,
  CatalogContentAvailability,
} from "@/types/domain";

import {
  backupFileSourceComparisonItemKey,
  useBackupFileSourceComparison,
  type BackupFileSourceComparisonStatus,
} from "./use-backup-file-source-comparison";
import type { BackupFileSourceVersionsFailure } from "./use-backup-file-sources";

// Busy buttons stay focusable (aria-disabled) so keyboard focus is not dropped mid-request.
const PENDING_BUTTON = "aria-disabled:cursor-not-allowed aria-disabled:opacity-60";

export interface BackupFileSourceCompareProps {
  token: string | null;
  role: string | null;
  /** Resolved backup set; comparison is unavailable until one exists. */
  backupSetId: string | null;
  versions: readonly BackupFileSourceVersion[];
  selectedRecoveryPointId?: string;
  hasMoreVersions: boolean;
  loadingMoreVersions: boolean;
  /** The version list for the selected backup set is still being fetched. */
  versionsLoading: boolean;
  onLoadMoreVersions: () => void | Promise<void>;
  /** Reloads the version list so comparison eligibility reflects the server after an invalidated pair. */
  onRefreshVersions: () => void;
  /** Why the candidate list is incomplete; shown inside the dialog, which hides the Files controls. */
  versionsFailure: BackupFileSourceVersionsFailure | null;
  /** Retries only the failed candidate request. */
  onRetryVersions: () => void;
}

/**
 * Entry and dialog for comparing two recovery points of one backup set. The dialog is bound
 * to the identity and backup set it was opened for: any change closes it and drops its state.
 */
export function BackupFileSourceCompare({ token, role, backupSetId, ...bodyProps }: BackupFileSourceCompareProps) {
  const { t } = useTranslation();
  const scopeKey = backupSetId === null || !token ? null : `${token}:${role ?? ""}:${backupSetId}`;
  const [openScope, setOpenScope] = useState<string | null>(null);
  const open = scopeKey !== null && openScope === scopeKey;
  const baseSelectRef = useRef<HTMLSelectElement>(null);

  return (
    <Dialog open={open} onOpenChange={(next) => setOpenScope(next ? scopeKey : null)}>
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="touch-target min-h-11 w-full lg:min-h-8"
          disabled={scopeKey === null}
        >
          <GitCompareArrows className="size-4" aria-hidden />
          {t("backupAssets.sources.compare.open")}
        </Button>
      </DialogTrigger>
      <DialogContent
        size="lg"
        className="flex flex-col overflow-hidden sm:max-w-[760px]"
        data-testid="backup-file-compare-dialog"
        onOpenAutoFocus={(event) => {
          const baseSelect = baseSelectRef.current;
          if (baseSelect === null) return;
          event.preventDefault();
          baseSelect.focus();
        }}
      >
        <DialogHeader className="shrink-0 border-b border-border/60">
          <DialogTitle>{t("backupAssets.sources.compare.title")}</DialogTitle>
          <DialogDescription>{t("backupAssets.sources.compare.description")}</DialogDescription>
        </DialogHeader>
        <div className="thin-scrollbar min-h-0 overflow-y-auto overscroll-contain">
          {scopeKey !== null && backupSetId !== null ? (
            <CompareBody
              key={scopeKey}
              token={token}
              backupSetId={backupSetId}
              active={open}
              baseSelectRef={baseSelectRef}
              {...bodyProps}
            />
          ) : null}
        </div>
        <DialogFooter className="shrink-0">
          <DialogClose asChild>
            <Button type="button" variant="secondary" className="touch-target min-h-11 lg:min-h-9">
              {t("common.close")}
            </Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type FocusTarget = "base" | "run" | "loadMoreVersions" | "retryVersions" | "loadMore" | "retryContinuation" | "reselect" | "summary";

/**
 * True when nothing the user chose holds focus: the focused control was removed and focus fell
 * to the document or back to the dialog container. A user who moved elsewhere keeps their place.
 */
function focusReleased(anchor: HTMLElement | null): boolean {
  const active = document.activeElement;
  return active === null || active === document.body || (anchor !== null && active === anchor.closest("[role='dialog']"));
}

interface CompareBodyProps extends Omit<BackupFileSourceCompareProps, "token" | "role" | "backupSetId"> {
  token: string | null;
  backupSetId: string;
  active: boolean;
  baseSelectRef: React.RefObject<HTMLSelectElement>;
}

function CompareBody({
  token, backupSetId, active, baseSelectRef, versions, selectedRecoveryPointId,
  hasMoreVersions, loadingMoreVersions, versionsLoading, onLoadMoreVersions, onRefreshVersions,
  versionsFailure, onRetryVersions,
}: CompareBodyProps) {
  const { t, i18n } = useTranslation();
  const comparison = useBackupFileSourceComparison({ token, backupSetId });
  const { cancel } = comparison;
  const [baseId, setBaseId] = useState(() => (
    versions.some((item) => item.recoveryPointId === selectedRecoveryPointId && item.comparisonState === "eligible")
      ? selectedRecoveryPointId ?? ""
      : ""
  ));
  const [compareId, setCompareId] = useState("");
  // Focus moves are DOM side effects, so they live in refs rather than render state.
  const requestedFocus = useRef<FocusTarget | null>(null);
  const pendingContinuationFocus = useRef(false);
  const pendingVersionsFocus = useRef(false);
  const runRef = useRef<HTMLButtonElement>(null);
  const loadMoreVersionsRef = useRef<HTMLButtonElement>(null);
  const retryVersionsRef = useRef<HTMLButtonElement>(null);
  const loadMoreRef = useRef<HTMLButtonElement>(null);
  const retryContinuationRef = useRef<HTMLButtonElement>(null);
  const reselectRef = useRef<HTMLButtonElement>(null);
  const summaryRef = useRef<HTMLParagraphElement>(null);

  // Closing aborts at once; the body itself unmounts once the dialog finishes animating out.
  useEffect(() => {
    if (!active) cancel();
  }, [active, cancel]);

  const formatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "medium" }),
    [i18n.language],
  );
  // Closed native selects clip long options; keep seconds so nearby points stay distinct.
  const optionFormatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: "short", timeStyle: "medium" }),
    [i18n.language],
  );
  const offlineHintId = useId();
  const versionById = useMemo(() => new Map(versions.map((item) => [item.recoveryPointId, item])), [versions]);
  const pointLabel = (version: BackupFileSourceVersion | undefined) => (
    version ? formatter.format(new Date(version.capturedAt ?? version.committedAt ?? version.createdAt)) : ""
  );
  const baseVersion = versionById.get(baseId);
  const compareVersion = versionById.get(compareId);
  const samePoint = baseVersion !== undefined && baseVersion.recoveryPointId === compareVersion?.recoveryPointId;
  const pairReady = baseVersion?.comparisonState === "eligible" && compareVersion?.comparisonState === "eligible" && !samePoint;
  const running = comparison.status === "loading";
  const loadingMore = comparison.status === "loading_more";
  const result = comparison.result;

  const focusTargets = {
    base: baseSelectRef,
    run: runRef,
    loadMoreVersions: loadMoreVersionsRef,
    retryVersions: retryVersionsRef,
    loadMore: loadMoreRef,
    retryContinuation: retryContinuationRef,
    reselect: reselectRef,
    summary: summaryRef,
  } as const;

  // Runs after every commit: when a finished request removed the control that started it while
  // that control still held focus, hand focus to its successor; then apply a requested move once
  // its target is rendered.
  useEffect(() => {
    if (pendingContinuationFocus.current && !loadingMore) {
      pendingContinuationFocus.current = false;
      if (focusReleased(summaryRef.current)) {
        if (comparison.status === "continuation_failed") requestedFocus.current = "retryContinuation";
        else if (comparison.status === "invalidated") requestedFocus.current = "reselect";
        else if (comparison.status === "ready" && result?.nextCursor === null) requestedFocus.current = "summary";
      }
    }
    if (pendingVersionsFocus.current && !loadingMoreVersions) {
      pendingVersionsFocus.current = false;
      if (focusReleased(summaryRef.current)) requestedFocus.current = versionsFailure === null ? "base" : "retryVersions";
    }
    const target = requestedFocus.current;
    const element = target === null ? null : focusTargets[target].current;
    if (element === null) return;
    requestedFocus.current = null;
    element.focus();
  });

  const selectPoint = (side: "base" | "compare", value: string) => {
    if (side === "base") setBaseId(value);
    else setCompareId(value);
    comparison.reset();
  };
  const runComparison = () => {
    if (!pairReady || running) return;
    comparison.run({ baseRecoveryPointId: baseId, compareRecoveryPointId: compareId });
  };
  const loadMoreChanges = (focus: FocusTarget | null) => {
    if (loadingMore) return;
    pendingContinuationFocus.current = true;
    requestedFocus.current = focus;
    comparison.loadMore();
  };
  const reselect = () => {
    pendingContinuationFocus.current = false;
    requestedFocus.current = "base";
    comparison.reset();
    onRefreshVersions();
  };
  const loadMoreVersions = () => {
    if (loadingMoreVersions) return;
    pendingVersionsFocus.current = true;
    void onLoadMoreVersions();
  };
  const retryVersions = () => {
    if (versionsFailure === "page") {
      // The page retry keeps the list; its pagination control shows progress and stays focused.
      pendingVersionsFocus.current = true;
      requestedFocus.current = "loadMoreVersions";
    } else {
      requestedFocus.current = "base";
    }
    onRetryVersions();
  };

  const failure = comparison.failure;
  const showReselect = failure?.invalidating === true;

  return (
    <DialogBody className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        {(["base", "compare"] as const).map((side) => {
          const value = side === "base" ? baseId : compareId;
          const label = t(side === "base" ? "backupAssets.sources.compare.base" : "backupAssets.sources.compare.target");
          // Before a run the closed select may clip the option text, so offline gets its own line.
          const offline = !result && versionById.get(value)?.contentAvailability.available === false;
          const hintId = `${offlineHintId}-${side}`;
          return (
            <div key={side} className="min-w-0 space-y-1.5">
              <span className="block text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
              <Select
                ref={side === "base" ? baseSelectRef : undefined}
                aria-label={label}
                aria-describedby={offline ? hintId : undefined}
                className="touch-target h-11 lg:h-9"
                value={versionById.has(value) ? value : ""}
                onChange={(event) => selectPoint(side, event.target.value)}
              >
                <option value="">{t("backupAssets.sources.compare.selectPoint")}</option>
                {/* Only selectable points appear enabled, so their state adds nothing; offline stays visible. */}
                {versions.map((version) => (
                  <option
                    key={version.recoveryPointId}
                    value={version.recoveryPointId}
                    disabled={version.comparisonState !== "eligible"}
                  >
                    {[
                      optionFormatter.format(new Date(version.capturedAt ?? version.committedAt ?? version.createdAt)),
                      ...(version.comparisonState === "eligible" ? [] : [t(`backupAssets.sources.compare.states.${version.comparisonState}`)]),
                      ...(version.contentAvailability.available ? [] : [t("backupAssets.sources.compare.contentOffline")]),
                    ].join(" · ")}
                  </option>
                ))}
              </Select>
              {offline ? (
                <p id={hintId} className="break-words text-xs font-medium text-warning">
                  {t("backupAssets.sources.compare.contentOffline")}
                </p>
              ) : null}
            </div>
          );
        })}
      </div>

      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        {hasMoreVersions ? (
          <Button
            ref={loadMoreVersionsRef}
            type="button"
            variant="ghost"
            size="sm"
            className={cn("touch-target min-h-11 lg:min-h-8", PENDING_BUTTON)}
            aria-disabled={loadingMoreVersions}
            onClick={loadMoreVersions}
          >
            {loadingMoreVersions ? t("backupAssets.sources.loadingMore") : t("backupAssets.sources.loadMoreVersions")}
          </Button>
        ) : <span />}
        <Button
          ref={runRef}
          type="button"
          className={cn("touch-target min-h-11 lg:min-h-9", PENDING_BUTTON)}
          aria-disabled={!pairReady || running}
          onClick={runComparison}
        >
          <GitCompareArrows className="size-4" aria-hidden />
          {running ? t("backupAssets.sources.compare.running") : t("backupAssets.sources.compare.run")}
        </Button>
      </div>
      {versionsLoading ? (
        <p role="status" className="text-xs text-muted-foreground">{t("backupAssets.sources.loading")}</p>
      ) : versionsFailure !== null ? (
        <InlineAlert tone="warning">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span>{t(`backupAssets.sources.compare.candidates.${versionsFailure}`)}</span>
            {versionsFailure === "permission_denied" ? null : (
              <Button
                ref={retryVersionsRef}
                type="button"
                variant="outline"
                size="sm"
                className="touch-target min-h-11 lg:min-h-8"
                onClick={retryVersions}
              >
                {t("backupAssets.sources.compare.candidates.retry")}
              </Button>
            )}
          </div>
        </InlineAlert>
      ) : versions.length === 0 && !hasMoreVersions ? (
        <p className="text-xs text-muted-foreground">{t("backupAssets.sources.compare.noVersions")}</p>
      ) : null}
      {samePoint ? <p className="text-xs text-warning">{t("backupAssets.sources.compare.samePoint")}</p> : null}

      <section aria-label={t("backupAssets.sources.compare.results")} className="space-y-3 border-t border-border pt-3">
        {result ? (
          <div className="grid gap-2 sm:grid-cols-2">
            <SideSummary
              label={t("backupAssets.sources.compare.sides.base")}
              point={pointLabel(versionById.get(result.baseRecoveryPointId))}
              availability={result.baseContentAvailability}
            />
            <SideSummary
              label={t("backupAssets.sources.compare.sides.compare")}
              point={pointLabel(versionById.get(result.compareRecoveryPointId))}
              availability={result.compareContentAvailability}
            />
          </div>
        ) : null}
        <p ref={summaryRef} tabIndex={-1} role="status" className="text-xs text-muted-foreground focus:outline-none">
          <ComparisonSummary status={comparison.status} itemCount={result?.items.length ?? 0} hasMore={Boolean(result?.nextCursor)} />
        </p>
        {result && result.items.length > 0 ? (
          <ul aria-label={t("backupAssets.sources.compare.items")} className="divide-y divide-border border-y border-border">
            {result.items.map((item) => (
              <ComparisonRow key={backupFileSourceComparisonItemKey(item)} item={item} />
            ))}
          </ul>
        ) : null}
        {result?.nextCursor && (comparison.status === "ready" || loadingMore) ? (
          <Button
            ref={loadMoreRef}
            type="button"
            variant="outline"
            size="sm"
            className={cn("touch-target min-h-11 w-full lg:min-h-8", PENDING_BUTTON)}
            aria-disabled={loadingMore}
            onClick={() => loadMoreChanges(null)}
          >
            {loadingMore ? t("backupAssets.sources.compare.loadingMore") : t("backupAssets.sources.compare.loadMore")}
          </Button>
        ) : null}
        {comparison.status === "continuation_failed" ? (
          <InlineAlert tone="warning">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>{t("backupAssets.sources.compare.continuationFailed")}</span>
              <Button
                ref={retryContinuationRef}
                type="button"
                variant="outline"
                size="sm"
                className="touch-target min-h-11 lg:min-h-8"
                onClick={() => loadMoreChanges("loadMore")}
              >
                {t("backupAssets.sources.compare.retryContinuation")}
              </Button>
            </div>
          </InlineAlert>
        ) : null}
        {failure && (comparison.status === "failed" || comparison.status === "invalidated") ? (
          <InlineAlert tone="warning">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>{t(`backupAssets.sources.compare.errors.${failure.code}`)}</span>
              {showReselect ? (
                <Button
                  ref={reselectRef}
                  type="button"
                  variant="outline"
                  size="sm"
                  className="touch-target min-h-11 lg:min-h-8"
                  onClick={reselect}
                >
                  {t("backupAssets.sources.compare.reselect")}
                </Button>
              ) : (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="touch-target min-h-11 lg:min-h-8"
                  onClick={() => {
                    requestedFocus.current = "run";
                    comparison.retry();
                  }}
                >
                  {t("backupAssets.sources.compare.retry")}
                </Button>
              )}
            </div>
          </InlineAlert>
        ) : null}
      </section>
    </DialogBody>
  );
}

function ComparisonSummary({ status, itemCount, hasMore }: {
  status: BackupFileSourceComparisonStatus;
  itemCount: number;
  hasMore: boolean;
}) {
  const { t } = useTranslation();
  if (status === "loading") return <>{t("backupAssets.sources.compare.loading")}</>;
  if (status !== "ready" && status !== "loading_more" && status !== "continuation_failed") return null;
  if (status === "ready" && itemCount === 0 && !hasMore) return <>{t("backupAssets.sources.compare.empty")}</>;
  const parts = [t("backupAssets.sources.compare.loadedCount", { count: itemCount })];
  if (status === "continuation_failed") parts.push(t("backupAssets.sources.compare.incomplete"));
  else if (hasMore) parts.push(t("backupAssets.sources.compare.moreAvailable"));
  return <>{parts.join(" · ")}</>;
}

function SideSummary({ label, point, availability }: { label: string; point: string; availability: CatalogContentAvailability }) {
  const { t } = useTranslation();
  return (
    <div className="min-w-0 border-l-2 border-primary/40 pl-3">
      <div className="text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">{label}</div>
      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-2 text-sm font-medium">
        <span className="truncate">{point}</span>
        {availability.available ? null : <Badge tone="warning">{t("backupAssets.sources.compare.contentOffline")}</Badge>}
      </div>
    </div>
  );
}

/** Whitespace-only Catalog names are legal; quote them so they stay visible. */
function displayName(name: string): string {
  return name.trim() === "" ? `“${name}”` : name;
}

interface SidePath {
  side: "base" | "compare";
  /** Raw identity of the location, so display quoting cannot merge distinct paths. */
  key: string;
  /** Authorized names above the shown tail, root first; empty when the path is complete. */
  omitted: readonly string[];
  text: string;
  /** The entry sits at the point root, so its path is only its name. */
  root: boolean;
}

function sidePath(sideName: "base" | "compare", side: BackupFileSourceComparisonSide): SidePath {
  const names = [...side.omittedAncestorNames, ...side.breadcrumb.map((crumb) => crumb.name), side.name];
  return {
    side: sideName,
    key: JSON.stringify(names),
    omitted: side.omittedAncestorNames,
    text: [...side.breadcrumb.map((crumb) => crumb.name), side.name].map(displayName).join(" / "),
    root: names.length === 1,
  };
}

/** Reveals the folders above a truncated path on request; the omitted names stay collapsed until then. */
function OmittedAncestors({ names }: { names: readonly string[] }) {
  const { t } = useTranslation();
  return (
    <details className="group mt-0.5">
      <summary
        className={cn(
          "touch-target inline-flex min-h-11 cursor-pointer list-none items-center gap-0.5 rounded-sm font-medium text-primary lg:min-h-6",
          "hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden",
        )}
      >
        <ChevronRight className="size-3 shrink-0 transition-transform group-open:rotate-90" aria-hidden />
        {t("backupAssets.sources.compare.omittedFolders", { count: names.length })}
      </summary>
      <p className="mt-0.5 border-l border-border pl-2 whitespace-pre-wrap break-words">
        {`${names.map(displayName).join(" / ")} /`}
      </p>
    </details>
  );
}

function ComparisonRow({ item }: { item: BackupFileSourceComparisonItem }) {
  const { t } = useTranslation();
  const basePath = item.base ? sidePath("base", item.base) : null;
  const comparePath = item.compare ? sidePath("compare", item.compare) : null;
  // Matched entries normally share a location; show each side separately only when they differ.
  const paths: SidePath[] = basePath !== null && comparePath !== null && basePath.key === comparePath.key
    ? [comparePath]
    : [...(basePath === null ? [] : [basePath]), ...(comparePath === null ? [] : [comparePath])];
  const fields = item.changedFields.map((field) => t(`backupAssets.diff.field.${field}`)).join(", ");
  const matched = item.kind === "modified" || item.kind === "type_changed";
  const name = item.compare?.name ?? item.base?.name ?? "";
  // A matched root entry's path is just its name, already shown above.
  const visiblePaths = matched && paths.length === 1 && paths[0].root ? [] : paths;
  return (
    <li className="min-w-0 py-2.5">
      <div className="flex min-w-0 items-start gap-2">
        <Badge tone={item.kind === "added" ? "success" : item.kind === "removed" ? "destructive" : "warning"}>
          {t(`backupAssets.diff.kind.${item.kind}`)}
        </Badge>
        <span className="min-w-0 flex-1 whitespace-pre-wrap break-words text-sm font-medium">{displayName(name)}</span>
      </div>
      <div className="mt-1 space-y-0.5 text-xs text-muted-foreground">
        {visiblePaths.map(({ side, omitted, text }) => (
          <div key={side} className="min-w-0 whitespace-pre-wrap break-words">
            {paths.length > 1 || !matched ? (
              <span className="font-medium text-foreground/80">{t(`backupAssets.sources.compare.sides.${side}`)}: </span>
            ) : null}
            {omitted.length > 0 ? (
              <>
                <span aria-hidden title={t("backupAssets.sources.compare.truncatedPath")}>…</span>
                <span className="sr-only">{t("backupAssets.sources.compare.truncatedPath")}</span>
                {" / "}
              </>
            ) : null}
            {text}
            {omitted.length > 0 ? <OmittedAncestors names={omitted} /> : null}
          </div>
        ))}
      </div>
      {matched ? (
        <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span>{t("backupAssets.diff.changedFields", { fields })}</span>
          <span>{t(`backupAssets.diff.contentEquality.${item.contentEquality}`)}</span>
        </div>
      ) : null}
    </li>
  );
}
