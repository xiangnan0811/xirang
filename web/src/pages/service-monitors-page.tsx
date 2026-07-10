import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Activity, Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { Switch } from "@/components/ui/switch";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { PageHero } from "@/components/ui/page-hero";
import { toast } from "@/components/ui/toast-sonner";
import { useAuth } from "@/context/auth-context.hooks";
import { useConfirm } from "@/hooks/use-confirm";
import { createServiceMonitorsApi } from "@/lib/api/service-monitors";
import { getErrorMessage } from "@/lib/utils";
import type { HeaderKV, HttpMethod, NewServiceMonitorInput, ServiceMonitorView } from "@/types/domain";

const HTTP_METHODS = ["GET", "POST", "HEAD"] as const;
const DEFAULT_INTERVAL_SECONDS = 60;
const DEFAULT_TIMEOUT_SECONDS = 10;
const DEFAULT_HTTP_EXPECTED_STATUS = 200;

function statusDot(status: string) {
  if (status === "up") return <span className="mr-1.5 inline-block size-2 rounded-full bg-success" aria-hidden="true" />;
  if (status === "down") return <span className="mr-1.5 inline-block size-2 rounded-full bg-destructive" aria-hidden="true" />;
  return <span className="mr-1.5 inline-block size-2 rounded-full bg-muted-foreground/40" aria-hidden="true" />;
}

function clampNumberInput(value: string, min: number, max: number, fallback: number): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, Math.round(parsed)));
}

