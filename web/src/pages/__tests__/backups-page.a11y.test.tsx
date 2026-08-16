import "@testing-library/jest-dom/vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  afterAll,
  afterEach,
  beforeAll,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { MemoryRouter, Navigate, Route, Routes } from "react-router-dom";
import { http, HttpResponse } from "msw";

import {
  backupAssetsFixtureIds,
  createBackupAssetsHandlers,
  type BackupAssetsFixtureScenario,
} from "@/features/backup-assets/__tests__/handlers";
import { BACKUP_ASSETS_PREFERENCES_KEY } from "@/features/backup-assets/backup-assets-preferences";
import { runAxe } from "@/test/a11y-helpers";
import { server } from "@/test/mocks/server";

const authRef = vi.hoisted(() => ({
  current: {
    token: "synthetic-test-token" as string | null,
    role: "admin" as const,
    ensureStepUpProof: vi.fn(),
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("recharts", () => ({
  Area: () => null,
  Bar: () => null,
  BarChart: ({ children }: { children: React.ReactNode }) => <svg>{children}</svg>,
  CartesianGrid: () => null,
  ComposedChart: ({ children }: { children: React.ReactNode }) => <svg>{children}</svg>,
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

vi.mock("@/components/storage-guide-card", () => ({
  StorageGuideCard: () => (
    <section aria-label="Synthetic storage guide">
      <h3>Synthetic storage guide</h3>
    </section>
  ),
}));

import { BackupsDataPage } from "../backups-page.data";
import { BackupsOverviewPage } from "../backups-page.overview";
import { BackupsRecoveryPage } from "../backups-page.recovery";
import { BackupsPage } from "../backups-page";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
  Object.defineProperties(HTMLElement.prototype, {
    getBoundingClientRect: {
      configurable: true,
      value: () =>
        ({
          x: 0,
          y: 0,
          top: 0,
          left: 0,
          right: 960,
          bottom: 560,
          width: 960,
          height: 560,
          toJSON: () => ({}),
        }) as DOMRect,
    },
    offsetHeight: { configurable: true, get: () => 560 },
    offsetWidth: { configurable: true, get: () => 960 },
    clientHeight: { configurable: true, get: () => 560 },
    clientWidth: { configurable: true, get: () => 960 },
  });
});

afterEach(() => {
  cleanup();
  server.resetHandlers();
  authRef.current.ensureStepUpProof.mockReset();
  window.localStorage.removeItem(BACKUP_ASSETS_PREFERENCES_KEY);
  document.documentElement.style.removeProperty("font-size");
});

afterAll(() => server.close());

describe("Backups routes accessibility", () => {
  it("keeps overview and recovery route panels axe-clean", async () => {
    useFixture("complete");
    setViewport(1440);
    const overview = renderBackups("/app/backups/overview");

    expect(await screen.findByText(/Backup Confidence|备份可信度/)).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Overview|概览/ })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    expect(await runAxe(overview.container)).toHaveNoViolations();

    cleanup();
    const recovery = renderBackups("/app/backups/recovery?taskId=71");
    expect(await screen.findByRole("heading", { name: /Recovery evidence|恢复证据/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Export|导出|Start recovery|开始恢复/ })).not.toBeInTheDocument();
    expect(await runAxe(recovery.container)).toHaveNoViolations();
  });

  it.each([1440, 1200, 390] as const)(
    "keeps the durable recovery wizard axe-clean at 200%% zoom and %ipx",
    async (width) => {
      useFixture("complete");
      setViewport(width);
      document.documentElement.style.fontSize = "200%";
      const user = userEvent.setup();
      const planId = "1".repeat(32);
      server.use(http.get(`/api/v1/recovery-plans/${planId}`, () => HttpResponse.json({
        code: 0,
        message: "ok",
        data: {
          schema_version: 1,
          id: planId,
          state: "preflight_ready",
          revision: "8",
          repository_id: backupAssetsFixtureIds.onlineRepository,
          recovery_point_id: backupAssetsFixtureIds.onlineRecoveryPoint,
          target_mode: "isolated",
          target_node_id: 4,
          target_root_id: "recovery-root",
          conflict_policy: "fail_on_conflict",
          security_decision: "allow_clean",
          selection_digest: "a".repeat(64),
          operation_set_digest: "b".repeat(64),
          delete_set_digest: "c".repeat(64),
          estimated_items: 2,
          estimated_bytes: 4096,
          created_at: "2026-08-16T01:00:00Z",
          updated_at: "2026-08-16T01:01:00Z",
        },
      })));

      renderBackups(
        `/app/backups/recovery?recoveryPointId=${backupAssetsFixtureIds.onlineRecoveryPoint}&planId=${planId}`,
      );

      const phaseHeading = await screen.findByRole("heading", { name: /Run recovery preflight|运行恢复预检/ });
      await waitFor(() => expect(phaseHeading).toHaveFocus());
      const dialog = screen.getByRole("dialog", { name: /Recover backup assets|恢复备份资产/ });
      const quietAnnouncement = screen.getByTestId("recovery-announcement");
      expect(quietAnnouncement).toHaveAttribute("aria-live", "polite");
      expect(quietAnnouncement).toHaveTextContent("");
      expect(document.body).not.toHaveTextContent(/grant_secret|step-up proof|write proof|delete proof/i);
      expect(JSON.stringify(browserChannels())).not.toMatch(/grant_secret|step-up proof|write proof|delete proof|ticket/i);
      await user.keyboard("{Tab}");
      expect(dialog).toContainElement(document.activeElement as HTMLElement);
      expect(await runAxe(document.body)).toHaveNoViolations();
    },
  );

  it("renders an axe-clean desktop workspace with tabs, directory tree, list, and grid semantics", async () => {
    useFixture("complete");
    setViewport(1440);
    const user = userEvent.setup();
    const page = renderBackups(completeRoute());

    expect(await screen.findByRole("listbox", { name: /Asset list|资产列表/ })).toBeInTheDocument();
    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getAllByRole("option").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("tabpanel", { hidden: true })).toHaveLength(3);
    expect(await runAxe(page.container)).toHaveNoViolations();

    await user.click(screen.getByRole("radio", { name: /Grid|网格/ }));
    expect(await screen.findByRole("grid", { name: /Asset grid|资产网格/ })).toBeInTheDocument();
    expect(screen.getAllByRole("gridcell").length).toBeGreaterThan(0);
    expect(await runAxe(page.container)).toHaveNoViolations();
  });

  it("announces partial offline coverage without claiming an authoritative empty result", async () => {
    useFixture("partial_offline");
    setViewport(1440);
    const page = renderBackups(
      `/app/backups/data?repositoryId=${backupAssetsFixtureIds.offlineRepository}` +
        `&recoveryPointId=${backupAssetsFixtureIds.offlineRecoveryPoint}`
    );

    expect(
      await screen.findByText(/Partial catalog coverage|目录覆盖不完整/, {}, { timeout: 3_000 })
    ).toBeInTheDocument();
    expect(screen.getAllByText(/Offline|离线/).length).toBeGreaterThan(0);
    expect(screen.queryByText(/No matching assets|没有匹配的资产/)).not.toBeInTheDocument();
    expect(await runAxe(page.container)).toHaveNoViolations();
  });

  it("scans an open overlay portal and returns focus to the invoking control", async () => {
    useFixture("complete");
    setViewport(1440);
    const user = userEvent.setup();
    renderBackups(completeRoute());

    await screen.findByRole("listbox", { name: /Asset list|资产列表/ });
    const trigger = screen.getByRole("button", { name: /Favorites.*0|收藏.*0/ });
    await user.click(trigger);
    expect(await screen.findByRole("dialog", { name: /Favorites|收藏/ })).toBeInTheDocument();
    expect(await screen.findByText("Synthetic investigation target")).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("keeps query, selection, fixture names, tickets, and proofs out of browser channels", async () => {
    useFixture("complete");
    setViewport(1440);
    const user = userEvent.setup();
    const before = browserChannels();
    renderBackups(completeRoute());

    await screen.findByRole("listbox", { name: /Asset list|资产列表/ });
    const temporaryQuery = "synthetic-memory-only-investigation";
    await user.type(
      screen.getByRole("searchbox", { name: /Search backup assets|搜索备份资产/ }),
      temporaryQuery
    );
    await user.click(within(screen.getByRole("listbox")).getAllByRole("option")[0]);
    await user.click(screen.getByRole("button", { name: /Export selected|导出所选/ }));
    expect(await screen.findByRole("dialog", { name: /Export backup assets|导出备份资产/ })).toBeInTheDocument();

    expect(browserChannels()).toEqual(before);
    const serialized = JSON.stringify(browserChannels());
    expect(serialized).not.toContain(temporaryQuery);
    expect(serialized).not.toContain("synthetic-service-config-with-a-deliberately-long-name");
    expect(serialized).not.toContain("/api/v1/asset-content/");
    expect(serialized).not.toContain("proof");
  });

  it.each([
    [1440, "desktop"],
    [1200, "intermediate"],
    [390, "mobile"],
  ] as const)("keeps the lazy export review axe-clean at %ipx", async (width, viewport) => {
    useFixture("complete");
    setViewport(width);
    const user = userEvent.setup();
    renderBackups(completeRoute());

    await user.click(within(await screen.findByRole("listbox")).getAllByRole("option")[0]);
    await user.click(screen.getByRole("button", { name: /Export selected|导出所选/ }));

    expect(await screen.findByRole("dialog", { name: /Export backup assets|导出备份资产/ })).toBeInTheDocument();
    expect(screen.getByTestId("backup-assets-workspace")).toHaveAttribute("data-viewport", viewport);
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("renders an axe-clean mobile full-screen inspector with focus entry and reversible controls", async () => {
    useFixture("complete");
    setViewport(375);
    renderBackups(
      `${completeRoute()}&entryId=${backupAssetsFixtureIds.primaryEntry}`
    );

    const inspector = await screen.findByTestId("backup-assets-mobile-inspector");
    const heading = await screen.findByRole("heading", {
      name: /synthetic-service-config-with-a-deliberately-long-name/,
    });
    await waitFor(() => expect(heading).toHaveFocus());
    expect(screen.queryByRole("region", { name: /Asset results|资产结果/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Close asset inspector|关闭资产检查器/ })).toBeInTheDocument();
    expect(await runAxe(inspector)).toHaveNoViolations();
  });

  it("keeps every backup-assets locale leaf in zh/en parity", async () => {
    const [{ default: zh }, { default: en }] = await Promise.all([
      import("@/i18n/locales/zh"),
      import("@/i18n/locales/en"),
    ]);

    expect(leafKeys(zh.backupAssets).sort()).toEqual(leafKeys(en.backupAssets).sort());
    expect(leafKeys(zh.backupAssets.recovery).sort()).toEqual(leafKeys(en.backupAssets.recovery).sort());
  });
});

function useFixture(scenario: BackupAssetsFixtureScenario) {
  server.use(...createBackupAssetsHandlers(scenario));
}

function renderBackups(initialEntry: string) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <main aria-label="Xirang test application">
        <Routes>
          <Route path="/app/backups" element={<BackupsPage />}>
            <Route index element={<Navigate to="overview" replace />} />
            <Route path="overview" element={<BackupsOverviewPage />} />
            <Route path="data" element={<BackupsDataPage />} />
            <Route path="recovery" element={<BackupsRecoveryPage />} />
          </Route>
        </Routes>
      </main>
    </MemoryRouter>
  );
}

function completeRoute() {
  return (
    `/app/backups/data?repositoryId=${backupAssetsFixtureIds.onlineRepository}` +
    `&recoveryPointId=${backupAssetsFixtureIds.onlineRecoveryPoint}`
  );
}

function setViewport(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  fireEvent(window, new Event("resize"));
}

function browserChannels() {
  return {
    localStorage: storageEntries(window.localStorage),
    sessionStorage: storageEntries(window.sessionStorage),
    historyState: window.history.state,
  };
}

function storageEntries(storage: Storage) {
  return Array.from({ length: storage.length }, (_, index) => storage.key(index))
    .filter((key): key is string => key !== null)
    .sort()
    .map((key) => [key, storage.getItem(key)] as const);
}

function leafKeys(value: unknown, prefix = ""): string[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return prefix ? [prefix] : [];
  }
  return Object.entries(value).flatMap(([key, child]) =>
    leafKeys(child, prefix ? `${prefix}.${key}` : key)
  );
}
