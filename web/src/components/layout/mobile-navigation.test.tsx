import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { ThemeProvider } from "@/context/theme-context";
import { MobileNavigation } from "./mobile-navigation";

function renderWithProviders() {
  return render(
    <ThemeProvider>
      <MemoryRouter
        initialEntries={["/app/overview"]}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <MobileNavigation username="alice" role="admin" totpEnabled={false} onLogout={vi.fn()} onRefresh={vi.fn()} onTotpSetup={vi.fn()} onTotpDisable={vi.fn()} />
      </MemoryRouter>
    </ThemeProvider>
  );
}

describe("MobileNavigation", () => {
  it("底部导航使用链接语义并标记当前页", () => {
    renderWithProviders();

    expect(screen.getByRole("link", { name: "切换到概览" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "切换到节点" })).toBeInTheDocument();
  });

  it("抽屉具备对话框语义并支持 Esc 关闭且焦点回到触发按钮", async () => {
    const user = userEvent.setup();

    renderWithProviders();

    const menuButton = screen.getByRole("button", { name: "打开快捷菜单" });
    expect(menuButton).toHaveAttribute("aria-expanded", "false");

    await user.click(menuButton);
    expect(menuButton).toHaveAttribute("aria-expanded", "true");

    const drawer = screen.getByRole("dialog", { name: /运维快捷操作/ });
    const activeLinkInDrawer = within(drawer).getByRole("link", { name: "概览" });
    expect(activeLinkInDrawer).toHaveAttribute("aria-current", "page");

    fireEvent.keyDown(document, { key: "Escape" });

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /运维快捷操作/ })).not.toBeInTheDocument();
    });
    expect(menuButton).toHaveFocus();
  });
});
