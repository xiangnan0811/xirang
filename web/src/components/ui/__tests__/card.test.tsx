import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from "../card";

describe("Card", () => {
  it("renders Card with CardTitle text", () => {
    render(
      <Card>
        <CardHeader>
          <CardTitle>My Card</CardTitle>
        </CardHeader>
      </Card>
    );
    expect(screen.getByText("My Card")).toBeDefined();
  });

  it("renders CardDescription text", () => {
    render(
      <Card>
        <CardHeader>
          <CardDescription>A description</CardDescription>
        </CardHeader>
      </Card>
    );
    expect(screen.getByText("A description")).toBeDefined();
  });

  it("renders CardContent and CardFooter", () => {
    render(
      <Card>
        <CardContent>Body text</CardContent>
        <CardFooter>Footer text</CardFooter>
      </Card>
    );
    expect(screen.getByText("Body text")).toBeDefined();
    expect(screen.getByText("Footer text")).toBeDefined();
  });

  it("does not apply hover lift globally unless the caller opts in", () => {
    const { rerender } = render(<Card data-testid="card">Body text</Card>);
    expect(screen.getByTestId("card")).not.toHaveClass("card-lift");

    rerender(
      <Card data-testid="card" className="card-lift">
        Body text
      </Card>
    );
    expect(screen.getByTestId("card")).toHaveClass("card-lift");
  });
});
