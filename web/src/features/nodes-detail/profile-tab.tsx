import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";

export default function ProfileTab({ nodeId }: { nodeId: number }) {
  const [node, setNode] = useState<NodeRecord | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchNode = useCallback(async (signal: AbortSignal) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token || nodeId <= 0) return;
    setLoading(true);
    try {
      const nodes = await apiClient.getNodes(token, { signal });
      if (!signal.aborted) {
        setNode(nodes.find((n) => n.id === nodeId) ?? null);
      }
    } catch {
      // ignore aborts and network errors
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchNode(controller.signal);
    return () => controller.abort();
  }, [fetchNode]);

  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>;
  if (!node) return <p className="text-sm text-muted-foreground">未找到该节点。</p>;

  return (
    <div className="grid grid-cols-1 gap-6 md:grid-cols-2" data-testid="profile-tab">
      <section className="rounded-md border border-border bg-card p-4">
        <h3 className="text-base font-medium">基础信息</h3>
        <dl className="mt-3 grid grid-cols-[120px_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">名称</dt>
          <dd>{node.name}</dd>
          <dt className="text-muted-foreground">地址</dt>
          <dd>
            {node.host}:{node.port}
          </dd>
          <dt className="text-muted-foreground">用户名</dt>
          <dd>{node.username}</dd>
          <dt className="text-muted-foreground">标签</dt>
          <dd>{node.tags.length > 0 ? node.tags.join(", ") : "-"}</dd>
          <dt className="text-muted-foreground">状态</dt>
          <dd>{node.status}</dd>
          <dt className="text-muted-foreground">备份目录</dt>
          <dd>{node.backupDir || "-"}</dd>
        </dl>
      </section>

      <section className="rounded-md border border-border bg-card p-4">
        <h3 className="text-base font-medium">时间 &amp; 维护窗</h3>
        <dl className="mt-3 grid grid-cols-[140px_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">最近探测</dt>
          <dd>
            {node.lastProbeAt ? new Date(node.lastProbeAt).toLocaleString() : "-"}
          </dd>
          <dt className="text-muted-foreground">最后在线</dt>
          <dd>
            {node.lastSeenAt ? new Date(node.lastSeenAt).toLocaleString() : "-"}
          </dd>
          <dt className="text-muted-foreground">最近备份</dt>
          <dd>
            {node.lastBackupAt ? new Date(node.lastBackupAt).toLocaleString() : "-"}
          </dd>
          <dt className="text-muted-foreground">维护窗口</dt>
          <dd>
            {node.maintenanceStart || node.maintenanceEnd
              ? `${node.maintenanceStart ?? "?"} → ${node.maintenanceEnd ?? "?"}`
              : "未设置"}
          </dd>
          <dt className="text-muted-foreground">归档</dt>
          <dd>{node.archived ? "是" : "否"}</dd>
        </dl>
      </section>
    </div>
  );
}
