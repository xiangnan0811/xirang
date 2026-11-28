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
import { useTranslation } from "react-i18next";
import { formatTime } from "@/lib/api/core";
import { FilePreviewDialog } from "@/components/file-preview-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { FileEntry, FileListResult, FileContentResult } from "@/lib/api/files-api";

type FileBrowserProps = {
  /** 加载目录内容 */
  fetchDir: (path: string, signal?: AbortSignal) => Promise<FileListResult>;
  /**
   * 可选：由服务端选择起始目录。提供时首次加载（以及首次成功前的重试/刷新）调用它而非
   * fetchDir(rootPath)，并以响应中的规范路径作为本次挂载的面包屑根；之后一律用显式路径。
   */
  fetchInitialDir?: (signal?: AbortSignal) => Promise<FileListResult>;
  /** 加载文件内容（用于预览） */
  fetchContent: (path: string, signal?: AbortSignal) => Promise<FileContentResult>;
  /** 固定根路径（默认 "/"）；提供 fetchInitialDir 时忽略 */
  rootPath?: string;
  className?: string;
};

type DirRequest = { kind: "initial" } | { kind: "path"; path: string };

type Location = { root: string; path: string };

/** Absolute, normalized POSIX path: no empty, "." or ".." components and no trailing slash. */
function isCanonicalPath(path: unknown): path is string {
  if (typeof path !== "string" || !path.startsWith("/") || path.includes("\0")) return false;
  if (path === "/") return true;
  return path.slice(1).split("/").every((seg) => seg !== "" && seg !== "." && seg !== "..");
}

