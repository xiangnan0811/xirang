import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { InlineAlert } from "./inline-alert";

describe("InlineAlert", () => {
  it("does not render role=alert by default (static usage)", () => {
    render(<InlineAlert>静态提示</InlineAlert>);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("静态提示")).toBeDefined();
  });

  it("renders role=alert when live prop is true", () => {
    render(<InlineAlert live>动态告警</InlineAlert>);
    expect(screen.getByRole("alert")).toBeDefined();
    expect(screen.getByText("动态告警")).toBeDefined();
  });

  it("does not render role=alert when live is false", () => {
    render(<InlineAlert live={false}>普通信息</InlineAlert>);
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
