import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { StatusPage } from "./status-page";
import type { StatusPageItem } from "@/types/domain";

vi.mock("@/lib/api/service-monitors", () => ({
  createServiceMonitorsApi: () => ({
    getStatusPage: vi.fn().mockResolvedValue([
      { name: "up-svc", type: "http", status: "up", uptime_pct: 99.9, last_checked_at: null },
      { name: "down-svc", type: "http", status: "down", uptime_pct: 0, last_checked_at: null },
    ] as StatusPageItem[]),
  }),
}));

function renderStatusPage() {
  return render(
    <I18nextProvider i18n={i18n}>
      <StatusPage />
    </I18nextProvider>
  );
}

describe("StatusPage status color tokens", () => {
  it("does not render raw emerald-500/red-500 palette classes", async () => {
    const { container } = renderStatusPage();

    await waitFor(() => {
      expect(container.textContent).toContain("up-svc");
    });

    const html = container.innerHTML;
    expect(html).not.toContain("emerald-500");
    expect(html).not.toContain("red-500");
  });

  it("uses semantic success/destructive token classes for up/down states", async () => {
    const { container } = renderStatusPage();

    await waitFor(() => {
      expect(container.textContent).toContain("up-svc");
    });

    const html = container.innerHTML;
    expect(html).toContain("success");
    expect(html).toContain("destructive");
  });
});