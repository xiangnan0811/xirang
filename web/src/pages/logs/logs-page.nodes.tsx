import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { useNodesContext } from "@/context/nodes-context";
import { queryNodeLogs, type NodeLogQuery } from "@/lib/api/node-logs";
import type { NodeLogEntry, NodeLogPriority, NodeLogQueryResult, NodeLogSource } from "@/types/domain";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";

const PRIORITIES: NodeLogPriority[] = ["emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"];

const PRIORITY_COLORS: Record<NodeLogPriority, string> = {
  emerg: "bg-destructive text-destructive-foreground",
  alert: "bg-destructive/80 text-destructive-foreground",
  crit: "bg-destructive/70 text-destructive-foreground",
  err: "bg-destructive/60 text-destructive-foreground",
  warning: "bg-warning text-warning-foreground",
  notice: "bg-info/70 text-info-foreground",
  info: "bg-info text-info-foreground",
  debug: "bg-muted text-muted-foreground",
  "": "bg-muted text-muted-foreground",
};

function nowMinus(hours: number): string {
  return new Date(Date.now() - hours * 3600_000).toISOString().slice(0, 16);
}

function nowISO(): string {
  return new Date().toISOString().slice(0, 16);
}

type TimePreset = "1h" | "6h" | "1d" | "custom";

interface FilterState {
  nodeIds: number[];
  sources: NodeLogSource[];
  path: string;
  priorities: NodeLogPriority[];
  timePreset: TimePreset;
  start: string;
  end: string;
  keyword: string;
  page: number;
}

const DEFAULT_PAGE_SIZE = 50;

function initialFilter(): FilterState {
  return {
    nodeIds: [],
    sources: [],
    path: "",
    priorities: [],
    timePreset: "1h",
    start: nowMinus(1),
    end: nowISO(),
    keyword: "",
    page: 1,
  };
}

function filterToQuery(f: FilterState): NodeLogQuery {
  const q: NodeLogQuery = { page: f.page, page_size: DEFAULT_PAGE_SIZE };
  if (f.nodeIds.length > 0) q.node_ids = f.nodeIds;
  if (f.sources.length > 0) q.source = f.sources;
  if (f.path.trim()) q.path = f.path.trim();
  if (f.priorities.length > 0) q.priority = f.priorities;
  if (f.timePreset !== "custom") {
    const hours = f.timePreset === "1h" ? 1 : f.timePreset === "6h" ? 6 : 24;
    q.start = new Date(Date.now() - hours * 3600_000).toISOString();
    q.end = new Date().toISOString();
  } else {
    if (f.start) q.start = new Date(f.start).toISOString();
    if (f.end) q.end = new Date(f.end).toISOString();
  }
  if (f.keyword.trim()) q.q = f.keyword.trim();
  return q;
}

