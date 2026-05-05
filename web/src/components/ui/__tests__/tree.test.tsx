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
