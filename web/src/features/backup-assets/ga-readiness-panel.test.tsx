import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { GaReadinessPanel } from "./ga-readiness-panel";
import type { BackupGaReadiness } from "@/lib/api/backup-ga-api";

const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

function readiness(overrides: Partial<BackupGaReadiness> = {}): BackupGaReadiness {
  return {
    schemaVersion: 1,
    class: "existing",
    status: "ready",
    inventoryComplete: true,
    inventoryDigest: digest,
    acknowledgedDigest: "",
    exportRootValid: true,
    keyDomainsReady: true,
    workerOptional: true,
    counts: { candidates: 2, conflicts: 1, unsupported: 1, capabilityGaps: 0 },
    conflicts: [
      {
        kind: "command_unsupported",
        taskIds: [9],
        repositoryId: "a".repeat(32),
        stableReasonCode: "backup_assets.ga.command_unsupported",
      },
    ],
    ...overrides,
  };
}

function api(snapshot: BackupGaReadiness = readiness()) {
  return {
    getReadiness: vi.fn().mockResolvedValue(snapshot),
    runInventory: vi.fn().mockResolvedValue(snapshot),
    acknowledge: vi.fn().mockResolvedValue({
      ...snapshot,
      status: "acknowledged" as const,
      acknowledgedDigest: digest,
    }),
    enable: vi.fn().mockResolvedValue(undefined),
  };
}

describe("GaReadinessPanel", () => {
  it("shows inventory conflicts and enablement controls only for Admin", async () => {
    const user = userEvent.setup();
    const adminApi = api();
    // `role` is the auth role prop, not an ARIA role.
    // eslint-disable-next-line jsx-a11y/aria-role
    render(<GaReadinessPanel token="admin-token" role="admin" api={adminApi} />);

    expect(await screen.findByRole("region", { name: /Backup asset readiness|备份资产就绪/ })).toBeInTheDocument();
    expect(screen.getByText(/Existing installation|现有安装/)).toBeInTheDocument();
    expect(screen.getAllByText(/command_unsupported|不支持的命令/).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: /Run inventory|运行清单/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Acknowledge inventory|确认清单/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Enable backup assets|启用备份资产/ })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: /Acknowledge inventory|确认清单/ }));
    expect(adminApi.acknowledge).toHaveBeenCalledWith("admin-token", digest);
  });

  it("hides conflict payloads and enable controls from Operator and Viewer", async () => {
    const operatorApi = api();
    const { rerender } = render(
      // `role` is the auth role prop, not an ARIA role.
      // eslint-disable-next-line jsx-a11y/aria-role
      <GaReadinessPanel token="operator-token" role="operator" api={operatorApi} />,
    );
    expect(operatorApi.getReadiness).not.toHaveBeenCalled();
    expect(screen.queryByRole("region", { name: /Backup asset readiness|备份资产就绪/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/command_unsupported|不支持的命令/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Enable backup assets|启用备份资产/ })).not.toBeInTheDocument();

    const viewerApi = api();
    // `role` is the auth role prop, not an ARIA role.
    // eslint-disable-next-line jsx-a11y/aria-role
    rerender(<GaReadinessPanel token="viewer-token" role="viewer" api={viewerApi} />);
    expect(viewerApi.getReadiness).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Acknowledge inventory|确认清单/ })).not.toBeInTheDocument();
  });

  it("announces readiness status after a successful inventory run", async () => {
    const user = userEvent.setup();
    const adminApi = api();
    // `role` is the auth role prop, not an ARIA role.
    // eslint-disable-next-line jsx-a11y/aria-role
    render(<GaReadinessPanel token="admin-token" role="admin" api={adminApi} />);
    await screen.findByRole("region", { name: /Backup asset readiness|备份资产就绪/ });
    await user.click(screen.getByRole("button", { name: /Run inventory|运行清单/ }));
    expect(adminApi.runInventory).toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent(/Ready|就绪/);
  });
});
