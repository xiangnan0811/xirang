import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { runAxe } from "@/test/a11y-helpers";
import { CredentialAccessGrantsPage } from "../credential-access-grants-page";

const { authState, listCredentialAccessGrantsMock } = vi.hoisted(() => ({
  authState: { token: "test-token", role: "admin" },
  listCredentialAccessGrantsMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState,
}));

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listCredentialAccessGrants: listCredentialAccessGrantsMock,
    },
  };
});

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: vi.fn(),
  },
}));

describe("CredentialAccessGrantsPage a11y smoke", () => {
  beforeEach(() => {
    authState.token = "test-token";
    authState.role = "admin";
    listCredentialAccessGrantsMock.mockReset();
    listCredentialAccessGrantsMock.mockResolvedValue({
      items: [
        {
          id: 1,
          requesterUserId: 7,
          requesterUsername: "admin",
          requesterRole: "admin",
          action: "terminal.open",
          purpose: "terminal",
          nodeId: 10,
          reason: "例行维护",
          status: "active",
          requestedTtlSeconds: 600,
          requestedAt: "2026-05-20 00:00:00",
          expiresAt: "2026-05-20 00:10:00",
          createdAt: "2026-05-20 00:00:00",
          updatedAt: "2026-05-20 00:00:00",
        },
      ],
      total: 1,
      page: 1,
      pageSize: 30,
    });
  });

  it("初始渲染无 axe violations", async () => {
    const { container } = render(
      <MemoryRouter>
        <CredentialAccessGrantsPage />
      </MemoryRouter>,
    );

    await waitFor(() => expect(listCredentialAccessGrantsMock).toHaveBeenCalledTimes(1));
    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });
});
