import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";

import { GaReadinessPanel } from "./ga-readiness-panel";
import type { BackupGaReadiness } from "@/lib/api/backup-ga-api";

const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

const snapshot: BackupGaReadiness = {
  schemaVersion: 1,
  class: "fresh",
  status: "ready",
  inventoryComplete: true,
  inventoryDigest: digest,
  acknowledgedDigest: "",
  exportRootValid: true,
  keyDomainsReady: true,
  workerOptional: true,
  counts: { candidates: 0, conflicts: 0, unsupported: 0, capabilityGaps: 0 },
  conflicts: [],
};

describe("GaReadinessPanel a11y", () => {
  it("keeps the Admin readiness panel axe-clean and keyboard reachable", async () => {
    const user = userEvent.setup();
    const api = {
      getReadiness: vi.fn().mockResolvedValue(snapshot),
      runInventory: vi.fn().mockResolvedValue(snapshot),
      acknowledge: vi.fn(),
      enable: vi.fn(),
    };
    // `role` is the auth role prop, not an ARIA role.
    // eslint-disable-next-line jsx-a11y/aria-role
    const { container } = render(<GaReadinessPanel token="admin-token" role="admin" api={api} />);
    const region = await screen.findByRole("region", { name: /Backup asset readiness|备份资产就绪/ });
    expect(await runAxe(container)).toHaveNoViolations();

    await user.tab();
    expect(screen.getByRole("button", { name: /Run inventory|运行清单/ })).toHaveFocus();
    expect(region).toHaveAccessibleName(/Backup asset readiness|备份资产就绪/);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("keeps the non-Admin empty render axe-clean", async () => {
    const { container } = render(
      <GaReadinessPanel
        token="operator-token"
        // `role` is the auth role prop, not an ARIA role.
        // eslint-disable-next-line jsx-a11y/aria-role
        role="operator"
        api={{
          getReadiness: vi.fn(),
          runInventory: vi.fn(),
          acknowledge: vi.fn(),
        }}
      />,
    );
    expect(await runAxe(container)).toHaveNoViolations();
    expect(screen.queryByRole("region", { name: /Backup asset readiness|备份资产就绪/ })).not.toBeInTheDocument();
  });
});
