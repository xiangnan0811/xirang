import { useTranslation } from "react-i18next";
import { Copy, Trash2, Wrench } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import type { NodeRecord, PolicyRecord } from "@/types/domain";

export type PolicyCardProps = {
  policy: PolicyRecord;
  nodes: NodeRecord[];
  selected: boolean;
  onToggleSelect: (id: number, checked: boolean) => void;
  onEdit: (policy: PolicyRecord) => void;
  onDelete: (policy: PolicyRecord) => void;
  onToggle: (policy: PolicyRecord) => void;
  onCloneFromTemplate: (policy: PolicyRecord) => void;
};

export function PolicyCard({
  policy,
  nodes,
  selected,
  onToggleSelect,
  onEdit,
  onDelete,
  onToggle,
  onCloneFromTemplate,
}: PolicyCardProps) {
  const { t } = useTranslation();

  return (
    <div className="rounded-lg border border-border bg-card shadow-sm hover:shadow-md transition-shadow p-4 flex flex-col">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <input
            type="checkbox"
            className="size-4 accent-primary rounded-sm"
            checked={selected}
            onChange={(e) => onToggleSelect(policy.id, e.target.checked)}
            aria-label={t('policies.selectAriaLabel', { name: policy.name })}
          />
          <div>
            <h3 className="font-medium">{policy.name}</h3>
            <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          {policy.isTemplate ? (
            <Badge tone="neutral">{t("policies.badgeTemplate")}</Badge>
          ) : (
            <Badge tone="info">{t("policies.badgeManual")}</Badge>
          )}
          <Badge tone={policy.enabled ? "success" : "neutral"}>
            {policy.enabled ? t('common.enabled') : t('common.disabled')}
          </Badge>
        </div>
      </div>

      <div className="mt-3 grid gap-2 text-sm text-muted-foreground flex-1">
        <p className="break-all">{t('policies.sourcePath', { path: policy.sourcePath })}</p>
        <p className="break-all">{t('policies.targetPath', { path: policy.targetPath })}</p>
      </div>

      <div className="mt-3 flex flex-wrap gap-2">
        <Badge tone="neutral">Cron: {policy.cron}</Badge>
        <Badge tone="neutral">{t('policies.failureThreshold', { value: policy.criticalThreshold })}</Badge>
        <Badge tone="neutral">{t('policies.nodeCount', { selected: policy.nodeIds?.length ?? 0, total: nodes?.length ?? 0 })}</Badge>
      </div>

      <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
        <Switch
          checked={policy.enabled}
          aria-label={t('policies.toggleAriaLabel', { action: policy.enabled ? t('common.disable') : t('common.enable'), name: policy.name })}
          onCheckedChange={() => void onToggle(policy)}
        />
        <div className="flex items-center gap-1">
          {policy.isTemplate && (
            <Button
              variant="ghost"
              size="icon"
              className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
              onClick={() => void onCloneFromTemplate(policy)}
              aria-label={t('policies.cloneAriaLabel', { name: policy.name })}
            >
              <Copy className="size-4" />
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
            onClick={() => onEdit(policy)}
            aria-label={t('policies.editAriaLabel')}
          >
            <Wrench className="size-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
            aria-label={t('policies.deleteAriaLabel', { name: policy.name })}
            onClick={() => void onDelete(policy)}
          >
            <Trash2 className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}
