import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useSharedContext } from "@/context/shared-context";
import { useNodesContext } from "@/context/nodes-context";
import { useSSHKeysContext } from "@/context/ssh-keys-context";
import {
  escapeCSVValue,
  nodeStatusPriority,
  parseCSVRows,
  parseDateTime,
} from "@/pages/nodes-page.utils";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { getErrorMessage } from "@/lib/utils";
import type { NewNodeInput, NodeRecord } from "@/types/domain";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import type { ViewMode } from "@/components/ui/view-mode-toggle";

const keywordStorageKey = "xirang.nodes.keyword";
const statusStorageKey = "xirang.nodes.status";
const tagStorageKey = "xirang.nodes.tag";
const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";
const groupViewStorageKey = "xirang.nodes.groupView";
const selectedStorageKey = "xirang.nodes.selected";

export function useNodesPageState() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { token, role } = useAuth();
  const isAdmin = role === "admin";
  const { loading, globalSearch, setGlobalSearch } = useSharedContext();
  const {
    nodes,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,
    refreshNodes,
  } = useNodesContext();
  const { sshKeys, refreshSSHKeys } = useSSHKeysContext();

  useEffect(() => {
    void refreshNodes();
    void refreshSSHKeys();
  }, [refreshNodes, refreshSSHKeys]);

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
  const handleEditorOpenChange = (open: boolean) => {
    setEditorOpen(open);
    if (!open) {
      setEditingNode(null);
    }
  };
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
  }, [groupView, sortedNodes, t]);

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

  return {
    // auth
    token,
    isAdmin,
    // data
    nodes,
    sshKeys,
    loading,
    // filters
    keyword,
    setKeyword,
    statusFilter,
    setStatusFilter,
    tagFilter,
    setTagFilter,
    sortBy,
    setSortBy,
    tags,
    resetFilters,
    // view
    viewMode,
    setViewMode,
    groupView,
    setGroupView,
    // computed
    nodeStats,
    sortedNodes,
    groupedNodes,
    selectedNodeSet,
    allVisibleSelected,
    // selection
    selectedNodeId,
    setSelectedNodeId,
    selectedNodeIds,
    setSelectedNodeIds,
    // node ids for loading states
    testingNodeId,
    triggeringNodeId,
    emergencyNodeId,
    // editor
    editorOpen,
    handleEditorOpenChange,
    editingNode,
    // terminal
    terminalNode,
    setTerminalNode,
    terminalKey,
    setTerminalKey,
    // file browser
    fileBrowserNode,
    setFileBrowserNode,
    fileBrowserTab,
    setFileBrowserTab,
    // batch
    batchCmdOpen,
    setBatchCmdOpen,
    batchResultId,
    setBatchResultId,
    batchRetain,
    setBatchRetain,
    // migrate
    migrateSourceNode,
    setMigrateSourceNode,
    // csv
    csvInputRef,
    // confirm dialog
    dialog,
    // refresh
    refreshNodes,
    // handlers
    openCreateDialog,
    openEditDialog,
    handleSaveNode,
    handleTestConnection,
    onDeleteNode,
    toggleNodeSelection,
    toggleSelectAllVisible,
    handleBulkDelete,
    onTestNode,
    handleTriggerBackup,
    handleImportCSV,
    handleExportCSV,
    handleEmergencyBackup,
    handleDownloadTemplate,
  };
}

export type NodesPageState = ReturnType<typeof useNodesPageState>;
