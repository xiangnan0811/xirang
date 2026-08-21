import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Check, File, FileQuestion, Folder, Link as LinkIcon } from "lucide-react";
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
  onActiveChange,
  onSelectionToggle,
  onOpen,
  restorationAnchor = null,
  onRestorationComplete,
}: AssetResultsViewProps) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const itemRefs = useRef(new Map<number, HTMLDivElement>());
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

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>, index: number) => {
    if (event.key === "ArrowDown") moveFocus(index + 1);
    else if (event.key === "ArrowUp") moveFocus(index - 1);
    else if (event.key === "Home") moveFocus(0);
    else if (event.key === "End") moveFocus(rows.length - 1);
    else if (event.key === " ") {
      event.preventDefault();
      onSelectionToggle(rows[index].ref);
      return;
    } else if (event.key === "Enter") {
      event.preventDefault();
      onOpen(rows[index], { index, offset: scrollRef.current?.scrollTop ?? 0 });
      return;
    } else return;
    event.preventDefault();
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        aria-hidden="true"
        className="grid h-8 shrink-0 grid-cols-[36px_minmax(0,1fr)_72px_96px] items-center border-b border-border px-2 text-[11px] font-medium text-muted-foreground"
      >
        <span />
        <span>{t("backupAssets.browser.name")}</span>
        <span className="text-right">{t("backupAssets.browser.size")}</span>
        <span className="text-right">{t("backupAssets.browser.modified")}</span>
      </div>
      <div
        ref={scrollRef}
        role="listbox"
        aria-label={t("backupAssets.browser.listLabel")}
        aria-multiselectable="true"
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
                ref={(node) => {
                  if (node) itemRefs.current.set(virtualRow.index, node);
                  else itemRefs.current.delete(virtualRow.index);
                }}
                role="option"
                aria-selected={selected}
                tabIndex={virtualRow.index === focusIndex ? 0 : -1}
                data-index={virtualRow.index}
                className="absolute left-0 top-0 grid h-11 w-full grid-cols-[36px_minmax(0,1fr)_72px_96px] items-center border-b border-border/65 px-2 text-xs outline-none transition-colors hover:bg-muted/55 focus-visible:bg-accent/60 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring aria-selected:bg-primary/8"
                style={{ transform: `translateY(${virtualRow.start}px)` }}
                onClick={() => {
                  setFocusIndex(virtualRow.index);
                  onActiveChange(row);
                  onSelectionToggle(row.ref);
                }}
                onDoubleClick={() =>
                  onOpen(row, { index: virtualRow.index, offset: scrollRef.current?.scrollTop ?? 0 })
                }
                onKeyDown={(event) => handleKeyDown(event, virtualRow.index)}
              >
                <span className="flex justify-center">
                  <AssetSelectionMark selected={selected} />
                </span>
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

export function AssetSelectionMark({ selected }: { selected: boolean }) {
  return (
    <span
      aria-hidden
      className={
        selected
          ? "flex size-[18px] items-center justify-center rounded-[5px] border-[1.5px] border-primary bg-primary text-primary-foreground"
          : "size-[18px] rounded-[5px] border-[1.5px] border-input bg-card"
      }
    >
      {selected ? <Check className="size-3" strokeWidth={3} aria-hidden /> : null}
    </span>
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
