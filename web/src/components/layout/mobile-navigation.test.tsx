import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { ThemeProvider } from "@/context/theme-context";
import { parseBackupAssetsRoute } from "@/features/backup-assets/backup-assets-route-state";
import { MobileNavigation } from "./mobile-navigation";

const savedSearchId = "e".repeat(32);
const filesSavedSearch = `?view=search&savedSearchId=${savedSearchId}`;

function renderWithProviders(initialEntry = "/app/overview") {
  return render(
    <ThemeProvider>
      <I18nextProvider i18n={i18n}>
        <MemoryRouter initialEntries={[initialEntry]}>
          {/* `role` 是组件自定义 prop（用户角色），并非 ARIA role；
               jsx-a11y 误判为 role 属性。 */}
          {/* eslint-disable-next-line jsx-a11y/aria-role */}
          <MobileNavigation username="alice" role="admin" onLogout={vi.fn()} onRefresh={vi.fn()} />
        </MemoryRouter>
      </I18nextProvider>
    </ThemeProvider>
  );
}

describe("MobileNavigation", () => {
  it("底部导航使用链接语义并标记当前页", async () => {
    await setLanguage("zh");
    renderWithProviders();

    expect(screen.getByRole("link", { name: "切换到概览" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "切换到节点" })).toBeInTheDocument();
  });

  it("More tab renders translated nav.more label, not the raw key", async () => {
    await setLanguage("zh");
    renderWithProviders();

    const moreButton = screen.getByRole("button", { name: "打开快捷菜单" });
    expect(moreButton).toHaveTextContent("更多");
    expect(moreButton).not.toHaveTextContent("nav.more");
  });

  it("抽屉具备对话框语义并支持 Esc 关闭且焦点回到触发按钮", async () => {
    await setLanguage("zh");
    const user = userEvent.setup();

    renderWithProviders();

    const menuButton = screen.getByRole("button", { name: "打开快捷菜单" });
    expect(menuButton).toHaveAttribute("aria-expanded", "false");

    await user.click(menuButton);
    expect(menuButton).toHaveAttribute("aria-expanded", "true");

    const drawer = screen.getByRole("dialog", { name: /运维快捷操作/ });
    // Drawer contains non-primary-tab items (Policies, Backups, Notifications, etc.)
    expect(within(drawer).getAllByRole("link").length).toBeGreaterThan(0);

    fireEvent.keyDown(document, { key: "Escape" });

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /运维快捷操作/ })).not.toBeInTheDocument();
    });
    expect(menuButton).toHaveFocus();
  });

  it("marks Backups current for nested and trailing backup paths", async () => {
    await setLanguage("zh");
    const user = userEvent.setup();
    renderWithProviders("/app/backups/data/");

    await user.click(screen.getByRole("button", { name: "打开快捷菜单" }));
    const backups = screen.getByRole("link", { name: "备份" });
    expect(backups).toHaveAttribute("aria-current", "page");
    expect(backups).toHaveAttribute("href", "/app/backups/data");
  });

  it("keeps Backups current on Overview and Recovery with a queryless Files href", async () => {
    await setLanguage("zh");
    const user = userEvent.setup();
    const overview = renderWithProviders("/app/backups/overview?foo=1");
    await user.click(screen.getByRole("button", { name: "打开快捷菜单" }));
    expect(screen.getByRole("link", { name: "备份" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "备份" })).toHaveAttribute("href", "/app/backups/data");
    overview.unmount();

    renderWithProviders("/app/backups/recovery?planId=abc");
    await user.click(screen.getByRole("button", { name: "打开快捷菜单" }));
    expect(screen.getByRole("link", { name: "备份" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "备份" })).toHaveAttribute("href", "/app/backups/data");
  });

  it("preserves exact Files search on the Backups href only while on Files", async () => {
    await setLanguage("zh");
    const user = userEvent.setup();
    expect(parseBackupAssetsRoute("/app/backups/data", filesSavedSearch)).toMatchObject({
      status: "valid",
      state: { page: "data", view: "search", savedSearchId },
    });
    renderWithProviders(`/app/backups/data${filesSavedSearch}`);

    await user.click(screen.getByRole("button", { name: "打开快捷菜单" }));
    expect(screen.getByRole("link", { name: "备份" })).toHaveAttribute(
      "href",
      `/app/backups/data${filesSavedSearch}`,
    );
  });
});
