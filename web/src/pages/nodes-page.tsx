import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useOutletContext, useSearchParams } from "react-router-dom";
import {
  CheckSquare,
  Download,
  FileUp,
  Layers,
  MoreHorizontal,
  ServerCog,
  Terminal,
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
import { BatchCommandDialog } from "@/components/batch-command-dialog";
import { BatchResultDialog } from "@/components/batch-result-dialog";
import { NodeEditorDialog } from "@/components/node-editor-dialog";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
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
import { useAuth } from "@/context/auth-context";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

import { DockerVolumesPanel } from "@/components/docker-volumes-panel";
import { FileBrowser } from "@/components/file-browser";
import { createFilesApi } from "@/lib/api/files-api";
import { apiClient } from "@/lib/api/client";

const filesApi = createFilesApi();

const WebTerminal = lazy(() => import("@/components/web-terminal"));

const keywordStorageKey = "xirang.nodes.keyword";
const statusStorageKey = "xirang.nodes.status";
const tagStorageKey = "xirang.nodes.tag";
const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";
const groupViewStorageKey = "xirang.nodes.groupView";
const selectedStorageKey = "xirang.nodes.selected";


export function NodesPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { token, role } = useAuth();
  const isAdmin = role === "admin";
  const {
    nodes,
    sshKeys,
    loading,
    globalSearch,
    setGlobalSearch,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,
    refreshNodes,
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshNodes();
  }, [refreshNodes]);

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
  }, globalSearch, setGlobalSearch);
  const [viewMode, setViewMode] = usePersistentState<ViewMode>(
    viewStorageKey,
    "cards"
  );
  const [groupView, setGroupView] = usePersistentState<boolean>(
    groupViewStorageKey,
    false
  );

  const { confirm, dialog } = useConfirm();
  const [editorOpen, setEditorOpen] = useState(false);
  const [terminalNode, setTerminalNode] = useState<NodeRecord | null>(null);
  const [terminalKey, setTerminalKey] = useState(0);
  const [fileBrowserNode, setFileBrowserNode] = useState<NodeRecord | null>(null);
  const [fileBrowserTab, setFileBrowserTab] = useState<"files" | "docker">("files");
  const [editingNode, setEditingNode] = useState<NodeRecord | null>(null);
  const [testingNodeId, setTestingNodeId] = useState<number | null>(null);
  const [triggeringNodeId, setTriggeringNodeId] = useState<number | null>(null);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [batchCmdOpen, setBatchCmdOpen] = useState(false);
  const [batchResultId, setBatchResultId] = useState<string | null>(null);
  const [batchRetain, setBatchRetain] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = usePersistentState<number | null>(selectedStorageKey, null);
  const [emergencyNodeId, setEmergencyNodeId] = useState<number | null>(null);
  const [migrateSourceNode, setMigrateSourceNode] = useState<NodeRecord | null>(null);
  const [migrateTargetId, setMigrateTargetId] = useState<number | null>(null);
  const [migratingNode, setMigratingNode] = useState(false);
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
      // 消费后清除 URL 参数，避免与重置按钮冲突
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev);
        next.delete("keyword");
        return next;
      }, { replace: true });
    }
  }, [queryKeyword, setKeyword, setSearchParams]);

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

  const groupedNodes = useMemo(() => {
    if (!groupView) return null;
    const groups: Record<string, typeof sortedNodes> = {};
    for (const node of sortedNodes) {
      const nodeTags = node.tags.length > 0 ? node.tags : [t("nodes.ungrouped")];
      for (const tag of nodeTags) {
        if (!groups[tag]) {
          groups[tag] = [];
        }
        groups[tag].push(node);
      }
    }
    return Object.entries(groups).sort(([a], [b]) => a.localeCompare(b));
  }, [groupView, sortedNodes]);

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
      toast.error(t("nodes.saveFailedEmpty"));
      return;
    }
    if (
      input.authType === "key" &&
      input.inlinePrivateKey !== undefined &&
      !input.inlinePrivateKey.trim()
    ) {
      toast.error(t("nodes.saveFailedKeyEmpty"));
      return;
    }

    let savedNodeId = nodeId;

    try {
      if (nodeId) {
        await updateNode(nodeId, input);
        toast.success(t("nodes.nodeUpdated", { name: input.name }));
      } else {
        savedNodeId = await createNode(input);
        toast.success(t("nodes.nodeCreated", { name: input.name }));
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
      toast.error(t("nodes.nodeChangedRetry"));
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
      title: t("nodes.confirmDeleteTitle"),
      description: t("nodes.confirmDeleteNodeDesc", { name: node.name }),
    });
    if (!ok) {
      return;
    }
    try {
      await deleteNode(node.id);
      setSelectedNodeIds((prev) => prev.filter((id) => id !== node.id));
      toast.success(t("nodes.nodeDeleted", { name: node.name }));
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
      toast.error(t("nodes.selectAtLeastOne"));
      return;
    }

    const ok = await confirm({
      title: t("nodes.bulkDeleteConfirmTitle"),
      description: t("nodes.bulkDeleteConfirmDesc", { count: selectedNodeIds.length }),
    });
    if (!ok) {
      return;
    }

    try {
      const result = await deleteNodes(selectedNodeIds);
      setSelectedNodeIds([]);
      if (result.notFoundIds.length > 0) {
        toast.success(
          t("nodes.bulkDeletePartial", { deleted: result.deleted, notFound: result.notFoundIds.length })
        );
      } else {
        toast.success(t("nodes.bulkDeleteSuccess", { count: result.deleted }));
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
      toast.success(t("nodes.backupTriggered", { name: nodeName }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setTriggeringNodeId(null);
    }
  };

  const handleImportCSV = async (content: string) => {
    const rows = parseCSVRows(content);
    if (!rows.length) {
      toast.error(t("nodes.csvImportEmpty"));
      return;
    }

    const defaultKeyID = sshKeys[0]?.id ?? null;
    if (!defaultKeyID) {
      toast.error(t("nodes.csvImportNeedKey"), {
        action: {
          label: t("nodes.csvImportNeedKeyAction"),
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

    const summary = t("nodes.csvImportSummary", { success: successCount, failed: failedCount });
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
    toast.success(t("nodes.csvExported", { count: sortedNodes.length }));
  };

  const handleEmergencyBackup = async (nodeId: number, nodeName: string) => {
    if (!token) return;
    const ok = await confirm({
      title: t("nodes.emergencyBackupConfirmTitle"),
      description: t("nodes.emergencyBackupConfirmDesc", { name: nodeName }),
    });
    if (!ok) return;
    try {
      setEmergencyNodeId(nodeId);
      const result = await apiClient.emergencyBackup(token, nodeId);
      toast.success(t("nodes.emergencyBackupTriggered", { count: result.triggered }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setEmergencyNodeId(null);
    }
  };

  const handleMigrateNode = async () => {
    if (!token || !migrateSourceNode || !migrateTargetId) return;
    try {
      setMigratingNode(true);
      const result = await apiClient.migrateNode(token, migrateSourceNode.id, migrateTargetId);
      toast.success(
        t("nodes.migrateSuccess", { policies: result.migratedPolicies, tasks: result.migratedTasks })
      );
      setMigrateSourceNode(null);
      setMigrateTargetId(null);
      void refreshNodes();
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setMigratingNode(false);
    }
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
    toast.success(t("nodes.templateDownloaded"));
  };

  return (
    <div className="animate-fade-in space-y-5">
      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("nodes.totalNodes"),
            value: nodes.length,
            description: t("nodes.totalNodesDesc"),
            tone: "info",
          },
          {
            title: t("nodes.onlineNodes"),
            value: nodeStats.online,
            description: t("nodes.onlineNodesDesc", {
              rate: nodes.length ? Math.round((nodeStats.online / nodes.length) * 100) : 0,
            }),
            tone: "success",
          },
          {
            title: t("nodes.warningOffline"),
            value: nodeStats.warning + nodeStats.offline,
            description: t("nodes.warningOfflineDesc", { warning: nodeStats.warning, offline: nodeStats.offline }),
            tone: "warning",
          },
          {
            title: t("nodes.filterSelection"),
            value: sortedNodes.length,
            description: t("nodes.selectedCount", { count: selectedNodeIds.length }),
            tone: "primary",
          },
        ]}
      />

      <Card className="overflow-hidden border-border/75">
        <CardContent className="space-y-4 pt-6">
          {/* 工具栏：左侧操作按钮 + 右侧视图/批量/重置 */}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" className="shrink-0" onClick={openCreateDialog}>
              <ServerCog className="mr-1 size-3.5" />
              {t("nodes.addNode")}
            </Button>
            {/* 移动端：收纳导入/模板/导出到下拉菜单 */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="shrink-0 md:hidden">
                  <MoreHorizontal className="mr-1 size-3.5" />
                  {t("nodes.more")}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                <DropdownMenuItem onClick={() => csvInputRef.current?.click()}>
                  <FileUp className="mr-2 size-3.5" />
                  {t("nodes.csvImport")}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={handleDownloadTemplate}>
                  <Download className="mr-2 size-3.5" />
                  {t("nodes.downloadTemplate")}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={handleExportCSV}>
                  <Download className="mr-2 size-3.5" />
                  {t("nodes.exportNodes")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            {/* 平板/桌面端：展示独立按钮 */}
            <Button
              variant="outline"
              size="sm"
              className="hidden shrink-0 md:inline-flex"
              onClick={() => {
                csvInputRef.current?.click();
              }}
            >
              <FileUp className="mr-1 size-3.5" />
              {t("nodes.csvImport")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="hidden shrink-0 md:inline-flex"
              onClick={handleDownloadTemplate}
            >
              <Download className="mr-1 size-3.5" />
              {t("nodes.templateShort")}
            </Button>
            <Button variant="outline" size="sm" className="hidden shrink-0 md:inline-flex" onClick={handleExportCSV}>
              <Download className="mr-1 size-3.5" />
              {t("nodes.exportShort")}
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
            {/* 分隔线：区分操作与视图/工具 */}
            <div className="hidden h-6 w-px bg-border/60 md:block" aria-hidden="true" />
            <ViewModeToggle
              className="hidden md:inline-flex"
              value={viewMode}
              onChange={setViewMode}
              groupLabel={t("nodes.viewToggleGroup")}
              cardsButtonLabel={t("nodes.viewCards")}
              listButtonLabel={t("nodes.viewList")}
            />
            <Button
              size="sm"
              variant={groupView ? "default" : "outline"}
              className="hidden shrink-0 md:inline-flex"
              onClick={() => setGroupView(!groupView)}
              aria-label={t("nodes.groupByTag")}
            >
              <Layers className="mr-1 size-3.5" />
              {t("nodes.groupLabel")}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button size="sm" variant="outline" aria-label={t("nodes.batchLabel")}>
                  <MoreHorizontal className="mr-1 size-4" />
                  {selectedNodeIds.length > 0 ? t("nodes.batchWithCount", { count: selectedNodeIds.length }) : t("nodes.batchLabel")}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => toggleSelectAllVisible(!allVisibleSelected)}>
                  <CheckSquare className="mr-2 size-4" />
                  {allVisibleSelected ? t("nodes.deselectAll") : t("nodes.selectAllFiltered")}
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={!selectedNodeIds.length}
                  onClick={() => setSelectedNodeIds([])}
                >
                  {t("nodes.clearSelection")}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={!selectedNodeIds.length}
                  onClick={() => setBatchCmdOpen(true)}
                >
                  <Terminal className="mr-2 size-3.5" />
                  {t("nodes.batchCommandCount", { count: selectedNodeIds.length })}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={!selectedNodeIds.length}
                  className="text-destructive focus:text-destructive"
                  onClick={() => void handleBulkDelete()}
                >
                  <Trash2 className="mr-2 size-3.5" />
                  {t("nodes.deleteCount", { count: selectedNodeIds.length })}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Button size="sm" variant="outline" onClick={resetFilters}>
              {t("nodes.reset")}
            </Button>
          </div>

          <FilterPanel sticky={false} className="grid gap-3 grid-cols-2 md:grid-cols-3 xl:grid-cols-[2fr_1fr_1fr_1fr] items-center">
            <SearchInput
              containerClassName="w-full col-span-2 md:col-span-3 xl:col-span-1"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              placeholder={t("nodes.searchPlaceholder")}
              aria-label={t("nodes.keywordAriaLabel")}
            />
            <AppSelect
              containerClassName="w-full"
              aria-label={t("nodes.statusAriaLabel")}
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">{t("nodes.allStatus")}</option>
              <option value="online">{t("nodes.statusOnline")}</option>
              <option value="warning">{t("nodes.statusWarning")}</option>
              <option value="offline">{t("nodes.statusOffline")}</option>
            </AppSelect>
            <AppSelect
              containerClassName="w-full"
              aria-label={t("nodes.tagAriaLabel")}
              value={tagFilter}
              onChange={(event) => setTagFilter(event.target.value)}
            >
              {tags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag === "all" ? t("nodes.allTags") : tag}
                </option>
              ))}
            </AppSelect>
            <AppSelect
              containerClassName="w-full col-span-2 md:col-span-1"
              aria-label={t("nodes.sortAriaLabel")}
              value={sortBy}
              onChange={(event) =>
                setSortBy(event.target.value as typeof sortBy)
              }
            >
              <option value="status">{t("nodes.sortStatus")}</option>
              <option value="name-asc">{t("nodes.sortNameAsc")}</option>
              <option value="name-desc">{t("nodes.sortNameDesc")}</option>
              <option value="disk-low">{t("nodes.sortDiskLow")}</option>
              <option value="backup-recent">{t("nodes.sortBackupRecent")}</option>
            </AppSelect>
          </FilterPanel>

          <FilterSummary filtered={sortedNodes.length} total={nodes.length} unit={t("nodes.nodeUnit")} />

          {/* 分组视图 */}
          {groupView && groupedNodes ? (
            <div className="space-y-4">
              {groupedNodes.map(([tag, tagNodes]) => (
                <details key={tag} open className="group">
                  <summary className="flex cursor-pointer items-center gap-2 rounded-md bg-muted/40 px-3 py-2 text-sm font-medium hover:bg-muted/60">
                    <Layers className="size-4 text-muted-foreground" />
                    {tag}
                    <span className="ml-auto text-xs text-muted-foreground">{t("nodes.groupNodeCount", { count: tagNodes.length })}</span>
                  </summary>
                  <div className="mt-2">
                    <NodesGrid
                      loading={loading}
                      sortedNodes={tagNodes}
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
                      onEmergencyBackup={handleEmergencyBackup}
                      emergencyNodeId={emergencyNodeId}
                      onOpenTerminal={(node) => { setTerminalNode(node); setTerminalKey((k) => k + 1); }}
                      onOpenFileBrowser={setFileBrowserNode}
                      isAdmin={isAdmin}
                    />
                  </div>
                </details>
              ))}
            </div>
          ) : (
          <>
          {/* 移动端始终显示卡片视图（viewMode 可能从桌面端持久化为 list） */}
          <div className={viewMode === "list" ? "md:hidden" : undefined}>
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
              onEmergencyBackup={handleEmergencyBackup}
              emergencyNodeId={emergencyNodeId}
              onOpenTerminal={(node) => { setTerminalNode(node); setTerminalKey((k) => k + 1); }}
              onOpenFileBrowser={setFileBrowserNode}
              isAdmin={isAdmin}
            />
          </div>
          {viewMode === "list" && (
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
              onOpenTerminal={(node) => { setTerminalNode(node); setTerminalKey((k) => k + 1); }}
              onOpenFileBrowser={setFileBrowserNode}
              isAdmin={isAdmin}
            />
          )}
          </>
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

      <Dialog
        open={terminalNode !== null}
        onOpenChange={(open) => { if (!open) setTerminalNode(null); }}
      >
        <DialogContent
          className="w-full max-w-[95vw] md:max-w-[90vw] h-[85vh] flex flex-col gap-0 p-0 resize overflow-hidden"
          onPointerDownOutside={(e) => e.preventDefault()}
          onInteractOutside={(e) => e.preventDefault()}
        >
          <DialogHeader className="px-4 pt-4 pb-2 shrink-0">
            <DialogTitle className="flex items-center justify-between">
              <span>{t("nodes.terminalTitle", { name: terminalNode?.name ?? "" })}</span>
              <DialogCloseButton />
            </DialogTitle>
            <DialogDescription className="sr-only">
              {t("nodes.terminalDesc")}
            </DialogDescription>
          </DialogHeader>
          <div className="flex-1 overflow-hidden px-4 pb-4">
            {terminalNode !== null && token !== null && (
              <Suspense fallback={<div className="flex h-full items-center justify-center text-sm text-muted-foreground">{t("nodes.terminalLoading")}</div>}>
                <WebTerminal
                  key={terminalKey}
                  nodeId={terminalNode.id}
                  token={token}
                  onDisconnect={() => setTerminalNode(null)}
                />
              </Suspense>
            )}
          </div>
        </DialogContent>
      </Dialog>

      {token && fileBrowserNode && (
        <Dialog
          open={fileBrowserNode !== null}
          onOpenChange={(open) => { if (!open) { setFileBrowserNode(null); setFileBrowserTab("files"); } }}
        >
          <DialogContent className="flex w-full max-w-[95vw] flex-col md:max-w-[80vw]" size="lg">
            <DialogHeader>
              <DialogTitle>{t("nodes.fileBrowserTitle", { name: fileBrowserNode.name })}</DialogTitle>
              <DialogDescription className="sr-only">
                {t("nodes.fileBrowserDesc", { name: fileBrowserNode.name })}
              </DialogDescription>
              <DialogCloseButton />
            </DialogHeader>
            <div className="flex gap-2 px-6">
              <Button
                variant={fileBrowserTab === "files" ? "default" : "outline"}
                size="sm"
                onClick={() => setFileBrowserTab("files")}
              >
                {t("nodes.tabFiles")}
              </Button>
              <Button
                variant={fileBrowserTab === "docker" ? "default" : "outline"}
                size="sm"
                onClick={() => setFileBrowserTab("docker")}
              >
                {t("nodes.tabDockerVolumes")}
              </Button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-6 thin-scrollbar">
              {fileBrowserTab === "files" ? (
                <FileBrowser
                  rootPath={
                    fileBrowserNode.basePath && fileBrowserNode.basePath !== "/"
                      ? fileBrowserNode.basePath
                      : fileBrowserNode.username === "root"
                        ? "/root"
                        : `/home/${fileBrowserNode.username}`
                  }
                  fetchDir={(path, signal) =>
                    filesApi.listNodeFiles(token, fileBrowserNode.id, path, { signal })
                  }
                  fetchContent={(path) =>
                    filesApi.getNodeFileContent(token, fileBrowserNode.id, path)
                  }
                />
              ) : (
                <DockerVolumesPanel
                  nodeId={fileBrowserNode.id}
                  token={token}
                />
              )}
            </div>
          </DialogContent>
        </Dialog>
      )}

      {token && (
        <>
          <BatchCommandDialog
            open={batchCmdOpen}
            onOpenChange={setBatchCmdOpen}
            nodes={nodes}
            token={token}
            defaultNodeIds={selectedNodeIds}
            onSuccess={(result) => {
              setBatchResultId(result.batchId);
              setBatchRetain(result.retain);
            }}
          />
          <BatchResultDialog
            open={batchResultId !== null}
            onOpenChange={(open) => { if (!open) setBatchResultId(null); }}
            batchId={batchResultId}
            retain={batchRetain}
            token={token}
          />
        </>
      )}

      {/* 迁移节点对话框 */}
      <Dialog
        open={migrateSourceNode !== null}
        onOpenChange={(open) => {
          if (!open) {
            setMigrateSourceNode(null);
            setMigrateTargetId(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("nodes.migrateDialogTitle", { name: migrateSourceNode?.name })}</DialogTitle>
            <DialogDescription>
              {t("nodes.migrateDialogDesc")}
            </DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <div className="space-y-3 px-6 pb-6">
            <div>
              <label htmlFor="migrate-target" className="mb-1 block text-sm font-medium">
                {t("nodes.migrateTargetLabel")}
              </label>
              <AppSelect
                id="migrate-target"
                containerClassName="w-full"
                value={migrateTargetId ? String(migrateTargetId) : ""}
                onChange={(e) => setMigrateTargetId(e.target.value ? Number(e.target.value) : null)}
              >
                <option value="">{t("nodes.migrateTargetPlaceholder")}</option>
                {nodes
                  .filter((n) => n.id !== migrateSourceNode?.id)
                  .map((n) => (
                    <option key={n.id} value={String(n.id)}>
                      {n.name} ({n.host})
                    </option>
                  ))}
              </AppSelect>
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <Button
                variant="outline"
                onClick={() => {
                  setMigrateSourceNode(null);
                  setMigrateTargetId(null);
                }}
              >
                {t("common.cancel")}
              </Button>
              <Button
                disabled={!migrateTargetId || migratingNode}
                onClick={() => void handleMigrateNode()}
              >
                {migratingNode ? t("nodes.migrating") : t("nodes.confirmMigrate")}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {dialog}
    </div>
  );
}
