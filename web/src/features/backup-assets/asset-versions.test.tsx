import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { recoveryPoint } from "./__tests__/test-utils";
import { AssetVersions } from "./asset-versions";

describe("AssetVersions", () => {
  it("shows only the exact producing lineage and truthful unavailable expansion", () => {
    render(<AssetVersions recoveryPoint={recoveryPoint} />);

    expect(screen.getByText(recoveryPoint.producingTaskName)).toBeInTheDocument();
    expect(screen.getByText(/2026-07-19 00:00/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/not deployed|未部署/);
    expect(screen.queryByText(/latest/i)).not.toBeInTheDocument();
  });
});
