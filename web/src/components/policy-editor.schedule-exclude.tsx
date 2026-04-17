import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { CronGenerator } from "@/components/cron-generator";
import type { PolicyDraft } from "@/components/policy-editor-dialog";

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, parsed));
}

type PolicyScheduleExcludeProps = {
  draft: PolicyDraft;
  saving: boolean;
  onChange: (updater: (prev: PolicyDraft) => PolicyDraft) => void;
};

export function PolicyScheduleExclude({ draft, saving, onChange }: PolicyScheduleExcludeProps) {
  const { t } = useTranslation();

  return (
    <>
      <div>
        <label htmlFor="policy-edit-cron" className="mb-1 block text-sm font-medium">
          {t("policyEditor.cronExpression")}
        </label>
        <CronGenerator
          id="policy-edit-cron"
          value={draft.cron}
          onChange={(val) => onChange((prev) => ({ ...prev, cron: val }))}
          disabled={saving}
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-threshold" className="mb-1 block text-sm font-medium">
            {t("policyEditor.failureThreshold")}
          </label>
          <Input
            id="policy-edit-threshold"
            type="number"
            min={1}
            max={10}
            value={draft.criticalThreshold}
            onChange={(e) =>
              onChange((prev) => ({
                ...prev,
                criticalThreshold: toBoundedInt(e.target.value, 2, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <div id="policy-status-label" className="mb-1 text-sm font-medium">
            {t("policyEditor.policyStatus")}
          </div>
          <div className="glass-panel flex h-10 items-center gap-2 px-3 text-sm">
            <Switch
              aria-labelledby="policy-status-label"
              checked={draft.enabled}
              onCheckedChange={(checked) => onChange((prev) => ({ ...prev, enabled: checked }))}
            />
            <span className="text-muted-foreground">
              {draft.enabled ? t("common.enabled") : t("common.disabled")}
            </span>
          </div>
        </div>
      </div>

      {/* Verify config */}
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <div id="policy-verify-label" className="mb-1 text-sm font-medium">
            {t("policyEditor.backupVerify")}
          </div>
          <div className="glass-panel flex h-10 items-center gap-2 px-3 text-sm">
            <Switch
              aria-labelledby="policy-verify-label"
              checked={draft.verifyEnabled}
              onCheckedChange={(checked) =>
                onChange((prev) => ({ ...prev, verifyEnabled: checked }))
              }
            />
            <span className="text-muted-foreground">
              {draft.verifyEnabled ? t("common.enabled") : t("common.disabled")}
            </span>
          </div>
        </div>
        {draft.verifyEnabled ? (
          <div>
            <label htmlFor="policy-edit-sample-rate" className="mb-1 block text-sm font-medium">
              {t("policyEditor.sampleRate")}
            </label>
            <Input
              id="policy-edit-sample-rate"
              type="number"
              min={1}
              max={100}
              value={draft.verifySampleRate}
              onChange={(e) =>
                onChange((prev) => ({
                  ...prev,
                  verifySampleRate: toBoundedInt(e.target.value, 10, 1, 100),
                }))
              }
            />
          </div>
        ) : null}
      </div>
    </>
  );
}
