import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import { GaReadinessPanel } from "./ga-readiness-panel";
import type { BackupGaApi, BackupGaReadiness } from "@/lib/api/backup-ga-api";

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

function renderAdminPanel(adminApi: Partial<BackupGaApi>) {
  return render(
    <MemoryRouter initialEntries={["/app/backups/overview"]}>
      <Routes>
        <Route
          path="/app/backups/overview"
          // `role` is the auth role prop, not an ARIA role.
          // eslint-disable-next-line jsx-a11y/aria-role
          element={<GaReadinessPanel token="admin-token" role="admin" api={adminApi} />}
        />
        <Route path="/app/tasks" element={<p>tasks route</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("GaReadinessPanel", () => {
  afterEach(() => {
    window.history.replaceState(null, "", "/");
  });

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
    expect(await screen.findByText(/If inventory results change|若清单结果发生变化/)).toBeInTheDocument();
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

  it("explains unconnected tasks with safe task IDs and links to Tasks", async () => {
    const user = userEvent.setup();
    const gapRepository = "b".repeat(32);
    const { container } = renderAdminPanel(api(readiness({
      status: "blocked",
      counts: { candidates: 2, conflicts: 2, unsupported: 1, capabilityGaps: 2 },
      conflicts: [
        {
          kind: "command_unsupported",
          taskIds: [9],
          repositoryId: "a".repeat(32),
          stableReasonCode: "backup_assets.ga.command_unsupported",
        },
        {
          kind: "capability_gap",
          taskIds: [12, 4],
          repositoryId: gapRepository,
          stableReasonCode: "backup_assets.ga.capability_gap",
        },
      ],
    })));

    for (const key of ["unconnectedTitle", "unconnectedDescription", "openTasks"]) {
      expect(i18n.exists(`backupAssets.ga.${key}`)).toBe(true);
    }
    const heading = await screen.findByRole("heading", { level: 3, name: i18n.t("backupAssets.ga.unconnectedTitle") });
    const section = heading.closest("section");
    expect(section).not.toBeNull();
    const gap = within(section!);
    expect(gap.getByText(i18n.t("backupAssets.ga.unconnectedDescription"))).toBeInTheDocument();
    expect(gap.getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      i18n.t("tasks.taskIdLabel", { id: 4 }),
      i18n.t("tasks.taskIdLabel", { id: 12 }),
    ]);

    expect(screen.getByRole("list", { name: i18n.t("backupAssets.ga.conflicts") })).toHaveTextContent(
      i18n.t("backupAssets.ga.conflictKind.command_unsupported"),
    );
    expect(container).not.toHaveTextContent(/capability_gap|command_unsupported/);
    expect(container).not.toHaveTextContent(gapRepository);
    expect(container).not.toHaveTextContent(i18n.t("backupAssets.ga.noConflicts"));

    const link = gap.getByRole("link", { name: i18n.t("backupAssets.ga.openTasks") });
    expect(link).toHaveAttribute("href", "/app/tasks");
    await user.click(link);
    expect(await screen.findByText("tasks route")).toBeInTheDocument();
  });

  it("focuses the readiness heading for the static overview hash target", async () => {
    window.history.replaceState(null, "", "/app/backups/overview#backup-assets-ga");
    renderAdminPanel(api());

    const heading = await screen.findByRole("heading", { level: 2, name: /Backup asset readiness|备份资产就绪/ });
    await waitFor(() => expect(heading).toHaveFocus());
    expect(heading.closest("#backup-assets-ga")).toHaveAttribute("role", "region");
  });
});
