import { useCallback, useRef, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { ApiError, apiClient } from "@/lib/api/client";
import { isNodeRevisionConflictError } from "@/lib/api/nodes-api";
import { toast } from "@/components/ui/toast-sonner";
import { getAuthSessionGeneration } from "@/lib/api/core";
import { preserveSSHPurposeScope, sshKeyIsBroadScope } from "@/lib/ssh-purpose-scope";
import { runSSHKeyReplacement, type SSHKeyReplacementOptions } from "@/lib/ssh-key-replacement";
import { parseTags } from "@/hooks/use-console-data.utils";
import { useApiAction } from "@/hooks/use-api-action";
import type {
  AlertRecord,
  NewNodeInput,
  NewSSHKeyInput,
  NodeConnectionProbeOutcome,
  NodeRecord,
  NodeSaveResult,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord
} from "@/types/domain";

// Demo builders must not enter the production startup chunk (same as mock.ts).
function loadDemoBuilders() {
  return import("@/hooks/use-console-data.demo");
}

/**
 * Per-node ownership of connection-test results. Every test start and node mutation advances the
 * node's fence, so a late result for a re-tested, edited or deleted node cannot publish.
 */
function advanceNodeFences(fences: Map<number, number>, nodeIDs: readonly number[]) {
  for (const nodeID of nodeIDs) {
    fences.set(nodeID, (fences.get(nodeID) ?? 0) + 1);
  }
}

type UseNodeOperationsParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  setNodes: Dispatch<SetStateAction<NodeRecord[]>>;
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setSSHKeys: Dispatch<SetStateAction<SSHKeyRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  markInventoryMutated: () => void;
  /** Shared inventory publisher; probe results reach the node list only through it. */
  refreshNodes: () => Promise<void>;
  /**
   * Generation of the newest settled node-list read, or null while one is in flight. Compared across
   * a probe's authoritative detail read to detect a list that started or published meanwhile.
   */
  settledNodesListGeneration: () => number | null;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function useNodeOperations({
  token,
  demoModeEnabled,
  nodes,
  policies,
  tasks,
  setNodes,
  setTasks,
  setAlerts,
  setSSHKeys,
  setWarning,
  markInventoryMutated,
  refreshNodes,
  settledNodesListGeneration,
  markTasksMutated,
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UseNodeOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });
  const nodeFencesRef = useRef(new Map<number, number>());

  const createSSHKey = useCallback(async (input: NewSSHKeyInput, isCurrent?: () => boolean): Promise<string> => {
    const result = await exec<SSHKeyRecord | null>(i18n.t("nodes.actions.createSSHKey"), async (t) => {
      try {
        return await apiClient.createSSHKey(t, input);
      } catch (error) {
        // 身份已失效时在共享错误处理前结束；已提交的创建不回滚。
        if (isCurrent && !isCurrent()) {
          return null;
        }
        throw error;
      }
    });
    if (isCurrent && !isCurrent()) {
      return "";
    }
    if (result) {
      if (result.ok && result.data) {
        const created = result.data;
        markInventoryMutated();
        setSSHKeys((prev) => [created, ...prev]);
        return created.id;
      }
      return "";
    }
    const { buildDemoSSHKey } = await loadDemoBuilders();
    if (isCurrent && !isCurrent()) {
      return "";
    }
    const item = buildDemoSSHKey(input);
    markInventoryMutated();
    setSSHKeys((prev) => [item, ...prev]);
    return item.id;
  }, [exec, markInventoryMutated, setSSHKeys]);

  // The editor owns challenge and failure feedback for this write, so errors bypass the shared warning.
  const updateSSHKey = useCallback(async (keyId: string, input: NewSSHKeyInput, options: SSHKeyReplacementOptions) => {
    const assertCurrent = () => {
      if (options.signal.aborted || !options.isCurrent()) {
        throw new DOMException("SSH key update no longer belongs to its owner", "AbortError");
      }
    };
    assertCurrent();
    const failure: { error?: unknown; failed: boolean } = { failed: false };
    const result = await exec<SSHKeyRecord | null>(i18n.t("nodes.actions.updateSSHKey"), async (t) => {
      try {
        return await runSSHKeyReplacement(
          (proof) => apiClient.updateSSHKey(t, keyId, input, { stepUpProof: proof, signal: options.signal }),
          options,
        );
      } catch (error) {
        failure.failed = true;
        failure.error = error;
        return null;
      }
    });
    if (failure.failed) {
      throw failure.error;
    }
    assertCurrent();
    if (result) {
      if (result.ok && result.data) {
        const updated = result.data;
        markInventoryMutated();
        setSSHKeys((prev) => prev.map((item) => (item.id === keyId ? updated : item)));
      }
      return;
    }
    const { buildDemoSSHKey } = await loadDemoBuilders();
    assertCurrent();
    markInventoryMutated();
    setSSHKeys((prev) =>
      prev.map((item) =>
        item.id === keyId
          ? {
              ...item,
              name: input.name,
              username: input.username,
              keyType: input.keyType,
              privateKey: input.privateKey,
              fingerprint: buildDemoSSHKey(input).fingerprint,
              disabled: input.disabled,
              expiresAt: input.expiresAt || undefined,
              allowedPurposes: preserveSSHPurposeScope(input.allowedPurposes),
              allowedNodeIds: input.allowedNodeIds,
              allowedNodeTags: input.allowedNodeTags,
              broadScope: sshKeyIsBroadScope(input)
            }
          : item
      )
    );
  }, [exec, markInventoryMutated, setSSHKeys]);

  // deleteSSHKey 有自定义 ApiError 处理，不使用 exec
  const deleteSSHKey = useCallback(async (keyId: string): Promise<boolean> => {
    const usedByNodes = nodes.some((node) => node.keyId === keyId);
    if (usedByNodes) {
      return false;
    }

    if (token) {
      try {
        await apiClient.deleteSSHKey(token, keyId);
      } catch (error) {
        if (error instanceof ApiError) {
          if (demoModeEnabled) {
            setWarning(typeof error.detail === "string" ? error.detail : i18n.t("nodes.actions.deleteSSHKeyFailed"));
          } else {
            const detail = typeof error.detail === "string" ? error.detail : error.message;
            const message = i18n.t("nodes.actions.deleteSSHKeyFailedDetail", { detail });
            setWarning(message);
            throw new Error(message);
          }
        } else if (!demoModeEnabled) {
          handleWriteApiError(i18n.t("nodes.actions.deleteSSHKey"), error);
        }
        if (!demoModeEnabled) {
          return false;
        }
      }
    } else {
      ensureDemoWriteAllowed(i18n.t("nodes.actions.deleteSSHKey"));
    }

    markInventoryMutated();
    setSSHKeys((prev) => prev.filter((item) => item.id !== keyId));
    return true;
  }, [demoModeEnabled, ensureDemoWriteAllowed, handleWriteApiError, markInventoryMutated, nodes, setSSHKeys, setWarning, token]);

  // Resolves to the committed node and its revision; null when the write did not commit for this caller
  // (ownership ended, or a demo-mode failure already reported through the shared warning).
  const createNode = useCallback(async (
    input: NewNodeInput,
    isCurrent?: () => boolean,
    onInlineKeyCreated?: (keyId: string) => void,
  ): Promise<NodeSaveResult | null> => {
    let keyId = input.keyId ?? null;

    if (input.authType === "key" && input.inlinePrivateKey?.trim()) {
      try {
        keyId = await createSSHKey({
          name: input.inlineKeyName?.trim() || `${input.name}-key`,
          username: input.username,
          keyType: input.inlineKeyType ?? "auto",
          privateKey: input.inlinePrivateKey.trim(),
          disabled: false,
          expiresAt: "",
          allowedPurposes: "",
          allowedNodeIds: "",
          allowedNodeTags: ""
        }, isCurrent);
      } catch (error) {
        if (isCurrent && !isCurrent()) {
          return null;
        }
        throw error;
      }
      if (isCurrent && !isCurrent()) {
        return null;
      }
      onInlineKeyCreated?.(keyId);
    }

    const finalInput: NewNodeInput = { ...input, keyId };
    const label = i18n.t("nodes.actions.createNode");

    if (!token) {
      ensureDemoWriteAllowed(label);
      const { buildDemoNode } = await loadDemoBuilders();
      if (isCurrent && !isCurrent()) {
        return null;
      }
      const nextNode = buildDemoNode(input, nodes, keyId);
      markInventoryMutated();
      setNodes((prev) => [nextNode, ...prev]);
      return { id: nextNode.id };
    }

    try {
      const created = await apiClient.createNode(token, finalInput);
      if (isCurrent && !isCurrent()) {
        return null;
      }
      markInventoryMutated();
      setNodes((prev) => [created, ...prev]);
      return created.revision ? { id: created.id, revision: created.revision } : { id: created.id };
    } catch (error) {
      if (isCurrent && !isCurrent()) {
        return null;
      }
      handleWriteApiError(label, error);
      return null;
    }
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, markInventoryMutated, nodes, setNodes, token]);

  // Resolves to the committed revision; null when ownership ended. A revision conflict rejects without the
  // shared warning: the editor that sent the opened revision owns that feedback.
  const updateNode = useCallback(async (
    nodeID: number,
    input: NewNodeInput,
    isCurrent?: () => boolean,
    onInlineKeyCreated?: (keyId: string) => void,
  ): Promise<NodeSaveResult | null> => {
    advanceNodeFences(nodeFencesRef.current, [nodeID]);
    let keyId = input.keyId ?? null;
    if (input.authType === "key" && input.inlinePrivateKey?.trim()) {
      try {
        keyId = await createSSHKey({
          name: input.inlineKeyName?.trim() || `${input.name}-key`,
          username: input.username,
          keyType: input.inlineKeyType ?? "auto",
          privateKey: input.inlinePrivateKey.trim(),
          disabled: false,
          expiresAt: "",
          allowedPurposes: "",
          allowedNodeIds: "",
          allowedNodeTags: ""
        }, isCurrent);
      } catch (error) {
        if (isCurrent && !isCurrent()) {
          return null;
        }
        throw error;
      }
      if (isCurrent && !isCurrent()) {
        return null;
      }
      onInlineKeyCreated?.(keyId);
    }

    const finalInput: NewNodeInput = { ...input, keyId };
    const label = i18n.t("nodes.actions.updateNode");

    if (!token) {
      ensureDemoWriteAllowed(label);
      if (isCurrent && !isCurrent()) {
        return null;
      }
      advanceNodeFences(nodeFencesRef.current, [nodeID]);
      markInventoryMutated();
      setNodes((prev) =>
        prev.map((node) =>
          node.id === nodeID
            ? {
                ...node,
                name: input.name,
                host: input.host,
                address: input.host,
                ip: input.host,
                port: input.port || 22,
                username: input.username,
                authType: input.authType,
                keyId,
                basePath: input.basePath || node.basePath,
                tags: parseTags(input.tags),
                rawTags: input.tags,
              }
            : node
        )
      );
      return { id: nodeID };
    }

    try {
      const updated = await apiClient.updateNode(token, nodeID, finalInput);
      if (isCurrent && !isCurrent()) {
        return null;
      }
      advanceNodeFences(nodeFencesRef.current, [nodeID]);
      markInventoryMutated();
      setNodes((prev) => prev.map((node) => (node.id === nodeID ? updated.node : node)));
      if (updated.warning) {
        setWarning(updated.warning);
        toast.warning(updated.warning);
      }
      return updated.node.revision ? { id: nodeID, revision: updated.node.revision } : { id: nodeID };
    } catch (error) {
      if (isCurrent && !isCurrent()) {
        return null;
      }
      if (!isNodeRevisionConflictError(error)) {
        handleWriteApiError(label, error);
      }
      throw error;
    }
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, markInventoryMutated, setNodes, setWarning, token]);


  const deleteNode = useCallback(async (nodeID: number, isCurrent?: () => boolean) => {
    advanceNodeFences(nodeFencesRef.current, [nodeID]);
    await exec(i18n.t("nodes.actions.deleteNode"), async (t) => {
      try {
        await apiClient.deleteNode(t, nodeID);
      } catch (error) {
        // 身份已失效时在共享错误处理前结束；已提交的删除不回滚。
        if (isCurrent && !isCurrent()) {
          return;
        }
        throw error;
      }
    });
    if (isCurrent && !isCurrent()) {
      return;
    }
    advanceNodeFences(nodeFencesRef.current, [nodeID]);
    markInventoryMutated();
    markTasksMutated();
    setNodes((prev) => prev.filter((node) => node.id !== nodeID));
    setTasks((prev) => prev.filter((task) => task.nodeId !== nodeID));
    setAlerts((prev) => prev.filter((alert) => alert.nodeId !== nodeID));
  }, [exec, markInventoryMutated, markTasksMutated, setAlerts, setNodes, setTasks]);

  const deleteNodes = useCallback(async (nodeIDs: number[], isCurrent?: () => boolean): Promise<{ deleted: number; notFoundIds: number[] }> => {
    const normalized = Array.from(new Set(nodeIDs.filter((item) => Number.isFinite(item) && item > 0)));
    if (!normalized.length) {
      return { deleted: 0, notFoundIds: [] };
    }
    advanceNodeFences(nodeFencesRef.current, normalized);

    const result = await exec<{ deleted: number; notFoundIds: number[] } | null>(
      i18n.t("nodes.actions.deleteNodes"),
      async (t) => {
        try {
          return await apiClient.deleteNodes(t, normalized);
        } catch (error) {
          // 身份已失效时在共享错误处理前结束；已提交的删除不回滚。
          if (isCurrent && !isCurrent()) {
            return null;
          }
          throw error;
        }
      }
    );
    if (isCurrent && !isCurrent()) {
      return { deleted: 0, notFoundIds: [] };
    }
    if (result) {
      if (result.ok && result.data) {
        const data = result.data;
        const deletedSet = new Set(normalized.filter((id) => !data.notFoundIds.includes(id)));
        advanceNodeFences(nodeFencesRef.current, normalized);
        markInventoryMutated();
        markTasksMutated();
        setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
        setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
        setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));
        return data;
      }
      return { deleted: 0, notFoundIds: normalized };
    }

    const deletedSet = new Set(normalized);
    advanceNodeFences(nodeFencesRef.current, normalized);
    markInventoryMutated();
    markTasksMutated();
    setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
    setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
    setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));
    return { deleted: normalized.length, notFoundIds: [] };
  }, [exec, markInventoryMutated, markTasksMutated, setAlerts, setNodes, setTasks]);

  // One reconciliation path for every connection-test trigger (row/card, editor, post-save, post-trust).
  // Results never patch health locally: the server persists the applied result and the shared
  // inventory refresh publishes it under its own abort and mutation fences, so a probe can neither
  // overwrite a newer edit nor resurrect a deleted node. Ownership ends when this node is re-tested
  // or mutated here, or when the auth session changes; such results are silently superseded.
  // `expectedRevision` (post-save only) admits the test only while the node keeps the committed revision;
  // a later change makes the server answer stale before dialing, which surfaces as a retest.
  const testNodeConnection = useCallback(async (
    nodeID: number,
    options?: { expectedRevision?: string },
  ): Promise<NodeConnectionProbeOutcome> => {
    const expectedRevision = options?.expectedRevision;
    const fences = nodeFencesRef.current;
    advanceNodeFences(fences, [nodeID]);
    const fence = fences.get(nodeID);
    const sessionGeneration = getAuthSessionGeneration();
    const requestToken = token;
    const owns = () => fences.get(nodeID) === fence && getAuthSessionGeneration() === sessionGeneration;
    const superseded: NodeConnectionProbeOutcome = { ok: false, message: "", stale: "superseded" };

    // A failure after ownership ended belongs to nobody: exec skips the shared warning and the
    // caller sees a silent supersession instead of the error.
    const result = await exec(
      i18n.t("nodes.actions.testConnection"),
      (t) => expectedRevision
        ? apiClient.testNodeConnection(t, nodeID, { expectedRevision })
        : apiClient.testNodeConnection(t, nodeID),
      owns,
    ).catch((error: unknown) => {
      if (owns()) {
        throw error;
      }
      return "superseded" as const;
    });
    if (result === "superseded" || !owns()) {
      return superseded;
    }
    if (result && requestToken) {
      if (!result.ok) {
        return { ok: false, message: i18n.t("nodes.connectionTestFailed") };
      }
      const r = result.data;
      const retest: NodeConnectionProbeOutcome = { ok: false, message: r.message, stale: "retest" };
      if (r.stale) {
        await refreshNodes();
        return owns() ? retest : superseded;
      }
      if (r.probeToken) {
        // Publish first, then validate: refreshNodes resolves only after the newest list read
        // settled (a superseded refresh waits for its successor), so the authoritative read is never
        // older than the published list. A list that starts or settles while the detail read is in
        // flight may publish a remote change the detail predates, so that also demands a retest
        // instead of trusting the earlier token. Any later drift is still refused server-side,
        // because trust is bound to this token.
        await refreshNodes();
        if (!owns()) {
          return superseded;
        }
        const listGeneration = settledNodesListGeneration();
        if (listGeneration === null) {
          return retest;
        }
        const current = await apiClient.getNode(requestToken, nodeID).catch(() => null);
        if (!owns()) {
          return superseded;
        }
        if (settledNodesListGeneration() !== listGeneration || current?.probeToken !== r.probeToken) {
          return retest;
        }
        return { ok: r.ok, message: r.message, errorCode: r.errorCode, hostKey: r.hostKey, probeToken: r.probeToken };
      }
      // Legacy server without probe tokens: it persisted the result, so publish through the shared
      // refresh and keep its legacy feedback only while this test still owns the node.
      await refreshNodes();
      if (!owns()) {
        return superseded;
      }
      return { ok: r.ok, message: r.message, errorCode: r.errorCode, hostKey: r.hostKey };
    }

    // Demo 模式模拟
    await new Promise((resolve) => setTimeout(resolve, 650));
    const { simulateDemoConnection } = await loadDemoBuilders();
    if (!owns()) {
      return superseded;
    }
    const sim = simulateDemoConnection(nodeID);
    markInventoryMutated();
    setNodes((prev) => prev.map((node) => (node.id === nodeID ? sim.nodeUpdate(node) : node)));
    return sim.result;
  }, [exec, markInventoryMutated, refreshNodes, setNodes, settledNodesListGeneration, token]);

  const triggerNodeBackup = useCallback(async (nodeID: number) => {
    const node = nodes.find((item) => item.id === nodeID);
    if (!node) {
      return;
    }

    const targetPolicy = policies.find((policy) => policy.enabled) ?? policies[0];

    const result = await exec(i18n.t("nodes.actions.triggerBackup"), (t) =>
      apiClient.createTask(t, {
        name: i18n.t("nodes.manualBackupName", { name: node.name }),
        nodeId: nodeID,
        policyId: targetPolicy?.id ?? null,
        executorType: "rsync",
        rsyncSource: targetPolicy?.sourcePath,
        rsyncTarget: targetPolicy?.targetPath,
        cronSpec: targetPolicy?.cron
      })
    );
    if (result) {
      if (result.ok) {
        markTasksMutated();
        setTasks((prev) => [result.data, ...prev]);
      }
      return;
    }

    markTasksMutated();
    const { buildDemoBackupTask } = await loadDemoBuilders();
    setTasks((prev) => [buildDemoBackupTask(node, tasks, policies), ...prev]);
    setNodes((prev) =>
      prev.map((item) =>
        item.id === nodeID
          ? { ...item, status: "online", lastSeenAt: i18n.t("common.justNow") }
          : item
      )
    );
  }, [exec, markTasksMutated, nodes, policies, setNodes, setTasks, tasks]);

  return {
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup
  };
}
