import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type PropsWithChildren
} from "react";
import {
  applyDensityData,
  applyPowerData,
  applyThemeClass,
  getStoredDensity,
  getStoredPowerMode,
  getStoredTheme,
  persistDensity,
  persistPowerMode,
  persistTheme,
  resolveInitialDensity,
  resolveInitialPowerMode,
  resolveInitialTheme,
  type DensityMode,
  type PowerMode,
  type ThemeMode
} from "@/lib/theme";

type ThemeContextValue = {
  theme: ThemeMode;
  setTheme: (theme: ThemeMode) => void;
  toggleTheme: () => void;
  density: DensityMode;
  setDensity: (density: DensityMode) => void;
  toggleDensity: () => void;
  powerMode: PowerMode;
  setPowerMode: (mode: PowerMode) => void;
  togglePowerMode: () => void;
};

const ThemeContext = createContext<ThemeContextValue | null>(null);

function resolveSystemDarkMode() {
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function resolveReducedMotionPreference() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function ThemeProvider({ children }: PropsWithChildren) {
  const [theme, setTheme] = useState<ThemeMode>(() =>
    resolveInitialTheme(getStoredTheme(), resolveSystemDarkMode())
  );
  const [density, setDensity] = useState<DensityMode>(() =>
    resolveInitialDensity(getStoredDensity())
  );
  const [powerMode, setPowerMode] = useState<PowerMode>(() =>
    resolveInitialPowerMode(getStoredPowerMode(), resolveReducedMotionPreference())
  );

  useEffect(() => {
    applyThemeClass(document.documentElement, theme);
    persistTheme(theme);
  }, [theme]);

  useEffect(() => {
    applyDensityData(document.documentElement, density);
    persistDensity(density);
  }, [density]);

  useEffect(() => {
    applyPowerData(document.documentElement, powerMode);
    persistPowerMode(powerMode);
  }, [powerMode]);

  const toggleTheme = useCallback(
    () => setTheme((prev) => (prev === "dark" ? "light" : "dark")),
    []
  );
  const toggleDensity = useCallback(
    () => setDensity((prev) => (prev === "compact" ? "comfortable" : "compact")),
    []
  );
  const togglePowerMode = useCallback(
    () => setPowerMode((prev) => (prev === "save" ? "normal" : "save")),
    []
  );

  const value = useMemo(
    () => ({
      theme,
      setTheme,
      toggleTheme,
      density,
      setDensity,
      toggleDensity,
      powerMode,
      setPowerMode,
      togglePowerMode
    }),
    [density, powerMode, theme, toggleDensity, togglePowerMode, toggleTheme]
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  const context = useContext(ThemeContext);
  if (!context) {
    throw new Error("useTheme 必须在 ThemeProvider 中使用");
  }
  return context;
}
