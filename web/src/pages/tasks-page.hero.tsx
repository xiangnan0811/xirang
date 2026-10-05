import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";

export function TasksHero({
  totalCount,
  runningCount,
  onCreate,
  canWriteTasks,
}: {
  totalCount: number;
  runningCount: number;
  onCreate: () => void;
  canWriteTasks: boolean;
}) {
  const { t } = useTranslation();
  return (
    <PageHero
      title={t("tasks.pageTitle")}
      subtitle={t("tasks.pageSubtitle", { total: totalCount, running: runningCount })}
      actions={canWriteTasks ? (
        <Button shape="pill" onClick={onCreate}>
          <Plus className="size-4" aria-hidden="true" />
          {t("tasks.createTask")}
        </Button>
      ) : null}
    />
  );
}
