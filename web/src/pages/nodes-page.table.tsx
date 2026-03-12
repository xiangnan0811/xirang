import React from "react";
import { Activity, Loader2, MonitorPlay, ServerCog, Terminal, Trash2, Wrench } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
import { getNodeStatusMeta } from "@/lib/status";
import { getDiskBarToneClass } from "@/pages/nodes-page.utils";
import { cn } from "@/lib/utils";
import type { NodesViewProps } from "@/pages/nodes-page.utils";

export const NodesTable = React.memo(function NodesTable({
  loading,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  allVisibleSelected,
  testingNodeId,
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onDeleteNode,
  handleTriggerBackup,
  onOpenTerminal,
  isAdmin,
}: NodesViewProps) {
  const navigate = useNavigate();

  return (
    <div className="hidden glass-panel overflow-x-auto md:block">
      <table className="min-w-[1280px] text-left text-sm">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th className="px-3 py-2.5">
              <input
                type="checkbox"
                aria-label="全选当前页可见节点"
                className="size-4"
                checked={allVisibleSelected}
                onChange={(event) =>
                  toggleSelectAllVisible(event.target.checked)
                }
              />
            </th>
            <th className="px-3 py-2.5">节点</th>
            <th className="px-3 py-2.5">地址</th>
            <th className="px-3 py-2.5">认证</th>
            <th className="px-3 py-2.5">状态</th>
            <th className="px-3 py-2.5">磁盘探测</th>
            <th className="px-3 py-2.5">最后备份</th>
            <th className="px-3 py-2.5">标签</th>
            <th className="px-3 py-2.5 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr>
              <td colSpan={9} className="px-3 py-4 text-muted-foreground">
                节点数据加载中...
              </td>
            </tr>
          ) : !sortedNodes.length ? (
            <tr>
              <td colSpan={9} className="px-3 py-6">
                <FilteredEmptyState
                  className="py-8"
                  title="当前筛选条件下暂无节点"
                  description="可以重置筛选条件，或新增一个节点继续测试。"
                  onReset={resetFilters}
                  onCreate={openCreateDialog}
                  createLabel="新增节点"
                  createIcon={ServerCog}
                />
              </td>
            </tr>
          ) : (
            sortedNodes.map((node) => {
              const status = getNodeStatusMeta(node.status);
              const keyLabel = node.keyId
                ? sshKeys.find((key) => key.id === node.keyId)?.name || "已绑定 Key"
                : "未绑定";

              return (
                <tr key={node.id} className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-accent/35">
                  <td className="px-3 py-2.5">
                    <input
                      type="checkbox"
                      aria-label={`选择节点 ${node.name}`}
                      className="size-4"
                      checked={selectedNodeSet.has(node.id)}
                      onChange={(event) =>
                        toggleNodeSelection(node.id, event.target.checked)
                      }
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <p className="font-medium">{node.name}</p>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    <p>
                      {node.host}:{node.port}
                    </p>
                    <p className="text-xs">{node.username}</p>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">
                    <p>
                      {node.authType === "key" ? "密钥" : "密码"}
                    </p>
                    <p>
                      {node.authType === "key" ? keyLabel : "-"}
                    </p>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="inline-flex items-center gap-1.5">
                      <StatusPulse tone={node.status} />
                      <Badge variant={status.variant}>
                        {status.label}
                      </Badge>
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="w-44">
                      <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
                        <span>{node.diskFreePercent}% 可用</span>
                        <span>
                          {node.connectionLatencyMs
                            ? `${node.connectionLatencyMs} ms`
                            : "-"}
                        </span>
                      </div>
                      <div className="h-2 rounded-full bg-muted">
                        <div
                          className={cn(
                            "h-2 rounded-full",
                            getDiskBarToneClass(node.diskFreePercent)
                          )}
                          style={{
                            width: `${Math.max(4, node.diskFreePercent)}%`,
                          }}
                        />
                      </div>
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        探测：{node.diskProbeAt || "未探测"}
                      </p>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {node.lastBackupAt}
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap gap-1">
                      {node.tags.map((tag) => (
                        <Badge key={tag} variant="outline">
                          {tag}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={`测试节点 ${node.name} 连接`} title="测试连接"
                        onClick={() => void onTestNode(node)}
                        disabled={testingNodeId === node.id}
                      >
                        {testingNodeId === node.id ? <Loader2 className="size-4 animate-spin" /> : <Activity className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={`查看节点 ${node.name} 日志`} title="查看日志"
                        onClick={() =>
                          navigate(`/app/logs?node=${encodeURIComponent(node.name)}`)
                        }
                      >
                        <Terminal className="size-4" />
                      </Button>
                      {isAdmin && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                          aria-label={`打开节点 ${node.name} Web 终端`} title="Web 终端"
                          onClick={() => onOpenTerminal?.(node)}
                        >
                          <MonitorPlay className="size-4" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={`编辑节点 ${node.name}`} title="编辑节点"
                        onClick={() => openEditDialog(node)}
                      >
                        <Wrench className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                        aria-label={`删除节点 ${node.name}`} title="删除节点"
                        onClick={() => onDeleteNode(node)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                      <Button
                        size="sm"
                        className="ml-2"
                        disabled={triggeringNodeId === node.id}
                        onClick={() => void handleTriggerBackup(node.id, node.name)}
                      >
                        {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                        手动备份
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
});
