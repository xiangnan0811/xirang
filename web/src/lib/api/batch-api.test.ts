import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BatchRetryContractError } from "./batch-command-response";
import {
  createBatchApi,
  type BatchRetrySource,
  type BatchStatus,
} from "./batch-api";

const SOURCE = "A".repeat(26);
const CHILD = "B".repeat(26);
const OTHER = "C".repeat(26);

function envelope(data: unknown, status = 200) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: () => null },
    text: async () => JSON.stringify({
      code: status === 200 ? 0 : status,
      message: status === 200 ? "ok" : "error",
      data,
    }),
  } as unknown as Response;
}

function wireTask(id: number, nodeId: number, patch: Record<string, unknown> = {}) {
  return {
    id,
    name: `task-${id}`,
    status: "failed",
    node_id: nodeId,
    node: { id: nodeId, name: `node-${nodeId}` },
    ...patch,
  };
}

function wireSource(taskId: number, nodeId: number, patch: Record<string, unknown> = {}) {
  return {
    task_id: taskId,
    node_id: nodeId,
    run_id: 0,
    eligible: false,
    reason: "not_failed",
    failure_kind: null,
    retry_batch_id: null,
    ...patch,
  };
}

function eligibleDispatch(taskId: number, nodeId: number) {
  return wireSource(taskId, nodeId, {
    eligible: true,
    reason: "eligible",
    failure_kind: "dispatch",
  });
}

