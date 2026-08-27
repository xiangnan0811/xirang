import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { File, FileQuestion, Folder, Link as LinkIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";

import { formatBytes } from "@/lib/utils";

import {
  assetRefKey,
  type BackupAssetResultRow,
  type BackupAssetsRestorationAnchor,
} from "./backup-assets-state";

export interface AssetResultsViewProps {
  rows: BackupAssetResultRow[];
  selectedKeys: ReadonlySet<string>;
  activeKey: string | null;
  currentKey?: string | null;
  onActiveChange: (row: BackupAssetResultRow) => void;
  onSelectionToggle: (ref: BackupAssetResultRow["ref"]) => void;
  onOpen: (row: BackupAssetResultRow, position: Pick<BackupAssetsRestorationAnchor, "index" | "offset">) => void;
  restorationAnchor?: BackupAssetsRestorationAnchor | null;
  onRestorationComplete?: () => void;
}

const ROW_HEIGHT = 44;

export function AssetList({
  rows,
  selectedKeys,
  activeKey,
  currentKey = activeKey,
  onActiveChange,
  onSelectionToggle,
  onOpen,
  restorationAnchor = null,
  onRestorationComplete,
}: AssetResultsViewProps) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const itemRefs = useRef(new Map<number, HTMLButtonElement>());
  const [focusIndex, setFocusIndex] = useState(() => indexForKey(rows, activeKey));

  useEffect(() => setFocusIndex(indexForKey(rows, activeKey)), [activeKey, rows]);

  // TanStack Virtual exposes mutable helpers by design and remains local here.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 8,
    getItemKey: (index) => assetRefKey(rows[index].ref),
  });

  useEffect(() => {
    if (!restorationAnchor || !scrollRef.current) return;
    const scrollElement = scrollRef.current;
    const index = rows.findIndex((row) => assetRefKey(row.ref) === assetRefKey(restorationAnchor.ref));
    scrollElement.scrollTop = restorationAnchor.offset;
    if (index < 0) {
      scrollElement.focus();
      onRestorationComplete?.();
      return;
    }

    setFocusIndex(index);
    virtualizer.scrollToOffset(restorationAnchor.offset, { align: "start" });
    let retryFrame = 0;
    const focusFrame = requestAnimationFrame(() => {
      scrollElement.scrollTop = restorationAnchor.offset;
      const item = itemRefs.current.get(index);
      if (item) {
        item.focus();
        onRestorationComplete?.();
        return;
      }
      virtualizer.scrollToIndex(index, { align: "auto" });
      retryFrame = requestAnimationFrame(() => {
        (itemRefs.current.get(index) ?? scrollElement).focus();
        onRestorationComplete?.();
      });
    });
    return () => {
      cancelAnimationFrame(focusFrame);
      cancelAnimationFrame(retryFrame);
    };
  }, [onRestorationComplete, restorationAnchor, rows, virtualizer]);

  const moveFocus = (index: number) => {
    const bounded = Math.max(0, Math.min(rows.length - 1, index));
    const row = rows[bounded];
    if (!row) return;
    setFocusIndex(bounded);
    onActiveChange(row);
    virtualizer.scrollToIndex(bounded, { align: "auto" });
    const mounted = itemRefs.current.get(bounded);
    if (mounted) mounted.focus();
    else requestAnimationFrame(() => itemRefs.current.get(bounded)?.focus());
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (event.key === "ArrowDown") moveFocus(index + 1);
    else if (event.key === "ArrowUp") moveFocus(index - 1);
    else if (event.key === "Home") moveFocus(0);
    else if (event.key === "End") moveFocus(rows.length - 1);
    else return;
    event.preventDefault();
  };

  const activate = (index: number) => {
    const row = rows[index];
    if (!row) return;
    setFocusIndex(index);
    onActiveChange(row);
    onOpen(row, { index, offset: scrollRef.current?.scrollTop ?? 0 });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        aria-hidden="true"
        className="grid h-8 shrink-0 grid-cols-[44px_minmax(0,1fr)_72px_96px] items-center border-b border-border text-[11px] font-medium text-muted-foreground"
      >
        <span />
        <span>{t("backupAssets.browser.name")}</span>
        <span className="text-right">{t("backupAssets.browser.size")}</span>
        <span className="text-right">{t("backupAssets.browser.modified")}</span>
      </div>
      <div
        ref={scrollRef}
        role="list"
        aria-label={t("backupAssets.browser.listLabel")}
        tabIndex={-1}
        className="thin-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto"
      >
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = rows[virtualRow.index];
            const key = assetRefKey(row.ref);
            const selected = selectedKeys.has(key);
            return (
              <div
                key={key}
                role="listitem"
                data-selected={selected}
                data-index={virtualRow.index}
                className="absolute left-0 top-0 grid h-11 w-full grid-cols-[44px_minmax(0,1fr)_72px_96px] items-center border-b border-border/65 text-xs transition-colors data-[selected=true]:bg-primary/8"
                style={{ transform: `translateY(${virtualRow.start}px)` }}
              >
                <AssetSelectionCheckbox
                  row={row}
                  selected={selected}
                  onSelectionToggle={onSelectionToggle}
                />
                <button
                  ref={(node) => {
                    if (node) itemRefs.current.set(virtualRow.index, node);
                    else itemRefs.current.delete(virtualRow.index);
                  }}
                  type="button"
                  aria-current={key === currentKey ? "true" : undefined}
                  aria-label={`${t("backupAssets.actions.openAsset")} ${row.asset.name}`}
                  tabIndex={virtualRow.index === focusIndex ? 0 : -1}
                  className="col-span-3 grid h-11 min-w-0 grid-cols-[minmax(0,1fr)_72px_96px] items-center px-2 text-left outline-none transition-colors hover:bg-muted/55 focus-visible:bg-accent/60 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring aria-[current=true]:bg-accent/40"
                  onClick={() => activate(virtualRow.index)}
                  onKeyDown={(event) => handleKeyDown(event, virtualRow.index)}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <AssetTypeIcon type={row.asset.entryType} />
                    <span className="min-w-0 truncate font-medium" title={row.asset.name}>
                      {row.asset.name}
                    </span>
                    <RetainedVersionCount row={row} />
                  </span>
                  <span className="text-right tabular-nums text-muted-foreground">{formatBytes(row.asset.size)}</span>
                  <time
                    dateTime={row.asset.modifiedAt ?? undefined}
                    title={row.asset.modifiedAt ? formatAssetTime(row.asset.modifiedAt) : undefined}
                    className="truncate text-right tabular-nums text-muted-foreground"
                  >
                    {formatAssetTimeCompact(row.asset.modifiedAt)}
                  </time>
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

export function RetainedVersionCount({ row }: { row: BackupAssetResultRow }) {
  const { t } = useTranslation();
  if (row.source !== "search" || row.retainedVersionCount === undefined || row.retainedVersionCount <= 1) {
    return null;
  }
  return (
    <span className="shrink-0 text-muted-foreground">
      {t("backupAssets.browser.retainedVersionCount", { count: row.retainedVersionCount })}
    </span>
  );
}

export function AssetSelectionCheckbox({
  row,
  selected,
  onSelectionToggle,
}: {
  row: BackupAssetResultRow;
  selected: boolean;
  onSelectionToggle: AssetResultsViewProps["onSelectionToggle"];
}) {
  const { t } = useTranslation();
  return (
    <label className="flex size-11 cursor-pointer items-center justify-center focus-within:ring-2 focus-within:ring-inset focus-within:ring-ring">
      <input
        type="checkbox"
        checked={selected}
        aria-label={t("backupAssets.browser.selectAsset", { name: row.asset.name })}
        className="size-[18px] cursor-pointer accent-primary"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => event.stopPropagation()}
        onChange={() => onSelectionToggle(row.ref)}
      />
    </label>
  );
}

export function AssetTypeIcon({ type }: { type: BackupAssetResultRow["asset"]["entryType"] }) {
  const className = "size-4 shrink-0 text-muted-foreground";
  if (type === "directory") return <Folder className={`${className} text-primary`} aria-hidden />;
  if (type === "symlink" || type === "hardlink") return <LinkIcon className={className} aria-hidden />;
  if (type === "unknown" || type === "special") return <FileQuestion className={className} aria-hidden />;
  return <File className={className} aria-hidden />;
}

function indexForKey(rows: BackupAssetResultRow[], activeKey: string | null): number {
  const index = activeKey === null ? -1 : rows.findIndex((row) => assetRefKey(row.ref) === activeKey);
  return index < 0 ? 0 : index;
}

function formatAssetTime(value: string | null): string {
  return value ? value.slice(0, 16).replace("T", " ") : "-";
}

function formatAssetTimeCompact(value: string | null): string {
  return value ? `${value.slice(5, 10)} ${value.slice(11, 16)}` : "-";
}
