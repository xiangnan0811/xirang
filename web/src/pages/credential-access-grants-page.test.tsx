import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { CredentialAccessGrantsPage } from "./credential-access-grants-page";
import i18n, { setLanguage } from "@/i18n";
import {
  ApiError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import { mapCredentialAccessGrant, type CredentialAccessGrantListOptions } from "@/lib/api/credential-access-grants-api";
import type { CredentialAccessGrant } from "@/types/domain";
import type * as ClientModule from "@/lib/api/client";

type GrantPage = { items: CredentialAccessGrant[]; total: number; page: number; pageSize: number };
type Role = "admin" | "operator" | "viewer";

const mocks = vi.hoisted(() => ({
  auth: {
    token: "test-token" as string | null,
    role: "admin" as "admin" | "operator" | "viewer" | null,
    authTransitioning: false,
  },
  list: vi.fn<(token: string, options?: CredentialAccessGrantListOptions, requestOptions?: { signal?: AbortSignal }) => Promise<GrantPage>>(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => mocks.auth,
}));

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof ClientModule>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      listCredentialAccessGrants: mocks.list,
    },
  };
});

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

// The page is rendered at 2026-05-20T00:05:00Z; every expiry below is relative to it.
const NOW = Date.parse("2026-05-20T00:05:00Z");
const PAST = "2026-05-20T00:00:01Z";
const FUTURE = "2026-05-20T00:10:00Z";

function createMemoryStorage() {
  const store = new Map<string, string>();
  const storage = {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    get length() {
      return store.size;
    },
    snapshot: () => Array.from(store.entries()),
  };
  return storage as Storage & { snapshot: () => Array<[string, string]> };
}

function storageSnapshot(storage: Storage): string {
  const maybeSnapshot = storage as Storage & { snapshot?: () => Array<[string, string]> };
  if (typeof maybeSnapshot.snapshot === "function") {
    return JSON.stringify(maybeSnapshot.snapshot());
  }
  return JSON.stringify(Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index) ?? "";
    return [key, storage.getItem(key) ?? ""];
  }));
}

// Rows go through the real wire mapper so status and expiry evidence match production parsing.
function grant(id: number, overrides: Record<string, unknown> = {}): CredentialAccessGrant {
  return mapCredentialAccessGrant({
    id,
    requester_user_id: 7,
    requester_username: `user-${id}`,
    requester_role: "admin",
    action: "task.restore_trigger",
    purpose: "task_restore",
    task_id: 102,
    reason: "例行恢复",
    status: "active",
    requested_ttl_seconds: 600,
    requested_at: "2026-05-20T00:00:00Z",
    approved_at: "2026-05-20T00:00:01Z",
    approver_user_id: 7,
    approver_username: "approver-7",
    expires_at: FUTURE,
    created_at: "2026-05-20T00:00:00Z",
    updated_at: "2026-05-20T00:00:00Z",
    ...overrides,
  });
}

function page(items: CredentialAccessGrant[], current = 1, total = items.length): GrantPage {
  return { items, page: current, pageSize: 30, total };
}

