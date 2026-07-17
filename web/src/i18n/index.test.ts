import { beforeEach, describe, expect, it } from "vitest";

import i18n, { getLanguage, i18nReady, setLanguage } from "@/i18n";

function leafKeys(value: unknown, prefix = ""): string[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return prefix ? [prefix] : [];
  }
  return Object.entries(value).flatMap(([key, child]) =>
    leafKeys(child, prefix ? `${prefix}.${key}` : key),
  );
}

describe("i18n bootstrap", () => {
  beforeEach(async () => {
    window.localStorage.setItem("xirang.language", "zh");
    await setLanguage("zh");
  });

  it("loads the initial language resources before use", async () => {
    await i18nReady;

    expect(getLanguage()).toBe("zh");
    expect(i18n.t("common.save")).toBe("保存");
    expect(document.documentElement.lang).toBe("zh-CN");
  });

  it("loads and persists the selected language", async () => {
    await setLanguage("en");

    expect(getLanguage()).toBe("en");
    expect(i18n.t("common.save")).toBe("Save");
    expect(window.localStorage.getItem("xirang.language")).toBe("en");
    expect(document.documentElement.lang).toBe("en");
  });

  it("resolves nav.more in both zh and en", async () => {
    await setLanguage("zh");
    expect(i18n.t("nav.more")).toBe("更多");

    await setLanguage("en");
    expect(i18n.t("nav.more")).toBe("More");
  });

  it("keeps every Rclone versioning locale key in zh/en parity", async () => {
    await setLanguage("zh");
    const zhBundle = i18n.getResourceBundle("zh", "translation") as Record<string, unknown>;
    await setLanguage("en");
    const enBundle = i18n.getResourceBundle("en", "translation") as Record<string, unknown>;

    expect(leafKeys(zhBundle.rcloneVersioning).sort()).toEqual(
      leafKeys(enBundle.rcloneVersioning).sort(),
    );
  });
});
