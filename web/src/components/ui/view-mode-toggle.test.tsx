import "@testing-library/jest-dom/vitest";
import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ViewModeToggle, type ViewMode } from "@/components/ui/view-mode-toggle";

describe("ViewModeToggle", () => {
  it("使用 radiogroup 语义并触发切换回调", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    render(
      <ViewModeToggle
        value="cards"
        onChange={onChange}
        groupLabel="任务视图切换"
        cardsButtonLabel="任务卡片视图"
        listButtonLabel="任务列表视图"
      />
    );

    expect(
      screen.getByRole("radiogroup", { name: "任务视图切换" })
    ).toBeInTheDocument();

    const cardsButton = screen.getByRole("radio", { name: "任务卡片视图" });
    const listButton = screen.getByRole("radio", { name: "任务列表视图" });

    expect(cardsButton).toHaveAttribute("aria-checked", "true");
    expect(listButton).toHaveAttribute("aria-checked", "false");

    await user.click(listButton);
    expect(onChange).toHaveBeenCalledWith("list");
  });

  it("受控更新后 aria-checked 同步变化", async () => {
    const user = userEvent.setup();

    function ControlledToggle() {
      const [mode, setMode] = useState<ViewMode>("cards");
      return (
        <ViewModeToggle
          value={mode}
          onChange={setMode}
          groupLabel="审计视图切换"
        />
      );
    }

    render(<ControlledToggle />);

    const cardsButton = screen.getByRole("radio", { name: "卡片" });
    const listButton = screen.getByRole("radio", { name: "列表" });

    expect(cardsButton).toHaveAttribute("aria-checked", "true");
    expect(listButton).toHaveAttribute("aria-checked", "false");

    await user.click(listButton);

    expect(cardsButton).toHaveAttribute("aria-checked", "false");
    expect(listButton).toHaveAttribute("aria-checked", "true");
  });

  it("roving tabindex：当前选中项 tabIndex=0，其余为 -1", () => {
    render(
      <ViewModeToggle
        value="list"
        onChange={vi.fn()}
        groupLabel="视图切换"
      />
    );

    const cardsButton = screen.getByRole("radio", { name: "卡片" });
    const listButton = screen.getByRole("radio", { name: "列表" });

    expect(cardsButton).toHaveAttribute("tabindex", "-1");
    expect(listButton).toHaveAttribute("tabindex", "0");
  });

  it("ArrowRight 键切换到下一个选项并触发 onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    render(
      <ViewModeToggle
        value="cards"
        onChange={onChange}
        groupLabel="视图切换"
      />
    );

    const cardsButton = screen.getByRole("radio", { name: "卡片" });
    cardsButton.focus();
    await user.keyboard("{ArrowRight}");

    expect(onChange).toHaveBeenCalledWith("list");
  });

  it("ArrowLeft 键切换到上一个选项并触发 onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    render(
      <ViewModeToggle
        value="list"
        onChange={onChange}
        groupLabel="视图切换"
      />
    );

    const listButton = screen.getByRole("radio", { name: "列表" });
    listButton.focus();
    await user.keyboard("{ArrowLeft}");

    expect(onChange).toHaveBeenCalledWith("cards");
  });
});
