import "@testing-library/jest-dom/vitest";
import { StrictMode, useEffect, useRef, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BackupAsset, BackupContentRenderer, BackupContentTicket } from "@/types/domain";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetPreview } from "./asset-preview";
import { selectBackupAssetExactPreviewProduct } from "./asset-preview-model";

const {
  archiveDismissMock,
  archivePanelRenderMock,
  archiveUnregisters,
  processingPanelRenderMock,
} = vi.hoisted(() => ({
  archiveDismissMock: vi.fn(),
  archivePanelRenderMock: vi.fn(),
  archiveUnregisters: [] as Array<(() => void) | undefined>,
  processingPanelRenderMock: vi.fn(),
}));

type SyntheticProcessingPanelProps = {
  onOpenPreview: (source: "derived" | "native") => void;
  onBrowseArchive?: () => void;
};

type SyntheticArchiveMemberPanelProps = {
  onDismissHandlerRegister?: (handler: () => void) => () => void;
};

vi.mock("./backup-asset-processing-panel", () => ({
  BackupAssetProcessingPanel: (props: SyntheticProcessingPanelProps) => {
    processingPanelRenderMock(props);
    return (
      <div data-testid="synthetic-processing-panel">
        <button type="button" data-testid="synthetic-derived-preview" onClick={() => props.onOpenPreview("derived")} />
        <button type="button" data-testid="synthetic-native-preview" onClick={() => props.onOpenPreview("native")} />
        <button type="button" data-testid="synthetic-browse-archive" onClick={props.onBrowseArchive} />
      </div>
    );
  },
}));

vi.mock("./archive-member-panel", () => ({
  ArchiveMemberPanel: (props: SyntheticArchiveMemberPanelProps) => {
    const { onDismissHandlerRegister } = props;
    const dismissHandlerRef = useRef(() => archiveDismissMock());
    archivePanelRenderMock(props);
    useEffect(() => {
      const unregister = onDismissHandlerRegister?.(dismissHandlerRef.current);
      archiveUnregisters.push(unregister);
      return unregister;
    }, [onDismissHandlerRegister]);
    return <section data-testid="synthetic-archive-member-panel">Archive member panel</section>;
  },
}));

const contentUrl = `/api/v1/asset-content/${"d".repeat(32)}`;
const processingRuntime = {
  token: "processing-token",
  role: "admin" as const,
  ensureStepUpProof: vi.fn(),
};

function asset(overrides: Partial<BackupAsset> = {}): BackupAsset {
  return { ...buildAssetRows(1)[0].asset, entryType: "file", ...overrides };
}

function ticket(
  renderer: BackupContentRenderer,
  overrides: Partial<BackupContentTicket> = {}
): BackupContentTicket {
  const products: Record<
    BackupContentRenderer,
    Pick<BackupContentTicket, "profile" | "contentType" | "range" | "action">
  > = {
    plain_text: {
      profile: "text_v2",
      contentType: "text/plain; charset=utf-8",
      range: "none",
      action: "preview",
    },
    escaped_text: {
      profile: "text_v1",
      contentType: "text/plain; charset=utf-8",
      range: "none",
      action: "preview",
    },
    safe_raster: { profile: "raster_v1", contentType: "image/png", range: "single", action: "preview" },
    same_origin_pdf: { profile: "pdf_v1", contentType: "application/pdf", range: "single", action: "preview" },
    native_audio: { profile: "audio_v1", contentType: "audio/mpeg", range: "single", action: "preview" },
    native_video: { profile: "video_v1", contentType: "video/mp4", range: "single", action: "preview" },
    metadata_hex: {
      profile: "hex_v1",
      contentType: "text/plain; charset=utf-8",
      range: "none",
      action: "preview",
    },
    attachment: {
      profile: "original_v1",
      contentType: "application/octet-stream",
      range: "single",
      action: "download",
    },
  };
  const now = Date.now();
  return {
    schemaVersion: 1,
    contentUrl,
    renderer,
    contentLength: 128,
    etag: '"synthetic"',
    lastModified: null,
    classification: "non_secret",
    expiresAt: new Date(now + 10 * 60_000).toISOString(),
    idleExpiresAt: new Date(now + 5 * 60_000).toISOString(),
    capabilityReason: null,
    fallbackActions: [],
    truncated: false,
    ...products[renderer],
    ...overrides,
  };
}