export function NodeLogsPanel() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { nodes } = useNodesContext();

  const [filter, setFilter] = useState<FilterState>(initialFilter);
  const [pendingFilter, setPendingFilter] = useState<FilterState>(initialFilter);
  const [result, setResult] = useState<NodeLogQueryResult | null>(null);
  const [loading, setLoading] = useState(false);

  const applyFilter = async (f: FilterState) => {
    if (!token) return;
    setFilter(f);
    setLoading(true);
    try {
      const data = await queryNodeLogs(token, filterToQuery(f));
      setResult(data);
    } catch (err: unknown) {
      toast.error(getErrorMessage(err));
    } finally {
      setLoading(false);
    }
  };

  const handleApply = () => {
    void applyFilter({ ...pendingFilter, page: 1 });
  };

  const handleReset = () => {
    const fresh = initialFilter();
    setPendingFilter(fresh);
    setResult(null);
    setFilter(fresh);
  };

  const handlePageChange = (newPage: number) => {
    const next = { ...filter, page: newPage };
    void applyFilter(next);
  };

  const toggleNodeId = (id: number) => {
    setPendingFilter((prev) => ({
      ...prev,
      nodeIds: prev.nodeIds.includes(id)
        ? prev.nodeIds.filter((n) => n !== id)
        : [...prev.nodeIds, id],
    }));
  };

  const toggleSource = (src: NodeLogSource) => {
    setPendingFilter((prev) => ({
      ...prev,
      sources: prev.sources.includes(src)
        ? prev.sources.filter((s) => s !== src)
        : [...prev.sources, src],
    }));
  };

  const togglePriority = (p: NodeLogPriority) => {
    setPendingFilter((prev) => ({
      ...prev,
      priorities: prev.priorities.includes(p)
        ? prev.priorities.filter((x) => x !== p)
        : [...prev.priorities, p],
    }));
  };

  const setPreset = (preset: TimePreset) => {
    if (preset === "custom") {
      setPendingFilter((prev) => ({ ...prev, timePreset: "custom" }));
    } else {
      const hours = preset === "1h" ? 1 : preset === "6h" ? 6 : 24;
      setPendingFilter((prev) => ({
        ...prev,
        timePreset: preset,
        start: nowMinus(hours),
        end: nowISO(),
      }));
    }
  };

  const totalPages = result
    ? Math.max(1, Math.ceil(result.total / DEFAULT_PAGE_SIZE))
    : 1;

  return (
    <div className="space-y-4">
      {/* Filter row */}
      <div className="rounded-lg border border-border bg-card p-4 space-y-3">
        {/* Row 1: nodes + source + path */}
        <div className="flex flex-wrap gap-4 items-start">
          {/* Node multi-select */}
          <div className="space-y-1 min-w-[160px]">
            <p className="text-xs font-medium text-muted-foreground">{t("nodeLogs.filters.nodes")}</p>
            <div className="flex flex-wrap gap-1 max-h-20 overflow-y-auto">
              {nodes.map((node) => (
                <button
                  key={node.id}
                  type="button"
                  onClick={() => toggleNodeId(node.id)}
                  className={`rounded-full px-2 py-0.5 text-xs border transition-colors ${
                    pendingFilter.nodeIds.includes(node.id)
                      ? "bg-primary text-primary-foreground border-primary"
                      : "border-border text-muted-foreground hover:border-primary/50"
                  }`}
                  aria-pressed={pendingFilter.nodeIds.includes(node.id)}
                >
                  {node.name}
                </button>
              ))}
              {nodes.length === 0 && (
                <span className="text-xs text-muted-foreground">—</span>
              )}
            </div>
          </div>

          {/* Source checkboxes */}
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{t("nodeLogs.filters.source")}</p>
            <div className="flex gap-3">
              {(["journalctl", "file"] as NodeLogSource[]).map((src) => (
                <label key={src} className="flex items-center gap-1.5 cursor-pointer text-sm">
                  <Checkbox
                    checked={pendingFilter.sources.includes(src)}
                    onCheckedChange={() => toggleSource(src)}
                    aria-label={t(`nodeLogs.source.${src}`)}
                  />
                  {t(`nodeLogs.source.${src}`)}
                </label>
              ))}
            </div>
          </div>

          {/* Path */}
          <div className="space-y-1 flex-1 min-w-[160px]">
            <p className="text-xs font-medium text-muted-foreground">{t("nodeLogs.filters.path")}</p>
            <Input
              value={pendingFilter.path}
              onChange={(e) => setPendingFilter((prev) => ({ ...prev, path: e.target.value }))}
              placeholder="/var/log/syslog"
              className="h-8 text-sm"
              aria-label={t("nodeLogs.filters.path")}
            />
          </div>
        </div>

        {/* Row 2: priority + time range + keyword */}
        <div className="flex flex-wrap gap-4 items-start">
          {/* Priority checkboxes */}
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{t("nodeLogs.filters.priority")}</p>
            <div className="flex flex-wrap gap-x-3 gap-y-1">
              {PRIORITIES.map((p) => (
                <label key={p} className="flex items-center gap-1.5 cursor-pointer text-sm">
                  <Checkbox
                    checked={pendingFilter.priorities.includes(p)}
                    onCheckedChange={() => togglePriority(p)}
                    aria-label={t(`nodeLogs.priority.${p}`)}
                  />
                  {t(`nodeLogs.priority.${p}`)}
                </label>
              ))}
            </div>
          </div>

          {/* Time range */}
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{t("nodeLogs.filters.timeRange")}</p>
            <div className="flex flex-wrap gap-2 items-center">
              {(["1h", "6h", "1d", "custom"] as TimePreset[]).map((preset) => (
                <Button
                  key={preset}
                  variant={pendingFilter.timePreset === preset ? "default" : "outline"}
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={() => setPreset(preset)}
                  aria-pressed={pendingFilter.timePreset === preset}
                >
                  {t(`nodeLogs.preset.${preset === "1h" ? "oneHour" : preset === "6h" ? "sixHour" : preset === "1d" ? "oneDay" : "custom"}`)}
                </Button>
              ))}
              {pendingFilter.timePreset === "custom" && (
                <div className="flex gap-2 items-center">
                  <input
                    type="datetime-local"
                    value={pendingFilter.start}
                    onChange={(e) => setPendingFilter((prev) => ({ ...prev, start: e.target.value }))}
                    className="h-7 rounded-md border border-input bg-background px-2 text-xs"
                    aria-label="开始时间"
                  />
                  <span className="text-muted-foreground text-xs">—</span>
                  <input
                    type="datetime-local"
                    value={pendingFilter.end}
                    onChange={(e) => setPendingFilter((prev) => ({ ...prev, end: e.target.value }))}
                    className="h-7 rounded-md border border-input bg-background px-2 text-xs"
                    aria-label="结束时间"
                  />
                </div>
              )}
            </div>
          </div>

          {/* Keyword */}
          <div className="space-y-1 flex-1 min-w-[180px]">
            <p className="text-xs font-medium text-muted-foreground">
              {t("nodeLogs.filters.keyword")}
              <span className="ml-1.5 font-normal opacity-70">{t("nodeLogs.filters.keywordHint")}</span>
            </p>
            <Input
              value={pendingFilter.keyword}
              onChange={(e) => setPendingFilter((prev) => ({ ...prev, keyword: e.target.value }))}
              onKeyDown={(e) => { if (e.key === "Enter") handleApply(); }}
              placeholder="error"
              className="h-8 text-sm"
              aria-label={t("nodeLogs.filters.keyword")}
            />
          </div>
        </div>

        {/* Actions */}
        <div className="flex gap-2">
          <Button size="sm" onClick={handleApply} disabled={loading} aria-label={t("nodeLogs.filters.apply")}>
            {loading ? t("nodeLogs.loading") : t("nodeLogs.filters.apply")}
          </Button>
          <Button size="sm" variant="outline" onClick={handleReset} aria-label={t("nodeLogs.filters.reset")}>
            {t("nodeLogs.filters.reset")}
          </Button>
        </div>
      </div>

      {/* Results */}
      {result !== null && (
        <div className="space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground px-1">
            <span>{t("nodeLogs.total", { total: result.total })}</span>
            <span>{t("nodeLogs.page", { page: filter.page })} / {totalPages}</span>
          </div>

          {result.data.length === 0 ? (
            <div className="rounded-lg border border-border bg-card p-8 text-center text-sm text-muted-foreground">
              {t("nodeLogs.empty")}
            </div>
          ) : (
            <NodeLogsTable entries={result.data} nodes={nodes} />
          )}

          {/* Pagination */}
          {(result.total > DEFAULT_PAGE_SIZE || filter.page > 1) && (
            <div className="flex items-center justify-between px-1">
              <span className="text-xs text-muted-foreground">
                {t("nodeLogs.perPage", { size: DEFAULT_PAGE_SIZE })}
              </span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={filter.page <= 1 || loading}
                  onClick={() => handlePageChange(filter.page - 1)}
                  aria-label="上一页"
                >
                  ‹
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!result.has_more || loading}
                  onClick={() => handlePageChange(filter.page + 1)}
                  aria-label="下一页"
                >
                  ›
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function NodeLogsTable({
  entries,
  nodes,
}: {
  entries: NodeLogEntry[];
  nodes: { id: number; name: string }[];
}) {
  const { t } = useTranslation();
  const nodeNameMap = new Map(nodes.map((n) => [n.id, n.name]));

  return (
    <div className="rounded-lg border border-border bg-card overflow-x-auto">
      <table className="min-w-[900px] w-full text-left text-xs">
        <thead>
          <tr className="border-b border-border bg-muted/35 text-mini uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-3 py-2.5 w-[160px]">{t("nodeLogs.columns.time")}</th>
            <th scope="col" className="px-3 py-2.5 w-[110px]">{t("nodeLogs.columns.node")}</th>
            <th scope="col" className="px-3 py-2.5 w-[160px]">{t("nodeLogs.columns.path")}</th>
            <th scope="col" className="px-3 py-2.5 w-[90px]">{t("nodeLogs.columns.priority")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodeLogs.columns.message")}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => {
            const localTime = (() => {
              try {
                return new Date(entry.timestamp).toLocaleString();
              } catch {
                return entry.timestamp;
              }
            })();
            const nodeName = nodeNameMap.get(entry.node_id) ?? String(entry.node_id);
            const pathShort = entry.path.length > 28
              ? "…" + entry.path.slice(-26)
              : entry.path;
            const priorityClass = PRIORITY_COLORS[entry.priority] ?? PRIORITY_COLORS[""];
            return (
              <tr key={entry.id} className="border-b border-border/60 hover:bg-muted/30 transition-colors">
                <td className="px-3 py-2 tabular-nums text-muted-foreground whitespace-nowrap">{localTime}</td>
                <td className="px-3 py-2 font-medium">{nodeName}</td>
                <td className="px-3 py-2">
                  <span title={entry.path} className="text-muted-foreground">{pathShort}</span>
                </td>
                <td className="px-3 py-2">
                  {entry.priority ? (
                    <span className={`inline-block rounded px-1.5 py-0.5 text-micro font-medium ${priorityClass}`}>
                      {entry.priority}
                    </span>
                  ) : null}
                </td>
                <td className="px-3 py-2 font-mono text-mini break-all">{entry.message}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
