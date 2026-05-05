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

// Wave 4 PR-B：将 i18next 内部语言代码映射为 BCP 47 lang 属性值
// （WCAG 3.1.1/3.1.2 — 页面与片段必须声明正确的 lang）
function mapLangToHtml(lng: string): string {
  if (lng?.startsWith("zh")) return "zh-CN";
  return "en";
}

function syncDocumentLang(lng: string) {
  if (typeof document === "undefined") return;
  document.documentElement.lang = mapLangToHtml(lng);
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

// 初始化后立即同步 <html lang>，并在切换语言时同步
syncDocumentLang(i18n.language);
i18n.on("languageChanged", syncDocumentLang);

export function setLanguage(lng: "zh" | "en") {
  localStorage.setItem(STORAGE_KEY, lng);
  i18n.changeLanguage(lng);
}

export function getLanguage(): "zh" | "en" {
  return (i18n.language ?? "zh") as "zh" | "en";
}

export default i18n;
