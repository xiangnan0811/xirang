import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { getVisibleNavItems } from "@/components/layout/navigation";
import { PageHero } from "@/components/ui/page-hero";

// Routes that the bottom tab bar already exposes — don't duplicate them in More
const INCLUDED_IN_TABS = new Set(["/app/overview", "/app/nodes", "/app/tasks", "/app/logs"]);

export function MorePage() {
  const { t } = useTranslation();
  const { role } = useAuth();
  const items = getVisibleNavItems(role).filter(
    (n) => !INCLUDED_IN_TABS.has(n.path)
  );

  return (
    <div className="space-y-4">
      <PageHero title={t("nav.more")} />
      <div className="grid grid-cols-1 gap-2">
        {items.map((item) => {
          const Icon = item.icon;
          return (
            <Link
              key={item.path}
              to={item.path}
              className="flex items-center gap-3 rounded-lg bg-card p-4 shadow-sm dark:border dark:border-border"
            >
              <div className="flex size-10 items-center justify-center rounded-md bg-accent text-foreground">
                <Icon className="size-5" aria-hidden />
              </div>
              <div className="text-sm font-medium text-foreground">{t(item.titleKey)}</div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}
