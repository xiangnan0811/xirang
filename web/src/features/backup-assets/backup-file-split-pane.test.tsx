import "@testing-library/jest-dom/vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackupFileSplitPane } from "./backup-file-split-pane";

describe("BackupFileSplitPane", () => {
  beforeEach(() => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1200 });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, top: 0, left: 0, right: 1200, bottom: 600, width: 1200, height: 600,
      toJSON: () => ({}),
    } as DOMRect);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.documentElement.style.removeProperty("font-size");
  });

  it("starts at 42/58 and exposes an accessible keyboard separator in 2 percent steps", () => {
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);
    const separator = screen.getByRole("separator");
    expect(separator).toHaveAttribute("aria-orientation", "vertical");
    expect(separator).toHaveAttribute("aria-valuenow", "42");
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(separator).toHaveAttribute("aria-valuenow", "44");
    fireEvent.keyDown(separator, { key: "ArrowLeft" });
    expect(separator).toHaveAttribute("aria-valuenow", "42");
    expect(separator.className).toContain("focus-visible:ring");
  });

  it("supports pointer dragging while enforcing both 20rem and 30rem minimum panes", () => {
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);
    const separator = screen.getByRole("separator");
    fireEvent.pointerDown(separator, { pointerId: 1, clientX: 504 });
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX: 0 }));
    expect(separator).toHaveAttribute("aria-valuenow", separator.getAttribute("aria-valuemin"));
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX: 900 }));
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX: 1200 }));
    fireEvent.pointerUp(window, { pointerId: 1 });
    expect(separator).toHaveAttribute("aria-valuenow", separator.getAttribute("aria-valuemax"));
  });

  it("ends a drag on pointer cancellation and ignores later pointer movement", () => {
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);
    const separator = screen.getByRole("separator");
    fireEvent.pointerDown(separator, { pointerId: 1, clientX: 504 });
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX: 600 }));
    expect(separator).toHaveAttribute("aria-valuenow", "50");
    fireEvent(window, new Event("pointercancel", { bubbles: true }));
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX: 900 }));

    expect(separator).toHaveAttribute("aria-valuenow", "50");
  });

  it("uses the measured container instead of viewport width for its sequential fallback", () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 500 });
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);

    expect(screen.getByRole("separator")).toBeInTheDocument();
  });

  it("clamps the current ratio when a measured resize tightens pane limits", async () => {
    vi.stubGlobal("ResizeObserver", undefined);
    let measuredWidth = 1200;
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => ({
      x: 0, y: 0, top: 0, left: 0, right: measuredWidth, bottom: 600, width: measuredWidth, height: 600,
      toJSON: () => ({}),
    } as DOMRect));
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);
    const separator = screen.getByRole("separator");
    for (let index = 0; index < 20; index += 1) fireEvent.keyDown(separator, { key: "ArrowRight" });
    measuredWidth = 900;
    fireEvent(window, new Event("resize"));

    await vi.waitFor(() => {
      expect(Number(separator.getAttribute("aria-valuenow"))).toBeLessThanOrEqual(Number(separator.getAttribute("aria-valuemax")));
    });
  });

  it("enters focused reading without remounting the browser and restores its exact ratio, scroll, and trigger focus", async () => {
    const user = userEvent.setup();
    render(
      <BackupFileSplitPane
        browser={<div data-testid="browser-scroll">Browser</div>}
        preview={<div>Preview</div>}
        previewActive
        onBack={vi.fn()}
      />,
    );
    const separator = screen.getByRole("separator");
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    const browser = screen.getByTestId("browser-scroll");
    browser.scrollTop = 177;
    const trigger = screen.getByRole("button", { name: /Focused reading|专注阅读/ });
    expect(trigger).toHaveClass("touch-target");
    expect(trigger.parentElement).toHaveClass("min-h-11", "lg:min-h-10");
    await user.click(trigger);
    const exit = screen.getByRole("button", { name: /Exit focused reading|退出专注阅读/ });
    expect(exit).toHaveClass("touch-target");
    expect(exit).toHaveFocus();
    expect(screen.queryByRole("button", { name: /^(Focused reading|专注阅读)$/ })).not.toBeInTheDocument();
    expect(screen.getByTestId("browser-scroll")).toBe(browser);
    expect(browser).not.toBeVisible();
    expect(browser.scrollTop).toBe(177);
    await user.click(exit);
    expect(screen.getByTestId("browser-scroll")).toBe(browser);
    expect(browser).toBeVisible();
    expect(browser.scrollTop).toBe(177);
    expect(screen.getByRole("separator")).toHaveAttribute("aria-valuenow", "44");
    expect(trigger).toHaveFocus();
  });

  it("uses the sequential layout when 200 percent zoom makes the rem clamps exceed the container", () => {
    document.documentElement.style.fontSize = "32px";

    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive={false} onBack={vi.fn()} />);

    expect(screen.getByText("Browser")).toBeVisible();
    expect(screen.queryByRole("separator")).not.toBeInTheDocument();
    expect(screen.getByText("Browser").closest("[data-layout]"))?.toHaveAttribute("data-layout", "sequential");
  });

  it("resets its ratio on remount without writing layout state to browser storage", () => {
    const localSetItem = vi.spyOn(Storage.prototype, "setItem");
    const first = render(
      <BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />,
    );
    const separator = screen.getByRole("separator");
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(separator).toHaveAttribute("aria-valuenow", "44");
    first.unmount();

    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={vi.fn()} />);

    expect(screen.getByRole("separator")).toHaveAttribute("aria-valuenow", "42");
    expect(localSetItem).not.toHaveBeenCalled();
  });

  it("uses a sequential browser to full-width preview flow below the shared minimum", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 700 });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, top: 0, left: 0, right: 700, bottom: 600, width: 700, height: 600,
      toJSON: () => ({}),
    } as DOMRect);
    const onBack = vi.fn();
    const { rerender } = render(
      <BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive={false} onBack={onBack} />,
    );
    expect(screen.getByText("Browser")).toBeInTheDocument();
    expect(screen.queryByRole("separator")).not.toBeInTheDocument();
    rerender(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive onBack={onBack} />);
    expect(screen.queryByText("Browser")).not.toBeInTheDocument();
    const back = screen.getByRole("button", { name: /Back to files|返回文件/ });
    expect(back).toHaveClass("min-h-11", "touch-target");
    await userEvent.click(back);
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("does not offer focused reading without an active preview", () => {
    render(<BackupFileSplitPane browser={<div>Browser</div>} preview={<div>Preview</div>} previewActive={false} onBack={vi.fn()} />);

    expect(screen.getByRole("button", { name: /Focused reading|专注阅读/ })).toBeDisabled();
  });
});
