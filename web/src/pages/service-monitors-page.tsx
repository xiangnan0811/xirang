import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Activity, Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import { useAuth } from "@/context/auth-context";
import { useConfirm } from "@/hooks/use-confirm";
import { createServiceMonitorsApi } from "@/lib/api/service-monitors";
import { getErrorMessage } from "@/lib/utils";
import type { NewServiceMonitorInput, ServiceMonitor } from "@/types/domain";

const HTTP_METHODS = ["GET", "POST", "HEAD"] as const;

type HeaderKV = { key: string; value: string };

function parseHeaders(raw: string): HeaderKV[] {
  if (!raw || raw === "{}") return [];
  try {
    const obj = JSON.parse(raw);
    if (typeof obj === "object" && obj !== null) {
      return Object.entries(obj).map(([k, v]) => ({ key: k, value: String(v) }));
    }
  } catch { /* ignore */ }
  return [];
}

function headersToJSON(kvs: HeaderKV[]): string {
  const obj: Record<string, string> = {};
  for (const kv of kvs) {
    if (kv.key.trim()) {
      obj[kv.key.trim()] = kv.value;
    }
  }
  return JSON.stringify(obj);
}

function statusDot(status: string) {
  if (status === "up") return <span className="mr-1.5 inline-block size-2 rounded-full bg-emerald-500" aria-hidden="true" />;
  if (status === "down") return <span className="mr-1.5 inline-block size-2 rounded-full bg-red-500" aria-hidden="true" />;
  return <span className="mr-1.5 inline-block size-2 rounded-full bg-muted-foreground/40" aria-hidden="true" />;
}

