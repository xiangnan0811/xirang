import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { BandwidthScheduleEditor } from "@/components/bandwidth-schedule-editor";
import type { HookTemplate } from "@/types/domain";
import type { PolicyDraft } from "@/components/policy-editor-dialog";

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, parsed));
}

type PolicyHooksProps = {
  draft: PolicyDraft;
  hookTemplates: HookTemplate[];
  onChange: (updater: (prev: PolicyDraft) => PolicyDraft) => void;
};

export function PolicyHooks({ draft, hookTemplates, onChange }: PolicyHooksProps) {
  const { t } = useTranslation();

  return (
    <>
      {/* Hook template selector */}
      {hookTemplates.length > 0 && (
        <div>
          <label className="mb-1 block text-sm font-medium">
            {t("policyEditor.insertTemplate")}
          </label>
          <div className="flex flex-wrap gap-1.5">
            {hookTemplates.map((tpl) => (
              <button
                key={tpl.id}
                type="button"
                className="rounded-md border border-border/60 bg-muted/30 px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors"
                onClick={() =>
                  onChange((prev) => ({
                    ...prev,
                    preHook: tpl.preHook,
                    postHook: tpl.postHook,
                  }))
                }
                title={tpl.description}
              >
                {tpl.name}
              </button>
            ))}
          </div>
        </div>
      )}

      <div>
        <label htmlFor="policy-edit-pre-hook" className="mb-1 block text-sm font-medium">
          {t("policyEditor.preHook")}
        </label>
        <Textarea
          id="policy-edit-pre-hook"
          className="min-h-16 text-xs font-mono"
          placeholder={t("policyEditor.preHookPlaceholder")}
          value={draft.preHook ?? ""}
          onChange={(e) => onChange((prev) => ({ ...prev, preHook: e.target.value }))}
        />
      </div>

      <div>
        <label htmlFor="policy-edit-post-hook" className="mb-1 block text-sm font-medium">
          {t("policyEditor.postHook")}
        </label>
        <Textarea
          id="policy-edit-post-hook"
          className="min-h-16 text-xs font-mono"
          placeholder={t("policyEditor.postHookPlaceholder")}
          value={draft.postHook ?? ""}
          onChange={(e) => onChange((prev) => ({ ...prev, postHook: e.target.value }))}
        />
      </div>

      <div>
        <label htmlFor="policy-edit-hook-timeout" className="mb-1 block text-sm font-medium">
          {t("policyEditor.hookTimeout")}
        </label>
        <Input
          id="policy-edit-hook-timeout"
          type="number"
          min={1}
          max={3600}
          value={draft.hookTimeoutSeconds ?? 300}
          onChange={(e) =>
            onChange((prev) => ({
              ...prev,
              hookTimeoutSeconds: toBoundedInt(e.target.value, 300, 1, 3600),
            }))
          }
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-max-retries" className="mb-1 block text-sm font-medium">
            {t("policyEditor.maxRetries")}
          </label>
          <Input
            id="policy-edit-max-retries"
            type="number"
            min={0}
            max={10}
            value={draft.maxRetries ?? 2}
            onChange={(e) =>
              onChange((prev) => ({
                ...prev,
                maxRetries: toBoundedInt(e.target.value, 2, 0, 10),
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="policy-edit-retry-base" className="mb-1 block text-sm font-medium">
            {t("policyEditor.retryBaseSeconds")}
          </label>
          <Input
            id="policy-edit-retry-base"
            type="number"
            min={10}
            max={3600}
            value={draft.retryBaseSeconds ?? 30}
            onChange={(e) =>
              onChange((prev) => ({
                ...prev,
                retryBaseSeconds: toBoundedInt(e.target.value, 30, 10, 3600),
              }))
            }
          />
        </div>
      </div>

      {(draft.maxRetries ?? 0) > 0 && (
        <p className="text-xs text-muted-foreground">
          {t("policyEditor.retryPreview")}
          {Array.from({ length: draft.maxRetries ?? 0 }, (_, i) => {
            const delay = (draft.retryBaseSeconds ?? 30) * Math.pow(2, i);
            return delay >= 60 ? `${(delay / 60).toFixed(1)}m` : `${delay}s`;
          }).join(" → ")}
        </p>
      )}

      <BandwidthScheduleEditor
        value={draft.bandwidthSchedule ?? ""}
        onChange={(val) => onChange((prev) => ({ ...prev, bandwidthSchedule: val }))}
      />
    </>
  );
}
