import "@testing-library/jest-dom/vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";

import { PrivateNetworkContentTransportPanel } from "./private-network-content-transport-panel";

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((done, failed) => {
    resolve = done;
    reject = failed;
  });
  return { promise, resolve, reject };
}

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("PrivateNetworkContentTransportPanel", () => {
  it("loads an inherited source and keeps the labelled control axe-clean", async () => {
    const api = { get: vi.fn().mockResolvedValue({ enabled: false, source: "env" }), update: vi.fn() };
    const { container } = render(<PrivateNetworkContentTransportPanel token="admin-token" api={api} />);

    expect(screen.getByRole("status")).toHaveTextContent(/Loading|加载/);
    const toggle = await screen.findByRole("switch", { name: /private network HTTP|私有网络 HTTP/i });
    expect(toggle).not.toBeChecked();
    expect(screen.getByText(/Environment|环境变量/)).toBeInTheDocument();
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("requires confirmation to enable, writes exactly once, and announces the saved warning", async () => {
    const user = userEvent.setup();
    const pending = deferred();
    const api = {
      get: vi.fn().mockResolvedValue({ enabled: false, source: "default" }),
      update: vi.fn().mockReturnValue(pending.promise),
    };
    render(<PrivateNetworkContentTransportPanel token="admin-token" api={api} />);
    const toggle = await screen.findByRole("switch", { name: /private network HTTP|私有网络 HTTP/i });

    await user.click(toggle);
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveTextContent(/preview|预览/i);
    await user.click(screen.getByRole("button", { name: /Cancel|取消/ }));
    expect(api.update).not.toHaveBeenCalled();

    await user.click(toggle);
    await user.click(screen.getByRole("button", { name: /Enable|启用/ }));
    expect(api.update).toHaveBeenCalledTimes(1);
    expect(api.update).toHaveBeenCalledWith("admin-token", true, expect.any(AbortSignal));
    expect(toggle).toBeDisabled();
    await user.click(toggle);
    expect(api.update).toHaveBeenCalledTimes(1);

    await act(async () => pending.resolve());
    expect(await screen.findByRole("status")).toHaveTextContent(/Saved|已保存/);
    expect(screen.getByRole("alert")).toHaveTextContent(/unencrypted|明文/i);
    expect(toggle).toBeChecked();
  });

  it("disables without confirmation and rolls back a failed mutation", async () => {
    const user = userEvent.setup();
    const api = {
      get: vi.fn().mockResolvedValue({ enabled: true, source: "db" }),
      update: vi.fn().mockRejectedValue(new Error("secret server detail")),
    };
    render(<PrivateNetworkContentTransportPanel token="admin-token" api={api} />);
    const toggle = await screen.findByRole("switch", { name: /private network HTTP|私有网络 HTTP/i });
    expect(toggle).toBeChecked();

    await user.click(toggle);
    await waitFor(() => expect(api.update).toHaveBeenCalledWith("admin-token", false, expect.any(AbortSignal)));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(await screen.findByText(/could not be saved|无法保存/)).toBeInTheDocument();
    expect(screen.getAllByRole("alert").map((item) => item.textContent).join(" ")).not.toContain("secret server detail");
    expect(toggle).toBeChecked();
  });

  it("focuses the stable heading for the approved static hash target", async () => {
    window.history.replaceState(null, "", "/app/backups/overview#backup-assets-content-transport");
    const api = { get: vi.fn().mockResolvedValue({ enabled: false, source: "default" }), update: vi.fn() };
    render(<PrivateNetworkContentTransportPanel token="admin-token" api={api} />);

    const heading = await screen.findByRole("heading", { name: /private network HTTP|私有网络 HTTP/i });
    await waitFor(() => expect(heading).toHaveFocus());
    expect(heading.closest("section")).toHaveAttribute("id", "backup-assets-content-transport");
  });
});
