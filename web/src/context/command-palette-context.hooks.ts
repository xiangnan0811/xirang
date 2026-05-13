import { useContext } from "react";
import { CommandPaletteContext } from "@/context/command-palette-context.shared";

export function useCommandPalette() {
  const ctx = useContext(CommandPaletteContext);
  if (!ctx) throw new Error("useCommandPalette must be used within CommandPaletteProvider");
  return ctx;
}
