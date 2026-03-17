import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Download, RefreshCw, Search } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { ApiError, apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { AuditLogRecord } from "@/types/domain";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";

const pageSize = 30;

type TimeRange = "all" | "1h" | "24h" | "7d" | "30d";

function methodBadge(method: string) {
  const normalized = method.toUpperCase();
  if (normalized === "DELETE") {
    return "danger" as const;
  }
  if (
    normalized === "POST" ||
    normalized === "PUT" ||
    normalized === "PATCH"
  ) {
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
    to: now.toISOString(),
  };
}

export function AuditPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [rows, setRows] = useState<AuditLogRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [keyword, setKeyword] = useState("");
  const [method, setMethod] = useState("all");
  const [timeRange, setTimeRange] = useState<TimeRange>("24h");

  const autoLoadKeyRef = useRef("");

  const pageIndex = page;
  const hasNext = page * pageSize < total;
  const auditStats = useMemo(() => {
    let writeOps = 0;
    let readOps = 0;
    let errorStatus = 0;
    for (const row of rows) {
      const methodValue = row.method.toUpperCase();
      if (
        methodValue === "POST" ||
        methodValue === "PUT" ||
        methodValue === "PATCH" ||
        methodValue === "DELETE"
      ) {
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

  const load = async (nextPage: number) => {
    if (!token) {
      toast.error(t("audit.errorNotLoggedIn"));
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
        pageSize,
        page: nextPage,
      });
      setRows(result.items);
      setTotal(result.total);
      setPage(result.page);
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error(t("audit.errorForbidden"));
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  };

  const exportCSV = async () => {
    if (!token) {
      toast.error(t("audit.errorExportNotLoggedIn"));
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
        pageSize: 5000,
      });

      const link = document.createElement("a");
      const url = URL.createObjectURL(blob);
      link.href = url;
      link.download = `audit-logs-${new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-")}.csv`;
      link.click();
      URL.revokeObjectURL(url);

      toast.success(t("audit.exportSuccess"));
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error(t("audit.errorExportForbidden"));
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
    void load(1);
  }, [keyword, method, timeRange, token]);

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="border-border/75">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void load(page)}
                disabled={loading}
              >
                <RefreshCw className="mr-1 size-3.5" />
                {t("common.refresh")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void exportCSV()}
                disabled={exporting || loading}
              >
                <Download className="mr-1 size-3.5" />
                {exporting ? t("audit.exporting") : t("audit.exportCSV")}
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">
                {t("audit.readOps", { count: auditStats.readOps })}
              </Badge>
              <Badge variant="warning">
                {t("audit.writeOps", { count: auditStats.writeOps })}
              </Badge>
              <Badge variant="danger">
                {t("audit.errorStatus", { count: auditStats.errorStatus })}
              </Badge>
              <Badge variant="secondary">
                {t("audit.total", { count: total })}
              </Badge>
            </div>
          </div>
          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-[1fr_auto] lg:grid-cols-[1fr_auto_auto]">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-9"
                placeholder={t("audit.pathFilterPlaceholder")}
                aria-label={t("audit.pathFilterAriaLabel")}
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
              />
            </div>
            <AppSelect
              value={method}
              onChange={(event) => setMethod(event.target.value)}
            >
              <option value="all">{t("audit.allMethods")}</option>
              <option value="GET">GET</option>
              <option value="POST">POST</option>
              <option value="PUT">PUT</option>
              <option value="PATCH">PATCH</option>
              <option value="DELETE">DELETE</option>
            </AppSelect>
            <Button
              className="md:col-span-2 lg:col-span-1"
              onClick={() => void load(1)}
              disabled={loading}
            >
              {t("audit.query")}
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                { label: t("audit.timeRanges.all"), value: "all" as const },
                { label: t("audit.timeRanges.1h"), value: "1h" as const },
                { label: t("audit.timeRanges.24h"), value: "24h" as const },
                { label: t("audit.timeRanges.7d"), value: "7d" as const },
                { label: t("audit.timeRanges.30d"), value: "30d" as const },
              ] as const
            ).map((item) => (
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

          {/* 小屏卡片，大屏表格 */}
          <div className="grid gap-3 sm:grid-cols-2 md:hidden lg:grid-cols-3">
            {rows.map((row) => (
              <div key={row.id} className="interactive-surface p-3">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-medium">{row.username || "-"}</p>
                  <Badge variant={methodBadge(row.method)}>{row.method}</Badge>
                </div>
                <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                  <p>
                    {t("audit.colTime")}：{row.createdAt}
                  </p>
                  <p>
                    {t("audit.colRole")}：{row.role || "-"}
                  </p>
                  <p>
                    {t("audit.colPath")}：
                    <span className="break-all font-mono">{row.path}</span>
                  </p>
                  <p>
                    {t("audit.colStatusCode")}：{row.statusCode}
                  </p>
                  <p>
                    {t("audit.colClientIP")}：{row.clientIP}
                  </p>
                </div>
              </div>
            ))}
            {!rows.length && !loading ? (
              <EmptyState title={t("audit.emptyTitle")} />
            ) : null}
          </div>

          <div className="glass-panel hidden overflow-x-auto md:block">
            <table className="min-w-[1080px] text-left text-sm">
              <thead>
                <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
                  <th className="px-3 py-2.5">{t("audit.colTime")}</th>
                  <th className="px-3 py-2.5">{t("audit.colUser")}</th>
                  <th className="px-3 py-2.5">{t("audit.colRole")}</th>
                  <th className="px-3 py-2.5">{t("audit.colMethod")}</th>
                  <th className="px-3 py-2.5">{t("audit.colPath")}</th>
                  <th className="px-3 py-2.5">{t("audit.colStatusCode")}</th>
                  <th className="px-3 py-2.5">{t("audit.colClientIP")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr
                    key={row.id}
                    className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-accent/35"
                  >
                    <td className="px-3 py-2.5">{row.createdAt}</td>
                    <td className="px-3 py-2.5">{row.username || "-"}</td>
                    <td className="px-3 py-2.5">{row.role || "-"}</td>
                    <td className="px-3 py-2.5">
                      <Badge variant={methodBadge(row.method)}>
                        {row.method}
                      </Badge>
                    </td>
                    <td className="px-3 py-2.5 font-mono text-xs text-muted-foreground">
                      {row.path}
                    </td>
                    <td className="px-3 py-2.5">{row.statusCode}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">
                      {row.clientIP}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!rows.length && !loading ? (
              <div className="px-3 py-4 text-sm text-muted-foreground">
                {t("audit.emptyTitle")}
              </div>
            ) : null}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <span>{t("common.pageInfo", { page: pageIndex, total })}</span>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => void load(Math.max(1, page - 1))}
                disabled={loading || page <= 1}
              >
                {t("common.prevPage")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void load(page + 1)}
                disabled={loading || !hasNext}
              >
                {t("common.nextPage")}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