function deferred() {
  let resolve!: (data: GrantPage) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<GrantPage>((ok, fail) => {
    resolve = ok;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function httpError(status: number, message: string, httpStatus?: number): ApiError {
  const error = new ApiError(status, message);
  if (httpStatus !== undefined) error.httpStatus = httpStatus;
  return error;
}

function setAuth(next: { token: string | null; role: Role | null; authTransitioning?: boolean }, bump = true) {
  mocks.auth.token = next.token;
  mocks.auth.role = next.role;
  mocks.auth.authTransitioning = next.authTransitioning ?? false;
  rememberAuthIdentity(next.token, next.role);
  if (bump) bumpAuthSessionGeneration();
}

function surface() {
  return (
    <MemoryRouter initialEntries={["/app/credential-access-grants"]}>
      <Routes>
        <Route path="/app/credential-access-grants" element={<CredentialAccessGrantsPage />} />
        <Route path="/app/overview" element={<p>Overview destination</p>} />
      </Routes>
    </MemoryRouter>
  );
}

function renderPage() {
  const view = render(surface());
  return { ...view, rerenderPage: () => view.rerender(surface()) };
}

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
  document.dispatchEvent(new Event("visibilitychange"));
}

function table() {
  return screen.getByRole("table", { hidden: true });
}

function cards() {
  return screen.getByRole("list", { name: i18n.t("credentialGrants.pageTitle"), hidden: true });
}

function desktopRow(username: string): HTMLTableRowElement {
  const row = within(table()).getByText(username).closest("tr");
  if (!(row instanceof HTMLTableRowElement)) throw new Error(`missing desktop row ${username}`);
  return row;
}

function mobileCard(username: string): HTMLLIElement {
  const card = within(cards()).getByText(username).closest("li");
  if (!(card instanceof HTMLLIElement)) throw new Error(`missing mobile card ${username}`);
  return card;
}

function expiryCell(username: string): HTMLTableCellElement {
  const headers = within(table()).getAllByRole("columnheader", { hidden: true });
  const index = headers.findIndex((header) => header.textContent === i18n.t("credentialGrants.colExpiresAt"));
  const cell = desktopRow(username).cells[index];
  if (!cell) throw new Error(`missing expiry cell ${username}`);
  return cell;
}

function statusSelect() {
  return screen.getByRole("combobox", { name: i18n.t("credentialGrants.statusFilter") }) as HTMLSelectElement;
}

function detailValue(label: string): HTMLElement {
  const term = within(screen.getByRole("dialog")).getByText(label, { selector: "dt" });
  const value = term.nextElementSibling;
  if (!(value instanceof HTMLElement)) throw new Error(`missing detail ${label}`);
  return value;
}

function lastOptions(): CredentialAccessGrantListOptions {
  const options = mocks.list.mock.lastCall?.[1];
  if (!options) throw new Error("missing list options");
  return options;
}

async function openDetail(user: UserEvent, username: string) {
  await user.click(within(desktopRow(username)).getByRole("button", { name: i18n.t("credentialGrants.viewDetails") }));
  return screen.findByRole("dialog");
}

// Each scope must keep the stored status label and a separate expiry indication.
function expectRecord(username: string, statusLabel: string, expiryLabel: string) {
  for (const scope of [desktopRow(username), mobileCard(username)]) {
    expect(within(scope).getByText(statusLabel)).toBeInTheDocument();
    expect(within(scope).getByText(expiryLabel)).toBeInTheDocument();
    expect(within(scope).queryByText("success")).not.toBeInTheDocument();
  }
}

function expectNoFeedback() {
  expect(mocks.toastError).not.toHaveBeenCalled();
  expect(mocks.toastSuccess).not.toHaveBeenCalled();
}

describe("CredentialAccessGrantsPage", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    Object.defineProperty(window, "localStorage", { configurable: true, value: createMemoryStorage() });
    Object.defineProperty(window, "sessionStorage", { configurable: true, value: createMemoryStorage() });
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    setAuth({ token: "test-token", role: "admin" });
    mocks.list.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.list.mockResolvedValue(page([grant(1)]));
  });

  afterEach(async () => {
    vi.useRealTimers();
    setHidden(false);
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
    if (i18n.language !== "zh") {
      await act(async () => {
        await setLanguage("zh");
      });
    }
  });

  it("筛选参数变更后会带入查询请求，连续输入不丢焦点", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("本页 1 条");

    mocks.list.mockClear();
    const username = screen.getByPlaceholderText("按申请人过滤");
    await user.type(username, "  admin  ");
    expect(username).toHaveFocus();
    expect(username).toHaveValue("  admin  ");
    await user.selectOptions(screen.getByLabelText("按申请人角色过滤凭据临时授权"), "admin");
    await user.type(screen.getByPlaceholderText("申请人用户 ID"), "7");
    await user.selectOptions(screen.getByLabelText("按授权动作过滤"), "batch_command.create");
    await user.selectOptions(screen.getByLabelText("按授权用途过滤"), "batch_command");
    await user.selectOptions(statusSelect(), "revoked");
    await user.type(screen.getByPlaceholderText("节点 ID"), "10");
    await user.type(screen.getByPlaceholderText("任务 ID"), "102");
    await user.type(screen.getByPlaceholderText("策略 ID"), "50");

    await screen.findByText("本页 1 条");
    expect(mocks.list).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({
        requesterUsername: "admin",
        requesterRole: "admin",
        requesterUserId: 7,
        action: "batch_command.create",
        purpose: "batch_command",
        status: "revoked",
        nodeId: 10,
        taskId: 102,
        policyId: 50,
        page: 1,
        pageSize: 30,
        sortBy: "created_at",
        sortOrder: "desc",
      }),
      expect.anything(),
    );
  });

  it("分页使用服务器页码与总数，翻页等待期间不保留旧记录", async () => {
    const user = userEvent.setup();
    const second = deferred();
    mocks.list.mockResolvedValueOnce(page([grant(1)], 1, 60)).mockImplementationOnce(() => second.promise);

    renderPage();
    expect(await screen.findByText("第 1 页 · 共 60 条")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(mocks.list).toHaveBeenLastCalledWith("test-token", expect.objectContaining({ page: 2, pageSize: 30 }), expect.anything());
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryAllByText("user-1")).toHaveLength(0);
    expect(screen.queryAllByText(/第 \d+ 页/)).toHaveLength(0);
    expect(screen.getByRole("button", { name: "查询" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "刷新" })).toBeDisabled();

    await act(async () => {
      second.resolve(page([grant(31, { status: "revoked" })], 2, 60));
    });
    expect(screen.getByText("第 2 页 · 共 60 条")).toBeInTheDocument();
    expect(screen.getByText("本页 1 条")).toBeInTheDocument();
    expect(screen.getByText("筛选结果 60 条")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "查询" })).toBeEnabled();
  });

  it("详情只展示安全只读字段且不提供变更操作", async () => {
    const user = userEvent.setup();
    mocks.list.mockResolvedValue(page([grant(1, { reason: "例行恢复", node_id: 10, task_id: 102, policy_id: 50 })]));

    renderPage();
    await screen.findByText("本页 1 条");
    const dialog = await openDetail(user, "user-1");

    expect(within(dialog).getByText("凭据临时授权详情")).toBeInTheDocument();
    expect(within(dialog).getByText("授权 ID")).toBeInTheDocument();
    expect(within(dialog).getByText("例行恢复")).toBeInTheDocument();
    expect(within(dialog).getByText(/自批准/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /批准|拒绝|撤销|刷新|重试/ })).not.toBeInTheDocument();
    expect(screen.queryAllByText(/step-up-marker|auth-marker|credential-material-marker|path-marker/)).toHaveLength(0);
  });

  it("不会把授权行或原因落入浏览器存储", async () => {
    renderPage();
    await screen.findByText("本页 1 条");

    const browserStorage = JSON.stringify({
      local: storageSnapshot(window.localStorage),
      session: storageSnapshot(window.sessionStorage),
    });
    expect(browserStorage).not.toContain("例行恢复");
    expect(browserStorage).not.toContain("task.restore_trigger");
    expect(browserStorage).not.toContain("test-token");
    expect(browserStorage).not.toContain("102");
  });

  it("批量命令重试是独立筛选，未知动作不能当成创建或重试", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("本页 1 条");

    const filter = screen.getByLabelText("按授权动作过滤") as HTMLSelectElement;
    const options = Array.from(filter.options);
    const retry = options.find((option) => option.value === "batch_command.retry");
    const create = options.find((option) => option.value === "batch_command.create");
    const batchTrigger = options.find((option) => option.value === "task.batch_trigger");

    expect(retry).toBeDefined();
    expect(create).toBeDefined();
    expect(batchTrigger).toBeDefined();
    expect(retry?.label).not.toBe(create?.label);
    expect(batchTrigger?.label).not.toBe(retry?.label);
    expect(options.some((option) => option.value === "batch_command")).toBe(false);
    expect(options.some((option) => option.value === "batch_command.retry.extra")).toBe(false);

    mocks.list.mockClear();
    await user.selectOptions(filter, "batch_command.retry");
    await screen.findByText("本页 1 条");

    expect(mocks.list).toHaveBeenLastCalledWith(
      "test-token",
      expect.objectContaining({ action: "batch_command.retry" }),
      expect.anything(),
    );
    expect(lastOptions().action).not.toBe("batch_command.create");
  });

  describe("状态保真", () => {
    it("六种存储状态原样显示，未知、空、缺失状态显示为未知且不冒充过期或有效", async () => {
      mocks.list.mockResolvedValue(page([
        grant(1, { status: "requested" }),
        grant(2, { status: "approved" }),
        grant(3, { status: "active" }),
        grant(4, { status: "denied" }),
        grant(5, { status: "expired" }),
        grant(6, { status: "revoked" }),
        grant(7, { status: "future_state" }),
        grant(8, { status: null }),
        grant(9, { status: undefined }),
      ]));

      renderPage();
      await screen.findByText("本页 9 条");

      expectRecord("user-1", "已申请", "尚未到期");
      expectRecord("user-2", "已批准（记录）", "尚未到期");
      expectRecord("user-3", "已激活（记录）", "尚未到期");
      expectRecord("user-4", "已拒绝", "尚未到期");
      expectRecord("user-5", "已过期", "尚未到期");
      expectRecord("user-6", "已撤销", "尚未到期");
      for (const username of ["user-7", "user-8", "user-9"]) {
        expectRecord(username, "未知状态", "尚未到期");
        expect(within(desktopRow(username)).queryByText("已过期")).not.toBeInTheDocument();
        expect(within(mobileCard(username)).queryByText("已过期")).not.toBeInTheDocument();
      }
      expect(screen.queryAllByText("future_state")).toHaveLength(0);
      expect(screen.queryAllByText("success")).toHaveLength(0);
      expect(within(desktopRow("user-4")).getByText("destructive")).toBeInTheDocument();
      expect(within(desktopRow("user-6")).getByText("destructive")).toBeInTheDocument();
    });

    it("到期提示来自严格解析的原始时间：小数秒、等价时区、等于现在、过去与非法时间", async () => {
      mocks.list.mockResolvedValue(page([
        grant(11, { expires_at: "2026-05-20T00:05:00.001Z" }),
        grant(12, { expires_at: "2026-05-20T08:05:00+08:00" }),
        grant(13, { expires_at: "2026-05-20T00:05:00Z" }),
        grant(14, { expires_at: "2026-05-20T00:04:59.999Z" }),
        grant(15, { expires_at: "2026-05-20T08:10:00+08:00" }),
        grant(16, { expires_at: "2026-05-20T00:10:00Z" }),
        grant(17, { expires_at: "" }),
        grant(18, { expires_at: null }),
        grant(19, { expires_at: 1779235500000 }),
        grant(20, { expires_at: "2026-05-20T00:10:00" }),
        grant(21, { expires_at: "not-a-date-marker" }),
        grant(22, { expires_at: "2026-13-45T25:00:00Z" }),
      ]));

      renderPage();
      await screen.findByText("本页 12 条");

      expectRecord("user-11", "已激活（记录）", "尚未到期");
      expectRecord("user-12", "已激活（记录）", "已到期");
      expectRecord("user-13", "已激活（记录）", "已到期");
      expectRecord("user-14", "已激活（记录）", "已到期");
      expectRecord("user-15", "已激活（记录）", "尚未到期");
      expectRecord("user-16", "已激活（记录）", "尚未到期");
      expect(expiryCell("user-12").textContent).toBe(expiryCell("user-13").textContent);
      expect(expiryCell("user-15").textContent).toBe(expiryCell("user-16").textContent);

      for (const username of ["user-17", "user-18", "user-19", "user-20", "user-21", "user-22"]) {
        expectRecord(username, "已激活（记录）", "到期时间未知");
        expect(within(expiryCell(username)).getByText("-")).toBeInTheDocument();
      }
      expect(screen.queryAllByText(/not-a-date-marker|2026-13-45|1779235500000/)).toHaveLength(0);
    });

    it("记录状态与到期提示分开展示，详情给出状态解释并保留原审批者", async () => {
      const user = userEvent.setup();
      mocks.list.mockResolvedValue(page([
        grant(31, { status: "active", expires_at: PAST }),
        grant(32, { status: "approved", expires_at: PAST }),
        grant(33, { status: "approved", expires_at: FUTURE, approver_user_id: null, approver_username: null }),
        grant(34, { status: "requested", expires_at: FUTURE }),
        grant(35, { status: "denied", expires_at: FUTURE }),
        grant(36, { status: "revoked", expires_at: FUTURE }),
        grant(37, { status: "future_state", expires_at: FUTURE }),
        grant(38, { status: "future_state", expires_at: PAST }),
        grant(39, { status: "expired", expires_at: FUTURE }),
      ]));

      renderPage();
      await screen.findByText("本页 9 条");

      expectRecord("user-31", "已激活（记录）", "已到期");
      expectRecord("user-32", "已批准（记录）", "已到期");
      expectRecord("user-33", "已批准（记录）", "尚未到期");
      expectRecord("user-34", "已申请", "尚未到期");
      expectRecord("user-35", "已拒绝", "尚未到期");
      expectRecord("user-36", "已撤销", "尚未到期");
      expectRecord("user-37", "未知状态", "尚未到期");
      expectRecord("user-38", "未知状态", "已到期");
      expectRecord("user-39", "已过期", "尚未到期");
      for (const username of ["user-32", "user-33"]) {
        expect(within(desktopRow(username)).queryByText("已激活（记录）")).not.toBeInTheDocument();
      }
      expect(screen.queryAllByText("success")).toHaveLength(0);

      const cases: Array<[string, string, string, string]> = [
        ["user-31", "已激活（记录）", "记录状态为已激活。到期时间及实际操作的服务器校验共同决定是否可用。", "已到期"],
        ["user-32", "已批准（记录）", "保留的批准状态；不是本页的审批待办。是否可用仍由服务器校验。", "已到期"],
        ["user-33", "已批准（记录）", "保留的批准状态；不是本页的审批待办。是否可用仍由服务器校验。", "尚未到期"],
        ["user-34", "已申请", "保留的申请记录；不表示当前产品有待管理员处理的审批流程。", "尚未到期"],
        ["user-35", "已拒绝", "保留的拒绝记录，本页只读展示。", "尚未到期"],
        ["user-36", "已撤销", "保留的撤销记录，本页不提供撤销操作。", "尚未到期"],
        ["user-37", "未知状态", "无法识别该记录状态，不将其视为有效或已过期。", "尚未到期"],
        ["user-38", "未知状态", "无法识别该记录状态，不将其视为有效或已过期。", "已到期"],
        ["user-39", "已过期", "记录已标记过期；与根据到期时间计算的提示分别展示。", "尚未到期"],
      ];
      for (const [username, statusLabel, description, expiryLabel] of cases) {
        await openDetail(user, username);
        const status = detailValue("记录状态");
        expect(within(status).getByText(statusLabel)).toBeInTheDocument();
        expect(within(status).getByText(description)).toBeInTheDocument();
        expect(within(detailValue("到期时间")).getByText(expiryLabel)).toBeInTheDocument();
        if (username === "user-33") {
          expect(detailValue("批准人")).toHaveTextContent(/^-$/);
          expect(detailValue("批准人用户 ID")).toHaveTextContent(/^-$/);
        } else {
          expect(detailValue("批准人")).toHaveTextContent(/^approver-7$/);
        }
        await user.keyboard("{Escape}");
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      }
    });
  });

  describe("时间变化", () => {
    it("可见时每秒推进本地时钟，隐藏时暂停、恢复立即修正，不重新请求，卸载后无计时器", async () => {
      vi.useRealTimers();
      vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
      vi.setSystemTime(NOW);
      mocks.list.mockResolvedValue(page([
        grant(41, { expires_at: "2026-05-20T00:05:00.500Z" }),
        grant(42, { expires_at: "2026-05-20T00:30:00Z" }),
        grant(43, { expires_at: "2026-05-20T02:00:00Z" }),
      ]));

      const view = renderPage();
      await screen.findByText("本页 3 条");
      fireEvent.click(within(desktopRow("user-41")).getByRole("button", { name: "查看详情" }));
      const dialog = screen.getByRole("dialog");
      expectRecord("user-41", "已激活（记录）", "尚未到期");
      expect(within(dialog).getByText("尚未到期")).toBeInTheDocument();

      act(() => {
        vi.advanceTimersByTime(1000);
      });
      expectRecord("user-41", "已激活（记录）", "已到期");
      expect(within(dialog).getByText("已到期")).toBeInTheDocument();
      expect(within(dialog).queryByText("尚未到期")).not.toBeInTheDocument();
      expectRecord("user-42", "已激活（记录）", "尚未到期");

      act(() => {
        setHidden(true);
      });
      act(() => {
        vi.advanceTimersByTime(30 * 60 * 1000);
      });
      expectRecord("user-42", "已激活（记录）", "尚未到期");
      act(() => {
        setHidden(false);
      });
      expectRecord("user-42", "已激活（记录）", "已到期");
      expectRecord("user-43", "已激活（记录）", "尚未到期");

      expect(mocks.list).toHaveBeenCalledTimes(1);
      expect(vi.getTimerCount()).toBe(1);
      view.unmount();
      expect(vi.getTimerCount()).toBe(0);
      act(() => {
        vi.advanceTimersByTime(5000);
      });
      expect(mocks.list).toHaveBeenCalledTimes(1);
    });

    it("没有未来到期时间时不启动本地时钟", async () => {
      vi.useRealTimers();
      vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
      vi.setSystemTime(NOW);
      mocks.list.mockResolvedValue(page([grant(44, { expires_at: PAST }), grant(45, { expires_at: null })]));

      renderPage();
      await screen.findByText("本页 2 条");
      expect(vi.getTimerCount()).toBe(0);
    });
  });

  describe("筛选及统计", () => {
    const firstPage = Array.from({ length: 30 }, (_, index) => grant(index + 101, { status: "requested" }));
    const secondPage = Array.from({ length: 5 }, (_, index) => grant(index + 131, { status: "requested" }));

    function pagedImplementation(byStatus: Partial<Record<string, GrantPage>> = {}) {
      return async (_token: string, options?: CredentialAccessGrantListOptions) => {
        const status = options?.status;
        if (status && byStatus[status]) return byStatus[status]!;
        return options?.page === 2 ? page(secondPage, 2, 35) : page(firstPage, 1, 35);
      };
    }

    it("默认全部记录状态与近24小时创建时间，统计本页条数与服务端筛选总数", async () => {
      const user = userEvent.setup();
      mocks.list.mockImplementation(pagedImplementation());

      renderPage();
      expect(await screen.findByText("本页 30 条")).toBeInTheDocument();
      expect(screen.getByText("筛选结果 35 条")).toBeInTheDocument();
      expect(screen.getByText("第 1 页 · 共 35 条")).toBeInTheDocument();
      expect(screen.queryAllByText(/待处理|有效 \d|非有效/)).toHaveLength(0);
      expect(statusSelect()).toHaveValue("all");

      const first = mocks.list.mock.calls[0]?.[1];
      expect(first).toMatchObject({ page: 1, pageSize: 30, sortBy: "created_at", sortOrder: "desc" });
      expect(first?.status).toBeUndefined();
      expect(Date.parse(first?.to ?? "")).toBe(NOW);
      expect(Date.parse(first?.to ?? "") - Date.parse(first?.from ?? "")).toBe(24 * 60 * 60 * 1000);

      await user.click(screen.getByRole("button", { name: "下一页" }));
      expect(await screen.findByText("本页 5 条")).toBeInTheDocument();
      expect(screen.getByText("筛选结果 35 条")).toBeInTheDocument();
      expect(screen.getByText("第 2 页 · 共 35 条")).toBeInTheDocument();
    });

    it("状态筛选只提供存储状态，历史状态位于单独分组且没有未知查询值", async () => {
      renderPage();
      await screen.findByText("本页 1 条");

      const select = statusSelect();
      expect(Array.from(select.children).filter((node) => node instanceof HTMLOptionElement).map((node) => (node as HTMLOptionElement).value))
        .toEqual(["all", "active", "expired"]);
      const group = select.querySelector("optgroup");
      expect(group).toHaveAttribute("label", "其他/历史记录状态");
      expect(Array.from(group?.querySelectorAll("option") ?? []).map((option) => option.value))
        .toEqual(["requested", "approved", "denied", "revoked"]);
      expect(Array.from(select.options).map((option) => option.label)).toEqual([
        "全部记录状态", "已激活（记录）", "已过期", "已申请", "已批准（记录）", "已拒绝", "已撤销",
      ]);
      expect(Array.from(select.options).some((option) => option.value === "unknown")).toBe(false);
      expect(select).toHaveAccessibleDescription("筛选按记录状态匹配，到期提示按浏览器时间计算。未到期不代表操作已获准，实际操作仍由服务器校验。");
      expect(screen.getByText("时间范围按创建时间过滤；查看旧记录请选择“全部”。")).toBeInTheDocument();
    });

    it.each([
      ["requested", "已申请"],
      ["approved", "已批准（记录）"],
      ["denied", "已拒绝"],
      ["revoked", "已撤销"],
    ] as const)("历史状态 %s 发送原值、读取对应记录并从第二页回到第一页", async (status, label) => {
      const user = userEvent.setup();
      mocks.list.mockImplementation(pagedImplementation({ [status]: page([grant(201, { status })], 1, 1) }));

      renderPage();
      await screen.findByText("本页 30 条");
      await user.click(screen.getByRole("button", { name: "下一页" }));
      await screen.findByText("第 2 页 · 共 35 条");

      await user.selectOptions(statusSelect(), status);
      await screen.findByText("user-201", { selector: "td" });
      expect(lastOptions()).toMatchObject({ status, page: 1 });
      expectRecord("user-201", label, "尚未到期");
      expect(screen.getByText("本页 1 条")).toBeInTheDocument();
      expect(screen.getByText("筛选结果 1 条")).toBeInTheDocument();
    });

    it("active 筛选保留已到期的存储 active 行，不在客户端删行扭曲分页", async () => {
      const user = userEvent.setup();
      mocks.list.mockImplementation(pagedImplementation({
        active: page([grant(301, { status: "active", expires_at: PAST }), grant(302, { status: "active", expires_at: FUTURE })], 1, 32),
        expired: page([grant(303, { status: "expired", expires_at: FUTURE })], 1, 1),
      }));

      renderPage();
      await screen.findByText("本页 30 条");
      await user.selectOptions(statusSelect(), "active");
      await screen.findByText("user-301", { selector: "td" });

      expect(lastOptions()).toMatchObject({ status: "active", page: 1 });
      expectRecord("user-301", "已激活（记录）", "已到期");
      expectRecord("user-302", "已激活（记录）", "尚未到期");
      expect(within(table()).getAllByRole("row")).toHaveLength(3);
      expect(screen.getByText("本页 2 条")).toBeInTheDocument();
      expect(screen.getByText("筛选结果 32 条")).toBeInTheDocument();
      expect(screen.getByText("第 1 页 · 共 32 条")).toBeInTheDocument();

      await user.selectOptions(statusSelect(), "expired");
      await screen.findByText("user-303", { selector: "td" });
      expect(lastOptions()).toMatchObject({ status: "expired", page: 1 });
      expectRecord("user-303", "已过期", "尚未到期");
    });

    it("只有成功的空结果才显示空态", async () => {
      mocks.list.mockResolvedValue(page([], 1, 0));
      renderPage();

      expect(await screen.findByText("当前筛选条件下没有凭据临时授权。")).toBeInTheDocument();
      expect(screen.getByText("本页 0 条")).toBeInTheDocument();
      expect(screen.getByText("筛选结果 0 条")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });

  describe("失败与迟到结果", () => {
    it.each(["resolve", "reject"] as const)("被替换的筛选查询迟到 %s 时不覆盖当前列表、统计、详情或错误", async (outcome) => {
      const user = userEvent.setup();
      const stale = deferred();
      mocks.list
        .mockResolvedValueOnce(page([grant(1)], 1, 1))
        .mockImplementationOnce(() => stale.promise)
        .mockResolvedValueOnce(page([grant(2, { status: "denied" })], 1, 7));

      renderPage();
      await screen.findByText("user-1", { selector: "td" });
      await user.selectOptions(statusSelect(), "revoked");
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryAllByText("user-1")).toHaveLength(0);
      expect(screen.queryAllByText(/本页 \d+ 条/)).toHaveLength(0);
      expect(mocks.list.mock.calls[1]?.[2]?.signal).toBeInstanceOf(AbortSignal);

      await user.selectOptions(statusSelect(), "denied");
      await screen.findByText("user-2", { selector: "td" });
      await openDetail(user, "user-2");

      await act(async () => {
        if (outcome === "resolve") stale.resolve(page([grant(9, { status: "revoked" })], 1, 99));
        else stale.reject(httpError(503, "stale-raw-marker", 503));
      });

      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(detailValue(i18n.t("credentialGrants.colStatus"))).toHaveTextContent("已拒绝");
      expect(screen.queryAllByText("user-9")).toHaveLength(0);
      expect(screen.getByText("筛选结果 7 条")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(screen.queryAllByText(/stale-raw-marker/)).toHaveLength(0);
      expectNoFeedback();
    });

    it("刷新等待时旧详情已关闭，被替换的旧刷新不能结束当前加载", async () => {
      const user = userEvent.setup();
      const refresh = deferred();
      const filtered = deferred();
      mocks.list
        .mockResolvedValueOnce(page([grant(1)], 1, 60))
        .mockImplementationOnce(() => refresh.promise)
        .mockImplementationOnce(() => filtered.promise);

      renderPage();
      await screen.findByText("user-1", { selector: "td" });
      await openDetail(user, "user-1");
      // Programmatic activation while the modal is open: the new query still owns the detail state.
      fireEvent.click(screen.getByRole("button", { name: "刷新", hidden: true }));

      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(mocks.list).toHaveBeenCalledTimes(2);
      expect(lastOptions()).toMatchObject({ page: 1 });
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryAllByText("user-1")).toHaveLength(0);

      await user.selectOptions(statusSelect(), "expired");
      expect(mocks.list).toHaveBeenCalledTimes(3);
      await act(async () => {
        refresh.resolve(page([grant(5)], 1, 60));
      });
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryAllByText("user-5")).toHaveLength(0);
      expect(screen.getByRole("button", { name: "刷新" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "查询" })).toBeDisabled();

      await act(async () => {
        filtered.resolve(page([grant(7, { status: "expired" })], 1, 1));
      });
      expect(screen.getByText("user-7", { selector: "td" })).toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "刷新" })).toBeEnabled();
    });

    it("查询按钮回到第一页重新读取，刷新保留当前页", async () => {
      const user = userEvent.setup();
      mocks.list.mockImplementation(async (_token, options) => page([grant(options?.page === 2 ? 31 : 1)], options?.page ?? 1, 60));

      renderPage();
      await screen.findByText("第 1 页 · 共 60 条");
      await user.click(screen.getByRole("button", { name: "下一页" }));
      await screen.findByText("第 2 页 · 共 60 条");

      await user.click(screen.getByRole("button", { name: "刷新" }));
      await screen.findByText("第 2 页 · 共 60 条");
      expect(mocks.list).toHaveBeenCalledTimes(3);
      expect(lastOptions()).toMatchObject({ page: 2 });

      await user.click(screen.getByRole("button", { name: "查询" }));
      await screen.findByText("第 1 页 · 共 60 条");
      expect(mocks.list).toHaveBeenCalledTimes(4);
      expect(lastOptions()).toMatchObject({ page: 1 });
    });

    it.each([
      ["503", httpError(503, "raw-503-marker", 503), "无法加载临时授权记录，请重试。"],
      ["HTTP 403", httpError(1003, "raw-forbidden-marker", 403), "当前账号无权访问凭据临时授权（仅管理员可读）。"],
      ["403 status", httpError(403, "raw-forbidden-marker"), "当前账号无权访问凭据临时授权（仅管理员可读）。"],
      ["network", new Error("raw-network-marker"), "无法加载临时授权记录，请重试。"],
    ] as const)("首次 %s 失败显示错误而非空结果，显式重试成功恢复", async (_label, error, message) => {
      const user = userEvent.setup();
      const retry = deferred();
      mocks.list.mockRejectedValueOnce(error).mockImplementationOnce(() => retry.promise);

      renderPage();
      expect(await screen.findByRole("alert")).toHaveTextContent(message);
      expect(screen.queryAllByText(/raw-.*-marker/)).toHaveLength(0);
      expect(screen.queryAllByText(/本页 \d+ 条|筛选结果 \d+ 条/)).toHaveLength(0);
      expect(screen.queryAllByText("当前筛选条件下没有凭据临时授权。")).toHaveLength(0);
      expect(screen.queryAllByText(/第 \d+ 页/)).toHaveLength(0);
      expect(screen.getByRole("button", { name: "查询" })).toBeEnabled();
      expect(screen.getByRole("button", { name: "刷新" })).toBeEnabled();
      expectNoFeedback();

      await user.click(screen.getByRole("button", { name: "重试" }));
      expect(mocks.list).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(screen.queryAllByText("user-1")).toHaveLength(0);

      await act(async () => {
        retry.resolve(page([grant(1)], 1, 1));
      });
      expect(screen.getByText("user-1", { selector: "td" })).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("后续普通失败不沿用上一查询的数据", async () => {
      const user = userEvent.setup();
      mocks.list.mockResolvedValueOnce(page([grant(1)], 1, 1)).mockRejectedValueOnce(httpError(500, "raw-500-marker", 500));

      renderPage();
      await screen.findByText("user-1", { selector: "td" });
      await user.selectOptions(statusSelect(), "expired");

      expect(await screen.findByRole("alert")).toHaveTextContent("无法加载临时授权记录，请重试。");
      expect(screen.queryAllByText("user-1")).toHaveLength(0);
      expect(screen.queryAllByText(/本页 \d+ 条|筛选结果 \d+ 条/)).toHaveLength(0);
      expect(screen.queryAllByText(/raw-500-marker/)).toHaveLength(0);
      expectNoFeedback();
    });
  });

  describe("身份与只读", () => {
    it.each(["operator", "viewer"] as const)("%s 直接访问页面时替换到概览且不读取列表", async (role) => {
      setAuth({ token: `${role}-token`, role });
      renderPage();

      expect(await screen.findByText("Overview destination")).toBeInTheDocument();
      expect(screen.queryAllByText("凭据临时授权")).toHaveLength(0);
      expect(mocks.list).not.toHaveBeenCalled();
    });

    it("没有令牌的管理员角色不挂载请求", async () => {
      setAuth({ token: null, role: "admin" });
      renderPage();

      expect(await screen.findByText("Overview destination")).toBeInTheDocument();
      expect(mocks.list).not.toHaveBeenCalled();
    });

    it("管理员身份转换期间只显示加载，不请求；恢复后新挂载重新读取", async () => {
      setAuth({ token: "test-token", role: "admin", authTransitioning: true });
      const view = renderPage();

      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryByRole("table")).not.toBeInTheDocument();
      expect(screen.queryAllByText("Overview destination")).toHaveLength(0);
      expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
      expect(mocks.list).not.toHaveBeenCalled();

      setAuth({ token: "test-token", role: "admin" }, false);
      view.rerenderPage();
      await screen.findByText("user-1", { selector: "td" });
      expect(mocks.list).toHaveBeenCalledTimes(1);
    });

    it.each(["token", "role", "generation", "transition"] as const)("%s 变化时旧列表与已打开详情同步移除", async (change) => {
      const user = userEvent.setup();
      const view = renderPage();
      await screen.findByText("user-1", { selector: "td" });
      await openDetail(user, "user-1");
      const next = deferred();
      mocks.list.mockImplementation(() => next.promise);

      if (change === "token") setAuth({ token: "other-token", role: "admin" });
      else if (change === "role") setAuth({ token: "test-token", role: "viewer" });
      else if (change === "generation") bumpAuthSessionGeneration();
      else setAuth({ token: "test-token", role: "admin", authTransitioning: true }, false);
      view.rerenderPage();

      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.queryAllByText("user-1")).toHaveLength(0);
      expect(screen.queryAllByText(/本页 \d+ 条/)).toHaveLength(0);
      if (change === "role") expect(screen.getByText("Overview destination")).toBeInTheDocument();
      else expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(mocks.list).toHaveBeenCalledTimes(change === "token" || change === "generation" ? 2 : 1);
      if (change === "token") expect(mocks.list.mock.lastCall?.[0]).toBe("other-token");
      expectNoFeedback();
    });

    const lateScenarios = ["token", "role", "unmount", "generation", "barrier", "aba"] as const;
    it.each(lateScenarios.flatMap((scenario) => (["resolve", "reject"] as const).map((outcome) => [scenario, outcome] as const)))(
      "%s 变化后的旧请求 %s 不显示旧记录、详情或反馈",
      async (scenario, outcome) => {
        const pending = [deferred(), deferred(), deferred()];
        mocks.list
          .mockImplementationOnce(() => pending[0]!.promise)
          .mockImplementationOnce(() => pending[1]!.promise)
          .mockImplementationOnce(() => pending[2]!.promise);
        const settle = async (index: number, id: number) => {
          await act(async () => {
            if (outcome === "resolve") pending[index]!.resolve(page([grant(id)], 1, 1));
            else pending[index]!.reject(httpError(503, `raw-${scenario}-${id}-marker`, 503));
          });
        };

        const view = renderPage();
        expect(mocks.list).toHaveBeenCalledTimes(1);
        expect(mocks.list.mock.calls[0]?.[0]).toBe("test-token");

        if (scenario === "token") {
          setAuth({ token: "other-token", role: "admin" });
          view.rerenderPage();
          expect(mocks.list).toHaveBeenCalledTimes(2);
          expect(mocks.list.mock.calls[1]?.[0]).toBe("other-token");
          await settle(0, 901);
          expect(screen.getByRole("status")).toHaveTextContent("加载中...");
          expect(screen.queryByRole("alert")).not.toBeInTheDocument();
          await settle(1, 902);
          if (outcome === "resolve") expect(screen.getByText("user-902", { selector: "td" })).toBeInTheDocument();
          else expect(screen.getByRole("alert")).toHaveTextContent("无法加载临时授权记录，请重试。");
        } else if (scenario === "role") {
          setAuth({ token: "test-token", role: "viewer" });
          view.rerenderPage();
          expect(screen.getByText("Overview destination")).toBeInTheDocument();
          await settle(0, 901);
          expect(screen.getByText("Overview destination")).toBeInTheDocument();
        } else if (scenario === "unmount") {
          view.unmount();
          await settle(0, 901);
        } else if (scenario === "generation") {
          bumpAuthSessionGeneration();
          await settle(0, 901);
          expect(screen.getByRole("status")).toHaveTextContent("加载中...");
          view.rerenderPage();
          expect(mocks.list).toHaveBeenCalledTimes(2);
        } else if (scenario === "barrier") {
          let barrier = 0;
          act(() => {
            barrier = beginAuthTransitionBarrier();
          });
          await settle(0, 901);
          expect(screen.getByRole("status")).toHaveTextContent("加载中...");
          setAuth({ token: "test-token", role: "admin", authTransitioning: true }, false);
          view.rerenderPage();
          expect(screen.getByRole("status")).toHaveTextContent("加载中...");
          expect(mocks.list).toHaveBeenCalledTimes(1);
          act(() => {
            releaseAuthTransitionBarrier(barrier);
          });
          setAuth({ token: "test-token", role: "admin" });
          view.rerenderPage();
          expect(mocks.list).toHaveBeenCalledTimes(2);
        } else {
          setAuth({ token: "token-b", role: "admin" });
          view.rerenderPage();
          setAuth({ token: "test-token", role: "admin" });
          view.rerenderPage();
          expect(mocks.list).toHaveBeenCalledTimes(3);
          await settle(0, 901);
          await settle(1, 902);
          expect(screen.getByRole("status")).toHaveTextContent("加载中...");
          expect(screen.queryByRole("alert")).not.toBeInTheDocument();
          await settle(2, 903);
          if (outcome === "resolve") expect(screen.getByText("user-903", { selector: "td" })).toBeInTheDocument();
          else expect(screen.getByRole("alert")).toHaveTextContent("无法加载临时授权记录，请重试。");
        }

        expect(screen.queryAllByText("user-901")).toHaveLength(0);
        if (scenario !== "token") expect(screen.queryAllByText("user-902")).toHaveLength(0);
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        expect(screen.queryAllByText(/raw-.*-marker/)).toHaveLength(0);
        if (scenario !== "token" && scenario !== "aba") expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expectNoFeedback();
      },
    );
  });

  describe("语言与可访问性", () => {
    it("英文界面渲染存储状态、到期提示与解释且无翻译键泄露", async () => {
      const user = userEvent.setup();
      await act(async () => {
        await setLanguage("en");
      });
      mocks.list.mockResolvedValue(page([
        grant(51, { status: "active", expires_at: PAST }),
        grant(52, { status: "approved", expires_at: FUTURE }),
        grant(53, { status: "future_state", expires_at: "not-a-date-marker" }),
      ], 1, 40));

      renderPage();
      expect(await screen.findByText("3 records on this page")).toBeInTheDocument();
      expect(screen.getByText("40 matching records")).toBeInTheDocument();
      expectRecord("user-51", "Active (stored)", "Expiry time passed");
      expectRecord("user-52", "Approved (stored)", "Not yet expired");
      expectRecord("user-53", "Unknown status", "Expiry time unknown");
      expect(statusSelect()).toHaveAccessibleName("Filter by stored status");
      expect(statusSelect()).toHaveAccessibleDescription("Filters match stored states. Expiry indications use the browser clock. An unelapsed expiry does not authorize an operation; the server still checks access.");
      expect(screen.getByText("Time ranges filter creation time. Choose All to inspect older records.")).toBeInTheDocument();
      expect(screen.getByText(/self-approved after the acting user completes additional verification/)).toBeInTheDocument();
      expect(statusSelect().querySelector("optgroup")).toHaveAttribute("label", "Other / historical stored statuses");

      await openDetail(user, "user-53");
      expect(within(detailValue("Stored status")).getByText("The stored state is unrecognized; it is not treated as active or expired.")).toBeInTheDocument();
      expect(within(detailValue("Expires at")).getByText("Expiry time unknown")).toBeInTheDocument();
      await user.keyboard("{Escape}");

      await openDetail(user, "user-51");
      expect(within(detailValue("Stored status")).getByText("Stored as active. Availability also depends on expiry and server checks for the operation.")).toBeInTheDocument();
      expect(document.body.textContent).not.toMatch(/credentialGrants\./);
    });

    it("英文加载失败使用本地化错误", async () => {
      await act(async () => {
        await setLanguage("en");
      });
      mocks.list.mockRejectedValue(httpError(503, "raw-503-marker", 503));

      renderPage();
      expect(await screen.findByRole("alert")).toHaveTextContent("Unable to load grant records. Try again.");
      expect(document.body.textContent).not.toMatch(/credentialGrants\./);
    });

    it("加载为 status，到期提示不在播报区域，详情键盘关闭后焦点回到触发按钮", async () => {
      const user = userEvent.setup();
      const pending = deferred();
      mocks.list.mockImplementationOnce(() => pending.promise);

      renderPage();
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.getByText("短期授权由发起操作的用户通过二次验证后自批准；此页仅供管理员只读查看，不提供批准、拒绝或撤销操作。")).toBeInTheDocument();
      await act(async () => {
        pending.resolve(page([grant(61, { expires_at: FUTURE })], 1, 1));
      });

      for (const label of screen.getAllByText("尚未到期")) {
        expect(label.closest("[aria-live],[role='status'],[role='alert']")).toBeNull();
      }
      expect(screen.getByRole("columnheader", { name: "记录状态" })).toBeInTheDocument();
      expect(screen.getByRole("columnheader", { name: "到期时间" })).toBeInTheDocument();

      const trigger = within(desktopRow("user-61")).getByRole("button", { name: "查看详情" });
      await user.click(trigger);
      expect(await screen.findByRole("dialog")).toHaveAccessibleDescription(/自批准/);
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
    });
  });
});
