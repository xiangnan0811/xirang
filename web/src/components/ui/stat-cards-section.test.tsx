import "@testing-library/jest-dom/vitest";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatCardsSection } from "@/components/ui/stat-cards-section";

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

    const successCard = screen.getByText("在线节点").closest(".glass-panel");
    expect(successCard).not.toBeNull();
    expect(successCard).toHaveAttribute("data-tone", "success");

    expect(screen.getByText("失败任务")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();

    const layout = container.querySelector("section");
    expect(layout).not.toBeNull();
    expect(layout).toHaveStyle({
      gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    });

    const description = screen.getByText("健康率 92%");
    expect(description).toHaveClass("hidden", "sm:block");

    const infoCard = screen.getByText("失败任务").closest(".glass-panel");
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

    const card = screen.getByText("当前吞吐").closest(".glass-panel");
    expect(card).toHaveAttribute("data-tone", "primary");
  });
});
