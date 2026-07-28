import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/core";
import type {
  AssetRef,
  BackupArchiveIndex,
  BackupArchiveMemberStatus,
  BackupExportDownloadTicket,
} from "@/types/domain";

import { useBackupArchive, type BackupArchiveApi } from "./use-backup-archive";

const ref: AssetRef = { recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) };
const replacementRef: AssetRef = { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) };
const memberId = "c".repeat(32);
const parentMemberId = "f".repeat(32);
const requestId = "d".repeat(32);
const revision = "e".repeat(64);

const index: BackupArchiveIndex = {
  schemaVersion: 1,
  indexRevision: revision,
  expiresAt: new Date(Date.now() + 60_000).toISOString(),
  entries: [{ id: memberId, parentId: null, displayName: "member.txt", type: "file", size: 12, mediaType: "text/plain", warning: "none" }],
};

function status(overrides: Partial<BackupArchiveMemberStatus> = {}): BackupArchiveMemberStatus {
  return {
    schemaVersion: 1,
    requestId,
    state: "failed",
    failureProduct: "limit",
    fallback: { action: "download_original", reason: null },
    retryable: false,
    terminal: true,
    ...overrides,
  };
}

function api(): BackupArchiveApi {
  return {
    listIndex: vi.fn().mockResolvedValue(index),
    create: vi.fn().mockResolvedValue({ schemaVersion: 1, requestId, state: "queued" }),
    status: vi.fn().mockResolvedValue(status()),
    cancel: vi.fn(),
    issueTicket: vi.fn(),
  };
}

function downloadTicket(): BackupExportDownloadTicket {
  return {
    schemaVersion: 1,
    contentUrl: `/api/v1/asset-content/${"f".repeat(32)}`,
    contentType: "text/plain",
    contentLength: 12,
    etag: '"member-etag"',
    range: "none",
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    idleExpiresAt: new Date(Date.now() + 30_000).toISOString(),
  };
}

