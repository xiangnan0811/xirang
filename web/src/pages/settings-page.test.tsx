import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, MemoryRouter, Route, RouterProvider, Routes, type InitialEntry } from "react-router-dom";
import {
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { createNotificationConfigReturn } from "@/lib/notification-config-context";
import { NotificationConfigLink, NotificationConfigLinks } from "@/components/notification-config-links";
import { apiClient } from "@/lib/api/client";
import type { UserRecord } from "@/types/domain";
import { SettingsPage } from "./settings-page";

type Role = "admin" | "operator" | "viewer";

const authState = vi.hoisted(() => ({
  current: {
    token: "test-token" as string | null,
    username: "admin",
    role: "admin" as Role | null,
  },
}));

function setAuthIdentity(token: string | null, role: Role | null, username = role ?? "anonymous") {
  rememberAuthIdentity(token, role);
  authState.current = {
    token,
    username,
    role,
  };
}

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState.current,
}));

vi.mock("react-i18next", () => {
  const t = (key: string, options?: Record<string, unknown>) => {
    if (typeof options?.count === "number") return `${key}:${options.count}`;
    if (typeof options?.id === "string" || typeof options?.id === "number") return `${key}:${options.id}`;
    return key;
  };
  const i18n = { language: "zh", changeLanguage: vi.fn() };
  return {
    useTranslation: () => ({ t, i18n }),
    initReactI18next: { type: "3rdParty", init: vi.fn() },
  };
});

vi.mock("@/context/theme-context.hooks", () => ({
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
    exportConfig: vi.fn().mockResolvedValue({}),
    getUsers: vi.fn().mockResolvedValue([]),
    createUser: vi.fn(),
    updateUser: vi.fn(),
    deleteUser: vi.fn(),
  },
}));

vi.mock("./settings-page.personal", () => ({
  PersonalTab: () => <div>settings.personal.title</div>,
}));

vi.mock("./settings-page.account", () => ({
  AccountTab: () => <div>settings.account.title</div>,
}));

vi.mock("./settings-page.channels", () => ({
  ChannelsTab: () => <div>settings.channels.title</div>,
}));

