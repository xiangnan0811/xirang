import "@testing-library/jest-dom/vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { ApiError } from "@/lib/api/core";
import { runAxe } from "@/test/a11y-helpers";
import type {
  AssetRef,
  BackupArchiveIndex,
  BackupArchiveMemberStatus,
  BackupExportDownloadTicket,
} from "@/types/domain";

import { ArchiveMemberPanel } from "./archive-member-panel";
import type { BackupArchiveApi } from "./use-backup-archive";

const ref: AssetRef = { recoveryPointId: "a".repeat(32), entryId: "b".repeat(64) };
const parentId = "c".repeat(32);
const memberId = "d".repeat(32);
const requestId = "e".repeat(32);
const indexRevision = "f".repeat(64);

function archiveIndex(): BackupArchiveIndex {
  return {
    schemaVersion: 1,
    indexRevision,
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    entries: [
      { id: parentId, parentId: null, displayName: "reports", type: "file", size: 0, mediaType: "application/octet-stream", warning: "none" },
      { id: memberId, parentId, displayName: "summary.txt", type: "file", size: 12, mediaType: "text/plain", warning: "none" },
    ],
  };
}

function archiveIndexWithEntries(count: number): BackupArchiveIndex {
  return {
    ...archiveIndex(),
    entries: Array.from({ length: count }, (_, index) => ({
      id: index.toString(16).padStart(32, "0"),
      parentId: null,
      displayName: `member-${String(index).padStart(5, "0")}`,
      type: "file" as const,
      size: index,
      mediaType: "application/octet-stream",
      warning: "none" as const,
    })),
  };
}

function archiveIndexWithReverseOrderedHierarchy(): BackupArchiveIndex {
  const hierarchyParentId = "1".repeat(32);
  const children = Array.from({ length: 150 }, (_, index) => ({
    id: (index + 2).toString(16).padStart(32, "0"),
    parentId: hierarchyParentId,
    displayName: `child-${String(index).padStart(3, "0")}`,
    type: "file" as const,
    size: index,
    mediaType: "application/octet-stream",
    warning: "none" as const,
  })).reverse();
  return {
    ...archiveIndex(),
    // The API order is not the presentation order. Descendants deliberately
    // arrive before their parent so the panel must build a stable hierarchy.
    entries: [
      ...children,
      {
        id: hierarchyParentId,
        parentId: null,
        displayName: "archive-root",
        type: "file",
        size: 0,
        mediaType: "application/octet-stream",
        warning: "none",
      },
    ],
  };
}

function readyStatus(): BackupArchiveMemberStatus {
  return {
    schemaVersion: 1,
    requestId,
    state: "ready",
    failureProduct: null,
    fallback: { action: null, reason: null },
    retryable: false,
    terminal: true,
  };
}

function ticket(): BackupExportDownloadTicket {
  return {
    schemaVersion: 1,
    contentUrl: `/api/v1/asset-content/${"1".repeat(32)}`,
    contentType: "text/plain",
    contentLength: 12,
    etag: '"member"',
    range: "none",
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    idleExpiresAt: new Date(Date.now() + 30_000).toISOString(),
  };
}

function archiveApi(): BackupArchiveApi {
  return {
    listIndex: vi.fn().mockResolvedValue(archiveIndex()),
    create: vi.fn().mockResolvedValue({ schemaVersion: 1, requestId, state: "queued" }),
    status: vi.fn().mockResolvedValue(readyStatus()),
    cancel: vi.fn(),
    issueTicket: vi.fn().mockResolvedValue(ticket()),
  };
}

