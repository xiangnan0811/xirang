import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { CredentialReferencesDialog } from "./credential-references-dialog";
import { ApiError } from "@/lib/api/core";
import { runAxe } from "@/test/a11y-helpers";

const { authState, listReferencesMock } = vi.hoisted(() => ({
  authState: {
    current: {
      token: "test-token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      authTransitioning: false,
    },
  },
  listReferencesMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState.current,
}));

vi.mock("@/lib/api/credentials", async (original) => {
  const actual = await original<typeof import("@/lib/api/credentials")>();
  return {
    ...actual,
    createCredentialsApi: () => ({
      listReferences: (...args: unknown[]) => listReferencesMock(...args),
    }),
  };
});

function renderDialog() {
  return render(
    <MemoryRouter>
      <CredentialReferencesDialog
        open
        onOpenChange={vi.fn()}
        credential={{ id: 4, name: "生产库" }}
      />
    </MemoryRouter>,
  );
}

describe("CredentialReferencesDialog a11y", () => {
  beforeEach(() => {
    authState.current = { token: "test-token", role: "admin", authTransitioning: false };
    listReferencesMock.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it("loaded reference links have no axe violations", async () => {
    listReferencesMock.mockResolvedValue({
      items: [
        { id: 11, name: "夜间备份" },
        { id: 12, name: "策略名称很长很长很长很长并且应该在对话框内换行而不是被截成无法识别的控件" },
      ],
      total: 2,
      page: 1,
      pageSize: 20,
    });
    renderDialog();
    expect(await screen.findByRole("dialog", { name: "引用 生产库 的策略" })).toHaveAccessibleDescription(
      /引用可能变化/,
    );
    expect(await screen.findByRole("link", { name: /夜间备份/ })).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("the empty reference state has no axe violations", async () => {
    listReferencesMock.mockResolvedValue({ items: [], total: 0, page: 1, pageSize: 20 });
    renderDialog();
    expect(await screen.findByText("当前没有策略引用此凭据。")).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("the reference error state has no axe violations", async () => {
    listReferencesMock.mockRejectedValue(new Error("server down"));
    renderDialog();
    expect(await screen.findByRole("alert")).toHaveTextContent("server down");
    expect(screen.getByRole("button", { name: "重试" })).toBeEnabled();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("the missing credential state has no axe violations", async () => {
    listReferencesMock.mockRejectedValue(new ApiError(404, "missing"));
    renderDialog();
    expect(await screen.findByRole("alert")).toHaveTextContent("凭据不存在");
    expect(await runAxe(document.body)).toHaveNoViolations();
  });
});