function renderPreview(
  renderer: BackupContentRenderer,
  options: {
    asset?: BackupAsset;
    ticket?: Partial<BackupContentTicket>;
    onRenew?: () => void;
    onDetach?: () => void;
  } = {}
) {
  return render(
    <AssetPreview
      asset={options.asset ?? asset()}
      resource={{ status: "ready", value: ticket(renderer, options.ticket) }}
      canPreview
      canDownload
      onLoadExactPreview={vi.fn()}
      onRetry={vi.fn()}
      onRenew={options.onRenew ?? vi.fn()}
      onPrepareDownload={vi.fn()}
      onDetach={options.onDetach ?? vi.fn()}
    />
  );
}

describe("backup asset preview product", () => {
  it.each([
    ["text/yaml", "escaped_text", "text_v1"],
    ["text/html", "escaped_text", "text_v1"],
    ["application/xml", "escaped_text", "text_v1"],
    ["image/svg+xml", "escaped_text", "text_v1"],
    ["image/png", "safe_raster", "raster_v1"],
    ["application/pdf", "same_origin_pdf", "pdf_v1"],
    ["audio/mpeg", "native_audio", "audio_v1"],
    ["video/mp4", "native_video", "video_v1"],
    ["application/octet-stream", "metadata_hex", "hex_v1"],
  ] as const)("maps %s to the closed %s renderer", (mimeType, renderer, profile) => {
    expect(selectBackupAssetExactPreviewProduct(asset({ mimeType }))).toEqual({ renderer, profile });
  });

  it("forces non-file entries to metadata/hex", () => {
    expect(selectBackupAssetExactPreviewProduct(asset({ entryType: "directory", mimeType: "image/png" }))).toEqual({
      renderer: "metadata_hex",
      profile: "hex_v1",
    });
  });
});

