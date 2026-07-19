import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Tree, type TreeItemData } from "../tree";

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "tree.treeViewLabel": "tree-view",
        "tree.expand": "展开",
        "tree.collapse": "折叠",
        "tree.select": "选中",
      };
      return map[key] ?? key;
    },
  }),
}));

function makeItems(): TreeItemData[] {
  return [
    {
      id: "root",
      label: "root",
      isDir: true,
      children: [
        { id: "root/child-a", label: "child-a", isDir: false },
        { id: "root/child-b", label: "child-b", isDir: false },
      ],
    },
    {
      id: "lazy",
      label: "lazy",
      isDir: true,
      // 没有 children，触发 onLoadChildren
    },
  ];
}

describe("Tree", () => {
  it("uses one roving tab stop across all visible tree items", async () => {
    render(<Tree items={makeItems()} />);

    fireEvent.click(screen.getByRole("button", { name: "展开 root" }));
    await screen.findByText("child-a");

    const buttons = screen.getAllByRole("button");
    expect(buttons.filter((button) => button.tabIndex === 0)).toHaveLength(1);
    expect(screen.getByRole("button", { name: "折叠 root" })).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("button", { name: "选中 child-a" })).toHaveAttribute("tabindex", "-1");
  });

  it("moves through visible items with ArrowUp/Down and Home/End", async () => {
    render(<Tree items={makeItems()} />);
    const root = screen.getByRole("button", { name: "展开 root" });
    fireEvent.click(root);
    const childA = await screen.findByRole("button", { name: "选中 child-a" });
    const childB = screen.getByRole("button", { name: "选中 child-b" });
    const lazy = screen.getByRole("button", { name: "展开 lazy" });

    root.focus();
    fireEvent.keyDown(root, { key: "ArrowDown" });
    expect(childA).toHaveFocus();
    fireEvent.keyDown(childA, { key: "ArrowDown" });
    expect(childB).toHaveFocus();
    fireEvent.keyDown(childB, { key: "End" });
    expect(lazy).toHaveFocus();
    fireEvent.keyDown(lazy, { key: "Home" });
    expect(root).toHaveFocus();
    fireEvent.keyDown(root, { key: "ArrowUp" });
    expect(root).toHaveFocus();
  });

  it("uses ArrowRight for expand/first child and ArrowLeft for collapse/parent", async () => {
    render(<Tree items={makeItems()} />);
    const root = screen.getByRole("button", { name: "展开 root" });
    root.focus();

    fireEvent.keyDown(root, { key: "ArrowRight" });
    const expandedRoot = await screen.findByRole("button", { name: "折叠 root" });
    expect(expandedRoot).toHaveFocus();

    fireEvent.keyDown(expandedRoot, { key: "ArrowRight" });
    const childA = screen.getByRole("button", { name: "选中 child-a" });
    expect(childA).toHaveFocus();

    fireEvent.keyDown(childA, { key: "ArrowLeft" });
    expect(expandedRoot).toHaveFocus();
    fireEvent.keyDown(expandedRoot, { key: "ArrowLeft" });
    await waitFor(() => expect(screen.queryByText("child-a")).toBeNull());
    expect(screen.getByRole("button", { name: "展开 root" })).toHaveFocus();
  });

  it("keeps focus on a lazy directory when loading completes", async () => {
    let resolveChildren!: (items: TreeItemData[]) => void;
    const onLoadChildren = vi.fn(
      () =>
        new Promise<TreeItemData[]>((resolve) => {
          resolveChildren = resolve;
        })
    );
    render(<Tree items={[{ id: "lazy", label: "lazy", isDir: true }]} onLoadChildren={onLoadChildren} />);
    const lazy = screen.getByRole("button", { name: "展开 lazy" });
    lazy.focus();

    fireEvent.keyDown(lazy, { key: "ArrowRight" });
    expect(lazy).toHaveFocus();
    resolveChildren([{ id: "lazy/child", label: "loaded-child" }]);
    await screen.findByText("loaded-child");
    expect(screen.getByRole("button", { name: "折叠 lazy" })).toHaveFocus();
  });

  it("restores focus to the nearest visible parent when a focused node is removed", async () => {
    const expanded = new Set(["root"]);
    const { rerender } = render(<Tree items={makeItems()} expanded={expanded} />);
    const childB = screen.getByRole("button", { name: "选中 child-b" });
    childB.focus();
    fireEvent.focus(childB);

    const nextItems = makeItems();
    nextItems[0] = {
      ...nextItems[0],
      children: [{ id: "root/child-a", label: "child-a", isDir: false }],
    };
    rerender(<Tree items={nextItems} expanded={expanded} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "折叠 root" })).toHaveFocus();
    });
  });

  it("preserves controlled selection/expansion callbacks with keyboard navigation", () => {
    const onToggle = vi.fn();
    render(
      <Tree
        items={makeItems()}
        selected="root/child-a"
        expanded={new Set(["root"])}
        onToggle={onToggle}
      />
    );
    const childA = screen.getByRole("button", { name: "选中 child-a" });
    expect(childA).toHaveAttribute("tabindex", "0");
    childA.focus();
    fireEvent.keyDown(childA, { key: "ArrowLeft" });
    const root = screen.getByRole("button", { name: "折叠 root" });
    expect(root).toHaveFocus();
    fireEvent.keyDown(root, { key: "ArrowLeft" });
    expect(onToggle).toHaveBeenCalledWith(expect.objectContaining({ id: "root" }));
  });

  it("toggle expand 不会 mutate 入参 items 的 children 引用", async () => {
    const items = makeItems();
    const childrenBefore = items[0].children;
    expect(childrenBefore).toBeDefined();

    render(<Tree items={items} />);

    // 展开 root（已有内联 children），不会触发 onLoadChildren
    const rootButton = screen.getByRole("button", { name: "展开 root" });
    fireEvent.click(rootButton);

    // 子节点出现
    expect(await screen.findByText("child-a")).toBeDefined();
    expect(screen.getByText("child-b")).toBeDefined();

    // 关键断言：原始 items 的 children 引用不应被改变
    expect(items[0].children).toBe(childrenBefore);
  });

  it("toggle 同一节点两次回到未展开状态", async () => {
    const items = makeItems();
    render(<Tree items={items} />);

    const rootButton = screen.getByRole("button", { name: "展开 root" });

    // 第一次展开
    fireEvent.click(rootButton);
    expect(await screen.findByText("child-a")).toBeDefined();

    // 第二次折叠（展开后 button 的 aria-label 变为 "折叠 root"）
    const collapseButton = screen.getByRole("button", { name: "折叠 root" });
    fireEvent.click(collapseButton);
    await waitFor(() => {
      expect(screen.queryByText("child-a")).toBeNull();
    });
  });

  it("懒加载结果写入内部缓存而非 mutate item.children", async () => {
    const items: TreeItemData[] = [
      { id: "lazy", label: "lazy", isDir: true },
    ];
    const lazyItemBefore = items[0];
    const childrenSnapshot = items[0].children; // undefined

    const onLoadChildren = vi.fn(async (item: TreeItemData): Promise<TreeItemData[]> => {
      return [
        { id: `${item.id}/loaded-1`, label: "loaded-1", isDir: false },
        { id: `${item.id}/loaded-2`, label: "loaded-2", isDir: false },
      ];
    });

    render(<Tree items={items} onLoadChildren={onLoadChildren} />);

    const lazyButton = screen.getByRole("button", { name: "展开 lazy" });
    fireEvent.click(lazyButton);

    // 等待懒加载完成 + 子节点渲染
    expect(await screen.findByText("loaded-1")).toBeDefined();
    expect(screen.getByText("loaded-2")).toBeDefined();

    // 原始 item 引用未变
    expect(items[0]).toBe(lazyItemBefore);
    // children 字段没有被 mutate（仍为 undefined）
    expect(items[0].children).toBe(childrenSnapshot);
    expect(items[0].children).toBeUndefined();

    // onLoadChildren 只被调用一次
    expect(onLoadChildren).toHaveBeenCalledTimes(1);
  });

  it("折叠后再次展开使用缓存，不重复触发 onLoadChildren", async () => {
    const items: TreeItemData[] = [
      { id: "lazy", label: "lazy", isDir: true },
    ];

    const onLoadChildren = vi.fn(async (): Promise<TreeItemData[]> => {
      return [{ id: "lazy/cached", label: "cached", isDir: false }];
    });

    render(<Tree items={items} onLoadChildren={onLoadChildren} />);

    // 第一次展开 → 触发加载
    fireEvent.click(screen.getByRole("button", { name: "展开 lazy" }));
    expect(await screen.findByText("cached")).toBeDefined();

    // 折叠
    fireEvent.click(screen.getByRole("button", { name: "折叠 lazy" }));
    await waitFor(() => {
      expect(screen.queryByText("cached")).toBeNull();
    });

    // 第二次展开 → 应使用缓存，不再调 onLoadChildren
    fireEvent.click(screen.getByRole("button", { name: "展开 lazy" }));
    expect(await screen.findByText("cached")).toBeDefined();
    expect(onLoadChildren).toHaveBeenCalledTimes(1);
  });

  it("toggle 多个节点时各自展开状态独立维护", async () => {
    const items: TreeItemData[] = [
      {
        id: "a",
        label: "a",
        isDir: true,
        children: [{ id: "a/1", label: "a-child", isDir: false }],
      },
      {
        id: "b",
        label: "b",
        isDir: true,
        children: [{ id: "b/1", label: "b-child", isDir: false }],
      },
    ];

    render(<Tree items={items} />);

    const aButton = screen.getByRole("button", { name: "展开 a" });
    const bButton = screen.getByRole("button", { name: "展开 b" });

    fireEvent.click(aButton);
    expect(await screen.findByText("a-child")).toBeDefined();

    fireEvent.click(bButton);
    expect(await screen.findByText("b-child")).toBeDefined();

    // 同时展开两个
    expect(screen.getByText("a-child")).toBeDefined();
    expect(screen.getByText("b-child")).toBeDefined();
  });
});
