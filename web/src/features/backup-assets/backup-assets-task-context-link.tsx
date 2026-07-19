import { ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { backupAssetsTaskContextHref } from "./backup-assets-route-state";

export interface BackupAssetsTaskContextLinkProps {
  taskId: number;
  className?: string;
}

export function BackupAssetsTaskContextLink({ taskId, className }: BackupAssetsTaskContextLinkProps) {
  const { t } = useTranslation();
  const label = t("backupAssets.actions.openTaskContext");

  return (
    <Button asChild variant="ghost" size="sm" className={cn("shrink-0", className)}>
      <a href={backupAssetsTaskContextHref(taskId)} aria-label={label}>
        <ExternalLink className="size-3.5" aria-hidden />
        {label}
      </a>
    </Button>
  );
}