describe("batch status retry metadata", () => {
  const fetchMock = vi.fn();
  const api = createBatchApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function load(data: unknown): Promise<BatchStatus> {
    fetchMock.mockResolvedValueOnce(envelope(data));
    return api.getBatchStatus("token", SOURCE);
  }

  it("preserves loaded copy fields when an old server omits retry metadata", async () => {
    const status = await load({
      batch_id: SOURCE,
      total: 3,
      tasks: [
        {
          id: 11,
          name: "loaded-copy",
          status: "failed",
          node_id: 1,
          node: { id: 9, name: "\u673a\u623f A" },
          last_error: "exit 7",
        },
        {
          id: 12,
          name: "dispatch-only",
          status: "pending",
          node_id: 4,
          last_error: "",
        },
        {
          id: 13,
          name: "accepted",
          status: "success",
          node_id: 5,
          node: { id: 5, name: "node-5" },
        },
      ],
      dispatches: [
        { task_id: 11, status: "failed", last_error: "ignored when the task already has an error" },
        { task_id: 12, status: "failed", last_error: "dial failed" },
        { task_id: 13, status: "accepted", last_error: "must-not-copy" },
      ],
    });

    expect(status.tasks).toEqual([
      {
        id: 11,
        name: "loaded-copy",
        status: "failed",
        nodeId: 9,
        nodeName: "\u673a\u623f A",
        lastError: "exit 7",
        dispatchStatus: "failed",
      },
      {
        id: 12,
        name: "dispatch-only",
        status: "pending",
        nodeId: 4,
        nodeName: "\u8282\u70b9-4",
        lastError: "dial failed",
        dispatchStatus: "failed",
      },
      {
        id: 13,
        name: "accepted",
        status: "success",
        nodeId: 5,
        nodeName: "node-5",
        lastError: undefined,
        dispatchStatus: "accepted",
      },
    ]);
    expect(status.total).toBe(3);
    expect(status.statusCounts).toEqual({});
    expect(status.retain).toBeUndefined();
    expect(status.sourceBatchId).toBeUndefined();
    expect(status.retrySources).toBeUndefined();
    expect(status.retryLinks).toBeUndefined();
    expect(status).not.toHaveProperty("retryLinks");
    expect(status).not.toHaveProperty("retrySources");
    expect(status.retryLinks).not.toEqual([]);
  });

  it("accepts eligibility only when retain, node, reason, failure shape, and consumption agree", async () => {
    const status = await load({
      batch_id: SOURCE,
      total: 4,
      status_counts: { failed: 3, running: 1 },
      retain: false,
      source_batch_id: null,
      tasks: [
        wireTask(11, 1, {
          name: "moved-node",
          last_error: "exit 7",
          node: { id: 9, name: "\u673a\u623f A" },
        }),
        wireTask(12, 4, { last_error: "history gap" }),
        wireTask(13, 5, { status: "failed", last_error: "exit 9" }),
        wireTask(14, 6, { status: "running" }),
      ],
      dispatches: [
        { task_id: 11, status: "failed", last_error: "dial" },
        { task_id: 12, status: "accepted" },
        { task_id: 13, status: "accepted" },
        { task_id: 14, status: "accepted" },
      ],
      retry_sources: [
        {
          ...eligibleDispatch(11, 9),
          command: "secret-command",
        },
        wireSource(12, 4, {
          reason: "history_unverified",
          failure_kind: "execution",
          run_id: 0,
        }),
        wireSource(13, 5, {
          reason: "already_retried",
          failure_kind: "execution",
          run_id: 8,
          retry_batch_id: CHILD,
        }),
        wireSource(14, 6, {
          reason: "active",
          failure_kind: "execution",
          run_id: 4,
        }),
      ],
      retry_links: [],
    });

    expect(status.retain).toBe(false);
    expect(status.sourceBatchId).toBeNull();
    expect(status.retryLinks).toEqual([]);
    expect(status.retrySources).toEqual([
      {
        taskId: 11,
        nodeId: 9,
        runId: 0,
        eligible: true,
        reason: "eligible",
        failureKind: "dispatch",
        retryBatchId: null,
      },
      {
        taskId: 12,
        nodeId: 4,
        runId: 0,
        eligible: false,
        reason: "history_unverified",
        failureKind: "execution",
        retryBatchId: null,
      },
      {
        taskId: 13,
        nodeId: 5,
        runId: 8,
        eligible: false,
        reason: "already_retried",
        failureKind: "execution",
        retryBatchId: CHILD,
      },
      {
        taskId: 14,
        nodeId: 6,
        runId: 4,
        eligible: false,
        reason: "active",
        failureKind: "execution",
        retryBatchId: null,
      },
    ]);
    expect(status.retrySources?.[0]).not.toHaveProperty("command");
    expect(status.tasks[0]).toMatchObject({
      name: "moved-node",
      nodeId: 9,
      nodeName: "\u673a\u623f A",
      lastError: "exit 7",
      dispatchStatus: "failed",
    });
    expect(status.tasks[1]).toMatchObject({ nodeId: 4, nodeName: "node-4", lastError: "history gap" });
  });

  it("withholds contradictory eligibility instead of fabricating an operable source", async () => {
    const viewed = wireTask(11, 9, { name: "loaded-copy", last_error: "exit 7" });
    const link = { task_id: 11, run_id: 0, batch_id: CHILD, deleted: false };
    const cases: Array<{ name: string; source: Record<string, unknown>; extra?: Record<string, unknown> }> = [
      { name: "unknown reason", source: { ...eligibleDispatch(11, 9), reason: "guessed" } },
      { name: "eligible flag disagrees with reason", source: { ...eligibleDispatch(11, 9), reason: "not_failed" } },
      { name: "eligible reason is not actually eligible", source: { ...eligibleDispatch(11, 9), eligible: false } },
      { name: "unknown failure kind", source: { ...eligibleDispatch(11, 9), failure_kind: "timeout" } },
      { name: "missing failure kind", source: { ...eligibleDispatch(11, 9), failure_kind: undefined } },
      { name: "missing consumption field", source: { ...eligibleDispatch(11, 9), retry_batch_id: undefined } },
      { name: "execution eligibility has no run", source: { ...eligibleDispatch(11, 9), failure_kind: "execution", run_id: 0 } },
      { name: "dispatch eligibility names a run", source: { ...eligibleDispatch(11, 9), run_id: 4 } },
      { name: "eligible row already has a child", source: { ...eligibleDispatch(11, 9), retry_batch_id: CHILD } },
      { name: "already retried without a child", source: { ...wireSource(11, 9), reason: "already_retried", failure_kind: "dispatch" } },
      { name: "unclaimed reason carries a child", source: { ...wireSource(11, 9), retry_batch_id: CHILD } },
      { name: "source node is not the loaded node", source: eligibleDispatch(11, 1) },
      { name: "string task id", source: { ...eligibleDispatch(11, 9), task_id: "11" } },
      { name: "dispatch shape with a positive run", source: { ...wireSource(11, 9), failure_kind: "dispatch", run_id: 4 } },
      { name: "successful run marked eligible", source: { ...wireSource(11, 9), run_id: 4, eligible: true } },
      { name: "positive run with null failure kind and another reason", source: { ...wireSource(11, 9), run_id: 4, reason: "legacy" } },
      { name: "successful run carrying a child", source: { ...wireSource(11, 9), run_id: 4, retry_batch_id: CHILD } },
    ];

    for (const entry of cases) {
      const status = await load({
        batch_id: SOURCE,
        total: 1,
        status_counts: { failed: 1 },
        retain: true,
        source_batch_id: null,
        tasks: [viewed],
        dispatches: [{ task_id: 11, status: "failed", last_error: "dial" }],
        retry_sources: [entry.source],
        retry_links: [link],
        ...entry.extra,
      });
      expect(status.retrySources, entry.name).toBeUndefined();
      expect(status.retrySources, entry.name).not.toEqual([]);
      expect(JSON.stringify(status.retrySources ?? null), entry.name).not.toContain("\"eligible\":true");
      expect(status.retryLinks, entry.name).toEqual([
        { taskId: 11, runId: 0, batchId: CHILD, deleted: false },
      ]);
      expect(status.tasks[0], entry.name).toMatchObject({
        name: "loaded-copy",
        nodeId: 9,
        nodeName: "node-9",
        lastError: "exit 7",
        dispatchStatus: "failed",
      });
    }

    const untrusted = await load({
      batch_id: SOURCE,
      total: 1,
      tasks: [viewed],
      retain: "false",
      source_batch_id: "",
      retry_sources: [eligibleDispatch(11, 9)],
      retry_links: [link],
    });
    expect(untrusted.retain).toBeUndefined();
    expect(untrusted.retain).not.toBe(false);
    expect(untrusted.sourceBatchId).toBeUndefined();
    expect(untrusted.sourceBatchId).not.toBeNull();
    expect(untrusted.retrySources).toBeUndefined();
    expect(untrusted.tasks[0].lastError).toBe("exit 7");

    const missingRetain = await load({
      batch_id: SOURCE,
      total: 1,
      tasks: [viewed],
      retry_sources: [eligibleDispatch(11, 9)],
      retry_links: null,
    });
    expect(missingRetain.retain).toBeUndefined();
    expect(missingRetain.retrySources).toBeUndefined();
    expect(missingRetain.retryLinks).toBeUndefined();
    expect(missingRetain.retryLinks).not.toEqual([]);
    expect(missingRetain).not.toHaveProperty("retryLinks");
  });

  it("keeps a successful positive run beside an eligible failure and its retry link", async () => {
    const status = await load({
      batch_id: SOURCE,
      total: 3,
      status_counts: { success: 1, failed: 2 },
      retain: false,
      source_batch_id: null,
      tasks: [
        wireTask(11, 3, { name: "ok", status: "success", node: { id: 3, name: "node-ok" } }),
        wireTask(12, 4, { name: "bad", status: "failed", last_error: "exit 7", node: { id: 4, name: "node-fail" } }),
        wireTask(13, 5, { name: "linked", status: "failed", last_error: "exit 8", node: { id: 5, name: "node-link" } }),
      ],
      dispatches: [
        { task_id: 11, status: "accepted" },
        { task_id: 12, status: "accepted" },
        { task_id: 13, status: "accepted" },
      ],
      retry_sources: [
        wireSource(11, 3, { run_id: 41 }),
        wireSource(12, 4, {
          run_id: 42,
          eligible: true,
          reason: "eligible",
          failure_kind: "execution",
        }),
        wireSource(13, 5, {
          run_id: 8,
          reason: "already_retried",
          failure_kind: "execution",
          retry_batch_id: CHILD,
        }),
      ],
      retry_links: [
        { task_id: 13, run_id: 8, batch_id: CHILD, deleted: false },
      ],
    });

    expect(status.retrySources).toEqual([
      {
        taskId: 11,
        nodeId: 3,
        runId: 41,
        eligible: false,
        reason: "not_failed",
        failureKind: null,
        retryBatchId: null,
      },
      {
        taskId: 12,
        nodeId: 4,
        runId: 42,
        eligible: true,
        reason: "eligible",
        failureKind: "execution",
        retryBatchId: null,
      },
      {
        taskId: 13,
        nodeId: 5,
        runId: 8,
        eligible: false,
        reason: "already_retried",
        failureKind: "execution",
        retryBatchId: CHILD,
      },
    ]);
    expect(status.retryLinks).toEqual([
      { taskId: 13, runId: 8, batchId: CHILD, deleted: false },
    ]);
    expect(status.tasks.map((task) => task.nodeName)).toEqual(["node-ok", "node-fail", "node-link"]);
  });

  it("does not turn duplicate sources or conflicting links into a known empty set", async () => {
    const tasks = [
      wireTask(11, 9, { name: "loaded-copy", last_error: "exit 7" }),
      wireTask(12, 4, { name: "second", last_error: "exit 8" }),
    ];
    const duplicateSources = await load({
      batch_id: SOURCE,
      total: 2,
      retain: true,
      source_batch_id: null,
      tasks,
      retry_sources: [eligibleDispatch(11, 9), eligibleDispatch(11, 9)],
      retry_links: [{ task_id: 11, run_id: 0, batch_id: CHILD, deleted: false }],
    });
    expect(duplicateSources.retrySources).toBeUndefined();
    expect(duplicateSources.retrySources).not.toEqual([
      expect.objectContaining({ taskId: 11, eligible: true }),
    ]);
    expect(duplicateSources.retryLinks).toEqual([
      { taskId: 11, runId: 0, batchId: CHILD, deleted: false },
    ]);
    expect(duplicateSources.tasks.map((task) => task.lastError)).toEqual(["exit 7", "exit 8"]);

    const deletedConflict = await load({
      batch_id: SOURCE,
      total: 2,
      retain: true,
      source_batch_id: null,
      tasks,
      retry_sources: [eligibleDispatch(11, 9), wireSource(12, 4)],
      retry_links: [
        { task_id: 11, run_id: 0, batch_id: CHILD, deleted: false },
        { task_id: 11, run_id: 0, batch_id: CHILD, deleted: true },
      ],
    });
    expect(deletedConflict.retrySources).toEqual([
      {
        taskId: 11,
        nodeId: 9,
        runId: 0,
        eligible: true,
        reason: "eligible",
        failureKind: "dispatch",
        retryBatchId: null,
      },
      {
        taskId: 12,
        nodeId: 4,
        runId: 0,
        eligible: false,
        reason: "not_failed",
        failureKind: null,
        retryBatchId: null,
      },
    ]);
    expect(deletedConflict.retryLinks).toBeUndefined();
    expect(deletedConflict.retryLinks).not.toEqual([]);
    expect(deletedConflict).not.toHaveProperty("retryLinks");

    const repeatedLink = await load({
      batch_id: SOURCE,
      total: 2,
      retain: true,
      source_batch_id: null,
      tasks,
      retry_sources: [wireSource(11, 9), wireSource(12, 4)],
      retry_links: [
        { task_id: 11, run_id: 0, batch_id: CHILD, deleted: false },
        { task_id: 11, run_id: 0, batch_id: CHILD, deleted: false },
      ],
    });
    expect(repeatedLink.retryLinks).toBeUndefined();
    expect(repeatedLink.retryLinks).not.toEqual([
      { taskId: 11, runId: 0, batchId: CHILD, deleted: false },
    ]);

    const mixedChildren = await load({
      batch_id: SOURCE,
      total: 2,
      retain: true,
      source_batch_id: null,
      tasks,
      retry_sources: [wireSource(11, 9), wireSource(12, 4)],
      retry_links: [
        { task_id: 11, run_id: 3, batch_id: CHILD, deleted: false },
        { task_id: 11, run_id: 3, batch_id: OTHER, deleted: true },
      ],
    });
    expect(mixedChildren.retryLinks).toEqual([
      { taskId: 11, runId: 3, batchId: CHILD, deleted: false },
      { taskId: 11, runId: 3, batchId: OTHER, deleted: true },
    ]);

    const absentLinks = await load({
      batch_id: SOURCE,
      total: 2,
      retain: true,
      source_batch_id: SOURCE,
      tasks,
      retry_sources: [wireSource(11, 9, { reason: "legacy" }), wireSource(12, 4, { reason: "legacy" })],
    });
    expect(absentLinks.sourceBatchId).toBe(SOURCE);
    expect(absentLinks.retryLinks).toBeUndefined();
    expect(absentLinks).not.toHaveProperty("retryLinks");
    expect(absentLinks.retrySources?.every((source) => source.eligible === false)).toBe(true);
  });
});

