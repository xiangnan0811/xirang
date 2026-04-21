import { useTranslation } from "react-i18next";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { TagChips } from "@/components/ui/tag-chips";
import type { EscalationLevel, IntegrationChannel } from "@/types/domain";

export type EscalationLevelRowErrors = {
  delay?: string;
  integrations?: string;
  tags?: string;
};

type Props = {
  level: EscalationLevel;
  index: number;
  isFirst: boolean;
  integrations: IntegrationChannel[];
  onChange: (next: EscalationLevel) => void;
  onRemove?: () => void;
  errors?: EscalationLevelRowErrors;
};

export function EscalationLevelRow({ level, index, isFirst, integrations, onChange, onRemove, errors }: Props) {
  const { t } = useTranslation();

  const toggleIntegration = (intId: number) => {
    const ids = level.integration_ids.includes(intId)
      ? level.integration_ids.filter((id) => id !== intId)
      : [...level.integration_ids, intId];
    onChange({ ...level, integration_ids: ids });
  };

  // Extract numeric id from "int-N" or plain number
  const parseIntId = (id: string): number => {
    if (id.startsWith("int-")) return Number(id.slice(4));
    return Number(id);
  };

  return (
    <div className="rounded-lg border border-border bg-card p-4 space-y-3">
      {/* Header */}
      <div className="flex items-center justify-between">
        <span className="text-sm font-semibold">
          {t("escalation.levels.level", { n: index + 1 })}
        </span>
        {onRemove && (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={onRemove}
            aria-label={t("escalation.levels.removeLevel")}
          >
            <X className="size-4 mr-1" />
            {t("escalation.levels.removeLevel")}
          </Button>
        )}
      </div>

      {/* Delay */}
      <div className="space-y-1">
        <label className="text-sm font-medium">{t("escalation.levels.delaySeconds")}</label>
        <Input
          type="number"
          min={0}
          value={level.delay_seconds}
          disabled={isFirst}
          onChange={(e) => onChange({ ...level, delay_seconds: Number(e.target.value) })}
          aria-label={t("escalation.levels.delaySeconds")}
        />
        {errors?.delay && (
          <p className="text-xs text-destructive">{errors.delay}</p>
        )}
        {isFirst && (
          <p className="text-xs text-muted-foreground">{t("escalation.levels.delayHint")}</p>
        )}
      </div>

      {/* Integrations multi-select */}
      <div className="space-y-1">
        <label className="text-sm font-medium">{t("escalation.levels.integrations")}</label>
        {integrations.length === 0 ? (
          <p className="text-xs text-muted-foreground">暂无可用通道</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {integrations.map((intg) => {
              const numId = parseIntId(intg.id);
              const selected = level.integration_ids.includes(numId);
              return (
                <button
                  key={intg.id}
                  type="button"
                  onClick={() => toggleIntegration(numId)}
                  aria-pressed={selected}
                  className={[
                    "inline-flex items-center gap-1 rounded-full border px-3 py-1 text-xs font-medium transition-colors",
                    selected
                      ? "border-primary bg-primary/10 text-primary"
                      : "border-border bg-muted text-muted-foreground hover:border-primary/50",
                  ].join(" ")}
                >
                  {intg.name}
                </button>
              );
            })}
          </div>
        )}
        {errors?.integrations && (
          <p className="text-xs text-destructive">{errors.integrations}</p>
        )}
      </div>

      {/* Severity override */}
      <div className="space-y-1">
        <label className="text-sm font-medium">{t("escalation.levels.severityOverride")}</label>
        <Select
          value={level.severity_override}
          onChange={(e) =>
            onChange({
              ...level,
              severity_override: e.target.value as EscalationLevel["severity_override"],
            })
          }
          aria-label={t("escalation.levels.severityOverride")}
        >
          <option value="">{t("escalation.severityOverride.empty")}</option>
          <option value="info">{t("escalation.severity.info")}</option>
          <option value="warning">{t("escalation.severity.warning")}</option>
          <option value="critical">{t("escalation.severity.critical")}</option>
        </Select>
      </div>

      {/* Tags */}
      <div className="space-y-1">
        <label className="text-sm font-medium">{t("escalation.levels.tags")}</label>
        <TagChips
          value={level.tags}
          onChange={(tags) => onChange({ ...level, tags })}
          placeholder={t("escalation.levels.tagsHint")}
        />
        {errors?.tags && (
          <p className="text-xs text-destructive">{errors.tags}</p>
        )}
      </div>
    </div>
  );
}
