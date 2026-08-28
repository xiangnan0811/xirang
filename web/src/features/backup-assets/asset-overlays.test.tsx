import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type {
  BackupAssetFavorite,
  BackupAssetRecentAccess,
  BackupAssetTag,
  SavedAssetSearch,
} from "@/types/domain";
import { buildAssetRows } from "./__tests__/test-utils";
import { AssetOverlays } from "./asset-overlays";

const query: SavedAssetSearch["query"] = {
  schemaVersion: 1,
  root: { op: "term", field: "any", text: "synthetic" },
  scope: { mode: "all_retained", repositoryIds: ["a".repeat(32)], taskIds: [], recoveryPointIds: [] },
  sort: "relevance",
  limit: 100,
  cursor: null,
};
const saved: SavedAssetSearch = {
  id: "1".repeat(32),
  query,
  version: 1,
  state: "active",
  stateReason: null,
  brokenAt: null,
  createdAt: "2026-07-19T00:00:00Z",
  updatedAt: "2026-07-19T00:00:00Z",
};
const broken: SavedAssetSearch = {
  ...saved,
  id: "2".repeat(32),
  state: "broken",
  stateReason: "point_expired",
  brokenAt: "2026-07-19T01:00:00Z",
};

describe("AssetOverlays", () => {
  it("executes active saved searches, blocks broken ones, and does not invent rename", async () => {
    const user = userEvent.setup();
    const onCreateSaved = vi.fn();
    const onUpdateSaved = vi.fn();
    const onExecuteSaved = vi.fn();
    const onDeleteSaved = vi.fn();
    render(
      <AssetOverlays
        {...baseProps()}
        section="saved"
        savedSearches={{ status: "ready", items: [saved, broken], nextCursor: null }}
        onCreateSaved={onCreateSaved}
        onUpdateSaved={onUpdateSaved}
        onExecuteSaved={onExecuteSaved}
        onDeleteSaved={onDeleteSaved}
      />
    );

    expect(screen.getByRole("dialog", { name: /Saved searches|保存搜索/ })).toBeInTheDocument();
    const executeButtons = screen.getAllByRole("button", { name: /Run saved search|执行保存搜索/ });
    expect(executeButtons[0]).toBeEnabled();
    expect(executeButtons[1]).toBeDisabled();
    expect(screen.getAllByText("2026-07-19 00:00")).toHaveLength(2);
    expect(screen.getByText(/Recovery point expired|恢复点已过期/)).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: /^(Save current search|保存当前搜索)$/ })
    );
    expect(onCreateSaved).toHaveBeenCalledTimes(1);
    await user.click(executeButtons[0]);
    expect(onExecuteSaved).toHaveBeenCalledWith(saved.id);
    await user.click(
      screen.getAllByRole("button", { name: /^(Update saved search|更新保存搜索)$/ })[0]
    );
    expect(onUpdateSaved).toHaveBeenCalledWith(saved);
    expect(screen.queryByRole("button", { name: /Rename|重命名/ })).not.toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: /Delete saved search|删除保存搜索/ })[0]);
    expect(onDeleteSaved).toHaveBeenCalledWith(saved);
  });

  it("keeps favorite tombstones opaque, non-openable, and removable", async () => {
    const user = userEvent.setup();
    const onToggleFavorite = vi.fn();
    const favorite: BackupAssetFavorite = {
      id: "5".repeat(32),
      ref: buildAssetRows(1)[0].ref,
      label: "private-payroll.csv",
      state: "tombstone",
      tombstoneReason: "source_expired",
      version: 2,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T01:00:00Z",
    };
    render(
      <AssetOverlays
        {...baseProps()}
        section="favorites"
        favorites={{ status: "ready", items: [favorite], nextCursor: null }}
        onToggleFavorite={onToggleFavorite}
      />
    );

    expect(screen.queryByText(favorite.label)).not.toBeInTheDocument();
    expect(screen.getByText(/Source recovery point expired|源恢复点已过期/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Open file or directory|打开文件或目录/ })).not.toBeInTheDocument();
    const remove = screen.getByRole("button", { name: /Remove favorite|移除收藏/ });
    expect(remove).toBeEnabled();
    await user.click(remove);
    expect(onToggleFavorite).toHaveBeenCalledWith(favorite.ref, favorite.label);
  });

  it("creates tags and exposes one-way assignment without a fabricated checked state", async () => {
    const user = userEvent.setup();
    const tag: BackupAssetTag = {
      id: "3".repeat(32),
      name: "investigate",
      version: 1,
      createdAt: "2026-07-19T00:00:00Z",
      updatedAt: "2026-07-19T00:00:00Z",
    };
    const onCreateTag = vi.fn();
    const onUpdateTag = vi.fn();
    const onDeleteTag = vi.fn();
    const onAssignTag = vi.fn();
    const selectedRef = buildAssetRows(1)[0].ref;
    render(
      <AssetOverlays
        {...baseProps()}
        section="tags"
        tags={{ status: "ready", items: [tag], nextCursor: null }}
        selectedRef={selectedRef}
        onCreateTag={onCreateTag}
        onUpdateTag={onUpdateTag}
        onDeleteTag={onDeleteTag}
        onAssignTag={onAssignTag}
      />
    );

    await user.type(screen.getByRole("textbox", { name: /Tag name|标签名称/ }), "review");
    await user.click(screen.getByRole("button", { name: /Create tag|创建标签/ }));
    expect(onCreateTag).toHaveBeenCalledWith("review");
    const assign = screen.getByRole("button", { name: /Assign investigate|分配 investigate/ });
    expect(assign).not.toHaveAttribute("aria-pressed");
    await user.click(assign);
    expect(onAssignTag).toHaveBeenCalledWith(tag.id, selectedRef);

    await user.click(screen.getByRole("button", { name: /Edit investigate|编辑 investigate/ }));
    const input = screen.getByRole("textbox", { name: /Tag name|标签名称/ });
    await user.clear(input);
    await user.type(input, "reviewed");
    await user.click(screen.getByRole("button", { name: /Save tag|保存标签/ }));
    expect(onUpdateTag).toHaveBeenCalledWith(tag, "reviewed");

    await user.click(screen.getByRole("button", { name: /Delete investigate|删除 investigate/ }));
    expect(onDeleteTag).toHaveBeenCalledWith(tag);
  });

  it("clears recent access through an explicit command", async () => {
    const user = userEvent.setup();
    const ref = buildAssetRows(1)[0].ref;
    const recent: BackupAssetRecentAccess = {
      id: "4".repeat(32),
      ref,
      accessCount: 2,
      lastAccessedAt: "2026-07-19T00:00:00Z",
      expiresAt: "2026-08-19T00:00:00Z",
      version: 1,
    };
    const onClearRecent = vi.fn();
    render(
      <AssetOverlays
        {...baseProps()}
        section="recent"
        recent={{ status: "ready", items: [recent], nextCursor: null }}
        onClearRecent={onClearRecent}
      />
    );

    await user.click(screen.getByRole("button", { name: /Clear recent|清除最近/ }));
    expect(onClearRecent).toHaveBeenCalledTimes(1);
  });
});

function baseProps() {
  return {
    section: null as "saved" | "favorites" | "tags" | "recent" | null,
    savedSearches: { status: "idle" as const, items: [], nextCursor: null },
    favorites: { status: "idle" as const, items: [] as BackupAssetFavorite[], nextCursor: null },
    tags: { status: "idle" as const, items: [] as BackupAssetTag[], nextCursor: null },
    recent: { status: "idle" as const, items: [] as BackupAssetRecentAccess[], nextCursor: null },
    pending: false,
    error: undefined,
    canSaveCurrent: true,
    selectedRef: null,
    onClose: vi.fn(),
    onCreateSaved: vi.fn(),
    onUpdateSaved: vi.fn(),
    onDeleteSaved: vi.fn(),
    onExecuteSaved: vi.fn(),
    onToggleFavorite: vi.fn(),
    onCreateTag: vi.fn(),
    onUpdateTag: vi.fn(),
    onDeleteTag: vi.fn(),
    onAssignTag: vi.fn(),
    onClearRecent: vi.fn(),
    onOpenRef: vi.fn(),
  };
}
