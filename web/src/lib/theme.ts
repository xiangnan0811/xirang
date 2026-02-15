export type ThemeMode = "light" | "dark";

export const THEME_STORAGE_KEY = "xirang-theme";

const VALID_THEMES: ThemeMode[] = ["light", "dark"];

export function getStoredTheme(): ThemeMode | null {
  const value = localStorage.getItem(THEME_STORAGE_KEY);
  if (!value) {
    return null;
  }
  return VALID_THEMES.includes(value as ThemeMode) ? (value as ThemeMode) : null;
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

export function persistTheme(theme: ThemeMode) {
  localStorage.setItem(THEME_STORAGE_KEY, theme);
}

export function applyThemeClass(root: HTMLElement, theme: ThemeMode) {
  root.classList.toggle("dark", theme === "dark");
}
