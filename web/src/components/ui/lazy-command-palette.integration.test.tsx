import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { CommandPaletteProvider } from "@/context/command-palette-context";
import { useCommandPalette } from "@/context/command-palette-context.hooks";
import { LazyCommandPalette } from "./lazy-command-palette";

vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => ({ role: "admin" }) }));
vi.mock("@/context/nodes-context.hooks", () => ({ useNodesContextOptional: () => null }));
vi.mock("@/context/tasks-context.hooks", () => ({ useTasksContextOptional: () => null }));

function Trigger() {
  const { toggle } = useCommandPalette();
  return <button onClick={toggle}>Search</button>;
}

it("focuses the real lazy dialog, closes on Escape, and clears search on reopen", async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const user = userEvent.setup();
  render(<MemoryRouter><CommandPaletteProvider><Trigger /><LazyCommandPalette /></CommandPaletteProvider></MemoryRouter>);
  const trigger = screen.getByRole("button", { name: "Search" });
  await user.click(trigger);
  const input = await screen.findByRole("combobox");
  await waitFor(() => expect(input).toHaveFocus());
  await user.type(input, "nodes");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await user.keyboard("{Control>}k{/Control}");
  expect(await screen.findByRole("combobox")).toHaveValue("");
  await waitFor(() => expect(screen.getByRole("combobox")).toHaveFocus());
});
