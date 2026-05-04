import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { LogEvent } from "@/types/domain";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { cn } from "@/lib/utils";
import { LogEntry } from "../logs-page.log-entry";

export interface LogsViewerProps {
  filteredLogs: LogEvent[];
  historyLoading: boolean;
  onReset: () => void;
}

/**
 * 注意：filteredLogs 由 logs-page.tsx 按 logId 降序排序（新日志在数组前端，
 * 渲染上即位于容器顶部），因此“吸附到最新日志”等价于“吸附到顶部”。
 *
 * STICK_THRESHOLD_PX 为允许偏离顶部的阈值；超过则视为用户主动向下滚动查看历史，
 * 此时不再自动跳回顶部，避免打断阅读。
 */
const ROW_ESTIMATED_HEIGHT = 28;
const ROW_OVERSCAN = 12;
const STICK_THRESHOLD_PX = 64;

export function LogsViewer({
  filteredLogs,
  historyLoading,
  onReset,
}: LogsViewerProps) {
  const { t } = useTranslation();
  const parentRef = useRef<HTMLDivElement | null>(null);
  // 默认 true：首次进入页面应吸附到最新日志
  const [stickToNewest, setStickToNewest] = useState(true);

  const virtualizer = useVirtualizer({
    count: filteredLogs.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW_ESTIMATED_HEIGHT,
    overscan: ROW_OVERSCAN,
    // 保留 logId/id 作为稳定 key，避免重渲染时丢失测量缓存
    getItemKey: (index) => {
      const log = filteredLogs[index];
      return log?.logId ? `log-${log.logId}` : (log?.id ?? index);
    },
  });

  // 滚动监听：根据滚动位置更新 stickToNewest
  // 数据按 logId 降序，最新日志位于顶部 → 顶部附近 = "贴近最新"
  const handleScroll = useCallback(() => {
    const el = parentRef.current;
    if (!el) return;
    const nearTop = el.scrollTop <= STICK_THRESHOLD_PX;
    setStickToNewest((prev) => (prev === nearTop ? prev : nearTop));
  }, []);

  // 当列表新增条目且当前处于"吸附最新"模式时，主动滚回顶部
  // 用 useLayoutEffect 避免 DOM 更新与滚动复位之间的可见跳动
  useLayoutEffect(() => {
    if (!stickToNewest) return;
    if (filteredLogs.length === 0) return;
    // scrollToIndex(0, { align: 'start' }) 等价于 scrollTop = 0
    virtualizer.scrollToIndex(0, { align: "start" });
  }, [filteredLogs.length, stickToNewest, virtualizer]);

  if (historyLoading && filteredLogs.length === 0) {
    return (
      <LoadingState
        title={t("logs.loadingTitle")}
        description={t("logs.loadingDesc")}
        rows={4}
      />
    );
  }

  const virtualItems = virtualizer.getVirtualItems();
  const totalSize = virtualizer.getTotalSize();

  return (
    <div
      ref={parentRef}
      onScroll={handleScroll}
      className={cn(
        "terminal-surface thin-scrollbar overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-nav",
        "h-[62vh]",
      )}
      role="log"
      aria-live="polite"
      aria-relevant="additions text"
      aria-label={t("logs.terminalAriaLabel", {
        count: filteredLogs.length,
      })}
    >
      {filteredLogs.length > 0 ? (
        <div
          className="px-1"
          style={{
            height: `${totalSize}px`,
            position: "relative",
            width: "100%",
          }}
        >
          {virtualItems.map((vi) => {
            const log = filteredLogs[vi.index];
            if (!log) return null;
            return (
              <div
                key={vi.key}
                ref={virtualizer.measureElement}
                data-index={vi.index}
                style={{
                  position: "absolute",
                  top: 0,
                  left: 0,
                  width: "100%",
                  transform: `translateY(${vi.start}px)`,
                }}
              >
                <LogEntry log={log} hoverClass="hover:bg-white/10" />
              </div>
            );
          })}
        </div>
      ) : (
        <div className="px-2 py-10">
          <EmptyState
            className="terminal-empty"
            title={t("logs.emptyTitle")}
            description={t("logs.emptyDesc")}
            action={
              <Button size="sm" variant="outline" onClick={onReset}>
                {t("logs.resetFilter")}
              </Button>
            }
          />
        </div>
      )}
    </div>
  );
}