function parseBreadcrumbs({ root, path }: Location, rootLabel: string): Array<{ label: string; path: string }> {
  const crumbs = [{ label: root === "/" ? rootLabel : root.slice(root.lastIndexOf("/") + 1), path: root }];
  if (path === root) return crumbs;
  if (root !== "/" && !path.startsWith(`${root}/`)) {
    // The server may authorize a canonical target outside this tab's root (e.g. a symlink into
    // another configured root). Never manufacture ancestors for it; the root crumb is the reset.
    crumbs.push({ label: path, path });
    return crumbs;
  }
  let current = root === "/" ? "" : root;
  for (const seg of path.slice(current.length + 1).split("/")) {
    current = `${current}/${seg}`;
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

const formatModTime = formatTime;

export function FileBrowser(props: FileBrowserProps) {
  const { i18n } = useTranslation();
  const mode = props.fetchInitialDir ? "initial" : `root:${props.rootPath ?? "/"}`;
  return <FileBrowserContent key={`${mode}:${i18n.language}`} {...props} />;
}

function FileBrowserContent({ fetchDir, fetchInitialDir, fetchContent, rootPath = "/", className }: FileBrowserProps) {
  const { t } = useTranslation();
  // Mode is fixed per mount (the wrapper key changes with it).
  const fixedRoot = fetchInitialDir ? null : rootPath.replace(/\/+$/, "") || "/";
  // null until the initial listing succeeds; root then stays fixed for this mount.
  const [location, setLocation] = useState<Location | null>(
    fixedRoot === null ? null : { root: fixedRoot, path: fixedRoot },
  );
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [previewOpen, setPreviewOpen] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const lastRequestRef = useRef<DirRequest | null>(null);
  const fetchDirRef = useRef(fetchDir);
  const fetchInitialDirRef = useRef(fetchInitialDir);

  useEffect(() => {
    fetchDirRef.current = fetchDir;
    fetchInitialDirRef.current = fetchInitialDir;
  }, [fetchDir, fetchInitialDir]);

  const loadDir = useCallback(
    (req: DirRequest) => {
      abortRef.current?.abort();
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      lastRequestRef.current = req;

      const pending = req.kind === "path"
        ? fetchDirRef.current(req.path, ctrl.signal)
        : fetchInitialDirRef.current?.(ctrl.signal) ?? Promise.reject(new Error(t('fileBrowser.loadDirFailed')));
      pending
        .then((result) => {
          if (ctrl.signal.aborted) return;
          if (!isCanonicalPath(result?.path) || !Array.isArray(result.entries)) {
            setError(t('fileBrowser.loadDirFailed'));
            setLoading(false);
            return;
          }
          setEntries(result.entries);
          setTruncated(result.truncated);
          // Only the initial listing may establish the root; explicit paths never widen it.
          setLocation((prev) =>
            req.kind === "initial" ? { root: result.path, path: result.path } : prev && { root: prev.root, path: result.path },
          );
          setLoading(false);
        })
        .catch((err: unknown) => {
          if (ctrl.signal.aborted) return;
          setError(err instanceof Error ? err.message : t('fileBrowser.loadDirFailed'));
          setLoading(false);
        });
    },
    [t]
  );

  useEffect(() => {
    loadDir(fixedRoot === null ? { kind: "initial" } : { kind: "path", path: fixedRoot });
    return () => abortRef.current?.abort();
  }, [fixedRoot, loadDir]);

  const startLoad = (req: DirRequest) => {
    setLoading(true);
    setError(null);
    loadDir(req);
  };

  const handleNavigate = (path: string) => startLoad({ kind: "path", path });

  const handleEntryClick = (entry: FileEntry) => {
    if (entry.isDir) {
      handleNavigate(entry.path);
    } else {
      setPreviewPath(entry.path);
      setPreviewOpen(true);
    }
  };

  const breadcrumbs = location ? parseBreadcrumbs(location, t('fileBrowser.rootDir')) : [];
  const parentPath = breadcrumbs.length > 1 ? breadcrumbs[breadcrumbs.length - 2].path : null;

  // 目录优先，文件其次，各自按名称排序
  const sorted = [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
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
            aria-label={t('fileBrowser.goUp')}
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
                title={crumb.path}
              >
                {crumb.label}
              </button>
            ) : (
              <span className="truncate max-w-[200px] font-medium" title={crumb.path}>{crumb.label}</span>
            )}
          </span>
        ))}
        <button
          type="button"
          className="ml-auto shrink-0 rounded p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
          // Before the first successful listing only the initial request is meaningful.
          onClick={() => startLoad(location === null ? { kind: "initial" } : { kind: "path", path: location.path })}
          aria-label={t('common.refresh')}
          disabled={loading}
        >
          <RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />
        </button>
      </div>

      {/* 截断提示 */}
      {truncated && (
        <InlineAlert tone="warning" className="mb-2">
          {t('fileBrowser.truncatedHint')}
        </InlineAlert>
      )}

      {/* 错误提示 */}
      {error && (
        <div className="flex items-center gap-2 rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertCircle className="size-4 shrink-0" />
          <span>{error}</span>
          <button
            type="button"
            className="ml-auto text-xs underline"
            onClick={() => { if (lastRequestRef.current) startLoad(lastRequestRef.current); }}
          >
            {t('common.retry')}
          </button>
        </div>
      )}

      {/* 文件列表 */}
      {!error && (
        <div className="glass-panel overflow-hidden rounded-lg relative">
          {loading && entries.length > 0 && (
            <div className="absolute inset-x-0 top-0 z-10 h-0.5 bg-primary/20">
              <div className="h-full w-2/5 animate-pulse bg-primary/60 rounded-full" />
            </div>
          )}
          {loading && entries.length === 0 ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              {t('common.loading')}
            </div>
          ) : sorted.length === 0 ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              {t('fileBrowser.emptyDir')}
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border/40 text-xs text-muted-foreground">
                  <th scope="col" className="px-4 py-2 text-left font-medium">{t('fileBrowser.colName')}</th>
                  <th scope="col" className="hidden px-4 py-2 text-right font-medium sm:table-cell">{t('fileBrowser.colSize')}</th>
                  <th scope="col" className="hidden px-4 py-2 text-left font-medium md:table-cell">{t('fileBrowser.colPermissions')}</th>
                  <th scope="col" className="hidden px-4 py-2 text-left font-medium lg:table-cell">{t('fileBrowser.colModTime')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/30">
                {sorted.map((entry) => (
                  <tr
                    key={entry.path}
                    className="group cursor-pointer transition-colors hover:bg-muted/40"
                    onClick={() => handleEntryClick(entry)}
                    tabIndex={0}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") handleEntryClick(entry);
                    }}
                    role="button"
                    aria-label={`${entry.isDir ? t('fileBrowser.enterDir') : t('fileBrowser.previewFile')} ${entry.name}`}
                  >
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        {entry.isDir ? (
                          <FolderOpen className="size-4 shrink-0 text-warning" />
                        ) : (
                          <File className="size-4 shrink-0 text-muted-foreground" />
                        )}
                        <span className={`truncate max-w-[240px] ${entry.isDir ? "font-medium" : ""}`}>
                          {entry.name}
                        </span>
                      </div>
                    </td>
                    <td className="hidden px-4 py-2 text-right tabular-nums text-muted-foreground sm:table-cell">
                      {formatSize(entry.size, entry.isDir)}
                    </td>
                    <td className="hidden px-4 py-2 font-mono text-xs text-muted-foreground md:table-cell">
                      {entry.mode}
                    </td>
                    <td className="hidden px-4 py-2 text-muted-foreground lg:table-cell">
                      {formatModTime(entry.modTime)}
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
          fetchContent={(signal) => fetchContent(previewPath, signal)}
        />
      )}
    </div>
  );
}

export { Folder };
