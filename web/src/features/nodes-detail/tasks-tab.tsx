import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { apiClient } from "@/lib/api/client";
import type { TaskRecord, TaskStatus } from "@/types/domain";
import type { NodeDetailTabProps } from "./types";

type Filter = "all" | "running" | "failed";

const FILTER_STATUSES: Record<Filter, TaskStatus[]> = {
  all: [],
  running: ["running", "pending", "retrying"],
  failed: ["failed"],
};

export default function TasksTab({ nodeId, token }: NodeDetailTabProps) {
  const { t } = useTranslation();
  const [tasks, setTasks] = useState<TaskRecord[]>([]);
  // First paint must show loading (not empty) when a fetch is expected.
  const [loading, setLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");

  const filterLabels: Record<Filter, string> = {
    all: t("nodes.nodeDetail.tasksFilterAll"),
    running: t("nodes.nodeDetail.tasksFilterRunning"),
    failed: t("nodes.nodeDetail.tasksFilterFailed"),
  };

  const fetchTasks = useCallback(async (signal: AbortSignal) => {
    if (!token || nodeId <= 0) {
      setLoading(false);
      setTasks([]);
      setError(false);
      return;
    }
    setLoading(true);
    setError(false);
    try {
      const all = await apiClient.getTasks(token, { signal });
      if (!signal.aborted) {
        setTasks(all.filter((row) => row.nodeId === nodeId));
      }
    } catch {
      if (!signal.aborted) {
        setTasks([]);
        setError(true);
      }
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId, token]);

  useEffect(() => {
    // Drop previous node's tasks immediately on node switch; keep loading so
    // consumers do not flash tasksEmpty before the fetch settles.
    setTasks([]);
    setError(false);
    setLoading(Boolean(token && nodeId > 0));
    const controller = new AbortController();
    void fetchTasks(controller.signal);
    return () => controller.abort();
  }, [fetchTasks, token, nodeId]);

  const filtered = tasks.filter((row) => {
    const statuses = FILTER_STATUSES[filter];
    if (statuses.length === 0) return true;
    return statuses.includes(row.status);
  });

  return (
    <div className="flex flex-col gap-4" data-testid="tasks-tab">
      <div className="flex items-center gap-2">
        {(["all", "running", "failed"] as Filter[]).map((f) => (
          <button
            key={f}
            type="button"
            data-testid={`filter-${f}`}
            onClick={() => setFilter(f)}
            data-state={filter === f ? "active" : "inactive"}
            className={
              "rounded-full px-3 py-1 text-xs font-medium " +
              (filter === f
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground")
            }
          >
            {filterLabels[f]}
          </button>
        ))}
      </div>

      {loading && <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.loading")}</p>}
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.tasksError")}
        </p>
      )}
      {!loading && !error && filtered.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.tasksEmpty")}</p>
      )}
      {filtered.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="min-w-full text-sm">
            <thead className="bg-muted">
              <tr>
                <th className="px-3 py-2 text-left font-medium">{t("nodes.nodeDetail.tasksColName")}</th>
                <th className="px-3 py-2 text-left font-medium">{t("nodes.nodeDetail.tasksColStatus")}</th>
                <th className="px-3 py-2 text-left font-medium">{t("nodes.nodeDetail.tasksColLastRun")}</th>
                <th className="px-3 py-2 text-left font-medium">{t("nodes.nodeDetail.tasksColNextRun")}</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((row) => (
                <tr key={row.id} className="border-t border-border hover:bg-muted/40">
                  <td className="px-3 py-2">
                    <Link to="/app/tasks" className="hover:underline">
                      {row.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2">
                    <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{row.status}</span>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {row.startedAt ? new Date(row.startedAt).toLocaleString() : "-"}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {row.nextRunAt ? new Date(row.nextRunAt).toLocaleString() : "-"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
