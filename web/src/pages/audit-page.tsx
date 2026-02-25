import { useEffect, useMemo, useRef, useState } from "react";
import { Download, RefreshCw, Search } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { ApiError, apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { AuditLogRecord } from "@/types/domain";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";
import { ViewModeToggle, type ViewMode } from "@/components/ui/view-mode-toggle";

const pageSize = 30;
const auditViewStorageKey = "xirang.audit.view";

type TimeRange = "all" | "1h" | "24h" | "7d" | "30d";

function methodBadge(method: string) {
  const normalized = method.toUpperCase();
  if (normalized === "DELETE") {
    return "danger" as const;
  }
  if (normalized === "POST" || normalized === "PUT" || normalized === "PATCH") {
    return "warning" as const;
  }
  return "outline" as const;
}

function resolveTimeRange(range: TimeRange): { from?: string; to?: string } {
  if (range === "all") {
    return {};
  }

  const now = new Date();
  const from = new Date(now);

  if (range === "1h") {
    from.setHours(from.getHours() - 1);
  } else if (range === "24h") {
    from.setHours(from.getHours() - 24);
  } else if (range === "7d") {
    from.setDate(from.getDate() - 7);
  } else if (range === "30d") {
    from.setDate(from.getDate() - 30);
  }

  return {
    from: from.toISOString(),
    to: now.toISOString()
  };
}

export function AuditPage() {
  const { token } = useAuth();
  const [rows, setRows] = useState<AuditLogRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [keyword, setKeyword] = useState("");
  const [method, setMethod] = useState("all");
  const [timeRange, setTimeRange] = useState<TimeRange>("24h");
  const [viewMode, setViewMode] = useState<ViewMode>(() => {
    const stored = localStorage.getItem(auditViewStorageKey);
    return stored === "list" ? "list" : "cards";
  });

  const autoLoadKeyRef = useRef("");

  const pageIndex = useMemo(() => Math.floor(offset / pageSize) + 1, [offset]);
  const hasNext = offset + pageSize < total;
  const auditStats = useMemo(() => {
    let writeOps = 0;
    let readOps = 0;
    let errorStatus = 0;
    for (const row of rows) {
      const methodValue = row.method.toUpperCase();
      if (methodValue === "POST" || methodValue === "PUT" || methodValue === "PATCH" || methodValue === "DELETE") {
        writeOps += 1;
      } else {
        readOps += 1;
      }
      if (row.statusCode >= 400) {
        errorStatus += 1;
      }
    }
    return { writeOps, readOps, errorStatus };
  }, [rows]);

  const load = async (nextOffset: number) => {
    if (!token) {
      toast.error("请先登录后查看审计日志。");
      return;
    }

    const { from, to } = resolveTimeRange(timeRange);

    setLoading(true);
    try {
      const result = await apiClient.getAuditLogs(token, {
        path: keyword.trim() || undefined,
        method: method === "all" ? undefined : method,
        from,
        to,
        limit: pageSize,
        offset: nextOffset
      });
      setRows(result.items);
      setTotal(result.total);
      setOffset(result.offset);
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error("当前账号无权访问审计日志（仅管理员可读）。");
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  };

  const exportCSV = async () => {
    if (!token) {
      toast.error("请先登录后导出审计日志。");
      return;
    }

    const { from, to } = resolveTimeRange(timeRange);

    setExporting(true);
    try {
      const blob = await apiClient.exportAuditLogsCSV(token, {
        path: keyword.trim() || undefined,
        method: method === "all" ? undefined : method,
        from,
        to,
        limit: 5000
      });

      const link = document.createElement("a");
      const url = URL.createObjectURL(blob);
      link.href = url;
      link.download = `audit-logs-${new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-")}.csv`;
      link.click();
      URL.revokeObjectURL(url);

      toast.success("审计日志 CSV 导出成功。");
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error("当前账号无权导出审计日志（仅管理员可读）。");
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setExporting(false);
    }
  };

  useEffect(() => {
    if (!token) {
      autoLoadKeyRef.current = "";
      return;
    }

    const loadKey = `${token}:${timeRange}:${keyword.trim()}:${method}`;
    if (autoLoadKeyRef.current === loadKey) {
      return;
    }
    autoLoadKeyRef.current = loadKey;
    void load(0);
  }, [keyword, method, timeRange, token]);

  useEffect(() => {
    localStorage.setItem(auditViewStorageKey, viewMode);
  }, [viewMode]);

  return (
    <div className="space-y-5 animate-fade-in">
      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">审计日志（管理员只读）</CardTitle>
              <p className="mt-1 text-xs text-muted-foreground">支持卡片/列表切换与多维筛选</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">读操作 {auditStats.readOps}</Badge>
              <Badge variant="warning">写操作 {auditStats.writeOps}</Badge>
              <Badge variant="danger">异常状态 {auditStats.errorStatus}</Badge>
              <Badge variant="secondary">总计 {total}</Badge>
              <Button size="sm" variant="outline" onClick={() => void load(offset)} disabled={loading}>
                <RefreshCw className="mr-1 size-4" />
                刷新
              </Button>
              <Button size="sm" variant="outline" onClick={() => void exportCSV()} disabled={exporting || loading}>
                <Download className="mr-1 size-4" />
                {exporting ? "导出中..." : "导出 CSV"}
              </Button>
              <ViewModeToggle
                className="bg-background/70"
                value={viewMode}
                onChange={setViewMode}
                groupLabel="审计视图切换"
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-[1fr_auto] lg:grid-cols-[1fr_auto_auto]">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-9"
                placeholder="按路径关键字过滤，例如 /nodes /policies"
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
              />
            </div>
            <AppSelect
              value={method}
              onChange={(event) => setMethod(event.target.value)}
            >
              <option value="all">全部方法</option>
              <option value="GET">GET</option>
              <option value="POST">POST</option>
              <option value="PUT">PUT</option>
              <option value="PATCH">PATCH</option>
              <option value="DELETE">DELETE</option>
            </AppSelect>
            <Button className="md:col-span-2 lg:col-span-1" onClick={() => void load(0)} disabled={loading}>
              查询
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {[
              { label: "全部", value: "all" as const },
              { label: "近 1 小时", value: "1h" as const },
              { label: "近 24 小时", value: "24h" as const },
              { label: "近 7 天", value: "7d" as const },
              { label: "近 30 天", value: "30d" as const }
            ].map((item) => (
              <Button
                key={item.value}
                size="sm"
                variant={timeRange === item.value ? "default" : "outline"}
                onClick={() => setTimeRange(item.value)}
              >
                {item.label}
              </Button>
            ))}
          </div>

          {viewMode === "cards" ? (
            <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
              {rows.map((row) => (
                <div
                  key={row.id}
                  className="interactive-surface p-3"
                >
                  <div className="flex items-center justify-between gap-2">
                    <p className="font-medium">{row.username || "-"}</p>
                    <Badge variant={methodBadge(row.method)}>{row.method}</Badge>
                  </div>
                  <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                    <p>时间：{row.createdAt}</p>
                    <p>角色：{row.role || "-"}</p>
                    <p>路径：<span className="break-all font-mono">{row.path}</span></p>
                    <p>状态码：{row.statusCode}</p>
                    <p>来源 IP：{row.clientIP}</p>
                  </div>
                </div>
              ))}
              {!rows.length && !loading ? (
                <EmptyState title="当前筛选条件下没有审计记录。" />
              ) : null}
            </div>
          ) : (
            <div className="overflow-x-auto rounded-2xl border border-border/70 bg-background/55 shadow-sm">
              <table className="min-w-[1080px] text-left text-sm">
                <thead>
                  <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
                    <th className="px-3 py-2.5">时间</th>
                    <th className="px-3 py-2.5">用户</th>
                    <th className="px-3 py-2.5">角色</th>
                    <th className="px-3 py-2.5">方法</th>
                    <th className="px-3 py-2.5">路径</th>
                    <th className="px-3 py-2.5">状态码</th>
                    <th className="px-3 py-2.5">来源 IP</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.id} className="border-b border-border/60 transition-colors hover:bg-accent/35">
                      <td className="px-3 py-2.5">{row.createdAt}</td>
                      <td className="px-3 py-2.5">{row.username || "-"}</td>
                      <td className="px-3 py-2.5">{row.role || "-"}</td>
                      <td className="px-3 py-2.5">
                        <Badge variant={methodBadge(row.method)}>{row.method}</Badge>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-xs text-muted-foreground">{row.path}</td>
                      <td className="px-3 py-2.5">{row.statusCode}</td>
                      <td className="px-3 py-2.5 text-muted-foreground">{row.clientIP}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!rows.length && !loading ? (
                <div className="px-3 py-4 text-sm text-muted-foreground">当前筛选条件下没有审计记录。</div>
              ) : null}
            </div>
          )}

          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span>
              第 {pageIndex} 页 · 共 {total} 条
            </span>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void load(Math.max(0, offset - pageSize))}
                disabled={loading || offset === 0}
              >
                上一页
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void load(offset + pageSize)}
                disabled={loading || !hasNext}
              >
                下一页
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

    </div>
  );
}
