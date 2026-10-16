import { useState } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLanguage } from "@/i18n";
import { CronGenerator } from "./cron-generator";

function Editor({ initial = "*/15 * * * *" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <><label htmlFor="cron-test">Schedule</label><CronGenerator id="cron-test" consumer="task" value={value} onChange={setValue} /></>;
}

beforeEach(async () => {
  await setLanguage("en");
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-15T10:07:42Z"));
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function expectEstimate(time: string) {
  expect(screen.getByText(`Browser-clock estimate: ${time} UTC`)).toBeVisible();
}

describe("CronGenerator preview", () => {
  it("retranslates the same expression and retains UTC and authority", async () => {
    render(<Editor />);
    expectEstimate("2026-10-15 10:15:00");
    expect(screen.getByText(/server nextRunAt is authoritative/)).toBeVisible();
    await act(async () => { await setLanguage("zh"); });
    expect(screen.getByText("浏览器时钟预估：2026-10-15 10:15:00 UTC")).toBeVisible();
    expect(screen.getByLabelText("Schedule")).toHaveValue("*/15 * * * *");
    await act(async () => { await setLanguage("en"); });
    expectEstimate("2026-10-15 10:15:00");
  });

  it("refreshes within one second across month boundaries and clock jumps", () => {
    vi.setSystemTime(new Date("2026-10-31T23:59:59Z"));
    render(<Editor initial="* * * * *" />);
    expectEstimate("2026-11-01 00:00:00");
    act(() => { vi.advanceTimersByTime(1000); });
    expectEstimate("2026-11-01 00:01:00");
    act(() => {
      vi.setSystemTime(new Date("2026-12-01T13:24:30Z"));
      vi.advanceTimersByTime(1000);
    });
    expectEstimate("2026-12-01 13:25:00");
    act(() => {
      vi.setSystemTime(new Date("2026-10-01T02:03:30Z"));
      vi.advanceTimersByTime(1000);
    });
    expectEstimate("2026-10-01 02:04:00");
  });

  it("skips hidden sampling and refreshes on visibility and focus", () => {
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    render(<Editor />);
    hidden.mockReturnValue(true);
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
      vi.setSystemTime(new Date("2026-10-15T12:00:00Z"));
      vi.advanceTimersByTime(1000);
    });
    expectEstimate("2026-10-15 10:15:00");
    hidden.mockReturnValue(false);
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    expectEstimate("2026-10-15 12:15:00");
    act(() => {
      vi.setSystemTime(new Date("2026-10-15T09:00:00Z"));
      window.dispatchEvent(new Event("focus"));
    });
    expectEstimate("2026-10-15 09:15:00");
  });

  it.each(["visibilitychange", "focus"])("restores a hidden rerender in the same minute on %s", (event) => {
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    const { rerender } = render(<Editor />);
    expectEstimate("2026-10-15 10:15:00");
    hidden.mockReturnValue(true);
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    // A background parent update must not leave the estimate blank after return.
    rerender(<Editor />);
    hidden.mockReturnValue(false);
    act(() => {
      (event === "focus" ? window : document).dispatchEvent(new Event(event));
    });
    expectEstimate("2026-10-15 10:15:00");
  });

  it("uses current time on input changes and distinguishes unsupported, no-match and empty", () => {
    render(<Editor />);
    vi.setSystemTime(new Date("2026-10-15T22:40:00Z"));
    fireEvent.change(screen.getByLabelText("Schedule"), { target: { value: "30 */5 * * *" } });
    expectEstimate("2026-10-16 00:30:00");
    fireEvent.change(screen.getByLabelText("Schedule"), { target: { value: "0 2 30 2 *" } });
    expect(screen.getByText("No matching time within the next five years.")).toBeVisible();
    fireEvent.change(screen.getByLabelText("Schedule"), { target: { value: "TZ=America/New_York 30 2 * * *" } });
    expect(screen.getByText(/This estimator does not support/)).toBeVisible();
    fireEvent.change(screen.getByLabelText("Schedule"), { target: { value: "  " } });
    expect(screen.queryByText(/server nextRunAt is authoritative/)).not.toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("preserves preset editing and provides a correct monthly preview", () => {
    render(<Editor />);
    fireEvent.click(screen.getByRole("button", { name: "1st of month at 2 AM" }));
    expect(screen.getByLabelText("Schedule")).toHaveValue("0 2 1 * *");
    expectEstimate("2026-11-01 02:00:00");
  });

  it.each([
    ["*/15 * * * *", "minutes", "2026-10-15 10:15:00"],
    ["30 */5 * * *", "hours", "2026-10-15 10:30:00"],
    ["0 2 * * *", "daily", "2026-10-16 02:00:00"],
    ["0 2 * * 1,3", "weekly", "2026-10-19 02:00:00"],
    ["0 2 31 * *", "monthly", "2026-10-31 02:00:00"],
    ["0 2 29 2 *", "custom", "2028-02-29 02:00:00"],
  ])("opening builder for %s does not rewrite the schedule", (initial, mode, expected) => {
    render(<Editor initial={initial} />);
    fireEvent.click(screen.getByRole("button", { name: "Visual builder" }));
    expect(screen.getByRole("combobox", { name: "Execution Frequency" })).toHaveValue(mode);
    expect(screen.getByLabelText("Schedule")).toHaveValue(initial);
    expectEstimate(expected);
  });

  it("uses only the report slot and raw expression, without a browser estimate", () => {
    const { rerender } = render(<CronGenerator consumer="report" value="*/15 * * * *" onChange={vi.fn()} preview={<p>Server preview controls</p>} />);
    expect(screen.getByText("By expression */15 * * * *")).toBeVisible();
    expect(screen.getByText("Server preview controls")).toBeVisible();
    expect(screen.queryByText("Every 15 minutes")).not.toBeInTheDocument();
    expect(screen.queryByText(/Browser-clock estimate/)).not.toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(0);
    rerender(<CronGenerator consumer="report" value=" " onChange={vi.fn()} preview={<p>Server preview controls</p>} />);
    expect(screen.queryByText("Server preview controls")).not.toBeInTheDocument();
  });

  it("cleans up sampling when unmounted", () => {
    const { unmount } = render(<Editor />);
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    act(() => {
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(60000);
    });
    expect(screen.queryByText(/Browser-clock estimate/)).not.toBeInTheDocument();
  });
});
