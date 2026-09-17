import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { StatusPage } from "./status-page";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  poll: null as (() => void) | null,
  language: "en",
}));

vi.mock("@/lib/api/service-monitors", () => ({
  createServiceMonitorsApi: () => ({ getStatusPage: state.request }),
}));
vi.mock("@/hooks/use-visibility-polling", () => ({
  useVisibilityPolling: (callback: () => void) => { state.poll = callback; },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => `${state.language}:${key}`, i18n: { language: state.language } }),
}));

describe("StatusPage public requests", () => {
  beforeEach(() => {
    state.request.mockReset();
    state.language = "en";
  });

  it("ends initial loading on error, translates locally, and recovers on polling", async () => {
    state.request.mockRejectedValueOnce(new Error("unavailable"));
    const { rerender } = render(<StatusPage />);
    expect(await screen.findByText("en:serviceMonitor.statusPageLoadFailed")).toBeInTheDocument();
    state.language = "zh";
    rerender(<StatusPage />);
    expect(screen.getByText("zh:serviceMonitor.statusPageLoadFailed")).toBeInTheDocument();
    expect(state.request).toHaveBeenCalledTimes(1);
    state.request.mockResolvedValueOnce([
      { name: "Public endpoint", type: "http", status: "up", uptimePct: 100 },
    ]);
    await act(async () => { state.poll?.(); });
    expect(await screen.findByText("Public endpoint")).toBeInTheDocument();
    expect(screen.queryByText("zh:serviceMonitor.statusPageLoadFailed")).not.toBeInTheDocument();
  });
});
