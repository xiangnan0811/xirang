import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { CommandPaletteProvider } from "@/context/command-palette-context";
import { LazyCommandPalette } from "./lazy-command-palette";

const load = vi.hoisted(() => ({ attempts: 0 }));
vi.mock("./command-palette", () => {
  load.attempts += 1;
  if (load.attempts === 1) throw new Error("Network failed");
  return { CommandPalette: () => <div role="dialog">Recovered</div> };
});

it("offers a retry after a failed import without breaking the shell", async () => {
  const user = userEvent.setup();
  render(<CommandPaletteProvider><LazyCommandPalette /></CommandPaletteProvider>);
  await user.keyboard("{Control>}k{/Control}");
  expect(await screen.findByRole("alert")).toBeInTheDocument();
  await user.click(screen.getByRole("button"));
  expect(await screen.findByRole("dialog")).toHaveTextContent("Recovered");
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
