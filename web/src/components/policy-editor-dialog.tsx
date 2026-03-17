import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, Clock3 } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppTextarea } from "@/components/ui/app-textarea";
import { Switch } from "@/components/ui/switch";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { CronGenerator } from "@/components/cron-generator";
import { BandwidthScheduleEditor } from "@/components/bandwidth-schedule-editor";
import { apiClient } from "@/lib/api/client";
import { useAuth } from "@/context/auth-context";
import type { HookTemplate, NewPolicyInput, NodeRecord, PolicyRecord } from "@/types/domain";

type PolicyDraft = NewPolicyInput & {
  id?: number;
};

const emptyDraft: PolicyDraft = {
  name: "",
  sourcePath: "",
  targetPath: "",
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

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

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

  const handleNodeToggle = (nodeId: number, checked: boolean) => {
    setDraft((prev) => ({
      ...prev,
      nodeIds: checked
        ? [...prev.nodeIds, nodeId]
        : prev.nodeIds.filter((id) => id !== nodeId),
    }));
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<Clock3 className="size-5 text-primary" />}
      title={isEditing ? t('policyEditor.titleEdit', { name: draft.name }) : t('policyEditor.titleCreate')}
      description={isEditing
        ? t('policyEditor.descEdit', { name: draft.name })
        : t('policyEditor.descCreate')}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? t('policyEditor.submitEdit') : t('policyEditor.submitCreate')}
    >
      <div>
        <label htmlFor="policy-edit-name" className="mb-1 block text-sm font-medium">{t('policyEditor.policyName')}</label>
        <Input id="policy-edit-name" placeholder={t('policyEditor.policyNamePlaceholder')}
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="policy-edit-cron" className="mb-1 block text-sm font-medium">
          {t('policyEditor.cronExpression')}
        </label>
        <CronGenerator
          id="policy-edit-cron"
          value={draft.cron}
          onChange={(val) => setDraft((prev) => ({ ...prev, cron: val }))}
          disabled={saving}
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-source" className="mb-1 block text-sm font-medium">{t('policyEditor.sourcePath')}</label>
          <Input id="policy-edit-source" placeholder={t('policyEditor.sourcePathPlaceholder')}
            value={draft.sourcePath}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                sourcePath: event.target.value,
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="policy-edit-target" className="mb-1 block text-sm font-medium">{t('policyEditor.targetPath')}</label>
          <Input id="policy-edit-target" placeholder={t('policyEditor.targetPathPlaceholder')}
            value={draft.targetPath}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                targetPath: event.target.value,
              }))
            }
          />
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-threshold" className="mb-1 block text-sm font-medium">
            {t('policyEditor.failureThreshold')}
          </label>
          <Input
            id="policy-edit-threshold"
            type="number"
            min={1}
            max={10}
            value={draft.criticalThreshold}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                criticalThreshold: toBoundedInt(event.target.value, 2, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <div id="policy-status-label" className="mb-1 text-sm font-medium">{t('policyEditor.policyStatus')}</div>
          <div className="glass-panel flex h-10 items-center gap-2 px-3 text-sm">
            <Switch
              aria-labelledby="policy-status-label"
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((prev) => ({ ...prev, enabled: checked }))
              }
            />
            <span className="text-muted-foreground">{draft.enabled ? t('common.enabled') : t('common.disabled')}</span>
          </div>
        </div>
      </div>

      {/* 关联节点 */}
      {nodes.length > 0 ? (
        <div>
          <div className="mb-1 text-sm font-medium">
            {t('policyEditor.relatedNodes')}
            {draft.nodeIds.length > 0 ? (
              <span className="ml-1 text-xs text-muted-foreground">
                {t('policyEditor.relatedNodesSelected', { count: draft.nodeIds.length })}
              </span>
            ) : null}
          </div>
          <div className="glass-panel max-h-40 overflow-y-auto rounded-md border border-border/60 p-2">
            <div className="flex flex-col gap-1.5">
              {nodes.map((node) => {
                const checked = draft.nodeIds.includes(node.id);
                return (
                  <label
                    key={node.id}
                    className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent/40"
                  >
                    <input
                      type="checkbox"
                      className="size-4 shrink-0"
                      checked={checked}
                      onChange={(event) => handleNodeToggle(node.id, event.target.checked)}
                      aria-label={t('policyEditor.selectNode', { name: node.name })}
                    />
                    <span className="truncate">{node.name}</span>
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">{node.host}</span>
                  </label>
                );
              })}
            </div>
          </div>
        </div>
      ) : null}

      {/* 校验配置 */}
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <div id="policy-verify-label" className="mb-1 text-sm font-medium">{t('policyEditor.backupVerify')}</div>
          <div className="glass-panel flex h-10 items-center gap-2 px-3 text-sm">
            <Switch
              aria-labelledby="policy-verify-label"
              checked={draft.verifyEnabled}
              onCheckedChange={(checked) =>
                setDraft((prev) => ({ ...prev, verifyEnabled: checked }))
              }
            />
            <span className="text-muted-foreground">{draft.verifyEnabled ? t('common.enabled') : t('common.disabled')}</span>
          </div>
        </div>
        {draft.verifyEnabled ? (
          <div>
            <label htmlFor="policy-edit-sample-rate" className="mb-1 block text-sm font-medium">
              {t('policyEditor.sampleRate')}
            </label>
            <Input
              id="policy-edit-sample-rate"
              type="number"
              min={1}
              max={100}
              value={draft.verifySampleRate}
              onChange={(event) =>
                setDraft((prev) => ({
                  ...prev,
                  verifySampleRate: toBoundedInt(event.target.value, 10, 1, 100),
                }))
              }
            />
          </div>
        ) : null}
      </div>

      {/* 高级设置 */}
      <div className="rounded-md border border-border/60">
        <button
          type="button"
          className="flex w-full items-center justify-between px-3 py-2 text-sm font-medium hover:bg-muted/40 transition-colors"
          onClick={() => setAdvancedOpen(!advancedOpen)}
          aria-expanded={advancedOpen}
        >
          {t('policyEditor.advancedSettings')}
          <ChevronDown className={`size-4 text-muted-foreground transition-transform ${advancedOpen ? "rotate-180" : ""}`} />
        </button>

        {advancedOpen && (
          <div className="space-y-3 border-t border-border/40 px-3 py-3 animate-in slide-in-from-top-1 fade-in duration-150">
            {/* Hook template selector */}
            {hookTemplates.length > 0 && (
              <div>
                <label className="mb-1 block text-sm font-medium">{t('policyEditor.insertTemplate')}</label>
                <div className="flex flex-wrap gap-1.5">
                  {hookTemplates.map((tpl) => (
                    <button
                      key={tpl.id}
                      type="button"
                      className="rounded-md border border-border/60 bg-muted/30 px-2 py-1 text-xs text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors"
                      onClick={() =>
                        setDraft((prev) => ({
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
                {t('policyEditor.preHook')}
              </label>
              <AppTextarea
                id="policy-edit-pre-hook"
                className="min-h-16 text-xs font-mono"
                placeholder={t('policyEditor.preHookPlaceholder')}
                value={draft.preHook ?? ""}
                onChange={(e) =>
                  setDraft((prev) => ({ ...prev, preHook: e.target.value }))
                }
              />
            </div>

            <div>
              <label htmlFor="policy-edit-post-hook" className="mb-1 block text-sm font-medium">
                {t('policyEditor.postHook')}
              </label>
              <AppTextarea
                id="policy-edit-post-hook"
                className="min-h-16 text-xs font-mono"
                placeholder={t('policyEditor.postHookPlaceholder')}
                value={draft.postHook ?? ""}
                onChange={(e) =>
                  setDraft((prev) => ({ ...prev, postHook: e.target.value }))
                }
              />
            </div>

            <div>
              <label htmlFor="policy-edit-hook-timeout" className="mb-1 block text-sm font-medium">
                {t('policyEditor.hookTimeout')}
              </label>
              <Input
                id="policy-edit-hook-timeout"
                type="number"
                min={1}
                max={3600}
                value={draft.hookTimeoutSeconds ?? 300}
                onChange={(e) =>
                  setDraft((prev) => ({
                    ...prev,
                    hookTimeoutSeconds: toBoundedInt(e.target.value, 300, 1, 3600),
                  }))
                }
              />
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              <div>
                <label htmlFor="policy-edit-max-retries" className="mb-1 block text-sm font-medium">
                  {t('policyEditor.maxRetries')}
                </label>
                <Input
                  id="policy-edit-max-retries"
                  type="number"
                  min={0}
                  max={10}
                  value={draft.maxRetries ?? 2}
                  onChange={(e) =>
                    setDraft((prev) => ({
                      ...prev,
                      maxRetries: toBoundedInt(e.target.value, 2, 0, 10),
                    }))
                  }
                />
              </div>
              <div>
                <label htmlFor="policy-edit-retry-base" className="mb-1 block text-sm font-medium">
                  {t('policyEditor.retryBaseSeconds')}
                </label>
                <Input
                  id="policy-edit-retry-base"
                  type="number"
                  min={10}
                  max={3600}
                  value={draft.retryBaseSeconds ?? 30}
                  onChange={(e) =>
                    setDraft((prev) => ({
                      ...prev,
                      retryBaseSeconds: toBoundedInt(e.target.value, 30, 10, 3600),
                    }))
                  }
                />
              </div>
            </div>

            {(draft.maxRetries ?? 0) > 0 && (
              <p className="text-xs text-muted-foreground">
                {t('policyEditor.retryPreview')}
                {Array.from({ length: draft.maxRetries ?? 0 }, (_, i) => {
                  const delay = (draft.retryBaseSeconds ?? 30) * Math.pow(2, i);
                  return delay >= 60 ? `${(delay / 60).toFixed(1)}m` : `${delay}s`;
                }).join(" → ")}
              </p>
            )}

            <BandwidthScheduleEditor
              value={draft.bandwidthSchedule ?? ""}
              onChange={(val) =>
                setDraft((prev) => ({ ...prev, bandwidthSchedule: val }))
              }
            />
          </div>
        )}
      </div>
    </FormDialog>
  );
}

export type { PolicyDraft };