describe("ArchiveMemberPanel", () => {
  it("renders the Admin transport action only for the exact archive-member ticket denial", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.issueTicket).mockRejectedValue(new ApiError(503, "private/archive/member", {
      data: { reason: { code: "secure_transport_required", params: {} } },
    }));
    const rendered = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "admin", ensureStepUpProof: vi.fn().mockResolvedValue("proof") }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /summary\.txt/i }));
    await user.click(await screen.findByRole("button", { name: /download member|下载成员/i }));

    expect(await screen.findByRole("link", { name: /content transport|内容传输/i })).toHaveAttribute(
      "href", "/app/backups/overview#backup-assets-content-transport",
    );
    expect(rendered.container.textContent).not.toContain("private/archive/member");
  });

  it("orders hierarchy before pagination and describes an off-page parent", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.listIndex).mockResolvedValue(archiveIndexWithReverseOrderedHierarchy());
    const { container } = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    const root = await screen.findByRole("button", { name: /archive-root/ });
    const firstChild = screen.getByRole("button", { name: /child-000/ });
    expect(root.compareDocumentPosition(firstChild) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(container.querySelectorAll("button[aria-pressed]")).toHaveLength(100);

    await user.click(screen.getByRole("button", { name: /下一页|Next page/ }));

    const offPageChild = screen.getByRole("button", { name: /child-099/ });
    const contextId = offPageChild.getAttribute("aria-describedby");
    expect(contextId).not.toBeNull();
    const context = document.getElementById(contextId ?? "");
    expect(context).toHaveTextContent("archive-root");
    expect(context).toHaveTextContent(/(?:Level\s*2|第\s*2\s*级)/);
    expect(context).toHaveTextContent(/(?:parent|父级)/i);
    expect(container.querySelectorAll("button[aria-pressed]")).toHaveLength(51);
  });

  it("keeps archive member rows bounded and pages to later members", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.listIndex).mockResolvedValue(archiveIndexWithEntries(100_000));
    const { container } = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    expect(await screen.findByRole("button", { name: /member-00000/ })).toBeInTheDocument();
    expect(container.querySelectorAll("button[aria-pressed]")).toHaveLength(100);

    await user.click(screen.getByRole("button", { name: /下一页|Next page/ }));

    expect(screen.queryByRole("button", { name: /member-00000/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /member-00100/ })).toBeInTheDocument();
    expect(container.querySelectorAll("button[aria-pressed]")).toHaveLength(100);
  });

  it("submits a hierarchy child as one opaque member and offers its ready attachment", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn().mockResolvedValue("fresh-proof") }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));

    expect(api.create).toHaveBeenCalledWith(
      "token",
      ref,
      "f".repeat(64),
      memberId,
      expect.any(String),
      expect.any(AbortSignal),
    );
    await waitFor(() => expect(screen.getByRole("button", { name: /下载成员|Download member/ })).toBeEnabled());
  });

  it("starts a fresh member request when another indexed member is selected after a terminal result", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    let createCount = 0;
    let resolveSecondCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("second archive create did not begin");
    };
    const pendingSecondCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveSecondCreate = resolve;
    });
    vi.mocked(api.create).mockImplementation(() => {
      createCount += 1;
      if (createCount === 1) return Promise.resolve({ schemaVersion: 1, requestId, state: "queued" });
      return pendingSecondCreate;
    });
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    const child = await screen.findByRole("button", { name: /summary\.txt/ });
    const parent = screen.getByRole("button", { name: /reports/ });
    await user.click(child);
    expect(await screen.findByRole("button", { name: /下载成员|Download member/ })).toBeEnabled();

    await user.click(parent);

    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(2));
    expect(api.create).toHaveBeenLastCalledWith(
      "token",
      ref,
      "f".repeat(64),
      parentId,
      expect.any(String),
      expect.any(AbortSignal),
    );
    expect(screen.queryByRole("button", { name: /下载成员|Download member/ })).not.toBeInTheDocument();
    expect(parent).toHaveAttribute("aria-pressed", "true");
    expect(child).toHaveAttribute("aria-pressed", "false");

    await act(async () => {
      resolveSecondCreate({ schemaVersion: 1, requestId, state: "queued" });
    });
  });

  it("clears the selected member after a failed create is reloaded", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.create).mockRejectedValue(new Error("archive member request unavailable"));
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /重新加载归档内容|Reload archive contents/ }));

    expect(await screen.findByRole("button", { name: /summary\.txt/ })).toHaveAttribute("aria-pressed", "false");
  });

  it("uses a fresh asset-download proof for the member ticket and clears it after handoff", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    const ensureStepUpProof = vi.fn().mockResolvedValue("fresh-member-proof");
    const onDownloadTicket = vi.fn();
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        onDownloadTicket={onDownloadTicket}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await user.click(await screen.findByRole("button", { name: /下载成员|Download member/ }));

    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.assetDownload, {
      persist: false,
      reuseCached: false,
    });
    expect(api.issueTicket).toHaveBeenCalledWith("token", ref, requestId, "fresh-member-proof", expect.any(AbortSignal));
    expect(onDownloadTicket).toHaveBeenCalledWith(expect.objectContaining({
      contentUrl: `/api/v1/asset-content/${"1".repeat(32)}`,
      contentLength: 12,
    }));
  });

  it.each(["proof", "ticket"] as const)("renders an accessible error when member %s issuance fails", async (failure) => {
    const user = userEvent.setup();
    const api = archiveApi();
    const ensureStepUpProof = failure === "proof"
      ? vi.fn().mockRejectedValue(new Error("proof unavailable"))
      : vi.fn().mockResolvedValue("fresh-member-proof");
    if (failure === "ticket") {
      vi.mocked(api.issueTicket).mockRejectedValue(new Error("ticket unavailable"));
    }
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await user.click(await screen.findByRole("button", { name: /下载成员|Download member/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/归档内容不可用|Archive contents are unavailable/);
    if (failure === "proof") expect(api.issueTicket).not.toHaveBeenCalled();
  });

  it("does not expose a ready member ticket when download permission is known false", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    const ensureStepUpProof = vi.fn().mockResolvedValue("fresh-member-proof");
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof }}
        contentAvailable
        downloadAllowed={false}
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));

    expect(await screen.findByText(/Ready|已就绪/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /下载成员|Download member/ })).not.toBeInTheDocument();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(api.issueTicket).not.toHaveBeenCalled();
  });

  it("keeps cancellation available while member creation is pending", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    vi.mocked(api.create).mockReturnValue(pendingCreate);
    vi.mocked(api.status).mockResolvedValue({
      ...readyStatus(),
      state: "queued",
      terminal: false,
    });
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    expect(screen.getByRole("button", { name: /取消|Cancel/ })).toBeEnabled();

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    expect(await screen.findByRole("button", { name: /取消|Cancel/ })).toBeEnabled();
  });

  it("keeps cancellation available after a pending member status poll fails", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.status).mockRejectedValueOnce(new Error("archive member status unavailable"));
    vi.mocked(api.cancel).mockResolvedValue({ ...readyStatus(), state: "canceled" });
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /取消|Cancel/ }));

    await waitFor(() => expect(api.cancel).toHaveBeenCalledWith("token", ref, indexRevision, requestId, expect.any(AbortSignal)));
  });

  it("keeps pending-member error recovery controls axe-clean", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.status).mockRejectedValueOnce(new Error("archive member status unavailable"));
    const { container } = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    expect(await screen.findByRole("button", { name: /取消|Cancel/ })).toBeEnabled();
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("keeps a pending member create when equivalent archive props rerender", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(api.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    const rendered = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    rendered.rerender(
      <ArchiveMemberPanel
        refValue={{ ...ref }}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    expect(createSignal?.aborted).toBe(false);
    expect(api.listIndex).toHaveBeenCalledTimes(1);

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await waitFor(() => expect(screen.getByRole("button", { name: /下载成员|Download member/ })).toBeEnabled());
    expect(api.cancel).not.toHaveBeenCalled();
  });

  it("keeps a pending member create while live archive availability changes", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(api.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    const commonProps = {
      refValue: ref,
      runtime: { token: "token", role: "operator" as const, ensureStepUpProof: vi.fn() },
      onPrepareDownload: vi.fn(),
      api,
    };
    const rendered = render(
      <ArchiveMemberPanel
        {...commonProps}
        contentAvailable
        downloadAllowed
        online
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));

    rendered.rerender(
      <ArchiveMemberPanel
        {...commonProps}
        contentAvailable
        downloadAllowed
        online={false}
      />,
    );
    expect(createSignal?.aborted).toBe(false);
    expect(api.cancel).not.toHaveBeenCalled();

    rendered.rerender(
      <ArchiveMemberPanel
        {...commonProps}
        contentAvailable={false}
        downloadAllowed
        online={false}
      />,
    );
    expect(createSignal?.aborted).toBe(false);
    expect(api.cancel).not.toHaveBeenCalled();

    rendered.rerender(
      <ArchiveMemberPanel
        {...commonProps}
        contentAvailable={false}
        downloadAllowed={false}
        online={false}
      />,
    );
    expect(createSignal?.aborted).toBe(false);
    expect(api.cancel).not.toHaveBeenCalled();
    expect(api.create).toHaveBeenCalledTimes(1);
    expect(api.listIndex).toHaveBeenCalledTimes(1);

    resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    await waitFor(() => expect(screen.getByText(/已就绪|Ready/)).toBeInTheDocument());
    expect(api.cancel).not.toHaveBeenCalled();
  });

  it("registers a dialog dismissal handler that reconciles a late accepted member create", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    let resolveCreate: (value: Awaited<ReturnType<BackupArchiveApi["create"]>>) => void = () => {
      throw new Error("archive create did not begin");
    };
    const pendingCreate = new Promise<Awaited<ReturnType<BackupArchiveApi["create"]>>>((resolve) => {
      resolveCreate = resolve;
    });
    let createSignal: AbortSignal | undefined;
    vi.mocked(api.create).mockImplementation((_token, _ref, _revision, _memberId, _idempotencyKey, signal) => {
      createSignal = signal;
      return pendingCreate;
    });
    const dismissHandlerRef: { current: (() => void) | null } = { current: null };
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        onDismissHandlerRegister={(handler) => {
          dismissHandlerRef.current = handler;
          return () => {
            if (dismissHandlerRef.current === handler) dismissHandlerRef.current = null;
          };
        }}
        api={api}
      />,
    );

    await waitFor(() => expect(dismissHandlerRef.current).toEqual(expect.any(Function)));
    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await waitFor(() => expect(api.create).toHaveBeenCalledTimes(1));
    const dismiss = dismissHandlerRef.current;
    if (!dismiss) throw new Error("archive dialog dismissal handler was not registered");
    await act(async () => {
      dismiss();
    });
    expect(createSignal?.aborted).toBe(true);

    await act(async () => {
      resolveCreate({ schemaVersion: 1, requestId, state: "queued" });
    });
    await waitFor(() => expect(api.cancel).toHaveBeenCalledTimes(1));
    expect(api.cancel).toHaveBeenCalledWith("token", ref, indexRevision, requestId, expect.any(AbortSignal));
  });

  it("opens a ready member ticket when no handoff callback is supplied", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn().mockResolvedValue("fresh-member-proof") }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    await user.click(await screen.findByRole("button", { name: /下载成员|Download member/ }));

    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const clickedAnchor = click.mock.instances[click.mock.instances.length - 1];
    expect(clickedAnchor).toBeInstanceOf(HTMLAnchorElement);
    if (!(clickedAnchor instanceof HTMLAnchorElement)) throw new Error("member download anchor was not clicked");
    expect(clickedAnchor.href).toBe(new URL(ticket().contentUrl, window.location.href).href);
    expect(clickedAnchor.rel).toBe("noreferrer");
  });

  it.each(["encrypted", "unsupported", "limit"] as const)(
    "delegates the closed %s fallback to the existing original download action",
    async (failureProduct) => {
      const user = userEvent.setup();
      const api = archiveApi();
      vi.mocked(api.status).mockResolvedValue({
        ...readyStatus(),
        state: "failed",
        failureProduct,
        fallback: { action: "download_original", reason: null },
      });
      const onPrepareDownload = vi.fn();
      render(
        <ArchiveMemberPanel
          refValue={ref}
          runtime={{ token: "token", role: "admin", ensureStepUpProof: vi.fn() }}
          contentAvailable
          downloadAllowed
          online
          onPrepareDownload={onPrepareDownload}
          api={api}
        />,
      );

      await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
      await user.click(await screen.findByRole("button", { name: /下载原件|Download original/ }));

      expect(onPrepareDownload).toHaveBeenCalledWith(ref);
      expect(screen.queryByRole("button", { name: /Recovery|恢复/ })).not.toBeInTheDocument();
    },
  );

  it("renders the same no-leak reason when original download is unavailable", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.status).mockResolvedValue({
      ...readyStatus(),
      state: "failed",
      failureProduct: "limit",
      fallback: { action: "download_original", reason: null },
    });
    render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "admin", ensureStepUpProof: vi.fn() }}
        contentAvailable={false}
        downloadAllowed={false}
        online={false}
        onPrepareDownload={vi.fn()}
        api={api}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));

    expect(await screen.findByText(/原件下载不可用|Original download is unavailable/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /下载原件|Download original/ })).not.toBeInTheDocument();
  });

  it("re-evaluates a terminal original fallback after availability props change", async () => {
    const user = userEvent.setup();
    const api = archiveApi();
    vi.mocked(api.status).mockResolvedValue({
      ...readyStatus(),
      state: "failed",
      failureProduct: "limit",
      fallback: { action: "download_original", reason: null },
    });
    const initialPrepareDownload = vi.fn();
    const currentPrepareDownload = vi.fn();
    const props = {
      refValue: ref,
      runtime: { token: "token", role: "admin" as const, ensureStepUpProof: vi.fn() },
      online: true,
      api,
    };
    const rendered = render(
      <ArchiveMemberPanel
        {...props}
        contentAvailable
        downloadAllowed
        onPrepareDownload={initialPrepareDownload}
      />,
    );

    await user.click(await screen.findByRole("button", { name: /summary\.txt/ }));
    expect(await screen.findByRole("button", { name: /下载原件|Download original/ })).toBeEnabled();

    rendered.rerender(
      <ArchiveMemberPanel
        {...props}
        contentAvailable={false}
        downloadAllowed
        onPrepareDownload={initialPrepareDownload}
      />,
    );
    expect(await screen.findByText(/原件下载不可用|Original download is unavailable/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /下载原件|Download original/ })).not.toBeInTheDocument();

    rendered.rerender(
      <ArchiveMemberPanel
        {...props}
        contentAvailable
        downloadAllowed
        onPrepareDownload={currentPrepareDownload}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /下载原件|Download original/ }));
    expect(initialPrepareDownload).not.toHaveBeenCalled();
    expect(currentPrepareDownload).toHaveBeenCalledWith(ref);
  });

  it("keeps the indexed-member review axe-clean", async () => {
    const { container } = render(
      <ArchiveMemberPanel
        refValue={ref}
        runtime={{ token: "token", role: "operator", ensureStepUpProof: vi.fn() }}
        contentAvailable
        downloadAllowed
        online
        onPrepareDownload={vi.fn()}
        api={archiveApi()}
      />,
    );

    expect(await screen.findByRole("button", { name: /summary\.txt/ })).toBeInTheDocument();
    expect(await runAxe(container)).toHaveNoViolations();
  });
});
