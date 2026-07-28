import { useEffect, useMemo } from "react";
import { Archive, CircleAlert, Download, Loader2, RefreshCw, Square } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { Pagination } from "@/components/ui/pagination";
import type { AuthContextValue } from "@/context/auth-context.shared";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { formatBytes } from "@/lib/utils";
import type { AssetRef, BackupArchiveIndexEntry, BackupExportDownloadTicket } from "@/types/domain";

import { useBackupArchive, type BackupArchiveApi } from "./use-backup-archive";

const ARCHIVE_MEMBER_PAGE_SIZE = 100;
const EMPTY_ARCHIVE_ENTRIES: BackupArchiveIndexEntry[] = [];

interface ArchiveMemberPresentation {
  entry: BackupArchiveIndexEntry;
  depth: number;
  parentName: string | null;
}

export interface ArchiveMemberPanelProps {
  refValue: AssetRef;
  runtime: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof">;
  contentAvailable: boolean;
  downloadAllowed: boolean;
  online?: boolean;
  onPrepareDownload: (ref: AssetRef) => void | Promise<void>;
  onDownloadTicket?: (ticket: BackupExportDownloadTicket) => void;
  onDismissHandlerRegister?: (handler: () => void) => () => void;
  api?: BackupArchiveApi;
}

export function ArchiveMemberPanel({
  refValue,
  runtime,
  contentAvailable,
  downloadAllowed,
  online,
  onPrepareDownload,
  onDownloadTicket,
  onDismissHandlerRegister,
  api,
}: ArchiveMemberPanelProps) {
  const { t } = useTranslation();
  const controller = useBackupArchive({
    token: runtime.token,
    role: runtime.role,
    ref: refValue,
    ensureStepUpProof: runtime.ensureStepUpProof,
    api,
    contentAvailable,
    downloadAllowed,
    online,
    onPrepareDownload,
    onDownloadTicket,
  });
  const { open } = controller;
  const selected = useMemo(
    () => controller.state.index?.entries.find((entry) => entry.id === controller.state.selectedMemberId) ?? null,
    [controller.state.index, controller.state.selectedMemberId],
  );
  const entries = controller.state.index?.entries ?? EMPTY_ARCHIVE_ENTRIES;
  const archiveMembers = useMemo(() => archiveMemberHierarchy(entries), [entries]);
  const { pagedItems: pagedEntries, page, pageSize, total, setPage } = useClientPagination(archiveMembers, ARCHIVE_MEMBER_PAGE_SIZE);

  useEffect(() => {
    void open();
  }, [open]);

  useEffect(() => {
    if (!onDismissHandlerRegister) return;
    return onDismissHandlerRegister(controller.dismiss);
  }, [controller.dismiss, onDismissHandlerRegister]);

  if (controller.state.phase === "closed" || controller.state.phase === "indexing") {
    return <LoadingState title={t("backupAssets.archive.loading")} rows={6} />;
  }
  if (controller.state.phase === "error") {
    return (
      <div className="p-4">
        <InlineAlert tone="warning">{t(`backupAssets.archive.error.${controller.state.error ?? "unavailable"}`)}</InlineAlert>
        <div className="mt-3 flex flex-wrap justify-end gap-2">
          {controller.state.requestId ? (
            <Button type="button" variant="outline" size="sm" onClick={() => void controller.cancel()}>
              <Square className="size-3.5" aria-hidden />
              {t("backupAssets.archive.cancel")}
            </Button>
          ) : null}
          <Button type="button" variant="outline" size="sm" onClick={() => void controller.reload()}>
            <RefreshCw className="size-4" aria-hidden />
            {t("backupAssets.archive.reload")}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <section aria-labelledby="backup-archive-member-title" className="flex min-h-0 flex-col">
      <header className="border-b border-border px-4 py-3">
        <div className="flex items-center gap-2">
          <Archive className="size-4 text-primary" aria-hidden />
          <h3 id="backup-archive-member-title" className="text-sm font-semibold">
            {t("backupAssets.archive.title")}
          </h3>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{t("backupAssets.archive.description")}</p>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {entries.length ? (
          <>
            <ul className="divide-y divide-border" aria-label={t("backupAssets.archive.members")}>
              {pagedEntries.map(({ entry, depth, parentName }) => (
                <ArchiveMemberRow
                  key={entry.id}
                  entry={entry}
                  depth={depth}
                  parentName={parentName}
                  disabled={controller.state.phase === "creating" || controller.state.phase === "active"}
                  selected={controller.state.selectedMemberId === entry.id}
                  onSelect={() => void controller.create(entry.id)}
                />
              ))}
            </ul>
            <Pagination
              className="mt-3"
              page={page}
              pageSize={pageSize}
              total={total}
              onPageChange={setPage}
            />
          </>
        ) : (
          <p role="status" className="p-4 text-center text-sm text-muted-foreground">
            {t("backupAssets.archive.empty")}
          </p>
        )}

        {controller.state.status ? (
          <div className="mt-3 rounded-md border border-border bg-muted/50 p-3" aria-live="polite">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="min-w-0 truncate text-xs font-medium">
                {selected?.displayName ?? t("backupAssets.archive.selectedMember")}
              </span>
              <Badge tone={controller.state.status.state === "ready" ? "success" : controller.state.status.state === "failed" ? "warning" : "info"}>
                {t(`backupAssets.archive.state.${controller.state.status.state}`)}
              </Badge>
            </div>
            {controller.state.status.failureProduct ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(`backupAssets.archive.failure.${controller.state.status.failureProduct}`)}
              </p>
            ) : null}
            {controller.state.fallback.reason ? (
              <p className="mt-2 text-xs text-muted-foreground">{t("backupAssets.archive.originalUnavailable")}</p>
            ) : null}
            {controller.state.error ? (
              <InlineAlert tone="warning" className="mt-3">
                {t(`backupAssets.archive.error.${controller.state.error}`)}
              </InlineAlert>
            ) : null}
          </div>
        ) : null}
      </div>

      <footer className="flex min-h-12 flex-wrap items-center justify-end gap-2 border-t border-border px-4 py-2">
        {controller.state.phase === "creating" || controller.state.phase === "active" ? (
          <Button type="button" variant="outline" size="sm" onClick={() => void controller.cancel()}>
            {controller.state.phase === "creating" ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Square className="size-3.5" aria-hidden />}
            {t("backupAssets.archive.cancel")}
          </Button>
        ) : null}
        {controller.state.status?.state === "ready" && downloadAllowed ? (
          <Button type="button" size="sm" onClick={() => void controller.download()}>
            <Download className="size-4" aria-hidden />
            {t("backupAssets.archive.downloadMember")}
          </Button>
        ) : null}
        {controller.state.fallback.action === "download_original" ? (
          <Button type="button" variant="outline" size="sm" onClick={() => void controller.downloadOriginal()}>
            <Download className="size-4" aria-hidden />
            {t("backupAssets.archive.downloadOriginal")}
          </Button>
        ) : null}
      </footer>
    </section>
  );
}