describe("useBackupArchive", () => {
  afterEach(() => vi.useRealTimers());

  it("binds a member job to the returned index revision and one-hop member id", async () => {
    const archiveApi = api();
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(archiveApi.create).toHaveBeenCalledWith(
      "token",
      ref,
      revision,
      memberId,
      expect.any(String),
      expect.any(AbortSignal),
    );
    expect(archiveApi.status).toHaveBeenCalledWith(
      "token",
      ref,
      revision,
      requestId,
      expect.any(AbortSignal),
    );
    expect(result.current.state.status?.failureProduct).toBe("limit");
  });

  it("replaces an active poll timer on reload and stops after a terminal status", async () => {
    vi.useFakeTimers();
    const archiveApi = api();
    const active = status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    });
    const terminal = status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    });
    vi.mocked(archiveApi.status)
      .mockResolvedValueOnce(active)
      .mockResolvedValueOnce(active)
      .mockResolvedValue(terminal);
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    expect(archiveApi.status).toHaveBeenCalledTimes(1);

    await act(async () => result.current.reload());
    expect(archiveApi.status).toHaveBeenCalledTimes(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(archiveApi.status).toHaveBeenCalledTimes(3);
    expect(result.current.state.phase).toBe("terminal");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(archiveApi.status).toHaveBeenCalledTimes(3);
  });

  it("keeps archive API calls bound to the selected identity after an equivalent caller ref is replaced", async () => {
    const archiveApi = api();
    const initialRef = { ...ref };
    const replacementRef = { ...ref };
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: initialRef } },
    );

    rerender({ refValue: replacementRef });
    initialRef.entryId = "f".repeat(64);

    await act(async () => result.current.open());

    expect(archiveApi.listIndex).toHaveBeenCalledWith("token", ref, expect.any(AbortSignal));
  });

  it("keeps the member download action stable when an equivalent selection ref is replaced", () => {
    const archiveApi = api();
    const ensureStepUpProof = vi.fn();
    const onPrepareDownload = vi.fn();
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "operator",
        ref: refValue,
        ensureStepUpProof,
        api: archiveApi,
        onPrepareDownload,
      }),
      { initialProps: { refValue: { ...ref } } },
    );
    const initialDownload = result.current.download;

    rerender({ refValue: { ...ref } });

    expect(result.current.download).toBe(initialDownload);
  });

  it("drops a pending archive index response when the semantic asset changes", async () => {
    const archiveApi = api();
    let resolveIndex: (value: BackupArchiveIndex) => void = () => {
      throw new Error("archive index request did not begin");
    };
    const pendingIndex = new Promise<BackupArchiveIndex>((resolve) => {
      resolveIndex = resolve;
    });
    let indexSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.listIndex).mockImplementation((_token, _ref, signal) => {
      indexSignal = signal;
      return pendingIndex;
    });
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: { ...ref } } },
    );

    let opening: Promise<void> | undefined;
    act(() => {
      opening = result.current.open();
    });
    await waitFor(() => expect(archiveApi.listIndex).toHaveBeenCalledTimes(1));

    rerender({ refValue: replacementRef });
    resolveIndex(index);
    await act(async () => {
      await opening;
    });

    expect(result.current.state).toMatchObject({ phase: "closed", index: null, requestId: null, status: null });
    expect(indexSignal?.aborted).toBe(true);
  });

  it("reconciles a late accepted create against the old asset after a semantic switch", async () => {
    const archiveApi = api();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const oldRef = { ...ref };
    const { result, rerender } = renderHook(
      ({ refValue, tokenValue }) => useBackupArchive({
        token: tokenValue,
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: oldRef, tokenValue: "old-token" } },
    );

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    rerender({ refValue: replacementRef, tokenValue: "new-token" });
    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await act(async () => {
      await creating;
    });

    expect(createSignal?.aborted).toBe(true);
    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("old-token", oldRef, revision, requestId, expect.any(AbortSignal));
    expect(archiveApi.status).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ phase: "closed", index: null, requestId: null, status: null });
  });

  it("reconciles successive abandoned requests while an older cancellation is still pending", async () => {
    const archiveApi = api();
    const secondRequestId = "3".repeat(32);
    const thirdRef: AssetRef = { recoveryPointId: "4".repeat(32), entryId: "5".repeat(64) };
    vi.mocked(archiveApi.create)
      .mockResolvedValueOnce({ schemaVersion: 1, requestId, state: "queued" })
      .mockResolvedValueOnce({ schemaVersion: 1, requestId: secondRequestId, state: "queued" });
    vi.mocked(archiveApi.status).mockImplementation(async (_token, _ref, _revision, currentRequestId) => status({
      requestId: currentRequestId,
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      retryable: true,
      terminal: false,
    }));
    let resolveFirstCancel: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("first abandoned cancellation did not begin");
    };
    const firstCancel = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveFirstCancel = resolve;
    });
    vi.mocked(archiveApi.cancel)
      .mockImplementationOnce(() => firstCancel)
      .mockResolvedValueOnce(status({
        requestId: secondRequestId,
        state: "canceled",
        failureProduct: null,
        fallback: { action: null, reason: null },
        terminal: true,
      }));
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: { ...ref } } },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    rerender({ refValue: replacementRef });
    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    rerender({ refValue: thirdRef });

    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(2));
    expect(archiveApi.cancel).toHaveBeenNthCalledWith(
      2,
      "token",
      replacementRef,
      revision,
      secondRequestId,
      expect.any(AbortSignal),
    );
    resolveFirstCancel(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
  });

  it("drops a pending member status response when the semantic asset changes", async () => {
    const archiveApi = api();
    let resolveStatus: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("archive status request did not begin");
    };
    const pendingStatus = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveStatus = resolve;
    });
    let statusSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.status).mockImplementation((_token, _ref, _revision, _requestId, signal) => {
      statusSignal = signal;
      return pendingStatus;
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const oldRef = { ...ref };
    const { result, rerender } = renderHook(
      ({ refValue, tokenValue }) => useBackupArchive({
        token: tokenValue,
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: oldRef, tokenValue: "old-token" } },
    );

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.status).toHaveBeenCalledTimes(1));

    rerender({ refValue: replacementRef, tokenValue: "new-token" });
    resolveStatus(status());
    await act(async () => {
      await creating;
    });

    expect(statusSignal?.aborted).toBe(true);
    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
    expect(archiveApi.cancel).toHaveBeenCalledWith("old-token", oldRef, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "closed", index: null, requestId: null, status: null });
  });

  it("preserves an in-flight index request when a new ref object has the same semantic ids", async () => {
    const archiveApi = api();
    let resolveIndex: (value: BackupArchiveIndex) => void = () => {
      throw new Error("archive index request did not begin");
    };
    const pendingIndex = new Promise<BackupArchiveIndex>((resolve) => {
      resolveIndex = resolve;
    });
    let indexSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.listIndex).mockImplementation((_token, _ref, signal) => {
      indexSignal = signal;
      return pendingIndex;
    });
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: { ...ref } } },
    );

    let opening: Promise<void> | undefined;
    act(() => {
      opening = result.current.open();
    });
    await waitFor(() => expect(archiveApi.listIndex).toHaveBeenCalledTimes(1));

    rerender({ refValue: { ...ref } });
    expect(indexSignal?.aborted).toBe(false);
    expect(result.current.state.phase).toBe("indexing");

    resolveIndex(index);
    await act(async () => {
      await opening;
    });

    expect(result.current.state).toMatchObject({ phase: "review", index });
  });

  it("coalesces concurrent member creates into one durable request and idempotency key", async () => {
    const archiveApi = api();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    let resolveCreateStarted: () => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    const createStarted = new Promise<void>((resolve) => {
      resolveCreateStarted = resolve;
    });
    const idempotencyKeys: string[] = [];
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, idempotencyKey, signal) => {
      idempotencyKeys.push(idempotencyKey);
      createSignal = signal;
      resolveCreateStarted();
      return pendingCreate;
    });
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());

    let firstCreate: Promise<void> | undefined;
    let secondCreate: Promise<void> | undefined;
    act(() => {
      firstCreate = result.current.create(memberId);
    });
    await act(async () => {
      await createStarted;
    });
    act(() => {
      secondCreate = result.current.create(memberId);
    });

    try {
      expect(archiveApi.create).toHaveBeenCalledTimes(1);
      expect(idempotencyKeys).toHaveLength(1);
      expect(createSignal?.aborted).toBe(false);
      expect(result.current.state.phase).toBe("creating");
      expect(result.current.state.error).toBeNull();
    } finally {
      resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
      await act(async () => {
        await Promise.all([firstCreate, secondCreate]);
      });
    }
    expect(result.current.state.status?.requestId).toBe(requestId);
  });

  it.each(["callback", "browser anchor"] as const)("clears the in-memory ticket after %s handoff", async (handoff) => {
    const archiveApi = api();
    const ticket = downloadTicket();
    const onDownloadTicket = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "ready",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    vi.mocked(archiveApi.issueTicket).mockResolvedValue(ticket);
    try {
      const { result } = renderHook(() => useBackupArchive({
        token: "token",
        role: "operator",
        ref,
        ensureStepUpProof: vi.fn().mockResolvedValue("fresh-member-proof"),
        api: archiveApi,
        onPrepareDownload: vi.fn(),
        onDownloadTicket: handoff === "callback" ? onDownloadTicket : undefined,
      }));

      await act(async () => result.current.open());
      await act(async () => result.current.create(memberId));
      await act(async () => result.current.download());

      expect(result.current.state.ticket).toBeNull();
      if (handoff === "callback") {
        expect(onDownloadTicket).toHaveBeenCalledWith(ticket);
        expect(click).not.toHaveBeenCalled();
        return;
      }

      const clickedAnchor = click.mock.instances[click.mock.instances.length - 1];
      expect(clickedAnchor).toBeInstanceOf(HTMLAnchorElement);
      if (!(clickedAnchor instanceof HTMLAnchorElement)) throw new Error("member download anchor was not clicked");
      expect(clickedAnchor.href).toBe(new URL(ticket.contentUrl, window.location.href).href);
    } finally {
      click.mockRestore();
    }
  });

  it.each([
    ["a network failure", () => new TypeError("network unavailable")],
    ["an ambiguous 500 response", () => new ApiError(500, "archive member create outcome unavailable")],
  ])("automatically replays a live member create with the same idempotency key after %s", async (_description, failure) => {
    const archiveApi = api();
    vi.mocked(archiveApi.create)
      .mockRejectedValueOnce(failure())
      .mockResolvedValueOnce({ schemaVersion: 1, requestId, state: "queued" });
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));
    await act(async () => result.current.open());

    await act(async () => result.current.create(memberId));

    expect(archiveApi.create).toHaveBeenCalledTimes(2);
    const firstCall = vi.mocked(archiveApi.create).mock.calls[0];
    const replayCall = vi.mocked(archiveApi.create).mock.calls[1];
    expect(replayCall?.[1]).toBe(firstCall?.[1]);
    expect(replayCall?.[2]).toBe(firstCall?.[2]);
    expect(replayCall?.[3]).toBe(firstCall?.[3]);
    expect(replayCall?.[4]).toBe(firstCall?.[4]);
    expect(result.current.state).toMatchObject({
      phase: "terminal",
      requestId,
      error: null,
    });
  });

  it("reconciles the original member binding after ambiguous retries are exhausted across an asset switch", async () => {
    const archiveApi = api();
    let attempts = 0;
    vi.mocked(archiveApi.create).mockImplementation(() => {
      attempts += 1;
      return attempts <= 4
        ? Promise.reject(new ApiError(500, "archive member create outcome unavailable"))
        : Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: ref } },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(archiveApi.create).toHaveBeenCalledTimes(4);
    expect(result.current.state.error).toBe("unavailable");
    const initialKey = vi.mocked(archiveApi.create).mock.calls[0]?.[4];

    await act(async () => {
      rerender({ refValue: replacementRef });
      await Promise.resolve();
    });
    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(archiveApi.create).toHaveBeenCalledTimes(5);
    for (const [callToken, callRef, callRevision, callMemberId, callKey] of vi.mocked(archiveApi.create).mock.calls) {
      expect(callToken).toBe("token");
      expect(callRef).toEqual(ref);
      expect(callRevision).toBe(revision);
      expect(callMemberId).toBe(memberId);
      expect(callKey).toBe(initialKey);
    }
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "review", requestId: null, error: null });
  });

  it.each(["dismiss", "unmount"] as const)(
    "reconciles an exhausted ambiguous member create after later %s",
    async (teardown) => {
      const archiveApi = api();
      let attempts = 0;
      vi.mocked(archiveApi.create).mockImplementation(() => {
        attempts += 1;
        return attempts <= 4
          ? Promise.reject(new ApiError(500, "archive member create outcome unavailable"))
          : Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
      });
      vi.mocked(archiveApi.cancel).mockResolvedValue(status({
        state: "canceled",
        failureProduct: null,
        fallback: { action: null, reason: null },
      }));
      const hook = renderHook(() => useBackupArchive({
        token: "token",
        role: "admin",
        ref,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }));

      await act(async () => hook.result.current.open());
      await act(async () => hook.result.current.create(memberId));

      expect(archiveApi.create).toHaveBeenCalledTimes(4);
      expect(hook.result.current.state.error).toBe("unavailable");
      const initialCall = vi.mocked(archiveApi.create).mock.calls[0];

      if (teardown === "dismiss") {
        act(() => hook.result.current.dismiss());
      } else {
        hook.unmount();
      }

      await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
      expect(archiveApi.create).toHaveBeenCalledTimes(5);
      const replayCall = vi.mocked(archiveApi.create).mock.calls[4];
      expect(replayCall?.slice(0, 5)).toEqual(initialCall?.slice(0, 5));
      expect(replayCall?.[5]).not.toBe(initialCall?.[5]);
      expect(replayCall?.[5]?.aborted).toBe(false);
      expect(archiveApi.cancel).toHaveBeenCalledWith(
        "token",
        ref,
        revision,
        requestId,
        expect.any(AbortSignal),
      );
      if (teardown === "dismiss") {
        expect(hook.result.current.state).toMatchObject({ phase: "closed", requestId: null, status: null });
      }
    },
  );

  it("does not request a member proof or ticket when download permission is known false", async () => {
    const archiveApi = api();
    const ensureStepUpProof = vi.fn().mockResolvedValue("fresh-member-proof");
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "ready",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "operator",
      ref,
      ensureStepUpProof,
      api: archiveApi,
      downloadAllowed: false,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    await act(async () => result.current.download());

    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(archiveApi.issueTicket).not.toHaveBeenCalled();
  });

  it("does not issue a member ticket when download permission becomes known false during proof", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "ready",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    vi.mocked(archiveApi.issueTicket).mockResolvedValue(downloadTicket());
    let resolveProof: (proof: string) => void = () => {
      throw new Error("member proof did not begin");
    };
    const proof = new Promise<string>((resolve) => {
      resolveProof = resolve;
    });
    const ensureStepUpProof = vi.fn().mockReturnValue(proof);
    const { result, rerender } = renderHook(
      ({ downloadAllowed }) => useBackupArchive({
        token: "token",
        role: "operator",
        ref,
        ensureStepUpProof,
        api: archiveApi,
        downloadAllowed,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { downloadAllowed: true } },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    let downloading: Promise<void> | undefined;
    act(() => {
      downloading = result.current.download();
    });
    rerender({ downloadAllowed: false });
    resolveProof("fresh-member-proof");
    await act(async () => {
      await downloading;
    });

    expect(archiveApi.issueTicket).not.toHaveBeenCalled();
  });

  it("does not hand off a member ticket when download permission becomes known false during issuance", async () => {
    const archiveApi = api();
    const onDownloadTicket = vi.fn();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "ready",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    let resolveTicket: (value: BackupExportDownloadTicket) => void = () => {
      throw new Error("member ticket did not begin");
    };
    const pendingTicket = new Promise<BackupExportDownloadTicket>((resolve) => {
      resolveTicket = resolve;
    });
    vi.mocked(archiveApi.issueTicket).mockReturnValue(pendingTicket);
    const { result, rerender } = renderHook(
      ({ downloadAllowed }) => useBackupArchive({
        token: "token",
        role: "operator",
        ref,
        ensureStepUpProof: vi.fn().mockResolvedValue("fresh-member-proof"),
        api: archiveApi,
        downloadAllowed,
        onPrepareDownload: vi.fn(),
        onDownloadTicket,
      }),
      { initialProps: { downloadAllowed: true } },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    let downloading: Promise<void> | undefined;
    act(() => {
      downloading = result.current.download();
    });
    await waitFor(() => expect(archiveApi.issueTicket).toHaveBeenCalledTimes(1));

    rerender({ downloadAllowed: false });
    resolveTicket(downloadTicket());
    await act(async () => {
      await downloading;
    });

    expect(onDownloadTicket).not.toHaveBeenCalled();
    expect(result.current.state.ticket).toBeNull();
  });

  it("clears a previous ticket error after a later member download succeeds", async () => {
    const archiveApi = api();
    const onDownloadTicket = vi.fn();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "ready",
      failureProduct: null,
      fallback: { action: null, reason: null },
    }));
    const expectedTicket = downloadTicket();
    vi.mocked(archiveApi.issueTicket)
      .mockRejectedValueOnce(new Error("member ticket unavailable"))
      .mockResolvedValueOnce(expectedTicket);
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "operator",
      ref,
      ensureStepUpProof: vi.fn().mockResolvedValue("fresh-member-proof"),
      api: archiveApi,
      onPrepareDownload: vi.fn(),
      onDownloadTicket,
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    await act(async () => result.current.download());
    expect(result.current.state.error).toBe("unavailable");

    await act(async () => result.current.download());

    expect(onDownloadTicket).toHaveBeenCalledWith(expectedTicket);
    expect(result.current.state.error).toBeNull();
    expect(result.current.state.ticket).toBeNull();
  });

  it("reconciles an active member request when the archive is dismissed", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    }));
    let resolveCancel: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("archive dismissal cancellation did not begin");
    };
    const pendingCancel = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveCancel = resolve;
    });
    vi.mocked(archiveApi.cancel).mockImplementation(() => pendingCancel);
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    expect(result.current.state).toMatchObject({ phase: "active", requestId });

    act(() => result.current.dismiss());

    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "closed", requestId: null, status: null });

    resolveCancel(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    await act(async () => {
      await pendingCancel;
    });

    expect(result.current.state).toMatchObject({ phase: "closed", requestId: null, status: null, error: null });
  });

  it("reconciles an active member request when the archive hook unmounts", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    }));
    let resolveCancel: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("archive unmount cancellation did not begin");
    };
    const pendingCancel = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveCancel = resolve;
    });
    vi.mocked(archiveApi.cancel).mockImplementation(() => pendingCancel);
    const { result, unmount } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    expect(result.current.state).toMatchObject({ phase: "active", requestId });

    unmount();

    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    resolveCancel(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    await act(async () => {
      await pendingCancel;
    });
  });

  it("reconciles an active member request before opening a replacement archive", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    }));
    let resolveCancel: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("archive replacement cancellation did not begin");
    };
    const pendingCancel = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveCancel = resolve;
    });
    vi.mocked(archiveApi.cancel).mockImplementation(() => pendingCancel);
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    expect(result.current.state).toMatchObject({ phase: "active", requestId });

    await act(async () => result.current.open());

    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state).toMatchObject({ phase: "review", requestId: null, status: null, error: null });

    resolveCancel(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    await act(async () => {
      await pendingCancel;
    });

    expect(result.current.state).toMatchObject({ phase: "review", requestId: null, status: null, error: null });
  });

  it("allows an active member cancellation to finish after the archive is dismissed", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    }));
    let resolveCancel: (value: BackupArchiveMemberStatus) => void = () => {
      throw new Error("archive cancellation did not begin");
    };
    const pendingCancel = new Promise<BackupArchiveMemberStatus>((resolve) => {
      resolveCancel = resolve;
    });
    vi.mocked(archiveApi.cancel).mockImplementation(() => pendingCancel);
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    let canceling: Promise<void> | undefined;
    act(() => {
      canceling = result.current.cancel();
    });
    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    const cancelSignal = vi.mocked(archiveApi.cancel).mock.calls[0]?.[4];

    act(() => result.current.dismiss());
    expect(cancelSignal?.aborted).toBe(false);
    resolveCancel(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    await act(async () => {
      await canceling;
    });

    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(result.current.state.phase).toBe("closed");
    expect(result.current.state.error).toBeNull();
  });

  it("reconciles a stale cancel failure against the original archive binding", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({
      state: "queued",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: false,
    }));
    let rejectCancel: (reason?: unknown) => void = () => {
      throw new Error("archive cancellation did not begin");
    };
    const failedCancel = new Promise<BackupArchiveMemberStatus>((_resolve, reject) => {
      rejectCancel = reject;
    });
    vi.mocked(archiveApi.cancel)
      .mockReturnValueOnce(failedCancel)
      .mockResolvedValueOnce(status({
        state: "canceled",
        failureProduct: null,
        fallback: { action: null, reason: null },
        terminal: true,
      }));
    const { result, rerender } = renderHook(
      ({ refValue }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref: refValue,
        api: archiveApi,
        onPrepareDownload: vi.fn(),
      }),
      { initialProps: { refValue: ref } },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    let canceling: Promise<void> | undefined;
    act(() => {
      canceling = result.current.cancel();
    });
    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(1));

    await act(async () => {
      rerender({ refValue: replacementRef });
      await Promise.resolve();
    });
    expect(result.current.state).toMatchObject({ phase: "closed", requestId: null, status: null });

    rejectCancel(new ApiError(503, "archive cancellation failed"));
    await act(async () => {
      await canceling;
    });

    await waitFor(() => expect(archiveApi.cancel).toHaveBeenCalledTimes(2));
    expect(archiveApi.cancel).toHaveBeenNthCalledWith(
      2,
      "token",
      ref,
      revision,
      requestId,
      expect.any(AbortSignal),
    );
    expect(result.current.state).toMatchObject({ phase: "closed", requestId: null, status: null, error: null });
  });

  it("aborts a pending member create and cancels one late durable request", async () => {
    const archiveApi = api();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    act(() => {
      void result.current.cancel();
      void result.current.cancel();
    });

    expect(createSignal?.aborted).toBe(true);
    expect(result.current.state.phase).toBe("review");
    expect(archiveApi.cancel).not.toHaveBeenCalled();

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await act(async () => {
      await creating;
    });

    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state.phase).toBe("review");
  });

  it("reconciles a late accepted create when reopening supersedes the pending request", async () => {
    const archiveApi = api();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    await act(async () => result.current.open());
    expect(createSignal?.aborted).toBe(true);

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await act(async () => {
      await creating;
    });

    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state.requestId).toBeNull();
    expect(result.current.state.phase).toBe("review");
  });

  it("reconciles a pending member create when the archive dialog unmounts", async () => {
    const archiveApi = api();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const { result, unmount } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    unmount();
    expect(createSignal?.aborted).toBe(true);

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await act(async () => {
      await creating;
    });

    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
  });

  it("replays an aborted member create to cancel a server-accepted request", async () => {
    const archiveApi = api();
    let rejectCreate: (reason?: unknown) => void = () => {
      throw new Error("archive create did not begin");
    };
    const abortedCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((_resolve, reject) => {
      rejectCreate = reject;
    });
    let createCall = 0;
    let initialKey: string | undefined;
    let replayKey: string | undefined;
    let initialSignal: AbortSignal | undefined;
    let replaySignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, idempotencyKey, signal) => {
      createCall += 1;
      if (createCall === 1) {
        initialKey = idempotencyKey;
        initialSignal = signal;
        return abortedCreate;
      }
      replayKey = idempotencyKey;
      replaySignal = signal;
      return Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    act(() => {
      void result.current.cancel();
    });
    expect(initialSignal?.aborted).toBe(true);

    rejectCreate(new DOMException("request aborted", "AbortError"));
    await act(async () => {
      await creating;
    });

    expect(archiveApi.create).toHaveBeenCalledTimes(2);
    expect(archiveApi.create).toHaveBeenNthCalledWith(
      1,
      "token",
      ref,
      revision,
      memberId,
      initialKey,
      initialSignal,
    );
    expect(archiveApi.create).toHaveBeenNthCalledWith(
      2,
      "token",
      ref,
      revision,
      memberId,
      initialKey,
      replaySignal,
    );
    expect(replayKey).toBe(initialKey);
    expect(replaySignal?.aborted).toBe(false);
    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
    expect(result.current.state.phase).toBe("review");
  });

  it("replays a canceled member create after an ambiguous server failure", async () => {
    const archiveApi = api();
    let rejectCreate: (reason?: unknown) => void = () => {
      throw new Error("archive create did not begin");
    };
    const failedCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((_resolve, reject) => {
      rejectCreate = reject;
    });
    const idempotencyKeys: string[] = [];
    let createCall = 0;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, idempotencyKey) => {
      createCall += 1;
      idempotencyKeys.push(idempotencyKey);
      if (createCall === 1) return failedCreate;
      return Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
    });
    vi.mocked(archiveApi.cancel).mockResolvedValue(status({
      state: "canceled",
      failureProduct: null,
      fallback: { action: null, reason: null },
      terminal: true,
    }));
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    act(() => {
      void result.current.cancel();
    });
    rejectCreate(new ApiError(503, "archive member create outcome unavailable"));
    await act(async () => {
      await creating;
    });

    expect(archiveApi.create).toHaveBeenCalledTimes(2);
    expect(idempotencyKeys[1]).toBe(idempotencyKeys[0]);
    expect(archiveApi.cancel).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).toHaveBeenCalledWith("token", ref, revision, requestId, expect.any(AbortSignal));
  });

  it("does not replay a canceled member create after a definitive API rejection", async () => {
    const archiveApi = api();
    let rejectCreate: (reason?: unknown) => void = () => {
      throw new Error("archive create did not begin");
    };
    const rejectedCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((_resolve, reject) => {
      rejectCreate = reject;
    });
    let createCall = 0;
    let createSignal: AbortSignal | undefined;
    vi.mocked(archiveApi.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createCall += 1;
      if (createCall === 1) {
        createSignal = signal;
        return rejectedCreate;
      }
      return Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
    });
    const { result, unmount } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    let creating: Promise<void> | undefined;
    act(() => {
      creating = result.current.create(memberId);
    });
    await waitFor(() => expect(archiveApi.create).toHaveBeenCalledTimes(1));

    act(() => {
      void result.current.cancel();
    });
    expect(createSignal?.aborted).toBe(true);

    rejectCreate(new ApiError(400, "invalid archive member request"));
    await act(async () => {
      await creating;
    });

    expect(archiveApi.create).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).not.toHaveBeenCalled();
    expect(result.current.state.phase).toBe("review");

    unmount();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(archiveApi.create).toHaveBeenCalledTimes(1);
    expect(archiveApi.cancel).not.toHaveBeenCalled();
  });

  it("accepts one opaque child member without turning its hierarchy into a nested chain", async () => {
    const archiveApi = api();
    vi.mocked(archiveApi.listIndex).mockResolvedValue({
      ...index,
      entries: [
        { id: parentMemberId, parentId: null, displayName: "folder", type: "file", size: 0, mediaType: "application/octet-stream", warning: "none" },
        { ...index.entries[0], parentId: parentMemberId },
      ],
    });
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(archiveApi.create).toHaveBeenCalledWith(
      "token",
      ref,
      revision,
      memberId,
      expect.any(String),
      expect.any(AbortSignal),
    );
  });

  it.each(["encrypted", "unsupported", "limit"] as const)(
    "delegates closed %s fallback only when the existing original download plane is available",
    async (failureProduct) => {
    const archiveApi = api();
    vi.mocked(archiveApi.status).mockResolvedValue(status({ failureProduct }));
    const onPrepareDownload = vi.fn();
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      contentAvailable: true,
      downloadAllowed: true,
      online: true,
      onPrepareDownload,
    }));
    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    await act(async () => result.current.downloadOriginal());
    expect(onPrepareDownload).toHaveBeenCalledWith(ref);
    },
  );

  it("keeps the same closed reason when original download permission or availability is missing", async () => {
    const archiveApi = api();
    const onPrepareDownload = vi.fn();
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      contentAvailable: false,
      downloadAllowed: false,
      online: false,
      onPrepareDownload,
    }));
    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    await act(async () => result.current.downloadOriginal());

    expect(onPrepareDownload).not.toHaveBeenCalled();
    expect(result.current.state.fallback.reason).toBe("original_download_unavailable");
  });

  it("normalizes an unavailable original fallback before rendering an action", async () => {
    const archiveApi = api();
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      contentAvailable: false,
      downloadAllowed: false,
      online: false,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(result.current.state.fallback).toEqual({ action: null, reason: "original_download_unavailable" });
  });

  it("recomputes a terminal original fallback from current capability props without polling again", async () => {
    const archiveApi = api();
    const initialPrepareDownload = vi.fn();
    const currentPrepareDownload = vi.fn();
    const { result, rerender } = renderHook(
      ({ contentAvailable, downloadAllowed, online, onPrepareDownload }) => useBackupArchive({
        token: "token",
        role: "admin",
        ref,
        api: archiveApi,
        contentAvailable,
        downloadAllowed,
        online,
        onPrepareDownload,
      }),
      {
        initialProps: {
          contentAvailable: true,
          downloadAllowed: true,
          online: true,
          onPrepareDownload: initialPrepareDownload,
        },
      },
    );

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));
    expect(result.current.state.fallback).toEqual({ action: "download_original", reason: null });
    expect(archiveApi.status).toHaveBeenCalledTimes(1);

    for (const unavailableCapability of [
      { contentAvailable: false, downloadAllowed: true, online: true },
      { contentAvailable: true, downloadAllowed: false, online: true },
      { contentAvailable: true, downloadAllowed: true, online: false },
    ]) {
      rerender({ ...unavailableCapability, onPrepareDownload: initialPrepareDownload });
      expect(result.current.state.fallback).toEqual({ action: null, reason: "original_download_unavailable" });
      expect(archiveApi.status).toHaveBeenCalledTimes(1);

      rerender({
        contentAvailable: true,
        downloadAllowed: true,
        online: true,
        onPrepareDownload: currentPrepareDownload,
      });
      expect(result.current.state.fallback).toEqual({ action: "download_original", reason: null });
    }

    await act(async () => result.current.downloadOriginal());
    expect(initialPrepareDownload).not.toHaveBeenCalled();
    expect(currentPrepareDownload).toHaveBeenCalledWith(ref);
  });

  it("allows an operator to inspect and request an owned archive member", async () => {
    const archiveApi = api();
    const { result } = renderHook(() => useBackupArchive({
      token: "operator-token",
      role: "operator",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    await act(async () => result.current.open());
    await act(async () => result.current.create(memberId));

    expect(archiveApi.listIndex).toHaveBeenCalledWith("operator-token", ref, expect.any(AbortSignal));
    expect(archiveApi.create).toHaveBeenCalled();
    expect(result.current.state.error).toBeNull();
  });

  it("aborts an in-flight index request and ignores its result after dismiss", async () => {
    let resolveIndex: ((value: BackupArchiveIndex) => void) | null = null;
    let requestSignal: AbortSignal | undefined;
    const pendingIndex = new Promise<BackupArchiveIndex>((resolve) => {
      resolveIndex = resolve;
    });
    const archiveApi = api();
    vi.mocked(archiveApi.listIndex).mockImplementation((_token, _ref, signal) => {
      requestSignal = signal;
      return pendingIndex;
    });
    const { result } = renderHook(() => useBackupArchive({
      token: "token",
      role: "admin",
      ref,
      api: archiveApi,
      onPrepareDownload: vi.fn(),
    }));

    let opening: Promise<void> | undefined;
    await act(async () => {
      opening = result.current.open();
      await Promise.resolve();
    });
    expect(requestSignal).toBeInstanceOf(AbortSignal);
    act(() => result.current.dismiss());
    expect(requestSignal?.aborted).toBe(true);

    await act(async () => {
      resolveIndex?.(index);
      await opening;
    });
    expect(result.current.state.phase).toBe("closed");
    expect(result.current.state.index).toBeNull();
  });
});
