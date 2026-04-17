import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { EmptyState } from "../empty-state";

describe("EmptyState", () => {
  it("renders title text", () => {
    render(<EmptyState title="No results found" />);
    expect(screen.getByText("No results found")).toBeDefined();
  });

  it("renders description when provided", () => {
    render(<EmptyState title="Empty" description="Try adjusting your filters." />);
    expect(screen.getByText("Try adjusting your filters.")).toBeDefined();
  });

  it("renders action when provided", () => {
    render(<EmptyState title="Empty" action={<button>Add item</button>} />);
    expect(screen.getByRole("button", { name: "Add item" })).toBeDefined();
  });

  it("omits description when not provided", () => {
    const { container } = render(<EmptyState title="Empty" />);
    // Only the title div, no description paragraph
    expect(container.querySelectorAll(".text-muted-foreground").length).toBe(0);
  });
});