describe("AssetPreview", () => {
  beforeEach(() => {
    archiveDismissMock.mockReset();
    archivePanelRenderMock.mockReset();
    archiveUnregisters.length = 0;
    processingPanelRenderMock.mockReset();
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it.each(["preview", "original download"])("renders Admin transport guidance for %s without leaking identifiers", (surface) => {
    const previewAsset = asset();
    const rendered = render(
      <AssetPreview
        asset={previewAsset}
        resource={{
          status: "error",
          value: null,
          error: {
            code: "secure_transport_required",
            translationKey: "backupAssets.errors.secureTransportRequired",
            retryable: false,
            action: "none",
          },
        }}
        canPreview={surface === "preview"}
        canDownload={surface === "original download"}
        processingRuntime={{ ...processingRuntime, role: "admin" }}
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    expect(screen.getByRole("link", { name: /content transport|内容传输/i })).toHaveAttribute(
      "href", "/app/backups/overview#backup-assets-content-transport",
    );
    expect(rendered.container.textContent).not.toContain(previewAsset.ref.entryId);
  });

  it("shows Operator transport guidance without an action and keeps Viewer guidance generic", () => {
    const props = {
      asset: asset(),
      resource: {
        status: "error" as const,
        value: null,
        error: {
          code: "secure_transport_required" as const,
          translationKey: "backupAssets.errors.secureTransportRequired" as const,
          retryable: false,
          action: "none" as const,
        },
      },
      canPreview: true,
      canDownload: true,
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(<AssetPreview {...props} processingRuntime={{ ...processingRuntime, role: "operator" }} />);
    expect(screen.getByText(/HTTPS/i)).toBeInTheDocument();
    expect(screen.getByText(/Admin|管理员/i)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    rendered.rerender(<AssetPreview {...props} processingRuntime={{ ...processingRuntime, role: "viewer" }} />);
    expect(screen.queryByText(/Admin|管理员|settings|设置/i)).not.toBeInTheDocument();
  });

  it("keeps one bounded viewport height before and after content loads", () => {
    const previewAsset = asset();
    const props = {
      asset: previewAsset,
      canPreview: true,
      canDownload: true,
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview {...props} resource={{ status: "idle", value: null }} />
    );

    expect(screen.getByTestId("asset-preview-viewport")).toHaveClass("min-h-[24rem]", "flex-1");
    expect(screen.getByTestId("asset-preview-viewport")).not.toHaveClass("h-[18rem]", "max-h-[18rem]");

    rendered.rerender(
      <AssetPreview
        {...props}
        resource={{ status: "ready", value: ticket("escaped_text") }}
      />
    );
    expect(screen.getByTitle(/File preview|文件预览/)).toHaveClass("h-full", "max-h-full");
  });

  it("announces current loading and typed error states without putting the filename in the error region", () => {
    const previewAsset = asset({ name: "synthetic-visible-only.yml" });
    const common = {
      asset: previewAsset,
      canPreview: true,
      canDownload: false,
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview {...common} resource={{ status: "loading", value: null }} />,
    );

    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
    expect(screen.getByRole("status")).toHaveAttribute("aria-busy", "true");

    rendered.rerender(
      <AssetPreview
        {...common}
        resource={{
          status: "error",
          value: null,
          error: {
            code: "temporarily_unavailable",
            translationKey: "backupAssets.errors.temporarilyUnavailable",
            retryable: true,
            action: "retry",
          },
        }}
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert).not.toHaveTextContent(previewAsset.name);
    expect(screen.getByRole("button", { name: /Retry preview|重试预览/ })).toBeInTheDocument();
  });

  it("shows direct source guidance and a safe correlation ID without Worker guidance", () => {
    const rendered = render(
      <AssetPreview
        asset={asset()}
        resource={{
          status: "error",
          value: null,
          error: {
            code: "temporarily_unavailable",
            translationKey: "backupAssets.errors.temporarilyUnavailable",
            retryable: true,
            action: "retry",
            sourceStage: "read",
            correlationId: "safe-correlation",
          },
        }}
        canPreview
        canDownload={false}
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />,
    );

    expect(rendered.container).toHaveTextContent(/source.*read|读取.*内容源/i);
    expect(rendered.container).toHaveTextContent(/safe-correlation/);
    expect(screen.queryByText(/ZIP browsing|ZIP 浏览/)).not.toBeInTheDocument();
  });

  it("loads the processing controller only after explicit interaction", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "image/png" });
    render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={processingRuntime}
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    expect(screen.queryByTestId("synthetic-processing-panel")).not.toBeInTheDocument();
    expect(processingPanelRenderMock).not.toHaveBeenCalled();
    const processingButton = screen.getByRole("button", { name: /Processing status|增强处理状态/ });
    expect(processingButton).toHaveClass("min-h-11", "touch-target");
    await user.click(processingButton);
    expect(await screen.findByTestId("synthetic-processing-panel")).toBeInTheDocument();
    expect(processingPanelRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      token: "processing-token",
      asset: previewAsset,
    }));
  });

  it("loads the archive member dialog only after the ready processing action", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/zip" });
    render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
        archiveContentAvailable
        archiveDownloadAllowed
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    expect(screen.queryByTestId("synthetic-archive-member-panel")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));

    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    expect(await screen.findByTestId("synthetic-archive-member-panel")).toBeInTheDocument();
    expect(archivePanelRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      refValue: previewAsset.ref,
      runtime: expect.objectContaining({ token: "processing-token", role: "operator" }),
      contentAvailable: true,
      downloadAllowed: true,
    }));
  });

  it("returns focus to the operator archive trigger when the member dialog closes", async () => {
    const user = userEvent.setup();
    render(
      <AssetPreview
        asset={asset({ mimeType: "application/zip" })}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    const trigger = await screen.findByTestId("synthetic-browse-archive");
    await user.click(trigger);
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(archiveDismissMock).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("falls back to the inspector heading when the archive trigger is disconnected on close", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <h2 tabIndex={-1}>Selected archive</h2>
        <section role="tabpanel">
          <AssetPreview
            asset={asset({ mimeType: "application/zip" })}
            resource={{ status: "idle", value: null }}
            canPreview
            canDownload
            processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
            onLoadExactPreview={vi.fn()}
            onRetry={vi.fn()}
            onRenew={vi.fn()}
            onPrepareDownload={vi.fn()}
            onDetach={vi.fn()}
          />
        </section>
      </div>
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    const trigger = await screen.findByTestId("synthetic-browse-archive");
    await user.click(trigger);
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();

    trigger.remove();
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.getByRole("heading", { name: "Selected archive" })).toHaveFocus());
  });

  it("falls back to the workspace results region when the trigger and inspector are removed on close", async () => {
    const user = userEvent.setup();
    function WorkspaceFixture() {
      const [inspectorOpen, setInspectorOpen] = useState(true);
      return (
        <div data-testid="backup-assets-workspace">
          <section aria-label="Results" tabIndex={-1}>Workspace results</section>
          <button type="button" onClick={() => setInspectorOpen(false)}>Close inspector</button>
          {inspectorOpen ? (
            <div data-testid="archive-inspector">
              <h2 tabIndex={-1}>Selected archive</h2>
              <section role="tabpanel">
                <AssetPreview
                  asset={asset({ mimeType: "application/zip" })}
                  resource={{ status: "idle", value: null }}
                  canPreview
                  canDownload
                  processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
                  onLoadExactPreview={vi.fn()}
                  onRetry={vi.fn()}
                  onRenew={vi.fn()}
                  onPrepareDownload={vi.fn()}
                  onDetach={vi.fn()}
                />
              </section>
            </div>
          ) : null}
        </div>
      );
    }
    render(
      <WorkspaceFixture />
    );

    const closeInspector = screen.getByRole("button", { name: "Close inspector" });
    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();

    fireEvent.click(closeInspector);

    await waitFor(() => expect(screen.getByRole("region", { name: "Results" })).toHaveFocus());
    expect(archiveDismissMock).toHaveBeenCalledTimes(1);
  });

  it("keeps an archive dialog closed after switching away and back until browse is selected again", async () => {
    const user = userEvent.setup();
    const firstAsset = asset({ mimeType: "application/zip" });
    const secondAsset = asset({
      mimeType: "application/zip",
      ref: { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) },
    });
    const commonProps = {
      resource: { status: "idle" as const, value: null },
      canPreview: true,
      canDownload: true,
      processingRuntime: { token: "processing-token", role: "operator" as const, ensureStepUpProof: vi.fn() },
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(<AssetPreview {...commonProps} asset={firstAsset} />);

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();

    rendered.rerender(<AssetPreview {...commonProps} asset={secondAsset} />);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument());

    rendered.rerender(<AssetPreview {...commonProps} asset={firstAsset} />);
    expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument();
    expect(archiveDismissMock).toHaveBeenCalledTimes(1);

    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    expect(screen.getAllByRole("dialog", { name: /Archive contents|归档内容/ })).toHaveLength(1);
  });

  it("keeps a successor archive dismissal registered when an older panel unregister runs again in the same session", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/zip" });
    const commonProps = {
      resource: { status: "idle" as const, value: null },
      canPreview: true,
      canDownload: true,
      processingRuntime: { token: "processing-token", role: "operator" as const, ensureStepUpProof: vi.fn() },
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(<AssetPreview {...commonProps} asset={previewAsset} />);

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    await waitFor(() => expect(archiveUnregisters).toHaveLength(1));
    const firstUnregister = archiveUnregisters[0];
    if (!firstUnregister) throw new Error("first archive dismissal handler was not registered");

    await user.click(screen.getByRole("button", { name: /Close archive contents|关闭归档内容/ }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument());
    expect(archiveDismissMock).toHaveBeenCalledTimes(1);
    archiveDismissMock.mockClear();

    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    await waitFor(() => expect(archiveUnregisters).toHaveLength(2));

    firstUnregister();
    rendered.unmount();

    expect(archiveDismissMock).toHaveBeenCalledTimes(1);
  });

  it("keeps the archive dialog and member panel alive while live original-download availability changes", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/zip" });
    const commonProps = {
      asset: previewAsset,
      resource: { status: "idle" as const, value: null },
      canPreview: true,
      canDownload: true,
      processingRuntime: { token: "processing-token", role: "operator" as const, ensureStepUpProof: vi.fn() },
      archiveContentAvailable: true,
      archiveDownloadAllowed: true,
      online: true,
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(<AssetPreview {...commonProps} />);

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    expect(archiveUnregisters).toHaveLength(1);

    rendered.rerender(
      <AssetPreview
        {...commonProps}
        online={false}
      />,
    );

    expect(screen.getByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    await waitFor(() => expect(archivePanelRenderMock.mock.calls.at(-1)?.[0]).toEqual(expect.objectContaining({
      online: false,
      contentAvailable: true,
      downloadAllowed: true,
    })));
    expect(archiveDismissMock).not.toHaveBeenCalled();
    expect(archiveUnregisters).toHaveLength(1);

    rendered.rerender(
      <AssetPreview
        {...commonProps}
        online={false}
        archiveContentAvailable={false}
      />,
    );

    expect(screen.getByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    await waitFor(() => expect(archivePanelRenderMock.mock.calls.at(-1)?.[0]).toEqual(expect.objectContaining({
      online: false,
      contentAvailable: false,
      downloadAllowed: true,
    })));
    expect(archiveDismissMock).not.toHaveBeenCalled();
    expect(archiveUnregisters).toHaveLength(1);

    rendered.rerender(
      <AssetPreview
        {...commonProps}
        online={false}
        archiveContentAvailable={false}
        archiveDownloadAllowed={false}
      />,
    );

    expect(screen.getByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    await waitFor(() => expect(archivePanelRenderMock.mock.calls.at(-1)?.[0]).toEqual(expect.objectContaining({
      online: false,
      contentAvailable: false,
      downloadAllowed: false,
    })));
    expect(archiveDismissMock).not.toHaveBeenCalled();
    expect(archiveUnregisters).toHaveLength(1);
  });

  it("keeps an archive dialog closed after its runtime becomes ineligible until browse is selected again", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/zip" });
    const commonProps = {
      asset: previewAsset,
      resource: { status: "idle" as const, value: null },
      canPreview: true,
      canDownload: true,
      archiveContentAvailable: true,
      archiveDownloadAllowed: true,
      onLoadExactPreview: vi.fn(),
      onRetry: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview
        {...commonProps}
        processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();

    rendered.rerender(
      <AssetPreview
        {...commonProps}
        processingRuntime={{ token: "processing-token", role: "viewer", ensureStepUpProof: vi.fn() }}
      />,
    );
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument());
    expect(archiveDismissMock).toHaveBeenCalledTimes(1);

    rendered.rerender(
      <AssetPreview
        {...commonProps}
        processingRuntime={{ token: "processing-token", role: "operator", ensureStepUpProof: vi.fn() }}
      />,
    );
    expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument();

    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(await screen.findByRole("dialog", { name: /Archive contents|归档内容/ })).toBeInTheDocument();
    expect(screen.getAllByRole("dialog", { name: /Archive contents|归档内容/ })).toHaveLength(1);
  });

  it("does not give a viewer processing surface an archive callback", async () => {
    const user = userEvent.setup();
    render(
      <AssetPreview
        asset={asset({ mimeType: "application/zip" })}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={{ token: "processing-token", role: "viewer", ensureStepUpProof: vi.fn() }}
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));

    expect(processingPanelRenderMock).toHaveBeenLastCalledWith(expect.objectContaining({ onBrowseArchive: undefined }));
    await user.click(await screen.findByTestId("synthetic-browse-archive"));
    expect(screen.queryByRole("dialog", { name: /Archive contents|归档内容/ })).not.toBeInTheDocument();
  });

  it.each([
    ["application/pdf", "image/png"],
    ["application/zip", "text/plain"],
  ])("adapts derived %s preview MIME without changing its identity fields", async (mimeType, derivedMimeType) => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType });
    const onLoadExactPreview = vi.fn();
    render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={processingRuntime}
        onLoadExactPreview={onLoadExactPreview}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-derived-preview"));

    expect(onLoadExactPreview).toHaveBeenCalledTimes(1);
    const selected = onLoadExactPreview.mock.calls[0][0] as BackupAsset;
    expect(selected).not.toBe(previewAsset);
    expect(selected.ref).toBe(previewAsset.ref);
    expect(selected.mimeType).toBe(derivedMimeType);
    for (const key of Object.keys(previewAsset) as Array<keyof BackupAsset>) {
      if (key === "mimeType") continue;
      expect(selected[key]).toBe(previewAsset[key]);
    }
  });

  it("passes the exact original asset object for native fallback", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/pdf" });
    const onLoadExactPreview = vi.fn();
    render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={processingRuntime}
        onLoadExactPreview={onLoadExactPreview}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-native-preview"));

    expect(onLoadExactPreview).toHaveBeenCalledTimes(1);
    expect(onLoadExactPreview.mock.calls[0][0]).toBe(previewAsset);
  });

  it("keeps derived exact preview separate from ordinary automatic preview controls", async () => {
    const user = userEvent.setup();
    const previewAsset = asset({ mimeType: "application/pdf" });
    const issuedProducts = vi.fn();
    const onLoadExactPreview = vi.fn((selected: BackupAsset) => {
      issuedProducts(selectBackupAssetExactPreviewProduct(selected));
    });
    const onRenew = vi.fn();
    const commonProps = {
      asset: previewAsset,
      canPreview: true,
      canDownload: true,
      processingRuntime,
      onLoadExactPreview,
      onRetry: vi.fn(),
      onRenew,
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview {...commonProps} resource={{ status: "idle", value: null }} />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    await user.click(await screen.findByTestId("synthetic-derived-preview"));
    expect(issuedProducts).toHaveBeenLastCalledWith({ renderer: "safe_raster", profile: "raster_v1" });

    rendered.rerender(
      <AssetPreview {...commonProps} resource={{ status: "ready", value: ticket("safe_raster") }} />
    );
    expect(screen.queryByRole("button", { name: /Refresh preview|刷新预览/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Load preview|加载预览/ })).not.toBeInTheDocument();
    expect(onRenew).not.toHaveBeenCalled();
    expect(onLoadExactPreview).toHaveBeenCalledTimes(1);
    expect(issuedProducts).toHaveBeenLastCalledWith({ renderer: "safe_raster", profile: "raster_v1" });
  });

  it("detaches once without selecting a processing preview on unmount", async () => {
    const user = userEvent.setup();
    const onLoadExactPreview = vi.fn();
    const onDetach = vi.fn();
    const rendered = render(
      <AssetPreview
        asset={asset({ mimeType: "application/zip" })}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        processingRuntime={processingRuntime}
        onLoadExactPreview={onLoadExactPreview}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={onDetach}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing status|增强处理状态/ }));
    expect(await screen.findByTestId("synthetic-processing-panel")).toBeInTheDocument();
    rendered.unmount();

    await waitFor(() => expect(onDetach).toHaveBeenCalledTimes(1));
    expect(onLoadExactPreview).not.toHaveBeenCalled();
  });

  it("does not detach during StrictMode effect replay and detaches once on real unmount", async () => {
    const onDetach = vi.fn();
    const rendered = render(
      <StrictMode>
        <AssetPreview
          asset={asset()}
          resource={{ status: "idle", value: null }}
          canPreview
          canDownload
          onLoadExactPreview={vi.fn()}
          onRetry={vi.fn()}
          onRenew={vi.fn()}
          onPrepareDownload={vi.fn()}
          onDetach={onDetach}
        />
      </StrictMode>,
    );

    expect(onDetach).not.toHaveBeenCalled();
    rendered.unmount();
    await waitFor(() => expect(onDetach).toHaveBeenCalledTimes(1));
  });

  it.each(["plain_text", "escaped_text"] as const)(
    "renders %s faithfully in a sandboxed opaque frame without active markup",
    (renderer) => {
      renderPreview(renderer, { asset: asset({ name: "unsafe.html", mimeType: "text/html" }) });

      const frame = screen.getByTitle(/File preview|文件预览/);
      expect(frame).toHaveAttribute("src", contentUrl);
      expect(frame).toHaveAttribute("sandbox", "");
      expect(frame).not.toHaveAttribute("srcdoc");
      const viewport = screen.getByTestId("asset-preview-viewport");
      expect(viewport.querySelector("script, svg")).not.toBeInTheDocument();
      expect(viewport).toHaveClass("min-h-[24rem]");
    },
  );

  it("shows a localized notice for a bounded truncated text preview", () => {
    renderPreview("plain_text", { ticket: { truncated: true } });

    expect(screen.getByRole("status")).toHaveTextContent(/beginning of the file|文件开头/);
  });

  it("renders escaped content in a sandboxed opaque frame without active markup", () => {
    renderPreview("escaped_text", { asset: asset({ name: "unsafe.html", mimeType: "text/html" }) });

    const frame = screen.getByTitle(/File preview|文件预览/);
    expect(frame).toHaveAttribute("src", contentUrl);
    expect(frame).toHaveAttribute("sandbox", "");
    expect(frame).not.toHaveAttribute("srcdoc");
    const viewport = screen.getByTestId("asset-preview-viewport");
    expect(viewport.querySelector("script, svg")).not.toBeInTheDocument();
    expect(viewport).toHaveClass("min-h-[24rem]");
  });

  it("uses safe native elements for raster, PDF, audio, video, and metadata/hex", () => {
    const raster = renderPreview("safe_raster", { asset: asset({ name: "safe.png", mimeType: "image/png" }) });
    expect(screen.getByRole("img", { name: "safe.png" }))
      .toHaveAttribute("src", contentUrl);
    expect(screen.getByRole("img", { name: "safe.png" })).toHaveClass("max-h-full");
    raster.unmount();

    const pdf = renderPreview("same_origin_pdf");
    expect(screen.getByTitle(/PDF preview|PDF 预览/))
      .toHaveAttribute("sandbox", "");
    expect(screen.getByTitle(/PDF preview|PDF 预览/)).toHaveClass("h-full", "max-h-full");
    pdf.unmount();

    const audio = renderPreview("native_audio");
    expect(audio.container.querySelector("audio[controls]")).toHaveAttribute("src", contentUrl);
    expect(audio.container.querySelector("audio[controls]")).toHaveAttribute("aria-label", asset().name);
    expect(screen.getByText(/Captions are unavailable|当前媒体没有可用字幕/)).toBeInTheDocument();
    audio.unmount();

    const video = renderPreview("native_video");
    expect(video.container.querySelector("video[controls]")).toHaveAttribute("src", contentUrl);
    expect(video.container.querySelector("video[controls]")).toHaveAttribute("aria-label", asset().name);
    expect(video.container.querySelector("video[controls]")).toHaveClass("max-h-full");
    expect(screen.getByText(/Captions are unavailable|当前媒体没有可用字幕/)).toBeInTheDocument();
    video.unmount();

    const hex = renderPreview("metadata_hex");
    expect(screen.getByTitle(/Metadata and hex preview|元数据与十六进制预览/)).toHaveAttribute("sandbox", "");
    expect(screen.getByTitle(/Metadata and hex preview|元数据与十六进制预览/)).toHaveClass("h-full", "max-h-full");
    hex.unmount();
  });

  it("removes first-run load and refresh while keeping permission-backed download", async () => {
    const user = userEvent.setup();
    const onLoadExactPreview = vi.fn();
    const onPrepareDownload = vi.fn();
    const previewAsset = asset();
    const { rerender } = render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        onLoadExactPreview={onLoadExactPreview}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={onPrepareDownload}
        onDetach={vi.fn()}
      />
    );

    expect(screen.queryByRole("button", { name: /Load preview|加载预览/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Refresh preview|刷新预览/ })).not.toBeInTheDocument();
    const prepareDownload = screen.getByRole("button", { name: /Prepare download|准备下载/ });
    expect(prepareDownload).toHaveClass("min-h-11", "touch-target");
    await user.click(prepareDownload);
    expect(onLoadExactPreview).not.toHaveBeenCalled();
    expect(onPrepareDownload).toHaveBeenCalledWith(previewAsset);

    rerender(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview={false}
        canDownload={false}
        onLoadExactPreview={onLoadExactPreview}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={onPrepareDownload}
        onDetach={vi.fn()}
      />
    );
    expect(screen.queryByRole("button", { name: /Load preview|加载预览/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Prepare download|准备下载/ })).not.toBeInTheDocument();
  });

  it("offers manual Retry only for the current retryable preview failure", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    const common = {
      asset: asset(),
      canPreview: true,
      canDownload: false,
      onLoadExactPreview: vi.fn(),
      onRetry,
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview
        {...common}
        resource={{
          status: "error",
          value: null,
          error: {
            code: "temporarily_unavailable",
            translationKey: "backupAssets.errors.temporarilyUnavailable",
            retryable: true,
            action: "retry",
          },
        }}
      />,
    );

    const retry = screen.getByRole("button", { name: /Retry preview|重试预览/ });
    expect(retry).toHaveClass("min-h-11", "touch-target");
		expect(screen.queryByText(/ZIP browsing|ZIP 浏览/)).not.toBeInTheDocument();
    await user.click(retry);
    expect(onRetry).toHaveBeenCalledTimes(1);

    rendered.rerender(
      <AssetPreview
        {...common}
        resource={{
          status: "blocked",
          value: null,
          error: {
            code: "preview_renderer_unsupported",
            translationKey: "backupAssets.errors.previewRendererUnsupported",
            retryable: false,
            action: "none",
          },
        }}
      />,
    );
    expect(screen.queryByRole("button", { name: /Retry preview|重试预览/ })).not.toBeInTheDocument();
    expect(screen.getByText(/cannot be previewed safely|无法安全预览/)).toBeInTheDocument();
		expect(screen.queryByText(/ZIP browsing|ZIP 浏览/)).not.toBeInTheDocument();
  });

  it("unmounts a native renderer before a replacement ticket can attach", () => {
    const rendered = renderPreview("native_video");
    const media = rendered.container.querySelector("video");
    expect(media).toHaveAttribute("src", contentUrl);

    rendered.rerender(
      <AssetPreview
        asset={asset({ ref: { recoveryPointId: "1".repeat(32), entryId: "2".repeat(64) } })}
        resource={{ status: "loading", value: null }}
        canPreview
        canDownload
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={vi.fn()}
        onPrepareDownload={vi.fn()}
        onDetach={vi.fn()}
      />,
    );

    expect(media).not.toHaveAttribute("src");
    expect(rendered.container.querySelector("video")).toBeNull();
  });

  it("renders an exact attachment ticket as a download without reading its URL", () => {
    renderPreview("attachment", {
      ticket: { classification: "secret" },
      asset: asset({ name: "evidence.bin" }),
    });

    const link = screen.getByRole("link", { name: /Download evidence.bin|下载 evidence.bin/ });
    expect(link).toHaveAttribute("href", contentUrl);
    expect(link).toHaveAttribute("download", "evidence.bin");
  });

  it("clears and reloads native media while detaching its ticket URL", () => {
    const load = HTMLMediaElement.prototype.load;
    const rendered = renderPreview("native_audio");
    const media = rendered.container.querySelector("audio");
    expect(media).toHaveAttribute("src", contentUrl);

    rendered.unmount();

    expect(media).not.toHaveAttribute("src");
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("renews near expiry, bounds media retry, and detaches the opaque URL on unmount", async () => {
    const onRenew = vi.fn();
    const onDetach = vi.fn();
    const now = Date.now();
    const rendered = renderPreview("safe_raster", {
      asset: asset({ name: "safe.png", mimeType: "image/png" }),
      ticket: { idleExpiresAt: new Date(now + 10_000).toISOString() },
      onRenew,
      onDetach,
    });
    const image = screen.getByRole("img", { name: "safe.png" });

    await waitFor(() => expect(onRenew).toHaveBeenCalledTimes(1));
    fireEvent.error(image);
    fireEvent.error(image);
    expect(onRenew).toHaveBeenCalledTimes(2);

    const renewedUrl = `/api/v1/asset-content/${"e".repeat(32)}`;
    rendered.rerender(
      <AssetPreview
        asset={asset({ name: "safe.png", mimeType: "image/png" })}
        resource={{
          status: "ready",
          value: ticket("safe_raster", { contentUrl: renewedUrl }),
        }}
        canPreview
        canDownload
        onLoadExactPreview={vi.fn()}
        onRetry={vi.fn()}
        onRenew={onRenew}
        onPrepareDownload={vi.fn()}
        onDetach={onDetach}
      />
    );
    fireEvent.error(screen.getByRole("img", { name: "safe.png" }));
    expect(onRenew).toHaveBeenCalledTimes(2);

    rendered.unmount();
    expect(image).not.toHaveAttribute("src");
    await waitFor(() => expect(onDetach).toHaveBeenCalledTimes(1));
  });
});
