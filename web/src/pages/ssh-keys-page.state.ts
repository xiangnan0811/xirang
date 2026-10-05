import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useNodesContext } from "@/context/nodes-context.hooks";
import { useSSHKeysContext } from "@/context/ssh-keys-context.hooks";
import { toast } from "@/components/ui/toast-sonner";
import {
  dialogOpenerFromTarget,
  restoreConnectedDialogOpener,
} from "@/components/ui/dialog-opener";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { getErrorMessage } from "@/lib/utils";
import type { NewSSHKeyInput, NodeRecord, SSHKeyRecord } from "@/types/domain";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type ViewMode = "table" | "cards";

// ---------------------------------------------------------------------------
// Filter config (持久化到 localStorage)
// ---------------------------------------------------------------------------

const FILTER_CONFIG = {
  keyword: { key: "xirang.sshkeys.keyword", default: "" },
  keyType: { key: "xirang.sshkeys.keyType", default: "all" },
  usageStatus: { key: "xirang.sshkeys.usage", default: "all" },
  sortBy: { key: "xirang.sshkeys.sort", default: "name-asc" },
} as const;

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function useSSHKeysPageState() {
  const { t } = useTranslation();
  const { loading, globalSearch, setGlobalSearch } = useSharedContext();
  const { nodes, refreshNodes } = useNodesContext();
  const {
    sshKeys,
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    refreshSSHKeys,
  } = useSSHKeysContext();
  const { role, token } = useAuth();
  const demoRole: AuthRole | null = !token && import.meta.env.VITE_ENABLE_DEMO_MODE === "true" ? "viewer" : null;
  const effectiveRole = role ?? demoRole;
  const canManageSSHKeys = effectiveRole === "admin";
  const { confirm, dialog, cancelPending } = useConfirm();
  const mountedRef = useRef(false);
  const authIdentityRef = useRef({ role: effectiveRole, token });
  const sessionGenerationRef = useRef(0);

  useLayoutEffect(() => {
    authIdentityRef.current = { role: effectiveRole, token };
    sessionGenerationRef.current += 1;
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      sessionGenerationRef.current += 1;
      cancelPending();
    };
  }, [cancelPending, effectiveRole, token]);

  const captureSessionGeneration = useCallback(() => sessionGenerationRef.current, []);
  const isCurrentSession = useCallback(
    (generation: number) =>
      mountedRef.current &&
      sessionGenerationRef.current === generation &&
      authIdentityRef.current.role === "admin",
    [],
  );

  // 首次挂载时刷新数据
  useEffect(() => {
    void refreshSSHKeys();
    void refreshNodes();
  }, [refreshSSHKeys, refreshNodes]);

  // ----- 筛选 -----
  const {
    keyword,
    setKeyword,
    keyType: keyTypeFilter,
    setKeyType: setKeyTypeFilter,
    usageStatus: usageStatusFilter,
    setUsageStatus: setUsageStatusFilter,
    sortBy,
    setSortBy,
    deferredKeyword,
    reset: resetFilters,
    isFiltered,
  } = usePageFilters(FILTER_CONFIG, globalSearch, setGlobalSearch);

  // ----- 视图模式 -----
  const [viewMode, setViewMode] = usePersistentState<ViewMode>(
    "xirang.sshkeys.view",
    "table",
  );

  // ----- 选中状态 -----
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());

  // 当密钥列表变化时，移除已不存在的 id
  const keyIdSet = useMemo(() => new Set(sshKeys.map((k) => k.id)), [sshKeys]);
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelectedIds((prev) => {
      const next = new Set<string>();
      for (const id of prev) {
        if (keyIdSet.has(id)) next.add(id);
      }
      return next.size === prev.size ? prev : next;
    });
  }, [keyIdSet]);

  const toggleSelection = useCallback((keyId: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) {
        next.add(keyId);
      } else {
        next.delete(keyId);
      }
      return next;
    });
  }, []);

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set());
  }, []);

  // ----- 计算：密钥使用映射 -----
  const keyUsageMap = useMemo(() => {
    const map = new Map<string, NodeRecord[]>();
    for (const node of nodes) {
      if (node.keyId) {
        const list = map.get(node.keyId);
        if (list) {
          list.push(node);
        } else {
          map.set(node.keyId, [node]);
        }
      }
    }
    return map;
  }, [nodes]);

  // ----- 统计 -----
  const stats = useMemo(() => {
    let inUse = 0;
    let unused = 0;
    let linkedNodes = 0;
    for (const key of sshKeys) {
      const nodeList = keyUsageMap.get(key.id);
      if (nodeList && nodeList.length > 0) {
        inUse += 1;
        linkedNodes += nodeList.length;
      } else {
        unused += 1;
      }
    }
    return {
      total: sshKeys.length,
      inUse,
      unused,
      totalNodes: linkedNodes,
    };
  }, [sshKeys, keyUsageMap]);

  // ----- 筛选 + 排序 -----
  const filteredKeys = useMemo(() => {
    const searchKey = deferredKeyword.trim().toLowerCase();

    const filtered = sshKeys.filter((key) => {
      // 关键字匹配：名称、用户名、指纹
      if (searchKey) {
        const candidate =
          `${key.name} ${key.username} ${key.fingerprint}`.toLowerCase();
        if (!candidate.includes(searchKey)) return false;
      }
      // 密钥类型筛选
      if (keyTypeFilter !== "all" && key.keyType !== keyTypeFilter) {
        return false;
      }
      // 使用状态筛选
      if (usageStatusFilter !== "all") {
        const isUsed = keyUsageMap.has(key.id);
        if (usageStatusFilter === "in-use" && !isUsed) return false;
        if (usageStatusFilter === "unused" && isUsed) return false;
      }
      return true;
    });

    // 排序
    filtered.sort((a, b) => {
      switch (sortBy) {
        case "name-asc":
          return a.name.localeCompare(b.name);
        case "name-desc":
          return b.name.localeCompare(a.name);
        case "created": {
          const ta = a.createdAt ? new Date(a.createdAt).getTime() : 0;
          const tb = b.createdAt ? new Date(b.createdAt).getTime() : 0;
          return tb - ta; // 最新在前
        }
        case "last-used": {
          const ta = a.lastUsedAt ? new Date(a.lastUsedAt).getTime() : 0;
          const tb = b.lastUsedAt ? new Date(b.lastUsedAt).getTime() : 0;
          return tb - ta; // 最近使用在前
        }
        default:
          return 0;
      }
    });

    return filtered;
  }, [sshKeys, deferredKeyword, keyTypeFilter, usageStatusFilter, sortBy, keyUsageMap]);

  // toggleSelectAllVisible 依赖 filteredKeys（当前可见项）
  const toggleSelectAllVisible = useCallback(
    (checked: boolean) => {
      if (checked) {
        setSelectedIds((prev) => {
          const next = new Set(prev);
          for (const key of filteredKeys) next.add(key.id);
          return next;
        });
      } else {
        const visibleIds = new Set(filteredKeys.map((k) => k.id));
        setSelectedIds((prev) => {
          const next = new Set<string>();
          for (const id of prev) {
            if (!visibleIds.has(id)) next.add(id);
          }
          return next;
        });
      }
    },
    [filteredKeys],
  );

  const allVisibleSelected = useMemo(
    () =>
      filteredKeys.length > 0 &&
      filteredKeys.every((key) => selectedIds.has(key.id)),
    [filteredKeys, selectedIds],
  );

  // ----- 分页 -----
  const pagination = useClientPagination(filteredKeys);

  // ----- 对话框状态 -----
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingKey, setEditingKey] = useState<SSHKeyRecord | null>(null);
  const editorOpenerRef = useRef<HTMLElement | null>(null);
  const rotationOpenerRef = useRef<HTMLElement | null>(null);
  const editorOnCloseAutoFocus: DialogCloseAutoFocus = useCallback((event) => {
    restoreConnectedDialogOpener(event, editorOpenerRef.current);
  }, []);
  const rotationOnCloseAutoFocus: DialogCloseAutoFocus = useCallback((event) => {
    restoreConnectedDialogOpener(event, rotationOpenerRef.current);
  }, []);
  const [testConnectionKey, setTestConnectionKeyState] =
    useState<SSHKeyRecord | null>(null);
  const [associatedNodesKey, setAssociatedNodesKey] =
    useState<SSHKeyRecord | null>(null);
  const [rotationOpen, setRotationOpenState] = useState(false);
  const [rotationKey, setRotationKey] = useState<SSHKeyRecord | null>(null);
  const [batchImportOpen, setBatchImportOpenState] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);

  const [previousAuth, setPreviousAuth] = useState({ role: effectiveRole, token });
  if (previousAuth.role !== effectiveRole || previousAuth.token !== token) {
    setPreviousAuth({ role: effectiveRole, token });
    if (effectiveRole !== "admin" || previousAuth.token !== token) {
      setEditorOpen(false);
      setEditingKey(null);
      setTestConnectionKeyState(null);
      setRotationOpenState(false);
      setRotationKey(null);
      setBatchImportOpenState(false);
    }
  }

  const setTestConnectionKey = useCallback((key: SSHKeyRecord | null) => {
    if (key && !canManageSSHKeys) return;
    setTestConnectionKeyState(key);
  }, [canManageSSHKeys]);

  const setRotationOpen = useCallback((open: boolean) => {
    if (open && !canManageSSHKeys) return;
    setRotationOpenState(open);
    if (!open) setRotationKey(null);
  }, [canManageSSHKeys]);

  const setBatchImportOpen = useCallback((open: boolean) => {
    if (open && !canManageSSHKeys) return;
    setBatchImportOpenState(open);
  }, [canManageSSHKeys]);

  // ----- 编辑器开关 -----
  const handleEditorOpenChange = useCallback(
    (open: boolean) => {
      if (open && !canManageSSHKeys) return;
      setEditorOpen(open);
      if (!open) setEditingKey(null);
    },
    [canManageSSHKeys],
  );

  // ----- Handlers -----
  const openCreateDialog = useCallback((event?: { currentTarget: EventTarget | null }) => {
    if (!canManageSSHKeys) return;
    editorOpenerRef.current = dialogOpenerFromTarget(event?.currentTarget);
    setEditingKey(null);
    setEditorOpen(true);
  }, [canManageSSHKeys]);

  const openEditDialog = useCallback((key: SSHKeyRecord, opener?: EventTarget | null) => {
    if (!canManageSSHKeys) return;
    editorOpenerRef.current = dialogOpenerFromTarget(opener);
    setEditingKey(key);
    setEditorOpen(true);
  }, [canManageSSHKeys]);

  const handleSave = useCallback(
    async (draft: NewSSHKeyInput, keyId?: string) => {
      if (!canManageSSHKeys) return;
      const generation = sessionGenerationRef.current;
      try {
        if (keyId) {
          await updateSSHKey(keyId, draft);
        } else {
          await createSSHKey(draft);
        }
        if (!isCurrentSession(generation)) return;
        toast.success(t(keyId ? "sshKeys.keyUpdated" : "sshKeys.keyCreated", { name: draft.name }));
        setEditorOpen(false);
        setEditingKey(null);
      } catch (error) {
        if (!isCurrentSession(generation)) return;
        toast.error(getErrorMessage(error));
      }
    },
    [canManageSSHKeys, createSSHKey, isCurrentSession, updateSSHKey, t],
  );

  const handleDelete = useCallback(
    async (key: SSHKeyRecord) => {
      if (!canManageSSHKeys) return;
      const generation = sessionGenerationRef.current;
      const ok = await confirm({
        title: t("sshKeys.confirmDeleteTitle"),
        description: t("sshKeys.confirmDeleteDesc", { name: key.name }),
      });
      if (!ok || !isCurrentSession(generation)) return;
      try {
        await deleteSSHKey(key.id);
        if (!isCurrentSession(generation)) return;
        setSelectedIds((prev) => {
          if (!prev.has(key.id)) return prev;
          const next = new Set(prev);
          next.delete(key.id);
          return next;
        });
        toast.success(t("sshKeys.keyDeleted", { name: key.name }));
      } catch (error) {
        if (!isCurrentSession(generation)) return;
        toast.error(getErrorMessage(error));
      }
    },
    [canManageSSHKeys, confirm, deleteSSHKey, isCurrentSession, t],
  );

  const openRotationWizard = useCallback((key?: SSHKeyRecord, opener?: EventTarget | null) => {
    if (!canManageSSHKeys) return;
    rotationOpenerRef.current = dialogOpenerFromTarget(opener);
    setRotationKey(key ?? null);
    setRotationOpenState(true);
  }, [canManageSSHKeys]);

  // ----- Return -----
  return {
    // 数据
    sshKeys,
    nodes,
    loading,
    canManageSSHKeys,
    captureSessionGeneration,
    isCurrentSession,

    // 筛选
    keyword,
    setKeyword,
    keyTypeFilter,
    setKeyTypeFilter,
    usageStatusFilter,
    setUsageStatusFilter,
    sortBy,
    setSortBy,
    deferredKeyword,
    resetFilters,
    isFiltered,

    // 视图
    viewMode,
    setViewMode,

    // 选中
    selectedIds,
    toggleSelection,
    toggleSelectAllVisible,
    clearSelection,
    allVisibleSelected,

    // 计算数据
    keyUsageMap,
    stats,
    filteredKeys,

    // 分页
    pagination,

    // 对话框
    editorOpen,
    handleEditorOpenChange,
    editorOnCloseAutoFocus,
    editingKey,
    testConnectionKey,
    setTestConnectionKey,
    associatedNodesKey,
    setAssociatedNodesKey,
    rotationOpen,
    setRotationOpen,
    rotationOnCloseAutoFocus,
    rotationKey,
    setRotationKey,
    batchImportOpen,
    setBatchImportOpen,
    exportOpen,
    setExportOpen,

    // 确认对话框
    confirm,
    dialog,

    // 刷新
    refreshSSHKeys,
    refreshNodes,

    // handlers
    openCreateDialog,
    openEditDialog,
    handleSave,
    handleDelete,
    openRotationWizard,
  };
}

export type SSHKeysPageState = ReturnType<typeof useSSHKeysPageState>;
