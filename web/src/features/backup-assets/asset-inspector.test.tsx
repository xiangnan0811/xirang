import "@testing-library/jest-dom/vitest";
import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { buildAssetRows, recoveryPoint } from "./__tests__/test-utils";
import { AssetInspector } from "./asset-inspector";
import type { BackupAssetsInspectorTab } from "./backup-assets-route-state";

describe("AssetInspector", () => {
  it("focuses the asset heading when the inspector opens and the exact asset changes", async () => {
    const rows = buildAssetRows(2);
    const inspector = (asset: (typeof rows)[number]["asset"]) => (
      <AssetInspector
        asset={asset}
        recoveryPoint={recoveryPoint}
        activeTab="preview"
        preview={null}
        evidence={null}
        diff={null}
        onTabChange={vi.fn()}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        hasPrevious
        hasNext
        onClose={vi.fn()}
      />
    );
    const { rerender } = render(inspector(rows[0].asset));

    await waitFor(() => expect(screen.getByRole("heading", { name: rows[0].asset.name })).toHaveFocus());

    rerender(inspector(rows[1].asset));
    await waitFor(() => expect(screen.getByRole("heading", { name: rows[1].asset.name })).toHaveFocus());
  });

  it("preserves browser focus when heading focus is disabled for a split desktop layout", async () => {
    const rows = buildAssetRows(2);
    const inspector = (asset: (typeof rows)[number]["asset"]) => (
      <>
        <button type="button">Origin row</button>
        <AssetInspector
          asset={asset}
          recoveryPoint={recoveryPoint}
          activeTab="preview"
          preview={null}
          evidence={null}
          diff={null}
          focusTitle={false}
          onTabChange={vi.fn()}
          onPrevious={vi.fn()}
          onNext={vi.fn()}
          hasPrevious={false}
          hasNext={false}
          onClose={vi.fn()}
        />
      </>
    );
    const { rerender } = render(inspector(rows[0].asset));
    const origin = screen.getByRole("button", { name: "Origin row" });
    origin.focus();

    rerender(inspector(rows[1].asset));

    await waitFor(() => expect(origin).toHaveFocus());
  });

  it("uses route-owned tabs and supports tab keyboard movement", async () => {
    const user = userEvent.setup();
    const onTabChange = vi.fn();
    const asset = buildAssetRows(1)[0].asset;
    function InspectorHarness() {
      const [activeTab, setActiveTab] = useState<BackupAssetsInspectorTab>("preview");
      return (
        <AssetInspector
          asset={asset}
          recoveryPoint={recoveryPoint}
          activeTab={activeTab}
          preview={<div>preview viewport</div>}
          evidence={<div>evidence panel</div>}
          diff={<div>diff panel</div>}
          onTabChange={(tab) => {
            onTabChange(tab);
            setActiveTab(tab);
          }}
          onPrevious={vi.fn()}
          onNext={vi.fn()}
          hasPrevious={false}
          hasNext
          onClose={vi.fn()}
        />
      );
    }
    render(<InspectorHarness />);

    const previewTab = screen.getByRole("tab", { name: /Preview|预览/ });
    previewTab.focus();
    await user.keyboard("{ArrowRight}");
    expect(onTabChange).toHaveBeenCalledWith("metadata");
    const metadataTab = screen.getByRole("tab", { name: /Metadata|元数据/ });
    await waitFor(() => expect(metadataTab).toHaveFocus());
    expect(metadataTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: /Metadata|元数据/ })).toHaveTextContent(asset.mimeType);
  });

  it("renders metadata and stable previous/next controls", async () => {
    const user = userEvent.setup();
    const onNext = vi.fn();
    const asset = buildAssetRows(1)[0].asset;
    render(
      <AssetInspector
        asset={asset}
        recoveryPoint={recoveryPoint}
        activeTab="metadata"
        preview={null}
        evidence={null}
        diff={null}
        onTabChange={vi.fn()}
        onPrevious={vi.fn()}
        onNext={onNext}
        hasPrevious={false}
        hasNext
        onClose={vi.fn()}
      />
    );

    expect(screen.getByRole("tabpanel", { name: /Metadata|元数据/ })).toHaveTextContent(asset.mimeType);
    expect(screen.getByRole("button", { name: /Previous file|上一个文件/ })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /Next file|下一个文件/ }));
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("keeps the mobile inspector navigation and tabs at 44px touch targets", () => {
    render(
      <AssetInspector
        asset={buildAssetRows(1)[0].asset}
        recoveryPoint={recoveryPoint}
        activeTab="preview"
        preview={null}
        evidence={null}
        diff={null}
        onTabChange={vi.fn()}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        hasPrevious={false}
        hasNext={false}
        onClose={vi.fn()}
      />,
    );

    const headerControls = [
      screen.getByRole("button", { name: /Previous file|上一个文件/ }),
      screen.getByRole("button", { name: /Next file|下一个文件/ }),
      screen.getByRole("button", { name: /Close file inspector|关闭文件检查器/ }),
    ];
    expect(headerControls.every((control) => control.classList.contains("size-11"))).toBe(true);
    expect(headerControls.every((control) => control.classList.contains("touch-target"))).toBe(true);
    expect(screen.getByRole("tablist")).toHaveClass("min-h-11", "lg:min-h-10");
    expect(screen.getAllByRole("tab").every((tab) => tab.classList.contains("min-h-11"))).toBe(true);
    expect(screen.getAllByRole("tab").every((tab) => tab.classList.contains("touch-target"))).toBe(true);
  });

  it("exposes a favorite toggle only after permission and complete membership are known", async () => {
    const user = userEvent.setup();
    const onToggleFavorite = vi.fn();
    const asset = buildAssetRows(1)[0].asset;
    const inspector = (favoriteState: "active" | null | undefined, canManageFavorite = true) => (
      <AssetInspector
        asset={asset}
        recoveryPoint={recoveryPoint}
        activeTab="preview"
        preview={null}
        evidence={null}
        diff={null}
        canManageFavorite={canManageFavorite}
        favoriteState={favoriteState}
        onToggleFavorite={onToggleFavorite}
        onTabChange={vi.fn()}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        hasPrevious={false}
        hasNext={false}
        onClose={vi.fn()}
      />
    );
    const rendered = render(inspector(undefined));

    expect(screen.queryByRole("button", { name: /Add favorite|添加收藏/ })).not.toBeInTheDocument();

    rendered.rerender(inspector(null));

    await user.click(screen.getByRole("button", { name: /Add favorite|添加收藏/ }));
    expect(onToggleFavorite).toHaveBeenCalledTimes(1);

    rendered.rerender(inspector("active"));
    expect(screen.getByRole("button", { name: /Remove favorite|移除收藏/ })).toBeInTheDocument();

    rendered.rerender(inspector("active", false));
    expect(screen.queryByRole("button", { name: /Remove favorite|移除收藏/ })).not.toBeInTheDocument();
  });

  it("reuses the workspace-owned recovery handoff for one inspected item", async () => {
    const user = userEvent.setup();
    const onRecover = vi.fn();
    render(
      <AssetInspector
        asset={buildAssetRows(1)[0].asset}
        recoveryPoint={recoveryPoint}
        activeTab="preview"
        preview={null}
        evidence={null}
        diff={null}
        canRecover
        onRecover={onRecover}
        onTabChange={vi.fn()}
        onPrevious={vi.fn()}
        onNext={vi.fn()}
        hasPrevious={false}
        hasNext={false}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Recover this item|恢复此项/ }));
    expect(onRecover).toHaveBeenCalledOnce();
  });
});
