import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Navigate } from "react-router-dom";
import { Eye, RefreshCw, Search } from "lucide-react";
import { AccessDeniedRedirect } from "@/components/access-denied-navigation";
import { useAuth } from "@/context/auth-context.hooks";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import { ApiError, apiClient } from "@/lib/api/client";
import { getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import type { CredentialAccessGrantListOptions } from "@/lib/api/credential-access-grants-api";
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

const pageSize = 30;

type TimeRange = "all" | "1h" | "24h" | "7d" | "30d";

type GrantPage = { items: CredentialAccessGrant[]; total: number; page: number; pageSize: number };
type GrantLoadState =
  | { status: "loading" }
  | { status: "error"; forbidden: boolean }
  | { status: "ready"; data: GrantPage };

const actionOptions = [
  "terminal.open",
  "config.import",
  "config.export",
  "snapshot.restore",
  "task.restore_trigger",
  "task.manual_trigger",
  "task.batch_trigger",
  "batch_command.create",
  "batch_command.retry",
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

// Filters send exact stored states; "unknown" stands for any unrecognized wire value and is never a query.
const primaryStatusOptions: CredentialAccessGrantStatus[] = ["active", "expired"];
const historicalStatusOptions: CredentialAccessGrantStatus[] = ["requested", "approved", "denied", "revoked"];

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

// Stored states never promise current validity, so only retained denials/revocations get emphasis.
function grantStatusTone(status: CredentialAccessGrantStatus) {
  return status === "denied" || status === "revoked" ? "destructive" as const : "neutral" as const;
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
  const { token, role, authTransitioning } = useAuth();

  if (authTransitioning) {
    return <p role="status" className="py-6 text-sm text-muted-foreground">{t("common.loading")}</p>;
  }
  if (role !== "admin" || !token) {
    if (token && (role === "operator" || role === "viewer")) {
      return <AccessDeniedRedirect sourcePath="/app/credential-access-grants" />;
    }
    return <Navigate to="/app/overview" replace />;
  }
  // A new identity or auth session owns a new page: filters, rows and details never cross it.
  return <GrantPageSession key={JSON.stringify([token, role, getAuthSessionGeneration()])} token={token} />;
}

function GrantPageSession({ token }: { token: string }) {
  const { t } = useTranslation();
  const statusHintId = useId();
  const [page, setPage] = useState(1);
  const [revision, setRevision] = useState(0);
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

  const usernameFilter = requesterUsername.trim();
  const requesterUserIdFilter = positiveFilter(requesterUserId);
  const nodeIdFilter = positiveFilter(nodeId);
  const taskIdFilter = positiveFilter(taskId);
  const policyIdFilter = positiveFilter(policyId);

  // Relative time ranges resolve once per semantic query; the clock never drives a request.
  const request = useMemo(() => {
    const { from, to } = resolveTimeRange(timeRange);
    const query: CredentialAccessGrantListOptions = {
      requesterUsername: usernameFilter || undefined,
      requesterRole: requesterRole === "all" ? undefined : requesterRole,
      requesterUserId: requesterUserIdFilter,
      action: action === "all" ? undefined : action,
      purpose: purpose === "all" ? undefined : purpose,
      status: status === "all" ? undefined : status,
      nodeId: nodeIdFilter,
      taskId: taskIdFilter,
      policyId: policyIdFilter,
      from,
      to,
      page,
      pageSize,
      sortBy: "created_at",
      sortOrder: "desc",
    };
    return { query, revision };
  }, [action, nodeIdFilter, page, policyIdFilter, purpose, requesterRole, requesterUserIdFilter, revision, status, taskIdFilter, timeRange, usernameFilter]);

  const filterChange = (apply: (value: string) => void) => (event: { target: { value: string } }) => {
    apply(event.target.value);
    setPage(1);
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div>
            <h1 className="text-lg font-semibold tracking-tight">{t("credentialGrants.pageTitle")}</h1>
            <p className="mt-1 text-sm text-muted-foreground">{t("credentialGrants.pageDescription")}</p>
          </div>

          <div className="filter-panel sticky-filter grid items-start gap-2 md:grid-cols-2 xl:grid-cols-4">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input
                className="pl-9"
                placeholder={t("credentialGrants.requesterUsernamePlaceholder")}
                aria-label={t("credentialGrants.requesterUsernameAriaLabel")}
                value={requesterUsername}
                onChange={filterChange(setRequesterUsername)}
              />
            </div>
            <Select value={requesterRole} onChange={filterChange(setRequesterRole)} aria-label={t("credentialGrants.requesterRoleFilter")}>
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
              onChange={filterChange(setRequesterUserId)}
            />
            <Select value={action} onChange={filterChange(setAction)} aria-label={t("credentialGrants.actionFilter")}>
              <option value="all">{t("credentialGrants.allActions")}</option>
              {actionOptions.map((item) => <option key={item} value={item}>{item === "batch_command.retry" ? t("credentialGrants.actions.batchCommandRetry") : item}</option>)}
            </Select>
            <Select value={purpose} onChange={filterChange(setPurpose)} aria-label={t("credentialGrants.purposeFilter")}>
              <option value="all">{t("credentialGrants.allPurposes")}</option>
              {purposeOptions.map((item) => <option key={item} value={item}>{item}</option>)}
            </Select>
            <div className="space-y-1 md:col-span-2">
              <Select
                value={status}
                onChange={filterChange(setStatus)}
                aria-label={t("credentialGrants.statusFilter")}
                aria-describedby={statusHintId}
              >
                <option value="all">{t("credentialGrants.allStatuses")}</option>
                {primaryStatusOptions.map((item) => <option key={item} value={item}>{t(`credentialGrants.statuses.${item}`)}</option>)}
                <optgroup label={t("credentialGrants.historicalStatuses")}>
                  {historicalStatusOptions.map((item) => <option key={item} value={item}>{t(`credentialGrants.statuses.${item}`)}</option>)}
                </optgroup>
              </Select>
              <p id={statusHintId} className="px-1 text-xs leading-relaxed text-muted-foreground">{t("credentialGrants.statusHint")}</p>
            </div>
            <Input
              placeholder={t("credentialGrants.nodeIdPlaceholder")}
              aria-label={t("credentialGrants.nodeIdAriaLabel")}
              inputMode="numeric"
              value={nodeId}
              onChange={filterChange(setNodeId)}
            />
            <Input
              placeholder={t("credentialGrants.taskIdPlaceholder")}
              aria-label={t("credentialGrants.taskIdAriaLabel")}
              inputMode="numeric"
              value={taskId}
              onChange={filterChange(setTaskId)}
            />
            <Input
              placeholder={t("credentialGrants.policyIdPlaceholder")}
              aria-label={t("credentialGrants.policyIdAriaLabel")}
              inputMode="numeric"
              value={policyId}
              onChange={filterChange(setPolicyId)}
            />
          </div>

          <div className="space-y-1.5">
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
                  onClick={() => {
                    setTimeRange(item.value);
                    setPage(1);
                  }}
                >
                  {item.label}
                </Button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">{t("credentialGrants.timeRangeHint")}</p>
          </div>

          {/* A new query owns a new result state: old rows, counts and details disappear synchronously. */}
          <GrantResults
            key={JSON.stringify([token, request.query, request.revision])}
            token={token}
            query={request.query}
            onPageChange={setPage}
            onQuery={() => {
              setPage(1);
              setRevision((value) => value + 1);
            }}
            onRefresh={() => setRevision((value) => value + 1)}
          />
        </CardContent>
      </Card>
    </div>
  );
}

function GrantResults({ token, query, onPageChange, onQuery, onRefresh }: {
  token: string;
  query: CredentialAccessGrantListOptions;
  onPageChange: (page: number) => void;
  onQuery: () => void;
  onRefresh: () => void;
}) {
  const { t } = useTranslation();
  const [state, setState] = useState<GrantLoadState>({ status: "loading" });
  const [selected, setSelected] = useState<CredentialAccessGrant | null>(null);
  // Browser clock for expiry indications only; it never drives requests or authorization.
  const [now, setNow] = useState(Date.now);
  const epochRef = useRef(0);
  // The detail dialog has no Radix trigger, so remember which desktop/mobile button opened it.
  const detailOpenerRef = useRef<HTMLButtonElement | null>(null);

  useLayoutEffect(() => {
    // Both passes move the epoch so a StrictMode remount cannot revive a discarded request.
    epochRef.current += 1;
    return () => {
      epochRef.current += 1;
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const epoch = epochRef.current;
    const generation = getAuthSessionGeneration();
    const current = () => {
      const identity = getAuthIdentitySnapshot();
      return epochRef.current === epoch
        && !controller.signal.aborted
        && identity.token === token
        && identity.role === "admin"
        && getAuthSessionGeneration() === generation
        && !isAuthTransitionActive();
    };
    if (current()) {
      apiClient.listCredentialAccessGrants(token, query, { signal: controller.signal }).then(
        (data) => {
          if (!current()) return;
          setNow(Date.now());
          setState({ status: "ready", data });
        },
        (error: unknown) => {
          if (!current()) return;
          const forbidden = error instanceof ApiError && (error.httpStatus ?? error.status) === 403;
          setState({ status: "error", forbidden });
        },
      );
    }
    return () => controller.abort();
  }, [token, query]);

  const loading = state.status === "loading";
  const rows = state.status === "ready" ? state.data.items : [];
  const hasPendingExpiry = rows.some((row) => row.expiresAtMs !== undefined && row.expiresAtMs > now);
  useVisibilityPolling(() => setNow(Date.now()), 1000, { enabled: hasPendingExpiry, immediate: false });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {state.status === "ready" ? (
            <>
              <Badge tone="neutral" dot={false}>{t("credentialGrants.pageCount", { count: state.data.items.length })}</Badge>
              <Badge tone="neutral" dot={false}>{t("credentialGrants.total", { count: state.data.total })}</Badge>
            </>
          ) : null}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={onQuery} disabled={loading}>
            <Search className="mr-1 size-3.5" aria-hidden />
            {t("credentialGrants.query")}
          </Button>
          <Button size="sm" variant="outline" onClick={onRefresh} disabled={loading}>
            <RefreshCw className="mr-1 size-3.5" aria-hidden />
            {t("common.refresh")}
          </Button>
        </div>
      </div>

      {state.status === "loading" ? (
        <p role="status" className="py-6 text-sm text-muted-foreground">{t("common.loading")}</p>
      ) : null}

      {state.status === "error" ? (
        <div className="space-y-3 rounded-lg border border-border p-4">
          <p role="alert" className="text-sm">{t(state.forbidden ? "credentialGrants.errorForbidden" : "credentialGrants.loadFailed")}</p>
          <Button size="sm" variant="outline" onClick={onRefresh}>{t("common.retry")}</Button>
        </div>
      ) : null}

      {state.status === "ready" && rows.length === 0 ? <EmptyState title={t("credentialGrants.emptyTitle")} /> : null}

      {rows.length > 0 ? (
        <>
          <ul aria-label={t("credentialGrants.pageTitle")} className="grid gap-3 sm:grid-cols-2 md:hidden lg:grid-cols-3">
            {rows.map((row) => (
              <li key={row.id} className="rounded-lg border border-border p-3 transition-colors hover:bg-accent">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-medium">{row.requesterUsername || "-"}</p>
                  <Badge tone={grantStatusTone(row.status)}>{t(`credentialGrants.statuses.${row.status}`)}</Badge>
                </div>
                <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                  <p>{t("credentialGrants.colCreatedAt")}：{row.createdAt || "-"}</p>
                  <p>{t("credentialGrants.colAction")}：<span className="font-mono">{row.action}</span></p>
                  <p>{t("credentialGrants.colPurpose")}：{row.purpose}</p>
                  <p>{t("credentialGrants.colResource")}：{resourceSummary(row)}</p>
                  <p>{t("credentialGrants.colExpiresAt")}：<GrantExpiry grant={row} now={now} /></p>
                </div>
                <Button
                  className="mt-3 w-full"
                  size="sm"
                  variant="outline"
                  onClick={(event) => {
                    detailOpenerRef.current = event.currentTarget;
                    setSelected(row);
                  }}
                >
                  <Eye className="mr-1 size-3.5" aria-hidden />
                  {t("credentialGrants.viewDetails")}
                </Button>
              </li>
            ))}
          </ul>

          <div
            role="region"
            // Keyboard focus enables native horizontal scrolling.
            // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
            tabIndex={0}
            aria-label={t("credentialGrants.pageTitle")}
            className="hidden overflow-x-auto rounded-lg border border-border bg-card md:block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          >
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
                    <td className="px-3 py-2.5 text-muted-foreground"><GrantExpiry grant={row} now={now} /></td>
                    <td className="px-3 py-2.5">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={(event) => {
                          detailOpenerRef.current = event.currentTarget;
                          setSelected(row);
                        }}
                      >
                        <Eye className="mr-1 size-3.5" aria-hidden />
                        {t("credentialGrants.viewDetails")}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : null}

      {state.status === "ready" ? (
        <Pagination page={state.data.page} pageSize={state.data.pageSize} total={state.data.total} onPageChange={onPageChange} />
      ) : null}

      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent
          size="lg"
          onCloseAutoFocus={(event) => {
            const opener = detailOpenerRef.current;
            detailOpenerRef.current = null;
            if (!opener?.isConnected) return;
            event.preventDefault();
            opener.focus();
          }}
        >
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
                <Detail
                  label={t("credentialGrants.colStatus")}
                  value={(
                    <>
                      <span>{t(`credentialGrants.statuses.${selected.status}`)}</span>
                      <span className="mt-0.5 block break-normal text-xs text-muted-foreground">{t(`credentialGrants.statusDescriptions.${selected.status}`)}</span>
                    </>
                  )}
                />
                <Detail label={t("credentialGrants.colRequestedTtl")} value={selected.requestedTtlSeconds || "-"} />
                <Detail label={t("credentialGrants.colNodeId")} value={selected.nodeId ?? "-"} />
                <Detail label={t("credentialGrants.colTaskId")} value={selected.taskId ?? "-"} />
                <Detail label={t("credentialGrants.colPolicyId")} value={selected.policyId ?? "-"} />
                <Detail label={t("credentialGrants.colRequestedAt")} value={selected.requestedAt || "-"} />
                <Detail label={t("credentialGrants.colApprovedAt")} value={selected.approvedAt || "-"} />
                <Detail label={t("credentialGrants.colApproverUserId")} value={selected.approverUserId ?? "-"} />
                <Detail label={t("credentialGrants.colApprover")} value={selected.approverUsername || "-"} />
                <Detail label={t("credentialGrants.colExpiresAt")} value={<GrantExpiry grant={selected} now={now} />} />
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

// The expiry indication uses the browser clock and is shown beside, never instead of, the stored status.
function GrantExpiry({ grant, now }: { grant: CredentialAccessGrant; now: number }) {
  const { t } = useTranslation();
  const expiresAtMs = grant.expiresAtMs;
  if (expiresAtMs === undefined || !Number.isFinite(expiresAtMs)) {
    return (
      <span className="inline-flex flex-wrap items-baseline gap-x-2">
        <span>-</span>
        <span className="text-xs text-muted-foreground">{t("credentialGrants.expiryUnknown")}</span>
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-2">
      <time className="whitespace-nowrap" dateTime={new Date(expiresAtMs).toISOString()}>{grant.expiresAt}</time>
      <span className="text-xs text-muted-foreground">
        {t(expiresAtMs <= now ? "credentialGrants.expiryElapsed" : "credentialGrants.expiryPending")}
      </span>
    </span>
  );
}

function Detail({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={mono ? "break-all font-mono text-xs" : "break-all"}>{value}</dd>
    </div>
  );
}
