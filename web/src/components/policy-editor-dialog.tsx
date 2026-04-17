import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Clock3, ChevronDown } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { apiClient } from "@/lib/api/client";
import { useAuth } from "@/context/auth-context";
import { PolicySourceTarget } from "@/components/policy-editor.source-target";
import { PolicyScheduleExclude } from "@/components/policy-editor.schedule-exclude";
import { PolicyHooks } from "@/components/policy-editor.hooks";
import type { HookTemplate, NewPolicyInput, NodeRecord, PolicyRecord } from "@/types/domain";

type PolicyDraft = NewPolicyInput & {
  id?: number;
};

const emptyDraft: PolicyDraft = {
  name: "",
  sourcePath: "",
  targetPath: "/backup",
  cron: "0 */2 * * *",
  criticalThreshold: 2,
  enabled: true,
  nodeIds: [],
  verifyEnabled: true,
  verifySampleRate: 0,
  preHook: "",
  postHook: "",
  hookTimeoutSeconds: 300,
  maxRetries: 2,
  retryBaseSeconds: 30,
  bandwidthSchedule: "",
};

function toDraft(policy: PolicyRecord): PolicyDraft {
  return {
    id: policy.id,
    name: policy.name,
    sourcePath: policy.sourcePath,
    targetPath: policy.targetPath,
    cron: policy.cron,
    criticalThreshold: policy.criticalThreshold,
    enabled: policy.enabled,
    nodeIds: policy.nodeIds ?? [],
    verifyEnabled: policy.verifyEnabled ?? false,
    verifySampleRate: policy.verifySampleRate ?? 0,
    preHook: policy.preHook ?? "",
    postHook: policy.postHook ?? "",
    hookTimeoutSeconds: policy.hookTimeoutSeconds ?? 300,
    maxRetries: policy.maxRetries ?? 2,
    retryBaseSeconds: policy.retryBaseSeconds ?? 30,
    bandwidthSchedule: policy.bandwidthSchedule ?? "",
  };
}

type PolicyEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingPolicy?: PolicyRecord | null;
  onSave: (draft: PolicyDraft) => Promise<void>;
  nodes?: NodeRecord[];
};

export function PolicyEditorDialog({
  open,
  onOpenChange,
  editingPolicy,
  onSave,
  nodes = [],
}: PolicyEditorDialogProps) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [draft, setDraft] = useDialogDraft<PolicyDraft, PolicyRecord>(open, emptyDraft, editingPolicy, toDraft);
  const [saving, setSaving] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [hookTemplates, setHookTemplates] = useState<HookTemplate[]>([]);

  useEffect(() => {
    if (!open || !token) return;
    apiClient.getHookTemplates(token).then(setHookTemplates).catch(() => {});
  }, [open, token]);

  const isEditing = Boolean(draft.id);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(draft);
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<Clock3 className="size-5 text-primary" />}
      title={isEditing ? t("policyEditor.titleEdit", { name: draft.name }) : t("policyEditor.titleCreate")}
      description={
        isEditing
          ? t("policyEditor.descEdit", { name: draft.name })
          : t("policyEditor.descCreate")
      }
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? t("policyEditor.submitEdit") : t("policyEditor.submitCreate")}
    >
      {/* Policy name */}
      <div>
        <label htmlFor="policy-edit-name" className="mb-1 block text-sm font-medium">
          {t("policyEditor.policyName")}
        </label>
        <Input
          id="policy-edit-name"
          placeholder={t("policyEditor.policyNamePlaceholder")}
          value={draft.name}
          onChange={(e) => setDraft((prev) => ({ ...prev, name: e.target.value }))}
        />
      </div>

      {/* Schedule + exclude section */}
      <PolicyScheduleExclude draft={draft} saving={saving} onChange={setDraft} />

      {/* Source / target / nodes section */}
      <PolicySourceTarget draft={draft} nodes={nodes} saving={saving} onChange={setDraft} />

      {/* Advanced settings (hooks + retry + bandwidth) */}
      <div className="rounded-md border border-border/60">
        <button
          type="button"
          className="flex w-full items-center justify-between px-3 py-2 text-sm font-medium hover:bg-muted/40 transition-colors"
          onClick={() => setAdvancedOpen(!advancedOpen)}
          aria-expanded={advancedOpen}
        >
          {t("policyEditor.advancedSettings")}
          <ChevronDown
            className={`size-4 text-muted-foreground transition-transform ${advancedOpen ? "rotate-180" : ""}`}
          />
        </button>

        {advancedOpen && (
          <div className="space-y-3 border-t border-border/40 px-3 py-3 animate-in slide-in-from-top-1 fade-in duration-150">
            <PolicyHooks draft={draft} hookTemplates={hookTemplates} onChange={setDraft} />
          </div>
        )}
      </div>
    </FormDialog>
  );
}

export type { PolicyDraft };
