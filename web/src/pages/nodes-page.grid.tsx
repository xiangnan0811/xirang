import React from "react";
import { Activity, Loader2, ServerCog, Terminal, Trash2, Wrench } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
import { getNodeStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { NodesViewProps } from "@/pages/nodes-page.utils";


export const NodesGrid = React.memo(function NodesGrid({
  loading,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  selectedNodeId,
  selectedNodeIds,
  allVisibleSelected,
  testingNodeId,
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  setSelectedNodeId,
  handleBulkDelete,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onDeleteNode,
  handleTriggerBackup,
}: NodesViewProps) {
  const navigate = useNavigate();

  return (
    <>
      <div className="space-y-3 p-2 md:hidden">
        <div className="flex items-center gap-2 justify-between rounded-xl border border-border/75 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
          <div className="inline-flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="全选当前页可见节点"
              className="size-4"
              checked={allVisibleSelected}
              onChange={(event) =>
                toggleSelectAllVisible(event.target.checked)
              }
            />
            <span>全选</span>
          </div>
          <Button
            size="sm"
            variant="destructive"
            disabled={!selectedNodeIds.length}
            onClick={() => void handleBulkDelete()}
          >
            删除 {selectedNodeIds.length}
          </Button>
        </div>

        {sortedNodes.map((node) => {
          const status = getNodeStatusMeta(node.status);
          const checked = selectedNodeSet.has(node.id);
          return (
            <div
              key={node.id}
              className="rounded-xl border border-border/75 bg-background/70 p-3 shadow-sm"
            >
              <div className="flex items-start justify-between gap-2">
                <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={`选择节点 ${node.name}`}
                    className="size-4"
                    checked={checked}
                    onChange={(event) =>
                      toggleNodeSelection(node.id, event.target.checked)
                    }
                  />
                  选择
                </label>
                <div className="inline-flex items-center gap-1.5">
                  <StatusPulse tone={node.status} />
                  <Badge variant={status.variant}>{status.label}</Badge>
                </div>
              </div>

              <div className="mt-2">
                <p className="font-medium">{node.name}</p>
                <p className="text-xs text-muted-foreground">
                  <span className="break-all">{node.host}:{node.port} · {node.username}</span>
                </p>
              </div>

              <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                <p>
                  磁盘余量：{node.diskFreePercent}%（探测：
                  {node.diskProbeAt || "未探测"}）
                </p>
                <p>最后备份：{node.lastBackupAt}</p>
                <p className="break-words">标签：{node.tags.join(" / ") || "-"}</p>
              </div>

              <div className="mt-4 flex flex-wrap-reverse items-center justify-between gap-2 border-t border-border/40 pt-3">
                <div className="flex flex-wrap items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label="测试连接"
                    onClick={() => void onTestNode(node)}
                    disabled={testingNodeId === node.id}
                  >
                    {testingNodeId === node.id ? <Loader2 className="size-4 animate-spin" /> : <Activity className="size-4" />}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={`查看节点 ${node.name} 日志`}
                    onClick={() =>
                      navigate(`/app/logs?node=${encodeURIComponent(node.name)}`)
                    }
                  >
                    <Terminal className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label="编辑节点"
                    onClick={() => openEditDialog(node)}
                  >
                    <Wrench className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-danger/80 hover:bg-danger/10 hover:text-danger"
                    aria-label="删除节点"
                    onClick={() => onDeleteNode(node)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
                <Button
                  size="sm"
                  disabled={triggeringNodeId === node.id}
                  onClick={() => void handleTriggerBackup(node.id, node.name)}
                >
                  {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                  手动备份
                </Button>
              </div>
            </div>
          );
        })}
      </div>

      <div className="hidden gap-3 md:grid md:grid-cols-2 lg:grid-cols-3">
        {loading ? (
          <LoadingState
            className="md:col-span-2 lg:col-span-3"
            title="节点数据加载中"
            description="正在刷新节点探测状态与可用性..."
            rows={4}
          />
        ) : null}

        {!loading && !sortedNodes.length ? (
          <FilteredEmptyState
            title="当前筛选条件下暂无节点"
            description="可以重置筛选条件，或新增一个节点继续测试。"
            onReset={resetFilters}
            onCreate={openCreateDialog}
            createLabel="新增节点"
            createIcon={ServerCog}
          />
        ) : null}

        {sortedNodes.map((node) => {
          const status = getNodeStatusMeta(node.status);
          const keyLabel = node.keyId
            ? sshKeys.find((key) => key.id === node.keyId)?.name ||
            "已绑定 Key"
            : "未绑定";
          const checked = selectedNodeSet.has(node.id);

          return (
            <div
              key={node.id}
              className={cn(
                "interactive-surface p-3 transition-colors outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:border-transparent",
                selectedNodeId === node.id && "border-primary/45 ring-1 ring-primary/40"
              )}
              role="button"
              aria-label={`节点卡片 ${node.name}`}
              tabIndex={0}
              onClick={(e) => {
                if (
                  e.target instanceof HTMLElement &&
                  e.target.closest("button, input, a, label, select, textarea")
                ) {
                  return;
                }
                setSelectedNodeId(node.id);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  if (e.target === e.currentTarget) {
                    e.preventDefault();
                    setSelectedNodeId(node.id);
                  }
                }
              }}
            >
              <div className="flex items-start justify-between gap-2">
                <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={`选择节点 ${node.name}`}
                    className="size-4"
                    checked={checked}
                    onChange={(event) =>
                      toggleNodeSelection(node.id, event.target.checked)
                    }
                  />
                  选择
                </label>
                <div className="inline-flex items-center gap-1.5">
                  <StatusPulse tone={node.status} />
                  <Badge variant={status.variant}>{status.label}</Badge>
                </div>
              </div>

              <div className="mt-2">
                <p className="font-medium">{node.name}</p>
                <p className="text-xs text-muted-foreground">
                  <span className="break-all">{node.host}:{node.port} · {node.username}</span>
                </p>
              </div>

              <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                <p>
                  认证：
                  {node.authType === "key"
                    ? `密钥 / ${keyLabel}`
                    : "密码"}
                </p>
                <p>
                  磁盘余量：{node.diskFreePercent}% · 延迟{" "}
                  {node.connectionLatencyMs
                    ? `${node.connectionLatencyMs}ms`
                    : "-"}
                </p>
                <p>探测：{node.diskProbeAt || "未探测"}</p>
                <p>最后备份：{node.lastBackupAt}</p>
                <p className="break-words">
                  标签：{node.tags.length ? node.tags.join(" / ") : "-"}
                </p>
              </div>

              <div className="mt-4 flex flex-wrap-reverse items-center justify-between gap-2 border-t border-border/40 pt-3">
                <div className="flex flex-wrap items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label="测试连接"
                    onClick={() => void onTestNode(node)}
                    disabled={testingNodeId === node.id}
                  >
                    {testingNodeId === node.id ? <Loader2 className="size-4 animate-spin" /> : <Activity className="size-4" />}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={`查看节点 ${node.name} 日志`}
                    onClick={() =>
                      navigate(`/app/logs?node=${encodeURIComponent(node.name)}`)
                    }
                  >
                    <Terminal className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label="编辑节点"
                    onClick={() => openEditDialog(node)}
                  >
                    <Wrench className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-danger/80 hover:bg-danger/10 hover:text-danger"
                    aria-label="删除节点"
                    onClick={() => onDeleteNode(node)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={triggeringNodeId === node.id}
                  onClick={() => void handleTriggerBackup(node.id, node.name)}
                >
                  {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                  手动备份
                </Button>
              </div>
            </div>
          );
        })}
      </div>
    </>
  );
});