export function ServiceMonitorsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { confirm, dialog } = useConfirm();

  const [monitors, setMonitors] = useState<ServiceMonitorView[]>([]);
  const [loading, setLoading] = useState(true);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingMonitor, setEditingMonitor] = useState<ServiceMonitorView | null>(null);
  const [saving, setSaving] = useState(false);

  // Form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [type, setType] = useState<"http" | "tcp">("http");
  const [target, setTarget] = useState("");
  const [intervalSeconds, setIntervalSeconds] = useState("60");
  const [timeoutSeconds, setTimeoutSeconds] = useState("10");
  const [httpMethod, setHttpMethod] = useState<HttpMethod>("GET");
  const [httpExpectedStatus, setHttpExpectedStatus] = useState("200");
  const [httpHeaders, setHttpHeaders] = useState<HeaderKV[]>([]);

  const [enabled, setEnabled] = useState(true);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

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
      setIntervalSeconds(String(editingMonitor.intervalSeconds));
      setTimeoutSeconds(String(editingMonitor.timeoutSeconds));
      setHttpMethod(editingMonitor.httpMethod || "GET");
      setHttpExpectedStatus(String(editingMonitor.httpExpectedStatus));
      setHttpHeaders(editingMonitor.httpHeaderList ?? []);
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
    setFieldErrors({});
  }, [editorOpen, editingMonitor]);

  const openCreateDialog = () => {
    setEditingMonitor(null);
    setEditorOpen(true);
  };

  const openEditDialog = (monitor: ServiceMonitorView) => {
    setEditingMonitor(monitor);
    setEditorOpen(true);
  };

  const handleDelete = async (monitor: ServiceMonitorView) => {
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

  const handleToggleEnabled = async (monitor: ServiceMonitorView) => {
    if (!token) return;
    try {
      const input: NewServiceMonitorInput = {
        name: monitor.name,
        type: monitor.type,
        target: monitor.target,
        intervalSeconds: monitor.intervalSeconds,
        timeoutSeconds: monitor.timeoutSeconds,
        httpMethod: monitor.httpMethod,
        httpExpectedStatus: monitor.httpExpectedStatus,
        httpHeaderList: monitor.httpHeaderList,
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
    const nextErrors: Record<string, string> = {};
    if (!name.trim()) {
      nextErrors.name = t("serviceMonitor.validation.nameRequired");
    }
    if (!target.trim()) {
      nextErrors.target = t("serviceMonitor.validation.targetRequired");
    }
    if (type === "tcp" && !/^[^:]+:\d+$/.test(target.trim())) {
      nextErrors.target = t("serviceMonitor.validation.tcpTargetFormat");
    }
    const intervalValue = clampNumberInput(intervalSeconds, 5, 3600, DEFAULT_INTERVAL_SECONDS);
    const timeoutValue = clampNumberInput(timeoutSeconds, 1, 300, DEFAULT_TIMEOUT_SECONDS);
    const expectedStatusValue = clampNumberInput(httpExpectedStatus, 100, 599, DEFAULT_HTTP_EXPECTED_STATUS);
    if (type === "http" && !/^https?:\/\/.+/i.test(target.trim())) {
      nextErrors.target = t("serviceMonitor.validation.httpTargetFormat");
    }
    if (Object.keys(nextErrors).length > 0) {
      setFieldErrors(nextErrors);
      return;
    }
    setFieldErrors({});

    setSaving(true);
    const input: NewServiceMonitorInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      type,
      target: target.trim(),
      intervalSeconds: intervalValue,
      timeoutSeconds: timeoutValue,
      enabled,
    };

    if (type === "http") {
      input.httpMethod = httpMethod;
      input.httpExpectedStatus = expectedStatusValue;
      input.httpHeaderList = httpHeaders;
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

  const enabledCount = monitors.filter((monitor) => monitor.enabled).length;
  const downCount = monitors.filter((monitor) => monitor.lastStatus === "down").length;
  const unknownCount = monitors.filter(
    (monitor) => !monitor.lastStatus || monitor.lastStatus === "unknown",
  ).length;

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero
        title={t("serviceMonitor.pageTitle")}
        subtitle={t("serviceMonitor.pageDesc")}
        meta={
          <>
            <Badge tone="neutral">
              {t("serviceMonitor.totalMeta", { count: monitors.length })}
            </Badge>
            <Badge tone={enabledCount > 0 ? "success" : "warning"}>
              {t("serviceMonitor.enabledMeta", { count: enabledCount })}
            </Badge>
            <Badge tone={downCount > 0 ? "destructive" : "neutral"}>
              {t("serviceMonitor.downMeta", { count: downCount })}
            </Badge>
            <Badge tone={unknownCount > 0 ? "warning" : "neutral"}>
              {t("serviceMonitor.unknownMeta", { count: unknownCount })}
            </Badge>
          </>
        }
        actions={
          <Button onClick={openCreateDialog}>
            <Plus className="mr-1.5 size-4" aria-hidden="true" />
            {t("serviceMonitor.createBtn")}
          </Button>
        }
      />

      <DataSurface>
        <DataSurfaceHeader
          title={t("serviceMonitor.surfaceTitle")}
          description={t("serviceMonitor.surfaceDesc")}
        />
        <DataSurfaceContent className="p-0">
          {loading ? (
            <div className="flex items-center justify-center py-16">
              <div
                className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent"
                aria-label={t("common.loading")}
                role="status"
              />
            </div>
          ) : monitors.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
              <Activity className="mb-3 size-10 opacity-30" aria-hidden="true" />
              <p className="text-sm">{t("serviceMonitor.empty")}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={openCreateDialog}
              >
                <Plus className="mr-1.5 size-3.5" aria-hidden="true" />
                {t("serviceMonitor.createBtn")}
              </Button>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/50">
                    <th scope="col" className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colName")}
                    </th>
                    <th scope="col" className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colType")}
                    </th>
                    <th scope="col" className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("serviceMonitor.colTarget")}
                    </th>
                    <th scope="col" className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("serviceMonitor.colStatus")}
                    </th>
                    <th scope="col" className="hidden px-4 py-3 text-right font-medium text-muted-foreground sm:table-cell">
                      {t("serviceMonitor.colUptime")}
                    </th>
                    <th scope="col" className="px-4 py-3 text-center font-medium text-muted-foreground">
                      {t("serviceMonitor.colEnabled")}
                    </th>
                    <th scope="col" className="px-4 py-3 text-right font-medium text-muted-foreground">
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
                          {statusDot(monitor.lastStatus)}
                          <span className={monitor.lastStatus === "down" ? "text-destructive" : ""}>
                            {lastStatusLabel(monitor.lastStatus)}
                          </span>
                        </span>
                      </td>
                      <td className="hidden px-4 py-3 text-right tabular-nums sm:table-cell">
                        {(monitor.uptimePct ?? 0).toFixed(1)}%
                      </td>
                      <td className="px-4 py-3 text-center">
                        <Switch
                          checked={monitor.enabled}
                          onCheckedChange={() => handleToggleEnabled(monitor)}
                          aria-label={
                            monitor.enabled
                              ? t("serviceMonitor.disableAriaLabel", { name: monitor.name })
                              : t("serviceMonitor.enableAriaLabel", { name: monitor.name })
                          }
                        />
                      </td>
                      <td className="px-4 py-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("serviceMonitor.editAriaLabel", { name: monitor.name })}
                            onClick={() => openEditDialog(monitor)}
                          >
                            <Pencil className="size-3.5" aria-hidden="true" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("serviceMonitor.deleteAriaLabel", { name: monitor.name })}
                            onClick={() => handleDelete(monitor)}
                          >
                            <Trash2 className="size-3.5 text-destructive" aria-hidden="true" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </DataSurfaceContent>
      </DataSurface>

      {/* Editor Dialog */}
      <FormDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        size="md"
        icon={<Activity className="size-5 text-primary" aria-hidden="true" />}
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
            name="service-monitor-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            autoComplete="off"
            aria-invalid={Boolean(fieldErrors.name)}
            aria-describedby={fieldErrors.name ? "sm-name-error" : undefined}
          />
          {fieldErrors.name ? (
            <p id="sm-name-error" role="alert" className="mt-1 text-xs text-destructive">
              {fieldErrors.name}
            </p>
          ) : null}
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
            name="service-monitor-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            autoComplete="off"
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
            name="service-monitor-target"
            placeholder={type === "tcp" ? "example.com:443" : "https://example.com/health"}
            value={target}
            onChange={(event) => setTarget(event.target.value)}
            type={type === "http" ? "url" : "text"}
            inputMode={type === "tcp" ? "url" : "url"}
            autoComplete="off"
            aria-invalid={Boolean(fieldErrors.target)}
            aria-describedby={fieldErrors.target ? "sm-target-error" : undefined}
          />
          {fieldErrors.target ? (
            <p id="sm-target-error" role="alert" className="mt-1 text-xs text-destructive">
              {fieldErrors.target}
            </p>
          ) : null}
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
              name="service-monitor-interval"
              type="number"
              inputMode="numeric"
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
              name="service-monitor-timeout"
              type="number"
              inputMode="numeric"
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
                  onChange={(event) => setHttpMethod(event.target.value as HttpMethod)}
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
                  name="service-monitor-http-status"
                  type="number"
                  inputMode="numeric"
                  min={100}
                  max={599}
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
                    <label className="sr-only" htmlFor={`sm-header-key-${idx}`}>
                      {t("serviceMonitor.headerKeyAriaLabel", { index: idx + 1 })}
                    </label>
                    <Input
                      id={`sm-header-key-${idx}`}
                      name={`service-monitor-header-key-${idx}`}
                      className="flex-1"
                      placeholder={t("serviceMonitor.headerKeyPlaceholder")}
                      autoComplete="off"
                      value={kv.key}
                      onChange={(event) => updateHeader(idx, "key", event.target.value)}
                    />
                    <label className="sr-only" htmlFor={`sm-header-value-${idx}`}>
                      {t("serviceMonitor.headerValueAriaLabel", { index: idx + 1 })}
                    </label>
                    <Input
                      id={`sm-header-value-${idx}`}
                      name={`service-monitor-header-value-${idx}`}
                      className="flex-1"
                      placeholder={t("serviceMonitor.headerValuePlaceholder")}
                      autoComplete="off"
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
                      <Trash2 className="size-3.5 text-muted-foreground" aria-hidden="true" />
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
