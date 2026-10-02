import { StrictMode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AlertDeliveryStats } from "@/types/domain";
import { DeliveryStatsCard } from "./notifications-page.delivery-stats";

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
};

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function stats(partial: Omit<AlertDeliveryStats, "byIntegration"> & Partial<Pick<AlertDeliveryStats, "byIntegration">>): AlertDeliveryStats {
  return {
    byIntegration: [],
    ...partial,
  };
}

const collapsedStorageKey = "xirang.notifications.stats-collapsed";

describe("DeliveryStatsCard", () => {
  beforeEach(() => {
    window.localStorage.removeItem(collapsedStorageKey);
  });

  it("切换到失败窗口后展开区不再保留旧数字，折叠摘要也不拼接旧结果", async () => {
    const user = userEvent.setup();
    const pending72 = createDeferred<AlertDeliveryStats>();
    const fetchAlertDeliveryStats = vi.fn((hours: number) => {
      if (hours === 72) {
        return pending72.promise;
      }
      return Promise.resolve(stats({
        windowHours: 24,
        totalSent: 9,
        totalFailed: 1,
        successRate: 90,
        byIntegration: [{
          integrationId: "mail",
          name: "运维邮箱",
          type: "email",
          sent: 9,
          failed: 1,
          successRate: 90,
        }],
      }));
    });

    render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("24h 内共 10 次投递，成功率 90%");
    });

    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    expect(screen.getByText("运维邮箱")).toBeInTheDocument();
    expect(screen.getByText("90%")).toBeInTheDocument();
    expect(screen.getByText("发送成功").parentElement).toHaveTextContent("9");
    expect(screen.getByText("发送失败").parentElement).toHaveTextContent("1");

    await user.click(screen.getByRole("button", { name: "72h" }));
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.queryByText("90%")).not.toBeInTheDocument();
    expect(screen.queryByText("运维邮箱")).not.toBeInTheDocument();
    expect(screen.queryByText("72h 内共 10 次投递，成功率 90%")).not.toBeInTheDocument();

    await act(async () => {
      pending72.reject(Object.assign(new Error("503 Service Unavailable"), { status: 503 }));
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("503 Service Unavailable");
    expect(screen.getByRole("button", { name: "重试" })).toBeEnabled();
    expect(screen.queryByText("90%")).not.toBeInTheDocument();
    expect(screen.queryByText("发送成功")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    const toggle = screen.getByRole("button", { name: /投递统计/ });
    expect(toggle).toHaveTextContent("获取告警投递统计失败");
    expect(toggle).not.toHaveTextContent("503");
    expect(toggle).not.toHaveTextContent("90%");
    expect(toggle).not.toHaveTextContent("10 次投递");
    expect(screen.queryByText("72h 内共 10 次投递，成功率 90%")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
  });

  it("同窗口重试可以恢复，随后的手动刷新失败后也能再恢复", async () => {
    const user = userEvent.setup();
    const queued: Array<Deferred<AlertDeliveryStats>> = [];
    let mode: "fail" | "defer" = "fail";
    const fetchAlertDeliveryStats = vi.fn((hours: number) => {
      expect(hours).toBe(24);
      if (mode === "fail") {
        return Promise.reject(new Error("stats down"));
      }
      const deferred = createDeferred<AlertDeliveryStats>();
      queued.push(deferred);
      return deferred.promise;
    });

    render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("获取告警投递统计失败");
    });
    const callsAfterFailure = fetchAlertDeliveryStats.mock.calls.length;

    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    await user.click(screen.getByRole("button", { name: "24h" }));
    expect(fetchAlertDeliveryStats).toHaveBeenCalledTimes(callsAfterFailure);
    expect(screen.getByRole("alert")).toHaveTextContent("stats down");
    expect(screen.queryByText("100%")).not.toBeInTheDocument();

    mode = "defer";
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "加载中..." })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
    const retryRequest = queued.at(-1);
    if (!retryRequest) {
      throw new Error("missing retry request");
    }
    await act(async () => {
      retryRequest.resolve(stats({ windowHours: 24, totalSent: 4, totalFailed: 0, successRate: 100 }));
    });
    await waitFor(() => {
      expect(screen.getByText("100%")).toBeInTheDocument();
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    mode = "fail";
    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("stats down");
    });
    expect(screen.queryByText("100%")).not.toBeInTheDocument();

    mode = "defer";
    await user.click(screen.getByRole("button", { name: "刷新" }));
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.queryByText("100%")).not.toBeInTheDocument();
    const refreshRequest = queued.at(-1);
    if (!refreshRequest || refreshRequest === retryRequest) {
      throw new Error("missing refresh request");
    }
    await act(async () => {
      refreshRequest.resolve(stats({ windowHours: 24, totalSent: 8, totalFailed: 2, successRate: 80 }));
    });
    await waitFor(() => {
      expect(screen.getByText("80%")).toBeInTheDocument();
    });
    expect(screen.queryByText("100%")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("A→B→A 时只采纳最后一次窗口，过期的成功和失败都不改当前结果", async () => {
    const user = userEvent.setup();
    const inflight = new Map<number, Array<Deferred<AlertDeliveryStats>>>();
    const fetchAlertDeliveryStats = vi.fn((hours: number) => {
      const deferred = createDeferred<AlertDeliveryStats>();
      const list = inflight.get(hours) ?? [];
      list.push(deferred);
      inflight.set(hours, list);
      return deferred.promise;
    });
    const requestsFor = (hours: number) => inflight.get(hours) ?? [];

    render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />);
    await waitFor(() => {
      expect(requestsFor(24).length).toBeGreaterThan(0);
    });
    const firstA = requestsFor(24).at(-1);
    if (!firstA) {
      throw new Error("missing first 24h request");
    }

    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    await user.click(screen.getByRole("button", { name: "72h" }));
    await waitFor(() => {
      expect(requestsFor(72).length).toBeGreaterThan(0);
    });
    const windowB = requestsFor(72).at(-1);
    if (!windowB) {
      throw new Error("missing 72h request");
    }

    await user.click(screen.getByRole("button", { name: "24h" }));
    await waitFor(() => {
      expect(requestsFor(24).length).toBeGreaterThan(1);
    });
    const thirdA = requestsFor(24).at(-1);
    if (!thirdA || thirdA === firstA) {
      throw new Error("missing replacement 24h request");
    }
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "加载中..." })).toBeDisabled();

    await act(async () => {
      windowB.resolve(stats({ windowHours: 72, totalSent: 1, totalFailed: 50, successRate: 2 }));
      firstA.resolve(stats({ windowHours: 24, totalSent: 9, totalFailed: 1, successRate: 90 }));
    });
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.queryByText("90%")).not.toBeInTheDocument();
    expect(screen.queryByText("2%")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await act(async () => {
      thirdA.resolve(stats({ windowHours: 24, totalSent: 3, totalFailed: 7, successRate: 30 }));
    });
    await waitFor(() => {
      expect(screen.getByText("30%")).toBeInTheDocument();
    });
    expect(screen.queryByText("90%")).not.toBeInTheDocument();
    expect(screen.queryByText("2%")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "72h" }));
    await waitFor(() => {
      expect(requestsFor(72).length).toBeGreaterThan(1);
    });
    const rejectedB = requestsFor(72).at(-1);
    if (!rejectedB || rejectedB === windowB) {
      throw new Error("missing second 72h request");
    }
    expect(screen.queryByText("30%")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "24h" }));
    await waitFor(() => {
      expect(requestsFor(24).filter((request) => request !== firstA && request !== thirdA).length).toBeGreaterThan(0);
    });
    const latestA = requestsFor(24).at(-1);
    if (!latestA || latestA === thirdA) {
      throw new Error("missing latest 24h request");
    }

    await act(async () => {
      rejectedB.reject(new Error("b failed"));
    });
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.queryByText("b failed")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("30%")).not.toBeInTheDocument();

    await act(async () => {
      latestA.resolve(stats({ windowHours: 24, totalSent: 8, totalFailed: 2, successRate: 80 }));
    });
    await waitFor(() => {
      expect(screen.getByText("80%")).toBeInTheDocument();
    });
    expect(screen.queryByText("30%")).not.toBeInTheDocument();
    expect(screen.queryByText("b failed")).not.toBeInTheDocument();
  });

  it("StrictMode 下过期请求不能结束仍在进行的加载", async () => {
    const inflight: Array<Deferred<AlertDeliveryStats>> = [];
    const fetchAlertDeliveryStats = vi.fn(() => {
      const deferred = createDeferred<AlertDeliveryStats>();
      inflight.push(deferred);
      return deferred.promise;
    });

    render(
      <StrictMode>
        <DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />
      </StrictMode>,
    );
    await waitFor(() => {
      expect(inflight.length).toBeGreaterThan(0);
    });
    expect(fetchAlertDeliveryStats.mock.calls.length).toBeGreaterThanOrEqual(1);

    await act(async () => {
      inflight.slice(0, -1).forEach((deferred) => {
        deferred.resolve(stats({ windowHours: 24, totalSent: 0, totalFailed: 8, successRate: 0 }));
      });
    });
    expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("加载中...");
    expect(screen.queryByText(/成功率 0%/)).not.toBeInTheDocument();

    const current = inflight.at(-1);
    if (!current) {
      throw new Error("missing current stats request");
    }
    await act(async () => {
      current.resolve(stats({ windowHours: 24, totalSent: 4, totalFailed: 0, successRate: 100 }));
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("成功率 100%");
    });
    expect(screen.queryByText(/成功率 0%/)).not.toBeInTheDocument();
  });

  it("卸载或替换统计回调后，迟到结果不会提交", async () => {
    const first = createDeferred<AlertDeliveryStats>();
    const second = createDeferred<AlertDeliveryStats>();
    const third = createDeferred<AlertDeliveryStats>();
    const fetchA = vi.fn(() => first.promise);
    const fetchB = vi.fn(() => second.promise);
    const fetchC = vi.fn(() => third.promise);

    const view = render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchA} />);
    await waitFor(() => {
      expect(fetchA).toHaveBeenCalled();
    });
    view.rerender(<DeliveryStatsCard fetchAlertDeliveryStats={fetchB} />);
    await waitFor(() => {
      expect(fetchB).toHaveBeenCalled();
    });

    await act(async () => {
      first.resolve(stats({ windowHours: 24, totalSent: 1, totalFailed: 9, successRate: 10 }));
    });
    expect(screen.queryByText(/成功率 10%/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("加载中...");

    view.rerender(<div />);
    await act(async () => {
      second.resolve(stats({ windowHours: 24, totalSent: 5, totalFailed: 5, successRate: 50 }));
    });
    expect(screen.queryByText(/成功率 50%/)).not.toBeInTheDocument();

    view.rerender(<DeliveryStatsCard fetchAlertDeliveryStats={fetchC} />);
    await waitFor(() => {
      expect(fetchC).toHaveBeenCalled();
    });
    expect(screen.queryByText(/成功率 10%/)).not.toBeInTheDocument();
    expect(screen.queryByText(/成功率 50%/)).not.toBeInTheDocument();

    await act(async () => {
      third.resolve(stats({ windowHours: 24, totalSent: 6, totalFailed: 0, successRate: 100 }));
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /投递统计/ })).toHaveTextContent("成功率 100%");
    });
  });

  it("返回窗口与请求不一致时显示失败而不是改标题", async () => {
    const user = userEvent.setup();
    const fetchAlertDeliveryStats = vi.fn(async (hours: number) => stats({
      windowHours: hours === 24 ? 24 : 99,
      totalSent: 4,
      totalFailed: 4,
      successRate: 50,
    }));

    render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />);
    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    await waitFor(() => {
      expect(screen.getByText("50%")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "72h" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("获取告警投递统计失败");
    expect(screen.queryByText("50%")).not.toBeInTheDocument();
    expect(screen.queryByText("72h 内共 8 次投递，成功率 50%")).not.toBeInTheDocument();
    expect(fetchAlertDeliveryStats).toHaveBeenCalledWith(72);
  });

  it("展示退役修正后的 10/0/100%，并仍然展示真实失败", async () => {
    const user = userEvent.setup();
    const refreshed = createDeferred<AlertDeliveryStats>();
    let calls = 0;
    const fetchAlertDeliveryStats = vi.fn(() => {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve(stats({
          windowHours: 24,
          totalSent: 10,
          totalFailed: 0,
          successRate: 100,
          byIntegration: [{
            integrationId: "live",
            name: "在线通道",
            type: "slack",
            sent: 10,
            failed: 0,
            successRate: 100,
          }],
        }));
      }
      return refreshed.promise;
    });

    render(<DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />);
    await user.click(screen.getByRole("button", { name: /投递统计/ }));
    await waitFor(() => {
      expect(screen.getByText("100%")).toBeInTheDocument();
    });
    expect(screen.getByText("发送成功").parentElement).toHaveTextContent("10");
    expect(screen.getByText("发送失败").parentElement).toHaveTextContent("0");
    expect(screen.getByText("在线通道")).toBeInTheDocument();
    expect(screen.queryByText("退役封存")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "刷新" }));
    expect(screen.getByText("统计数据加载中")).toBeInTheDocument();
    expect(screen.queryByText("100%")).not.toBeInTheDocument();
    expect(screen.queryByText("在线通道")).not.toBeInTheDocument();
    expect(screen.queryByText("退役封存")).not.toBeInTheDocument();

    await act(async () => {
      refreshed.resolve(stats({
        windowHours: 24,
        totalSent: 10,
        totalFailed: 1,
        successRate: 90.9,
        byIntegration: [{
          integrationId: "live",
          name: "在线通道",
          type: "slack",
          sent: 10,
          failed: 1,
          successRate: 90.9,
        }],
      }));
    });
    await waitFor(() => {
      expect(screen.getByText("90.9%")).toBeInTheDocument();
    });
    expect(screen.getByText("发送成功").parentElement).toHaveTextContent("10");
    expect(screen.getByText("发送失败").parentElement).toHaveTextContent("1");
    expect(screen.getByText("在线通道")).toBeInTheDocument();
    expect(screen.queryByText("退役封存")).not.toBeInTheDocument();
    expect(screen.queryByText("100%")).not.toBeInTheDocument();
  });
});
