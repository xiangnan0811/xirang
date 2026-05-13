import { createContext } from "react";

export interface CommandPaletteContextValue {
  open: boolean;
  setOpen: (open: boolean) => void;
  toggle: () => void;
}

export const CommandPaletteContext = createContext<CommandPaletteContextValue | null>(null);
