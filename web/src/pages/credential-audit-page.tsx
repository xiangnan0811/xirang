import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate } from "react-router-dom";
import { Download, Eye, RefreshCw, Search } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError, apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { CredentialAuditEventRecord, CredentialAuditMetadataValue, CredentialAuditOutcome } from "@/types/domain";
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
  "ssh_key.test_connection",
  "ssh_key.export",
  "node.credential.test_connection",
  "auth.step_up",
  "terminal.open",
  "terminal.failure",
  "terminal.close",
  "task.manual_trigger",
  "task.restore_trigger",
  "task.batch_trigger",
  "snapshot.restore",
  "batch_command.create",
  "task.credential.use",
  "drill.trigger",
  "drill.phase",
  "file_browser.list",
  "file_browser.preview",
  "docker_volumes.discover",
  "config.export",
  "node.doctor.run",
  "node_migration.preflight",
  "probe.ssh",
  "probe.metrics",
  "node_logs.collect",
];

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

function outcomeTone(outcome: CredentialAuditOutcome) {
  if (outcome === "success") {
    return "success" as const;
  }
  if (outcome === "blocked") {
    return "warning" as const;
  }
  if (outcome === "failure") {
    return "destructive" as const;
  }
  return "neutral" as const;
}

function renderMetadataValue(value: CredentialAuditMetadataValue): string {
  if (Array.isArray(value)) {
    return value.join(", ");
  }
  return String(value);
}

