export type ThemeMode = "light" | "dark";
export type DensityMode = "comfortable" | "compact";
export type PowerMode = "normal" | "save";

export const THEME_STORAGE_KEY = "xirang-theme";
export const DENSITY_STORAGE_KEY = "xirang-density";
export const POWER_MODE_STORAGE_KEY = "xirang-power-mode";

const VALID_THEMES: ThemeMode[] = ["light", "dark"];
const VALID_DENSITY: DensityMode[] = ["comfortable", "compact"];
const VALID_POWER_MODE: PowerMode[] = ["normal", "save"];

function safeGetItem(key: string): string | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function safeSetItem(key: string, value: string): void {
  if (typeof window === "undefined") {
    return;
  }
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // 忽略 Safari 隐私模式、配额或受限环境下的存储异常。
  }
}

function safeRemoveItem(key: string): void {
  if (typeof window === "undefined") {
    return;
  }
  try {
    window.localStorage.removeItem(key);
  } catch {
    // 忽略受限环境下的存储异常。
  }
}

export function getStoredTheme(): ThemeMode | null {
  const value = safeGetItem(THEME_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_THEMES.includes(value as ThemeMode) ? (value as ThemeMode) : null;
}

export function getStoredDensity(): DensityMode | null {
  const value = safeGetItem(DENSITY_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_DENSITY.includes(value as DensityMode) ? (value as DensityMode) : null;
}

export function getStoredPowerMode(): PowerMode | null {
  const value = safeGetItem(POWER_MODE_STORAGE_KEY);
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
  safeSetItem(THEME_STORAGE_KEY, theme);
}

export function clearStoredTheme() {
  safeRemoveItem(THEME_STORAGE_KEY);
}

export function persistDensity(density: DensityMode) {
  safeSetItem(DENSITY_STORAGE_KEY, density);
}

export function persistPowerMode(powerMode: PowerMode) {
  safeSetItem(POWER_MODE_STORAGE_KEY, powerMode);
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
