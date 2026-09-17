import "@testing-library/jest-dom/vitest";
import { StrictMode, type ReactNode } from "react";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PanelEditorDialog } from "./panel-editor-dialog";

// Isolate layout scheduling from Radix's own animation frames and chart measurement.
vi.mock("@/components/ui/dialog", () => {
  const Part = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
  return {
    Dialog: ({ open, children }: { open: boolean; children: ReactNode }) => open ? <div>{children}</div> : null,
    DialogContent: Part, DialogHeader: Part, DialogTitle: Part,
    DialogDescription: Part, DialogBody: Part, DialogFooter: Part, DialogCloseButton: Part,
  };
});
vi.mock("./panel-renderer", () => ({ PanelRenderer: () => <div data-testid="preview-chart" /> }));
vi.mock("@/lib/api/client", () => ({ apiClient: {
  listMetrics: vi.fn().mockResolvedValue([{ key: "node.cpu", label: "CPU", family: "node", defaultAggregation: "avg", supportedAggregations: ["avg"] }]),
  queryPanel: vi.fn().mockResolvedValue({ series: [], stepSeconds: 60 }),
} }));
vi.mock("@/lib/api/nodes-api", () => ({ createNodesApi: () => ({ getNodes: vi.fn().mockResolvedValue([]) }) }));
vi.mock("@/lib/api/tasks-api", () => ({ createTasksApi: () => ({ getTasks: vi.fn().mockResolvedValue([]) }) }));

describe("PanelEditorDialog animation lifecycle", () => {
  let nextID: number;
  let frames: Map<number, FrameRequestCallback>;

  beforeEach(() => {
    vi.useFakeTimers();
    nextID = 1;
    frames = new Map();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      const id = nextID++;
      frames.set(id, callback);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const dialog = (open: boolean) => <PanelEditorDialog open={open} onOpenChange={() => {}} dashboardID={1} start="2026-01-01T00:00:00Z" end="2026-01-01T01:00:00Z" onSaved={() => {}} token="test" panel={{ id: 1, dashboardId: 1, title: "CPU", chartType: "line", metric: "node.cpu", aggregation: "avg", filters: {}, layoutX: 0, layoutY: 0, layoutW: 6, layoutH: 4 }} />;
  async function settlePreview() {
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
  }
  function paint() {
    act(() => {
      const scheduled = [...frames];
      frames.clear();
      scheduled.forEach(([, callback]) => callback(0));
    });
  }

  it("waits two frames before rendering the loaded preview, including after reopening", async () => {
    const view = render(dialog(true));
    await settlePreview();
    expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
    paint();
    expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
    paint();
    expect(screen.getByTestId("preview-chart")).toBeInTheDocument();
    view.rerender(dialog(false));
    view.rerender(dialog(true));
    await settlePreview();
    expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
    paint();
    expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
    paint();
    expect(screen.getByTestId("preview-chart")).toBeInTheDocument();
  });

  it.each(["close", "unmount"])("cancels the first frame with ID zero on %s", async (action) => {
    nextID = 0;
    const view = render(dialog(true));
    await settlePreview();
    expect(frames.has(0)).toBe(true);
    if (action === "close") view.rerender(dialog(false));
    else view.unmount();
    expect(frames.size).toBe(0);
  });

  it.each(["close", "unmount"])("cancels the second frame with ID zero on %s", async (action) => {
    const view = render(dialog(true));
    await settlePreview();
    nextID = 0; // A browser RAF counter may wrap to zero.
    paint();
    expect(frames.has(0)).toBe(true);
    if (action === "close") view.rerender(dialog(false));
    else view.unmount();
    expect(frames.size).toBe(0);
    if (action === "close") {
      view.rerender(dialog(true));
      await settlePreview();
      paint();
      expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
      paint();
      expect(screen.getByTestId("preview-chart")).toBeInTheDocument();
    }
  });

  it("owns only the current frame during StrictMode effect replay", async () => {
    nextID = 0;
    const view = render(<StrictMode>{dialog(true)}</StrictMode>);
    await settlePreview();
    expect(frames.size).toBe(1);
    expect(frames.has(0)).toBe(false);
    paint();
    expect(screen.queryByTestId("preview-chart")).not.toBeInTheDocument();
    expect(frames.size).toBe(1);
    view.unmount();
    expect(frames.size).toBe(0);
  });
});
