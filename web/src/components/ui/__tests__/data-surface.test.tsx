import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceFooter,
  DataSurfaceHeader,
  DataSurfaceToolbar,
} from "../data-surface";

describe("DataSurface", () => {
  it("renders header, toolbar and content slots", () => {
    render(
      <DataSurface>
        <DataSurfaceHeader
          title="Nodes"
          description="Fleet inventory"
          actions={<button>Refresh</button>}
        />
        <DataSurfaceToolbar>
          <label>
            Search
            <input />
          </label>
        </DataSurfaceToolbar>
        <DataSurfaceContent>
          <p>Rows</p>
        </DataSurfaceContent>
        <DataSurfaceFooter>
          <p>Pagination</p>
        </DataSurfaceFooter>
      </DataSurface>
    );

    expect(screen.getByRole("heading", { level: 2, name: "Nodes" })).toBeInTheDocument();
    expect(screen.getByText("Fleet inventory")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.getByLabelText("Search")).toBeInTheDocument();
    expect(screen.getByText("Rows")).toBeInTheDocument();
    expect(screen.getByText("Pagination")).toBeInTheDocument();
  });

  it("supports flat workbench surfaces", () => {
    const { container } = render(
      <DataSurface variant="flat">
        <DataSurfaceContent>Rows</DataSurfaceContent>
      </DataSurface>
    );

    expect(container.querySelector("section")).toHaveAttribute("data-variant", "flat");
  });
});
