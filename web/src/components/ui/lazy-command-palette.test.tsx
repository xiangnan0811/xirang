import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { CommandPaletteProvider } from "@/context/command-palette-context";
import { useCommandPalette } from "@/context/command-palette-context.hooks";
import { LazyCommandPalette } from "./lazy-command-palette";

const pending = vi.hoisted(() => {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve, imported: vi.fn(), mounted: vi.fn() };
});

vi.mock("./command-palette", async () => {
  pending.imported();
  await pending.promise;
  const React = await import("react");
  const { useCommandPalette: usePalette } = await import("@/context/command-palette-context.hooks");
  return {
    CommandPalette: function Palette() {
      const { open } = usePalette();
      React.useEffect(() => { pending.mounted(); }, []);
      return open ? <div role="dialog">Loaded palette</div> : null;
    },
  };
});

function Trigger() {
  const { toggle } = useCommandPalette();
  return <button onClick={toggle}>Search</button>;
}

it("loads only on demand, can close while loading, and preserves the mounted palette on reopen", async () => {
  const user = userEvent.setup();
  render(<CommandPaletteProvider><Trigger /><LazyCommandPalette /></CommandPaletteProvider>);
  expect(pending.imported).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(pending.imported).toHaveBeenCalledOnce());
  expect(screen.getByRole("status")).toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
  await act(async () => { pending.resolve(); await pending.promise; });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await user.keyboard("{Control>}k{/Control}");
  expect(await screen.findByRole("dialog")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Search" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Search" }));
  expect(await screen.findByRole("dialog")).toBeInTheDocument();
  expect(pending.mounted).toHaveBeenCalledOnce();
});
