import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { runAxe } from "@/test/a11y-helpers";
import { CredentialAccessGrantsPage } from "../credential-access-grants-page";
import i18n from "@/i18n";
import { ApiError, bumpAuthSessionGeneration, clearAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import { mapCredentialAccessGrant } from "@/lib/api/credential-access-grants-api";
import type * as ClientModule from "@/lib/api/client";

const { authState, listCredentialAccessGrantsMock } = vi.hoisted(() => ({
  authState: { token: "test-token", role: "admin", authTransitioning: false },
  listCredentialAccessGrantsMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState,
}));

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof ClientModule>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listCredentialAccessGrants: listCredentialAccessGrantsMock,
    },
  };
});

function renderPage() {
  return render(
    <MemoryRouter>
      <CredentialAccessGrantsPage />
    </MemoryRouter>,
  );
}

describe("CredentialAccessGrantsPage a11y smoke", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    authState.token = "test-token";
    authState.role = "admin";
    authState.authTransitioning = false;
    rememberAuthIdentity("test-token", "admin");
    bumpAuthSessionGeneration();
    listCredentialAccessGrantsMock.mockReset();
    listCredentialAccessGrantsMock.mockResolvedValue({
      items: [
        mapCredentialAccessGrant({
          id: 1,
          requester_user_id: 7,
          requester_username: "admin",
          requester_role: "admin",
          action: "terminal.open",
          purpose: "terminal",
          node_id: 10,
          reason: "例行维护",
          status: "active",
          requested_ttl_seconds: 600,
          requested_at: "2026-05-20T00:00:00Z",
          expires_at: "2026-05-20T00:10:00Z",
          created_at: "2026-05-20T00:00:00Z",
          updated_at: "2026-05-20T00:00:00Z",
        }),
        mapCredentialAccessGrant({
          id: 2,
          requester_user_id: 8,
          requester_username: "legacy",
          requester_role: "operator",
          action: "config.import",
          purpose: "config_import",
          status: "future_state",
          requested_ttl_seconds: 600,
          expires_at: null,
          created_at: "2026-05-20T00:00:00Z",
          updated_at: "2026-05-20T00:00:00Z",
        }),
      ],
      total: 2,
      page: 1,
      pageSize: 30,
    });
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
  });

  it("初始渲染无 axe violations", async () => {
    const { container } = renderPage();

    await screen.findByRole("region", { name: i18n.t("credentialGrants.pageTitle") });
    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });

  it("加载失败的错误提示无 axe violations", async () => {
    const error = new ApiError(503, "raw-503-marker");
    error.httpStatus = 503;
    listCredentialAccessGrantsMock.mockReset();
    listCredentialAccessGrantsMock.mockRejectedValue(error);
    const { container } = renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("credentialGrants.loadFailed"));
    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });

  it("names the scrollable table region with the grants title and accepts focus", async () => {
    renderPage();

    const region = await screen.findByRole("region", { name: i18n.t("credentialGrants.pageTitle") });
    region.focus();
    expect(region).toHaveFocus();
  });
});
