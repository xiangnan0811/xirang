import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Trash2, Zap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast-sonner";
import { useAuth } from "@/context/auth-context.hooks";
import { useConfirm } from "@/hooks/use-confirm";
import { createAutomationRulesApi } from "@/lib/api/automation-rules";
import { getErrorMessage } from "@/lib/utils";
import type { AutomationRule, AutomationRuleInput } from "@/types/domain";

const EVENT_TYPES = [
  "anomaly_detected",
  "backup_failed",
  "backup_succeeded",
  "drill_failed",
  "node_offline",
  "node_disk_high",
] as const;

const ACTION_TYPES = [
  "pause_policy",
  "disable_policy",
  "trigger_task",
  "send_notification",
] as const;

/** Filter field labels available for each event type. */
const FILTER_KEYS_BY_EVENT: Record<string, string[]> = {
  anomaly_detected: ["detector", "metric", "severity"],
  backup_failed: ["policy_id", "node_id", "executor_type"],
  backup_succeeded: ["policy_id", "node_id", "executor_type"],
  drill_failed: ["policy_id"],
  node_offline: ["node_id"],
  node_disk_high: ["node_id"],
};

/** Config field labels available for each action type. */
const CONFIG_KEYS_BY_ACTION: Record<string, string[]> = {
  pause_policy: ["policy_id"],
  disable_policy: ["policy_id"],
  trigger_task: ["task_id"],
  send_notification: ["message"],
};

/** Key-value pair helper for filter / config editors. */
type KV = { key: string; value: string };

function kvToRecord(kvs: KV[]): Record<string, string> {
  const rec: Record<string, string> = {};
  for (const kv of kvs) {
    if (kv.key.trim()) {
      rec[kv.key.trim()] = kv.value;
    }
  }
  return rec;
}

function recordToKV(rec: Record<string, string> | undefined): KV[] {
  if (!rec) return [];
  return Object.entries(rec).map(([key, value]) => ({ key, value }));
}