function positiveFilter(value: string): number | undefined {
  const parsed = Number(value.trim());
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

export function CredentialAuditPage() {
  const { t } = useTranslation();
  const { token, role: authRole } = useAuth();
  const [rows, setRows] = useState<CredentialAuditEventRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [selected, setSelected] = useState<CredentialAuditEventRecord | null>(null);
  const [username, setUsername] = useState("");
  const [role, setRole] = useState("all");
  const [userId, setUserId] = useState("");
  const [action, setAction] = useState("all");
  const [purpose, setPurpose] = useState("");
  const [credentialKind, setCredentialKind] = useState("all");
  const [credentialSource, setCredentialSource] = useState("");
  const [outcome, setOutcome] = useState("all");
  const [nodeId, setNodeId] = useState("");
  const [sshKeyId, setSSHKeyId] = useState("");
  const [taskId, setTaskId] = useState("");
  const [taskRunId, setTaskRunId] = useState("");
  const [policyId, setPolicyId] = useState("");
  const [timeRange, setTimeRange] = useState<TimeRange>("24h");
  const autoLoadKeyRef = useRef("");

  const stats = useMemo(() => {
    let success = 0;
    let blocked = 0;
    let failure = 0;
    for (const row of rows) {
      if (row.outcome === "success") {
        success += 1;
      } else if (row.outcome === "blocked") {
        blocked += 1;
      } else if (row.outcome === "failure") {
        failure += 1;
      }
    }
    return { success, blocked, failure };
  }, [rows]);

  const buildFilters = (nextPage: number, exportMode = false) => {
    const { from, to } = resolveTimeRange(timeRange);
    return {
      username: username.trim() || undefined,
      role: role === "all" ? undefined : role,
      userId: positiveFilter(userId),
      action: action === "all" ? undefined : action,
      purpose: purpose.trim() || undefined,
      credentialKind: credentialKind === "all" ? undefined : credentialKind,
      credentialSource: credentialSource.trim() || undefined,
      outcome: outcome === "all" ? undefined : outcome,
      nodeId: positiveFilter(nodeId),
      sshKeyId: positiveFilter(sshKeyId),
      taskId: positiveFilter(taskId),
      taskRunId: positiveFilter(taskRunId),
      policyId: positiveFilter(policyId),
      from,
      to,
      page: exportMode ? undefined : nextPage,
      pageSize: exportMode ? 5000 : pageSize,
      sortBy: "created_at" as const,
      sortOrder: "desc" as const,
    };
  };

  const load = async (nextPage: number) => {
    if (!token) {
      toast.error(t("credentialAudit.errorNotLoggedIn"));
      return;
    }

    setLoading(true);
    try {
      const result = await apiClient.getCredentialAuditEvents(token, buildFilters(nextPage));
      setRows(result.items);
      setTotal(result.total);
      setPage(result.page);
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error(t("credentialAudit.errorForbidden"));
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  };

  const exportCSV = async () => {
    if (!token) {
      toast.error(t("credentialAudit.errorExportNotLoggedIn"));
      return;
    }

    setExporting(true);
    try {
      const blob = await apiClient.exportCredentialAuditEventsCSV(token, buildFilters(page, true));
      const link = document.createElement("a");
      const url = URL.createObjectURL(blob);
      link.href = url;
      link.download = `credential-audit-events-${new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-")}.csv`;
      link.click();
      URL.revokeObjectURL(url);
      toast.success(t("credentialAudit.exportSuccess"));
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) {
        toast.error(t("credentialAudit.errorExportForbidden"));
      } else {
        toast.error(getErrorMessage(error));
      }
    } finally {
      setExporting(false);
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
      username.trim(),
      role,
      userId.trim(),
      action,
      purpose.trim(),
      credentialKind,
      credentialSource.trim(),
      outcome,
      nodeId.trim(),
      sshKeyId.trim(),
      taskId.trim(),
      taskRunId.trim(),
      policyId.trim(),
    ].join(":");
    if (autoLoadKeyRef.current === loadKey) {
      return;
    }
    autoLoadKeyRef.current = loadKey;
    void load(1);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- load is intentionally excluded to prevent loop
  }, [action, authRole, credentialKind, credentialSource, nodeId, outcome, policyId, purpose, role, sshKeyId, taskId, taskRunId, timeRange, token, userId, username]);

  if (authRole !== "admin") {
    return <Navigate to="/app/overview" replace />;
  }

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div>
              <h1 className="text-lg font-semibold tracking-tight">{t("credentialAudit.pageTitle")}</h1>
              <p className="mt-1 text-sm text-muted-foreground">{t("credentialAudit.pageDescription")}</p>
            </div>
            <div className="flex items-center gap-2">
              <Button size="sm" variant="outline" onClick={() => void load(page)} disabled={loading}>
                <RefreshCw className="mr-1 size-3.5" aria-hidden />
                {t("common.refresh")}
              </Button>
              <Button size="sm" variant="outline" onClick={() => void exportCSV()} disabled={exporting || loading}>
                <Download className="mr-1 size-3.5" aria-hidden />
                {exporting ? t("credentialAudit.exporting") : t("credentialAudit.exportCSV")}
              </Button>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="success">{t("credentialAudit.statsSuccess", { count: stats.success })}</Badge>
            <Badge tone="warning">{t("credentialAudit.statsBlocked", { count: stats.blocked })}</Badge>
            <Badge tone="destructive">{t("credentialAudit.statsFailure", { count: stats.failure })}</Badge>
            <Badge tone="neutral">{t("credentialAudit.total", { count: total })}</Badge>
          </div>

          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-2 xl:grid-cols-4">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input
                className="pl-9"
                placeholder={t("credentialAudit.usernamePlaceholder")}
                aria-label={t("credentialAudit.usernameAriaLabel")}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </div>
            <Select value={role} onChange={(event) => setRole(event.target.value)} aria-label={t("credentialAudit.roleFilter")}>
              <option value="all">{t("credentialAudit.allRoles")}</option>
              <option value="admin">admin</option>
              <option value="operator">operator</option>
              <option value="viewer">viewer</option>
              <option value="system">system</option>
            </Select>
            <Input
              placeholder={t("credentialAudit.userIdPlaceholder")}
              aria-label={t("credentialAudit.userIdAriaLabel")}
              inputMode="numeric"
              value={userId}
              onChange={(event) => setUserId(event.target.value)}
            />
            <Select value={action} onChange={(event) => setAction(event.target.value)} aria-label={t("credentialAudit.actionFilter")}>
              <option value="all">{t("credentialAudit.allActions")}</option>
              {actionOptions.map((item) => <option key={item} value={item}>{item}</option>)}
            </Select>
            <Input
              placeholder={t("credentialAudit.purposePlaceholder")}
              aria-label={t("credentialAudit.purposeAriaLabel")}
              value={purpose}
              onChange={(event) => setPurpose(event.target.value)}
            />
            <Select value={credentialKind} onChange={(event) => setCredentialKind(event.target.value)} aria-label={t("credentialAudit.credentialKindFilter")}>
              <option value="all">{t("credentialAudit.allCredentialKinds")}</option>
              <option value="ssh_key">ssh_key</option>
              <option value="password">password</option>
              <option value="node_private_key">node_private_key</option>
              <option value="system_setting">system_setting</option>
              <option value="step_up">step_up</option>
              <option value="snapshot">snapshot</option>
              <option value="unknown">unknown</option>
            </Select>
            <Input
              placeholder={t("credentialAudit.credentialSourcePlaceholder")}
              aria-label={t("credentialAudit.credentialSourceAriaLabel")}
              value={credentialSource}
              onChange={(event) => setCredentialSource(event.target.value)}
            />
            <Select value={outcome} onChange={(event) => setOutcome(event.target.value)} aria-label={t("credentialAudit.outcomeFilter")}>
              <option value="all">{t("credentialAudit.allOutcomes")}</option>
              <option value="success">{t("credentialAudit.outcomes.success")}</option>
              <option value="blocked">{t("credentialAudit.outcomes.blocked")}</option>
              <option value="failure">{t("credentialAudit.outcomes.failure")}</option>
            </Select>
            <Input
              placeholder={t("credentialAudit.nodeIdPlaceholder")}
              aria-label={t("credentialAudit.nodeIdAriaLabel")}
              inputMode="numeric"
              value={nodeId}
              onChange={(event) => setNodeId(event.target.value)}
            />
            <Input
              placeholder={t("credentialAudit.sshKeyIdPlaceholder")}
              aria-label={t("credentialAudit.sshKeyIdAriaLabel")}
              inputMode="numeric"
              value={sshKeyId}
              onChange={(event) => setSSHKeyId(event.target.value)}
            />
            <Input
              placeholder={t("credentialAudit.taskIdPlaceholder")}
              aria-label={t("credentialAudit.taskIdAriaLabel")}
              inputMode="numeric"
              value={taskId}
              onChange={(event) => setTaskId(event.target.value)}
            />
            <Input
              placeholder={t("credentialAudit.taskRunIdPlaceholder")}
              aria-label={t("credentialAudit.taskRunIdAriaLabel")}
              inputMode="numeric"
              value={taskRunId}
              onChange={(event) => setTaskRunId(event.target.value)}
            />
            <Input
              placeholder={t("credentialAudit.policyIdPlaceholder")}
              aria-label={t("credentialAudit.policyIdAriaLabel")}
              inputMode="numeric"
              value={policyId}
              onChange={(event) => setPolicyId(event.target.value)}
            />
            <Button onClick={() => void load(1)} disabled={loading}>{t("credentialAudit.query")}</Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                { label: t("credentialAudit.timeRanges.all"), value: "all" as const },
                { label: t("credentialAudit.timeRanges.1h"), value: "1h" as const },
                { label: t("credentialAudit.timeRanges.24h"), value: "24h" as const },
                { label: t("credentialAudit.timeRanges.7d"), value: "7d" as const },
                { label: t("credentialAudit.timeRanges.30d"), value: "30d" as const },
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
                  <p className="font-medium">{row.username || "-"}</p>
                  <Badge tone={outcomeTone(row.outcome)}>{t(`credentialAudit.outcomes.${row.outcome}`)}</Badge>
                </div>
                <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                  <p>{t("credentialAudit.colTime")}：{row.createdAt}</p>
                  <p>{t("credentialAudit.colAction")}：<span className="font-mono">{row.rawAction || row.action}</span></p>
                  <p>{t("credentialAudit.colPurpose")}：{row.purpose || "-"}</p>
                  <p>{t("credentialAudit.colNodeId")}：{row.nodeId ?? "-"}</p>
                </div>
                <Button className="mt-3 w-full" size="sm" variant="outline" onClick={() => setSelected(row)}>
                  <Eye className="mr-1 size-3.5" aria-hidden />
                  {t("credentialAudit.viewDetails")}
                </Button>
              </div>
            ))}
            {!rows.length && !loading ? <EmptyState title={t("credentialAudit.emptyTitle")} /> : null}
          </div>

          <div className="hidden overflow-x-auto rounded-lg border border-border bg-card md:block">
            <table className="min-w-[1180px] text-left text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/35 text-mini uppercase tracking-wide text-muted-foreground">
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colTime")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colUser")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colAction")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colPurpose")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colCredentialKind")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colOutcome")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colNodeId")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colSSHKeyId")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colClientIP")}</th>
                  <th scope="col" className="px-3 py-2.5">{t("credentialAudit.colDetails")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className="border-b border-border transition-colors duration-200 ease-out hover:bg-muted/40">
                    <td className="px-3 py-2.5">{row.createdAt}</td>
                    <td className="px-3 py-2.5">{row.username || "-"}</td>
                    <td className="px-3 py-2.5 font-mono text-xs">{row.rawAction || row.action}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{row.purpose || "-"}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{row.credentialKind || "-"}</td>
                    <td className="px-3 py-2.5"><Badge tone={outcomeTone(row.outcome)}>{t(`credentialAudit.outcomes.${row.outcome}`)}</Badge></td>
                    <td className="px-3 py-2.5">{row.nodeId ?? "-"}</td>
                    <td className="px-3 py-2.5">{row.sshKeyId ?? "-"}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{row.clientIP || "-"}</td>
                    <td className="px-3 py-2.5">
                      <Button size="sm" variant="outline" onClick={() => setSelected(row)}>
                        <Eye className="mr-1 size-3.5" aria-hidden />
                        {t("credentialAudit.viewDetails")}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!rows.length && !loading ? <EmptyState className="rounded-none border-0 py-8" title={t("credentialAudit.emptyTitle")} /> : null}
          </div>

          <Pagination page={page} pageSize={pageSize} total={total} loading={loading} onPageChange={(p) => void load(p)} />
        </CardContent>
      </Card>

      <Dialog open={Boolean(selected)} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t("credentialAudit.detailTitle")}</DialogTitle>
            <DialogDescription>{t("credentialAudit.detailDescription")}</DialogDescription>
          </DialogHeader>
          {selected ? (
            <DialogBody className="space-y-4">
              <dl className="grid gap-2 text-sm sm:grid-cols-2">
                <Detail label={t("credentialAudit.colTime")} value={selected.createdAt} />
                <Detail label={t("credentialAudit.colUser")} value={selected.username || "-"} />
                <Detail label={t("credentialAudit.colRole")} value={selected.role || "-"} />
                <Detail label={t("credentialAudit.colAction")} value={selected.rawAction || selected.action} mono />
                <Detail label={t("credentialAudit.colPurpose")} value={selected.purpose || "-"} />
                <Detail label={t("credentialAudit.colCredentialKind")} value={selected.credentialKind || "-"} />
                <Detail label={t("credentialAudit.colCredentialSource")} value={selected.credentialSource || "-"} />
                <Detail label={t("credentialAudit.colOutcome")} value={t(`credentialAudit.outcomes.${selected.outcome}`)} />
                <Detail label={t("credentialAudit.colUserId")} value={selected.userId || "-"} />
                <Detail label={t("credentialAudit.colSSHKeyId")} value={selected.sshKeyId ?? "-"} />
                <Detail label={t("credentialAudit.colNodeId")} value={selected.nodeId ?? "-"} />
                <Detail label={t("credentialAudit.colTaskId")} value={selected.taskId ?? "-"} />
                <Detail label={t("credentialAudit.colTaskRunId")} value={selected.taskRunId ?? "-"} />
                <Detail label={t("credentialAudit.colPolicyId")} value={selected.policyId ?? "-"} />
                <Detail label={t("credentialAudit.colClientIP")} value={selected.clientIP || "-"} />
                <Detail label={t("credentialAudit.colUserAgent")} value={selected.userAgent || "-"} />
              </dl>
              {selected.errorMessage ? (
                <dl>
                  <Detail label={t("credentialAudit.colErrorMessage")} value={selected.errorMessage} mono />
                </dl>
              ) : null}
              <div>
                <h2 className="text-sm font-semibold">{t("credentialAudit.metadataTitle")}</h2>
                {Object.keys(selected.metadata).length ? (
                  <dl className="mt-2 grid gap-2 rounded-lg border border-border p-3 text-sm">
                    {Object.entries(selected.metadata).map(([key, value]) => (
                      <div key={key} className="grid gap-1 sm:grid-cols-[160px_1fr]">
                        <dt className="font-mono text-xs text-muted-foreground">{key}</dt>
                        <dd className="break-all font-mono text-xs">{renderMetadataValue(value)}</dd>
                      </div>
                    ))}
                  </dl>
                ) : (
                  <p className="mt-2 text-sm text-muted-foreground">{t("credentialAudit.metadataEmpty")}</p>
                )}
              </div>
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
