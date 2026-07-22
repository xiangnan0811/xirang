import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";
import type { BackupAsset, BackupProcessingProduct } from "@/types/domain";

import { buildAssetRows } from "./__tests__/test-utils";
import { BackupAssetProcessingPanel } from "./backup-asset-processing-panel";

const { cancelMock, requestMock, useProcessingMock } = vi.hoisted(() => ({
  cancelMock: vi.fn(),
  requestMock: vi.fn(),
  useProcessingMock: vi.fn(),
}));

vi.mock("./use-backup-asset-processing", () => ({
  useBackupAssetProcessing: useProcessingMock,
}));

function asset(overrides: Partial<BackupAsset> = {}): BackupAsset {
  return { ...buildAssetRows(1)[0].asset, mimeType: "image/png", ...overrides };
}

function product(overrides: Partial<BackupProcessingProduct> = {}): BackupProcessingProduct {
  return {
    schemaVersion: 1,
    jobId: null,
    state: "not_deployed",
    representation: "thumbnail",
    capability: "image.thumbnail",
    profile: "raster_thumbnail_v1",
    coverage: null,
    freshness: "current",
    scanStatus: "not_scanned",
    sensitivityStatus: "unknown",
    reason: "worker_not_deployed",
    retryable: false,
    fallbackActions: ["native_preview", "download"],
    pollAfterSeconds: 0,
    terminal: true,
    ...overrides,
  };
}

function setHookState(current: BackupProcessingProduct, status: "ready" | "error" = "ready") {
  useProcessingMock.mockReturnValue({
    state: {
      revision: 1,
      status,
      products: [current],
      active: current.jobId ? current : null,
      error: status === "error" ? new Error("/private/provider/path") : null,
    },
    request: requestMock,
    cancel: cancelMock,
  });
}

function renderPanel(overrides: Partial<React.ComponentProps<typeof BackupAssetProcessingPanel>> = {}) {
  const props: React.ComponentProps<typeof BackupAssetProcessingPanel> = {
    token: "token",
    asset: asset(),
    canNativePreview: true,
    canDownload: true,
    onOpenPreview: vi.fn(),
    onPrepareDownload: vi.fn(),
    ...overrides,
  };
  return { ...render(<BackupAssetProcessingPanel {...props} />), props };
}

describe("BackupAssetProcessingPanel", () => {
  beforeEach(() => {
    cancelMock.mockReset();
    requestMock.mockReset();
    useProcessingMock.mockReset();
    setHookState(product());
  });

  it("announces queued work politely and cancels the active public interest", async () => {
    const user = userEvent.setup();
    setHookState(product({ jobId: "5".repeat(32), state: "queued", terminal: false, pollAfterSeconds: 2 }));
    renderPanel();

    expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent(/Queued|排队/);
    await user.click(screen.getByRole("button", { name: /Cancel processing|取消处理/ }));
    expect(cancelMock).toHaveBeenCalledTimes(1);
  });

  it("uses only server-allowed fallback commands and retries the closed representation", async () => {
    const user = userEvent.setup();
    const current = product({ state: "failed", retryable: true, fallbackActions: ["download"] });
    setHookState(current);
    const onOpenPreview = vi.fn();
    const onPrepareDownload = vi.fn();
    renderPanel({ onOpenPreview, onPrepareDownload });

    expect(screen.queryByRole("button", { name: /Native preview|原生预览/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Download|下载/ }));
    expect(onPrepareDownload).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: /Retry processing|重试处理/ }));
    expect(requestMock).toHaveBeenCalledWith("thumbnail");
    expect(onOpenPreview).not.toHaveBeenCalled();
  });

  it("starts a closed enhanced-preview request when the Worker is ready", async () => {
    const user = userEvent.setup();
    setHookState(product({ state: "native", reason: null }));
    renderPanel();

    await user.click(screen.getByRole("button", { name: /Generate enhanced preview|生成增强预览/ }));
    expect(requestMock).toHaveBeenCalledWith("thumbnail");
  });

  it("keeps not-scanned distinct from no-finding in an axe-clean derived preview", async () => {
    const user = userEvent.setup();
    setHookState(product({ state: "derived", scanStatus: "not_scanned", reason: null }));
    const onOpenPreview = vi.fn();
    const { container } = renderPanel({ onOpenPreview });

    expect(screen.getByText(/Not scanned|未扫描/)).toBeInTheDocument();
    expect(screen.queryByText(/No finding|未发现/)).not.toBeInTheDocument();
    expect(await runAxe(container)).toHaveNoViolations();
    await user.click(screen.getByRole("button", { name: /Open enhanced preview|打开增强预览/ }));
    expect(onOpenPreview).toHaveBeenCalledWith("derived");
  });

  it("keeps the native fallback distinct from the derived preview", async () => {
    const user = userEvent.setup();
    setHookState(product({ state: "derived", reason: null, fallbackActions: ["native_preview"] }));
    const onOpenPreview = vi.fn();
    renderPanel({ onOpenPreview });

    await user.click(screen.getByRole("button", { name: /Native preview|原生预览/ }));
    expect(onOpenPreview).toHaveBeenCalledWith("native");
  });

  it("renders a safe generic error without exposing raw request detail", () => {
    setHookState(product(), "error");
    renderPanel();

    expect(screen.getByRole("alert")).toHaveTextContent(/Processing status is unavailable|处理状态不可用/);
    expect(screen.getByRole("alert")).not.toHaveTextContent("/private/provider/path");
  });
});
