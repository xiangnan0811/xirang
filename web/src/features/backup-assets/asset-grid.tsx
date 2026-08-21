import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";

import { formatBytes } from "@/lib/utils";

import { AssetSelectionMark, AssetTypeIcon, RetainedVersionCount, type AssetResultsViewProps } from "./asset-list";
import { assetRefKey } from "./backup-assets-state";

const TILE_HEIGHT = 144;
const TILE_MIN_WIDTH = 168;
const TILE_GAP = 8;

export function AssetGrid({
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
  const [columns, setColumns] = useState(4);
  const [focusIndex, setFocusIndex] = useState(0);

  useEffect(() => {
    const updateColumns = () => {
      const width = scrollRef.current?.clientWidth ?? 0;
      if (width > 0) setColumns(Math.max(1, Math.floor((width + TILE_GAP) / (TILE_MIN_WIDTH + TILE_GAP))));
    };
    updateColumns();
    window.addEventListener("resize", updateColumns);
    return () => window.removeEventListener("resize", updateColumns);
  }, []);

  useEffect(() => {
    const index = activeKey === null ? -1 : rows.findIndex((row) => assetRefKey(row.ref) === activeKey);
    setFocusIndex(index < 0 ? 0 : index);
  }, [activeKey, rows]);

  const virtualRowCount = Math.ceil(rows.length / columns);
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: virtualRowCount,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => TILE_HEIGHT + TILE_GAP,
    overscan: 4,
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
      virtualizer.scrollToIndex(Math.floor(index / columns), { align: "auto" });
      retryFrame = requestAnimationFrame(() => {
        (itemRefs.current.get(index) ?? scrollElement).focus();
        onRestorationComplete?.();
      });
    });
    return () => {
      cancelAnimationFrame(focusFrame);
      cancelAnimationFrame(retryFrame);
    };
  }, [columns, onRestorationComplete, restorationAnchor, rows, virtualizer]);

  const moveFocus = (index: number) => {
    const bounded = Math.max(0, Math.min(rows.length - 1, index));
    const row = rows[bounded];
    if (!row) return;
    setFocusIndex(bounded);
    onActiveChange(row);
    virtualizer.scrollToIndex(Math.floor(bounded / columns), { align: "auto" });
    const mounted = itemRefs.current.get(bounded);
    if (mounted) mounted.focus();
    else requestAnimationFrame(() => itemRefs.current.get(bounded)?.focus());
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>, index: number) => {
    if (event.key === "ArrowRight") moveFocus(index + 1);
    else if (event.key === "ArrowLeft") moveFocus(index - 1);
    else if (event.key === "ArrowDown") moveFocus(index + columns);
    else if (event.key === "ArrowUp") moveFocus(index - columns);
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
    <div
      ref={scrollRef}
      role="grid"
      aria-label={t("backupAssets.browser.gridLabel")}
      aria-multiselectable="true"
      tabIndex={-1}
      className="thin-scrollbar min-h-0 flex-1 overflow-auto p-2"
    >
      <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((virtualRow) => (
          <div
            key={virtualRow.key}
            role="row"
            className="absolute left-0 top-0 grid w-full gap-2"
            style={{
              gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
              transform: `translateY(${virtualRow.start}px)`,
            }}
          >
            {rows
              .slice(virtualRow.index * columns, virtualRow.index * columns + columns)
              .map((row, columnIndex) => {
                const index = virtualRow.index * columns + columnIndex;
                const key = assetRefKey(row.ref);
                const selected = selectedKeys.has(key);
                return (
                  <div
                    key={key}
                    ref={(node) => {
                      if (node) itemRefs.current.set(index, node);
                      else itemRefs.current.delete(index);
                    }}
                    role="gridcell"
                    aria-selected={selected}
                    tabIndex={index === focusIndex ? 0 : -1}
                    className="group relative flex h-36 min-w-0 flex-col border border-border bg-card p-3 outline-none transition-colors hover:border-primary/45 hover:bg-muted/35 focus-visible:ring-2 focus-visible:ring-ring aria-selected:border-primary aria-selected:bg-primary/8"
                    onClick={() => {
                      setFocusIndex(index);
                      onActiveChange(row);
                      onSelectionToggle(row.ref);
                    }}
                    onDoubleClick={() =>
                      onOpen(row, { index, offset: scrollRef.current?.scrollTop ?? 0 })
                    }
                    onKeyDown={(event) => handleKeyDown(event, index)}
                  >
                    <span className="absolute right-2 top-2">
                      <AssetSelectionMark selected={selected} />
                    </span>
                    <span className="flex size-9 items-center justify-center border border-border bg-background">
                      <AssetTypeIcon type={row.asset.entryType} />
                    </span>
                    <span className="mt-3 line-clamp-2 min-w-0 break-all text-xs font-medium" title={row.asset.name}>
                      {row.asset.name}
                    </span>
                    <RetainedVersionCount row={row} />
                    <span className="mt-auto text-[11px] tabular-nums text-muted-foreground">
                      {formatBytes(row.asset.size)}
                    </span>
                  </div>
                );
              })}
          </div>
        ))}
      </div>
    </div>
  );
}
