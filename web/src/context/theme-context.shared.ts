import { createContext } from "react";
import type { DensityMode, PowerMode, ThemeMode } from "@/lib/theme";

export type AccentColor = "emerald" | "blue" | "violet" | "amber" | "pink";

export type ThemeContextValue = {
  theme: ThemeMode;
  setTheme: (theme: ThemeMode) => void;
  toggleTheme: () => void;
  density: DensityMode;
  setDensity: (density: DensityMode) => void;
  toggleDensity: () => void;
  powerMode: PowerMode;
  setPowerMode: (mode: PowerMode) => void;
  togglePowerMode: () => void;
  accentColor: AccentColor;
  setAccentColor: (color: AccentColor) => void;
};

export const ThemeContext = createContext<ThemeContextValue | null>(null);
