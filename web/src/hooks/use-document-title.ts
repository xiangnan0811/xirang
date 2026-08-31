import { useEffect } from "react";
import { useTranslation } from "react-i18next";

const APP_SECTION_TITLE_KEYS: Record<string, string> = {
  overview: "nav.overview",
  dashboards: "dashboards.pageTitle",
  nodes: "nav.nodes",
  "ssh-keys": "nav.sshKeys",
  policies: "nav.policies",
  backups: "nav.backups",
  logs: "nav.logs",
  notifications: "nav.alertCenter",
  tasks: "nav.tasks",
  audit: "nav.audit",
  "credential-audit": "nav.credentialAudit",
  "credential-access-grants": "nav.credentialGrants",
  reports: "nav.reports",
  credentials: "nav.credentials",
  settings: "nav.settings",
  more: "nav.more",
  "automation-rules": "nav.automationRules",
  "service-monitors": "nav.serviceMonitors",
};

export function titleKeyForPathname(pathname: string): string {
  const path = pathname.split("?")[0] ?? pathname;
  if (path === "/login") return "login.welcomeTitle";
  if (path === "/status") return "serviceMonitor.statusPageTitle";
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "app" && parts[1]) {
    return APP_SECTION_TITLE_KEYS[parts[1]] ?? "notFound.title";
  }
  return "notFound.title";
}

export function useDocumentTitle(pageTitle: string) {
  const { t, i18n } = useTranslation();

  useEffect(() => {
    const brand = t("login.consoleName");
    document.title = pageTitle ? `${pageTitle} · ${brand}` : brand;
  }, [pageTitle, t, i18n.language]);
}
