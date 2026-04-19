import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { apiClient } from "@/lib/api/client";
import type { TaskRecord, TaskStatus } from "@/types/domain";

type Filter = "all" | "running" | "failed";

const FILTER_STATUSES: Record<Filter, TaskStatus[]> = {
  all: [],
  running: ["running", "pending", "retrying"],
  failed: ["failed"],
};

export default function TasksTab({ nodeId }: { nodeId: number }) {
  const [tasks, setTasks] = useState<TaskRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");

  const fetchTasks = useCallback(async (signal: AbortSignal) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token || nodeId <= 0) return;
    setLoading(true);
    try {
      const all = await apiClient.getTasks(token, { signal });
      if (!signal.aborted) {
        setTasks(all.filter((t) => t.nodeId === nodeId));
      }
    } catch {
      // ignore aborts and network errors
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchTasks(controller.signal);
    return () => controller.abort();
  }, [fetchTasks]);

  const filtered = tasks.filter((t) => {
    const statuses = FILTER_STATUSES[filter];
    if (statuses.length === 0) return true;
    return statuses.includes(t.status);
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
            {f === "all" ? "全部" : f === "running" ? "运行中" : "近期失败"}
          </button>
        ))}
      </div>

      {loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {!loading && filtered.length === 0 && (
        <p className="text-sm text-muted-foreground">该节点暂无关联任务记录。</p>
      )}
      {filtered.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="min-w-full text-sm">
            <thead className="bg-muted">
              <tr>
                <th className="px-3 py-2 text-left font-medium">任务</th>
                <th className="px-3 py-2 text-left font-medium">状态</th>
                <th className="px-3 py-2 text-left font-medium">最近运行</th>
                <th className="px-3 py-2 text-left font-medium">下次运行</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((t) => (
                <tr key={t.id} className="border-t border-border hover:bg-muted/40">
                  <td className="px-3 py-2">
                    <Link to="/app/tasks" className="hover:underline">
                      {t.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2">
                    <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{t.status}</span>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {t.startedAt ? new Date(t.startedAt).toLocaleString() : "-"}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {t.nextRunAt ? new Date(t.nextRunAt).toLocaleString() : "-"}
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
