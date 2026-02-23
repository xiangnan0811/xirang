import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useOutletContext, useSearchParams } from "react-router-dom";
import {
  CheckSquare,
  Download,
  FileUp,
  LayoutGrid,
  List,
  Search,
  ServerCog,
  KeyRound,
  Trash2,
  Wrench,
} from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { MobileNodeSearchDrawer } from "@/pages/nodes-page.components";
import {
  escapeCSVValue,
  nodeStatusPriority,
  parseCSVRows,
  parseDateTime,
} from "@/pages/nodes-page.utils";
import { NodeEditorDialog } from "@/components/node-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { toast } from "@/components/ui/toast";
import { StatusPulse } from "@/components/status-pulse";
import { useConfirm } from "@/hooks/use-confirm";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { getNodeStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { NewNodeInput, NodeRecord } from "@/types/domain";

const keywordStorageKey = "xirang.nodes.keyword";
const statusStorageKey = "xirang.nodes.status";
const tagStorageKey = "xirang.nodes.tag";
const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";
const selectedStorageKey = "xirang.nodes.selected";

export function NodesPage() {
  const [searchParams] = useSearchParams();
  const {
    nodes,
    sshKeys,
    loading,
    globalSearch,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,
  } = useOutletContext<ConsoleOutletContext>();

  const queryKeyword = searchParams.get("keyword") ?? "";
  const [keyword, setKeyword] =
    usePersistentState<string>(keywordStorageKey, queryKeyword || "");
  const [statusFilter, setStatusFilter] =
    usePersistentState<"all" | "online" | "warning" | "offline">(
      statusStorageKey,
      "all"
    );
  const [tagFilter, setTagFilter] = usePersistentState<string>(tagStorageKey, "all");
  const [sortBy, setSortBy] =
    usePersistentState<"status" | "name-asc" | "name-desc" | "disk-low" | "backup-recent">(
      sortStorageKey,
      "status"
    );
  const [viewMode, setViewMode] = usePersistentState<"cards" | "list">(
    viewStorageKey,
    "cards"
  );

  const { confirm, dialog } = useConfirm();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingNode, setEditingNode] = useState<NodeRecord | null>(null);
  const [showSearchDrawer, setShowSearchDrawer] = useState(false);
  const [testingNodeId, setTestingNodeId] = useState<number | null>(null);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [selectedNodeId, setSelectedNodeId] = usePersistentState<number | null>(selectedStorageKey, null);
  const csvInputRef = useRef<HTMLInputElement | null>(null);


  useEffect(() => {
    setSelectedNodeIds((prev) =>
      prev.filter((id) => nodes.some((node) => node.id === id))
    );
  }, [nodes]);

  useEffect(() => {
    if (queryKeyword) {
      setKeyword(queryKeyword);
    }
  }, [queryKeyword, setKeyword]);

  const tags = useMemo(
    () => ["all", ...Array.from(new Set(nodes.flatMap((node) => node.tags)))],
    [nodes]
  );

  const nodeStats = useMemo(() => {
    let online = 0;
    let warning = 0;
    let offline = 0;
    for (const node of nodes) {
      if (node.status === "online") {
        online += 1;
      } else if (node.status === "warning") {
        warning += 1;
      } else {
        offline += 1;
      }
    }
    return { online, warning, offline };
  }, [nodes]);

  const effectiveKeyword = keyword || globalSearch;

  const filteredNodes = useMemo(() => {
    return nodes.filter((node) => {
      if (statusFilter !== "all" && node.status !== statusFilter) {
        return false;
      }
      if (tagFilter !== "all" && !node.tags.includes(tagFilter)) {
        return false;
      }
      if (!effectiveKeyword.trim()) {
        return true;
      }
      const candidate =
        `${node.name} ${node.host} ${node.ip} ${node.username} ${node.tags.join(" ")} ${node.status}`
          .toLowerCase()
          .trim();
      return candidate.includes(effectiveKeyword.trim().toLowerCase());
    });
  }, [effectiveKeyword, nodes, statusFilter, tagFilter]);

  const sortedNodes = useMemo(() => {
    const list = [...filteredNodes];
    list.sort((first, second) => {
      if (sortBy === "status") {
        const rankGap =
          nodeStatusPriority[second.status] - nodeStatusPriority[first.status];
        if (rankGap !== 0) {
          return rankGap;
        }
        return first.name.localeCompare(second.name);
      }
      if (sortBy === "name-asc") {
        return first.name.localeCompare(second.name);
      }
      if (sortBy === "name-desc") {
        return second.name.localeCompare(first.name);
      }
      if (sortBy === "disk-low") {
        return first.diskFreePercent - second.diskFreePercent;
      }
      return (
        parseDateTime(second.lastBackupAt) - parseDateTime(first.lastBackupAt)
      );
    });
    return list;
  }, [filteredNodes, sortBy]);

  useEffect(() => {
    if (!sortedNodes.length) {
      if (selectedNodeId !== null) {
        setSelectedNodeId(null);
      }
      return;
    }
    if (selectedNodeId === null || !sortedNodes.some((node) => node.id === selectedNodeId)) {
      setSelectedNodeId(sortedNodes[0].id);
    }
  }, [selectedNodeId, setSelectedNodeId, sortedNodes]);

  const selectedNode = useMemo(
    () => sortedNodes.find((node) => node.id === selectedNodeId) ?? null,
    [selectedNodeId, sortedNodes]
  );

  const selectedNodeSet = useMemo(
    () => new Set(selectedNodeIds),
    [selectedNodeIds]
  );
  const allVisibleSelected =
    sortedNodes.length > 0 &&
    sortedNodes.every((node) => selectedNodeSet.has(node.id));

  const resetFilters = () => {
    setKeyword("");
    setStatusFilter("all");
    setTagFilter("all");
    setSortBy("status");
  };

  const openCreateDialog = () => {
    setEditingNode(null);
    setEditorOpen(true);
  };

  const openEditDialog = (node: NodeRecord) => {
    setEditingNode(node);
    setEditorOpen(true);
  };

  const handleSaveNode = async (input: NewNodeInput, nodeId?: number) => {
    if (!input.name.trim() || !input.host.trim() || !input.username.trim()) {
      toast.error("保存失败：节点名称、主机地址、用户名不能为空。");
      return;
    }
    if (
      input.authType === "key" &&
      input.inlinePrivateKey !== undefined &&
      !input.inlinePrivateKey.trim()
    ) {
      toast.error("保存失败：请填写新 SSH Key 的私钥内容。");
      return;
    }

    let savedNodeId = nodeId;

    try {
      if (nodeId) {
        await updateNode(nodeId, input);
        toast.success(`节点 ${input.name} 已更新。`);
      } else {
        savedNodeId = await createNode(input);
        toast.success(`节点 ${input.name} 已新增。`);
      }

      setEditorOpen(false);
      setEditingNode(null);

      if (savedNodeId) {
        setTestingNodeId(savedNodeId);
        const result = await testNodeConnection(savedNodeId);
        setTestingNodeId(null);
        toast.success(result.message);
      }
    } catch (error) {
      setTestingNodeId(null);
      toast.error((error as Error).message);
    }
  };

  const handleTestConnection = async (nodeId: number) => {
    const existing = nodes.find((node) => node.id === nodeId);
    if (!existing) {
      toast.error("节点记录已变更，请先保存后重试连接测试。");
      return;
    }
    await onTestNode(existing);
  };

  const onDeleteNode = async (node: NodeRecord) => {
    const ok = await confirm({
      title: "确认操作",
      description: `确认删除节点 ${node.name} 吗？此操作会移除关联任务记录。`,
    });
    if (!ok) {
      return;
    }
    try {
      await deleteNode(node.id);
      setSelectedNodeIds((prev) => prev.filter((id) => id !== node.id));
      toast.success(`节点 ${node.name} 已删除。`);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const toggleNodeSelection = (nodeId: number, checked: boolean) => {
    setSelectedNodeIds((prev) => {
      if (checked) {
        if (prev.includes(nodeId)) {
          return prev;
        }
        return [...prev, nodeId];
      }
      return prev.filter((id) => id !== nodeId);
    });
  };

  const toggleSelectAllVisible = (checked: boolean) => {
    if (checked) {
      setSelectedNodeIds((prev) =>
        Array.from(new Set([...prev, ...sortedNodes.map((node) => node.id)]))
      );
      return;
    }
    const visibleIDs = new Set(sortedNodes.map((node) => node.id));
    setSelectedNodeIds((prev) => prev.filter((id) => !visibleIDs.has(id)));
  };

  const handleBulkDelete = async () => {
    if (!selectedNodeIds.length) {
      toast.error("请先选择至少一个节点。");
      return;
    }

    const ok = await confirm({
      title: "确认操作",
      description: `确认批量删除 ${selectedNodeIds.length} 个节点吗？此操作会移除关联任务与告警记录。`,
    });
    if (!ok) {
      return;
    }

    try {
      const result = await deleteNodes(selectedNodeIds);
      setSelectedNodeIds([]);
      if (result.notFoundIds.length > 0) {
        toast.success(
          `批量删除完成：成功 ${result.deleted}，未找到 ${result.notFoundIds.length} 个节点。`
        );
      } else {
        toast.success(`已批量删除 ${result.deleted} 个节点。`);
      }
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const onTestNode = async (node: NodeRecord) => {
    try {
      setTestingNodeId(node.id);
      const result = await testNodeConnection(node.id);
      setTestingNodeId(null);
      toast.success(`${node.name}：${result.message}`);
    } catch (error) {
      setTestingNodeId(null);
      toast.error((error as Error).message);
    }
  };

  const handleTriggerBackup = async (nodeId: number, nodeName: string) => {
    try {
      await triggerNodeBackup(nodeId);
      toast.success(`已触发 ${nodeName} 的手动备份任务。`);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const handleImportCSV = async (content: string) => {
    const rows = parseCSVRows(content);
    if (!rows.length) {
      toast.error(
        "未解析到有效节点记录，请检查 CSV 格式（name,host,username,port,tags）。"
      );
      return;
    }

    const defaultKeyID = sshKeys[0]?.id ?? null;
    if (!defaultKeyID) {
      toast.error("批量导入前请先创建 SSH Key。出于安全考虑，CSV 导入仅支持密钥认证。", {
        action: {
          label: "去创建",
          onClick: () => {
            window.location.href = "/app/ssh-keys";
          }
        }
      });
      return;
    }

    let successCount = 0;
    let failedCount = 0;
    const errors: string[] = [];

    for (const row of rows) {
      try {
        await createNode({
          name: row.name,
          host: row.host,
          username: row.username,
          port: row.port,
          tags: row.tags,
          authType: "key",
          keyId: defaultKeyID,
          password: undefined,
          basePath: "/",
        });
        successCount += 1;
      } catch (error) {
        failedCount += 1;
        if (errors.length < 3) {
          errors.push(`${row.name}: ${(error as Error).message}`);
        }
      }
    }

    const summary = `批量导入完成：成功 ${successCount}，失败 ${failedCount}。`;
    toast.success(errors.length ? `${summary} ${errors.join(" | ")}` : summary);
  };

  const handleExportCSV = () => {
    const lines = ["name,host,username,port,tags,status,last_backup_at"];
    for (const node of sortedNodes) {
      lines.push(
        [
          escapeCSVValue(node.name),
          escapeCSVValue(node.host),
          escapeCSVValue(node.username),
          String(node.port),
          escapeCSVValue(node.tags.join(",")),
          node.status,
          escapeCSVValue(node.lastBackupAt),
        ].join(",")
      );
    }
    const blob = new Blob([lines.join("\n")], {
      type: "text/csv;charset=utf-8",
    });
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = `xirang-nodes-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.csv`;
    link.click();
    URL.revokeObjectURL(link.href);
    toast.success(`已导出 ${sortedNodes.length} 条节点记录。`);
  };

  const handleDownloadTemplate = () => {
    const template = [
      "name,host,username,port,tags",
      "prod-app-01,10.10.0.11,root,22,prod|app",
      "prod-db-01,10.10.0.21,root,22,prod|db",
    ].join("\n");
    const blob = new Blob([template], { type: "text/csv;charset=utf-8" });
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = "xirang-nodes-template.csv";
    link.click();
    URL.revokeObjectURL(link.href);
    toast.success("已下载节点导入模板。");
  };

  return (
    <div className="animate-fade-in space-y-5">
      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Card className="border-emerald-500/25 bg-gradient-to-br from-emerald-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">节点总数</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{nodes.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">覆盖全部资产主机</p>
          </CardContent>
        </Card>

        <Card className="border-brand-life/25 bg-gradient-to-br from-brand-life/15 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">在线节点</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{nodeStats.online}</p>
            <p className="mt-1 text-xs text-muted-foreground">健康率 {nodes.length ? Math.round((nodeStats.online / nodes.length) * 100) : 0}%</p>
          </CardContent>
        </Card>

        <Card className="border-amber-500/25 bg-gradient-to-br from-amber-500/15 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">告警 / 离线</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{nodeStats.warning + nodeStats.offline}</p>
            <p className="mt-1 text-xs text-muted-foreground">告警 {nodeStats.warning} · 离线 {nodeStats.offline}</p>
          </CardContent>
        </Card>

        <Card className="border-cyan-500/25 bg-gradient-to-br from-cyan-500/15 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">当前筛选 / 选择</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{sortedNodes.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">已选 {selectedNodeIds.length} 个节点</p>
          </CardContent>
        </Card>
      </section>

      <Card className="overflow-hidden border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">
              主机资产管理（新增 / 编辑 / 删除 / 排序 / 测试连接）
              </CardTitle>
              <p className="mt-1 text-xs text-muted-foreground">支持卡片与列表双视图，覆盖批量管理与节点运维</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                className="md:hidden"
                onClick={() => setShowSearchDrawer(true)}
              >
                <Search className="mr-1 size-4" />
                侧滑搜索
              </Button>
              <Link to="/app/ssh-keys">
                <Button variant="outline" size="sm">
                  <KeyRound className="mr-1 size-4" />
                  SSH Key 管理
                </Button>
              </Link>
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  csvInputRef.current?.click();
                }}
              >
                <FileUp className="mr-1 size-4" />
                CSV 导入
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={handleDownloadTemplate}
              >
                <Download className="mr-1 size-4" />
                模板
              </Button>
              <Button variant="outline" size="sm" onClick={handleExportCSV}>
                <Download className="mr-1 size-4" />
                CSV 导出
              </Button>
              <Button size="sm" onClick={openCreateDialog}>
                <ServerCog className="mr-1 size-4" />
                新增节点
              </Button>
              <input
                ref={csvInputRef}
                type="file"
                accept=".csv,text/csv"
                className="hidden"
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  if (!file) {
                    return;
                  }
                  void file
                    .text()
                    .then((content) => handleImportCSV(content))
                    .catch((error) =>
                      toast.error((error as Error).message)
                    );
                  event.target.value = "";
                }}
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="rounded-lg border border-cyan-500/30 bg-cyan-500/10 px-3 py-2 text-xs text-cyan-700 shadow-sm dark:text-cyan-300">
            无需在目标服务器安装客户端：仅依赖 SSH + rsync。页面中的磁盘余量来自最近一次
            SSH 探测（如远程执行
            <code className="mx-1">df</code>）快照。
          </div>

          <div className="filter-panel sticky-filter hidden flex-wrap items-center gap-2 md:flex">
            <div className="relative min-w-[220px] flex-1 md:basis-[280px]">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-9"
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder="按名称 / 标签 / IP / 用户名 / 连接状态筛选"
              />
            </div>
            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">全部状态</option>
              <option value="online">在线</option>
              <option value="warning">告警</option>
              <option value="offline">离线</option>
            </select>
            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
              value={tagFilter}
              onChange={(event) => setTagFilter(event.target.value)}
            >
              {tags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag === "all" ? "全部标签" : tag}
                </option>
              ))}
            </select>
            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
              value={sortBy}
              onChange={(event) =>
                setSortBy(event.target.value as typeof sortBy)
              }
            >
              <option value="status">按异常优先</option>
              <option value="name-asc">名称 A-Z</option>
              <option value="name-desc">名称 Z-A</option>
              <option value="disk-low">磁盘余量升序</option>
              <option value="backup-recent">最近备份优先</option>
            </select>
            <div className="inline-flex items-center gap-1 rounded-lg border border-border/80 bg-background/80 p-1 md:ml-auto">
              <Button
                size="sm"
                variant={viewMode === "cards" ? "default" : "ghost"}
                onClick={() => setViewMode("cards")}
              >
                <LayoutGrid className="mr-1 size-4" />
                卡片
              </Button>
              <Button
                size="sm"
                variant={viewMode === "list" ? "default" : "ghost"}
                onClick={() => setViewMode("list")}
              >
                <List className="mr-1 size-4" />
                列表
              </Button>
            </div>
            <Button size="sm" variant="outline" onClick={resetFilters}>
              重置筛选
            </Button>
          </div>

          <div className="hidden flex-wrap items-center justify-between gap-2 rounded-lg border border-border/75 bg-muted/20 px-3 py-2 text-xs text-muted-foreground md:flex">
            <div className="inline-flex items-center gap-2">
              <input
                type="checkbox"
                className="size-4"
                checked={allVisibleSelected}
                onChange={(event) =>
                  toggleSelectAllVisible(event.target.checked)
                }
              />
              <span>
                已选 {selectedNodeIds.length} 个 · 当前筛选{" "}
                {sortedNodes.length} 个节点
              </span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => toggleSelectAllVisible(!allVisibleSelected)}
              >
                <CheckSquare className="mr-1 size-4" />
                {allVisibleSelected ? "取消全选" : "全选当前筛选"}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={!selectedNodeIds.length}
                onClick={() => setSelectedNodeIds([])}
              >
                清空选择
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={!selectedNodeIds.length}
                onClick={() => void handleBulkDelete()}
              >
                批量删除 ({selectedNodeIds.length})
              </Button>
            </div>
          </div>

          {viewMode === "cards" ? (
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
                <EmptyState
                  title="当前筛选条件下暂无节点"
                  description="可以重置筛选条件，或新增一个节点继续测试。"
                  action={(
                    <div className="flex flex-wrap items-center justify-center gap-2">
                      <Button size="sm" variant="outline" onClick={resetFilters}>
                        重置筛选
                      </Button>
                      <Button size="sm" onClick={openCreateDialog}>
                        <ServerCog className="mr-1 size-4" />
                        新增节点
                      </Button>
                    </div>
                  )}
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
                      "interactive-surface p-3 transition-colors",
                      selectedNode?.id === node.id && "border-primary/45 ring-1 ring-primary/40"
                    )}
                    onClick={() => setSelectedNodeId(node.id)}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                        <input
                          type="checkbox"
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

                    <div className="mt-3 grid grid-cols-3 gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-10"
                        onClick={() => void onTestNode(node)}
                        disabled={testingNodeId === node.id}
                      >
                        {testingNodeId === node.id ? "探测中" : "测试"}
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-10"
                        onClick={() => openEditDialog(node)}
                      >
                        编辑
                      </Button>
                      <Button
                        variant="danger"
                        size="sm"
                        className="h-10"
                        onClick={() => onDeleteNode(node)}
                      >
                        删除
                      </Button>
                    </div>

                    <div className="mt-2 grid grid-cols-2 gap-2">
                      <Button
                        size="sm"
                        className="h-10"
                        onClick={() =>
                          void handleTriggerBackup(node.id, node.name)
                        }
                      >
                        手动备份
                      </Button>
                      <Link to={`/app/logs?node=${encodeURIComponent(node.name)}`}>
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-10 w-full"
                        >
                          查看日志
                        </Button>
                      </Link>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="hidden overflow-x-auto rounded-2xl border border-border/70 bg-background/55 shadow-sm md:block">
              <table className="min-w-[1280px] text-left text-sm">
                <thead>
                  <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
                    <th className="px-3 py-2.5">
                      <input
                        type="checkbox"
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
                      <td
                        className="px-3 py-4 text-muted-foreground"
                        colSpan={9}
                      >
                        节点数据加载中...
                      </td>
                    </tr>
                  ) : !sortedNodes.length ? (
                    <tr>
                      <td
                        className="px-3 py-4 text-muted-foreground"
                        colSpan={9}
                      >
                        当前筛选条件下暂无节点。
                      </td>
                    </tr>
                  ) : (
                    sortedNodes.map((node) => {
                      const status = getNodeStatusMeta(node.status);
                      const keyLabel = node.keyId
                        ? sshKeys.find((key) => key.id === node.keyId)?.name ||
                          "已绑定 Key"
                        : "未绑定";

                      return (
                        <tr key={node.id} className="border-b border-border/60 transition-colors hover:bg-accent/35">
                          <td className="px-3 py-2.5">
                            <input
                              type="checkbox"
                              className="size-4"
                              checked={selectedNodeSet.has(node.id)}
                              onChange={(event) =>
                                toggleNodeSelection(
                                  node.id,
                                  event.target.checked
                                )
                              }
                            />
                          </td>
                          <td className="px-3 py-2.5">
                            <p className="font-medium">{node.name}</p>
                            <p className="text-xs text-muted-foreground">
                              成功率 {node.successRate}%
                            </p>
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
                                  className="h-2 rounded-full bg-emerald-500"
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
                            <div className="flex justify-end gap-2">
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => void onTestNode(node)}
                                disabled={testingNodeId === node.id}
                              >
                                {testingNodeId === node.id
                                  ? "探测中"
                                  : "测试连接"}
                              </Button>
                              <Link to={`/app/logs?node=${encodeURIComponent(node.name)}`}>
                                <Button variant="outline" size="sm">
                                  日志
                                </Button>
                              </Link>
                              <Button
                                size="sm"
                                onClick={() =>
                                  void handleTriggerBackup(
                                    node.id,
                                    node.name
                                  )
                                }
                              >
                                手动备份
                              </Button>
                              <Button
                                variant="outline"
                                size="sm"
                                aria-label={`编辑节点 ${node.name}`}
                                onClick={() => openEditDialog(node)}
                              >
                                <Wrench className="size-4" />
                              </Button>
                              <Button
                                variant="danger"
                                size="sm"
                                aria-label={`删除节点 ${node.name}`}
                                onClick={() => onDeleteNode(node)}
                              >
                                <Trash2 className="size-4" />
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
          )}

            <div className="space-y-2 p-2 md:hidden">
            <div className="grid grid-cols-3 gap-2">
              <select
                className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
                value={statusFilter}
                onChange={(event) =>
                  setStatusFilter(event.target.value as typeof statusFilter)
                }
              >
                <option value="all">全部状态</option>
                <option value="online">在线</option>
                <option value="warning">告警</option>
                <option value="offline">离线</option>
              </select>
              <select
                className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
                value={tagFilter}
                onChange={(event) => setTagFilter(event.target.value)}
              >
                {tags.map((tag) => (
                  <option key={tag} value={tag}>
                    {tag === "all" ? "全部标签" : tag}
                  </option>
                ))}
              </select>
              <select
                className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
                value={sortBy}
                onChange={(event) =>
                  setSortBy(event.target.value as typeof sortBy)
                }
              >
                <option value="status">异常优先</option>
                <option value="name-asc">名称 A-Z</option>
                <option value="disk-low">磁盘余量</option>
                <option value="backup-recent">最近备份</option>
              </select>
            </div>

            <div className="grid grid-cols-2 gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => toggleSelectAllVisible(!allVisibleSelected)}
              >
                {allVisibleSelected ? "取消全选" : "全选当前"}
              </Button>
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

                  <div className="mt-3 grid grid-cols-3 gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-10"
                      onClick={() => void onTestNode(node)}
                      disabled={testingNodeId === node.id}
                    >
                      测试
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-10"
                      onClick={() => openEditDialog(node)}
                    >
                      编辑
                    </Button>
                    <Button
                      variant="danger"
                      size="sm"
                      className="h-10"
                      onClick={() => onDeleteNode(node)}
                    >
                      删除
                    </Button>
                  </div>

                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <Button
                      size="sm"
                      className="h-10"
                      onClick={() =>
                        void handleTriggerBackup(node.id, node.name)
                      }
                    >
                      手动备份
                    </Button>
                    <Link to={`/app/logs?node=${encodeURIComponent(node.name)}`}>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-10 w-full"
                      >
                        查看日志
                      </Button>
                    </Link>
                  </div>
                </div>
              );
            })}
          </div>
        </CardContent>
      </Card>

      <MobileNodeSearchDrawer
        open={showSearchDrawer}
        keyword={keyword}
        nodes={sortedNodes}
        onKeywordChange={setKeyword}
        onClose={() => setShowSearchDrawer(false)}
        onPickNode={(name) => {
          setKeyword(name);
          setShowSearchDrawer(false);
        }}
      />

      <NodeEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        editingNode={editingNode}
        sshKeys={sshKeys}
        onSave={handleSaveNode}
        onTestConnection={handleTestConnection}
      />

      {dialog}
    </div>
  );
}