describe("retryBatchCommand", () => {
  const fetchMock = vi.fn();
  const api = createBatchApi();
  const sources: readonly BatchRetrySource[] = [
    { taskId: 20, runId: 1 },
    { taskId: 3, runId: 0 },
  ];

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function loadedPayload() {
    return {
      batch_id: SOURCE,
      total: 1,
      status_counts: { failed: 1 },
      tasks: [
        {
          id: 11,
          name: "loaded-copy",
          status: "failed",
          node_id: 9,
          node: { id: 9, name: "\u673a\u623f A" },
          last_error: "exit 7",
        },
      ],
      dispatches: [{ task_id: 11, status: "failed", last_error: "dial" }],
    };
  }

  function childPayload(patch: Record<string, unknown> = {}) {
    return {
      batch_id: CHILD,
      source_batch_id: SOURCE,
      task_ids: [501, 502],
      run_ids: [0, 88],
      retain: false,
      dispatches: [{
        task_id: 501,
        status: "pending",
        run_id: 0,
        command_fingerprint: "hidden-fingerprint",
      }],
      dispatch_incomplete: false,
      ...patch,
    };
  }

  it("rejects an empty key or duplicate sources before fetch and omits command and proof secrets", async () => {
    const randomUUID = vi.spyOn(crypto, "randomUUID");
    const writes: string[] = [];
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation((key, value) => {
      writes.push(`${key}=${String(value)}`);
    });

    try {
      fetchMock.mockResolvedValueOnce(envelope(childPayload()));
      const result = await api.retryBatchCommand(
        "token",
        SOURCE,
        [
          { taskId: 20, runId: 1, command: "secret-command" } as BatchRetrySource,
          { taskId: 3, runId: 0 },
        ],
        "proof-once",
        "caller-key-1",
      );

      expect(randomUUID).not.toHaveBeenCalled();
      const persisted = writes.join("\n");
      expect(persisted).not.toContain("proof-once");
      expect(persisted).not.toContain("caller-key-1");
      expect(persisted).not.toContain("secret-command");
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const sent = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined;
      expect(String(sent?.body ?? "")).not.toContain("secret-command");
      expect(String(sent?.body ?? "")).not.toContain("proof-once");
      const returned = JSON.stringify(result);
      expect(returned).not.toContain("secret-command");
      expect(returned).not.toContain("proof-once");
      expect(returned).not.toContain("hidden-fingerprint");

      fetchMock.mockReset();
      fetchMock.mockResolvedValueOnce(envelope(childPayload()));
      await api.retryBatchCommand("token", SOURCE, sources, undefined, "caller-key-2");
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const omittedProof = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined;
      const omittedText = `${JSON.stringify(omittedProof?.headers ?? {})}\n${String(omittedProof?.body ?? "")}`;
      expect(omittedText).not.toContain("proof-once");
      expect(omittedText).not.toContain("secret-command");

      fetchMock.mockReset();
      const rejectedRequests = [
        api.retryBatchCommand("token", SOURCE, sources, undefined, ""),
        api.retryBatchCommand(
          "token",
          SOURCE,
          [{ taskId: 3, runId: 0 }, { taskId: 3, runId: 1 }],
          undefined,
          "caller-key-1",
        ),
      ];
      for (const rejectedRequest of rejectedRequests) {
        const error = await rejectedRequest.then(() => null, (caught: unknown) => caught);
        expect(error).toBeInstanceOf(Error);
        expect(error).not.toBeInstanceOf(BatchRetryContractError);
      }
      expect(fetchMock).not.toHaveBeenCalled();
    } finally {
      randomUUID.mockRestore();
      setItem.mockRestore();
    }
  });

  it("classifies an unusable success as a contract error without replacing the loaded result", async () => {
    fetchMock.mockImplementation(async (url: string) => {
      if (String(url).endsWith("/retry")) return envelope(childPayload());
      return envelope(loadedPayload());
    });
    const loaded = await api.getBatchStatus("token", SOURCE);
    const snapshot = structuredClone(loaded);

    const badPayloads = [
      childPayload({ batch_id: SOURCE }),
      childPayload({ batch_id: "A".repeat(25) }),
      childPayload({ batch_id: "D".repeat(65) }),
      childPayload({ batch_id: "b".repeat(26) }),
      childPayload({ source_batch_id: CHILD }),
      childPayload({ source_batch_id: null }),
      childPayload({ retain: "false" }),
      childPayload({ retain: 0 }),
      childPayload({ retain: null }),
      childPayload({ task_ids: [] }),
      childPayload({ task_ids: [0], run_ids: [0] }),
      childPayload({ task_ids: [501.5, 502], run_ids: [0, 1] }),
      childPayload({ task_ids: ["501", "502"] }),
      childPayload({ task_ids: [501, 501], run_ids: [0, 1] }),
      childPayload({ task_ids: [20, 502], run_ids: [0, 1] }),
      childPayload({ run_ids: [0] }),
      childPayload({ run_ids: [-1, 1] }),
      childPayload({ task_ids: undefined, run_ids: undefined }),
    ];

    for (const payload of badPayloads) {
      fetchMock.mockImplementation(async (url: string) => {
        if (String(url).endsWith("/retry")) return envelope(payload);
        return envelope(loadedPayload());
      });
      const error = await api.retryBatchCommand("token", SOURCE, sources, undefined, "caller-key-1")
        .then(() => null, (caught: unknown) => caught);
      expect(error).toBeInstanceOf(BatchRetryContractError);
      expect(String(error)).not.toContain("hidden-fingerprint");
      expect(loaded).toEqual(snapshot);
      expect(loaded.tasks).toEqual([
        {
          id: 11,
          name: "loaded-copy",
          status: "failed",
          nodeId: 9,
          nodeName: "\u673a\u623f A",
          lastError: "exit 7",
          dispatchStatus: "failed",
        },
      ]);
      expect(loaded).not.toHaveProperty("retryLinks");
    }

    fetchMock.mockImplementation(async (url: string) => {
      if (String(url).endsWith("/retry")) {
        return envelope(childPayload({ batch_id: "D".repeat(64), retain: true }));
      }
      return envelope(loadedPayload());
    });
    await api.retryBatchCommand("token", SOURCE, sources, undefined, "caller-key-1");
    const again = await api.getBatchStatus("token", SOURCE);
    expect(again.tasks).toEqual(loaded.tasks);
    expect(again.retryLinks).toBeUndefined();
    expect(loaded).toEqual(snapshot);
  });
});
