import { useCallback, useEffect, useRef, useState } from "react";
import { File, Loader2, Search, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { apiClient } from "@/lib/api/client";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import type { SearchResult, SearchIndexingStatus } from "@/lib/api/snapshots-api";
import { toast } from "sonner";

interface SnapshotSearchProps {
  taskId: number;
  token: string;
  onNavigateToFile: (snapshotId: string, path: string) => void;
}

function isIndexingStatus(
  value: SearchResult[] | SearchIndexingStatus
): value is SearchIndexingStatus {
  return value !== null && typeof value === "object" && !Array.isArray(value) && "status" in value;
}

export function SnapshotSearch({ taskId, token, onNavigateToFile }: SnapshotSearchProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [indexing, setIndexing] = useState(false);
  const [indexingMessage, setIndexingMessage] = useState("");
  const [searched, setSearched] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const performSearch = useCallback(
    async (q: string) => {
      if (!q.trim()) {
        setResults(null);
        setSearched(false);
        return;
      }

      // 取消上一次请求
      if (abortRef.current) {
        abortRef.current.abort();
      }
      const controller = new AbortController();
      abortRef.current = controller;

      setSearching(true);
      setIndexing(false);
      setIndexingMessage("");
      setSearched(true);

      try {
        const data = await apiClient.searchFiles(token, taskId, q, controller.signal);
        if (controller.signal.aborted) return;

        if (isIndexingStatus(data)) {
          setIndexing(true);
          setIndexingMessage(data.message ?? t("snapshotSearch.indexing"));
          setResults(null);
        } else {
          setResults(data);
          setIndexing(false);
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          toast.error(getErrorMessage(err, t("snapshots.loadFailed")));
          setResults(null);
        }
      } finally {
        if (!controller.signal.aborted) {
          setSearching(false);
        }
      }
    },
    [token, taskId, t]
  );

  // 防抖搜索（300ms）
  useEffect(() => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
    }
    debounceRef.current = setTimeout(() => {
      performSearch(query);
    }, 300);

    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
      }
    };
  }, [query, performSearch]);

  // 组件卸载时取消请求
  useEffect(() => {
    return () => {
      if (abortRef.current) {
        abortRef.current.abort();
      }
    };
  }, []);

  const handleClear = () => {
    setQuery("");
    setResults(null);
    setSearched(false);
    setIndexing(false);
    setIndexingMessage("");
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      handleClear();
      (e.target as HTMLInputElement).blur();
    }
  };

  return (
    <div className="space-y-3">
      {/* 搜索栏 */}
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={t("snapshotSearch.placeholder")}
            aria-label={t("snapshotSearch.placeholder")}
            className="w-full rounded-md border border-border bg-background pl-8 pr-8 py-1.5 text-sm"
          />
          {query && (
            <button
              type="button"
              onClick={handleClear}
              className="absolute right-2 top-1/2 -translate-y-1/2 size-4 text-muted-foreground hover:text-foreground"
              aria-label={t("common.reset")}
            >
              <X className="size-3.5" />
            </button>
          )}
        </div>
      </div>

      {/* 索引构建中 */}
      {indexing && (
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          <span>{indexingMessage}</span>
        </div>
      )}

      {/* 搜索中 */}
      {searching && !indexing && (
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          <span>{t("snapshotSearch.searching")}</span>
        </div>
      )}

      {/* 搜索结果 */}
      {results !== null && (
        <>
          <p className="text-xs text-muted-foreground">
            {t("snapshotSearch.resultCount", { count: results.length })}
          </p>
          {results.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-4">
              {t("snapshotSearch.noResults")}
            </p>
          ) : (
            <div className="rounded-md border border-border/60 divide-y divide-border/30 max-h-64 overflow-y-auto">
              {results.map((result, i) => (
                <button
                  key={`${result.snapshot_id}-${result.path}-${i}`}
                  type="button"
                  className="w-full flex items-center gap-2.5 px-3 py-2 text-sm hover:bg-muted/40 text-left"
                  onClick={() => onNavigateToFile(result.snapshot_id, result.path)}
                >
                  <File className="size-3.5 text-muted-foreground shrink-0" />
                  <span className="flex-1 min-w-0 truncate font-mono text-xs">
                    {result.path}
                  </span>
                  <span className="text-xs text-muted-foreground shrink-0">
                    {result.snapshot_id.length > 8
                      ? result.snapshot_id.slice(0, 8)
                      : result.snapshot_id}
                  </span>
                  {result.size > 0 && (
                    <span className="text-xs text-muted-foreground shrink-0">
                      {formatBytes(result.size)}
                    </span>
                  )}
                </button>
              ))}
            </div>
          )}
        </>
      )}

      {/* 初始状态提示 */}
      {!searched && !searching && !indexing && (
        <p className="text-xs text-muted-foreground text-center py-4">
          {t("snapshotSearch.searchHint")}
        </p>
      )}
    </div>
  );
}