export function AutomationRulesPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { confirm, dialog } = useConfirm();

  const [rules, setRules] = useState<AutomationRule[]>([]);
  const [loading, setLoading] = useState(true);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<AutomationRule | null>(null);
  const [saving, setSaving] = useState(false);

  // Form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [eventType, setEventType] = useState("");
  const [actionType, setActionType] = useState("");
  const [filters, setFilters] = useState<KV[]>([]);
  const [configs, setConfigs] = useState<KV[]>([]);
  const [enabled, setEnabled] = useState(true);

  const isEditing = Boolean(editingRule);

  const fetchRules = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const data = await createAutomationRulesApi().list(token);
      setRules(data);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    fetchRules();
  }, [fetchRules]);

  // Reset form when dialog opens/closes or editing rule changes
  useEffect(() => {
    if (!editorOpen) return;
    if (editingRule) {
      setName(editingRule.name);
      setDescription(editingRule.description ?? "");
      setEventType(editingRule.event_type);
      setActionType(editingRule.action_type);
      setFilters(recordToKV(editingRule.event_filter));
      setConfigs(recordToKV(editingRule.action_config));
      setEnabled(editingRule.enabled);
    } else {
      setName("");
      setDescription("");
      setEventType("");
      setActionType("");
      setFilters([]);
      setConfigs([]);
      setEnabled(true);
    }
  }, [editorOpen, editingRule]);

  const availableFilterKeys = useMemo(
    () => FILTER_KEYS_BY_EVENT[eventType] ?? [],
    [eventType],
  );

  const availableConfigKeys = useMemo(
    () => CONFIG_KEYS_BY_ACTION[actionType] ?? [],
    [actionType],
  );

  const openCreateDialog = () => {
    setEditingRule(null);
    setEditorOpen(true);
  };

  const openEditDialog = (rule: AutomationRule) => {
    setEditingRule(rule);
    setEditorOpen(true);
  };

  const handleDelete = async (rule: AutomationRule) => {
    if (!token) return;
    const ok = await confirm({
      title: t("automation.confirmDeleteTitle"),
      description: t("automation.confirmDeleteDesc", { name: rule.name }),
    });
    if (!ok) return;
    try {
      await createAutomationRulesApi().delete(token, rule.id);
      toast.success(t("automation.deleted", { name: rule.name }));
      fetchRules();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleToggleEnabled = async (rule: AutomationRule) => {
    if (!token) return;
    try {
      await createAutomationRulesApi().update(token, rule.id, {
        name: rule.name,
        event_type: rule.event_type,
        action_type: rule.action_type,
        event_filter: rule.event_filter,
        action_config: rule.action_config,
        enabled: !rule.enabled,
      });
      fetchRules();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleSubmit = async () => {
    if (!token) return;
    if (!name.trim()) {
      toast.error(t("automation.validation.nameRequired"));
      return;
    }
    if (!eventType) {
      toast.error(t("automation.validation.eventTypeRequired"));
      return;
    }
    if (!actionType) {
      toast.error(t("automation.validation.actionTypeRequired"));
      return;
    }

    setSaving(true);
    const input: AutomationRuleInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      event_type: eventType,
      action_type: actionType,
      event_filter: kvToRecord(filters),
      action_config: kvToRecord(configs),
      enabled,
    };

    try {
      const api = createAutomationRulesApi();
      if (isEditing && editingRule) {
        await api.update(token, editingRule.id, input);
        toast.success(t("automation.updated"));
      } else {
        await api.create(token, input);
        toast.success(t("automation.created"));
      }
      fetchRules();
      setEditorOpen(false);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setSaving(false);
    }
  };

  const addFilter = () => setFilters((prev) => [...prev, { key: "", value: "" }]);
  const addConfig = () => setConfigs((prev) => [...prev, { key: "", value: "" }]);

  const updateFilter = (idx: number, field: "key" | "value", val: string) => {
    setFilters((prev) => {
      const next = [...prev];
      next[idx] = { ...next[idx], [field]: val };
      return next;
    });
  };

  const removeFilter = (idx: number) =>
    setFilters((prev) => prev.filter((_, i) => i !== idx));

  const updateConfig = (idx: number, field: "key" | "value", val: string) => {
    setConfigs((prev) => {
      const next = [...prev];
      next[idx] = { ...next[idx], [field]: val };
      return next;
    });
  };

  const removeConfig = (idx: number) =>
    setConfigs((prev) => prev.filter((_, i) => i !== idx));

  // Display helpers
  const eventTypeLabel = (type: string) =>
    t(`automation.eventTypes.${type}`, type);
  const actionTypeLabel = (type: string) =>
    t(`automation.actionTypes.${type}`, type);
  const filterFieldLabel = (key: string) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const labels = (t as any)("automation.filterFields", { returnObjects: true });
    return labels?.[key] ?? key;
  };
  const configFieldLabel = (key: string) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const labels = (t as any)("automation.configFields", { returnObjects: true });
    return labels?.[key] ?? key;
  };

  const renderFilterSummary = (rule: AutomationRule) => {
    const entries = Object.entries(rule.event_filter ?? {});
    if (entries.length === 0) return <span className="text-muted-foreground">—</span>;
    return entries.map(([k, v]) => (
      <Badge key={k} tone="neutral" className="mr-1">
        {filterFieldLabel(k)}: {v}
      </Badge>
    ));
  };

  const renderConfigSummary = (rule: AutomationRule) => {
    const entries = Object.entries(rule.action_config ?? {});
    if (entries.length === 0) return <span className="text-muted-foreground">—</span>;
    return entries.map(([k, v]) => (
      <Badge key={k} tone="info" className="mr-1">
        {configFieldLabel(k)}: {v}
      </Badge>
    ));
  };

  return (
    <div className="animate-fade-in space-y-5">
      {/* ── Header ── */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">
            {t("automation.pageTitle")}
          </h1>
          <p className="text-sm text-muted-foreground">
            {t("automation.pageDesc")}
          </p>
        </div>
        <Button onClick={openCreateDialog}>
          <Zap className="mr-1.5 size-4" />
          {t("automation.createBtn")}
        </Button>
      </div>

      {/* ── Table ── */}
      <Card className="overflow-hidden rounded-lg border border-border bg-card">
        <CardContent className="p-0">
          {loading ? (
            <div className="flex items-center justify-center py-16">
              <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
            </div>
          ) : rules.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
              <Zap className="mb-3 size-10 opacity-30" />
              <p className="text-sm">{t("automation.empty")}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={openCreateDialog}
              >
                {t("automation.createBtn")}
              </Button>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/50">
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("automation.colName")}
                    </th>
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("automation.colEventType")}
                    </th>
                    <th className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("automation.eventFilter")}
                    </th>
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("automation.colActionType")}
                    </th>
                    <th className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("automation.actionConfig")}
                    </th>
                    <th className="px-4 py-3 text-center font-medium text-muted-foreground">
                      {t("automation.colEnabled")}
                    </th>
                    <th className="px-4 py-3 text-right font-medium text-muted-foreground">
                      {t("automation.colActions")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rules.map((rule) => (
                    <tr
                      key={rule.id}
                      className="border-b border-border transition-colors hover:bg-muted/30"
                    >
                      <td className="px-4 py-3 font-medium">
                        <div>{rule.name}</div>
                        {rule.description && (
                          <div className="mt-0.5 text-xs text-muted-foreground">
                            {rule.description}
                          </div>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <Badge tone="warning">{eventTypeLabel(rule.event_type)}</Badge>
                      </td>
                      <td className="hidden px-4 py-3 md:table-cell">
                        {renderFilterSummary(rule)}
                      </td>
                      <td className="px-4 py-3">
                        <Badge tone="success">{actionTypeLabel(rule.action_type)}</Badge>
                      </td>
                      <td className="hidden px-4 py-3 md:table-cell">
                        {renderConfigSummary(rule)}
                      </td>
                      <td className="px-4 py-3 text-center">
                        <Switch
                          checked={rule.enabled}
                          onCheckedChange={() => handleToggleEnabled(rule)}
                          aria-label={
                            rule.enabled ? t("common.disable") : t("common.enable")
                          }
                        />
                      </td>
                      <td className="px-4 py-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.edit")}
                            onClick={() => openEditDialog(rule)}
                          >
                            <Pencil className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.delete")}
                            onClick={() => handleDelete(rule)}
                          >
                            <Trash2 className="size-3.5 text-destructive" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* ── Editor Dialog ── */}
      <FormDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        size="md"
        icon={<Zap className="size-5 text-primary" />}
        title={isEditing ? t("automation.editTitle") : t("automation.createTitle")}
        description={
          isEditing ? t("automation.editDesc") : t("automation.createDesc")
        }
        saving={saving}
        onSubmit={handleSubmit}
        submitLabel={isEditing ? t("common.save") : t("common.create")}
      >
        {/* Name */}
        <div>
          <label
            htmlFor="automation-rule-name"
            className="mb-1 block text-sm font-medium"
          >
            {t("common.name")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">
              *
            </span>
          </label>
          <Input
            id="automation-rule-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </div>

        {/* Description */}
        <div>
          <label
            htmlFor="automation-rule-desc"
            className="mb-1 block text-sm font-medium"
          >
            {t("common.description")}
          </label>
          <Input
            id="automation-rule-desc"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </div>

        {/* Event Type */}
        <div>
          <label
            htmlFor="automation-rule-event-type"
            className="mb-1 block text-sm font-medium"
          >
            {t("automation.eventType")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">
              *
            </span>
          </label>
          <Select
            id="automation-rule-event-type"
            containerClassName="w-full"
            value={eventType}
            onChange={(event) => setEventType(event.target.value)}
          >
            <option value="">—</option>
            {EVENT_TYPES.map((et) => (
              <option key={et} value={et}>
                {eventTypeLabel(et)}
              </option>
            ))}
          </Select>
        </div>

        {/* Event Filter */}
        {eventType && (
          <div>
            <div className="mb-1 flex items-center justify-between">
              <label className="text-sm font-medium">
                {t("automation.eventFilter")}
              </label>
              <Button
                variant="ghost"
                size="sm"
                type="button"
                className="h-auto px-2 py-0.5 text-xs"
                onClick={addFilter}
              >
                + {t("automation.addFilterField")}
              </Button>
            </div>
            <p className="mb-2 text-xs text-muted-foreground">
              {t("automation.filterHint", {
                policyVar: "{{.PolicyID}}",
              })}
            </p>
            <div className="space-y-2">
              {filters.map((kv, idx) => (
                <div key={idx} className="flex items-center gap-2">
                  <Select
                    containerClassName="flex-1"
                    value={kv.key}
                    onChange={(event) =>
                      updateFilter(idx, "key", event.target.value)
                    }
                  >
                    <option value="">
                      {t("automation.filterKeyPlaceholder")}
                    </option>
                    {availableFilterKeys.map((k) => (
                      <option key={k} value={k}>
                        {filterFieldLabel(k)}
                      </option>
                    ))}
                  </Select>
                  <Input
                    className="flex-1"
                    placeholder={t("automation.filterValuePlaceholder")}
                    value={kv.value}
                    onChange={(event) =>
                      updateFilter(idx, "value", event.target.value)
                    }
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    type="button"
                    className="size-8 shrink-0"
                    aria-label={t("automation.removeFilterField")}
                    onClick={() => removeFilter(idx)}
                  >
                    <Trash2 className="size-3.5 text-muted-foreground" />
                  </Button>
                </div>
              ))}
              {filters.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  {t("automation.filtersByEvent." + eventType, "")}
                </p>
              )}
            </div>
          </div>
        )}

        {/* Action Type */}
        <div>
          <label
            htmlFor="automation-rule-action-type"
            className="mb-1 block text-sm font-medium"
          >
            {t("automation.actionType")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">
              *
            </span>
          </label>
          <Select
            id="automation-rule-action-type"
            containerClassName="w-full"
            value={actionType}
            onChange={(event) => setActionType(event.target.value)}
          >
            <option value="">—</option>
            {ACTION_TYPES.map((at) => (
              <option key={at} value={at}>
                {actionTypeLabel(at)}
              </option>
            ))}
          </Select>
        </div>

        {/* Action Config */}
        {actionType && (
          <div>
            <div className="mb-1 flex items-center justify-between">
              <label className="text-sm font-medium">
                {t("automation.actionConfig")}
              </label>
              <Button
                variant="ghost"
                size="sm"
                type="button"
                className="h-auto px-2 py-0.5 text-xs"
                onClick={addConfig}
              >
                + {t("automation.addConfigField")}
              </Button>
            </div>
            <p className="mb-2 text-xs text-muted-foreground">
              {t("automation.configHint", {
                policyVar: "{{.PolicyID}}",
              })}
            </p>
            <div className="space-y-2">
              {configs.map((kv, idx) => (
                <div key={idx} className="flex items-center gap-2">
                  <Select
                    containerClassName="flex-1"
                    value={kv.key}
                    onChange={(event) =>
                      updateConfig(idx, "key", event.target.value)
                    }
                  >
                    <option value="">
                      {t("automation.filterKeyPlaceholder")}
                    </option>
                    {availableConfigKeys.map((k) => (
                      <option key={k} value={k}>
                        {configFieldLabel(k)}
                      </option>
                    ))}
                    <option value="__custom__">
                      {t("common.unknown")}
                    </option>
                  </Select>
                  {kv.key === "__custom__" ? (
                    <div className="flex flex-1 gap-2">
                      <Input
                        className="flex-1"
                        placeholder={t("automation.filterKeyPlaceholder")}
                        value=""
                        onChange={(event) =>
                          updateConfig(idx, "key", event.target.value)
                        }
                      />
                      <Input
                        className="flex-1"
                        placeholder={t("automation.configValuePlaceholder")}
                        value={kv.value}
                        onChange={(event) =>
                          updateConfig(idx, "value", event.target.value)
                        }
                      />
                    </div>
                  ) : (
                    <Input
                      className="flex-1"
                      placeholder={t("automation.configValuePlaceholder")}
                      value={kv.value}
                      onChange={(event) =>
                        updateConfig(idx, "value", event.target.value)
                      }
                    />
                  )}
                  <Button
                    variant="ghost"
                    size="icon"
                    type="button"
                    className="size-8 shrink-0"
                    aria-label={t("automation.removeFilterField")}
                    onClick={() => removeConfig(idx)}
                  >
                    <Trash2 className="size-3.5 text-muted-foreground" />
                  </Button>
                </div>
              ))}
              {configs.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  {availableConfigKeys.map((k) => configFieldLabel(k)).join(", ")}
                </p>
              )}
            </div>
          </div>
        )}

        {/* Enabled */}
        <div className="flex items-center justify-between rounded-md border border-border px-3 py-2.5">
          <label
            htmlFor="automation-rule-enabled"
            className="text-sm font-medium"
          >
            {t("common.enabled")}
          </label>
          <Switch
            id="automation-rule-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>
      </FormDialog>

      {/* ── Delete Confirmation ── */}
      {dialog}
    </div>
  );
}
