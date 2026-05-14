import i18n, { type BackendModule, type ReadCallback, type ResourceKey } from "i18next";
import { initReactI18next } from "react-i18next";

const STORAGE_KEY = "xirang.language";
export type SupportedLanguage = "zh" | "en";

const SUPPORTED_LANGUAGES = ["zh", "en"] as const;

const localeLoaders: Record<SupportedLanguage, () => Promise<ResourceKey>> = {
  zh: () => import("./locales/zh").then((module) => module.default),
  en: () => import("./locales/en").then((module) => module.default),
};

function normalizeLanguage(lng: string | null | undefined): SupportedLanguage {
  return lng?.startsWith("zh") ? "zh" : "en";
}

function detectLanguage(): SupportedLanguage {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === "en" || stored === "zh") return stored;
  const nav = navigator.language;
  return normalizeLanguage(nav);
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

const localeBackend: BackendModule = {
  type: "backend",
  init() {},
  read(language: string, _namespace: string, callback: ReadCallback) {
    const normalizedLanguage = normalizeLanguage(language);
    localeLoaders[normalizedLanguage]()
      .then((resources) => callback(null, resources))
      .catch((error: unknown) => {
        const message = error instanceof Error ? error : new Error(String(error));
        callback(message, null);
      });
  },
};

const initialLanguage = detectLanguage();

export const i18nReady = i18n.use(localeBackend).use(initReactI18next).init({
  lng: initialLanguage,
  fallbackLng: "zh",
  supportedLngs: SUPPORTED_LANGUAGES,
  load: "languageOnly",
  showSupportNotice: false,
  interpolation: { escapeValue: false },
});

// 初始化后立即同步 <html lang>，并在切换语言时同步
syncDocumentLang(initialLanguage);
i18n.on("languageChanged", syncDocumentLang);

export async function setLanguage(lng: SupportedLanguage) {
  localStorage.setItem(STORAGE_KEY, lng);
  await i18n.changeLanguage(lng);
}

export function getLanguage(): SupportedLanguage {
  return normalizeLanguage(i18n.language);
}

export default i18n;
