import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";

import { recoveryPoint, repository } from "./__tests__/test-utils";
import { RepositoryManagementPanel } from "./repository-management-panel";
import { RetentionPolicyPanel } from "./retention-policy-panel";

describe("backup assets lifecycle panels a11y", () => {
  it("keeps the Admin repository and retention panels axe-clean", async () => {
    const { container } = render(
      <div>
        <RepositoryManagementPanel
          repositories={[{ status: "available", value: repository }]}
          selectedRepositoryId={repository.id}
          viewport="desktop"
          onBrowse={vi.fn()}
          runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
          api={{}}
        />
        <RetentionPolicyPanel
          repositories={[{ status: "available", value: repository }]}
          recoveryPoints={[{ status: "available", value: recoveryPoint }]}
          runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
          api={{ listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }) }}
        />
      </div>,
    );
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("keeps the empty and non-Admin repository panel axe-clean", async () => {
    const { container, rerender } = render(
      <RepositoryManagementPanel
        repositories={[]}
        viewport="mobile"
        onBrowse={vi.fn()}
        runtime={{ token: "viewer-token", role: "viewer", ensureStepUpProof: vi.fn() }}
      />,
    );
    expect(await runAxe(container)).toHaveNoViolations();
    rerender(
      <RepositoryManagementPanel
        repositories={[{ status: "blocked", reason: { code: "unknown_internal_state", params: {} } }]}
        viewport="intermediate"
        onBrowse={vi.fn()}
      />,
    );
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("keeps an open lifecycle dialog axe-clean", async () => {
    const user = userEvent.setup();
    render(
      <RepositoryManagementPanel
        repositories={[{ status: "available", value: repository }]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={{}}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Reconcile|重新探测/ }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });
});
