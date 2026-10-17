import "@testing-library/jest-dom/vitest";
import * as React from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
  useNavigate,
  type InitialEntry,
  type Location,
  type NavigateFunction,
} from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import i18n from "@/i18n";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core";
import type { AuthRole } from "@/context/auth-context.shared";
import { AppShell } from "@/components/layout/app-shell";
import { AuditPage } from "@/pages/audit-page";
import { AutomationRulesPage } from "@/pages/automation-rules-page";
import { CredentialsPage } from "@/pages/credentials-page";
import { CredentialAuditPage } from "@/pages/credential-audit-page";
import { CredentialAccessGrantsPage } from "@/pages/credential-access-grants-page";
import { resetAccessDeniedNavigationForTests } from "./access-denied-navigation.state";

type AuthState = {
  username: string;
  role: AuthRole | null;
  token: string | null;
  authTransitioning: boolean;
  totpEnabled: boolean;
  logout: () => void;
  setTotpEnabled: () => void;
};

const mocks = vi.hoisted(() => {
  let state: AuthState = {
    username: "alice",
    role: "operator",
    token: "token-a",
    authTransitioning: false,
    totpEnabled: false,
    logout: () => undefined,
    setTotpEnabled: () => undefined,
  };
  const listeners = new Set<() => void>();
  return {
    toastInfo: vi.fn<(message: string, options: { id: string }) => void>(),
    toastDismiss: vi.fn<(id?: string) => void>(),
    toastError: vi.fn<(message: string) => void>(),
    auth: {
      get: () => state,
      set(next: Partial<AuthState>) {
        state = { ...state, ...next };
        for (const listener of listeners) listener();
      },
      subscribe(listener: () => void) {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      },
    },
  };
});
vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => React.useSyncExternalStore(mocks.auth.subscribe, mocks.auth.get),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { info: mocks.toastInfo, dismiss: mocks.toastDismiss, success: vi.fn(), error: mocks.toastError },
}));

