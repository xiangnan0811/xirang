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

vi.mock("@/components/ui/toast", () => ({
  toast: { success: mockToastSuccess, error: mockToastError },
}));

const BASE_CONFIG = {
  log_paths: ["/var/log/nginx/access.log"],
  log_journalctl_enabled: true,
  log_retention_days: 14,
};

describe("LogConfigTab", () => {
  beforeEach(() => {
    sessionStorage.setItem("xirang-auth-token", "test-token");
    mockGetNodeLogConfig.mockResolvedValue(BASE_CONFIG);
    mockUpdateNodeLogConfig.mockResolvedValue(BASE_CONFIG);
  });

  afterEach(() => {
    sessionStorage.removeItem("xirang-auth-token");
    vi.clearAllMocks();
  });

  test("renders with loaded config values", async () => {
    render(<LogConfigTab nodeId={1} />);

    expect(await screen.findByDisplayValue("/var/log/nginx/access.log")).toBeInTheDocument();
    expect(screen.getByDisplayValue("14")).toBeInTheDocument();
    const switchEl = screen.getByRole("switch");
    expect(switchEl).toHaveAttribute("data-state", "checked");
  });

  test("save succeeds and calls updateNodeLogConfig with correct shape", async () => {
    render(<LogConfigTab nodeId={1} />);
    await screen.findByDisplayValue("/var/log/nginx/access.log");

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => {
      expect(mockUpdateNodeLogConfig).toHaveBeenCalledWith("test-token", 1, {
        log_paths: ["/var/log/nginx/access.log"],
        log_journalctl_enabled: true,
        log_retention_days: 14,
      });
    });
    expect(mockToastSuccess).toHaveBeenCalled();
  });

  test("invalid relative path shows validation error without calling updateNodeLogConfig", async () => {
    render(<LogConfigTab nodeId={1} />);
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

  test("toggle journalctl off and save calls mock with log_journalctl_enabled false", async () => {
    render(<LogConfigTab nodeId={1} />);
    await screen.findByDisplayValue("/var/log/nginx/access.log");

    const switchEl = screen.getByRole("switch");
    fireEvent.click(switchEl);

    mockUpdateNodeLogConfig.mockResolvedValue({ ...BASE_CONFIG, log_journalctl_enabled: false });

    fireEvent.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => {
      expect(mockUpdateNodeLogConfig).toHaveBeenCalledWith("test-token", 1, expect.objectContaining({
        log_journalctl_enabled: false,
      }));
    });
  });
});
