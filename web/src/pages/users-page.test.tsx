import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UsersPage } from "./users-page";

const state = vi.hoisted(() => ({
  auth: { token: "token", role: "admin", username: "operator", userId: 1, logout: vi.fn() },
  getUsers: vi.fn(),
  t: (key: string) => key,
}));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => state.auth }));
vi.mock("@/lib/api/client", () => ({ apiClient: { getUsers: state.getUsers } }));
vi.mock("@/hooks/use-confirm", () => ({ useConfirm: () => ({ confirm: vi.fn(), dialog: null }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: state.t }) }));

describe("UsersPage authorization changes", () => {
  beforeEach(() => {
    state.auth.role = "admin";
    state.getUsers.mockReset();
  });

  it("discards pending admin inventory on role loss and fetches fresh on role restoration", async () => {
    let resolveOld!: (rows: { id: number; username: string; role: string }[]) => void;
    state.getUsers.mockReturnValueOnce(new Promise((resolve) => { resolveOld = resolve; }));
    const { rerender } = render(<MemoryRouter><UsersPage /></MemoryRouter>);
    state.auth.role = "viewer";
    rerender(<MemoryRouter><UsersPage /></MemoryRouter>);
    await act(async () => { resolveOld([{ id: 2, username: "stale-user", role: "admin" }]); });
    expect(screen.queryByText("stale-user")).not.toBeInTheDocument();
    expect(state.getUsers).toHaveBeenCalledTimes(1);
    state.getUsers.mockResolvedValueOnce([{ id: 3, username: "fresh-user", role: "viewer" }]);
    state.auth.role = "admin";
    rerender(<MemoryRouter><UsersPage /></MemoryRouter>);
    expect(await screen.findByText("fresh-user")).toBeInTheDocument();
    expect(screen.queryByText("stale-user")).not.toBeInTheDocument();
    expect(state.getUsers).toHaveBeenCalledTimes(2);
  });
});
