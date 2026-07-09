import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { NodeMigrateWizard } from "./node-migrate-wizard";

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    migrateNodePreflight: vi.fn(),
    migrateNode: vi.fn(),
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
  },
}));

describe("NodeMigrateWizard", () => {
  it("labels the step indicator with localized copy", () => {
    render(
      <NodeMigrateWizard
        open
        onOpenChange={vi.fn()}
        sourceNode={{ id: 1, name: "source", host: "10.0.0.1", status: "online" }}
        nodes={[
          { id: 1, name: "source", host: "10.0.0.1", status: "online" },
          { id: 2, name: "target", host: "10.0.0.2", status: "online" },
        ]}
        token="token"
        onSuccess={vi.fn()}
      />
    );

    expect(screen.getByRole("navigation", { name: "迁移步骤" })).toBeInTheDocument();
  });
});
