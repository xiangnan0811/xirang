import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";

export function PoliciesHero({
  total,
  enabled,
  onCreate,
}: {
  total: number;
  enabled: number;
  onCreate: () => void;
}) {
  const { t } = useTranslation();
  return (
    <PageHero
      title={t("policies.pageTitle")}
      subtitle={t("policies.pageSubtitle", { total, enabled })}
      actions={
        <Button shape="pill" onClick={onCreate}>
          <Plus className="size-4" aria-hidden="true" />
          {t("policies.newPolicy")}
        </Button>
      }
    />
  );
}
