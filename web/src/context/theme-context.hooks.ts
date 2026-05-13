import { useContext } from "react";
import i18n from "@/i18n";
import { ThemeContext } from "@/context/theme-context.shared";

export function useTheme() {
  const context = useContext(ThemeContext);
  if (!context) {
    throw new Error(i18n.t("context.useThemeError"));
  }
  return context;
}
