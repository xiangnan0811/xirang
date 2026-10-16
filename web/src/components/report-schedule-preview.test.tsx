import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setLanguage } from "@/i18n";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import { ReportSchedulePreview } from "./report-schedule-preview";

const { authState, toastMock } = vi.hoisted(() => ({
  authState: { token: "tok-admin" as string | null, role: "admin" as "admin" | "operator" | "viewer" | null },
  toastMock: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: authState.token, role: authState.role }),
}));

vi.mock("sonner", () => ({ toast: toastMock }));

const TOKEN = "tok-admin";
const PREVIEW_PATH = "/api/v1/report-configs/schedule-preview";

type PendingFetch = {
  url: string;
  init: RequestInit;
  signal: AbortSignal;
  resolve: (response: Response) => void;
  reject: (reason: unknown) => void;
};

let calls: PendingFetch[] = [];
// A transport that ignores abort mimics a response already in flight when the owner moved on.
let honorAbort = true;
let monotonicNow = 1_000;

const fetchMock = vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => new Promise<Response>((resolve, reject) => {
  const signal = init.signal as AbortSignal;
  calls.push({ url: String(input), init, signal, resolve, reject });
  if (honorAbort) {
    signal.addEventListener("abort", () => reject(new DOMException("The operation was aborted.", "AbortError")), { once: true });
  }
}));

