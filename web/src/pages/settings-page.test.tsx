import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { SettingsPage } from "./settings-page";

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token", username: "admin", role: "admin" }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: "zh", changeLanguage: vi.fn() },
  }),
  initReactI18next: { type: "3rdParty", init: vi.fn() },
}));

vi.mock("@/context/theme-context", () => ({
  useTheme: () => ({
    theme: "system",
    setTheme: vi.fn(),
    density: "comfortable",
    setDensity: vi.fn(),
    powerMode: "normal",
    setPowerMode: vi.fn(),
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getSettings: vi.fn().mockResolvedValue({ definitions: [], values: {} }),
    updateSettings: vi.fn(),
    resetSetting: vi.fn(),
    changePassword: vi.fn(),
    backupDB: vi.fn(),
    listBackups: vi.fn().mockResolvedValue([]),
    exportConfig: vi.fn().mockResolvedValue({}),
  },
}));

vi.mock("./settings-page.personal", () => ({
  PersonalTab: () => <div>settings.personal.title</div>,
}));

vi.mock("./settings-page.account", () => ({
  AccountTab: () => <div>settings.account.title</div>,
}));

vi.mock("./settings-page.users", () => ({
  UsersTab: () => <div>settings.users.title</div>,
}));

vi.mock("./settings-page.channels", () => ({
  ChannelsTab: () => <div>settings.channels.title</div>,
}));

vi.mock("./settings-page.system", () => ({
  SystemTab: () => <div>settings.system.title</div>,
}));

vi.mock("./settings-page.maintenance", () => ({
  MaintenanceTab: () => <div>settings.maintenance.title</div>,
}));

vi.mock("./settings-page.escalation", () => ({
  SettingsPageEscalation: () => <div>escalation.tabTitle</div>,
}));

function renderSettingsPage(initialEntries: string[] = ["/app/settings"]) {
  const router = createMemoryRouter(
    [{ path: "/app/settings", element: <SettingsPage /> }],
    { initialEntries }
  );
  return {
    router,
    ...render(<RouterProvider router={router} />)
  };
}

describe("SettingsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders 8 tabs for admin", () => {
    renderSettingsPage();
    expect(screen.getByRole("tab", { name: "settings.tabs.personal" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.account" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.users" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.channels" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.system" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.maintenance" })).toBeDefined();
  });

  it("shows personal tab content by default", () => {
    renderSettingsPage();
    expect(screen.getByText("settings.personal.title")).toBeDefined();
  });

  it("each tab has aria-controls pointing to its own panel id", () => {
    renderSettingsPage();
    const tabs = screen.getAllByRole("tab");
    const tabIds = ["personal", "account", "users", "channels", "silences", "escalation", "system", "maintenance"];
    tabs.forEach((tab, i) => {
      expect(tab).toHaveAttribute("id", `settings-tab-${tabIds[i]}`);
      expect(tab).toHaveAttribute("aria-controls", `settings-panel-${tabIds[i]}`);
    });
  });

  it("active tabpanel has id and aria-labelledby matching active tab", () => {
    renderSettingsPage();
    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveAttribute("id", "settings-panel-personal");
    expect(panel).toHaveAttribute("aria-labelledby", "settings-tab-personal");
  });

  it("respects initial tab from query string", () => {
    renderSettingsPage(["/app/settings?tab=system"]);

    expect(screen.getByRole("tab", { name: "settings.tabs.system" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "settings-panel-system");
  });

  it("syncs active tab when search params change after mount", async () => {
    const { router } = renderSettingsPage(["/app/settings?tab=personal"]);

    act(() => {
      void router.navigate("/app/settings?tab=maintenance");
    });

    await waitFor(() => {
      expect(screen.getByRole("tab", { name: "settings.tabs.maintenance" })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "settings-panel-maintenance");
    });
  });

  it("supports keyboard navigation across tabs", async () => {
    const user = userEvent.setup();
    renderSettingsPage(["/app/settings?tab=personal"]);

    const personalTab = screen.getByRole("tab", { name: "settings.tabs.personal" });
    personalTab.focus();

    await user.keyboard("{ArrowRight}");

    const accountTab = screen.getByRole("tab", { name: "settings.tabs.account" });
    expect(accountTab).toHaveAttribute("aria-selected", "true");
    expect(accountTab).toHaveFocus();
    expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "settings-panel-account");
  });
});
