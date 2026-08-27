import { useTranslation } from "react-i18next";

import type { AuthRole } from "@/context/auth-context.shared";

const SETTINGS_TARGET = "/app/backups/overview#backup-assets-content-transport";

export function ContentTransportGuidance({ authRole }: { authRole: AuthRole | null }) {
  const { t } = useTranslation();

  if (authRole === "admin") {
    return (
      <div className="mt-2 space-y-2">
        <p>{t("backupAssets.transport.guidance.admin")}</p>
        <a
          href={SETTINGS_TARGET}
          className="inline-flex min-h-9 items-center rounded-md border border-input bg-background px-3 text-sm font-medium text-foreground focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35"
        >
          {t("backupAssets.transport.guidance.action")}
        </a>
      </div>
    );
  }
  if (authRole === "operator") {
    return <p className="mt-2">{t("backupAssets.transport.guidance.operator")}</p>;
  }
  return <p className="mt-2">{t("backupAssets.transport.guidance.generic")}</p>;
}
