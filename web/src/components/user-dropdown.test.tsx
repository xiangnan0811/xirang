import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { apiClient } from "@/lib/api/client";
import { UserDropdown } from "./user-dropdown";

const { logoutMock } = vi.hoisted(() => ({
  logoutMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    username: "alice",
    role: "admin",
    token: "token-1",
    logout: logoutMock,
  }),
}));

function PathProbe() {
  const location = useLocation();
  return <div data-testid="path">{location.pathname}</div>;
}

beforeAll(() => {
  HTMLElement.prototype.hasPointerCapture = () => false;
  HTMLElement.prototype.setPointerCapture = () => undefined;
  HTMLElement.prototype.releasePointerCapture = () => undefined;
  Element.prototype.scrollIntoView = () => undefined;
});

describe("UserDropdown logout", () => {
  it("clears the local session before the backend logout request", async () => {
    const order: string[] = [];
    let resolve!: () => void;
    const promise = new Promise<void>((done) => {
      resolve = done;
    });
    logoutMock.mockImplementation(() => {
      order.push("local");
    });
    vi.spyOn(apiClient, "logout").mockImplementation((token: string) => {
      order.push(`api:${token}`);
      return promise;
    });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/app"]}>
        <UserDropdown />
        <PathProbe />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("button", { name: "alice" }));
    await user.click(await screen.findByRole("menuitem", { name: /退出登录|Sign out|appShell\.logout/ }));

    expect(order).toEqual(["local", "api:token-1"]);
    expect(screen.getByTestId("path")).toHaveTextContent("/app");
    resolve();
    await waitFor(() => {
      expect(screen.getByTestId("path")).toHaveTextContent("/login");
    });
  });
});
