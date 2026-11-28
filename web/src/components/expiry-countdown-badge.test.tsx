import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { ExpiryCountdownBadge } from "./expiry-countdown-badge";

const EXPIRY = "2026-06-15T12:00:00.000Z";
const EXPIRY_MS = Date.parse(EXPIRY);
const DAY_MS = 24 * 60 * 60 * 1000;

describe("ExpiryCountdownBadge", () => {
  beforeEach(async () => {
    window.localStorage.setItem("xirang.language", "zh");
    await setLanguage("zh");
    vi.useFakeTimers({ toFake: ["Date"] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows one remaining day just before the expiry instant", () => {
    vi.setSystemTime(EXPIRY_MS - 1);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);

    expect(screen.getByText("还剩 1 天")).toHaveClass("text-destructive");
    expect(screen.queryByText("已到期")).not.toBeInTheDocument();
  });

  it("shows expired exactly at the expiry instant", () => {
    vi.setSystemTime(EXPIRY_MS);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);

    expect(screen.getByText("已到期")).toHaveClass("text-destructive");
    expect(screen.queryByText(/还剩/)).not.toBeInTheDocument();
  });

  it("shows expired immediately after the expiry instant instead of zero days left", () => {
    vi.setSystemTime(EXPIRY_MS + 1);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);

    expect(screen.getByText("已到期")).toBeInTheDocument();
    expect(screen.queryByText("还剩 0 天")).not.toBeInTheDocument();
  });

  it("shows expired anywhere within the last day after expiry", () => {
    vi.setSystemTime(EXPIRY_MS + DAY_MS - 1);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);

    expect(screen.getByText("已到期")).toBeInTheDocument();
    expect(screen.queryByText(/还剩/)).not.toBeInTheDocument();
  });

  it("keeps the remaining-day color windows", () => {
    vi.setSystemTime(EXPIRY_MS - 3 * DAY_MS);
    const { unmount } = render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);
    expect(screen.getByText("还剩 3 天")).toHaveClass("text-destructive");
    unmount();

    vi.setSystemTime(EXPIRY_MS - 7 * DAY_MS);
    const second = render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);
    expect(screen.getByText("还剩 7 天")).toHaveClass("text-warning");
    second.unmount();

    vi.setSystemTime(EXPIRY_MS - 7 * DAY_MS - 1);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} />);
    expect(screen.getByText("还剩 8 天")).toHaveClass("text-success");
  });

  it("shows archived instead of expired when the node is archived", () => {
    vi.setSystemTime(EXPIRY_MS + 1);
    render(<ExpiryCountdownBadge expiryDate={EXPIRY} archived />);

    expect(screen.getByText("已归档")).toHaveClass("text-muted-foreground");
    expect(screen.queryByText("已到期")).not.toBeInTheDocument();
  });

  it("renders nothing for a missing or invalid expiry date", () => {
    vi.setSystemTime(EXPIRY_MS);
    const { container, rerender } = render(<ExpiryCountdownBadge />);
    expect(container).toBeEmptyDOMElement();

    rerender(<ExpiryCountdownBadge expiryDate="not-a-date" />);
    expect(container).toBeEmptyDOMElement();
  });
});
