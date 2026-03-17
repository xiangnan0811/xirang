import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type PropsWithChildren
} from "react";
import i18n from "@/i18n";
import {
  applyDensityData,
  applyPowerData,
  applyThemeClass,
  clearStoredTheme,
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
  const initialStoredTheme = getStoredTheme();
  const [theme, setTheme] = useState<ThemeMode>(() =>
    resolveInitialTheme(initialStoredTheme, resolveSystemDarkMode())
  );
  const [themeSource, setThemeSource] = useState<"system" | "manual">(
    initialStoredTheme ? "manual" : "system"
  );
  const [density, setDensity] = useState<DensityMode>(() =>
    resolveInitialDensity(getStoredDensity())
  );
  const [powerMode, setPowerMode] = useState<PowerMode>(() =>
    resolveInitialPowerMode(getStoredPowerMode(), resolveReducedMotionPreference())
  );

  useEffect(() => {
    applyThemeClass(document.documentElement, theme);
    if (themeSource === "manual") {
      persistTheme(theme);
    } else {
      clearStoredTheme();
    }
  }, [theme, themeSource]);

  useEffect(() => {
    if (themeSource !== "system") {
      return;
    }
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (event: MediaQueryListEvent) => {
      setTheme(event.matches ? "dark" : "light");
    };
    media.addEventListener("change", onChange);
    return () => {
      media.removeEventListener("change", onChange);
    };
  }, [themeSource]);

  useEffect(() => {
    applyDensityData(document.documentElement, density);
    persistDensity(density);
  }, [density]);

  useEffect(() => {
    applyPowerData(document.documentElement, powerMode);
    persistPowerMode(powerMode);
  }, [powerMode]);

  const setManualTheme = useCallback((nextTheme: ThemeMode) => {
    setThemeSource("manual");
    setTheme(nextTheme);
  }, []);

  const toggleTheme = useCallback(() => {
    setThemeSource("manual");
    setTheme((prev) => (prev === "dark" ? "light" : "dark"));
  }, []);
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
      setTheme: setManualTheme,
      toggleTheme,
      density,
      setDensity,
      toggleDensity,
      powerMode,
      setPowerMode,
      togglePowerMode
    }),
    [density, powerMode, setManualTheme, theme, toggleDensity, togglePowerMode, toggleTheme]
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  const context = useContext(ThemeContext);
  if (!context) {
    throw new Error(i18n.t("context.useThemeError"));
  }
  return context;
}
