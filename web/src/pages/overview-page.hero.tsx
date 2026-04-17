import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { getTimeOfDay } from "@/lib/date-utils";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";
import { Download } from "lucide-react";

export function OverviewHero({ onRefresh }: { onRefresh?: () => void }) {
  const { t } = useTranslation();
  const { username } = useAuth();
  const timeOfDay = getTimeOfDay();
  const firstName = (username ?? "").split(/[.\s@]/)[0] || (username ?? "");
  const title = `${t(`overview.greeting.${timeOfDay}`)}${firstName ? ", " + firstName : ""}.`;
  const subtitle = t("overview.heroSubtitle", "System health at a glance");
  return (
    <PageHero
      title={title}
      subtitle={subtitle}
      actions={
        onRefresh ? (
          <Button variant="secondary" size="sm" shape="pill" onClick={onRefresh}>
            <Download className="size-4" aria-hidden /> {t("overview.exportReport", "Export")}
          </Button>
        ) : undefined
      }
    />
  );
}
