import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { runAxe } from "@/test/a11y-helpers";

describe("StatCardsSection", () => {
  it("渲染统计卡片内容并应用 tone 样式", () => {
    const { container } = render(
      <StatCardsSection
        items={[
          {
            title: "在线节点",
            value: 12,
            description: "健康率 92%",
            tone: "success",
          },
          {
            title: "失败任务",
            value: 3,
          },
        ]}
      />
    );

    expect(screen.getByText("在线节点")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.getByText("健康率 92%")).toBeInTheDocument();

    const successCard = screen.getByText("在线节点").closest("[data-tone]");
    expect(successCard).not.toBeNull();
    expect(successCard).toHaveAttribute("data-tone", "success");

    expect(screen.getByText("失败任务")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();

    const layout = container.querySelector("section");
    expect(layout).not.toBeNull();
    expect(layout).toHaveClass("grid-cols-1", "sm:grid-cols-2", "xl:grid-cols-4");
    expect(layout).not.toHaveStyle({
      gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    });

    const description = screen.getByText("健康率 92%");
    expect(description).toHaveClass("mt-2", "text-xs", "font-medium");

    const infoCard = screen.getByText("失败任务").closest("[data-tone]");
    expect(infoCard).not.toBeNull();
    expect(infoCard).toHaveAttribute("data-tone", "info");
  });

  it("渲染 unit 后缀标注", () => {
    render(
      <StatCardsSection
        items={[
          {
            title: "节点健康率",
            value: 95,
            unit: "%",
            tone: "success",
          },
        ]}
      />
    );

    expect(screen.getByText("节点健康率")).toBeInTheDocument();
    expect(screen.getByText("95")).toBeInTheDocument();
    expect(screen.getByText("%")).toBeInTheDocument();
  });

  it("渲染 icon + unit + description 组合", () => {
    render(
      <StatCardsSection
        items={[
          {
            title: "当前吞吐",
            value: 128,
            unit: "Mbps",
            icon: <span data-testid="throughput-icon">📊</span>,
            description: "近 5 分钟平均值",
            tone: "primary",
          },
        ]}
      />
    );

    expect(screen.getByText("当前吞吐")).toBeInTheDocument();
    expect(screen.getByText("128")).toBeInTheDocument();
    expect(screen.getByText("Mbps")).toBeInTheDocument();
    expect(screen.getByTestId("throughput-icon")).toBeInTheDocument();
    expect(screen.getByText("近 5 分钟平均值")).toBeInTheDocument();

    const card = screen.getByText("当前吞吐").closest("[data-tone]");
    expect(card).toHaveAttribute("data-tone", "primary");
  });

  it("为 5 个指标提供桌面五列布局但保留小屏断点", () => {
    const { container } = render(
      <StatCardsSection
        items={[
          { title: "A", value: 1 },
          { title: "B", value: 2 },
          { title: "C", value: 3 },
          { title: "D", value: 4 },
          { title: "E", value: 5 },
        ]}
      />
    );

    const layout = container.querySelector("section");
    expect(layout).toHaveClass("grid-cols-1", "sm:grid-cols-2", "xl:grid-cols-4", "2xl:grid-cols-5");
    expect(layout).not.toHaveAttribute("style");
  });

  it("紧凑模式在移动端使用双列以减少首屏滚动", () => {
    const { container } = render(
      <StatCardsSection
        compact
        items={[
          { title: "待执行", value: 1 },
          { title: "成功", value: 2 },
          { title: "运行中", value: 3 },
          { title: "失败", value: 4 },
        ]}
      />
    );

    const layout = container.querySelector("section");
    expect(layout).toHaveClass("grid-cols-2", "sm:grid-cols-2", "xl:grid-cols-4");
  });

  it("紧凑模式仅有单个卡片时在移动端回退为单列而非半屏留白", () => {
    const { container, rerender } = render(
      <StatCardsSection
        compact
        items={[{ title: "策略覆盖", value: 4 }]}
      />
    );

    const layout = container.querySelector("section");
    expect(layout).toHaveClass("grid-cols-1", "sm:grid-cols-2", "xl:grid-cols-4");
    expect(layout).not.toHaveClass("grid-cols-2");

    rerender(
      <StatCardsSection
        compact
        items={[
          { title: "策略覆盖", value: 4 },
          { title: "节点总数", value: 8 },
        ]}
      />
    );
    expect(layout).toHaveClass("grid-cols-2");
    expect(layout).not.toHaveClass("grid-cols-1");
  });

  it("静态统计卡片不具备交互角色或悬停抬升误导样式", async () => {
    const user = userEvent.setup();
    render(
      <StatCardsSection
        items={[
          { title: "在线节点", value: 12, tone: "success" },
          { title: "空白链接降级", value: 0, href: "   " },
        ]}
      />
    );

    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();

    const staticCard = screen.getByText("在线节点").closest("[data-tone]") as HTMLElement;
    expect(staticCard).not.toBeNull();
    expect(staticCard.tagName).toBe("DIV");
    expect(staticCard).not.toHaveAttribute("tabindex");
    expect(staticCard).not.toHaveClass(
      "cursor-pointer",
      "hover:border-primary/30",
      "hover:shadow-[var(--shadow-panel-hover)]"
    );

    const blankHrefCard = screen.getByText("空白链接降级").closest("[data-tone]") as HTMLElement;
    expect(blankHrefCard.tagName).toBe("DIV");

    await waitFor(() => {
      expect(staticCard).toHaveStyle({ opacity: "1", transform: "none" });
    });

    await user.hover(staticCard);
    fireEvent.mouseEnter(staticCard);
    expect(staticCard).toHaveStyle({ transform: "none" });
  });

  it("传入 onClick 时渲染为原生 button 卡片并保留交互样式、悬停抬升与键盘触发", async () => {
    const user = userEvent.setup();
    const handleClick = vi.fn();

    render(
      <StatCardsSection
        items={[
          {
            title: "失败任务",
            value: 3,
            tone: "destructive",
            onClick: handleClick,
          },
        ]}
      />
    );

    const buttonCard = screen.getByRole("button", { name: /失败任务/ });
    expect(buttonCard).toHaveAttribute("type", "button");
    expect(buttonCard).toHaveAttribute("data-tone", "destructive");
    expect(buttonCard).toHaveClass(
      "cursor-pointer",
      "hover:border-primary/30",
      "hover:shadow-[var(--shadow-panel-hover)]",
      "focus-visible:ring-[3px]"
    );

    await waitFor(() => {
      expect(buttonCard).toHaveStyle({ opacity: "1", transform: "none" });
    });

    await user.hover(buttonCard);
    await waitFor(() => {
      expect(buttonCard.style.transform).toContain("translateY(-");
    });

    await user.click(buttonCard);
    expect(handleClick).toHaveBeenCalledTimes(1);

    buttonCard.focus();
    await user.keyboard("{Enter}");
    expect(handleClick).toHaveBeenCalledTimes(2);
  });

  it("传入 href 时渲染为 link 卡片并在路由内支持 SPA 跳转", async () => {
    const user = userEvent.setup();

    const standalone = render(
      <StatCardsSection
        items={[
          {
            title: "外部状态页",
            value: "OK",
            href: "https://status.example.com",
          },
        ]}
      />
    );

    const externalLink = screen.getByRole("link", { name: /外部状态页/ });
    expect(externalLink).toHaveAttribute("href", "https://status.example.com");
    expect(externalLink).toHaveClass(
      "cursor-pointer",
      "hover:border-primary/30",
      "hover:shadow-[var(--shadow-panel-hover)]"
    );
    standalone.unmount();

    render(
      <MemoryRouter initialEntries={["/app/overview"]}>
        <Routes>
          <Route
            path="/app/overview"
            element={
              <StatCardsSection
                items={[
                  {
                    title: "告警中心",
                    value: 5,
                    tone: "warning",
                    href: "/app/notifications",
                  },
                ]}
              />
            }
          />
          <Route path="/app/notifications" element={<div>通知列表页</div>} />
        </Routes>
      </MemoryRouter>
    );

    const internalLink = screen.getByRole("link", { name: /告警中心/ });
    expect(internalLink).toHaveAttribute("href", "/app/notifications");
    await user.click(internalLink);
    expect(screen.getByText("通知列表页")).toBeInTheDocument();
  });

  it("supports as='div' when wrapped inside an existing section landmark", () => {
    const { container } = render(
      <section aria-label="外层区域">
        <StatCardsSection as="div" items={[{ title: "静态指标", value: 10 }]} />
      </section>
    );

    expect(container.querySelectorAll("section")).toHaveLength(1);
    expect(screen.getByText("静态指标")).toBeInTheDocument();
  });

  it("静态与交互卡片组合均无 axe 可访问性违规", async () => {
    const { container } = render(
      <MemoryRouter>
        <StatCardsSection
          items={[
            { title: "静态指标", value: 10 },
            { title: "按钮指标", value: 2, onClick: vi.fn() },
            { title: "链接指标", value: 5, href: "/app/nodes" },
          ]}
        />
      </MemoryRouter>
    );

    expect(await runAxe(container)).toHaveNoViolations();
  });
});
