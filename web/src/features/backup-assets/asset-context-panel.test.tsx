import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { buildAssetRows, recoveryPoint, repository } from "./__tests__/test-utils";
import { defaultBackupAssetsRouteState } from "./backup-assets-route-state";
import { AssetContextPanel } from "./asset-context-panel";

const route = {
  ...defaultBackupAssetsRouteState("data"),
  repositoryId: repository.id,
  recoveryPointId: recoveryPoint.id,
};

describe("AssetContextPanel", () => {
  it("renders an opaque directory tree and navigates by entry ID", async () => {
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    const directoryRows = buildAssetRows(12);
    const firstDirectory = directoryRows.find((row) => row.asset.entryType === "directory");
    expect(firstDirectory).toBeDefined();

    render(
      <AssetContextPanel
        route={route}
        repositories={{ status: "ready", items: [{ status: "available", value: repository }], nextCursor: null }}
        recoveryPoints={{ status: "ready", items: [{ status: "available", value: recoveryPoint }], nextCursor: null }}
        selectedRepository={repository}
        selectedRecoveryPoint={recoveryPoint}
        directoryRows={directoryRows}
        overlayCounts={{ savedSearches: 0, favorites: 0, tags: 0, recent: 0 }}
        onRoutePatch={onRoutePatch}
        onOverlaySectionChange={vi.fn()}
      />
    );

    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getByText(/Root directory|根目录/)).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", {
        name: new RegExp(firstDirectory?.asset.name ?? "missing-directory"),
      })
    );

    expect(onRoutePatch).toHaveBeenCalledWith({
      parentEntryId: firstDirectory?.ref.entryId,
      entryId: undefined,
    });
    expect(JSON.stringify(onRoutePatch.mock.calls)).not.toContain(firstDirectory?.asset.name);
  });

  it("renders repository, recovery point, catalog, content, and overlay facts independently", () => {
    render(
      <AssetContextPanel
        route={route}
        repositories={{ status: "ready", items: [{ status: "available", value: repository }], nextCursor: null }}
        recoveryPoints={{ status: "ready", items: [{ status: "available", value: recoveryPoint }], nextCursor: null }}
        selectedRepository={repository}
        selectedRecoveryPoint={recoveryPoint}
        directoryRows={[]}
        overlayCounts={{ savedSearches: 2, favorites: 4, tags: 3, recent: 5 }}
        onRoutePatch={vi.fn()}
        onOverlaySectionChange={vi.fn()}
      />
    );

    expect(screen.getAllByText(repository.displayName)).toHaveLength(2);
    expect(screen.getByText(/Restic/)).toBeInTheDocument();
    expect(screen.getAllByText(/Online|在线/)).toHaveLength(2);
    expect(screen.getByText(/Native snapshot|原生快照/)).toBeInTheDocument();
    expect(screen.getByText(/Backend versioned|后端版本化/)).toBeInTheDocument();
    expect(screen.getByText(/Complete|完整/)).toBeInTheDocument();
    expect(screen.getByText(/Content available|内容可用/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Saved searches.*2|保存搜索.*2/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Favorites.*4|收藏.*4/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Tags.*3|标签.*3/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Recent.*5|最近.*5/ })).toBeInTheDocument();
  });

  it("shows a localized content capability reason without treating offline as corruption", () => {
    if (recoveryPoint.catalog.status !== "available") {
      throw new Error("synthetic recovery point must expose Catalog status");
    }
    const offlinePoint = {
      ...recoveryPoint,
      physicalAvailability: "offline" as const,
      catalog: {
        status: "available" as const,
        value: {
          ...recoveryPoint.catalog.value,
          contentAvailability: {
            available: false,
            reason: {
              code: "repository_offline" as const,
              params: { detail: "raw /private/provider/path" },
            },
          },
        },
      },
    };

    render(
      <AssetContextPanel
        route={route}
        repositories={{ status: "ready", items: [{ status: "available", value: repository }], nextCursor: null }}
        recoveryPoints={{ status: "ready", items: [{ status: "available", value: offlinePoint }], nextCursor: null }}
        selectedRepository={repository}
        selectedRecoveryPoint={offlinePoint}
        directoryRows={[]}
        overlayCounts={{ savedSearches: 0, favorites: 0, tags: 0, recent: 0 }}
        onRoutePatch={vi.fn()}
        onOverlaySectionChange={vi.fn()}
      />
    );

    expect(screen.getByText(/Content unavailable|内容不可用/)).toBeInTheDocument();
    expect(screen.getByText(/^Repository offline$|^仓库离线$/)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/private\/provider|corrupt|损坏/i);
  });

  it("emits opaque route patches for repository and recovery-point selection", async () => {
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    render(
      <AssetContextPanel
        route={route}
        repositories={{ status: "ready", items: [{ status: "available", value: repository }], nextCursor: null }}
        recoveryPoints={{ status: "ready", items: [{ status: "available", value: recoveryPoint }], nextCursor: null }}
        selectedRepository={repository}
        selectedRecoveryPoint={recoveryPoint}
        directoryRows={[]}
        overlayCounts={{ savedSearches: 0, favorites: 0, tags: 0, recent: 0 }}
        onRoutePatch={onRoutePatch}
        onOverlaySectionChange={vi.fn()}
      />
    );

    await user.selectOptions(screen.getByRole("combobox", { name: /Repository|仓库/ }), repository.id);
    await user.selectOptions(screen.getByRole("combobox", { name: /Recovery point|恢复点/ }), recoveryPoint.id);

    expect(onRoutePatch).toHaveBeenCalledWith({ repositoryId: repository.id });
    expect(onRoutePatch).toHaveBeenCalledWith({ recoveryPointId: recoveryPoint.id });
    expect(JSON.stringify(onRoutePatch.mock.calls)).not.toContain(repository.displayName);
  });

  it("fails closed without rendering repository facts when the resource is blocked", () => {
    render(
      <AssetContextPanel
        route={defaultBackupAssetsRouteState("data")}
        repositories={{
          status: "blocked",
          items: [],
          nextCursor: null,
          error: {
            code: "permission_denied",
            translationKey: "backupAssets.errors.permissionDenied",
            retryable: false,
            action: "none",
          },
        }}
        recoveryPoints={{ status: "idle", items: [], nextCursor: null }}
        selectedRepository={null}
        selectedRecoveryPoint={null}
        directoryRows={[]}
        overlayCounts={{ savedSearches: 0, favorites: 0, tags: 0, recent: 0 }}
        onRoutePatch={vi.fn()}
        onOverlaySectionChange={vi.fn()}
      />
    );

    expect(screen.getByRole("alert")).toHaveTextContent(/Permission|权限/);
    expect(screen.queryByText(repository.displayName)).not.toBeInTheDocument();
  });
});
