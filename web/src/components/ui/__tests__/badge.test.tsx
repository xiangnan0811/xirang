import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Badge } from "../badge";

describe("Badge", () => {
  it("renders label text", () => {
    render(<Badge tone="success">Online</Badge>);
    expect(screen.getByText("Online")).toBeDefined();
  });

  it("renders without dot when dot=false", () => {
    const { container } = render(<Badge tone="info" dot={false}>Running</Badge>);
    // No aria-hidden dot span
    const dots = container.querySelectorAll("span[aria-hidden]");
    expect(dots.length).toBe(0);
  });

  it("renders dot by default", () => {
    const { container } = render(<Badge tone="warning">Warning</Badge>);
    const dots = container.querySelectorAll("span[aria-hidden]");
    expect(dots.length).toBe(1);
  });

  it("renders neutral tone", () => {
    render(<Badge tone="neutral">Disabled</Badge>);
    expect(screen.getByText("Disabled")).toBeDefined();
  });
});
