import "@testing-library/jest-dom/vitest";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import type { LogEvent } from "@/types/domain";
import { LogsViewer } from "./logs-viewer";

// jsdom 下元素默认 0×0，react-virtual 会判定容器无尺寸而拒绝渲染任何 row。
// 这里 stub HTMLElement.prototype 的尺寸 getter，让虚拟化能正常出 row。
// ResizeObserver 已在全局 vitest.setup.ts 中 polyfill。
beforeAll(() => {
  const proto = HTMLElement.prototype as unknown as {
    __logsViewerJsdomPatched?: boolean;
  };
  if (proto.__logsViewerJsdomPatched) return;
  Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value: function () {
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 800,
        bottom: 600,
        width: 800,
        height: 600,
        toJSON: () => ({}),
      } as DOMRect;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 800,
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, "clientWidth", {
    configurable: true,
    get: () => 800,
  });
  proto.__logsViewerJsdomPatched = true;
});

afterEach(() => {
  cleanup();
});

function makeLogs(count: number): LogEvent[] {
  // 模拟 logs-page.tsx 的降序排序：新日志在前
  const out: LogEvent[] = [];
  for (let i = count; i >= 1; i--) {
    out.push({
      id: `log-${i}`,
      logId: i,
      timestamp: "2026-05-03 10:00:00",
      level: "info",
      message: `message ${i}`,
      taskId: 1,
      nodeName: "node-1",
    });
  }
  return out;
}

describe("LogsViewer (virtualization)", () => {
  it("1000 条日志输入下，DOM 中实际渲染的行数远小于总数（虚拟化生效）", () => {
    const logs = makeLogs(1000);

    const { container } = render(
      <LogsViewer
        filteredLogs={logs}
        historyLoading={false}
        onReset={() => {}}
      />,
    );

    // 通过 data-index 属性精确定位虚拟化渲染出的 row 包装 div，
    // 不会被 LogEntry 内部 div 干扰。
    const rendered = container.querySelectorAll("[data-index]");
    expect(rendered.length).toBeGreaterThan(0);
    // 严格上限：62vh ≈ 600px，单行估高 28px，overscan 12 → 上限远小于 50。
    // 这里给 50 的宽松上限以避免 jsdom 高度近似带来的不稳定。
    expect(rendered.length).toBeLessThanOrEqual(50);
    // 业务断言：1000 条全部进 DOM 是不可接受的
    expect(rendered.length).toBeLessThan(logs.length);
  });

  it("处于顶部时（stickToNewest=true），新日志到达后保持滚动在顶部", () => {
    const logs = makeLogs(50);

    const { container, rerender } = render(
      <LogsViewer
        filteredLogs={logs}
        historyLoading={false}
        onReset={() => {}}
      />,
    );

    const scroller = container.querySelector('[role="log"]') as HTMLElement;
    expect(scroller).not.toBeNull();
    // 初始 scrollTop = 0（贴近最新）
    expect(scroller.scrollTop).toBe(0);

    // 模拟新日志到达：在数组前端插入一条
    const newLog: LogEvent = {
      id: "log-51",
      logId: 51,
      timestamp: "2026-05-03 10:01:00",
      level: "info",
      message: "freshly arrived",
      taskId: 1,
      nodeName: "node-1",
    };

    act(() => {
      rerender(
        <LogsViewer
          filteredLogs={[newLog, ...logs]}
          historyLoading={false}
          onReset={() => {}}
        />,
      );
    });

    // stickToNewest=true 时新日志到达，仍应吸附在顶部
    expect(scroller.scrollTop).toBe(0);
  });

  it("用户主动向下滚动后（stickToNewest=false），新日志到达不会强制把视口拖回顶部", () => {
    const logs = makeLogs(80);

    const { container, rerender } = render(
      <LogsViewer
        filteredLogs={logs}
        historyLoading={false}
        onReset={() => {}}
      />,
    );

    const scroller = container.querySelector('[role="log"]') as HTMLElement;
    expect(scroller).not.toBeNull();

    // 模拟用户向下滚动到非顶部位置（远超 STICK_THRESHOLD_PX=64）
    act(() => {
      scroller.scrollTop = 400;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(scroller.scrollTop).toBe(400);

    // 模拟新日志到达
    const newLog: LogEvent = {
      id: "log-81",
      logId: 81,
      timestamp: "2026-05-03 10:01:00",
      level: "info",
      message: "freshly arrived while scrolled away",
      taskId: 1,
      nodeName: "node-1",
    };

    act(() => {
      rerender(
        <LogsViewer
          filteredLogs={[newLog, ...logs]}
          historyLoading={false}
          onReset={() => {}}
        />,
      );
    });

    // 关键断言：scrollTop 不应被自动改写为 0
    expect(scroller.scrollTop).toBe(400);
  });

  it("无日志时显示空态，不渲染虚拟化行", () => {
    const onReset = vi.fn();
    const { container } = render(
      <LogsViewer
        filteredLogs={[]}
        historyLoading={false}
        onReset={onReset}
      />,
    );

    expect(container.querySelectorAll("[data-index]").length).toBe(0);
  });
});
