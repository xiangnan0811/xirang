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

    const successCard = screen.getByText("在线节点").closest(".glass-card");
    expect(successCard).not.toBeNull();
    expect(successCard).toHaveClass("border-success/30");

    expect(screen.getByText("失败任务")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();

    const layout = container.querySelector("section");
    expect(layout).not.toBeNull();
    expect(layout).toHaveStyle({
      gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    });

    const description = screen.getByText("健康率 92%");
    expect(description).toHaveClass("hidden", "sm:block");

    const infoCard = screen.getByText("失败任务").closest(".glass-card");
    expect(infoCard).not.toBeNull();
    expect(infoCard).toHaveClass("border-info/30");
  });
});
