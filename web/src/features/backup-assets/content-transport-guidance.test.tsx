import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ContentTransportGuidance } from "./content-transport-guidance";

describe("ContentTransportGuidance", () => {
  it("gives Admin an accessible action to the exact Backups target", () => {
    render(<ContentTransportGuidance authRole="admin" />);
    expect(screen.getByRole("link", { name: /content transport|内容传输/i })).toHaveAttribute(
      "href",
      "/app/backups/overview#backup-assets-content-transport",
    );
  });

  it("gives Operator HTTPS-or-contact-Admin guidance without a settings action", () => {
    render(<ContentTransportGuidance authRole="operator" />);
    expect(screen.getByText(/HTTPS/i)).toBeInTheDocument();
    expect(screen.getByText(/Admin|管理员/i)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("keeps Viewer guidance generic without privilege inference", () => {
    render(<ContentTransportGuidance authRole="viewer" />);
    expect(screen.queryByText(/Admin|管理员|settings|设置/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
