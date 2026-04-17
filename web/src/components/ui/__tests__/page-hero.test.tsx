import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { PageHero } from "../page-hero";

describe("PageHero", () => {
  it("renders title as h1", () => {
    render(<PageHero title="Nodes" />);
    expect(screen.getByRole("heading", { level: 1, name: "Nodes" })).toBeDefined();
  });

  it("renders subtitle when provided", () => {
    render(<PageHero title="Nodes" subtitle="Manage your servers" />);
    expect(screen.getByText("Manage your servers")).toBeDefined();
  });

  it("renders actions when provided", () => {
    render(<PageHero title="Tasks" actions={<button>Add task</button>} />);
    expect(screen.getByRole("button", { name: "Add task" })).toBeDefined();
  });

  it("omits subtitle element when not provided", () => {
    const { container } = render(<PageHero title="Overview" />);
    expect(container.querySelector("p")).toBeNull();
  });
});
