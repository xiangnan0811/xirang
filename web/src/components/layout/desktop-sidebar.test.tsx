import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { parseBackupAssetsRoute } from "@/features/backup-assets/backup-assets-route-state";
import { DesktopSidebar } from "./desktop-sidebar";

const savedSearchId = "e".repeat(32);
const filesSavedSearch = `?view=search&savedSearchId=${savedSearchId}`;

function renderSidebar(initialEntry: string) {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={[initialEntry]}>
        {/* eslint-disable-next-line jsx-a11y/aria-role -- role is the user-role prop, not an ARIA role */}
        <DesktopSidebar role="admin" isCollapsed={false} onToggleCollapse={vi.fn()} />
      </MemoryRouter>
    </I18nextProvider>,
  );
}

function backupsLink() {
  return screen.getByRole("link", { name: "备份", hidden: true });
}

describe("DesktopSidebar backups navigation", () => {
  it.each(["/app/backups", "/app/backups/data", "/app/backups/overview", "/app/backups/recovery"])(
    "marks Backups current on %s with canonical Files href",
    async (entry) => {
      await setLanguage("zh");
      renderSidebar(entry);
      expect(backupsLink()).toHaveAttribute("aria-current", "page");
      expect(backupsLink()).toHaveAttribute("href", "/app/backups/data");
    },
  );

  it("preserves exact Files search only while on Files", async () => {
    await setLanguage("zh");
    expect(parseBackupAssetsRoute("/app/backups/data", filesSavedSearch)).toMatchObject({
      status: "valid",
      state: { page: "data", view: "search", savedSearchId },
    });
    const files = renderSidebar(`/app/backups/data${filesSavedSearch}`);
    expect(backupsLink()).toHaveAttribute("href", `/app/backups/data${filesSavedSearch}`);
    files.unmount();

    const overview = renderSidebar("/app/backups/overview?foo=1");
    expect(backupsLink()).toHaveAttribute("aria-current", "page");
    expect(backupsLink()).toHaveAttribute("href", "/app/backups/data");
    overview.unmount();

    renderSidebar("/app/backups/recovery?planId=abc");
    expect(backupsLink()).toHaveAttribute("aria-current", "page");
    expect(backupsLink()).toHaveAttribute("href", "/app/backups/data");
  });

  it("is not current for false prefix siblings", async () => {
    await setLanguage("zh");
    renderSidebar("/app/backups-data");
    expect(backupsLink()).not.toHaveAttribute("aria-current");
    expect(backupsLink()).toHaveAttribute("href", "/app/backups/data");
  });
});
