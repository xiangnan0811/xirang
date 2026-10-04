import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Pagination } from "@/components/ui/pagination";
import { Select } from "@/components/ui/select";
import { createAutomationRulesApi, type AutomationRuleLog, type AutomationRuleLogQuery, type AutomationRuleLogPage } from "@/lib/api/automation-rules";
import { formatTime } from "@/lib/date-utils";
import type { AutomationRule } from "@/types/domain";

const pageSize = 30;
type Props = { token: string; rules: AutomationRule[] };
type LoadState = { status: "loading" } | { status: "error" } | { status: "ready"; data: AutomationRuleLogPage };

export function AutomationRuleHistory({ token, rules }: Props) {
  const { t } = useTranslation();
  const titleId = useId();
  const [ruleId, setRuleId] = useState<number | undefined>();
  const [result, setResult] = useState<"success" | "error" | undefined>();
  const [page, setPage] = useState(1);
  const [revision, setRevision] = useState(0);
  const retry = () => setRevision((value) => value + 1);
  return (
    <section aria-labelledby={titleId} className="space-y-3">
      <h2 id={titleId} className="text-lg font-semibold">{t("automation.history.title")}</h2>
      <p className="text-sm text-muted-foreground">{t("automation.history.description")}</p>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
        <Select aria-label={t("automation.history.ruleFilter")} value={ruleId ?? "all"} onChange={(event) => {
          setRuleId(event.target.value === "all" ? undefined : Number(event.target.value));
          setPage(1);
        }}>
          <option value="all">{t("automation.history.allRules")}</option>
          {rules.map((rule) => <option key={rule.id} value={rule.id}>{rule.name} (#{rule.id})</option>)}
          {ruleId !== undefined && !rules.some((rule) => rule.id === ruleId) && (
            <option value={ruleId}>{t("automation.history.unavailableRule", { id: ruleId })}</option>
          )}
        </Select>
        <Select aria-label={t("automation.history.resultFilter")} value={result ?? "all"} onChange={(event) => {
          const value = event.target.value;
          setResult(value === "success" || value === "error" ? value : undefined);
          setPage(1);
        }}>
          <option value="all">{t("automation.history.allResults")}</option>
          <option value="success">{t("automation.history.recorded")}</option>
          <option value="error">{t("automation.history.failed")}</option>
        </Select>
        <Button variant="outline" onClick={retry}>{t("common.refresh")}</Button>
      </div>
      {/* A new query owns a new result state: old rows disappear synchronously. */}
      <HistoryResults key={`${token}:${ruleId}:${result}:${page}:${revision}`} token={token} rules={rules}
        query={{ ruleId, result, page, pageSize }} onPageChange={setPage} onRetry={retry} />
    </section>
  );
}

function HistoryOutcome({ row }: { row: AutomationRuleLog }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1">
      <Badge dot={false} tone={row.result === "success" ? "success" : row.result === "error" ? "destructive" : "neutral"}>
        {t(`automation.history.${row.result === "success" ? "recorded" : row.result === "error" ? "failed" : "unknown"}`)}
      </Badge>
      {row.result === "error" && <p className="max-w-sm text-xs text-muted-foreground">{t("automation.history.safeError")}</p>}
    </div>
  );
}

function HistoryAction({ row }: { row: AutomationRuleLog }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1">
      <p>{row.actionType === "unknown" ? t("automation.history.unknown") : t(`automation.actionTypes.${row.actionType}`)}</p>
      {row.result === "success" && row.actionType === "trigger_task" && <p className="max-w-sm text-xs text-muted-foreground">{t("automation.history.dispatched")}</p>}
      {row.result === "success" && row.actionType === "send_notification" && <p className="max-w-sm text-xs text-muted-foreground">{t("automation.history.notificationRecorded")}</p>}
      {row.targetTaskId !== null && row.targetTaskRunId !== null && (
        <p className="text-xs text-muted-foreground">{t("automation.history.targets", { taskId: row.targetTaskId, runId: row.targetTaskRunId })}</p>
      )}
    </div>
  );
}

function HistoryResults({ token, rules, query, onPageChange, onRetry }: Props & {
  query: AutomationRuleLogQuery; onPageChange: (page: number) => void; onRetry: () => void;
}) {
  const { t } = useTranslation();
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const { ruleId, result, page } = query;
  useEffect(() => {
    const controller = new AbortController();
    void createAutomationRulesApi().listLogs(token, { ruleId, result, page, pageSize }, { signal: controller.signal })
      .then((data) => { if (!controller.signal.aborted) setState({ status: "ready", data }); })
      .catch(() => { if (!controller.signal.aborted) setState({ status: "error" }); });
    return () => controller.abort();
  }, [token, ruleId, result, page]);

  if (state.status === "loading") return <p role="status" className="py-6 text-sm text-muted-foreground">{t("common.loading")}</p>;
  if (state.status === "error") return (
    <div className="space-y-3 rounded-lg border border-border p-4">
      <p role="alert">{t("automation.history.loadFailed")}</p>
      <Button variant="outline" onClick={onRetry}>{t("common.retry")}</Button>
    </div>
  );
  const { items, total } = state.data;
  const ruleName = (row: AutomationRuleLog) => {
    const rule = rules.find((item) => item.id === row.ruleId);
    return rule ? `${rule.name} (#${rule.id})` : t("automation.history.unavailableRule", { id: row.ruleId });
  };
  const eventName = (row: AutomationRuleLog) => row.eventType === "unknown" ? t("automation.history.unknown") : t(`automation.eventTypes.${row.eventType}`);
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        {items.length === 0 ? <p role="status" className="py-6 text-center text-muted-foreground">{t("automation.history.empty")}</p> : <>
          <div role="region" aria-label={t("automation.history.table")}
            // Keyboard focus enables native horizontal scrolling, as on the audit tables.
            // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
            tabIndex={0}
            className="hidden overflow-x-auto rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring md:block">
            <table className="w-full min-w-[800px] text-sm">
              <thead><tr className="border-b border-border text-left text-muted-foreground">
                {["time", "rule", "event", "action", "result"].map((column) => <th key={column} scope="col" className="px-3 py-2">{t(`automation.history.${column}`)}</th>)}
              </tr></thead>
              <tbody>{items.map((row) => <tr key={row.id} className="border-b border-border align-top last:border-0">
                <td className="whitespace-nowrap px-3 py-3"><time dateTime={row.createdAt}>{formatTime(row.createdAt)}</time><p className="text-xs text-muted-foreground">{t("automation.history.logId", { id: row.id })}</p></td>
                <td className="max-w-64 break-words px-3 py-3">{ruleName(row)}</td>
                <td className="px-3 py-3">{eventName(row)}</td>
                <td className="px-3 py-3"><HistoryAction row={row} /></td>
                <td className="px-3 py-3"><HistoryOutcome row={row} /></td>
              </tr>)}</tbody>
            </table>
          </div>
          <ul aria-label={t("automation.history.title")} className="space-y-3 md:hidden">
            {items.map((row) => <li key={row.id} className="space-y-2 rounded-lg border border-border p-3 text-sm">
              <p className="break-words font-medium">{ruleName(row)}</p>
              <p className="text-xs text-muted-foreground"><time dateTime={row.createdAt}>{formatTime(row.createdAt)}</time> · {t("automation.history.logId", { id: row.id })}</p>
              <p>{eventName(row)}</p><HistoryAction row={row} /><HistoryOutcome row={row} />
            </li>)}
          </ul>
        </>}
        <Pagination page={state.data.page} pageSize={state.data.pageSize} total={total} onPageChange={onPageChange} />
      </CardContent>
    </Card>
  );
}
