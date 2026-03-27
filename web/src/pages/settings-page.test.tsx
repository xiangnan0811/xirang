import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
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
    exportConfig: vi.fn().mockResolvedValue({}),
  },
}));

function renderWithRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

describe("SettingsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders 6 tabs for admin", () => {
    renderWithRouter(<SettingsPage />);
    expect(screen.getByRole("tab", { name: "settings.tabs.personal" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.account" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.users" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.channels" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.system" })).toBeDefined();
    expect(screen.getByRole("tab", { name: "settings.tabs.maintenance" })).toBeDefined();
  });

  it("shows personal tab content by default", () => {
    renderWithRouter(<SettingsPage />);
    expect(screen.getByText("settings.personal.title")).toBeDefined();
  });

  it("each tab has aria-controls pointing to its own panel id", () => {
    renderWithRouter(<SettingsPage />);
    const tabs = screen.getAllByRole("tab");
    const tabIds = ["personal", "account", "users", "channels", "system", "maintenance"];
    tabs.forEach((tab, i) => {
      expect(tab).toHaveAttribute("id", `settings-tab-${tabIds[i]}`);
      expect(tab).toHaveAttribute("aria-controls", `settings-panel-${tabIds[i]}`);
    });
  });

  it("active tabpanel has id and aria-labelledby matching active tab", () => {
    renderWithRouter(<SettingsPage />);
    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveAttribute("id", "settings-panel-personal");
    expect(panel).toHaveAttribute("aria-labelledby", "settings-tab-personal");
  });
});