vi.mock("@/hooks/use-console-data", () => ({
  useConsoleData: () => ({ globalSearch: "", setGlobalSearch: vi.fn(), refresh: vi.fn(), loading: false, nodes: [], overview: { activePolicies: 0 }, warning: null }),
}));
vi.mock("@/hooks/use-persistent-state", () => ({
  usePersistentState: <T,>(_: string, initialValue: T) => React.useState(initialValue),
}));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/display-preferences-toggle", () => ({ DisplayPreferencesToggle: () => null }));
vi.mock("@/components/scroll-to-top", () => ({ ScrollToTop: () => null }));
vi.mock("@/components/error-boundary", () => ({
  ErrorBoundary: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/layout/mobile-navigation", () => ({ MobileNavigation: () => null }));
vi.mock("@/components/ui/command-palette", () => ({ CommandPalette: () => null }));

const DENIED_PATHS = [
  "/app/audit",
  "/app/automation-rules",
  "/app/credentials",
  "/app/credential-audit",
  "/app/credential-access-grants",
] as const;
const RESTRICTED_REQUEST = /\/(audit-logs|automation-rule|credential)/;
const ADMIN_PAGES = [
  ["/app/audit", "nav.audit"],
  ["/app/automation-rules", "automation.pageTitle"],
  ["/app/credential-audit", "credentialAudit.pageTitle"],
] as const;

const probe = {} as { navigate: NavigateFunction; location: Location };
let fetchSpy: MockInstance<typeof fetch>;
let seenLocations: Location[] = [];

function Probe() {
  const navigate = useNavigate();
  const location = useLocation();
  React.useEffect(() => {
    probe.navigate = navigate;
    probe.location = location;
    if (!seenLocations.some((entry) => entry.key === location.key)) seenLocations.push(location);
  }, [location, navigate]);
  return null;
}

function shellTree(initialEntries: InitialEntry[]) {
  return (
    <React.StrictMode>
      <MemoryRouter initialEntries={initialEntries}>
        <Probe />
        <Routes>
          <Route path="/app" element={<AppShell />}>
            <Route path="overview" element={<p>Overview destination</p>} />
            <Route path="settings" element={<p>Settings destination</p>} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="automation-rules" element={<AutomationRulesPage />} />
            <Route path="credentials" element={<CredentialsPage />} />
            <Route path="credential-audit" element={<CredentialAuditPage />} />
            <Route path="credential-access-grants" element={<CredentialAccessGrantsPage />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </React.StrictMode>
  );
}

function signIn(token: string | null, role: AuthRole | null) {
  act(() => {
    bumpAuthSessionGeneration();
    rememberAuthIdentity(token, role);
    mocks.auth.set({ token, role });
  });
}

async function settle(ms: number) {
  await act(() => new Promise<void>((resolve) => window.setTimeout(resolve, ms)));
}

function restrictedRequests(): string[] {
  return fetchSpy.mock.calls
    .map(([input]) => (input instanceof Request ? input.url : String(input)))
    .filter((url) => RESTRICTED_REQUEST.test(url));
}

function noticeIds(): string[] {
  return mocks.toastInfo.mock.calls.map(([, options]) => options.id);
}

const deniedText = () => i18n.t("navigation.accessDenied");

function respondWithEmptyRestrictedLists() {
  fetchSpy.mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    const body = url.includes("/automation-rules")
      ? { code: 0, message: "ok", data: [] }
      : { code: 0, message: "ok", data: [], total: 0, page: 1, page_size: 20 };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}

// Mirrors the provider: authTransitioning is derived from the core barrier.
function enterAuthTransition() {
  act(() => {
    beginAuthTransitionBarrier();
    mocks.auth.set({ authTransitioning: true });
  });
}

function settleAuthTransition() {
  act(() => {
    clearAuthTransitionBarrier();
    mocks.auth.set({ authTransitioning: false });
  });
}

const pageHeading = (titleKey: string) => ({ level: 1, name: i18n.t(titleKey) });

beforeEach(() => {
  resetAccessDeniedNavigationForTests();
  clearAuthTransitionBarrier();
  seenLocations = [];
  mocks.toastInfo.mockReset();
  mocks.toastDismiss.mockReset();
  mocks.toastError.mockReset();
  bumpAuthSessionGeneration();
  rememberAuthIdentity("token-a", "operator");
  mocks.auth.set({ token: "token-a", role: "operator", authTransitioning: false });
  fetchSpy = vi.spyOn(globalThis, "fetch");
});

afterEach(() => {
  clearAuthTransitionBarrier();
  fetchSpy.mockRestore();
});

describe("access-denied navigation", () => {
  it.each(DENIED_PATHS.flatMap((path) => (["operator", "viewer"] as const).map((role) => [path, role] as const)))(
    "returns %s to overview once for %s without restricted requests",
    async (path, role) => {
      signIn("token-a", role);
      const view = render(shellTree([path]));

      await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
      expect(mocks.toastInfo).toHaveBeenCalledWith(deniedText(), { id: expect.stringMatching(/^access-denied:.+/) });
      expect(probe.location.pathname).toBe("/app/overview");
      await waitFor(() => expect(probe.location.state).toBeNull());
      expect(await screen.findByText("Overview destination")).toBeInTheDocument();

      // The handoff state carries only the fixed code and opaque id.
      const handoff = seenLocations.find((entry) => entry.state !== null && entry.pathname === "/app/overview");
      expect(handoff?.state).toEqual({ navigationNotice: { code: "access_denied", id: expect.any(String) } });
      expect(JSON.stringify(handoff?.state)).not.toMatch(/token-a|alice|\/app\//);

      // Ordinary rerenders (polling/context refresh) do not replay the notice.
      view.rerender(shellTree([path]));
      act(() => mocks.auth.set({}));
      await settle(50);
      expect(mocks.toastInfo).toHaveBeenCalledTimes(1);
      expect(restrictedRequests()).toEqual([]);
    },
  );

  it("redirects a same-route visit with a new key during the exit animation and notifies again", async () => {
    render(shellTree(["/app/audit"]));
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1), { interval: 5 });
    // Precondition: the denied page is still the exiting AnimatePresence child.
    expect(screen.queryByText("Overview destination")).toBeNull();

    act(() => {
      void probe.navigate("/app/audit");
    });

    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(2));
    const [first, second] = noticeIds();
    expect(second).not.toBe(first);
    expect(probe.location.pathname).toBe("/app/overview");
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();

    // No retained producer steals a later, unrelated navigation.
    act(() => {
      void probe.navigate("/app/settings");
    });
    expect(await screen.findByText("Settings destination")).toBeInTheDocument();
    await settle(400);
    expect(probe.location.pathname).toBe("/app/settings");
    expect(mocks.toastInfo).toHaveBeenCalledTimes(2);
    expect(restrictedRequests()).toEqual([]);
  });

  it("does not replay on back/forward and repeats only for an independent denial", async () => {
    render(shellTree(["/app/settings", "/app/audit"]));
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();

    act(() => {
      void probe.navigate(-1);
    });
    expect(await screen.findByText("Settings destination")).toBeInTheDocument();
    act(() => {
      void probe.navigate(1);
    });
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    await settle(50);
    expect(mocks.toastInfo).toHaveBeenCalledTimes(1);

    act(() => {
      void probe.navigate("/app/credentials");
    });
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(2));
    expect(new Set(noticeIds()).size).toBe(2);
    expect(mocks.toastInfo).toHaveBeenLastCalledWith(deniedText(), { id: expect.stringMatching(/^access-denied:/) });
  });

  it("strips a refreshed or forged notice without toasting and keeps the rest of the location", async () => {
    render(shellTree([{
      pathname: "/app/overview",
      search: "?range=7d",
      hash: "#health",
      state: { navigationNotice: { code: "access_denied", id: "replayed-id" }, securityReturnTo: "/app/settings" },
    }]));

    await waitFor(() => expect(probe.location.state).toEqual({ securityReturnTo: "/app/settings" }));
    expect(probe.location.search).toBe("?range=7d");
    expect(probe.location.hash).toBe("#health");
    await settle(50);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
  });

  it("strips forged notices and unknown codes without toasting", async () => {
    render(shellTree([{ pathname: "/app/overview", state: { navigationNotice: { code: "<b>unknown</b>", id: "x" } } }]));
    await waitFor(() => expect(probe.location.state).toBeNull());
    act(() => {
      void probe.navigate("/app/overview", { state: { navigationNotice: { code: "access_denied", id: "guess" } } });
    });
    await waitFor(() => expect(probe.location.state).toBeNull());
    await settle(50);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
  });

  it.each([
    ["missing token", null, "operator"],
    ["settled null role", "token-a", null],
  ] as const)("keeps the silent fallback for %s", async (_label, token, role) => {
    signIn(token, role);
    render(shellTree(["/app/credential-audit"]));
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    await settle(50);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
    expect(restrictedRequests()).toEqual([]);
  });

  it("does not claim a denial when the live identity disagrees with the rendered context", async () => {
    act(() => rememberAuthIdentity("token-b", "viewer"));
    render(shellTree(["/app/audit"]));
    expect(await screen.findByText("Overview destination")).toBeInTheDocument();
    await settle(50);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
  });

  it("waits during an auth transition and notifies once after it settles", async () => {
    act(() => mocks.auth.set({ authTransitioning: true }));
    render(shellTree(["/app/automation-rules"]));
    await settle(50);
    expect(probe.location.pathname).toBe("/app/automation-rules");
    expect(mocks.toastInfo).not.toHaveBeenCalled();

    act(() => mocks.auth.set({ authTransitioning: false }));
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    expect(probe.location.pathname).toBe("/app/overview");
    expect(restrictedRequests()).toEqual([]);
  });

  it("dismisses only its own notice on identity change and never revives it on A-B-A", async () => {
    render(shellTree(["/app/audit"]));
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    const [ownId] = noticeIds();
    expect(mocks.toastDismiss).not.toHaveBeenCalled();

    signIn("token-b", "viewer");
    expect(mocks.toastDismiss).toHaveBeenCalledTimes(1);
    expect(mocks.toastDismiss).toHaveBeenCalledWith(ownId);

    signIn("token-a", "operator");
    await settle(50);
    expect(mocks.toastInfo).toHaveBeenCalledTimes(1);
    expect(mocks.toastDismiss).toHaveBeenCalledTimes(1);
  });

  it("dismisses its own notice when an auth barrier begins", async () => {
    render(shellTree(["/app/credential-access-grants"]));
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    const [ownId] = noticeIds();

    act(() => {
      beginAuthTransitionBarrier();
    });
    expect(mocks.toastDismiss).toHaveBeenCalledTimes(1);
    expect(mocks.toastDismiss).toHaveBeenCalledWith(ownId);
  });

  describe.each(ADMIN_PAGES)("admin guard for %s", (path, titleKey) => {
    beforeEach(() => {
      signIn("token-admin", "admin");
      respondWithEmptyRestrictedLists();
    });

    it("does not mount restricted content while entering a transition, then loads once settled", async () => {
      enterAuthTransition();
      render(shellTree([path]));
      await settle(50);
      expect(screen.queryByRole("heading", pageHeading(titleKey))).toBeNull();
      expect(restrictedRequests()).toEqual([]);
      expect(mocks.toastError).not.toHaveBeenCalled();
      expect(probe.location.pathname).toBe(path);

      settleAuthTransition();
      expect(await screen.findByRole("heading", pageHeading(titleKey))).toBeInTheDocument();
      await waitFor(() => expect(restrictedRequests().length).toBeGreaterThan(0));
      await settle(50);
      expect(mocks.toastError).not.toHaveBeenCalled();
      expect(mocks.toastInfo).not.toHaveBeenCalled();
      expect(probe.location.pathname).toBe(path);
    });

    it("unmounts admitted content during a same-token transition and reloads after it settles", async () => {
      render(shellTree([path]));
      expect(await screen.findByRole("heading", pageHeading(titleKey))).toBeInTheDocument();
      await waitFor(() => expect(restrictedRequests().length).toBeGreaterThan(0));
      await settle(50);
      const admittedRequests = restrictedRequests().length;

      enterAuthTransition();
      expect(screen.queryByRole("heading", pageHeading(titleKey))).toBeNull();
      await settle(50);
      expect(restrictedRequests()).toHaveLength(admittedRequests);
      expect(mocks.toastError).not.toHaveBeenCalled();

      // An aborted activation keeps the same token; the settled admin must still reload.
      settleAuthTransition();
      expect(await screen.findByRole("heading", pageHeading(titleKey))).toBeInTheDocument();
      await waitFor(() => expect(restrictedRequests().length).toBeGreaterThan(admittedRequests));
      await settle(50);
      expect(mocks.toastError).not.toHaveBeenCalled();
      expect(mocks.toastInfo).not.toHaveBeenCalled();
      expect(probe.location.pathname).toBe(path);
    });
  });
});