function ArchiveMemberRow({
  entry,
  depth,
  parentName,
  disabled,
  selected,
  onSelect,
}: {
  entry: BackupArchiveIndexEntry;
  depth: number;
  parentName: string | null;
  disabled: boolean;
  selected: boolean;
  onSelect: () => void;
}) {
  const { t } = useTranslation();
  const hierarchyId = `backup-archive-member-${entry.id}-hierarchy`;
  const hierarchy = parentName === null
    ? t("backupAssets.archive.hierarchy.root", { level: depth + 1 })
    : t("backupAssets.archive.hierarchy.child", { level: depth + 1, parent: parentName });
  return (
    <li>
      <button
        type="button"
        className="flex min-h-12 w-full items-center gap-3 px-2 py-2 text-left hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35 disabled:cursor-not-allowed disabled:opacity-50"
        style={{ paddingInlineStart: `${0.5 + Math.min(depth, 8) * 1.25}rem` }}
        disabled={disabled}
        aria-pressed={selected}
        aria-label={t("backupAssets.archive.retrieveMember", { name: entry.displayName })}
        aria-describedby={hierarchyId}
        onClick={onSelect}
      >
        <Archive className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-medium">{entry.displayName}</span>
          <span className="block truncate text-xs text-muted-foreground">
            {entry.mediaType} · {formatBytes(entry.size)}
          </span>
          <span id={hierarchyId} className="block truncate text-xs text-muted-foreground">
            {hierarchy}
          </span>
          {entry.warning !== "none" ? (
            <span className="mt-1 flex items-center gap-1 text-xs text-warning" role="status">
              <CircleAlert className="size-3 shrink-0" aria-hidden />
              {t("backupAssets.archive.warning.advisory")}
            </span>
          ) : null}
        </span>
      </button>
    </li>
  );
}

function archiveMemberHierarchy(entries: BackupArchiveIndexEntry[]): ArchiveMemberPresentation[] {
  const byID = new Map(entries.map((entry) => [entry.id, entry]));
  const roots: BackupArchiveIndexEntry[] = [];
  const childrenByParent = new Map<string, BackupArchiveIndexEntry[]>();

  for (const entry of entries) {
    if (entry.parentId !== null && byID.has(entry.parentId)) {
      const children = childrenByParent.get(entry.parentId) ?? [];
      children.push(entry);
      childrenByParent.set(entry.parentId, children);
    } else {
      roots.push(entry);
    }
  }

  roots.sort(compareArchiveMembers);
  for (const children of childrenByParent.values()) children.sort(compareArchiveMembers);

  const presentations: ArchiveMemberPresentation[] = [];
  const visited = new Set<string>();
  const appendHierarchy = (root: BackupArchiveIndexEntry) => {
    const stack: ArchiveMemberPresentation[] = [{ entry: root, depth: 0, parentName: null }];
    while (stack.length > 0) {
      const current = stack.pop();
      if (!current || visited.has(current.entry.id)) continue;
      visited.add(current.entry.id);
      presentations.push(current);
      const children = childrenByParent.get(current.entry.id) ?? [];
      for (let index = children.length - 1; index >= 0; index -= 1) {
        stack.push({ entry: children[index], depth: current.depth + 1, parentName: current.entry.displayName });
      }
    }
  };

  for (const root of roots) appendHierarchy(root);
  if (presentations.length < entries.length) {
    for (const entry of [...entries].sort(compareArchiveMembers)) appendHierarchy(entry);
  }

  return presentations;
}

function compareArchiveMembers(left: BackupArchiveIndexEntry, right: BackupArchiveIndexEntry): number {
  const leftName = left.displayName.normalize("NFKC").toLocaleLowerCase("en-US");
  const rightName = right.displayName.normalize("NFKC").toLocaleLowerCase("en-US");
  if (leftName < rightName) return -1;
  if (leftName > rightName) return 1;
  if (left.id < right.id) return -1;
  if (left.id > right.id) return 1;
  return 0;
}
