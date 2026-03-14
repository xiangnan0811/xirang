import { useCallback, useEffect, useRef, useState } from "react";
import {
  Folder,
  FolderOpen,
  File,
  ChevronRight,
  ArrowLeft,
  AlertCircle,
  RefreshCw,
} from "lucide-react";
import { FilePreviewDialog } from "@/components/file-preview-dialog";
import type { FileEntry, FileListResult, FileContentResult } from "@/lib/api/files-api";

type FileBrowserProps = {
  /** 加载目录内容 */
  fetchDir: (path: string, signal?: AbortSignal) => Promise<FileListResult>;
  /** 加载文件内容（用于预览） */
  fetchContent: (path: string) => Promise<FileContentResult>;
  /** 根路径（默认 "/"） */
  rootPath?: string;
  className?: string;
};

function parseBreadcrumbs(path: string, rootPath: string): Array<{ label: string; path: string }> {
  const root = rootPath || "/";
  const relative = path.startsWith(root) ? path.slice(root.length) : path;
  const segments = relative.split("/").filter(Boolean);

  const crumbs = [{ label: root === "/" ? "根目录" : root.split("/").pop() || root, path: root }];
  let current = root.endsWith("/") ? root.slice(0, -1) : root;
  for (const seg of segments) {
    current = current ? `${current}/${seg}` : `/${seg}`;
    crumbs.push({ label: seg, path: current });
  }
  return crumbs;
}

function formatSize(bytes: number, isDir: boolean): string {
  if (isDir) return "—";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

function formatModTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { hour12: false });
}

export function FileBrowser({ fetchDir, fetchContent, rootPath = "/", className }: FileBrowserProps) {
  const [currentPath, setCurrentPath] = useState<string>(rootPath);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [previewOpen, setPreviewOpen] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  const loadDir = useCallback(
    (path: string) => {
      abortRef.current?.abort();
      const ctrl = new AbortController();
      abortRef.current = ctrl;

      setLoading(true);
      setError(null);

      fetchDir(path, ctrl.signal)
        .then((result) => {
          setEntries(result.entries);
          setTruncated(result.truncated);
          setCurrentPath(result.path);
        })
        .catch((err: unknown) => {
          if (err instanceof Error && err.name === "AbortError") return;
          setError(err instanceof Error ? err.message : "加载目录失败");
        })
        .finally(() => setLoading(false));
    },
    [fetchDir]
  );

  useEffect(() => {
    loadDir(rootPath);
    return () => abortRef.current?.abort();
  }, [rootPath, loadDir]);

  const handleNavigate = (path: string) => {
    loadDir(path);
  };

  const handleEntryClick = (entry: FileEntry) => {
    if (entry.is_dir) {
      handleNavigate(entry.path);
    } else {
      setPreviewPath(entry.path);
      setPreviewOpen(true);
    }
  };

  const breadcrumbs = parseBreadcrumbs(currentPath, rootPath);
  const parentPath = breadcrumbs.length > 1 ? breadcrumbs[breadcrumbs.length - 2].path : null;

  // 目录优先，文件其次，各自按名称排序
  const sorted = [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, "zh-CN");
  });

  return (
    <div className={className}>
      {/* 面包屑导航 */}
      <div className="mb-2 flex items-center gap-1 overflow-x-auto text-sm thin-scrollbar">
        {parentPath !== null && (
          <button
            type="button"
            className="mr-1 flex items-center gap-1 rounded p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
            onClick={() => handleNavigate(parentPath)}
            aria-label="返回上级目录"
          >
            <ArrowLeft className="size-3.5" />
          </button>
        )}
        {breadcrumbs.map((crumb, i) => (
          <span key={crumb.path} className="flex items-center gap-1 min-w-0">
            {i > 0 && <ChevronRight className="size-3 shrink-0 text-muted-foreground/50" />}
            {i < breadcrumbs.length - 1 ? (
              <button
                type="button"
                className="truncate max-w-[160px] text-primary hover:underline"
                onClick={() => handleNavigate(crumb.path)}
              >
                {crumb.label}
              </button>
            ) : (
              <span className="truncate max-w-[200px] font-medium">{crumb.label}</span>
            )}
          </span>
        ))}
        <button
          type="button"
          className="ml-auto shrink-0 rounded p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
          onClick={() => loadDir(currentPath)}
          aria-label="刷新"
          disabled={loading}
        >
          <RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />
        </button>
      </div>

      {/* 截断提示 */}
      {truncated && (
        <div className="mb-2 rounded border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-600 dark:text-amber-400">
          目录条目超过 500 条，仅展示前 500 项
        </div>
      )}

      {/* 错误提示 */}
      {error && (
        <div className="flex items-center gap-2 rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="size-4 shrink-0" />
          <span>{error}</span>
          <button
            type="button"
            className="ml-auto text-xs underline"
            onClick={() => loadDir(currentPath)}
          >
            重试
          </button>
        </div>
      )}

      {/* 文件列表 */}
      {!error && (
        <div className="glass-panel overflow-hidden rounded-lg">
          {loading && entries.length === 0 ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              加载中...
            </div>
          ) : sorted.length === 0 ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              目录为空
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border/40 text-xs text-muted-foreground">
                  <th className="px-4 py-2 text-left font-medium">名称</th>
                  <th className="hidden px-4 py-2 text-right font-medium sm:table-cell">大小</th>
                  <th className="hidden px-4 py-2 text-left font-medium md:table-cell">权限</th>
                  <th className="hidden px-4 py-2 text-left font-medium lg:table-cell">修改时间</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/30">
                {sorted.map((entry) => (
                  <tr
                    key={entry.path}
                    className="group cursor-pointer transition-colors hover:bg-muted/30"
                    onClick={() => handleEntryClick(entry)}
                    tabIndex={0}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") handleEntryClick(entry);
                    }}
                    role="button"
                    aria-label={`${entry.is_dir ? "进入目录" : "预览文件"} ${entry.name}`}
                  >
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        {entry.is_dir ? (
                          <FolderOpen className="size-4 shrink-0 text-amber-500" />
                        ) : (
                          <File className="size-4 shrink-0 text-muted-foreground" />
                        )}
                        <span className={`truncate max-w-[240px] ${entry.is_dir ? "font-medium" : ""}`}>
                          {entry.name}
                        </span>
                      </div>
                    </td>
                    <td className="hidden px-4 py-2 text-right tabular-nums text-muted-foreground sm:table-cell">
                      {formatSize(entry.size, entry.is_dir)}
                    </td>
                    <td className="hidden px-4 py-2 font-mono text-xs text-muted-foreground md:table-cell">
                      {entry.mode}
                    </td>
                    <td className="hidden px-4 py-2 text-muted-foreground lg:table-cell">
                      {formatModTime(entry.mod_time)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {/* 文件预览对话框 */}
      {previewPath && (
        <FilePreviewDialog
          open={previewOpen}
          onOpenChange={setPreviewOpen}
          filePath={previewPath}
          fetchContent={() => fetchContent(previewPath)}
        />
      )}
    </div>
  );
}

export { Folder };
