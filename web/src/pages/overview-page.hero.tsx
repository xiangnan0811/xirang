import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context.hooks";
import { getTimeOfDay } from "@/lib/date-utils";
import { PageHero } from "@/components/ui/page-hero";

export function OverviewHero() {
  const { t } = useTranslation();
  const { username } = useAuth();
  const timeOfDay = getTimeOfDay();
  const firstName = (username ?? "").split(/[.\s@]/)[0] || (username ?? "");
  const greeting = t(`overview.greeting.${timeOfDay}`);
  const title = firstName
    ? t("overview.greetingWithName", { greeting, name: firstName })
    : t("overview.greetingNoName", { greeting });
  const subtitle = t("overview.heroSubtitle", "System health at a glance");
  return (
    <PageHero
      title={title}
      subtitle={subtitle}
    />
  );
}
