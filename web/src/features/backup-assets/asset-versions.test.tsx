import "@testing-library/jest-dom/vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { recoveryPoint, buildAssetRows } from "./__tests__/test-utils";
import { AssetVersions } from "./asset-versions";

const listAssetVersionsMock = vi.fn();

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    listAssetVersions: (...args: unknown[]) => listAssetVersionsMock(...args),
  },
}));

describe("AssetVersions", () => {
  it("lists retained versions and opens an opaque deep link", async () => {
    const user = userEvent.setup();
    const asset = buildAssetRows(1)[0]?.asset;
    if (!asset) throw new Error("missing synthetic asset");
    const older = {
      ref: { recoveryPointId: "c".repeat(32), entryId: "d".repeat(64) },
      capturedAt: "2026-07-18T00:00:00Z",
      size: 10,
      entryType: "file" as const,
    };
    listAssetVersionsMock.mockResolvedValue({
      status: "available",
      value: {
        items: [
          { ref: asset.ref, capturedAt: recoveryPoint.capturedAt, size: asset.size, entryType: asset.entryType },
          older,
        ],
      },
    });
    const onOpenVersion = vi.fn();
    render(
      <AssetVersions
        token="test-token"
        asset={asset}
        recoveryPoint={recoveryPoint}
        onOpenVersion={onOpenVersion}
      />
    );

    expect(screen.getByText(recoveryPoint.producingTaskName)).toBeInTheDocument();
    expect(await screen.findByRole("list", { name: /Retained versions|保留版本/ })).toBeInTheDocument();
    expect(screen.queryByText(/not deployed|未部署/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: new RegExp(older.ref.recoveryPointId) }));
    expect(onOpenVersion).toHaveBeenCalledWith(older.ref);
    await waitFor(() => expect(listAssetVersionsMock).toHaveBeenCalledWith("test-token", asset.ref, expect.any(AbortSignal)));
  });
});
