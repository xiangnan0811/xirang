import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate } from "react-router-dom";
import { Eye, RefreshCw, Search } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError, apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { CredentialAccessGrant, CredentialAccessGrantStatus } from "@/types/domain";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Pagination } from "@/components/ui/pagination";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast-sonner";

const pageSize = 30;

type TimeRange = "all" | "1h" | "24h" | "7d" | "30d";

const actionOptions = [
  "terminal.open",
  "config.import",
  "config.export",
  "snapshot.restore",
  "task.restore_trigger",
  "task.manual_trigger",
  "task.batch_trigger",
  "batch_command.create",
];

const purposeOptions = [
  "terminal",
  "config_import",
  "config_export",
  "snapshot",
  "task_restore",
  "task_command",
  "batch_command",
];

const statusOptions: CredentialAccessGrantStatus[] = ["requested", "approved", "active", "denied", "expired", "revoked"];

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
  return { from: from.toISOString(), to: now.toISOString() };
}

function positiveFilter(value: string): number | undefined {
  const parsed = Number(value.trim());
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

function grantStatusTone(status: CredentialAccessGrantStatus) {
  if (status === "active" || status === "approved") {
    return "success" as const;
  }
  if (status === "requested") {
    return "warning" as const;
  }
  if (status === "denied" || status === "revoked") {
    return "destructive" as const;
  }
  return "neutral" as const;
}

function resourceSummary(grant: CredentialAccessGrant): string {
  const parts = [];
  if (grant.nodeId) {
    parts.push(`node:${grant.nodeId}`);
  }
  if (grant.taskId) {
    parts.push(`task:${grant.taskId}`);
  }
  if (grant.policyId) {
    parts.push(`policy:${grant.policyId}`);
  }
  return parts.length ? parts.join(" · ") : "-";
}

export function CredentialAccessGrantsPage() {
  const { t } = useTranslation();
  const { token, role: authRole } = useAuth();
  const [rows, setRows] = useState<CredentialAccessGrant[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<CredentialAccessGrant | null>(null);
  const [requesterUsername, setRequesterUsername] = useState("");
  const [requesterRole, setRequesterRole] = useState("all");
  const [requesterUserId, setRequesterUserId] = useState("");
  const [action, setAction] = useState("all");
  const [purpose, setPurpose] = useState("all");
  const [status, setStatus] = useState("all");
  const [nodeId, setNodeId] = useState("");
  const [taskId, setTaskId] = useState("");
  const [policyId, setPolicyId] = useState("");
  const [timeRange, setTimeRange] = useState<TimeRange>("24h");
  const autoLoadKeyRef = useRef("");

  const stats = useMemo(() => {
    let active = 0;
    let requested = 0;
    let inactive = 0;
    for (const row of rows) {
      if (row.status === "active" || row.status === "approved") {
        active += 1;
      } else if (row.status === "requested") {
        requested += 1;
      } else {
        inactive += 1;
      }
    }
    return { active, requested, inactive };
  }, [rows]);

  const buildFilters = (nextPage: number) => {
    const { from, to } = resolveTimeRange(timeRange);
    return {
      requesterUsername: requesterUsername.trim() || undefined,
      requesterRole: requesterRole === "all" ? undefined : requesterRole,
      requesterUserId: positiveFilter(requesterUserId),
      action: action === "all" ? undefined : action,
      purpose: purpose === "all" ? undefined : purpose,
      status: status === "all" ? undefined : status,
      nodeId: positiveFilter(nodeId),
      taskId: positiveFilter(taskId),
      policyId: positiveFilter(policyId),
      from,
      to,
      page: nextPage,
      pageSize,
      sortBy: "created_at" as const,
      sortOrder: "desc" as const,
    };
  };

  const load = async (nextPage: number) => {
    if (!token) {
      return;
    }

    setLoading(true);
    try {
      const result = await apiClient.listCredentialAccessGrants(token, buildFilters(nextPage));
      setRows(result.items);
      setTotal(result.total);
      setPage(result.page);
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error(t("credentialGrants.errorForbidden"));
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (!token || authRole !== "admin") {
      autoLoadKeyRef.current = "";
      return;
    }

    const loadKey = [
      token,
      authRole,
      timeRange,
      requesterUsername.trim(),
      requesterRole,
      requesterUserId.trim(),
      action,
      purpose,
      status,
      nodeId.trim(),
      taskId.trim(),
      policyId.trim(),
    ].join(":");
    if (autoLoadKeyRef.current === loadKey) {
      return;
    }
    autoLoadKeyRef.current = loadKey;
    void load(1);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- load is intentionally excluded to prevent loop
  }, [action, authRole, nodeId, policyId, purpose, requesterRole, requesterUserId, requesterUsername, status, taskId, timeRange, token]);

  if (authRole !== "admin") {
    return <Navigate to="/app/overview" replace />;
  }

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div>
              <h1 className="text-lg font-semibold tracking-tight">{t("credentialGrants.pageTitle")}</h1>
              <p className="mt-1 text-sm text-muted-foreground">{t("credentialGrants.pageDescription")}</p>
            </div>
            <Button size="sm" variant="outline" onClick={() => void load(page)} disabled={loading}>
              <RefreshCw className="mr-1 size-3.5" aria-hidden />
              {t("common.refresh")}
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="success">{t("credentialGrants.statsActive", { count: stats.active })}</Badge>
            <Badge tone="warning">{t("credentialGrants.statsRequested", { count: stats.requested })}</Badge>
            <Badge tone="neutral">{t("credentialGrants.statsInactive", { count: stats.inactive })}</Badge>
            <Badge tone="neutral">{t("credentialGrants.total", { count: total })}</Badge>
          </div>

          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-2 xl:grid-cols-4">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input
                className="pl-9"
                placeholder={t("credentialGrants.requesterUsernamePlaceholder")}
                aria-label={t("credentialGrants.requesterUsernameAriaLabel")}
                value={requesterUsername}
                onChange={(event) => setRequesterUsername(event.target.value)}
              />
            </div>
            <Select value={requesterRole} onChange={(event) => setRequesterRole(event.target.value)} aria-label={t("credentialGrants.requesterRoleFilter")}>
              <option value="all">{t("credentialGrants.allRoles")}</option>
              <option value="admin">admin</option>
              <option value="operator">operator</option>
              <option value="viewer">viewer</option>
            </Select>
            <Input
              placeholder={t("credentialGrants.requesterUserIdPlaceholder")}
              aria-label={t("credentialGrants.requesterUserIdAriaLabel")}
              inputMode="numeric"
              value={requesterUserId}
              onChange={(event) => setRequesterUserId(event.target.value)}
            />
            <Select value={action} onChange={(event) => setAction(event.target.value)} aria-label={t("credentialGrants.actionFilter")}>
              <option value="all">{t("credentialGrants.allActions")}</option>
              {actionOptions.map((item) => <option key={item} value={item}>{item}</option>)}
            </Select>
            <Select value={purpose} onChange={(event) => setPurpose(event.target.value)} aria-label={t("credentialGrants.purposeFilter")}>
              <option value="all">{t("credentialGrants.allPurposes")}</option>
              {purposeOptions.map((item) => <option key={item} value={item}>{item}</option>)}
            </Select>
            <Select value={status} onChange={(event) => setStatus(event.target.value)} aria-label={t("credentialGrants.statusFilter")}>
              <option value="all">{t("credentialGrants.allStatuses")}</option>
              {statusOptions.map((item) => <option key={item} value={item}>{t(`credentialGrants.statuses.${item}`)}</option>)}
            </Select>
            <Input
              placeholder={t("credentialGrants.nodeIdPlaceholder")}
              aria-label={t("credentialGrants.nodeIdAriaLabel")}
              inputMode="numeric"
              value={nodeId}
              onChange={(event) => setNodeId(event.target.value)}
            />
            <Input
              placeholder={t("credentialGrants.taskIdPlaceholder")}
              aria-label={t("credentialGrants.taskIdAriaLabel")}
              inputMode="numeric"
              value={taskId}
              onChange={(event) => setTaskId(event.target.value)}
            />
            <Input
              placeholder={t("credentialGrants.policyIdPlaceholder")}
              aria-label={t("credentialGrants.policyIdAriaLabel")}
              inputMode="numeric"
              value={policyId}
              onChange={(event) => setPolicyId(event.target.value)}
            />
            <Button onClick={() => void load(1)} disabled={loading}>{t("credentialGrants.query")}</Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                { label: t("credentialGrants.timeRanges.all"), value: "all" as const },
                { label: t("credentialGrants.timeRanges.1h"), value: "1h" as const },
                { label: t("credentialGrants.timeRanges.24h"), value: "24h" as const },
                { label: t("credentialGrants.timeRanges.7d"), value: "7d" as const },
                { label: t("credentialGrants.timeRanges.30d"), value: "30d" as const },
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

          <div className="grid gap-3 sm:grid-cols-2 md:hidden lg:grid-cols-3">
            {rows.map((row) => (
              <div key={row.id} className="rounded-lg border border-border p-3 transition-colors hover:bg-accent">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-medium">{row.requesterUsername || "-"}</p>
                  <Badge tone={grantStatusTone(row.status)}>{t(`credentialGrants.statuses.${row.status}`)}</Badge>
                </div>
                <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                  <p>{t("credentialGrants.colCreatedAt")}：{row.createdAt || "-"}</p>
                  <p>{t("credentialGrants.colAction")}：<span className="font-mono">{row.action}</span></p>
                  <p>{t("credentialGrants.colPurpose")}：{row.purpose}</p>
                  <p>{t("credentialGrants.colResource")}：{resourceSummary(row)}</p>
                </div>
                <Button className="mt-3 w-full" size="sm" variant="outline" onClick={() => setSelected(row)}>
                  <Eye className="mr-1 size-3.5" aria-hidden />
                  {t("credentialGrants.viewDetails")}
                </Button>
              </div>
            ))}
            {!rows.length && !loading ? <EmptyState title={t("credentialGrants.emptyTitle")} /> : null}
          </div>

          <div className="hidden overflow-x-auto rounded-lg border border-border bg-card md:block">
            <table className="min-w-[1100px] text-left text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/35 text-mini uppercase tracking-wide text-muted-foreground">
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colCreatedAt")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colRequester")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colAction")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colPurpose")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colStatus")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colResource")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colExpiresAt")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialGrants.colDetails")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className="border-b border-border transition-colors duration-200 ease-out hover:bg-muted/40">
                    <td className="px-3 py-2.5">{row.createdAt || "-"}</td>
                    <td className="px-3 py-2.5">{row.requesterUsername || "-"}</td>
                    <td className="px-3 py-2.5 font-mono text-xs">{row.action}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{row.purpose}</td>
                    <td className="px-3 py-2.5"><Badge tone={grantStatusTone(row.status)}>{t(`credentialGrants.statuses.${row.status}`)}</Badge></td>
                    <td className="px-3 py-2.5 text-muted-foreground">{resourceSummary(row)}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{row.expiresAt || "-"}</td>
                    <td className="px-3 py-2.5">
                      <Button size="sm" variant="outline" onClick={() => setSelected(row)}>
                        <Eye className="mr-1 size-3.5" aria-hidden />
                        {t("credentialGrants.viewDetails")}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!rows.length && !loading ? <EmptyState className="rounded-none border-0 py-8" title={t("credentialGrants.emptyTitle")} /> : null}
          </div>

          <Pagination page={page} pageSize={pageSize} total={total} loading={loading} onPageChange={(p) => void load(p)} />
        </CardContent>
      </Card>

      <Dialog open={Boolean(selected)} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t("credentialGrants.detailTitle")}</DialogTitle>
            <DialogDescription>{t("credentialGrants.detailDescription")}</DialogDescription>
          </DialogHeader>
          {selected ? (
            <DialogBody>
              <dl className="grid gap-2 text-sm sm:grid-cols-2">
                <Detail label={t("credentialGrants.colGrantId")} value={selected.id || "-"} />
                <Detail label={t("credentialGrants.colRequesterUserId")} value={selected.requesterUserId || "-"} />
                <Detail label={t("credentialGrants.colRequester")} value={selected.requesterUsername || "-"} />
                <Detail label={t("credentialGrants.colRole")} value={selected.requesterRole || "-"} />
                <Detail label={t("credentialGrants.colAction")} value={selected.action} mono />
                <Detail label={t("credentialGrants.colPurpose")} value={selected.purpose} />
                <Detail label={t("credentialGrants.colStatus")} value={t(`credentialGrants.statuses.${selected.status}`)} />
                <Detail label={t("credentialGrants.colRequestedTtl")} value={selected.requestedTtlSeconds || "-"} />
                <Detail label={t("credentialGrants.colNodeId")} value={selected.nodeId ?? "-"} />
                <Detail label={t("credentialGrants.colTaskId")} value={selected.taskId ?? "-"} />
                <Detail label={t("credentialGrants.colPolicyId")} value={selected.policyId ?? "-"} />
                <Detail label={t("credentialGrants.colRequestedAt")} value={selected.requestedAt || "-"} />
                <Detail label={t("credentialGrants.colApprovedAt")} value={selected.approvedAt || "-"} />
                <Detail label={t("credentialGrants.colApproverUserId")} value={selected.approverUserId ?? "-"} />
                <Detail label={t("credentialGrants.colApprover")} value={selected.approverUsername || "-"} />
                <Detail label={t("credentialGrants.colExpiresAt")} value={selected.expiresAt || "-"} />
                <Detail label={t("credentialGrants.colRevokedAt")} value={selected.revokedAt || "-"} />
                <Detail label={t("credentialGrants.colRevokedByUserId")} value={selected.revokedByUserId ?? "-"} />
                <Detail label={t("credentialGrants.colCreatedAt")} value={selected.createdAt || "-"} />
                <Detail label={t("credentialGrants.colUpdatedAt")} value={selected.updatedAt || "-"} />
                <Detail label={t("credentialGrants.colReason")} value={selected.reason || "-"} mono />
              </dl>
            </DialogBody>
          ) : null}
          <DialogFooter>
            <Button variant="outline" onClick={() => setSelected(null)}>{t("common.close")}</Button>
          </DialogFooter>
          <DialogCloseButton />
        </DialogContent>
      </Dialog>
    </div>
  );
}

function Detail({ label, value, mono }: { label: string; value: string | number; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={mono ? "break-all font-mono text-xs" : "break-all"}>{value}</dd>
    </div>
  );
}
