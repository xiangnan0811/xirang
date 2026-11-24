import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { formatTime, getTimeOfDay } from "@/lib/date-utils";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";

export type OverviewHeroProps = {
  scopeLabel?: string;
  updatedAt?: string;
};

export function OverviewHero({ scopeLabel, updatedAt }: OverviewHeroProps) {
  const { t } = useTranslation();
  const { username } = useAuth();
  const timeOfDay = getTimeOfDay();
  const firstName = (username ?? "").split(/[.\s@]/)[0] || (username ?? "");
  const greeting = t(`overview.greeting.${timeOfDay}`);
  const title = firstName
    ? t("overview.greetingWithName", { greeting, name: firstName })
    : t("overview.greetingNoName", { greeting });

  return (
    <PageHero
      title={title}
      meta={
        scopeLabel || updatedAt ? (
          <>
            {scopeLabel ? <span>{scopeLabel}</span> : null}
            {scopeLabel && updatedAt ? <span aria-hidden>·</span> : null}
            {updatedAt ? (
              <span>
                {t("overview.generatedAtLabel")}
                <time dateTime={updatedAt} title={formatTime(updatedAt)} className="tabular-nums">
                  {formatTime(updatedAt)}
                </time>
              </span>
            ) : null}
          </>
        ) : undefined
      }
      actions={
        <Button asChild variant="outline" size="sm">
          <Link to="/app/backups/overview">
            {t("overview.viewBackupHealth")}
            <ArrowRight className="size-3.5" aria-hidden />
          </Link>
        </Button>
      }
    />
  );
}
