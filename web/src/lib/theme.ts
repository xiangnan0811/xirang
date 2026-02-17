export type ThemeMode = "light" | "dark";
export type DensityMode = "comfortable" | "compact";
export type PowerMode = "normal" | "save";

export const THEME_STORAGE_KEY = "xirang-theme";
export const DENSITY_STORAGE_KEY = "xirang-density";
export const POWER_MODE_STORAGE_KEY = "xirang-power-mode";

const VALID_THEMES: ThemeMode[] = ["light", "dark"];
const VALID_DENSITY: DensityMode[] = ["comfortable", "compact"];
const VALID_POWER_MODE: PowerMode[] = ["normal", "save"];

export function getStoredTheme(): ThemeMode | null {
  const value = localStorage.getItem(THEME_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_THEMES.includes(value as ThemeMode) ? (value as ThemeMode) : null;
}

export function getStoredDensity(): DensityMode | null {
  const value = localStorage.getItem(DENSITY_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_DENSITY.includes(value as DensityMode) ? (value as DensityMode) : null;
}

export function getStoredPowerMode(): PowerMode | null {
  const value = localStorage.getItem(POWER_MODE_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_POWER_MODE.includes(value as PowerMode) ? (value as PowerMode) : null;
}

export function resolveInitialTheme(
  storedTheme: string | null,
  prefersDarkMode: boolean
): ThemeMode {
  if (storedTheme && VALID_THEMES.includes(storedTheme as ThemeMode)) {
    return storedTheme as ThemeMode;
  }
  return prefersDarkMode ? "dark" : "light";
}

export function resolveInitialDensity(storedDensity: string | null): DensityMode {
  if (storedDensity && VALID_DENSITY.includes(storedDensity as DensityMode)) {
    return storedDensity as DensityMode;
  }
  return "comfortable";
}

export function resolveInitialPowerMode(
  storedPowerMode: string | null,
  prefersReducedMotion: boolean
): PowerMode {
  if (storedPowerMode && VALID_POWER_MODE.includes(storedPowerMode as PowerMode)) {
    return storedPowerMode as PowerMode;
  }
  return prefersReducedMotion ? "save" : "normal";
}

export function persistTheme(theme: ThemeMode) {
  localStorage.setItem(THEME_STORAGE_KEY, theme);
}

export function persistDensity(density: DensityMode) {
  localStorage.setItem(DENSITY_STORAGE_KEY, density);
}

export function persistPowerMode(powerMode: PowerMode) {
  localStorage.setItem(POWER_MODE_STORAGE_KEY, powerMode);
}

export function applyThemeClass(root: HTMLElement, theme: ThemeMode) {
  root.classList.toggle("dark", theme === "dark");
}

export function applyDensityData(root: HTMLElement, density: DensityMode) {
  root.dataset.density = density;
}

export function applyPowerData(root: HTMLElement, powerMode: PowerMode) {
  root.dataset.power = powerMode;
}