vi.mock("./settings-page.silences", () => ({
  SilencesPanel: () => <div>settings.silences.title</div>,
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

function usersSettingsSurface() {
  return (
    <MemoryRouter initialEntries={["/app/settings?tab=users"]}>
      <Routes>
        <Route path="/app/settings" element={<SettingsPage />} />
      </Routes>
    </MemoryRouter>
  );
}

function renderSettingsPage(initialEntries: InitialEntry[] = ["/app/settings"]) {
  const router = createMemoryRouter(
    [
      { path: "/app/settings", element: <SettingsPage /> },
      { path: "/app/notifications", element: <div>notifications-page</div> },
    ],
    { initialEntries }
  );
  return {
    router,
    ...render(<RouterProvider router={router} />),
  };
}

describe("SettingsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
    setAuthIdentity("test-token", "admin");
    vi.mocked(apiClient.getUsers).mockReset();
    vi.mocked(apiClient.getUsers).mockResolvedValue([]);
    vi.mocked(apiClient.createUser).mockReset();
    vi.mocked(apiClient.updateUser).mockReset();
    vi.mocked(apiClient.deleteUser).mockReset();
  });

  it("renders the workbench header and admin metadata", () => {
    renderSettingsPage();

    expect(screen.getByRole("heading", { name: "settings.title" })).toBeInTheDocument();
    expect(screen.getByText("settings.pageDesc")).toBeInTheDocument();
    expect(screen.getByText("users.roles.admin")).toBeInTheDocument();
    expect(screen.getByText("8 · settings.tabListLabel")).toBeInTheDocument();
    expect(screen.getByRole("tablist", { name: "settings.tabListLabel" })).toBeInTheDocument();
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

  it("preserves unrelated query params when switching tabs", async () => {
    const user = userEvent.setup();
    const { router } = renderSettingsPage(["/app/settings?tab=personal&mode=compact"]);

    await user.click(screen.getByRole("tab", { name: "settings.tabs.account" }));

    expect(router.state.location.search).toBe("?tab=account&mode=compact");
  });

  it("limits non-admin users to personal settings and account security", () => {
    setAuthIdentity("test-token", "viewer");

    renderSettingsPage(["/app/settings?tab=system"]);

    expect(screen.getAllByText("settings.tabs.personal").length).toBeGreaterThan(0);
    expect(screen.getByText("2 · settings.tabListLabel")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "settings.tabs.personal" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("tab", { name: "settings.tabs.users" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "settings.tabs.system" })).not.toBeInTheDocument();
  });

  it("names the create-user role from users.role and keeps the selected role", async () => {
    const user = userEvent.setup();
    renderSettingsPage(["/app/settings?tab=users"]);

    expect(
      await screen.findByRole("heading", { name: "users.userManagement" }),
    ).toBeInTheDocument();
    const roleSelect = screen.getByRole("combobox", { name: "users.role" });
    expect(roleSelect).toBeInstanceOf(HTMLSelectElement);
    expect(roleSelect).toHaveValue("operator");

    await user.selectOptions(roleSelect, "users.roles.admin");

    expect(await screen.findByText("users.emptyTitle")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "users.role" })).toHaveValue("admin");
  });

  it.each(["operator", "viewer"] as const)(
    "returns %s from the users settings URL to personal settings and still opens account without loading users",
    async (role) => {
      const user = userEvent.setup();
      setAuthIdentity("test-token", role);

      renderSettingsPage(["/app/settings?tab=users"]);

      expect(screen.getByRole("tab", { name: "settings.tabs.personal" })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText("settings.personal.title")).toBeInTheDocument();
      expect(screen.queryByRole("tab", { name: "settings.tabs.users" })).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "users.userManagement" })).not.toBeInTheDocument();
      expect(apiClient.getUsers).not.toHaveBeenCalled();

      await user.click(screen.getByRole("tab", { name: "settings.tabs.account" }));

      expect(screen.getByRole("tab", { name: "settings.tabs.account" })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText("settings.account.title")).toBeInTheDocument();
      expect(apiClient.getUsers).not.toHaveBeenCalled();
    },
  );

  it("discards a pending user list on role loss and fetches a fresh list when admin access returns", async () => {
    let resolveUsers!: (users: UserRecord[]) => void;
    const pendingUsers = new Promise<UserRecord[]>((resolve) => { resolveUsers = resolve; });
    vi.mocked(apiClient.getUsers).mockReturnValueOnce(pendingUsers);
    const { rerender } = render(usersSettingsSurface());
    await waitFor(() => expect(apiClient.getUsers).toHaveBeenCalledTimes(1));

    setAuthIdentity("test-token", "viewer");
    rerender(usersSettingsSurface());

    await act(async () => {
      resolveUsers([{ id: 2, username: "stale-user", role: "admin" }]);
    });

    expect(screen.queryByText("stale-user")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "users.userManagement" })).not.toBeInTheDocument();
    expect(apiClient.getUsers).toHaveBeenCalledTimes(1);

    vi.mocked(apiClient.getUsers).mockResolvedValueOnce([
      { id: 3, username: "fresh-user", role: "viewer" },
    ]);
    setAuthIdentity("test-token", "admin");
    rerender(usersSettingsSurface());

    expect(await screen.findByText("fresh-user")).toBeInTheDocument();
    expect(screen.queryByText("stale-user")).not.toBeInTheDocument();
    expect(apiClient.getUsers).toHaveBeenCalledTimes(2);
    expect(apiClient.getUsers).toHaveBeenNthCalledWith(2, "test-token");
  });

  it("creates, updates, and deletes a user through labelled role selectors", async () => {
    const user = userEvent.setup();
    const created: UserRecord = { id: 7, username: "ada", role: "viewer" };
    vi.mocked(apiClient.createUser).mockResolvedValue(created);
    vi.mocked(apiClient.updateUser).mockResolvedValue({ ...created, role: "admin" });
    vi.mocked(apiClient.deleteUser).mockResolvedValue(undefined);
    renderSettingsPage(["/app/settings?tab=users"]);

    expect(await screen.findByText("users.emptyTitle")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "users.newUsername" }), "ada");
    await user.type(screen.getByLabelText("users.initialPassword"), "long-enough-pass");
    await user.selectOptions(screen.getByRole("combobox", { name: "users.role" }), "users.roles.viewer");
    await user.click(screen.getByRole("button", { name: "users.createUser" }));

    expect(await screen.findByText("ada")).toBeInTheDocument();
    expect(apiClient.createUser).toHaveBeenCalledWith("test-token", {
      username: "ada",
      password: "long-enough-pass",
      role: "viewer",
    });

    const roleSelect = screen.getByRole("combobox", { name: "users.roleForUser" });
    expect(roleSelect).toBeInstanceOf(HTMLSelectElement);
    await user.selectOptions(roleSelect, "users.roles.admin");
    await user.click(screen.getByRole("button", { name: "common.save" }));

    await waitFor(() => {
      expect(apiClient.updateUser).toHaveBeenCalledWith("test-token", 7, {
        role: "admin",
        password: undefined,
      });
      expect(screen.getByText("ada").parentElement?.textContent).toContain("ID: 7");
      expect(screen.getByText("ada").parentElement?.textContent).toContain("users.roles.admin");
    });

    await user.click(screen.getByRole("button", { name: "common.delete" }));
    const dialog = await screen.findByRole("alertdialog", { name: "users.confirmDeleteTitle" });
    await user.click(within(dialog).getByRole("button", { name: "common.delete" }));

    await waitFor(() => {
      expect(apiClient.deleteUser).toHaveBeenCalledWith("test-token", 7);
      expect(screen.queryByText("ada")).not.toBeInTheDocument();
    });
    expect(screen.getByText("users.emptyTitle")).toBeInTheDocument();
  });

  describe("notification config context", () => {
    it("renders wrap-return region for an alert target, focuses the active tab once, and preserves state across tabs", async () => {
      const user = userEvent.setup();
      const payload = createNotificationConfigReturn("42");
      expect(payload).not.toBeNull();

      const { router } = renderSettingsPage([
        {
          pathname: "/app/settings",
          search: "?tab=channels&securityReturnTo=/app/notifications",
          hash: "#config",
          state: { notificationConfigReturn: payload },
        },
      ]);

      const channelsTab = screen.getByRole("tab", { name: "settings.tabs.channels" });
      expect(channelsTab).toHaveAttribute("aria-selected", "true");
      expect(channelsTab).toHaveFocus();

      expect(screen.getByText("notifications.configContext.fromAlert:42")).toBeInTheDocument();
      expect(screen.getByText("notifications.configContext.manualReturnHint")).toBeInTheDocument();

      const returnLink = screen.getByRole("link", { name: "notifications.configContext.returnAlert" });
      expect(returnLink).toHaveAttribute("href", "/app/notifications?alert=42");

      // Switch to personal tab: region is hidden, but valid state is preserved
      await user.click(screen.getByRole("tab", { name: "settings.tabs.personal" }));
      expect(screen.queryByText("notifications.configContext.fromAlert:42")).not.toBeInTheDocument();
      expect(router.state.location.search).toBe("?tab=personal&securityReturnTo=%2Fapp%2Fnotifications");
      expect(router.state.location.hash).toBe("#config");
      expect(router.state.location.state).toEqual({ notificationConfigReturn: payload });

      // Switch back to silences tab: region reappears, focus is not stolen back to channels
      await user.click(screen.getByRole("tab", { name: "settings.tabs.silences" }));
      expect(screen.getByText("notifications.configContext.fromAlert:42")).toBeInTheDocument();
      const silencesTab = screen.getByRole("tab", { name: "settings.tabs.silences" });
      expect(silencesTab).toHaveFocus();

      // Return navigation carries only the notificationConfigReturn key
      await user.click(screen.getByRole("link", { name: "notifications.configContext.returnAlert" }));
      expect(router.state.location.pathname).toBe("/app/notifications");
      expect(router.state.location.search).toBe("?alert=42");
      expect(router.state.location.state).toEqual({ notificationConfigReturn: payload });
    });

    it("renders return link for list context without alert ID", () => {
      const payload = createNotificationConfigReturn(null);
      expect(payload).not.toBeNull();

      renderSettingsPage([
        {
          pathname: "/app/settings",
          search: "?tab=escalation",
          state: { notificationConfigReturn: payload },
        },
      ]);

      expect(screen.getByText("notifications.configContext.fromList")).toBeInTheDocument();
      const returnLink = screen.getByRole("link", { name: "notifications.configContext.returnList" });
      expect(returnLink).toHaveAttribute("href", "/app/notifications");
    });

    it("cleans up invalid or expired notification context while preserving query params, hash and other state", async () => {
      const invalidPayload = { version: 1, source: "notifications", alertId: "42", owner: "expired-epoch:0" };
      const { router } = renderSettingsPage([
        {
          pathname: "/app/settings",
          search: "?tab=channels&mode=compact",
          hash: "#details",
          state: { keep: "yes", notificationConfigReturn: invalidPayload },
        },
      ]);

      await waitFor(() => {
        expect(router.state.location.state).toEqual({ keep: "yes" });
      });
      expect(router.state.location.search).toBe("?tab=channels&mode=compact");
      expect(router.state.location.hash).toBe("#details");
      expect(screen.queryByText("notifications.configContext.manualReturnHint")).not.toBeInTheDocument();
    });

    it("synchronously hides return region and prevents stale navigation after session generation advances", async () => {
      const user = userEvent.setup();
      const payload = createNotificationConfigReturn("42");
      expect(payload).not.toBeNull();

      renderSettingsPage([
        {
          pathname: "/app/settings",
          search: "?tab=channels",
          state: { notificationConfigReturn: payload },
        },
      ]);

      const returnLink = screen.getByRole("link", { name: "notifications.configContext.returnAlert" });
      act(() => {
        bumpAuthSessionGeneration();
      });
      await user.click(returnLink);

      expect(screen.queryByText("notifications.configContext.returnAlert")).not.toBeInTheDocument();
    });
  });

  describe("NotificationConfigLinks and NotificationConfigLink", () => {
    it("renders channels, silences, and escalation links with description for valid alert target", () => {
      render(
        <MemoryRouter>
          <NotificationConfigLinks alertId="alert-42" />
        </MemoryRouter>
      );

      const nav = screen.getByRole("navigation", { name: "notifications.configNavLabel" });
      expect(nav).toBeInTheDocument();
      expect(within(nav).getByRole("link", { name: "notifications.configChannels" })).toHaveAttribute(
        "href",
        "/app/settings?tab=channels"
      );
      expect(within(nav).getByRole("link", { name: "notifications.configSilences" })).toHaveAttribute(
        "href",
        "/app/settings?tab=silences"
      );
      expect(within(nav).getByRole("link", { name: "notifications.configEscalation" })).toHaveAttribute(
        "href",
        "/app/settings?tab=escalation"
      );
      expect(within(nav).getByText("notifications.configContext.forAlert")).toBeInTheDocument();
    });

    it("renders links without description for list target (alertId null)", () => {
      render(
        <MemoryRouter>
          <NotificationConfigLinks alertId={null} />
        </MemoryRouter>
      );

      const nav = screen.getByRole("navigation", { name: "notifications.configNavLabel" });
      expect(nav).toBeInTheDocument();
      expect(screen.queryByText("notifications.configContext.forAlert")).not.toBeInTheDocument();
    });

    it("hides entire group for invalid non-null alert ID without downgrading to list", () => {
      const { container } = render(
        <MemoryRouter>
          <NotificationConfigLinks alertId="invalid-id" />
        </MemoryRouter>
      );

      expect(container).toBeEmptyDOMElement();
    });

    it("hides for non-admin user", () => {
      setAuthIdentity("test-token", "viewer");
      const { container } = render(
        <MemoryRouter>
          <NotificationConfigLinks alertId="42" />
        </MemoryRouter>
      );

      expect(container).toBeEmptyDOMElement();
    });

    it("prevents stale navigation when session is invalidated before click", async () => {
      const user = userEvent.setup();
      const router = createMemoryRouter([
        {
          path: "/",
          element: (
            <NotificationConfigLink tab="channels" alertId="42">
              Go to channels
            </NotificationConfigLink>
          ),
        },
        {
          path: "/app/settings",
          element: <div>settings-page</div>,
        },
      ]);
      render(<RouterProvider router={router} />);

      const link = screen.getByRole("link", { name: "Go to channels" });
      // Change core ownership without notifying React: the rendered click handler itself must reject it.
      rememberAuthIdentity("other-session", "admin");
      await user.click(link);

      expect(router.state.location.pathname).toBe("/");
    });
  });
});
