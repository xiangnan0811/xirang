import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import zh from "./locales/zh";
import en from "./locales/en";

const STORAGE_KEY = "xirang.language";

function detectLanguage(): string {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === "en" || stored === "zh") return stored;
  const nav = navigator.language;
  if (nav.startsWith("zh")) return "zh";
  return "en";
}

i18n.use(initReactI18next).init({
  resources: {
    zh: { translation: zh },
    en: { translation: en },
  },
  lng: detectLanguage(),
  fallbackLng: "zh",
  interpolation: { escapeValue: false },
});

export function setLanguage(lng: "zh" | "en") {
  localStorage.setItem(STORAGE_KEY, lng);
  i18n.changeLanguage(lng);
}

export function getLanguage(): "zh" | "en" {
  return (i18n.language ?? "zh") as "zh" | "en";
}

export default i18n;