function envelope(data: unknown): Response {
  return new Response(JSON.stringify({ code: 200, message: "ok", data }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function failure(status: number): Response {
  return new Response(JSON.stringify({ code: status, message: "server says no", data: null }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const NEXT_WINDOW = {
  status: "match",
  reason: "",
  evaluated_at: "2026-10-18T23:59:30+08:00",
  expires_at: "2026-10-19T00:00:00+08:00",
  timezone: "Local",
  search_until: "2031-10-18T23:59:30+08:00",
  window_start: "2026-10-19T08:00:00+08:00",
  window_end: "2026-10-19T08:01:00+08:00",
};

const CURRENT_WINDOW = {
  status: "match",
  reason: "",
  evaluated_at: "2026-10-15T12:34:45.123456789+08:00",
  expires_at: "2026-10-15T12:35:00+08:00",
  timezone: "Asia/Shanghai",
  search_until: "2031-10-15T12:34:45.123456789+08:00",
  window_start: "2026-10-15T12:34:00+08:00",
  window_end: "2026-10-15T12:35:00+08:00",
};

function noMatch(reason: string) {
  return {
    status: "no_match",
    reason,
    evaluated_at: "2026-10-15T10:00:00-04:00",
    expires_at: "2026-10-15T10:01:00-04:00",
    timezone: "America/New_York",
    search_until: "2031-10-15T10:00:00-04:00",
    window_start: null,
    window_end: null,
  };
}

const NEXT_TEXT = "Next matching window: 2026-10-19T08:00:00+08:00 to 2026-10-19T08:01:00+08:00 (end exclusive)";

type ViewProps = { cron: string; token: string; active: boolean; disabled?: boolean };

function view(initial: Partial<ViewProps> = {}) {
  let props: ViewProps = { cron: "0 8 * * 1", token: TOKEN, active: true, ...initial };
  const utils = render(<ReportSchedulePreview {...props} />);
  return {
    ...utils,
    update(next: Partial<ViewProps> = {}) {
      props = { ...props, ...next };
      utils.rerender(<ReportSchedulePreview {...props} />);
    },
  };
}

async function flush() {
  for (let index = 0; index < 6; index += 1) {
    await act(async () => {
      await new Promise<void>((resolve) => setImmediate(resolve));
    });
  }
}

function setupUser() {
  return {
    async click(element: HTMLElement) {
      // RTL's userEvent async wrapper drains a timer using Jest-only detection.
      // Dispatch the actual click under RTL's act wrapper without that timer.
      fireEvent.click(element);
      await flush();
    },
  };
}

function previewButton() {
  return screen.getByRole("button", { name: /Estimate report time|Estimate again|预估报表时间|重新预估/ });
}

function status() {
  return screen.getByRole("status");
}

async function respond(index: number, response: Response) {
  calls[index]?.resolve(response);
  await flush();
}

function setVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, value });
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeAll(async () => {
  await setLanguage("zh");
  await setLanguage("en");
});

afterAll(async () => {
  await setLanguage("zh");
});

beforeEach(async () => {
  await setLanguage("en");
  calls = [];
  honorAbort = true;
  monotonicNow = 1_000;
  fetchMock.mockClear();
  Object.values(toastMock).forEach((mock) => mock.mockClear());
  authState.token = TOKEN;
  authState.role = "admin";
  clearAuthTransitionBarrier();
  rememberAuthIdentity(TOKEN, "admin");
  vi.stubGlobal("fetch", fetchMock);
  vi.spyOn(performance, "now").mockImplementation(() => monotonicNow);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(new Date("2026-10-15T10:00:00Z"));
});

afterEach(() => {
  cleanup();
  clearAuthTransitionBarrier();
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  rememberAuthIdentity(null, null);
});

describe("ReportSchedulePreview", () => {
  it("only requests after an explicit click and shows the server window in server offset", async () => {
    const user = setupUser();
    view();
    expect(screen.getByText(/Reports use server-local time and the existing simplified matcher/)).toBeInTheDocument();
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    await flush();
    expect(fetchMock).not.toHaveBeenCalled();

    await user.click(previewButton());
    expect(status()).toHaveTextContent("Reading server estimate…");
    expect(previewButton()).toBeDisabled();
    expect(calls).toHaveLength(1);
    const url = new URL(calls[0].url, "http://localhost");
    expect(url.pathname).toBe(PREVIEW_PATH);
    expect([...url.searchParams.entries()]).toEqual([["cron", "0 8 * * 1"]]);
    expect(calls[0].init.method).toBe("GET");
    expect(new Headers(calls[0].init.headers).get("Authorization")).toBe(`Bearer ${TOKEN}`);

    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent(NEXT_TEXT);
    expect(status()).toHaveTextContent("Server timezone: Local; evaluated at 2026-10-18T23:59:30+08:00");
    expect(previewButton()).toHaveAccessibleName("Estimate again");
    expect(previewButton()).toBeEnabled();
    // Estimating never saves the configuration or triggers report generation.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(toastMock.success).not.toHaveBeenCalled();
  });

  it("collapses a double click into one request", async () => {
    view();
    const button = previewButton();
    act(() => {
      button.click();
      button.click();
    });
    expect(calls).toHaveLength(1);
    await respond(0, envelope(NEXT_WINDOW));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("re-translates on language switch without requesting again", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));

    await act(async () => {
      await setLanguage("zh");
    });
    expect(status()).toHaveTextContent("下一匹配窗口：2026-10-19T08:00:00+08:00 至 2026-10-19T08:01:00+08:00（不含结束时刻）");
    expect(status()).toHaveTextContent("服务端时区：Local；评估于 2026-10-18T23:59:30+08:00");
    expect(previewButton()).toHaveAccessibleName("重新预估");

    await act(async () => {
      await setLanguage("en");
    });
    expect(status()).toHaveTextContent(NEXT_TEXT);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("labels a window that contains the evaluation instant as the current window", async () => {
    const user = setupUser();
    view({ cron: "* * * * *" });
    await user.click(previewButton());
    await respond(0, envelope(CURRENT_WINDOW));
    expect(status()).toHaveTextContent(
      "Current matching window: 2026-10-15T12:34:00+08:00 to 2026-10-15T12:35:00+08:00 (end exclusive)",
    );
    // Fractional seconds and offset are shown exactly as returned.
    expect(status()).toHaveTextContent("Server timezone: Asia/Shanghai; evaluated at 2026-10-15T12:34:45.123456789+08:00");
  });

  it.each([
    ["unsupported_month", "The current report scheduler only matches expressions with month *."],
    ["unmatchable_field", "These fields cannot match under the current report rules."],
    ["horizon_exhausted", "No server matching window within the next five years."],
  ])("shows no_match %s as a reason rather than a window", async (reason, text) => {
    const user = setupUser();
    view({ cron: "0 8 1 6 *" });
    await user.click(previewButton());
    await respond(0, envelope(noMatch(reason)));
    expect(status()).toHaveTextContent(text);
    expect(status()).toHaveTextContent("Server timezone: America/New_York; evaluated at 2026-10-15T10:00:00-04:00");
    expect(status()).not.toHaveTextContent(/matching window:/);
  });

  it.each([
    ["unknown status", { ...NEXT_WINDOW, status: "maybe" }],
    ["timestamp without offset", { ...NEXT_WINDOW, window_start: "2026-10-19T08:00:00" }],
    ["contradictory window", { ...NEXT_WINDOW, window_end: "2026-10-19T08:05:00+08:00" }],
    ["no_match carrying a window", { ...noMatch("horizon_exhausted"), window_start: NEXT_WINDOW.window_start }],
  ])("rejects a %s response as a failed estimate", async (_label, raw) => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    await respond(0, envelope(raw));
    expect(status()).toHaveTextContent("Could not read the report estimate. Try again.");
    expect(status()).not.toHaveTextContent(/matching window/);
    expect(previewButton()).toHaveAccessibleName("Estimate report time");
  });

  it.each([
    [403, "This account cannot estimate report schedules."],
    [404, "This server does not support report schedule estimates."],
    [429, "Too many requests. Try again later."],
    [500, "Could not read the report estimate. Try again."],
  ])("shows HTTP %i safely without retry, fallback or toast", async (code, text) => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));
    await user.click(previewButton());
    await respond(1, failure(code));

    expect(status()).toHaveTextContent(text);
    expect(status()).not.toHaveTextContent("server says no");
    expect(status()).not.toHaveTextContent(/matching window/);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(Object.values(toastMock).every((mock) => mock.mock.calls.length === 0)).toBe(true);

    await user.click(previewButton());
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("drops the old window as soon as the expression changes and does not request", async () => {
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent(NEXT_TEXT);

    ui.update({ cron: "0 9 * * 1" });
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    expect(previewButton()).toHaveAccessibleName("Estimate report time");
    await flush();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("ignores a superseded success and keeps the new request pending until its own response", async () => {
    honorAbort = false;
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    ui.update({ cron: "0 9 * * 1" });
    expect(calls[0].signal.aborted).toBe(true);
    await user.click(previewButton());
    expect(calls).toHaveLength(2);
    expect(new URL(calls[1].url, "http://localhost").searchParams.get("cron")).toBe("0 9 * * 1");

    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent("Reading server estimate…");
    expect(previewButton()).toBeDisabled();

    await respond(1, envelope(noMatch("horizon_exhausted")));
    expect(status()).toHaveTextContent("No server matching window within the next five years.");
  });

  it("ignores a superseded failure without unlocking the new request", async () => {
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    ui.update({ cron: "0 9 * * 1" });
    await user.click(previewButton());
    calls[0].reject(new TypeError("network down"));
    await flush();
    expect(status()).toHaveTextContent("Reading server estimate…");
    expect(previewButton()).toBeDisabled();
    await respond(1, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent(NEXT_TEXT);
  });

  it("starts empty after close and reopen and aborts in-flight work on close", async () => {
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));

    ui.update({ active: false });
    expect(status()).not.toHaveTextContent(/matching window/);
    expect(previewButton()).toBeDisabled();
    ui.update({ active: true });
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");

    await user.click(previewButton());
    ui.update({ active: false });
    expect(calls[1].signal.aborted).toBe(true);
    ui.update({ active: true });
    await flush();
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("aborts and blocks the estimate when the account is downgraded", async () => {
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    authState.role = "operator";
    rememberAuthIdentity(TOKEN, "operator");
    ui.update();
    expect(calls[0].signal.aborted).toBe(true);
    expect(status()).toHaveTextContent("This account cannot estimate report schedules.");
    expect(previewButton()).toBeDisabled();
  });

  it("discards work from an earlier session generation with the same token", async () => {
    honorAbort = false;
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    act(() => {
      bumpAuthSessionGeneration();
    });
    ui.update();
    expect(calls[0].signal.aborted).toBe(true);
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");

    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).not.toHaveTextContent(/matching window/);
    await user.click(previewButton());
    expect(calls).toHaveLength(2);
  });

  it("drops a response whose identity changed before it settled", async () => {
    honorAbort = false;
    const user = setupUser();
    view();
    await user.click(previewButton());
    rememberAuthIdentity("tok-other", "admin");
    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).not.toHaveTextContent(/matching window/);
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
  });

  it("clears results and aborts at an auth barrier, then waits for an explicit click", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));

    let barrier = 0;
    act(() => {
      barrier = beginAuthTransitionBarrier();
    });
    expect(status()).not.toHaveTextContent(/matching window/);
    expect(previewButton()).toBeDisabled();
    act(() => {
      releaseAuthTransitionBarrier(barrier);
    });
    expect(previewButton()).toBeEnabled();
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");

    await user.click(previewButton());
    act(() => {
      barrier = beginAuthTransitionBarrier();
    });
    expect(calls[1].signal.aborted).toBe(true);
    act(() => {
      releaseAuthTransitionBarrier(barrier);
    });
    await flush();
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await user.click(previewButton());
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("aborts on unmount", async () => {
    const user = setupUser();
    const ui = view();
    await user.click(previewButton());
    ui.unmount();
    expect(calls[0].signal.aborted).toBe(true);
    await flush();
  });

  it("expires after the server lifetime minus the round trip, independent of the browser clock", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    monotonicNow += 2_000;
    // Wall-clock jumps must not shorten or extend the server-issued lifetime.
    vi.setSystemTime(new Date("2035-01-01T00:00:00Z"));
    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent(NEXT_TEXT);

    vi.setSystemTime(new Date("2001-01-01T00:00:00Z"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(27_999);
    });
    expect(status()).toHaveTextContent(NEXT_TEXT);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(status()).toHaveTextContent("Estimate expired; estimate again.");
    expect(status()).not.toHaveTextContent(/matching window/);
    expect(previewButton()).toHaveAccessibleName("Estimate again");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("never shows a window whose lifetime was consumed by the round trip", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    monotonicNow += 30_000;
    await respond(0, envelope(NEXT_WINDOW));
    expect(status()).toHaveTextContent("Estimate expired; estimate again.");
    expect(status()).not.toHaveTextContent(/matching window/);
  });

  it("expires a shown window when the page is hidden and does not refetch when visible", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    await respond(0, envelope(NEXT_WINDOW));
    setVisibility("hidden");
    expect(status()).toHaveTextContent("Estimate expired; estimate again.");
    setVisibility("visible");
    await flush();
    expect(status()).toHaveTextContent("Estimate expired; estimate again.");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("cancels a pending request on hidden page or window blur without automatic resend", async () => {
    const user = setupUser();
    view();
    await user.click(previewButton());
    setVisibility("hidden");
    expect(calls[0].signal.aborted).toBe(true);
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    setVisibility("visible");

    await user.click(previewButton());
    act(() => {
      window.dispatchEvent(new Event("blur"));
    });
    expect(calls[1].signal.aborted).toBe(true);
    act(() => {
      window.dispatchEvent(new Event("focus"));
    });
    await flush();
    expect(status()).toHaveTextContent("Estimate using the server's current time and matching rules.");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("disables the estimate while the dialog is saving", () => {
    view({ disabled: true });
    expect(previewButton()).toBeDisabled();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
