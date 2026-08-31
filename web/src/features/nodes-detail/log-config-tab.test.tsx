import "@testing-library/jest-dom/vitest";
import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import LogConfigTab from "./log-config-tab";

const { mockGetNodeLogConfig, mockUpdateNodeLogConfig } = vi.hoisted(() => ({
  mockGetNodeLogConfig: vi.fn(),
  mockUpdateNodeLogConfig: vi.fn(),
}));

vi.mock("@/lib/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/client")>("@/lib/api/client");
  return {
    ...actual,
    apiClient: {
      ...actual.apiClient,
      getNodeLogConfig: mockGetNodeLogConfig,
      updateNodeLogConfig: mockUpdateNodeLogConfig,
    },
  };
});

const { mockToastSuccess, mockToastError } = vi.hoisted(() => ({
  mockToastSuccess: vi.fn(),
  mockToastError: vi.fn(),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: mockToastSuccess, error: mockToastError },
}));

const BASE_CONFIG = {
  logPaths: ["/var/log/nginx/access.log"],
  logJournalctlEnabled: true,
  logRetentionDays: 14,
};

describe("LogConfigTab", () => {
  beforeEach(() => {
    mockGetNodeLogConfig.mockResolvedValue(BASE_CONFIG);
    mockUpdateNodeLogConfig.mockResolvedValue(BASE_CONFIG);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  test("renders with loaded config values", async () => {
    render(<LogConfigTab nodeId={1} token="test-token" />);

    expect(await screen.findByDisplayValue("/var/log/nginx/access.log")).toBeInTheDocument();
    expect(screen.getByDisplayValue("14")).toBeInTheDocument();
    const switchEl = screen.getByRole("switch");
    expect(switchEl).toHaveAttribute("data-state", "checked");
    expect(mockGetNodeLogConfig).toHaveBeenCalledWith("test-token", 1, expect.objectContaining({
      signal: expect.any(AbortSignal),
    }));
  });

  test("save succeeds and calls updateNodeLogConfig with correct shape", async () => {
    render(<LogConfigTab nodeId={1} token="test-token" />);
    await screen.findByDisplayValue("/var/log/nginx/access.log");

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => {
      expect(mockUpdateNodeLogConfig).toHaveBeenCalledWith("test-token", 1, {
        logPaths: ["/var/log/nginx/access.log"],
        logJournalctlEnabled: true,
        logRetentionDays: 14,
      });
    });
    expect(mockToastSuccess).toHaveBeenCalled();
  });

  test("invalid relative path shows validation error without calling updateNodeLogConfig", async () => {
    render(<LogConfigTab nodeId={1} token="test-token" />);
    const textarea = await screen.findByDisplayValue("/var/log/nginx/access.log");
    fireEvent.change(textarea, { target: { value: "relative/path/here" } });

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));

    // Validation error renders inline (role="alert"), not via toast.
    const alert = await screen.findByRole("alert");
    expect(alert).toBeInTheDocument();
    expect(textarea).toHaveAttribute("aria-invalid", "true");
    expect(mockToastError).not.toHaveBeenCalled();
    expect(mockUpdateNodeLogConfig).not.toHaveBeenCalled();
  });

  test("toggle journalctl off and save calls mock with logJournalctlEnabled false", async () => {
    render(<LogConfigTab nodeId={1} token="test-token" />);
    await screen.findByDisplayValue("/var/log/nginx/access.log");

    const switchEl = screen.getByRole("switch");
    fireEvent.click(switchEl);

    mockUpdateNodeLogConfig.mockResolvedValue({ ...BASE_CONFIG, logJournalctlEnabled: false });

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => {
      expect(mockUpdateNodeLogConfig).toHaveBeenCalledWith("test-token", 1, expect.objectContaining({
        logJournalctlEnabled: false,
      }));
    });
  });

  test("skips loading and saving when token is missing", () => {
    render(<LogConfigTab nodeId={1} token={null} />);
    expect(mockGetNodeLogConfig).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));
    expect(mockUpdateNodeLogConfig).not.toHaveBeenCalled();
  });
});
