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
    expect(screen.getByRole("button", { name: /Previous asset|上一个资产/ })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /Next asset|下一个资产/ }));
    expect(onNext).toHaveBeenCalledTimes(1);
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
});
