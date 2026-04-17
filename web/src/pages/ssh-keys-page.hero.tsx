import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";

export function SSHKeysHero({
  total,
  active,
  onCreate,
}: {
  total: number;
  active: number;
  onCreate: () => void;
}) {
  const { t } = useTranslation();
  return (
    <PageHero
      title={t("sshKeys.pageTitle")}
      subtitle={t("sshKeys.pageSubtitle", { count: total, active })}
      actions={
        <Button shape="pill" onClick={onCreate}>
          <Plus className="size-4" aria-hidden="true" />
          {t("sshKeys.addKey")}
        </Button>
      }
    />
  );
}
