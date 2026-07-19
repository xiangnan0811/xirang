import "@testing-library/jest-dom/vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BackupAsset, BackupContentRenderer, BackupContentTicket } from "@/types/domain";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetPreview } from "./asset-preview";
import { selectBackupAssetPreviewProduct } from "./asset-preview-model";

const contentUrl = `/api/v1/asset-content/${"d".repeat(32)}`;

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
      onLoadPreview={vi.fn()}
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
    expect(selectBackupAssetPreviewProduct(asset({ mimeType }))).toEqual({ renderer, profile });
  });

  it("forces non-file entries to metadata/hex", () => {
    expect(selectBackupAssetPreviewProduct(asset({ entryType: "directory", mimeType: "image/png" }))).toEqual({
      renderer: "metadata_hex",
      profile: "hex_v1",
    });
  });
});

describe("AssetPreview", () => {
  beforeEach(() => {
    vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("keeps one bounded viewport height before and after content loads", () => {
    const previewAsset = asset();
    const props = {
      asset: previewAsset,
      canPreview: true,
      canDownload: true,
      onLoadPreview: vi.fn(),
      onRenew: vi.fn(),
      onPrepareDownload: vi.fn(),
      onDetach: vi.fn(),
    };
    const rendered = render(
      <AssetPreview {...props} resource={{ status: "idle", value: null }} />
    );

    expect(screen.getByTestId("asset-preview-viewport")).toHaveClass(
      "h-[18rem]",
      "min-h-[18rem]",
      "max-h-[18rem]",
      "shrink-0"
    );

    rendered.rerender(
      <AssetPreview
        {...props}
        resource={{ status: "ready", value: ticket("escaped_text") }}
      />
    );
    expect(screen.getByTitle(/Asset preview|资产预览/)).toHaveClass("h-full", "max-h-full");
  });

  it("renders escaped content in a sandboxed opaque frame without active markup", () => {
    renderPreview("escaped_text", { asset: asset({ name: "unsafe.html", mimeType: "text/html" }) });

    const frame = screen.getByTitle(/Asset preview|资产预览/);
    expect(frame).toHaveAttribute("src", contentUrl);
    expect(frame).toHaveAttribute("sandbox", "");
    expect(frame).not.toHaveAttribute("srcdoc");
    const viewport = screen.getByTestId("asset-preview-viewport");
    expect(viewport.querySelector("script, svg")).not.toBeInTheDocument();
    expect(viewport).toHaveClass("min-h-[18rem]");
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

  it("shows only permission-backed load and download commands", async () => {
    const user = userEvent.setup();
    const onLoadPreview = vi.fn();
    const onPrepareDownload = vi.fn();
    const previewAsset = asset();
    const { rerender } = render(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview
        canDownload
        onLoadPreview={onLoadPreview}
        onRenew={vi.fn()}
        onPrepareDownload={onPrepareDownload}
        onDetach={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Load preview|加载预览/ }));
    await user.click(screen.getByRole("button", { name: /Prepare download|准备下载/ }));
    expect(onLoadPreview).toHaveBeenCalledWith(previewAsset);
    expect(onPrepareDownload).toHaveBeenCalledWith(previewAsset);

    rerender(
      <AssetPreview
        asset={previewAsset}
        resource={{ status: "idle", value: null }}
        canPreview={false}
        canDownload={false}
        onLoadPreview={onLoadPreview}
        onRenew={vi.fn()}
        onPrepareDownload={onPrepareDownload}
        onDetach={vi.fn()}
      />
    );
    expect(screen.queryByRole("button", { name: /Load preview|加载预览/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Prepare download|准备下载/ })).not.toBeInTheDocument();
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
        onLoadPreview={vi.fn()}
        onRenew={onRenew}
        onPrepareDownload={vi.fn()}
        onDetach={onDetach}
      />
    );
    fireEvent.error(screen.getByRole("img", { name: "safe.png" }));
    expect(onRenew).toHaveBeenCalledTimes(2);

    rendered.unmount();
    expect(image).not.toHaveAttribute("src");
    expect(onDetach).toHaveBeenCalledTimes(1);
  });
});
