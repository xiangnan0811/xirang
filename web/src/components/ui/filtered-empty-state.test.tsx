import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Plus } from "lucide-react";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";

describe("FilteredEmptyState", () => {
  it("默认动作支持重置与新建", async () => {
    const user = userEvent.setup();
    const onReset = vi.fn();
    const onCreate = vi.fn();

    render(
      <FilteredEmptyState
        title="暂无任务"
        description="请先创建任务"
        onReset={onReset}
        onCreate={onCreate}
        createLabel="新建任务"
        createIcon={Plus}
      />
    );

    await user.click(screen.getByRole("button", { name: "重置筛选" }));
    await user.click(screen.getByRole("button", { name: "新建任务" }));

    expect(onReset).toHaveBeenCalledTimes(1);
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it("传入自定义 action 时覆盖内置动作", () => {
    render(
      <FilteredEmptyState
        title="暂无节点"
        action={<button type="button">自定义动作</button>}
        onReset={() => undefined}
      />
    );

    expect(screen.getByRole("button", { name: "自定义动作" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重置筛选" })).not.toBeInTheDocument();
  });
});
