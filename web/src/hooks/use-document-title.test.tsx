import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { titleKeyForPathname, useDocumentTitle } from "./use-document-title";

function TitleProbe({ title }: { title: string }) {
  useDocumentTitle(title);
  return null;
}

describe("document title", () => {
  it("maps routes to i18n keys", () => {
    expect(titleKeyForPathname("/app/overview")).toBe("nav.overview");
    expect(titleKeyForPathname("/app/backups/data")).toBe("nav.backups");
    expect(titleKeyForPathname("/app/foo")).toBe("notFound.title");
    expect(titleKeyForPathname("/login")).toBe("login.welcomeTitle");
    expect(titleKeyForPathname("/this-does-not-exist")).toBe("notFound.title");
  });

  it("sets a language-aware document title", async () => {
    await setLanguage("en");
    const { unmount } = render(
      <I18nextProvider i18n={i18n}>
        <TitleProbe title={i18n.t("nav.overview")} />
      </I18nextProvider>,
    );
    expect(document.title).toBe("Overview · Xirang Console");
    unmount();

    await setLanguage("zh");
    render(
      <I18nextProvider i18n={i18n}>
        <TitleProbe title={i18n.t("nav.overview")} />
      </I18nextProvider>,
    );
    expect(document.title).toMatch(/概览/);
  });
});