export function ServiceMonitorsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { confirm, dialog } = useConfirm();

  const [monitors, setMonitors] = useState<ServiceMonitor[]>([]);
  const [loading, setLoading] = useState(true);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingMonitor, setEditingMonitor] = useState<ServiceMonitor | null>(null);
  const [saving, setSaving] = useState(false);

  // Form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [type, setType] = useState<"http" | "tcp">("http");
  const [target, setTarget] = useState("");
  const [intervalSeconds, setIntervalSeconds] = useState("60");
  const [timeoutSeconds, setTimeoutSeconds] = useState("10");
  const [httpMethod, setHttpMethod] = useState("GET");
  const [httpExpectedStatus, setHttpExpectedStatus] = useState("200");
  const [httpHeaders, setHttpHeaders] = useState<HeaderKV[]>([]);
  const [enabled, setEnabled] = useState(true);

  const isEditing = Boolean(editingMonitor);

  const fetchMonitors = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const data = await createServiceMonitorsApi().list(token);
      setMonitors(data);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    fetchMonitors();
  }, [fetchMonitors]);

  // Reset form when dialog opens/closes or editing monitor changes
  useEffect(() => {
    if (!editorOpen) return;
    if (editingMonitor) {
      setName(editingMonitor.name);
      setDescription(editingMonitor.description ?? "");
      setType(editingMonitor.type as "http" | "tcp");
      setTarget(editingMonitor.target);
      setIntervalSeconds(String(editingMonitor.interval_seconds));
      setTimeoutSeconds(String(editingMonitor.timeout_seconds));
      setHttpMethod(editingMonitor.http_method || "GET");
      setHttpExpectedStatus(String(editingMonitor.http_expected_status));
      setHttpHeaders(parseHeaders(editingMonitor.http_headers));
      setEnabled(editingMonitor.enabled);
    } else {
      setName("");
      setDescription("");
      setType("http");
      setTarget("");
      setIntervalSeconds("60");
      setTimeoutSeconds("10");
      setHttpMethod("GET");
      setHttpExpectedStatus("200");
      setHttpHeaders([]);
      setEnabled(true);
    }
  }, [editorOpen, editingMonitor]);

  const openCreateDialog = () => {
    setEditingMonitor(null);
    setEditorOpen(true);
  };

  const openEditDialog = (monitor: ServiceMonitor) => {
    setEditingMonitor(monitor);
    setEditorOpen(true);
  };

  const handleDelete = async (monitor: ServiceMonitor) => {
    if (!token) return;
    const ok = await confirm({
      title: t("serviceMonitor.confirmDeleteTitle"),
      description: t("serviceMonitor.confirmDeleteDesc", { name: monitor.name }),
    });
    if (!ok) return;
    try {
      await createServiceMonitorsApi().delete(token, monitor.id);
      toast.success(t("serviceMonitor.deleted", { name: monitor.name }));
      fetchMonitors();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleToggleEnabled = async (monitor: ServiceMonitor) => {
    if (!token) return;
    try {
      const input: NewServiceMonitorInput = {
        name: monitor.name,
        type: monitor.type as "http" | "tcp",
        target: monitor.target,
        interval_seconds: monitor.interval_seconds,
        timeout_seconds: monitor.timeout_seconds,
        http_method: monitor.http_method,
        http_expected_status: monitor.http_expected_status,
        http_headers: monitor.http_headers,
        enabled: !monitor.enabled,
      };
      await createServiceMonitorsApi().update(token, monitor.id, input);
      fetchMonitors();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleSubmit = async () => {
    if (!token) return;
    if (!name.trim()) {
      toast.error(t("serviceMonitor.validation.nameRequired"));
      return;
    }
    if (!type) {
      toast.error(t("serviceMonitor.validation.typeRequired"));
      return;
    }
    if (!target.trim()) {
      toast.error(t("serviceMonitor.validation.targetRequired"));
      return;
    }

    setSaving(true);
    const input: NewServiceMonitorInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      type,
      target: target.trim(),
      interval_seconds: Number(intervalSeconds) || 60,
      timeout_seconds: Number(timeoutSeconds) || 10,
      enabled,
    };

    if (type === "http") {
      input.http_method = httpMethod;
      input.http_expected_status = Number(httpExpectedStatus) || 200;
      input.http_headers = headersToJSON(httpHeaders);
    }

    try {
      const api = createServiceMonitorsApi();
      if (isEditing && editingMonitor) {
        await api.update(token, editingMonitor.id, input);
        toast.success(t("serviceMonitor.updated"));
      } else {
        await api.create(token, input);
        toast.success(t("serviceMonitor.created"));
      }
      fetchMonitors();
      setEditorOpen(false);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setSaving(false);
    }
  };

  const addHeader = () => setHttpHeaders((prev) => [...prev, { key: "", value: "" }]);
  const updateHeader = (idx: number, field: "key" | "value", val: string) => {
    setHttpHeaders((prev) => {
      const next = [...prev];
      next[idx] = { ...next[idx], [field]: val };
      return next;
    });
  };
  const removeHeader = (idx: number) =>
    setHttpHeaders((prev) => prev.filter((_, i) => i !== idx));

  const lastStatusLabel = (status: string) => {
    if (status === "up") return t("serviceMonitor.statusUp");
    if (status === "down") return t("serviceMonitor.statusDown");
    return t("serviceMonitor.statusUnknown");
  };

  return (
    <div className="animate-fade-in space-y-5">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">
            {t("serviceMonitor.pageTitle")}
          </h1>
          <p className="text-sm text-muted-foreground">
            {t("serviceMonitor.pageDesc")}
          </p>
        </div>
        <Button onClick={openCreateDialog}>
          <Plus className="mr-1.5 size-4" />
          {t("serviceMonitor.createBtn")}
        </Button>
      </div>

      {/* Table */}
      <Card className="overflow-hidden rounded-lg border border-border bg-card">
        <CardContent className="p-0">
          {loading ? (
            <div className="flex items-center justify-center py-16">
              <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
            </div>
          ) : monitors.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
              <Activity className="mb-3 size-10 opacity-30" />
              <p className="text-sm">{t("serviceMonitor.empty")}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={openCreateDialog}
              >
                {t("serviceMonitor.createBtn")}
              </Button>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/50">
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colName")}
                    </th>
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colType")}
                    </th>
                    <th className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("serviceMonitor.colTarget")}
                    </th>
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colStatus")}
                    </th>
                    <th className="hidden px-4 py-3 text-right font-medium text-muted-foreground sm:table-cell">
                      {t("serviceMonitor.colUptime")}
                    </th>
                    <th className="px-4 py-3 text-center font-medium text-muted-foreground">
                      {t("serviceMonitor.colEnabled")}
                    </th>
                    <th className="px-4 py-3 text-right font-medium text-muted-foreground">
                      {t("serviceMonitor.colActions")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {monitors.map((monitor) => (
                    <tr
                      key={monitor.id}
                      className="border-b border-border transition-colors hover:bg-muted/30"
                    >
                      <td className="px-4 py-3 font-medium">
                        <div>{monitor.name}</div>
                        {monitor.description && (
                          <div className="mt-0.5 text-xs text-muted-foreground">
                            {monitor.description}
                          </div>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <Badge tone={monitor.type === "http" ? "info" : "neutral"}>
                          {monitor.type.toUpperCase()}
                        </Badge>
                      </td>
                      <td className="hidden max-w-[180px] truncate px-4 py-3 text-muted-foreground md:table-cell">
                        {monitor.target}
                      </td>
                      <td className="px-4 py-3">
                        <span className="inline-flex items-center">
                          {statusDot(monitor.last_status)}
                          <span className={monitor.last_status === "down" ? "text-red-600" : ""}>
                            {lastStatusLabel(monitor.last_status)}
                          </span>
                        </span>
                      </td>
                      <td className="hidden px-4 py-3 text-right tabular-nums sm:table-cell">
                        {(monitor.uptime_pct ?? 0).toFixed(1)}%
                      </td>
                      <td className="px-4 py-3 text-center">
                        <Switch
                          checked={monitor.enabled}
                          onCheckedChange={() => handleToggleEnabled(monitor)}
                          aria-label={
                            monitor.enabled ? t("common.disable") : t("common.enable")
                          }
                        />
                      </td>
                      <td className="px-4 py-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.edit")}
                            onClick={() => openEditDialog(monitor)}
                          >
                            <Pencil className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.delete")}
                            onClick={() => handleDelete(monitor)}
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

      {/* Editor Dialog */}
      <FormDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        size="md"
        icon={<Activity className="size-5 text-primary" />}
        title={isEditing ? t("serviceMonitor.editTitle") : t("serviceMonitor.createTitle")}
        description={
          isEditing ? t("serviceMonitor.editDesc") : t("serviceMonitor.createDesc")
        }
        saving={saving}
        onSubmit={handleSubmit}
        submitLabel={isEditing ? t("common.save") : t("common.create")}
      >
        {/* Name */}
        <div>
          <label
            htmlFor="sm-name"
            className="mb-1 block text-sm font-medium"
          >
            {t("common.name")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">*</span>
          </label>
          <Input
            id="sm-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </div>

        {/* Description */}
        <div>
          <label
            htmlFor="sm-desc"
            className="mb-1 block text-sm font-medium"
          >
            {t("common.description")}
          </label>
          <Input
            id="sm-desc"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </div>

        {/* Type */}
        <div>
          <label
            htmlFor="sm-type"
            className="mb-1 block text-sm font-medium"
          >
            {t("serviceMonitor.fieldType")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">*</span>
          </label>
          <Select
            id="sm-type"
            containerClassName="w-full"
            value={type}
            onChange={(event) => setType(event.target.value as "http" | "tcp")}
          >
            <option value="http">HTTP</option>
            <option value="tcp">TCP</option>
          </Select>
        </div>

        {/* Target */}
        <div>
          <label
            htmlFor="sm-target"
            className="mb-1 block text-sm font-medium"
          >
            {t("serviceMonitor.fieldTarget")}
            <span className="ml-0.5 text-destructive" aria-hidden="true">*</span>
          </label>
          <Input
            id="sm-target"
            placeholder={type === "tcp" ? "host:port" : "https://example.com/health"}
            value={target}
            onChange={(event) => setTarget(event.target.value)}
          />
        </div>

        {/* Interval + Timeout */}
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label
              htmlFor="sm-interval"
              className="mb-1 block text-sm font-medium"
            >
              {t("serviceMonitor.fieldInterval")}
            </label>
            <Input
              id="sm-interval"
              type="number"
              min={5}
              max={3600}
              value={intervalSeconds}
              onChange={(event) => setIntervalSeconds(event.target.value)}
            />
          </div>
          <div>
            <label
              htmlFor="sm-timeout"
              className="mb-1 block text-sm font-medium"
            >
              {t("serviceMonitor.fieldTimeout")}
            </label>
            <Input
              id="sm-timeout"
              type="number"
              min={1}
              max={300}
              value={timeoutSeconds}
              onChange={(event) => setTimeoutSeconds(event.target.value)}
            />
          </div>
        </div>

        {/* HTTP-specific fields */}
        {type === "http" && (
          <>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label
                  htmlFor="sm-http-method"
                  className="mb-1 block text-sm font-medium"
                >
                  {t("serviceMonitor.fieldHttpMethod")}
                </label>
                <Select
                  id="sm-http-method"
                  containerClassName="w-full"
                  value={httpMethod}
                  onChange={(event) => setHttpMethod(event.target.value)}
                >
                  {HTTP_METHODS.map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </Select>
              </div>
              <div>
                <label
                  htmlFor="sm-http-status"
                  className="mb-1 block text-sm font-medium"
                >
                  {t("serviceMonitor.fieldHttpExpectedStatus")}
                </label>
                <Input
                  id="sm-http-status"
                  type="number"
                  value={httpExpectedStatus}
                  onChange={(event) => setHttpExpectedStatus(event.target.value)}
                />
              </div>
            </div>

            {/* HTTP Headers */}
            <div>
              <div className="mb-1 flex items-center justify-between">
                <label className="text-sm font-medium">
                  {t("serviceMonitor.fieldHttpHeaders")}
                </label>
                <Button
                  variant="ghost"
                  size="sm"
                  type="button"
                  className="h-auto px-2 py-0.5 text-xs"
                  onClick={addHeader}
                >
                  + {t("serviceMonitor.addHeader")}
                </Button>
              </div>
              <p className="mb-2 text-xs text-muted-foreground">
                {t("serviceMonitor.httpHeadersHint")}
              </p>
              <div className="space-y-2">
                {httpHeaders.map((kv, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <Input
                      className="flex-1"
                      placeholder={t("serviceMonitor.headerKeyPlaceholder")}
                      value={kv.key}
                      onChange={(event) => updateHeader(idx, "key", event.target.value)}
                    />
                    <Input
                      className="flex-1"
                      placeholder={t("serviceMonitor.headerValuePlaceholder")}
                      value={kv.value}
                      onChange={(event) => updateHeader(idx, "value", event.target.value)}
                    />
                    <Button
                      variant="ghost"
                      size="icon"
                      type="button"
                      className="size-8 shrink-0"
                      aria-label={t("serviceMonitor.removeHeader")}
                      onClick={() => removeHeader(idx)}
                    >
                      <Trash2 className="size-3.5 text-muted-foreground" />
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          </>
        )}

        {/* Enabled toggle */}
        <div className="flex items-center justify-between rounded-md border border-border px-3 py-2.5">
          <label htmlFor="sm-enabled" className="text-sm font-medium">
            {t("common.enabled")}
          </label>
          <Switch
            id="sm-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>
      </FormDialog>

      {/* Delete Confirmation */}
      {dialog}
    </div>
  );
}
