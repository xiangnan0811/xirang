import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { AuthRole } from "@/context/auth-context.shared";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useNodesContext } from "@/context/nodes-context.hooks";
import { useSSHKeysContext } from "@/context/ssh-keys-context.hooks";
import { compareLastBackupDesc, nodeStatusPriority } from "@/pages/nodes-page.utils";
import { getNodeLastTestStatus, type NodeLastTestStatus } from "@/lib/status";
import {
  buildNodesCSV,
  CSV_IMPORT_BASE_PATH,
  describeCSVNodeRowError,
  importCSVNodeRows,
  NODES_CSV_TEMPLATE,
  parseCSVNodeRows,
} from "@/pages/nodes-page.csv";
import { toast } from "@/components/ui/toast-sonner";
import { dialogOpenerFromTarget } from "@/components/ui/dialog-opener";
import { isAbortError } from "@/hooks/inventory-request-state";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { sensitiveStepUpBlock } from "@/lib/sensitive-step-up";
import { getErrorMessage } from "@/lib/utils";
import type { NewNodeInput, NodeConnectionProbeOutcome, NodeDoctorResult, NodeHostKeyInfo, NodeHostKeyIssueCode, NodeRecord, NodeSaveResult, TaskRecord } from "@/types/domain";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { isNodeProbeStaleError } from "@/lib/api/nodes-api";
import { getAuthSessionGeneration, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { ViewMode } from "@/components/ui/view-mode-toggle";

const keywordStorageKey = "xirang.nodes.keyword";
const statusStorageKey = "xirang.nodes.status";
const tagStorageKey = "xirang.nodes.tag";
const sortStorageKey = "xirang.nodes.sort";
const viewStorageKey = "xirang.nodes.view";
const groupViewStorageKey = "xirang.nodes.groupView";

// Status sort ranks problems first; a node without any test has nothing to surface and ranks below online.
const lastTestStatusPriority: Record<NodeLastTestStatus, number> = { ...nodeStatusPriority, untested: 0 };

type HostKeyIssueState = {
  nodeId: number;
  nodeName: string;
  code: NodeHostKeyIssueCode;
  hostKey: NodeHostKeyInfo;
  /** Identity of the test that reported this host key; trust is bound to it when present. */
  probeToken?: string;
};

function policyEmergencyTaskIds(tasks: readonly TaskRecord[], nodeId: number): number[] {
  const seen = new Set<number>();
  const ids: number[] = [];
  for (const task of tasks) {
    if (task.nodeId !== nodeId || task.source !== "policy") continue;
    if (task.executorType !== "rsync" && task.executorType !== "restic" && task.executorType !== "rclone") continue;
    if (seen.has(task.id)) continue;
    seen.add(task.id);
    ids.push(task.id);
  }
  ids.sort((left, right) => left - right);
  return ids;
}

export function useNodesPageState() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { token, role, totpEnabled } = useAuth();
  const isAdmin = role === "admin";
  const canOperateNodes = role === "admin" || role === "operator";
  const canBrowseNodeFiles = canOperateNodes;
  const demoRole: AuthRole | null = !token && import.meta.env.VITE_ENABLE_DEMO_MODE === "true" ? "viewer" : null;
  const effectiveRole = role ?? demoRole;
  const roleRef = useRef(effectiveRole);
  const authEpochRef = useRef(0);
  const { globalSearch, setGlobalSearch } = useSharedContext();
  const {
    nodes,
    nodesLoading,
    nodesError,
    nodesLoaded,
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
    isFiltered: hasActiveFilters,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: keywordStorageKey, default: "" },
    status: { key: statusStorageKey, default: "all" },
    tag: { key: tagStorageKey, default: "all" },
    sort: { key: sortStorageKey, default: "status" },
  }, globalSearch, setGlobalSearch);

  useEffect(() => {
    if (sortBy === "disk-low") {
      setSortBy("status");
    }
  }, [setSortBy, sortBy]);

  const [viewMode, setViewMode] = usePersistentState<ViewMode>(
    viewStorageKey,
    "cards"
  );
  const [groupView, setGroupView] = usePersistentState<boolean>(
    groupViewStorageKey,
    false
  );

  const { confirm, dialog, cancelPending } = useConfirm();
  const cancelPendingRef = useRef(cancelPending);
  useLayoutEffect(() => {
    cancelPendingRef.current = cancelPending;
  }, [cancelPending]);
  const [editorOpen, setEditorOpen] = useState(false);
  const nodeEditorOpenerRef = useRef<HTMLElement | null>(null);
  // Every editor open or close starts a new page-side editor session; a reload requested by an earlier
  // session may neither report to nor seed a later one. Identity resets are fenced by captureAuthLifetime.
  const editorSessionRef = useRef(0);
  const handleEditorOpenChange = (open: boolean) => {
    editorSessionRef.current += 1;
    setEditorOpen(open);
    if (!open) {
      setEditingNode(null);
    }
  };
  const [terminalNode, setTerminalNode] = useState<NodeRecord | null>(null);
  const [terminalKey, setTerminalKey] = useState(0);
  const [fileBrowserNode, setFileBrowserNode] = useState<NodeRecord | null>(null);
  // The file browser has no Radix trigger, so remember the card button or the
  // list row's More menu button that opened it.
  const fileBrowserOpenerRef = useRef<HTMLElement | null>(null);
  const [fileBrowserTab, setFileBrowserTab] = useState<"files" | "docker">("files");
  const [editingNode, setEditingNode] = useState<NodeRecord | null>(null);
  const [testingNodeId, setTestingNodeId] = useState<number | null>(null);
  // The latest test start owns the spinner, so an older completion cannot clear a newer test's state.
  const testOperationRef = useRef(0);
  const [doctorNode, setDoctorNode] = useState<NodeRecord | null>(null);
  const [doctorResult, setDoctorResult] = useState<NodeDoctorResult | null>(null);
  const [doctorLoading, setDoctorLoading] = useState(false);
  const [doctorError, setDoctorError] = useState<string | null>(null);
  const doctorOperationRef = useRef(0);
  const doctorAbortRef = useRef<AbortController | null>(null);
  const doctorNodeIdRef = useRef<number | null>(null);
  const [hostKeyIssue, setHostKeyIssue] = useState<HostKeyIssueState | null>(null);
  const [hostKeyTrusting, setHostKeyTrusting] = useState(false);
  const [hostKeyError, setHostKeyError] = useState<string | null>(null);
  // Node whose trust request currently owns the host key dialog; a ref so async
  // probe completions observe it without stale closures.
  const hostKeyTrustOwnerRef = useRef<number | null>(null);
  // Identity of the trust request that owns the pending state. An identity change retires it, so a
  // request from an earlier session cannot release a newer dialog's pending trust (A→B→A).
  const hostKeyTrustOperationRef = useRef(0);
  const [triggeringNodeId, setTriggeringNodeId] = useState<number | null>(null);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [batchCmdOpen, setBatchCmdOpen] = useState(false);
  const [batchResultId, setBatchResultId] = useState<string | null>(null);
  const [batchRetain, setBatchRetain] = useState(false);
  const [emergencyNodeId, setEmergencyNodeId] = useState<number | null>(null);
  // Non-zero while a click owns the flow, including the confirm dialog. The
  // button's disabled state is async, so a second click must be rejected here.
  const emergencyOperationRef = useRef(0);
  const emergencyAbortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);
  const withEmergencyStepUp = useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger, {
    persist: false,
    reuseCached: false,
  });

  const [migrateSourceNode, setMigrateSourceNode] = useState<NodeRecord | null>(null);
  const csvInputRef = useRef<HTMLInputElement | null>(null);
  // A successful explicit reload read the node after the inventory row went stale. Until the inventory publishes
  // another revision for that node, a new edit session starts from the reloaded node, so a delayed or stale list
  // read cannot reopen the editor on the revision that just conflicted. Open drafts keep their own snapshot.
  const reloadedNodesRef = useRef(new Map<number, { node: NodeRecord; staleRevision: string | undefined }>());

  const [authIdentity, setAuthIdentity] = useState({ token, role });
  if (authIdentity.token !== token || authIdentity.role !== role) {
    const tokenChanged = authIdentity.token !== token;
    setAuthIdentity({ token, role });
    setTestingNodeId(null);
    setTriggeringNodeId(null);
    // 批量选择属于当前身份；切换账号或角色后不得沿用。
    setSelectedNodeIds([]);
    setEditorOpen(false);
    setEditingNode(null);
    setTerminalNode(null);
    setFileBrowserNode(null);
    setBatchCmdOpen(false);
    if (tokenChanged) {
      setBatchResultId(null);
      setBatchRetain(false);
    }
    setMigrateSourceNode(null);
    setDoctorNode(null);
    setDoctorResult(null);
    setDoctorError(null);
    setDoctorLoading(false);
    setHostKeyIssue(null);
    setHostKeyError(null);
    setHostKeyTrusting(false);
  }

  useLayoutEffect(() => {
    const reloadedNodes = reloadedNodesRef.current;
    roleRef.current = effectiveRole;
    authEpochRef.current += 1;
    mountedRef.current = true;
    hostKeyTrustOwnerRef.current = null;
    hostKeyTrustOperationRef.current += 1;
    // Reloaded rows were read under the previous identity; none may seed an editor under this one.
    reloadedNodes.clear();
    return () => {
      authEpochRef.current += 1;
      mountedRef.current = false;
      reloadedNodes.clear();
      emergencyAbortRef.current?.abort();
      cancelPendingRef.current();
    };
  }, [effectiveRole, role, token]);

  // 身份/角色变化或卸载时作废 Doctor 操作；可见状态由上面的渲染期身份重置清空。
  useLayoutEffect(() => {
    const operationRef = doctorOperationRef;
    const abortRef = doctorAbortRef;
    const nodeIdRef = doctorNodeIdRef;
    operationRef.current += 1;
    abortRef.current?.abort();
    abortRef.current = null;
    nodeIdRef.current = null;
    return () => {
      operationRef.current += 1;
      abortRef.current?.abort();
      abortRef.current = null;
      nodeIdRef.current = null;
    };
  }, [effectiveRole, role, token]);

  const captureAuthLifetime = () => {
    const authEpoch = authEpochRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    return () => mountedRef.current
      && authEpochRef.current === authEpoch
      && getAuthSessionGeneration() === sessionGeneration;
  };

  const nodeIdSet = useMemo(() => new Set(nodes.map((n) => n.id)), [nodes]);
  const latestNodesRef = useRef(nodes);
  useLayoutEffect(() => {
    latestNodesRef.current = nodes;
  }, [nodes]);

  const validSelectedNodeIds = selectedNodeIds.filter((id) => nodeIdSet.has(id));
  if (validSelectedNodeIds.length !== selectedNodeIds.length) {
    setSelectedNodeIds(validSelectedNodeIds);
  }

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
    // Revised nodes with no recorded test have no outcome yet: neither passed nor failed.
    let untested = 0;
    for (const node of nodes) {
      const status = getNodeLastTestStatus(node);
      if (status === "online") {
        online += 1;
      } else if (status === "warning") {
        warning += 1;
      } else if (status === "untested") {
        untested += 1;
      } else {
        offline += 1;
      }
    }
    return { online, warning, offline, untested };
  }, [nodes]);

  const filteredNodes = useMemo(() => {
    const searchKey = deferredKeyword.trim().toLowerCase();
    return nodes.filter((node) => {
      // Counters, filter, search and sort follow the status the badges show (latest test evidence first).
      const status = getNodeLastTestStatus(node);
      if (statusFilter !== "all" && status !== statusFilter) {
        return false;
      }
      if (tagFilter !== "all" && !node.tags.includes(tagFilter)) {
        return false;
      }
      if (!searchKey) {
        return true;
      }
      // 搜索框提示可按连接状态筛选，因此同时匹配状态码与当前语言下显示的状态文字。
      const statusLabel = t(`status.node.${status}`);
      const candidate = `${node.name} ${node.host} ${node.ip} ${node.username} ${node.tags.join(" ")} ${status} ${statusLabel}`.toLowerCase();
      return candidate.includes(searchKey);
    });
  }, [deferredKeyword, nodes, statusFilter, t, tagFilter]);

  const sortedNodes = useMemo(() => {
    const list = [...filteredNodes];
    list.sort((first, second) => {
      if (sortBy === "status") {
        const rankGap =
          lastTestStatusPriority[getNodeLastTestStatus(second)] - lastTestStatusPriority[getNodeLastTestStatus(first)];
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
      return compareLastBackupDesc(first, second);
    });
    return list;
  }, [filteredNodes, sortBy]);

  const groupedNodes = useMemo(() => {
    if (!groupView) return null;
    const groups = new Map<string, NodeRecord[]>();
    for (const node of sortedNodes) {
      const nodeTags = node.tags.length > 0 ? node.tags : [t("nodes.ungrouped")];
      for (const tag of nodeTags) {
        const group = groups.get(tag);
        if (group) {
          group.push(node);
        } else {
          groups.set(tag, [node]);
        }
      }
    }
    return [...groups].sort(([a], [b]) => a.localeCompare(b));
  }, [groupView, sortedNodes, t]);

  const selectedNodeSet = useMemo(
    () => new Set(selectedNodeIds),
    [selectedNodeIds]
  );
  const allVisibleSelected =
    sortedNodes.length > 0 &&
    sortedNodes.every((node) => selectedNodeSet.has(node.id));

  const openCreateDialog = (event?: { currentTarget: EventTarget | null }) => {
    if (roleRef.current !== "admin") {
      return;
    }
    nodeEditorOpenerRef.current = dialogOpenerFromTarget(event?.currentTarget);
    setEditingNode(null);
    handleEditorOpenChange(true);
  };

  const latestKnownNode = (node: NodeRecord): NodeRecord => {
    const reloaded = reloadedNodesRef.current.get(node.id);
    if (!reloaded) {
      return node;
    }
    if (node.revision === reloaded.staleRevision) {
      return reloaded.node;
    }
    // The inventory moved past the stale revision: its row is at least as new as the reload.
    reloadedNodesRef.current.delete(node.id);
    return node;
  };

  const openEditDialog = (node: NodeRecord, opener?: EventTarget | null) => {
    if (roleRef.current !== "admin") {
      return;
    }
    nodeEditorOpenerRef.current = dialogOpenerFromTarget(opener);
    setEditingNode(latestKnownNode(node));
    handleEditorOpenChange(true);
  };

  // A host key issue describes one test of one node configuration; a newer test or a mutation of
  // that node retires it, unless a trust request currently owns the dialog.
  const retireHostKeyIssue = (nodeIds: readonly number[]) => {
    const trustOwner = hostKeyTrustOwnerRef.current;
    setHostKeyIssue((current) => (
      current && nodeIds.includes(current.nodeId) && trustOwner !== current.nodeId ? null : current
    ));
  };

  const startNodeTest = (nodeId: number) => {
    retireHostKeyIssue([nodeId]);
    testOperationRef.current += 1;
    setTestingNodeId(nodeId);
    return testOperationRef.current;
  };

  const finishNodeTest = (operation: number) => {
    if (testOperationRef.current === operation) {
      setTestingNodeId(null);
    }
  };

  const presentProbeResult = (
    nodeId: number,
    nodeName: string,
    result: NodeConnectionProbeOutcome,
    prefixName: boolean,
  ) => {
    // A superseded result belongs to a newer test, mutation or identity: stay silent. A result the
    // server no longer accepts gets retest guidance and never reaches the host key dialog.
    if (result.stale) {
      if (result.stale === "retest") {
        retireHostKeyIssue([nodeId]);
        toast.info(t("nodes.connectionTestStale", { name: nodeName }));
      }
      return;
    }
    const message = prefixName ? `${nodeName}：${result.message}` : result.message;
    if (!result.ok && result.errorCode && result.hostKey) {
      // While a trust request owns the dialog, a concurrent probe for another
      // node must not replace it; report that node's failure as a toast instead.
      const trustOwner = hostKeyTrustOwnerRef.current;
      if (trustOwner !== null && trustOwner !== nodeId) {
        toast.error(message);
        return;
      }
      setHostKeyIssue({
        nodeId,
        nodeName,
        code: result.errorCode,
        hostKey: result.hostKey,
        ...(result.probeToken ? { probeToken: result.probeToken } : {}),
      });
      setHostKeyError(null);
      return;
    }
    // Plain results never touch the host key dialog: it belongs to whichever
    // node last reported a host key issue.
    if (result.ok) {
      toast.success(message);
    } else {
      toast.error(message);
    }
  };

  const handleSaveNode = async (input: NewNodeInput, nodeId?: number, onInlineKeyCreated?: (keyId: string) => void) => {
    if (roleRef.current !== "admin") {
      return;
    }
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

    const saveStillCurrent = captureAuthLifetime();
    let saved: NodeSaveResult;

    try {
      if (nodeId) {
        // A revision conflict rejects here and reaches the editor, which keeps the draft; nothing was written,
        // so no connection test follows.
        const updated = await updateNode(nodeId, input, saveStillCurrent, onInlineKeyCreated);
        if (!saveStillCurrent() || !updated) {
          return;
        }
        saved = updated;
        // The committed write supersedes any earlier reload of this node.
        reloadedNodesRef.current.delete(updated.id);
        toast.success(t("nodes.nodeUpdated", { name: input.name }));
      } else {
        const created = await createNode(input, saveStillCurrent, onInlineKeyCreated);
        if (!saveStillCurrent()) {
          return;
        }
        if (!created || created.id <= 0) {
          throw new Error(t("console.actionRequestFailed", { action: t("nodes.actions.createNode") }));
        }
        saved = created;
        toast.success(t("nodes.nodeCreated", { name: input.name }));
      }

      handleEditorOpenChange(false);
    } catch (error) {
      if (saveStillCurrent()) {
        throw error;
      }
      return;
    }

    // Test exactly the committed revision: if the node changes again before this test is admitted, the
    // server answers stale instead of testing settings this save did not write. Legacy servers and demo
    // return no revision and keep the unconditional test.
    const savedNodeId = saved.id;
    const savedRevision = saved.revision;
    const testOperation = startNodeTest(savedNodeId);
    const probe = savedRevision
      ? testNodeConnection(savedNodeId, { expectedRevision: savedRevision })
      : testNodeConnection(savedNodeId);
    void probe.then((result) => {
      if (saveStillCurrent()) {
        presentProbeResult(savedNodeId, input.name, result, false);
      }
    }).catch((error: unknown) => {
      if (saveStillCurrent()) {
        toast.error(getErrorMessage(error));
      }
    }).finally(() => {
      if (saveStillCurrent()) {
        finishNodeTest(testOperation);
      }
    });
  };

  // Explicit reload after a revision conflict: reads the saved node under the current token. Null once the
  // admin lifetime or the editor session that requested it has ended; such a late read is dropped without
  // reporting, remembering or refreshing. The editor additionally fences the result to its own session.
  // A newer read is remembered for later edit sessions and the inventory is refreshed to catch up with it.
  // Revisions are opaque, so when the inventory published another revision of this node while the read was
  // outstanding, neither can be shown to be newer: the reload is refused, the draft stays and a retry reads again.
  const reloadEditingNode = async (nodeId: number): Promise<NodeRecord | null> => {
    if (roleRef.current !== "admin") {
      return null;
    }
    if (!token) {
      throw new Error(t("console.notLoggedIn"));
    }
    const authStillCurrent = captureAuthLifetime();
    const editorSession = editorSessionRef.current;
    const reloadStillCurrent = () =>
      authStillCurrent() && roleRef.current === "admin" && editorSessionRef.current === editorSession;
    const listedAtStart = latestNodesRef.current.find((candidate) => candidate.id === nodeId);
    let node: NodeRecord;
    try {
      node = await apiClient.getNode(token, nodeId);
    } catch (error) {
      if (!reloadStillCurrent()) {
        return null;
      }
      throw error;
    }
    if (!reloadStillCurrent()) {
      return null;
    }
    const listed = latestNodesRef.current.find((candidate) => candidate.id === nodeId);
    if (node.id !== nodeId || !listed || listed.revision === node.revision) {
      return node;
    }
    if (!listedAtStart || listed.revision !== listedAtStart.revision) {
      throw new Error(t("nodeEditor.reloadSavedNodeFailed"));
    }
    reloadedNodesRef.current.set(nodeId, { node, staleRevision: listed.revision });
    void refreshNodes();
    return node;
  };

  const handleTestConnection = async (nodeId: number) => {
    if (roleRef.current !== "admin" && roleRef.current !== "operator") {
      return;
    }
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

  // Doctor 当前结果所有权：每次 open/run 递增代次并取消旧请求；只有同一代次、
  // 同一节点、同一身份/会话、无 barrier 且仍具操作角色的完成才能提交。
  // 即使底层请求忽略 abort，代次栅栏也独立阻止旧结果/错误/finally 覆盖新状态。
  const invalidateDoctorOperation = () => {
    doctorOperationRef.current += 1;
    doctorAbortRef.current?.abort();
    doctorAbortRef.current = null;
    doctorNodeIdRef.current = null;
  };

  const runDoctorForNode = async (node: NodeRecord) => {
    if ((roleRef.current !== "admin" && roleRef.current !== "operator") || isAuthTransitionActive()) {
      return;
    }
    invalidateDoctorOperation();
    const operation = doctorOperationRef.current;
    const controller = new AbortController();
    doctorAbortRef.current = controller;
    doctorNodeIdRef.current = node.id;
    const authStillCurrent = captureAuthLifetime();
    const requestToken = token;
    setDoctorNode(node);
    setDoctorResult(null);
    setDoctorError(null);
    setDoctorLoading(true);
    const doctorStillCurrent = () => authStillCurrent()
      && doctorOperationRef.current === operation
      && doctorNodeIdRef.current === node.id
      && !isAuthTransitionActive()
      && (roleRef.current === "admin" || roleRef.current === "operator");
    try {
      const result = requestToken
        ? await apiClient.runNodeDoctor(requestToken, node.id, { signal: controller.signal })
        : import.meta.env.VITE_ENABLE_DEMO_MODE === "true"
          ? (await import("@/data/mock")).buildMockNodeDoctorResult(node)
          : null;
      if (!doctorStillCurrent()) {
        return;
      }
      if (!result) {
        setDoctorError(t("console.notLoggedIn"));
        return;
      }
      if (result.nodeId !== node.id) {
        setDoctorError(t("nodes.doctorResultMismatch"));
        return;
      }
      setDoctorResult(result);
    } catch (error) {
      if (!doctorStillCurrent()) {
        return;
      }
      const message = getErrorMessage(error);
      setDoctorError(message);
      toast.error(message);
    } finally {
      if (doctorStillCurrent()) {
        setDoctorLoading(false);
        doctorAbortRef.current = null;
      }
    }
  };

  const openDoctor = (node: NodeRecord) => {
    void runDoctorForNode(node);
  };

  const closeDoctor = () => {
    invalidateDoctorOperation();
    setDoctorNode(null);
    setDoctorResult(null);
    setDoctorError(null);
    setDoctorLoading(false);
  };

  const handleDoctorOpenChange = (open: boolean) => {
    if (!open) {
      closeDoctor();
    }
  };

  const closeDoctorRef = useRef(closeDoctor);
  useLayoutEffect(() => {
    closeDoctorRef.current = closeDoctor;
  });
  useEffect(() => subscribeAuthTransition(() => {
    if (isAuthTransitionActive()) {
      closeDoctorRef.current();
    }
  }), []);

  const onDeleteNode = async (node: NodeRecord) => {
    if (roleRef.current !== "admin") {
      return;
    }
    const deleteStillCurrent = captureAuthLifetime();
    const ok = await confirm({
      title: t("nodes.confirmDeleteTitle"),
      description: t("nodes.confirmDeleteNodeDesc", { name: node.name }),
    });
    if (!ok || !deleteStillCurrent()) {
      return;
    }
    try {
      await deleteNode(node.id, deleteStillCurrent);
      if (!deleteStillCurrent()) {
        return;
      }
      retireHostKeyIssue([node.id]);
      setSelectedNodeIds((prev) => prev.filter((id) => id !== node.id));
      toast.success(t("nodes.nodeDeleted", { name: node.name }));
    } catch (error) {
      if (!deleteStillCurrent()) {
        return;
      }
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
    if (roleRef.current !== "admin") {
      return;
    }
    if (!selectedNodeIds.length) {
      toast.error(t("nodes.selectAtLeastOne"));
      return;
    }

    const ids = [...selectedNodeIds];
    const bulkStillCurrent = captureAuthLifetime();
    const ok = await confirm({
      title: t("nodes.bulkDeleteConfirmTitle"),
      description: t("nodes.bulkDeleteConfirmDesc", { count: ids.length }),
    });
    if (!ok || !bulkStillCurrent()) {
      return;
    }

    try {
      const result = await deleteNodes(ids, bulkStillCurrent);
      if (!bulkStillCurrent()) {
        return;
      }
      retireHostKeyIssue(ids);
      setSelectedNodeIds([]);
      if (result.notFoundIds.length > 0) {
        toast.success(
          t("nodes.bulkDeletePartial", { deleted: result.deleted, notFound: result.notFoundIds.length })
        );
      } else {
        toast.success(t("nodes.bulkDeleteSuccess", { count: result.deleted }));
      }
    } catch (error) {
      if (!bulkStillCurrent()) {
        return;
      }
      toast.error(getErrorMessage(error));
    }
  };

  const onTestNode = async (node: NodeRecord) => {
    if (roleRef.current !== "admin" && roleRef.current !== "operator") {
      return;
    }
    const authEpoch = authEpochRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const testStillCurrent = () => authEpochRef.current === authEpoch
      && getAuthSessionGeneration() === sessionGeneration
      && (roleRef.current === "admin" || roleRef.current === "operator");
    const testOperation = startNodeTest(node.id);
    try {
      const result = await testNodeConnection(node.id);
      if (!testStillCurrent()) {
        return;
      }
      finishNodeTest(testOperation);
      presentProbeResult(node.id, node.name, result, true);
    } catch (error) {
      if (!testStillCurrent()) {
        return;
      }
      finishNodeTest(testOperation);
      toast.error(getErrorMessage(error));
    }
  };

  const trustHostKey = async () => {
    const issue = hostKeyIssue;
    if (!issue) {
      return;
    }
    if (roleRef.current !== "admin") {
      return;
    }
    if (!token) {
      setHostKeyError(t("console.notLoggedIn"));
      return;
    }
    // Synchronous duplicate guard: the button's disabled state lags a render behind.
    if (hostKeyTrustOwnerRef.current !== null) {
      return;
    }
    const sessionGeneration = getAuthSessionGeneration();
    const authEpoch = authEpochRef.current;
    const trustOperation = ++hostKeyTrustOperationRef.current;
    hostKeyTrustOwnerRef.current = issue.nodeId;
    setHostKeyTrusting(true);
    setHostKeyError(null);
    const trustStillCurrent = () => mountedRef.current
      && getAuthSessionGeneration() === sessionGeneration
      && authEpochRef.current === authEpoch
      && roleRef.current === "admin";
    // Only the request that still owns the pending state may release it.
    const releaseTrust = () => {
      if (hostKeyTrustOperationRef.current !== trustOperation) {
        return false;
      }
      hostKeyTrustOwnerRef.current = null;
      if (mountedRef.current) {
        setHostKeyTrusting(false);
      }
      return true;
    };
    try {
      await apiClient.trustNodeHostKey(token, issue.nodeId, issue.hostKey.fingerprintSha256, issue.probeToken);
    } catch (error) {
      if (!releaseTrust() || !trustStillCurrent()) {
        return;
      }
      if (isNodeProbeStaleError(error)) {
        // The reviewed test no longer describes the node; nothing was trusted.
        setHostKeyIssue(null);
        setHostKeyError(null);
        toast.info(t("nodes.hostKeyTrustStale", { name: issue.nodeName }));
        return;
      }
      setHostKeyError(getErrorMessage(error));
      return;
    }
    if (!releaseTrust() || !trustStillCurrent()) {
      return;
    }
    toast.success(t("nodes.hostKeyTrusted"));
    setHostKeyIssue(null);
    const retestNodeId = issue.nodeId;
    const testOperation = startNodeTest(retestNodeId);
    try {
      const result = await testNodeConnection(retestNodeId);
      if (!trustStillCurrent()) {
        return;
      }
      presentProbeResult(retestNodeId, issue.nodeName, result, true);
    } catch (error) {
      if (!trustStillCurrent()) {
        return;
      }
      // The dialog is already closed once trust succeeded; report retest failures as a toast.
      toast.error(`${issue.nodeName}：${getErrorMessage(error)}`);
    } finally {
      if (trustStillCurrent()) {
        finishNodeTest(testOperation);
      }
    }
  };

  const handleHostKeyOpenChange = (open: boolean) => {
    // Keep the dialog bound to its pending trust request so the outcome
    // (error or success) cannot land on a different node's dialog.
    if (open || hostKeyTrusting) {
      return;
    }
    setHostKeyIssue(null);
    setHostKeyError(null);
  };

  const openHostKeySettings = () => {
    handleHostKeyOpenChange(false);
    navigate("/app/settings?tab=system");
  };

  const handleTriggerBackup = async (nodeId: number, nodeName: string) => {
    if (roleRef.current !== "admin" && roleRef.current !== "operator") {
      return;
    }
    const authEpoch = authEpochRef.current;
    const backupStillCurrent = () => authEpochRef.current === authEpoch
      && (roleRef.current === "admin" || roleRef.current === "operator");
    try {
      setTriggeringNodeId(nodeId);
      await triggerNodeBackup(nodeId);
      if (!backupStillCurrent()) {
        return;
      }
      toast.success(t("nodes.backupTriggered", { name: nodeName }));
    } catch (error) {
      if (!backupStillCurrent()) {
        return;
      }
      toast.error(getErrorMessage(error));
    } finally {
      if (backupStillCurrent()) {
        setTriggeringNodeId(null);
      }
    }
  };

  const handleImportCSV = async (file: File) => {
    if (roleRef.current !== "admin") {
      return;
    }
    const importStillCurrent = captureAuthLifetime();
    let content: string;
    try {
      content = await file.text();
    } catch (error) {
      if (!importStillCurrent()) {
        return;
      }
      toast.error(getErrorMessage(error));
      return;
    }
    if (!importStillCurrent()) {
      return;
    }
    const { rows, errors } = parseCSVNodeRows(content);
    const diagnostics = errors.slice(0, 3).map((error) => describeCSVNodeRowError(error, t));
    if (!rows.length) {
      toast.error(diagnostics.join(" | ") || t("nodes.csvImportEmpty"));
      return;
    }

    const key = sshKeys[0];
    if (!key) {
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

    const confirmed = await confirm({
      title: t("nodes.csvImportConfirmTitle"),
      description: t("nodes.csvImportConfirmDesc", { key: key.name, path: CSV_IMPORT_BASE_PATH }),
    });
    if (!confirmed || !importStillCurrent()) {
      return;
    }

    const outcome = await importCSVNodeRows(
      rows,
      key.id,
      async (input, isCurrent) => (await createNode(input, isCurrent))?.id ?? -1,
      importStillCurrent,
    );
    if (outcome.status === "cancelled" || !importStillCurrent()) {
      return;
    }

    const failed = errors.length + outcome.failures.length;
    for (const { name, error } of outcome.failures.slice(0, 3 - diagnostics.length)) {
      diagnostics.push(error === null
        ? t("nodes.csvImportRowFailed", { name })
        : `${name}: ${getErrorMessage(error)}`);
    }
    const summary = t("nodes.csvImportSummary", { success: outcome.succeeded, failed });
    if (failed === 0) {
      toast.success(summary);
    } else if (outcome.succeeded === 0) {
      toast.error(`${summary} ${diagnostics.join(" | ")}`);
    } else {
      toast.warning(`${summary} ${diagnostics.join(" | ")}`);
    }
  };

  const handleExportCSV = () => {
    const blob = new Blob([buildNodesCSV(sortedNodes)], {
      type: "text/csv;charset=utf-8",
    });
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = `xirang-nodes-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.csv`;
    link.click();
    URL.revokeObjectURL(link.href);
    toast.success(t("nodes.csvExported", { count: sortedNodes.length }));
  };

  const openTerminal = (node: NodeRecord) => {
    if (roleRef.current !== "admin") {
      return;
    }
    setTerminalNode(node);
    setTerminalKey((key) => key + 1);
  };

  const openFileBrowser = (node: NodeRecord, opener?: EventTarget | null) => {
    if (roleRef.current !== "admin" && roleRef.current !== "operator") {
      return;
    }
    fileBrowserOpenerRef.current = dialogOpenerFromTarget(opener);
    setFileBrowserNode(node);
  };

  const openBatchCommand = (open: boolean) => {
    if (open && roleRef.current !== "admin" && roleRef.current !== "operator") {
      return;
    }
    setBatchCmdOpen(open);
  };

  const openMigrate = (node: NodeRecord | null) => {
    if (node && roleRef.current !== "admin") {
      return;
    }
    setMigrateSourceNode(node);
  };

  const handleEmergencyBackup = async (nodeId: number, nodeName: string) => {
    if (roleRef.current !== "admin" && roleRef.current !== "operator") return;
    if (!token || emergencyOperationRef.current !== 0) return;
    if (sensitiveStepUpBlock({ token, totpEnabled }) !== "ready") return;
    const operationId = emergencyOperationRef.current + 1;
    emergencyOperationRef.current = operationId;
    const sessionGeneration = getAuthSessionGeneration();
    const authEpoch = authEpochRef.current;
    const capturedToken = token;
    let controller: AbortController | null = null;
    const isCurrentAttempt = () => mountedRef.current
      && emergencyOperationRef.current === operationId
      && getAuthSessionGeneration() === sessionGeneration
      && authEpochRef.current === authEpoch
      && !isAuthTransitionActive()
      && (roleRef.current === "admin" || roleRef.current === "operator");

    try {
      const ok = await confirm({
        title: t("nodes.emergencyBackupConfirmTitle"),
        description: t("nodes.emergencyBackupConfirmDesc", { name: nodeName }),
      });
      if (!ok || !isCurrentAttempt()) return;

      setEmergencyNodeId(nodeId);
      controller = new AbortController();
      emergencyAbortRef.current = controller;
      const tasks = await apiClient.getTasks(capturedToken, { signal: controller.signal });
      if (!isCurrentAttempt()) return;

      const taskIds = policyEmergencyTaskIds(tasks, nodeId);
      await withEmergencyStepUp(async (proof) => {
        try {
          if (!isCurrentAttempt()) return;
          for (const taskId of taskIds) {
            if (!isCurrentAttempt()) return;
            await apiClient.requestTaskManualTriggerCredentialGrant(capturedToken, {
              taskId,
              reason: t("tasks.manualTriggerGrantReason", { id: taskId }),
              requestedTtlSeconds: 600,
            }, proof);
          }
          if (!isCurrentAttempt()) return;
          const result = await apiClient.emergencyBackup(capturedToken, nodeId, proof);
          if (!isCurrentAttempt()) return;
          const summary = t("nodes.emergencyBackupTriggered", { count: result.triggered });
          if (result.errors.length > 0) {
            toast.error(`${summary} ${result.errors.join(" | ")}`);
            return;
          }
          toast.success(summary);
        } catch (error) {
          // A rejection that lands after unmount or session change must not reach
          // useStepUpAction, which would open a global OTP for the abandoned operation.
          if (!isCurrentAttempt()) return;
          throw error;
        }
      });
    } catch (error) {
      if (!isCurrentAttempt() || isAbortError(error)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (controller && emergencyAbortRef.current === controller) {
        emergencyAbortRef.current = null;
      }
      if (emergencyOperationRef.current === operationId) {
        emergencyOperationRef.current = 0;
        if (mountedRef.current) setEmergencyNodeId(null);
      }
    }
  };

  const handleDownloadTemplate = () => {
    const blob = new Blob([NODES_CSV_TEMPLATE], { type: "text/csv;charset=utf-8" });
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
    totpEnabled,
    isAdmin,
    canOperateNodes,
    canBrowseNodeFiles,
    // data
    nodes,
    sshKeys,
    loading: (nodesLoading || !nodesLoaded) && nodes.length === 0 && !nodesError,
    requestFailed: Boolean(nodesError) && nodes.length === 0,
    inventoryEmpty: nodesLoaded && !nodesLoading && !nodesError && nodes.length === 0,
    nodesError,
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
    hasActiveFilters,
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
    selectedNodeIds,
    setSelectedNodeIds,
    // node ids for loading states
    testingNodeId,
    doctorNodeId: doctorLoading ? doctorNode?.id ?? null : null,
    triggeringNodeId,
    emergencyNodeId,
    // editor
    editorOpen,
    handleEditorOpenChange,
    nodeEditorOpenerRef,
    editingNode,
    // terminal
    terminalNode,
    setTerminalNode,
    openTerminal,
    terminalKey,
    setTerminalKey,
    // doctor
    doctorNode,
    doctorResult,
    doctorLoading,
    doctorError,
    handleDoctorOpenChange,
    runDoctorForNode,
    hostKeyIssue,
    hostKeyTrusting,
    hostKeyError,
    trustHostKey,
    openHostKeySettings,
    handleHostKeyOpenChange,
    // file browser
    fileBrowserNode,
    setFileBrowserNode,
    openFileBrowser,
    fileBrowserOpenerRef,
    fileBrowserTab,
    setFileBrowserTab,
    // batch
    batchCmdOpen,
    setBatchCmdOpen,
    openBatchCommand,
    batchResultId,
    setBatchResultId,
    batchRetain,
    setBatchRetain,
    // migrate
    migrateSourceNode,
    setMigrateSourceNode,
    openMigrate,
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
    reloadEditingNode,
    handleTestConnection,
    openDoctor,
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
