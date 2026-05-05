import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, Clock3 } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/confirm-dialog";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { CronGenerator } from "@/components/cron-generator";
import { BandwidthScheduleEditor } from "@/components/bandwidth-schedule-editor";
import { apiClient } from "@/lib/api/client";
import { useAuth } from "@/context/auth-context";
import { toast } from "@/components/ui/toast";
import type { AppCredential, EscalationPolicy, HookTemplate, NewPolicyInput, NodeRecord, PolicyRecord, ProfileSchema } from "@/types/domain";

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
  escalation_policy_id: null,
  app_profile: "",
  app_credential_id: null,
  drill_enabled: false,
  drill_cron: "",
  drill_target_node_id: null,
  drill_restore_path: "/tmp/xirang-drill",
  drill_pre_verify: "",
  drill_verify: "",
  drill_post_verify: "",
  drill_auto_cleanup: true,
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
    escalation_policy_id: policy.escalation_policy_id ?? null,
    app_profile: policy.app_profile ?? "",
    app_credential_id: policy.app_credential_id ?? null,
    drill_enabled: policy.drill_enabled ?? false,
    drill_cron: policy.drill_cron ?? "",
    drill_target_node_id: policy.drill_target_node_id ?? null,
    drill_restore_path: policy.drill_restore_path ?? "/tmp/xirang-drill",
    drill_pre_verify: policy.drill_pre_verify ?? "",
    drill_verify: policy.drill_verify ?? "",
    drill_post_verify: policy.drill_post_verify ?? "",
    drill_auto_cleanup: policy.drill_auto_cleanup ?? true,
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
  const [drillOpen, setDrillOpen] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [drillConfirmOpen, setDrillConfirmOpen] = useState(false);
  const [hookTemplates, setHookTemplates] = useState<HookTemplate[]>([]);
  const [escalationPolicies, setEscalationPolicies] = useState<EscalationPolicy[]>([]);
  const [profiles, setProfiles] = useState<ProfileSchema[]>([]);
  const [credentials, setCredentials] = useState<AppCredential[]>([]);

  useEffect(() => {
    if (!open || !token) return;
    apiClient.getHookTemplates(token).then(setHookTemplates).catch(() => {});
    apiClient.listEscalationPolicies(token)
      .then((list) => setEscalationPolicies(list.filter((p) => p.enabled)))
      .catch(() => {});
    apiClient.getProfiles(token).then(setProfiles).catch(() => {});
    apiClient.getCredentials(token).then(setCredentials).catch(() => {});
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

  const handleTriggerDrill = async () => {
    if (!draft.id || !token) return;
    setDrillConfirmOpen(false);
    setTriggering(true);
    try {
      const result = await apiClient.triggerDrill(token, draft.id);
      toast.success(t('policyEditor.drill.triggerSuccess', { id: result.task_run_id }));
    } catch {
      toast.error(t('policyEditor.drill.triggerFailed'));
    } finally {
      setTriggering(false);
    }
  };

  const selectedProfileMeta = profiles.find((p) => p.id === draft.app_profile);
  const filteredCredentials = credentials.filter(
    (c) => !draft.app_profile ? false : c.type === selectedProfileMeta?.credential_type
  );
  const sourceNodeIds = new Set(draft.nodeIds);
  const sandboxNodes = nodes.filter((n) => !sourceNodeIds.has(n.id));

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
        <p className="mt-1 text-xs text-muted-foreground">
          {t('policyEditor.backupStorageInfo')}
        </p>
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

      {/* 各节点备份路径预览 */}
      {draft.nodeIds.length > 0 && nodes.length > 0 && (
        <div>
          <div className="mb-1 text-sm font-medium">{t('policyEditor.perNodePathPreview')}</div>
          <div className="glass-panel rounded-md border border-border/60 px-3 py-2 font-mono text-xs text-muted-foreground">
            {draft.nodeIds.map((nodeId, idx) => {
              const node = nodes.find((n) => n.id === nodeId);
              if (!node) return null;
              const dirName = node.backupDir || node.name;
              const isLast = idx === draft.nodeIds.length - 1;
              const prefix = isLast ? '\u2514' : '\u251C';
              return (
                <div key={nodeId}>
                  {prefix} {node.name} → /backup/{dirName}/
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* 应用感知备份 */}
      {profiles.length > 0 && (
        <div className="rounded-md border border-border/60 p-3 space-y-3">
          <div className="text-sm font-medium">{t('policyEditor.appAwareBackup')}</div>

          <div>
            <label htmlFor="policy-edit-app-profile" className="mb-1 block text-sm font-medium">
              {t('policyEditor.selectProfile')}
            </label>
            <Select
              id="policy-edit-app-profile"
              value={draft.app_profile ?? ""}
              onChange={(e) => {
                const profileId = e.target.value;
                setDraft((prev) => ({
                  ...prev,
                  app_profile: profileId,
                  app_credential_id: profileId === "" || profileId !== prev.app_profile ? null : prev.app_credential_id,
                }));
              }}
            >
              <option value="">{t('policyEditor.appProfileNone')}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </div>

          {selectedProfileMeta && (
            <p className="text-xs text-muted-foreground">{selectedProfileMeta.description}</p>
          )}

          {draft.app_profile && (
            <div>
              <label htmlFor="policy-edit-app-credential" className="mb-1 block text-sm font-medium">
                {t('policyEditor.selectCredential')}
              </label>
              <Select
                id="policy-edit-app-credential"
                value={draft.app_credential_id == null ? "" : String(draft.app_credential_id)}
                onChange={(e) =>
                  setDraft((prev) => ({
                    ...prev,
                    app_credential_id: e.target.value === "" ? null : Number(e.target.value),
                  }))
                }
              >
                <option value="">{t('policyEditor.appCredentialNone')}</option>
                {filteredCredentials.map((c) => (
                  <option key={c.id} value={String(c.id)}>
                    {c.name} ({c.type})
                  </option>
                ))}
              </Select>
            </div>
          )}

          {draft.app_profile && draft.app_credential_id != null && (
            <p className="text-xs text-muted-foreground">{t('policyEditor.appAwareHint')}</p>
          )}
        </div>
      )}

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

      {/* 升级策略 */}
      <div>
        <label htmlFor="policy-edit-escalation" className="mb-1 block text-sm font-medium">
          {t("escalation.tabTitle")}
        </label>
        <Select
          id="policy-edit-escalation"
          value={draft.escalation_policy_id == null ? "" : String(draft.escalation_policy_id)}
          onChange={(e) =>
            setDraft((prev) => ({
              ...prev,
              escalation_policy_id: e.target.value === "" ? null : Number(e.target.value),
            }))
          }
        >
          <option value="">无升级策略 / None</option>
          {escalationPolicies.map((p) => (
            <option key={p.id} value={String(p.id)}>
              {p.name}
            </option>
          ))}
        </Select>
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
              <Textarea
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
              <Textarea
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

      {/* 恢复演练 */}
      <div className="rounded-md border border-border/60">
        <button
          type="button"
          className="flex w-full items-center justify-between px-3 py-2 text-sm font-medium hover:bg-muted/40 transition-colors"
          onClick={() => setDrillOpen(!drillOpen)}
          aria-expanded={drillOpen}
        >
          {t('policyEditor.drill.title')}
          <ChevronDown className={`size-4 text-muted-foreground transition-transform ${drillOpen ? "rotate-180" : ""}`} />
        </button>

        {drillOpen && (
          <div className="space-y-3 border-t border-border/40 px-3 py-3 animate-in slide-in-from-top-1 fade-in duration-150">
            {/* 启用开关 */}
            <div className="flex items-center justify-between">
              <span className="text-sm">{t('policyEditor.drill.enable')}</span>
              <Switch
                aria-label={t('policyEditor.drill.enable')}
                checked={draft.drill_enabled ?? false}
                onCheckedChange={(checked) =>
                  setDraft((prev) => ({ ...prev, drill_enabled: checked }))
                }
              />
            </div>

            {draft.drill_enabled && (
              <>
                {/* Cron 表达式 */}
                <div>
                  <label htmlFor="policy-edit-drill-cron" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.cron')}
                  </label>
                  <CronGenerator
                    id="policy-edit-drill-cron"
                    value={draft.drill_cron ?? ""}
                    onChange={(val) => setDraft((prev) => ({ ...prev, drill_cron: val }))}
                    disabled={saving}
                  />
                </div>

                {/* 沙箱节点 */}
                <div>
                  <label htmlFor="policy-edit-drill-node" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.sandboxNode')}
                  </label>
                  <Select
                    id="policy-edit-drill-node"
                    value={draft.drill_target_node_id == null ? "" : String(draft.drill_target_node_id)}
                    onChange={(e) =>
                      setDraft((prev) => ({
                        ...prev,
                        drill_target_node_id: e.target.value === "" ? null : Number(e.target.value),
                      }))
                    }
                  >
                    <option value="">{t('policyEditor.drill.sandboxNodeSelect')}</option>
                    {sandboxNodes.map((n) => (
                      <option key={n.id} value={String(n.id)}>
                        {n.name} ({n.host})
                      </option>
                    ))}
                  </Select>
                  {sandboxNodes.length === 0 && nodes.length > 0 && (
                    <p className="mt-1 text-xs text-muted-foreground">{t('policyEditor.drill.sandboxMustNotBeSource')}</p>
                  )}
                </div>

                {/* 恢复路径 */}
                <div>
                  <label htmlFor="policy-edit-drill-path" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.restorePath')}
                  </label>
                  <Input
                    id="policy-edit-drill-path"
                    placeholder="/tmp/xirang-drill"
                    value={draft.drill_restore_path ?? "/tmp/xirang-drill"}
                    onChange={(e) =>
                      setDraft((prev) => ({ ...prev, drill_restore_path: e.target.value }))
                    }
                  />
                </div>

                {/* pre_verify */}
                <div>
                  <label htmlFor="policy-edit-drill-pre-verify" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.preVerify')}
                  </label>
                  <Textarea
                    id="policy-edit-drill-pre-verify"
                    className="min-h-16 text-xs font-mono"
                    placeholder={t('policyEditor.drill.preVerifyPlaceholder')}
                    value={draft.drill_pre_verify ?? ""}
                    onChange={(e) =>
                      setDraft((prev) => ({ ...prev, drill_pre_verify: e.target.value }))
                    }
                  />
                </div>

                {/* verify */}
                <div>
                  <label htmlFor="policy-edit-drill-verify" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.verify')}
                  </label>
                  <Textarea
                    id="policy-edit-drill-verify"
                    className="min-h-16 text-xs font-mono"
                    placeholder={t('policyEditor.drill.verifyPlaceholder')}
                    value={draft.drill_verify ?? ""}
                    onChange={(e) =>
                      setDraft((prev) => ({ ...prev, drill_verify: e.target.value }))
                    }
                  />
                </div>

                {/* post_verify */}
                <div>
                  <label htmlFor="policy-edit-drill-post-verify" className="mb-1 block text-sm font-medium">
                    {t('policyEditor.drill.postVerify')}
                  </label>
                  <Textarea
                    id="policy-edit-drill-post-verify"
                    className="min-h-16 text-xs font-mono"
                    placeholder={t('policyEditor.drill.postVerifyPlaceholder')}
                    value={draft.drill_post_verify ?? ""}
                    onChange={(e) =>
                      setDraft((prev) => ({ ...prev, drill_post_verify: e.target.value }))
                    }
                  />
                </div>

                {/* 自动清理开关 */}
                <div className="flex items-center justify-between">
                  <span className="text-sm">{t('policyEditor.drill.autoCleanup')}</span>
                  <Switch
                    aria-label={t('policyEditor.drill.autoCleanup')}
                    checked={draft.drill_auto_cleanup ?? true}
                    onCheckedChange={(checked) =>
                      setDraft((prev) => ({ ...prev, drill_auto_cleanup: checked }))
                    }
                  />
                </div>

                {/* 手动触发按钮（仅编辑已有策略时可用） */}
                {isEditing && (
                  <div>
                    <button
                      type="button"
                      className="inline-flex items-center gap-1.5 rounded-md bg-primary/10 px-3 py-1.5 text-sm font-medium text-primary hover:bg-primary/20 transition-colors disabled:opacity-50"
                      disabled={triggering}
                      onClick={() => setDrillConfirmOpen(true)}
                    >
                      {triggering ? t('common.executing') : t('policyEditor.drill.trigger')}
                    </button>
                  </div>
                )}
              </>
            )}
          </div>
        )}
      </div>

      {/* 手动触发确认对话框 */}
      <AlertDialog open={drillConfirmOpen} onOpenChange={setDrillConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('policyEditor.drill.trigger')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('policyEditor.drill.triggerConfirm')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={handleTriggerDrill}>
              {t('common.confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </FormDialog>
  );
}

export type { PolicyDraft };
