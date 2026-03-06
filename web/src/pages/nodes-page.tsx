import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useOutletContext, useSearchParams } from "react-router-dom";
import {
  CheckSquare,
  Download,
  FileUp,
  KeyRound,
  MoreHorizontal,
  ServerCog,
  Trash2,
} from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { NodesGrid } from "@/pages/nodes-page.grid";
import { NodesTable } from "@/pages/nodes-page.table";
import {
  escapeCSVValue,
  nodeStatusPriority,
  parseCSVRows,
  parseDateTime,
} from "@/pages/nodes-page.utils";
import { NodeEditorDialog } from "@/components/node-editor-dialog";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { SearchInput } from "@/components/ui/search-input";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { toast } from "@/components/ui/toast";
import { ViewModeToggle, type ViewMode } from "@/components/ui/view-mode-toggle";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { getErrorMessage } from "@/lib/utils";
import type { NewNodeInput, NodeRecord } from "@/types/domain";

const keywordStorageKey = "xirang.nodes.keyword";
const statusStorageKey = "xirang.nodes.status";
const tagStorageKey = "xirang.nodes.tag";
const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";
const selectedStorageKey = "xirang.nodes.selected";


export function NodesPage() {
  const navigate = useNavigate();
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
  const {
    keyword, setKeyword,
    status: statusFilter, setStatus: setStatusFilter,
    tag: tagFilter, setTag: setTagFilter,
    sort: sortBy, setSort: setSortBy,
    deferredKeyword,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: keywordStorageKey, default: "" },
    status: { key: statusStorageKey, default: "all" },
    tag: { key: tagStorageKey, default: "all" },
    sort: { key: sortStorageKey, default: "status" },
  }, globalSearch);
  const [viewMode, setViewMode] = usePersistentState<ViewMode>(
    viewStorageKey,
    "cards"
  );

  const { confirm, dialog } = useConfirm();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingNode, setEditingNode] = useState<NodeRecord | null>(null);
  const [testingNodeId, setTestingNodeId] = useState<number | null>(null);
  const [triggeringNodeId, setTriggeringNodeId] = useState<number | null>(null);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [selectedNodeId, setSelectedNodeId] = usePersistentState<number | null>(selectedStorageKey, null);
  const csvInputRef = useRef<HTMLInputElement | null>(null);

  const nodeIdSet = useMemo(() => new Set(nodes.map((n) => n.id)), [nodes]);

  useEffect(() => {
    setSelectedNodeIds((prev) => {
      const filtered = prev.filter((id) => nodeIdSet.has(id));
      return filtered.length === prev.length ? prev : filtered;
    });
  }, [nodeIdSet, setSelectedNodeIds]);

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

  const filteredNodes = useMemo(() => {
    const searchKey = deferredKeyword.trim().toLowerCase();
    return nodes.filter((node) => {
      if (statusFilter !== "all" && node.status !== statusFilter) {
        return false;
      }
      if (tagFilter !== "all" && !node.tags.includes(tagFilter)) {
        return false;
      }
      if (!searchKey) {
        return true;
      }
      const candidate = `${node.name} ${node.host} ${node.ip} ${node.username} ${node.tags.join(" ")} ${node.status}`.toLowerCase();
      return candidate.includes(searchKey);
    });
  }, [deferredKeyword, nodes, statusFilter, tagFilter]);

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

  const selectedNodeSet = useMemo(
    () => new Set(selectedNodeIds),
    [selectedNodeIds]
  );
  const allVisibleSelected =
    sortedNodes.length > 0 &&
    sortedNodes.every((node) => selectedNodeSet.has(node.id));

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
        if (result.ok) {
          toast.success(result.message);
        } else {
          toast.error(result.message);
        }
      }
    } catch (error) {
      setTestingNodeId(null);
      toast.error(getErrorMessage(error));
    }
  };

  const handleTestConnection = async (nodeId: number) => {
    const existing = nodes.find((node) => node.id === nodeId);
    if (!existing) {
      toast.error("节点记录已变更，请先保存后重试连接测试。");
      return;
    }
    try {
      await onTestNode(existing);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
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
      toast.error(getErrorMessage(error));
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
      toast.error(getErrorMessage(error));
    }
  };

  const onTestNode = async (node: NodeRecord) => {
    try {
      setTestingNodeId(node.id);
      const result = await testNodeConnection(node.id);
      setTestingNodeId(null);
      if (result.ok) {
        toast.success(`${node.name}：${result.message}`);
      } else {
        toast.error(`${node.name}：${result.message}`);
      }
    } catch (error) {
      setTestingNodeId(null);
      toast.error(getErrorMessage(error));
    }
  };

  const handleTriggerBackup = async (nodeId: number, nodeName: string) => {
    try {
      setTriggeringNodeId(nodeId);
      await triggerNodeBackup(nodeId);
      toast.success(`已触发 ${nodeName} 的手动备份任务。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setTriggeringNodeId(null);
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
            navigate("/app/ssh-keys");
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
          errors.push(`${row.name}: ${getErrorMessage(error)}`);
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
      <StatCardsSection
        items={[
          {
            title: "节点总数",
            value: nodes.length,
            description: "覆盖全部资产主机",
            tone: "info",
          },
          {
            title: "在线节点",
            value: nodeStats.online,
            description: `健康率 ${nodes.length ? Math.round((nodeStats.online / nodes.length) * 100) : 0
              }%`,
            tone: "success",
          },
          {
            title: "告警 / 离线",
            value: nodeStats.warning + nodeStats.offline,
            description: `告警 ${nodeStats.warning} · 离线 ${nodeStats.offline}`,
            tone: "warning",
          },
          {
            title: "当前筛选 / 选择",
            value: sortedNodes.length,
            description: `已选 ${selectedNodeIds.length} 个节点`,
            tone: "primary",
          },
        ]}
      />

      <Card className="overflow-hidden border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">
                主机资产管理（新增 / 编辑 / 删除 / 排序 / 测试连接）
              </CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">支持卡片与列表双视图，覆盖批量管理与节点运维</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => navigate("/app/ssh-keys")}
              >
                <KeyRound className="mr-1 size-4" />
                SSH Key 管理
              </Button>
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
                      toast.error(getErrorMessage(error))
                    );
                  event.target.value = "";
                }}
              />
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="rounded-xl border border-info/30 bg-info/10 px-3 py-2 text-xs text-info shadow-sm">
            无需在目标服务器安装客户端：仅依赖 SSH + rsync。页面中的磁盘余量来自最近一次
            SSH 探测（如远程执行
            <code className="mx-1">df</code>）快照。
          </div>

          <FilterPanel className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2.5fr_1fr_1fr_1fr_auto] items-center">
            <SearchInput
              containerClassName="w-full"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              placeholder="按名称 / 标签 / IP / 用户名 / 连接状态筛选"
              aria-label="节点关键词筛选"
            />
            <AppSelect
              className="w-full"
              aria-label="节点状态筛选"
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">全部状态</option>
              <option value="online">在线</option>
              <option value="warning">告警</option>
              <option value="offline">离线</option>
            </AppSelect>
            <AppSelect
              className="w-full"
              aria-label="节点标签筛选"
              value={tagFilter}
              onChange={(event) => setTagFilter(event.target.value)}
            >
              {tags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag === "all" ? "全部标签" : tag}
                </option>
              ))}
            </AppSelect>
            <AppSelect
              className="w-full"
              aria-label="节点排序方式"
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
            </AppSelect>
            <div className="flex items-center gap-2 justify-end col-span-full sm:col-span-2 md:col-span-3 lg:col-span-1">
              <ViewModeToggle
                value={viewMode}
                onChange={setViewMode}
                groupLabel="节点视图切换"
                cardsButtonLabel="节点卡片视图"
                listButtonLabel="节点列表视图"
              />
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="sm" variant="outline" aria-label="批量操作">
                    <MoreHorizontal className="mr-1 size-4" />
                    批量{selectedNodeIds.length > 0 ? ` (${selectedNodeIds.length})` : ""}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => toggleSelectAllVisible(!allVisibleSelected)}>
                    <CheckSquare className="mr-2 size-4" />
                    {allVisibleSelected ? "取消全选" : "全选当前筛选"}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={!selectedNodeIds.length}
                    onClick={() => setSelectedNodeIds([])}
                  >
                    清空选择
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    disabled={!selectedNodeIds.length}
                    className="text-destructive focus:text-destructive"
                    onClick={() => void handleBulkDelete()}
                  >
                    <Trash2 className="mr-2 size-4" />
                    批量删除 ({selectedNodeIds.length})
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button size="sm" variant="outline" onClick={resetFilters}>
                重置
              </Button>
            </div>
          </FilterPanel>

          <FilterSummary filtered={sortedNodes.length} total={nodes.length} unit="个节点" />

          {viewMode === "cards" ? (
            <NodesGrid
              loading={loading}
              sortedNodes={sortedNodes}
              sshKeys={sshKeys}
              selectedNodeSet={selectedNodeSet}
              selectedNodeId={selectedNodeId}
              selectedNodeIds={selectedNodeIds}
              allVisibleSelected={allVisibleSelected}
              testingNodeId={testingNodeId}
              triggeringNodeId={triggeringNodeId}
              toggleNodeSelection={toggleNodeSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
              setSelectedNodeId={setSelectedNodeId}
              handleBulkDelete={handleBulkDelete}
              resetFilters={resetFilters}
              openCreateDialog={openCreateDialog}
              openEditDialog={openEditDialog}
              onTestNode={onTestNode}
              onDeleteNode={onDeleteNode}
              handleTriggerBackup={handleTriggerBackup}
            />
          ) : (
            <NodesTable
              loading={loading}
              sortedNodes={sortedNodes}
              sshKeys={sshKeys}
              selectedNodeSet={selectedNodeSet}
              selectedNodeId={selectedNodeId}
              selectedNodeIds={selectedNodeIds}
              allVisibleSelected={allVisibleSelected}
              testingNodeId={testingNodeId}
              triggeringNodeId={triggeringNodeId}
              toggleNodeSelection={toggleNodeSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
              setSelectedNodeId={setSelectedNodeId}
              handleBulkDelete={handleBulkDelete}
              resetFilters={resetFilters}
              openCreateDialog={openCreateDialog}
              openEditDialog={openEditDialog}
              onTestNode={onTestNode}
              onDeleteNode={onDeleteNode}
              handleTriggerBackup={handleTriggerBackup}
            />
          )}
        </CardContent>
      </Card>

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
