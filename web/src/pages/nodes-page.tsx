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
  TerminalSquare,
  Trash2,
  Wrench,
} from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
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
import { toast } from "@/components/ui/toast";
import { StatusPulse } from "@/components/status-pulse";
import { useConfirm } from "@/hooks/use-confirm";
import { getNodeStatusMeta } from "@/lib/status";
import type { NewNodeInput, NodeRecord } from "@/types/domain";

const statusPriority: Record<NodeRecord["status"], number> = {
  offline: 3,
  warning: 2,
  online: 1,
};

const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";

function parseDateTime(input: string) {
  const value = Date.parse(input);
  return Number.isNaN(value) ? 0 : value;
}

type CSVNodeRow = {
  name: string;
  host: string;
  username: string;
  port: number;
  tags: string;
};

function escapeCSVValue(value: string): string {
  if (value.includes(",") || value.includes("\"") || value.includes("\n")) {
    return `"${value.replace(/"/g, "\"\"")}"`;
  }
  return value;
}

function parseCSVRows(content: string): CSVNodeRow[] {
  const lines = content
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (!lines.length) {
    return [];
  }

  const first = lines[0].toLowerCase();
  const hasHeader = first.includes("name") && first.includes("host");
  const body = hasHeader ? lines.slice(1) : lines;

  return body
    .map((line) => {
      const [name = "", host = "", username = "root", portRaw = "22", tags = ""] = line
        .split(",")
        .map((one) => one.trim());
      const port = Number(portRaw);
      if (!name || !host) {
        return null;
      }
      return {
        name,
        host,
        username: username || "root",
        port: Number.isFinite(port) && port > 0 ? port : 22,
        tags,
      } as CSVNodeRow;
    })
    .filter((item): item is CSVNodeRow => Boolean(item));
}

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
    execNodeCommand,
  } = useOutletContext<ConsoleOutletContext>();

  const queryKeyword = searchParams.get("keyword") ?? "";
  const [keyword, setKeyword] = useState(queryKeyword || globalSearch);
  const [statusFilter, setStatusFilter] = useState<
    "all" | "online" | "warning" | "offline"
  >("all");
  const [tagFilter, setTagFilter] = useState("all");
  const [sortBy, setSortBy] = useState<
    "status" | "name-asc" | "name-desc" | "disk-low" | "backup-recent"
  >(() => {
    const stored = localStorage.getItem(sortStorageKey);
    if (
      stored === "name-asc" ||
      stored === "name-desc" ||
      stored === "disk-low" ||
      stored === "backup-recent" ||
      stored === "status"
    ) {
      return stored;
    }
    return "status";
  });
  const [viewMode, setViewMode] = useState<"cards" | "list">(() => {
    const stored = localStorage.getItem(viewStorageKey);
    return stored === "list" ? "list" : "cards";
  });

  const { confirm, dialog } = useConfirm();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingNode, setEditingNode] = useState<NodeRecord | null>(null);
  const [showSearchDrawer, setShowSearchDrawer] = useState(false);
  const [testingNodeId, setTestingNodeId] = useState<number | null>(null);
  const [terminalNodeId, setTerminalNodeId] = useState<number | null>(null);
  const [terminalCommand, setTerminalCommand] = useState("hostname && uptime");
  const [terminalTimeout, setTerminalTimeout] = useState(20);
  const [terminalOutput, setTerminalOutput] = useState("");
  const [terminalRunning, setTerminalRunning] = useState(false);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const csvInputRef = useRef<HTMLInputElement | null>(null);

  const terminalNode = useMemo(
    () => nodes.find((item) => item.id === terminalNodeId) ?? null,
    [nodes, terminalNodeId]
  );

  useEffect(() => {
    localStorage.setItem(sortStorageKey, sortBy);
  }, [sortBy]);

  useEffect(() => {
    localStorage.setItem(viewStorageKey, viewMode);
  }, [viewMode]);

  useEffect(() => {
    setSelectedNodeIds((prev) =>
      prev.filter((id) => nodes.some((node) => node.id === id))
    );
  }, [nodes]);

  useEffect(() => {
    if (queryKeyword) {
      setKeyword(queryKeyword);
    }
  }, [queryKeyword]);

  const tags = useMemo(
    () => ["all", ...Array.from(new Set(nodes.flatMap((node) => node.tags)))],
    [nodes]
  );

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
          statusPriority[second.status] - statusPriority[first.status];
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

  const handleOpenTerminal = (node: NodeRecord) => {
    setTerminalNodeId(node.id);
    setTerminalOutput(
      `$ 连接 ${node.name} (${node.host}:${node.port})\n# 可输入远程命令并执行`
    );
    setTerminalCommand("hostname && uptime");
  };

  const handleRunTerminalCommand = async () => {
    if (!terminalNode) {
      toast.error("请先选择节点。");
      return;
    }
    if (!terminalCommand.trim()) {
      toast.error("请输入要执行的命令。");
      return;
    }

    setTerminalRunning(true);
    try {
      const result = await execNodeCommand(
        terminalNode.id,
        terminalCommand,
        terminalTimeout
      );
      const lines = [
        `$ ${terminalCommand}`,
        result.output || "(无输出)",
        `# 退出码: ${result.exitCode} · 耗时: ${result.durationMs} ms`,
      ];
      setTerminalOutput(lines.join("\n"));
      toast.success(`${terminalNode.name}：${result.message}`);
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setTerminalRunning(false);
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
    <div className="animate-fade-in space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">
              主机资产管理（新增 / 编辑 / 删除 / 排序 / 测试连接）
            </CardTitle>
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
          <div className="rounded-md border border-cyan-500/30 bg-cyan-500/10 px-3 py-2 text-xs text-cyan-700 dark:text-cyan-300">
            无需在目标服务器安装客户端：仅依赖 SSH + rsync。页面中的磁盘余量来自最近一次
            SSH 探测（如远程执行
            <code className="mx-1">df</code>）快照。
          </div>

          <div className="hidden items-center gap-2 md:flex">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-9"
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder="按名称 / 标签 / IP / 用户名 / 连接状态筛选"
              />
            </div>
            <select
              className="h-10 rounded-md border bg-background px-3 text-sm"
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
              className="h-10 rounded-md border bg-background px-3 text-sm"
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
              className="h-10 rounded-md border bg-background px-3 text-sm"
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
            <div className="inline-flex items-center gap-1 rounded-md border bg-background p-1">
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
          </div>

          <div className="hidden items-center justify-between rounded-lg border bg-muted/20 px-3 py-2 text-xs text-muted-foreground md:flex">
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
            <div className="flex items-center gap-2">
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
                <div className="rounded-lg border p-4 text-sm text-muted-foreground">
                  节点数据加载中...
                </div>
              ) : null}

              {!loading && !sortedNodes.length ? (
                <EmptyState title="当前筛选条件下暂无节点" />
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
                    className="rounded-lg border bg-background p-3"
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
                        {node.host}:{node.port} · {node.username}
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
                      <p>
                        标签：{node.tags.length ? node.tags.join(" / ") : "-"}
                      </p>
                    </div>

                    <div className="mt-3 grid grid-cols-3 gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => void onTestNode(node)}
                        disabled={testingNodeId === node.id}
                      >
                        {testingNodeId === node.id ? "探测中" : "测试"}
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => openEditDialog(node)}
                      >
                        编辑
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => onDeleteNode(node)}
                      >
                        删除
                      </Button>
                    </div>

                    <div className="mt-2 grid grid-cols-2 gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => handleOpenTerminal(node)}
                      >
                        <TerminalSquare className="mr-1 size-4" />
                        终端
                      </Button>
                      <Button
                        size="sm"
                        onClick={() =>
                          void handleTriggerBackup(node.id, node.name)
                        }
                      >
                        手动备份
                      </Button>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="hidden overflow-x-auto rounded-lg border md:block">
              <table className="min-w-[1280px] text-left text-sm">
                <thead>
                  <tr className="border-b bg-muted/40 text-muted-foreground">
                    <th className="px-3 py-3">
                      <input
                        type="checkbox"
                        className="size-4"
                        checked={allVisibleSelected}
                        onChange={(event) =>
                          toggleSelectAllVisible(event.target.checked)
                        }
                      />
                    </th>
                    <th className="px-3 py-3">节点</th>
                    <th className="px-3 py-3">地址</th>
                    <th className="px-3 py-3">认证</th>
                    <th className="px-3 py-3">状态</th>
                    <th className="px-3 py-3">磁盘探测</th>
                    <th className="px-3 py-3">最后备份</th>
                    <th className="px-3 py-3">标签</th>
                    <th className="px-3 py-3 text-right">操作</th>
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
                        <tr key={node.id} className="border-b">
                          <td className="px-3 py-3">
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
                          <td className="px-3 py-3">
                            <p className="font-medium">{node.name}</p>
                            <p className="text-xs text-muted-foreground">
                              成功率 {node.successRate}%
                            </p>
                          </td>
                          <td className="px-3 py-3 text-muted-foreground">
                            <p>
                              {node.host}:{node.port}
                            </p>
                            <p className="text-xs">{node.username}</p>
                          </td>
                          <td className="px-3 py-3 text-xs text-muted-foreground">
                            <p>
                              {node.authType === "key" ? "密钥" : "密码"}
                            </p>
                            <p>
                              {node.authType === "key" ? keyLabel : "-"}
                            </p>
                          </td>
                          <td className="px-3 py-3">
                            <div className="inline-flex items-center gap-1.5">
                              <StatusPulse tone={node.status} />
                              <Badge variant={status.variant}>
                                {status.label}
                              </Badge>
                            </div>
                          </td>
                          <td className="px-3 py-3">
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
                          <td className="px-3 py-3 text-muted-foreground">
                            {node.lastBackupAt}
                          </td>
                          <td className="px-3 py-3">
                            <div className="flex flex-wrap gap-1">
                              {node.tags.map((tag) => (
                                <Badge key={tag} variant="outline">
                                  {tag}
                                </Badge>
                              ))}
                            </div>
                          </td>
                          <td className="px-3 py-3 text-right">
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
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => handleOpenTerminal(node)}
                              >
                                终端
                              </Button>
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
                                onClick={() => openEditDialog(node)}
                              >
                                <Wrench className="size-4" />
                              </Button>
                              <Button
                                variant="outline"
                                size="sm"
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
                className="h-10 rounded-md border bg-background px-2 text-sm"
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
                className="h-10 rounded-md border bg-background px-2 text-sm"
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
                className="h-10 rounded-md border bg-background px-2 text-sm"
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
                  className="rounded-lg border bg-background p-3"
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
                      {node.host}:{node.port} · {node.username}
                    </p>
                  </div>

                  <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                    <p>
                      磁盘余量：{node.diskFreePercent}%（探测：
                      {node.diskProbeAt || "未探测"}）
                    </p>
                    <p>最后备份：{node.lastBackupAt}</p>
                    <p>标签：{node.tags.join(" / ") || "-"}</p>
                  </div>

                  <div className="mt-3 grid grid-cols-3 gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => void onTestNode(node)}
                      disabled={testingNodeId === node.id}
                    >
                      测试
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => openEditDialog(node)}
                    >
                      编辑
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onDeleteNode(node)}
                    >
                      删除
                    </Button>
                  </div>

                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleOpenTerminal(node)}
                    >
                      <TerminalSquare className="mr-1 size-4" />
                      终端
                    </Button>
                    <Button
                      size="sm"
                      onClick={() =>
                        void handleTriggerBackup(node.id, node.name)
                      }
                    >
                      手动备份
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </CardContent>
      </Card>

      {terminalNode ? (
        <Card className="border-cyan-500/30">
          <CardHeader>
            <div className="flex items-center justify-between gap-2">
              <CardTitle className="text-base">
                节点终端（真实 SSH 命令执行）
              </CardTitle>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setTerminalNodeId(null)}
              >
                关闭
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              当前节点：{terminalNode.name} · {terminalNode.host}:
              {terminalNode.port}
            </p>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="grid gap-2 md:grid-cols-[1fr_auto_auto]">
              <Input
                value={terminalCommand}
                onChange={(event) => setTerminalCommand(event.target.value)}
                placeholder="输入命令，例如 df -h /"
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    void handleRunTerminalCommand();
                  }
                }}
              />
              <select
                className="h-10 rounded-md border bg-background px-3 text-sm"
                value={terminalTimeout}
                onChange={(event) =>
                  setTerminalTimeout(Number(event.target.value || 20))
                }
              >
                <option value={10}>超时 10s</option>
                <option value={20}>超时 20s</option>
                <option value={30}>超时 30s</option>
                <option value={60}>超时 60s</option>
              </select>
              <Button
                onClick={() => void handleRunTerminalCommand()}
                disabled={terminalRunning}
              >
                <TerminalSquare className="mr-1 size-4" />
                {terminalRunning ? "执行中..." : "执行命令"}
              </Button>
            </div>

            <div className="terminal-surface min-h-52 overflow-auto rounded-lg p-3 font-mono text-xs text-slate-100">
              <pre className="whitespace-pre-wrap break-all">
                {terminalOutput || "等待命令执行输出..."}
              </pre>
            </div>
          </CardContent>
        </Card>
      ) : null}

      {showSearchDrawer ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            className="absolute inset-0 bg-black/45"
            onClick={() => setShowSearchDrawer(false)}
          />
          <section className="absolute right-0 top-0 h-full w-[86%] border-l bg-background p-4 shadow-2xl">
            <h3 className="text-sm font-semibold">侧滑全局搜索</h3>
            <p className="mt-1 text-xs text-muted-foreground">
              通过名称或 IP 快速定位任意主机
            </p>
            <Input
              className="mt-3"
              placeholder="搜索主机"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />
            <div className="mt-3 space-y-2 overflow-auto">
              {sortedNodes.map((node) => (
                <button
                  key={node.id}
                  onClick={() => {
                    setKeyword(node.name);
                    setShowSearchDrawer(false);
                  }}
                  className="w-full rounded-md border px-3 py-2 text-left"
                >
                  <p className="text-sm font-medium">{node.name}</p>
                  <p className="text-xs text-muted-foreground">
                    {node.host}:{node.port}
                  </p>
                </button>
              ))}
            </div>
          </section>
        </div>
      ) : null}

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
